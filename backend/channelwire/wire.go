// Package channelwire provides shared wire-format helpers and constants for
// the E2EE channel relay protocol used between Frontend/Desktop and Hub.
package channelwire

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const (
	// MaxPlaintextPerChunk is the maximum plaintext bytes per Noise transport
	// message (65535 max ciphertext - 16 byte AEAD auth tag).
	MaxPlaintextPerChunk = 65535 - 16

	// DefaultMaxMessageSize is the maximum reassembled message size (16 MiB).
	DefaultMaxMessageSize = 16 * 1024 * 1024

	// DefaultMaxIncompleteChunked is the maximum number of in-flight chunked
	// sequences per channel before new ones are rejected.
	DefaultMaxIncompleteChunked = 4

	// WSReadLimit is the WebSocket per-message read limit for channel relays.
	// It must exceed the max ciphertext size to accommodate the 4-byte length
	// prefix and protobuf framing of a ChannelMessage.
	WSReadLimit = 65535 + 4096

	// AuthTokenSubprotocolPrefix is the prefix for auth tokens passed via
	// the Sec-WebSocket-Protocol header (e.g. "auth.token.<token>").
	AuthTokenSubprotocolPrefix = "auth.token."
)

// HTTPToWS converts an http(s) URL to the corresponding ws(s) URL.
func HTTPToWS(rawURL string) string {
	if strings.HasPrefix(rawURL, "https://") {
		return "wss://" + rawURL[8:]
	}
	if strings.HasPrefix(rawURL, "http://") {
		return "ws://" + rawURL[7:]
	}
	return rawURL
}

// OrgEventsURL builds the per-org WebSocket URL the hub serves at
// /ws/orgevents. baseURL is an http(s) URL; it is rewritten to ws(s)
// via HTTPToWS. workspaceIDs is optional — when non-empty it scopes the
// subscription to those workspaces. Used by every client that opens the
// org-events feed (desktop relay, remote CLI, worker-side relay) so the
// query-string shape stays consistent.
func OrgEventsURL(baseURL, orgID string, workspaceIDs []string) string {
	q := url.Values{}
	q.Set("org_id", orgID)
	if len(workspaceIDs) > 0 {
		q.Set("workspace_ids", strings.Join(workspaceIDs, ","))
	}
	return HTTPToWS(baseURL) + "/ws/orgevents?" + q.Encode()
}

// WriteFramedBytes writes a length-prefixed binary frame to a
// WebSocket. Wire format: [4 bytes big-endian length][payload].
// The shared wire format used by /ws/channel (ChannelMessage) and
// /ws/orgevents (MarshaledEvent / WatchOrgEvent); routing both
// writers through one helper keeps the framing spec in one place.
func WriteFramedBytes(ctx context.Context, ws *websocket.Conn, payload []byte) error {
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)
	return ws.Write(ctx, websocket.MessageBinary, buf)
}

// ReadFramedBytes reads one length-prefixed binary frame from a
// WebSocket and returns the unwrapped payload (without the 4-byte
// length prefix). Companion to WriteFramedBytes.
func ReadFramedBytes(ctx context.Context, ws *websocket.Conn) ([]byte, error) {
	_, data, err := ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("framed: message too short (%d bytes)", len(data))
	}
	length := binary.BigEndian.Uint32(data[:4])
	if int(length) != len(data)-4 {
		return nil, fmt.Errorf("framed: length mismatch (header=%d, actual=%d)", length, len(data)-4)
	}
	return data[4:], nil
}

// WriteChannelMessage writes a length-prefixed ChannelMessage to a WebSocket.
// Wire format: [4 bytes big-endian length][protobuf-encoded ChannelMessage]
func WriteChannelMessage(ctx context.Context, ws *websocket.Conn, msg *leapmuxv1.ChannelMessage) error {
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return WriteFramedBytes(ctx, ws, data)
}

// ReadChannelMessage reads a length-prefixed ChannelMessage from a WebSocket.
func ReadChannelMessage(ctx context.Context, ws *websocket.Conn) (*leapmuxv1.ChannelMessage, error) {
	payload, err := ReadFramedBytes(ctx, ws)
	if err != nil {
		return nil, err
	}
	msg := &leapmuxv1.ChannelMessage{}
	if err := proto.Unmarshal(payload, msg); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return msg, nil
}

// OrgEventsReadLimit is the per-message read budget for /ws/orgevents
// subscribers. Matches the 16 MiB ceiling the hub uses on the writer
// side (large initial-bootstrap snapshots can hit several MB on busy
// orgs).
const OrgEventsReadLimit = 16 * 1024 * 1024

// OpenOrgEventsWS dials /ws/orgevents on `hubURL` with the supplied
// bearer + workspace scope and returns the resulting WebSocket. Used
// by both the worker's WatchOrg relay (hub_stream.go) and the CLI's
// hub-bound client (client.go) so the dial + subprotocol + read-limit
// triple lives in one place. Caller owns the returned WS and must
// Close it.
//
// `bearer` is added as "Authorization: Bearer <bearer>". `httpClient`
// may be nil; pass one when the caller's transport requires
// unix/npipe dialers or shared HTTP/2 settings.
func OpenOrgEventsWS(ctx context.Context, httpClient *http.Client, hubURL, bearer, orgID string, workspaceIDs []string) (*websocket.Conn, error) {
	header := http.Header{}
	if bearer != "" {
		header.Set("Authorization", "Bearer "+bearer)
	}
	opts := &websocket.DialOptions{
		Subprotocols: []string{"orgevents-relay"},
		HTTPHeader:   header,
	}
	if httpClient != nil {
		opts.HTTPClient = httpClient
	}
	ws, _, err := websocket.Dial(ctx, OrgEventsURL(hubURL, orgID, workspaceIDs), opts)
	if err != nil {
		return nil, fmt.Errorf("dial /ws/orgevents: %w", err)
	}
	ws.SetReadLimit(OrgEventsReadLimit)
	return ws, nil
}

// IsOrgEventsCloseError reports whether `err` from ReadFramedBytes
// represents a clean stream termination (context cancellation, EOF,
// or any WebSocket CloseError). Lets callers map those to a clean
// `(nil, io.EOF)` / `nil` return without repeating the type-assertion
// dance.
func IsOrgEventsCloseError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return true
	}
	var closeErr websocket.CloseError
	return errors.As(err, &closeErr)
}

// RunOrgEventsReadLoop reads frames from `ws` and feeds each one to
// `onFrame` until the connection closes or onFrame returns an error.
// Whether to strip the 4-byte length prefix is the caller's call:
// pass `stripPrefix=true` (typical worker path: downstream consumers
// expect raw protos) or `false` (desktop relay path: frontend's
// length-prefix parser handles the framing).
//
// Returns nil on clean close (IsOrgEventsCloseError). Other read /
// frame errors bubble up.
func RunOrgEventsReadLoop(ctx context.Context, ws *websocket.Conn, stripPrefix bool, onFrame func(payload []byte) error) error {
	for {
		var payload []byte
		var err error
		if stripPrefix {
			payload, err = ReadFramedBytes(ctx, ws)
		} else {
			_, payload, err = ws.Read(ctx)
		}
		if err != nil {
			if IsOrgEventsCloseError(err) {
				return nil
			}
			return err
		}
		if err := onFrame(payload); err != nil {
			return err
		}
	}
}
