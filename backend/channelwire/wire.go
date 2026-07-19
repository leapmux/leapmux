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
	// NoiseAEADAuthTagSize is the Noise transport AEAD auth-tag overhead appended
	// to every ciphertext. It is the other half of the chunk-size relationship
	// MaxCiphertextForChunk encodes, broken out as its own constant so a
	// transport change (e.g. a post-quantum AEAD with a different tag size) is
	// one edit instead of a 16-and-65535 pair each receiver re-derives.
	NoiseAEADAuthTagSize = 16

	// MaxCiphertextForChunk is the maximum ciphertext size for a single Noise
	// transport message. The Noise spec caps a transport message at 65535 bytes
	// of ciphertext (a 2-byte length prefix's range), which every chunk-split
	// and per-chunk cap enforces independently. Paired with NoiseAEADAuthTagSize
	// it derives MaxPlaintextPerChunk.
	MaxCiphertextForChunk = 65535

	// MaxPlaintextPerChunk is the maximum plaintext bytes per Noise transport
	// message (MaxCiphertextForChunk - NoiseAEADAuthTagSize).
	MaxPlaintextPerChunk = MaxCiphertextForChunk - NoiseAEADAuthTagSize

	// DefaultMaxMessageSize is the maximum reassembled message size (16 MiB).
	// It is a fixed protocol constant every receiver (hub relay, worker,
	// tunnel client, browser) enforces independently -- not an operator knob;
	// reintroducing one requires propagating the value to every receiver and
	// is tracked in https://github.com/leapmux/leapmux/issues/291.
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

	// PingMethod is the no-op inner RPC a client issues once, before OpenChannel
	// returns, to prove the E2EE session works end to end.
	//
	// Noise_NK's handshake only proves the CLIENT can encrypt to the worker's static
	// key; nothing in it proves the worker's session decrypts, nor that its replies
	// decrypt back. Without a round trip at open time, a session broken in either
	// direction (a key mismatch, a corrupted handshake payload, a relay that mangles
	// a frame) opens "successfully" and fails later on the first real call -- and
	// because channels are POOLED (crossworker.Client, the desktop TunnelManager),
	// that broken session gets cached and served to every subsequent caller until
	// something else evicts it. One round trip keeps the failure at the open, where
	// the caller can attribute it and re-resolve.
	//
	// It carries no payload: the proof is that the round trip decrypts at both ends,
	// not anything it says.
	//
	// It lives here, beside the other constants both ends of the channel must agree
	// on, because the client (backend/tunnel) and the worker's handler
	// (internal/worker/service) would otherwise each spell it themselves and rely on
	// a test to notice the day they disagree. The frontend necessarily keeps its own
	// copy (see PING_METHOD in frontend/src/lib/channel.ts).
	PingMethod = "Ping"
)

// NewErrorResponse is the single constructor for an error InnerRpcResponse, the
// envelope both Go receivers (the tunnel client's Channel.deliverRPCError and
// the worker session's channelSender.sendError) emit when an inner RPC fails.
// Routing both through one constructor keeps the IsError+ErrorCode+ErrorMessage
// shape in one place, alongside PingMethod and the chunk/message ceilings both
// ends of the channel must also agree on -- so a future field (a retryable bit,
// a category) lands once instead of in two packages whose tests would only
// notice the day they disagree.
func NewErrorResponse(code int32, message string) *leapmuxv1.InnerRpcResponse {
	return &leapmuxv1.InnerRpcResponse{IsError: true, ErrorCode: code, ErrorMessage: message}
}

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

