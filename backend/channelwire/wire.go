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
	"strconv"
	"strings"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/generated/contracts"
)

// The wire-protocol limits both ends must agree on are owned by
// contracts/wire.json and generated into
// github.com/leapmux/leapmux/generated/contracts (and into
// frontend/src/generated/contracts for the browser); consumers import that
// package directly -- one Go spelling per contract constant. The
// derivations (contracts.MaxPlaintextPerChunk, contracts.DefaultMaxReassembledMessageSize,
// contracts.SessionKeyHardCeiling) are computed by the generator, so neither language
// re-derives them. Go-only limits (WSReadLimit, UserEventsReadLimit) stay
// hand-written here, but WSReadLimit reads the contract's chunk cap so the
// two cannot drift apart.
const (
	// WSReadLimit is the WebSocket per-message read limit for channel relays.
	// It must exceed contracts.MaxCiphertextForChunk to accommodate the
	// 4-byte length prefix and protobuf framing of a ChannelMessage; the
	// 4096 slack covers that framing.
	WSReadLimit = contracts.MaxCiphertextForChunk + 4096
)

// ResolveMaxMessageSize maps a configured max_message_size to the effective
// payload budget. Non-positive values mean "use the protocol default".
func ResolveMaxMessageSize(n int) int {
	if n <= 0 {
		return contracts.MaxMessageSize
	}
	return n
}

// MaxReassembledMessageSize returns the receive/send-gate ceiling for a
// (resolved) payload budget: the budget plus contracts.InnerEnvelopeHeadroom.
func MaxReassembledMessageSize(maxMessageSize int) int {
	return maxMessageSize + contracts.InnerEnvelopeHeadroom
}

// ValidateMaxMessageSize checks a configured (or negotiated) payload budget.
// Zero/negative are allowed at config load time (ResolveMaxMessageSize maps
// them to the default); call this only for strictly positive values, or after
// resolving. Open-path negotiation always validates the absolute resolved size.
func ValidateMaxMessageSize(n int) error {
	if n < contracts.MaxPlaintextPerChunk {
		return fmt.Errorf("max_message_size %d is below the floor of %d (contracts.MaxPlaintextPerChunk)", n, contracts.MaxPlaintextPerChunk)
	}
	if n > contracts.MaxConfigurableMessageSize {
		return fmt.Errorf("max_message_size %d exceeds the ceiling of %d (contracts.MaxConfigurableMessageSize)", n, contracts.MaxConfigurableMessageSize)
	}
	return nil
}

// ValidateConfiguredMaxMessageSize validates an operator-facing config value.
// Non-positive means "use the protocol default" and is accepted without
// bounds checks; positive values must pass ValidateMaxMessageSize.
func ValidateConfiguredMaxMessageSize(n int) error {
	if n <= 0 {
		return nil
	}
	return ValidateMaxMessageSize(n)
}

// IntFromUint64 converts a negotiated wire size to int, rejecting values that
// cannot be represented on this architecture (mirrors the tunnel client's
// open-path overflow guard).
func IntFromUint64(n uint64) (int, error) {
	maxInt := uint64(^uint(0) >> 1)
	if n > maxInt {
		return 0, fmt.Errorf("max_message_size %d overflows int", n)
	}
	return int(n), nil
}

// NegotiatePayloadBudget returns min(hub, worker) for two already-validated
// (or already-resolved) positive payload budgets. Hub↔Worker open always
// agrees this value; keep the rule in one place so session and any future
// verifier cannot drift.
func NegotiatePayloadBudget(hub, worker int) int {
	if worker < hub {
		return worker
	}
	return hub
}

// AdoptWireMaxMessageSize parses a ChannelOpen / OpenChannelResponse
// max_message_size wire field: reject zero (missing / version skew), overflow,
// and out-of-bounds values. Callers that need peer-specific ceilings (e.g. hub
// max) apply those after this returns.
func AdoptWireMaxMessageSize(wire uint64) (int, error) {
	if wire == 0 {
		return 0, fmt.Errorf("no max_message_size")
	}
	n, err := IntFromUint64(wire)
	if err != nil {
		return 0, err
	}
	if err := ValidateMaxMessageSize(n); err != nil {
		return 0, err
	}
	return n, nil
}