// ChunkContinuation interprets a ChannelMessage's flags field for the
// reassembly path. It returns more=true only for FLAGS_MORE (a non-final
// chunk), more=false for FLAGS_UNSPECIFIED (a final chunk) and FLAGS_CLOSE
// (a standalone teardown frame, which never carries chunk data), and
// valid=false for every other value.
//
// The wire enum is a set of distinct values, not a bitmask: SendChannelFrames
// (and the browser's copy in frontend/src/lib/channel.ts) emit exactly
// UNSPECIFIED or MORE on data frames. proto3 enums are open, though, so a
// hostile or non-conformant peer can put any integer on the wire -- and a
// site that reads the field as `flags == FLAGS_MORE` silently reads a
// combined value such as MORE|CLOSE (3) as "final chunk", delivering a
// truncated assembly to the decoder. Every receiver of this wire contract
// (hub relay, ws relay, worker session, tunnel client) routes the decision
// through this one predicate and DROPS the frame when valid is false: an
// out-of-spec flags value is a protocol violation, not a chunk boundary the
// receiver may guess about.
func ChunkContinuation(flags leapmuxv1.ChannelMessageFlags) (more, valid bool) {
	switch flags {
	case leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE:
		return true, true
	case leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED,
		leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE:
		return false, true
	default:
		return false, false
	}
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

// SendChannelFrames splits plaintext into MaxPlaintextPerChunk-sized chunks,
// encrypts each under encrypt, wraps each in a ChannelMessage addressed to
// channelID/correlationID with the MORE flag set on every chunk but the last,
// and passes each frame to send in order. It is the one place the chunk-split /
// per-chunk-encrypt / ChannelMessage-build sequence lives, shared by the two Go
// senders of this wire contract -- the worker's channelSender.sendEncrypted and
// the tunnel client's Channel.sendInnerContext -- so a chunking change (the
// flag, the cap, the envelope) lands once instead of drifting between them.
// The browser keeps its own copy (frontend/src/lib/channel.ts), which cannot
// import Go.
//
// It encrypts and sends one chunk per iteration -- it does NOT buffer the whole
// message's ciphertext -- so a large send peaks at one chunk of ciphertext
// beyond the plaintext, not the full reassembled size. send is called under the
// same serialisation the caller enforces (the worker's channelSender mutex; the
// tunnel's single-slot sendPermit), so it must not re-enter either.
//
// Returns the first error from encrypt (wrapped) or send (passed through); on a
// mid-message failure earlier chunks have already been sent, so the caller
// owns the recovery (the tunnel cancels its channel; the worker returns the
// error to its single sender). Empty plaintext emits exactly one zero-byte
// frame and terminates -- both callers marshal an InnerMessage with a set oneof
// (always >= 1 byte), but handling empty here forecloses the infinite-loop
// landmine the boundary math once carried as a standalone helper.
func SendChannelFrames(
	encrypt func([]byte) ([]byte, error),
	channelID string,
	correlationID uint64,
	plaintext []byte,
	send func(*leapmuxv1.ChannelMessage) error,
) error {
	for offset := 0; ; {
		end := offset + MaxPlaintextPerChunk
		more := true
		if end >= len(plaintext) {
			end = len(plaintext)
			more = false
		}
		ciphertext, err := encrypt(plaintext[offset:end])
		if err != nil {
			return fmt.Errorf("encrypt inner message: %w", err)
		}
		flags := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
		if more {
			flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
		}
		if err := send(&leapmuxv1.ChannelMessage{
			ProtocolVersion: 1,
			ChannelId:       channelID,
			CorrelationId:   correlationID,
			Flags:           flags,
			Ciphertext:      ciphertext,
		}); err != nil {
			return err
		}
		offset = end
		if !more {
			return nil
		}
	}
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
// by the worker's WatchOrg relay, the CLI's hub-bound client, and the desktop
// sidecar so the dial + subprotocol + read-limit
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
	return OpenOrgEventsWSWithHeader(ctx, httpClient, hubURL, header, orgID, workspaceIDs)
}

// OpenOrgEventsWSWithHeader is OpenOrgEventsWS for callers whose authentication
// is already represented by HTTP headers, such as the desktop cookie jar.
func OpenOrgEventsWSWithHeader(ctx context.Context, httpClient *http.Client, hubURL string, header http.Header, orgID string, workspaceIDs []string) (*websocket.Conn, error) {
	opts := &websocket.DialOptions{
		Subprotocols: []string{"orgevents-relay"},
		HTTPHeader:   header.Clone(),
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
// represents a recoverable stream termination the caller can reconnect from:
// context cancellation, EOF, or a WebSocket close with a recoverable code
// (see isRecoverableCloseCode). Lets callers map those to a clean
// `(nil, io.EOF)` / `nil` return without repeating the type-assertion dance.
func IsOrgEventsCloseError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return true
	}
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		return false
	}
	return isRecoverableCloseCode(closeErr.Code)
}

// isRecoverableCloseCode is the single source of truth for the "should the
// consumer reconnect" decision. It classifies a WebSocket close status as
// recoverable -- a clean shutdown (NormalClosure), an endpoint going away
// (GoingAway), or a transient intermediary signal an HTTP server/load balancer
// in front of the Hub emits during a restart (ServiceRestart / TryAgainLater)
// -- versus a terminal protocol/policy failure (ProtocolError, PolicyViolation,
// InternalError, ...). Both IsOrgEventsCloseError (the CLI/worker
// collapse-to-clean path) and WebSocketCloseDetails (the desktop relay's
// wasClean flag) route through it, so a future recoverable code is a one-line
// change that applies everywhere instead of an allowlist that must be updated
// at each consumer.
func isRecoverableCloseCode(code websocket.StatusCode) bool {
	switch code {
	case websocket.StatusNormalClosure,
		websocket.StatusGoingAway,
		websocket.StatusServiceRestart,
		websocket.StatusTryAgainLater,
		// A close frame carrying NO status code, which coder/websocket surfaces as
		// StatusNoStatusRcvd. The Hub never sends it (the library rejects 1005 on the
		// send side), so it means an intermediary -- an nginx proxy_pass on the WS
		// upgrade, an ALB/ingress, a corporate proxy -- ended an idle /ws/orgevents
		// with a bare close frame. That is a routine event on a long-lived stream and
		// says nothing terminal about the Hub, so it must not surface to the CLI as a
		// hard error where the same proxy dropping TCP outright (io.EOF, handled
		// above) reconnects cleanly.
		websocket.StatusNoStatusRcvd:
		return true
	}
	return false
}

// WebSocketCloseDetails converts a WebSocket read result into the close
// metadata exposed by the desktop relay. Non-close transport failures use the
// RFC 6455 abnormal-closure code so callers never mistake them for clean EOF.
func WebSocketCloseDetails(err error) (code uint32, reason string, wasClean bool) {
	if err == nil {
		return uint32(websocket.StatusNormalClosure), "", true
	}
	var closeErr websocket.CloseError
	if errors.As(err, &closeErr) {
		return uint32(closeErr.Code), closeErr.Reason, isRecoverableCloseCode(closeErr.Code)
	}
	return uint32(websocket.StatusAbnormalClosure), err.Error(), false
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
	err := ReadOrgEventsFrames(ctx, ws, stripPrefix, onFrame)
	if IsOrgEventsCloseError(err) {
		return nil
	}
	return err
}

// ReadOrgEventsFrames has the same framing behavior as
// RunOrgEventsReadLoop, but preserves the terminal WebSocket close error. Relay
// adapters use it when they must forward the peer's exact close code and
// reason rather than collapsing a clean close to nil.
func ReadOrgEventsFrames(ctx context.Context, ws *websocket.Conn, stripPrefix bool, onFrame func(payload []byte) error) error {
	for {
		var payload []byte
		var err error
		if stripPrefix {
			payload, err = ReadFramedBytes(ctx, ws)
		} else {
			_, payload, err = ws.Read(ctx)
		}
		if err != nil {
			return err
		}
		if err := onFrame(payload); err != nil {
			return err
		}
	}
}