// NewErrorResponse is the single constructor for an error InnerRpcResponse, the
// envelope both Go receivers (the tunnel client's Channel.deliverRPCError and
// the worker session's channelSender.sendError) emit when an inner RPC fails.
// Routing both through one constructor keeps the IsError+ErrorCode+ErrorMessage
// shape in one place, alongside contracts.PingMethod and the chunk/message ceilings both
// ends of the channel must also agree on -- so a future field (a retryable bit,
// a category) lands once instead of in two packages whose tests would only
// notice the day they disagree.
func NewErrorResponse(code int32, message string) *leapmuxv1.InnerRpcResponse {
	return &leapmuxv1.InnerRpcResponse{IsError: true, ErrorCode: code, ErrorMessage: message}
}

// NewChannelMessage wraps one encrypted chunk in the ChannelMessage envelope
// both Go senders put on the wire. The protocol version is
// contracts.ProtocolVersion (contracts/wire.json), so a version bump is one
// edit that moves every sender -- this constructor, the senders that bypass
// it, and the browser's channelSession -- instead of a literal in each.
func NewChannelMessage(channelID string, correlationID uint64,
	flags leapmuxv1.ChannelMessageFlags, ciphertext []byte) *leapmuxv1.ChannelMessage {
	return &leapmuxv1.ChannelMessage{
		ProtocolVersion: contracts.ProtocolVersion,
		ChannelId:       channelID,
		CorrelationId:   correlationID,
		Flags:           flags,
		Ciphertext:      ciphertext,
	}
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

// UserEventsURL builds the per-user WebSocket URL the hub serves at
// /ws/userevents. baseURL is an http(s) URL; it is rewritten to ws(s)
// via HTTPToWS. workspaceIDs is optional — when non-empty it scopes the
// subscription to those workspaces. The authenticated session implies the
// user; no user_id query parameter is required. Used by every
// client that opens the user-events feed (desktop relay, remote CLI,
// worker-side relay) so the query-string shape stays consistent.
//
// cursor is the client's last-applied canonical HLC (nil on a first connect,
// which makes the hub send a full snapshot); epoch is the current_epoch it saw
// it under (0 when there is no cursor). The hub falls back to a full snapshot
// when the cursor is nil/zero or at/below the op-retention watermark (the
// lagging floor decideResume actually gates on -- NOT compaction_watermark,
// which always equals max_hlc and would reject every cursor).
func UserEventsURL(baseURL string, workspaceIDs []string, cursor *leapmuxv1.HLC, epoch int64) string {
	q := url.Values{}
	if len(workspaceIDs) > 0 {
		q.Set(contracts.WSParamWorkspaceIDs, strings.Join(workspaceIDs, ","))
	}
	// Gate on the DECODER's own accept rule, not the weaker all-zero test.
	// hlcIsZero only rejects {0,0,""}, but DecodeResumeHLC rejects any
	// physical <= 0 (the shared corpus pins `{"raw": "0.4.c", "ok": false}`), so
	// a cursor like {Physical: 0, Logical: 7, ClientId: "c"} used to be written
	// onto the URL and then 400'd by the hub -- an error where the intent was to
	// omit the cursor and degrade to a full snapshot. Asking the decoder keeps
	// the encode and decode sides one rule instead of two that must agree.
	if resumeCursorEncodable(cursor) {
		q.Set(contracts.WSParamResumeAfterHLC, EncodeResumeHLC(cursor))
		q.Set(contracts.WSParamResumeEpoch, strconv.FormatInt(epoch, 10))
	}
	qs := q.Encode()
	if qs == "" {
		return HTTPToWS(baseURL) + contracts.WSRouteUserEvents
	}
	return HTTPToWS(baseURL) + contracts.WSRouteUserEvents + "?" + qs
}

// EncodeResumeHLC renders an HLC as the "<physical>.<logical>.<client_id>"
// triple carried in the resume_after_hlc query param. Inverse of
// DecodeResumeHLC.
func EncodeResumeHLC(h *leapmuxv1.HLC) string {
	return strconv.FormatInt(h.GetPhysical(), 10) + "." +
		strconv.FormatInt(h.GetLogical(), 10) + "." +
		h.GetClientId()
}

// DecodeResumeHLC parses the resume_after_hlc query param back into an HLC,
// returning nil for malformed input (non-numeric physical/logical, a
// `+`-signed number, missing client_id, or a client_id longer than 128 bytes
// as a sanity limit). Inverse of EncodeResumeHLC.
//
// The client_id limit counts UTF-8 BYTES, because `len` counts bytes: 128 bytes
// holds 128 ASCII characters but only about 42 CJK ones. The TypeScript decoder
// measures the same bytes (hlc.ts parseHlcWire), and the corpus pins both edges
// of the limit in each language.
//
// nil is NOT a silent degrade: the only caller, ParseResumeCursor, turns it
// into an error that the hub answers with HTTP 400 and the desktop sidecar
// answers with a rejected relay-open RPC. A malformed cursor is a client bug,
// and failing loudly beats limping along on full snapshots forever.
//
// plusSigned is rejected even though strconv.ParseInt accepts it, because the
// TS decoder (frontend/src/lib/crdt/hlc.ts parseHlcWire) does not — and neither
// encoder ever emits it. Silently accepting on one side only would mean a
// cursor the hub honours but the client considers corrupt (or vice versa).
// testdata/hlc_wire_corpus.json pins the agreed grammar from both languages.
func DecodeResumeHLC(raw string) *leapmuxv1.HLC {
	dot1 := strings.IndexByte(raw, '.')
	if dot1 <= 0 {
		return nil
	}
	dot2 := strings.IndexByte(raw[dot1+1:], '.')
	if dot2 <= 0 {
		return nil
	}
	dot2 += dot1 + 1
	physicalRaw, logicalRaw := raw[:dot1], raw[dot1+1:dot2]
	if plusSigned(physicalRaw) || plusSigned(logicalRaw) {
		return nil
	}
	physical, err := strconv.ParseInt(physicalRaw, 10, 64)
	if err != nil || physical <= 0 {
		return nil
	}
	logical, err := strconv.ParseInt(logicalRaw, 10, 64)
	if err != nil || logical < 0 {
		return nil
	}
	clientID := raw[dot2+1:]
	if clientID == "" || len(clientID) > 128 {
		return nil
	}
	return &leapmuxv1.HLC{Physical: physical, Logical: logical, ClientId: clientID}
}

// ParseResumeCursor validates a (resume_after_hlc, resume_epoch) pair from its
// raw wire strings into a cursor HLC + epoch. The single authority for "what
// counts as a well-formed cursor" so the hub (URL query params) and the desktop
// sidecar (proto fields) cannot drift: both decode the same two strings and
// apply the same strictness (a malformed cursor is a client bug, not a legacy
// client — callers surface the error rather than silently degrading).
//
// Both empty → (nil, 0, nil): a first connect. resume_epoch without a matching
// resume_after_hlc, or either field present-but-malformed, returns an error.
//
// The epoch is domain-checked, not merely parsed. `current_epoch` is
// `NOT NULL DEFAULT 1` in every dialect and only ever increments, so zero and
// negative are not values the hub can hold — accepting them would let a client
// bug (a serializer that emits 0 for "unset") sail past this gate and fail
// silently downstream instead, where `decideResume`'s epoch equality just
// misses and degrades that client to a full snapshot on every single connect,
// forever, with nothing logged. That is precisely the "limping along on full
// snapshots" outcome this function exists to turn into a loud 400.
func ParseResumeCursor(hlcRaw, epochRaw string) (cursor *leapmuxv1.HLC, epoch int64, err error) {
	if hlcRaw == "" {
		if epochRaw != "" {
			return nil, 0, fmt.Errorf("resume_epoch requires resume_after_hlc")
		}
		return nil, 0, nil
	}
	cursor = DecodeResumeHLC(hlcRaw)
	if cursor == nil {
		return nil, 0, fmt.Errorf("malformed resume_after_hlc %q", hlcRaw)
	}
	if epochRaw == "" {
		// resume_after_hlc is present, so resume_epoch is required. A bare
		// ParseInt("") would report "malformed" for an absent value, misdirecting
		// debugging.
		return nil, 0, fmt.Errorf("resume_epoch required with resume_after_hlc")
	}
	// plusSigned for the same reason DecodeResumeHLC rejects it: ParseInt accepts
	// "+7" but neither producer emits it (both stringify a bigint), so allowing
	// it here would make this decoder laxer than the TS one it must match.
	if plusSigned(epochRaw) {
		return nil, 0, fmt.Errorf("malformed resume_epoch %q", epochRaw)
	}
	e, perr := strconv.ParseInt(epochRaw, 10, 64)
	if perr != nil {
		return nil, 0, fmt.Errorf("malformed resume_epoch %q", epochRaw)
	}
	if e <= 0 {
		return nil, 0, fmt.Errorf("out-of-range resume_epoch %q: epochs start at 1", epochRaw)
	}
	return cursor, e, nil
}

// ParseResumeCursorFromQuery extracts the resume params from a URL query and
// validates them via ParseResumeCursor.
//
// It lives HERE, next to UserEventsURL, because UserEventsURL is what WRITES
// these two keys -- so the reader and the writer of the query-param names are
// one file, and a rename cannot land on one side only. It previously sat in the
// hub's service package under the name `ParseResumeCursor`, shadowing the
// function it delegated to and putting the key literals two packages away from
// their producer.
func ParseResumeCursorFromQuery(q url.Values) (*leapmuxv1.HLC, int64, error) {
	return ParseResumeCursor(q.Get(contracts.WSParamResumeAfterHLC), q.Get(contracts.WSParamResumeEpoch))
}

// plusSigned reports whether s carries an explicit leading `+`.
// strconv.ParseInt accepts that form; the TS decoder's `^-?\d+$` does not, and
// no encoder emits it. Rejecting here keeps one grammar across both languages.
func plusSigned(s string) bool {
	return len(s) > 0 && s[0] == '+'
}

// resumeCursorEncodable reports whether `cursor` would survive the round trip
// through DecodeResumeHLC -- i.e. whether putting it on the wire produces a
// cursor the hub will accept rather than a 400.
//
// Defined SOLELY in terms of the decoder so the two can never diverge: any rule
// DecodeResumeHLC gains is automatically honoured here. That is also why there is
// no separate nil/zero pre-test -- EncodeResumeHLC is nil-safe (it reads the
// cursor through generated getters) and DecodeResumeHLC already rejects
// physical <= 0 and an empty client id, so the ordinary "first connect" nil/zero
// cursor falls out as false without a second, hand-maintained predicate to keep
// in sync.
func resumeCursorEncodable(cursor *leapmuxv1.HLC) bool {
	return DecodeResumeHLC(EncodeResumeHLC(cursor)) != nil
}

// WriteFramedBytes writes a length-prefixed binary frame to a
// WebSocket. Wire format: [4 bytes big-endian length][payload].
// The shared wire format used by /ws/channel (ChannelMessage) and
// /ws/userevents (MarshaledEvent / WatchUserEvent); routing both
// writers through one helper keeps the framing spec in one place.
func WriteFramedBytes(ctx context.Context, ws *websocket.Conn, payload []byte) error {
	buf := make([]byte, contracts.LengthPrefixBytes+len(payload))
	binary.BigEndian.PutUint32(buf[:contracts.LengthPrefixBytes], uint32(len(payload)))
	copy(buf[contracts.LengthPrefixBytes:], payload)
	return ws.Write(ctx, websocket.MessageBinary, buf)
}

// ReadFramedBytes reads one length-prefixed binary frame from a
// WebSocket and returns the unwrapped payload (without the length
// prefix). Companion to WriteFramedBytes.
func ReadFramedBytes(ctx context.Context, ws *websocket.Conn) ([]byte, error) {
	_, data, err := ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	if len(data) < contracts.LengthPrefixBytes {
		return nil, fmt.Errorf("framed: message too short (%d bytes)", len(data))
	}
	length := binary.BigEndian.Uint32(data[:contracts.LengthPrefixBytes])
	if int(length) != len(data)-contracts.LengthPrefixBytes {
		return nil, fmt.Errorf("framed: length mismatch (header=%d, actual=%d)", length, len(data)-contracts.LengthPrefixBytes)
	}
	return data[contracts.LengthPrefixBytes:], nil
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

// SendChannelFrames splits plaintext into contracts.MaxPlaintextPerChunk-sized chunks and
// hands each to sendChunk in order, with the MORE flag set on every chunk but
// the last.
//
// sendChunk owns the per-chunk encrypt + ChannelMessage build + write as ONE
// step. That pairing is not a convenience: noiseutil.CipherState.Encrypt uses an
// implicit counter nonce (backend/internal/noise/noise.go) and the peer decrypts
// in strict arrival order, so ciphertext order must equal wire order. Owning
// both inside the callback is what lets a sender serialize exactly one chunk at
// a time (see SendGate) instead of a whole message.
//
// Returns sendChunk's first error unchanged; on a mid-message failure earlier
// chunks are already on the wire, so the caller owns the recovery (the tunnel
// cancels its channel; the worker returns the error to its single sender).
// Empty plaintext emits exactly one zero-byte frame and terminates -- both
// callers marshal an InnerMessage with a set oneof (always >= 1 byte), but
// handling empty here forecloses the infinite-loop landmine the boundary math
// once carried as a standalone helper.
func SendChannelFrames(plaintext []byte, sendChunk func(chunk []byte, flags leapmuxv1.ChannelMessageFlags) error) error {
	for offset := 0; ; {
		end := offset + contracts.MaxPlaintextPerChunk
		more := true
		if end >= len(plaintext) {
			end, more = len(plaintext), false
		}
		flags := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
		if more {
			flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
		}
		if err := sendChunk(plaintext[offset:end], flags); err != nil {
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

// UserEventsReadLimit is the per-message read budget for /ws/userevents
// subscribers (large initial-bootstrap snapshots can hit several MB on
// busy accounts).
//
// Independent of contracts.DefaultMaxReassembledMessageSize: user events are
// plaintext CRDT frames on their own socket, not chunked encrypted channel
// messages, so the two limits answer different questions and are free to
// diverge.
const UserEventsReadLimit = 16 * 1024 * 1024

// The WebSocket close-reason TOKENS the Hub sends alongside a
// policy-violation status live in contracts/wire.json (contracts.CloseReason*
// here, CLOSE_REASON_* in the browser), each with the user-facing advice it
// branches on documented beside its value in the JSON. They are stable
// tokens rather than prose because a client has to branch on them, and short
// on purpose: RFC 6455 caps a close reason at 123 bytes and coder/websocket
// enforces that on the send side, so a descriptive sentence would fail to
// send at all.

// OpenUserEventsWS dials /ws/userevents on `hubURL` with the supplied
// bearer + workspace scope and returns the resulting WebSocket. Used
// by the worker's WatchUser relay, the CLI's hub-bound client, and the desktop
// sidecar so the dial + subprotocol + read-limit
// triple lives in one place. Caller owns the returned WS and must
// Close it.
//
// `bearer` is added as "Authorization: Bearer <bearer>". `httpClient`
// may be nil; pass one when the caller's transport requires
// unix/npipe dialers or shared HTTP/2 settings.
func OpenUserEventsWS(ctx context.Context, httpClient *http.Client, hubURL, bearer string, workspaceIDs []string, cursor *leapmuxv1.HLC, epoch int64) (*websocket.Conn, error) {
	header := http.Header{}
	if bearer != "" {
		header.Set("Authorization", "Bearer "+bearer)
	}
	return OpenUserEventsWSWithHeader(ctx, httpClient, hubURL, header, workspaceIDs, cursor, epoch)
}

// OpenUserEventsWSWithHeader is OpenUserEventsWS for callers whose authentication
// is already represented by HTTP headers, such as the desktop cookie jar.
func OpenUserEventsWSWithHeader(ctx context.Context, httpClient *http.Client, hubURL string, header http.Header, workspaceIDs []string, cursor *leapmuxv1.HLC, epoch int64) (*websocket.Conn, error) {
	opts := &websocket.DialOptions{
		Subprotocols: []string{contracts.WSSubprotocolUserEventsRelay},
		HTTPHeader:   header.Clone(),
	}
	if httpClient != nil {
		opts.HTTPClient = httpClient
	}
	ws, _, err := websocket.Dial(ctx, UserEventsURL(hubURL, workspaceIDs, cursor, epoch), opts)
	if err != nil {
		return nil, fmt.Errorf("dial /ws/userevents: %w", err)
	}
	ws.SetReadLimit(UserEventsReadLimit)
	return ws, nil
}

// IsUserEventsCloseError reports whether `err` from ReadFramedBytes
// represents a recoverable stream termination the caller can reconnect from:
// context cancellation, EOF, or a WebSocket close with a recoverable code
// (see isRecoverableCloseCode). Lets callers map those to a clean
// `(nil, io.EOF)` / `nil` return without repeating the type-assertion dance.
func IsUserEventsCloseError(err error) bool {
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
// InternalError, ...). Both IsUserEventsCloseError (the CLI/worker
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
		// upgrade, an ALB/ingress, a corporate proxy -- ended an idle /ws/userevents
		// with a bare close frame. That is a routine event on a long-lived stream and
		// says nothing terminal about the Hub, so it must not surface to the CLI as a
		// hard error where the same proxy dropping TCP outright (io.EOF, handled
		// above) reconnects cleanly.
		websocket.StatusNoStatusRcvd:
		return true
	default:
		return false
	}
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

// RunUserEventsReadLoop reads frames from `ws` and feeds each one to
// `onFrame` until the connection closes or onFrame returns an error.
// Whether to strip the 4-byte length prefix is the caller's call:
// pass `stripPrefix=true` (typical worker path: downstream consumers
// expect raw protos) or `false` (desktop relay path: frontend's
// length-prefix parser handles the framing).
//
// Returns nil on clean close (IsUserEventsCloseError). Other read /
// frame errors bubble up.
func RunUserEventsReadLoop(ctx context.Context, ws *websocket.Conn, stripPrefix bool, onFrame func(payload []byte) error) error {
	err := ReadUserEventsFrames(ctx, ws, stripPrefix, onFrame)
	if IsUserEventsCloseError(err) {
		return nil
	}
	return err
}

// ReadUserEventsFrames has the same framing behavior as
// RunUserEventsReadLoop, but preserves the terminal WebSocket close error. Relay
// adapters use it when they must forward the peer's exact close code and
// reason rather than collapsing a clean close to nil.
//
// It NEVER returns nil. The loop ends only at a read error or an onFrame
// error, so a caller that guards its error handling with `if err != nil`
// writes a condition that is always true. Use RunUserEventsReadLoop when a
// clean close should read as success.
func ReadUserEventsFrames(ctx context.Context, ws *websocket.Conn, stripPrefix bool, onFrame func(payload []byte) error) error {
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
