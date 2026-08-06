// Package channel manages encrypted E2EE channels on the Worker side.
// It handles Noise_NK handshakes, session encryption/decryption,
// and inner RPC message dispatch.
package channel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/userid"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// ErrMessageRejected marks a send that the channel refused on the
// message's own terms -- it was unmarshalable or over the size cap --
// rather than because the transport failed. The channel stays healthy
// and later sends on it can still succeed.
//
// It exists so fan-out callers can tell the two apart. Retiring a
// subscriber on one of these would silently deafen a live client: no
// transport error follows, so nothing trips the frontend's reconnect and
// the subscription is never re-established.
var ErrMessageRejected = errors.New("channel rejected the message")

// ErrTransportGone is ErrMessageRejected's opposite, and SendFunc's contract for
// the case where there is no connection left to send on: the Hub stream is down,
// so no message will succeed until it comes back.
//
// It lives here rather than in the hub client because SendFunc is the interface
// the client implements, and because the classification is only useful to the
// layers above it -- see sendFailureLevel, which stops a disconnect from
// producing one warning per stream that was open when it happened.
var ErrTransportGone = errors.New("channel transport is not connected")

// SendFunc sends a ConnectRequest (containing ChannelMessage) to the Hub.
// It returns ErrTransportGone when no Connect stream is active.
type SendFunc func(msg *leapmuxv1.ConnectRequest) error

// TrySendFunc enqueues a ConnectRequest if the connection writer's budget
// allows, without blocking. Used from the shared Connect receive goroutine.
type TrySendFunc func(msg *leapmuxv1.ConnectRequest) bool

// errorSendQueueSize bounds a sender's error-send queue. Sized for a burst of
// protocol-violation responses; a peer that overflows it is already
// misbehaving, and a dropped error response costs it nothing its own refusal
// to drain was not already costing (see queueError).
const errorSendQueueSize = 16

// errorSend is one queued error response from the receive loop (see
// channelSender.errorSends).
type errorSend struct {
	requestID uint64
	code      int32
	message   string
}

// channelSession tracks an active encrypted channel.
type channelSession struct {
	ChannelID string
	UserID    userid.UserID
	Session   *noiseutil.Session
	sender    *channelSender // shared sender for this channel (protects Encrypt+Send)
	// ctx is the session-scoped context handed to every inner-RPC
	// handler dispatched on this channel. cancel fires on HandleClose
	// (and CloseAll) so handlers that pass ctx to subprocesses /
	// `exec.CommandContext` see the cancellation as soon as the
	// channel goes away — no waiting for a 30s read timeout to bite.
	ctx        context.Context
	cancel     context.CancelFunc
	reassembly *reassembler // per-channel chunk-reassembly state machine (see chunker.go)
	// lastRekeyAt is the handshake time until the first successful in-band
	// rekey, then the time of the latest accepted rekey. Captured via
	// time.Now(); Go embeds a monotonic reading so MinRekeyInterval checks
	// via Before/Sub are immune to NTP wall-clock steps within the process.
	lastRekeyAt time.Time
	// compositeKey is the worker's long-lived X25519 + ML-KEM + SLH-DSA
	// keypair, retained on the session so an in-band rekey can decapsulate
	// the initiator's fresh ML-KEM ciphertext. Only the X25519 private and
	// ML-KEM decapsulation halves are touched (the SLH-DSA signing key is not
	// needed — rekey frames ride inside the AEAD-encrypted channel).
	compositeKey *noiseutil.CompositeKeypair
	// encryptionMode records whether this channel used the post-quantum hybrid
	// or classical handshake, so the rekey knows whether to expect an ML-KEM
	// ciphertext. Matches m.encryptionMode at open time.
	encryptionMode leapmuxv1.EncryptionMode
	// streams maps live server-stream correlation ids to their controllers so
	// client->worker InnerStreamRequest frames can revise or cancel a
	// subscription without tearing the channel down.
	streams streamRegistry
}

// CloseCallback is called when a channel is closed, allowing cleanup
// of associated resources (e.g. removing watchers).
type CloseCallback func(channelID string)

// Manager manages encrypted channel sessions on the Worker side.
type Manager struct {
	mu                   sync.RWMutex
	sessions             map[string]*channelSession  // channelID -> session
	compositeKey         *noiseutil.CompositeKeypair // Worker's composite keypair (X25519 + ML-KEM + SLH-DSA)
	encryptionMode       leapmuxv1.EncryptionMode    // Encryption mode
	sendFn               SendFunc                    // Function to send messages to Hub
	trySendFn            TrySendFunc                 // Non-blocking send for the receive goroutine
	dispatcher           *Dispatcher                 // Inner RPC dispatcher
	closeCallback        CloseCallback               // Called when a channel is closed
	maxMessageSize       int                         // configured application payload budget (0 resolved to MaxMessageSize)
	maxIncompleteChunked int                         // maximum in-flight chunked sequences per channel
	// reassembledOverride, when > 0, forces the per-session reassembled
	// ceiling (test harness only). Production always derives
	// MaxReassembledMessageSize(effectivePayload).
	reassembledOverride int
	// onNegotiatedMaxMessageSize, when set, is called after a successful
	// open with the channel id and effective payload budget (min(hub,
	// worker)). Bootstrap wires agent.ObserveNegotiatedMaxMessageSize so
	// agent stdout scanners adopt the Hub↔Worker negotiated ceiling.
	onNegotiatedMaxMessageSize func(channelID string, effectiveMax int)
	// onReleasedMaxMessageSize, when set, is called from HandleClose so
	// producers can drop that channel's open ref and recompute.
	onReleasedMaxMessageSize func(channelID string)
	// onReleasedAllMaxMessageSizes, when set, is called from CloseAll to
	// clear every open-channel ref in one shot.
	onReleasedAllMaxMessageSizes func()
}

// NewManager creates a new channel Manager.
// Pass 0 for maxMessageSize / maxIncompleteChunked to use the defaults.
//
// The close callback is wired separately via SetOnChannelClose rather
// than taken here, because its only real implementation reaches into the
// worker service -- which cannot be constructed until this Manager
// exists. Taking it as a constructor argument forced both entry points
// to declare a nil *service.Service, capture it in the callback, and
// assign it afterwards: safe only for as long as nothing closed a
// channel during bootstrap, and a nil dereference the day something did.
func NewManager(
	compositeKey *noiseutil.CompositeKeypair,
	encryptionMode leapmuxv1.EncryptionMode,
	sendFn SendFunc,
	trySendFn TrySendFunc,
	maxMessageSize int,
	maxIncompleteChunked int,
) *Manager {
	maxMessageSize = channelwire.ResolveMaxMessageSize(maxMessageSize)
	if maxIncompleteChunked <= 0 {
		maxIncompleteChunked = channelwire.DefaultMaxIncompleteChunked
	}
	return &Manager{
		sessions:             make(map[string]*channelSession),
		compositeKey:         compositeKey,
		encryptionMode:       encryptionMode,
		sendFn:               sendFn,
		trySendFn:            trySendFn,
		maxMessageSize:       maxMessageSize,
		maxIncompleteChunked: maxIncompleteChunked,
	}
}

// SetDispatcher sets the inner RPC dispatcher for handling decrypted requests.
func (m *Manager) SetDispatcher(d *Dispatcher) {
	m.dispatcher = d
}

// SetOnChannelClose registers an optional callback fired when a channel
// closes. WatchEvents subscriptions are NOT retired here — each stream's
// StreamController.OnCancel (via streams.releaseAll) owns that teardown.
// Call it after the service exists and before the Manager is reachable from
// the connect loop (SetChannelMgr); like SetDispatcher, it is a bootstrap
// write, not a runtime one.
func (m *Manager) SetOnChannelClose(cb CloseCallback) {
	m.closeCallback = cb
}

// SetOnNegotiatedMaxMessageSize registers a callback invoked with the
// channel id and effective application payload budget after each
// successful HandleOpen. Bootstrap wires producer clamps (agent stdout) here.
func (m *Manager) SetOnNegotiatedMaxMessageSize(cb func(channelID string, effectiveMax int)) {
	m.onNegotiatedMaxMessageSize = cb
}

// SetOnReleasedMaxMessageSize registers a callback invoked when a channel
// session is removed via HandleClose so producers can release that
// channel's negotiated budget.
func (m *Manager) SetOnReleasedMaxMessageSize(cb func(channelID string)) {
	m.onReleasedMaxMessageSize = cb
}

// SetOnReleasedAllMaxMessageSizes registers a callback invoked from
// CloseAll to clear every open-channel budget in one shot.
func (m *Manager) SetOnReleasedAllMaxMessageSizes(cb func()) {
	m.onReleasedAllMaxMessageSizes = cb
}

// Dispatcher returns the inner RPC dispatcher.
func (m *Manager) Dispatcher() *Dispatcher {
	return m.dispatcher
}

// rejectChannelReopen logs and builds the response for a channel id that is
// already active. HandleOpen rejects a re-open twice -- once on the RLock fast
// path and once under m.mu.Lock as the authority -- so centralizing the log
// line and the error string here keeps the two rejections from drifting to
// describe the same condition differently.
func rejectChannelReopen(channelID string) *leapmuxv1.ChannelOpenResponse {
	slog.Warn("rejecting channel re-open: channel id already active",
		"channel_id", channelID,
	)
	return channelwire.NewChannelOpenError(
		channelID,
		"channel id already active",
		leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_CHANNEL_ALREADY_ACTIVE,
	)
}

// rejectInvalidHubMaxMessageSize logs and builds the structured open reject
// for an unusable hub max_message_size (overflow or out of bounds).
func (m *Manager) rejectInvalidHubMaxMessageSize(channelID string, hubMax any, err error) *leapmuxv1.ChannelOpenResponse {
	slog.Warn("rejecting channel open: hub max_message_size invalid",
		"channel_id", channelID,
		"hub_max_message_size", hubMax,
		"error", err,
	)
	return channelwire.NewChannelOpenError(
		channelID,
		fmt.Sprintf("invalid hub max_message_size: %v", err),
		leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_INVALID_MAX_MESSAGE_SIZE,
	)
}

// HandleOpen processes a ChannelOpenRequest from the Hub.
// It performs the Noise_NK responder handshake and returns the response.
func (m *Manager) HandleOpen(req *leapmuxv1.ChannelOpenRequest) *leapmuxv1.ChannelOpenResponse {
	// Fast-reject duplicate channel ids BEFORE running the (potentially
	// expensive post-quantum) responder handshake. Without this, a peer
	// that repeats the same ChannelOpenRequest amplifies worker CPU
	// consumption — each retry burns a full ML-KEM handshake only to
	// fail on the cheap duplicate check inside m.mu below. A TOCTOU
	// race against a sibling HandleOpen for the same channel id is
	// possible but harmless: the second checked-and-failed insertion
	// is rejected under m.mu.Lock below, just with one wasted
	// handshake instead of N.
	m.mu.RLock()
	_, dup := m.sessions[req.GetChannelId()]
	m.mu.RUnlock()
	if dup {
		return rejectChannelReopen(req.GetChannelId())
	}

	// The Hub establishes channel identity and names it here, so an empty user id is
	// not an anonymous caller -- it is the Hub failing to say who this is, which it
	// has no legitimate path to do. Refuse rather than install a session with one.
	//
	// A session's UserID is the ONLY thing scoping every handler on this channel:
	// requireWorkerOwner matches it against the Worker's registrant, and the
	// owner-scoped stores (worker_file_tabs, worktree_tabs) bind it as half their
	// key. An empty one would simply run as nobody -- denied by requireWorkerOwner
	// but reaching no row either way, with every audit line recording nobody.
	//
	// This is the fourth boundary of this handshake, and the same rule the other
	// three already apply: requireWorkerOwner refuses an empty identity on either
	// side rather than matching two empty strings, verifyDelegationWorkerScope
	// refuses an unrecorded minter, and both channel clients reject a Hub response
	// that omits the identity. Fail closed here too.
	uid, ok := userid.New(req.GetUserId())
	if !ok {
		slog.Warn("rejecting channel open: hub named no authenticated user",
			"channel_id", req.GetChannelId(),
		)
		return channelwire.NewChannelOpenError(
			req.GetChannelId(),
			"no authenticated user id",
			leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_NO_AUTHENTICATED_USER,
		)
	}

	hubMax, err := channelwire.IntFromUint64(req.GetMaxMessageSize())
	if err != nil {
		return m.rejectInvalidHubMaxMessageSize(req.GetChannelId(), req.GetMaxMessageSize(), err)
	}
	if err := channelwire.ValidateMaxMessageSize(hubMax); err != nil {
		return m.rejectInvalidHubMaxMessageSize(req.GetChannelId(), hubMax, err)
	}
	effectiveMax := channelwire.NegotiatePayloadBudget(hubMax, m.maxMessageSize)
	maxPayload := effectiveMax
	reassembledMax := channelwire.MaxReassembledMessageSize(effectiveMax)
	if m.reassembledOverride > 0 {
		reassembledMax = m.reassembledOverride
		// Test harness may force a tiny reassembled gate below the
		// validated payload floor. Keep MaxPayloadBudget from advertising
		// a larger producer clamp than the send/reassembly ceiling.
		if maxPayload > reassembledMax {
			maxPayload = reassembledMax
		}
	}

	var handshakeResp []byte
	var session *noiseutil.Session

	switch m.encryptionMode {
	case leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC:
		// Classical Noise_NK (X25519 only, no PQ).
		handshakeResp, session, err = noiseutil.ClassicalResponderHandshake(
			m.compositeKey.X25519Public,
			m.compositeKey.X25519Private,
			req.GetHandshakePayload(),
		)
	default:
		// Post-quantum hybrid Noise_NK (X25519 + ML-KEM + SLH-DSA).
		handshakeResp, session, err = noiseutil.ResponderHandshake(
			m.compositeKey,
			req.GetHandshakePayload(),
		)
	}

	if err != nil {
		slog.Error("channel handshake failed",
			"channel_id", req.GetChannelId(),
			"error", err,
		)
		return channelwire.NewChannelOpenError(
			req.GetChannelId(),
			fmt.Sprintf("handshake failed: %v", err),
			leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_HANDSHAKE_FAILED,
		)
	}

	ctx, cancel := context.WithCancel(context.Background())

	m.mu.Lock()
	// Reject re-open of an already-active channel id rather than
	// trying to swap the session in place. A swap-then-cancel sequence
	// has two unsafe windows:
	//
	//  1. OLD's noise session may still encrypt+send a final response
	//     after NEW is installed but before OLD's cancel propagates.
	//     The wire bytes ride the same channel id but the frontend has
	//     rekeyed, so they decrypt to garbage and may force a tear-down.
	//  2. closeCallback(channelID) unregisters subscriptions keyed by
	//     channelID — those subscriptions may already belong to NEW
	//     (e.g. a fresh tab-event watcher registered on the new session),
	//     and dropping them leaves NEW silently event-less.
	//
	// Returning an error here keeps OLD intact and tells the hub its
	// re-open attempt was rejected. The hub must close OLD first (which
	// will fire closeCallback against the right session) before opening
	// a new channel with the same id.
	if _, exists := m.sessions[req.GetChannelId()]; exists {
		m.mu.Unlock()
		cancel()
		return rejectChannelReopen(req.GetChannelId())
	}
	sess := &channelSession{
		ChannelID: req.GetChannelId(),
		UserID:    uid,
		Session:   session,
		sender: &channelSender{
			channelID:      req.GetChannelId(),
			session:        session,
			sendFn:         m.sendFn,
			maxPayload:     maxPayload,
			maxReassembled: reassembledMax,
			lifetime:       ctx,
			errorSends:     make(chan errorSend, errorSendQueueSize),
		},
		ctx:            ctx,
		cancel:         cancel,
		reassembly:     newReassembler(reassembledMax, m.maxIncompleteChunked),
		lastRekeyAt:    time.Now(),
		compositeKey:   m.compositeKey,
		encryptionMode: m.encryptionMode,
	}
	sess.sender.streams = &sess.streams
	m.sessions[req.GetChannelId()] = sess
	// Observe while still holding m.mu so HandleClose cannot Release
	// (no-op) between insert and Observe and leave an orphan budget that
	// permanently ratchets the process-wide scanner ceiling down.
	if m.onNegotiatedMaxMessageSize != nil {
		m.onNegotiatedMaxMessageSize(req.GetChannelId(), effectiveMax)
	}
	m.mu.Unlock()
	// One drainer per session; it exits when HandleClose/CloseAll cancel
	// sess.ctx (see channelSender.errorSends).
	go sess.sender.drainErrorSends()

	slog.Info("channel opened",
		"channel_id", req.GetChannelId(),
		"user_id", req.GetUserId(),
		"encryption_mode", m.encryptionMode,
		"max_message_size", effectiveMax,
	)

	return &leapmuxv1.ChannelOpenResponse{
		ChannelId:        req.GetChannelId(),
		HandshakePayload: handshakeResp,
		MaxMessageSize:   uint64(effectiveMax),
	}
}

// getSession looks up a channel's session under the manager's read lock and
// releases the lock before returning, so callers hold only the per-session
// state (the reassembler's implicit single-goroutine access) while they
// act on it. It is the one place the "RLock, look up m.sessions, RUnlock, bail
// on miss" contract lives, so a new read-side Manager method cannot get the
// locking subtly wrong. HandleClose is the deliberate exception: it takes the
// write lock and deletes under it, so it does not route through here.
func (m *Manager) getSession(channelID string) (*channelSession, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.sessions[channelID]
	return sess, ok
}

// HandleMessage processes an encrypted ChannelMessage from the Hub.
// It decrypts the message, dispatches the inner RPC, and sends encrypted responses.
func (m *Manager) HandleMessage(msg *leapmuxv1.ChannelMessage) {
	sess, ok := m.getSession(msg.GetChannelId())
	if !ok {
		slog.Warn("received message for unknown channel", "channel_id", msg.GetChannelId())
		return
	}

	// Decrypt. This must remain sequential in the receive loop because
	// the receive cipher state tracks a nonce counter.
	decrypted, err := sess.Session.Decrypt(msg.GetCiphertext())
	if err != nil {
		channelID := msg.GetChannelId()
		slog.Error("failed to decrypt channel message, closing channel",
			"channel_id", channelID,
			"ciphertext_len", len(msg.GetCiphertext()),
			"error", err,
		)
		// Nonce desync is unrecoverable — notify the frontend and tear down.
		// trySendFn (wired to Client.TrySendOrReset) enqueues ahead of
		// HandleClose on the connection's FIFO writer; a drop under budget
		// cancels the connection so reconnect recovers rather than leaving
		// the frontend on a dead channel until its own timeout.
		if m.trySendFn != nil {
			_ = m.trySendFn(&leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{
					ChannelMessageResp: channelwire.NewChannelMessage(
						channelID, 0,
						leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE,
						nil,
					),
				},
			})
		}
		m.HandleClose(channelID)
		return
	}

	requestID := msg.GetCorrelationId()
	// The flags read runs AFTER the decrypt so a dropped frame still advances
	// the receive nonce; an out-of-spec value (e.g. MORE|CLOSE combined) is a
	// protocol violation dropped here rather than misread as a chunk boundary
	// (see channelwire.ChunkContinuation).
	more, validFlags := channelwire.ChunkContinuation(msg.GetFlags())
	if !validFlags {
		slog.Debug("dropping channel message with out-of-spec flags",
			"channel_id", msg.GetChannelId(),
			"correlation_id", requestID,
			"flags", msg.GetFlags(),
		)
		return
	}

	// The reassembler owns the chunk state machine -- buffering, the in-flight
	// cap, oversize poisoning, and tombstone reaping (see its doc in
	// chunker.go). Acting on its decision -- logging with channel context and
	// erroring the peer through the session's error-send queue -- lives in
	// reactToReassembly, so HandleMessage reads as decrypt -> reassemble ->
	// dispatch.
	outcome := sess.reassembly.accept(requestID, decrypted, more)
	plaintext, deliver := m.reactToReassembly(sess, msg.GetChannelId(), requestID, len(decrypted), outcome)
	if !deliver {
		return
	}
	if outcome.chunked {
		slog.Debug("reassembled chunked message",
			"channel_id", msg.GetChannelId(),
			"correlation_id", requestID,
			"total_size", len(plaintext),
		)
	}

	slog.Debug("received channel message",
		"channel_id", msg.GetChannelId(),
		"correlation_id", requestID,
	)

	// Use the per-session sender so all messages on this channel share
	// a single mutex protecting Encrypt+Send (prevents nonce reuse).
	// Wrap it with boundSender to bind the request ID for responses.
	bs := &boundSender{sender: sess.sender, requestID: requestID}

	// Parse InnerMessage envelope.
	var envelope leapmuxv1.InnerMessage
	if err := proto.Unmarshal(plaintext, &envelope); err != nil {
		slog.Error("failed to unmarshal inner message",
			"channel_id", msg.GetChannelId(),
			"error", err,
		)
		return
	}

	switch kind := envelope.GetKind().(type) {
	case *leapmuxv1.InnerMessage_Request:
		bs.method = kind.Request.GetMethod()
		slog.Debug("received inner RPC request",
			"channel_id", msg.GetChannelId(),
			"correlation_id", requestID,
			"method", bs.method,
		)
		if m.dispatcher != nil {
			// Dispatch on a fresh goroutine so the receive loop isn't
			// blocked by slow handlers (e.g. WatchEvents with git ops).
			// sess.ctx is cancelled on channel close, so handlers
			// that pass it to subprocesses get free cleanup.
			//
			// DispatchAsync (not `go DispatchWith`) is what guarantees
			// Shutdown.Wait can't slip past a tracked mutation's
			// Add(1): the dispatcher increments its bound cleanup
			// WaitGroup BEFORE launching the goroutine for tracked
			// methods.
			m.dispatcher.DispatchAsync(sess.ctx, sess.UserID, kind.Request, bs)
		} else {
			// No dispatcher: route the error through the sender's error-send
			// queue so this receive-goroutine send can neither block the loop
			// under backpressure nor spawn a goroutine per frame (see
			// channelSender.errorSends).
			sess.sender.queueError(requestID, int32(codes.Unimplemented), "no dispatcher configured")
		}

	case *leapmuxv1.InnerMessage_RekeyRequest:
		sess.handleRekeyRequest(requestID, kind.RekeyRequest)

	case *leapmuxv1.InnerMessage_StreamRequest:
		sess.streams.deliver(requestID, kind.StreamRequest)

	default:
		slog.Warn("unexpected inner message kind",
			"channel_id", msg.GetChannelId(),
			"kind", fmt.Sprintf("%T", envelope.GetKind()),
		)
	}
}

// handleRekeyRequest applies the worker (responder) half of in-band Noise
// rekey. Rate-limited requests get RekeyReject without rotating keys (Request
// decrypt and Reject encrypt still advance nonces); accepted ones mix the fresh
// X25519 + ML-KEM entropy from the request, rotate receive (retaining the
// previous key for the grace window) before Ack, and rotate send after the Ack
// is on the wire.
//
// The fresh DH + ML-KEM exchange is what closes the #321 forward-secrecy gap:
// the next epoch's key cannot be derived from the current one because it mixes
// new ephemeral key-agreement entropy. The rekey frames ride inside the
// AEAD-encrypted channel, so the dh_pub / mlkem_ct fields need no signature.
//
// Soft-nonce inspection of Send and the send rotation both run under the
// SendGate frame permit so they cannot race concurrent Encrypt from
// DispatchAsync handlers. Rekey still runs on the receive goroutine
// (RekeyReceiveWithSecret must happen before the next Decrypt); rekey is rare
// (~hourly) so a brief gate wait is acceptable versus the error-send queue's
// flood path.
func (sess *channelSession) handleRekeyRequest(requestID uint64, req *leapmuxv1.RekeyRequest) {
	now := time.Now()
	soft := sess.Session.Receive.NeedsRekey() || sess.sender.peekSendNeedsRekey()
	if !channelwire.AllowRekey(now, sess.lastRekeyAt, soft) {
		retryAfter := channelwire.RejectRetryAfter(now, sess.lastRekeyAt)
		if err := sess.sender.sendEncrypted(requestID, &leapmuxv1.InnerMessage{
			Kind: &leapmuxv1.InnerMessage_RekeyReject{RekeyReject: &leapmuxv1.RekeyReject{
				RetryAfterMs: retryAfter.Milliseconds(),
			}},
		}); err != nil {
			slog.Warn("failed to send rekey reject",
				"channel_id", sess.ChannelID,
				"correlation_id", requestID,
				"error", err,
			)
		}
		return
	}

	// Validate the fresh ephemeral the initiator sent before touching keys.
	initiatorPub := req.GetDhPub()
	if len(initiatorPub) != noiseutil.EphemeralPublicKeySize {
		// A missing/wrong-size ephemeral is a protocol violation by a peer
		// that did not run the fresh-DH rekey. Fail closed rather than fall
		// back to the old HKDF derivation: the channel cannot safely continue
		// under a derivation the attacker could predict.
		slog.Error("rekey request missing valid ephemeral key; closing channel",
			"channel_id", sess.ChannelID,
			"correlation_id", requestID,
			"dh_pub_len", len(initiatorPub),
		)
		sess.cancel()
		return
	}

	// Decapsulate the fresh ML-KEM ciphertext (post-quantum channels only).
	// Classic channels send an empty mlkem_ct and skip PQ entropy.
	var pqSecret []byte
	if sess.encryptionMode != leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC {
		mlkemCT := req.GetMlkemCt()
		if len(mlkemCT) != noiseutil.MlkemCiphertextSize {
			slog.Error("rekey request has wrong-size ML-KEM ciphertext; closing channel",
				"channel_id", sess.ChannelID,
				"correlation_id", requestID,
				"mlkem_ct_len", len(mlkemCT),
			)
			sess.cancel()
			return
		}
		var decapErr error
		pqSecret, decapErr = sess.compositeKey.MlkemDecapsulationKey.Decapsulate(mlkemCT)
		if decapErr != nil {
			slog.Error("rekey ML-KEM decapsulation failed; closing channel",
				"channel_id", sess.ChannelID,
				"correlation_id", requestID,
				"error", decapErr,
			)
			sess.cancel()
			return
		}
	}

	// Generate the responder's fresh ephemeral and complete the DH agreement.
	ephemeralSeed, err := noiseutil.GenerateEphemeralX25519Seed()
	if err != nil {
		slog.Error("rekey ephemeral generation failed; closing channel",
			"channel_id", sess.ChannelID,
			"correlation_id", requestID,
			"error", err,
		)
		noiseutil.Zero(pqSecret)
		sess.cancel()
		return
	}
	responderPub, pubErr := noiseutil.X25519PublicFromSeed(ephemeralSeed)
	if pubErr != nil {
		slog.Error("rekey ephemeral pubkey derivation failed; closing channel",
			"channel_id", sess.ChannelID,
			"correlation_id", requestID,
			"error", pubErr,
		)
		noiseutil.Zero(pqSecret)
		noiseutil.Zero(ephemeralSeed)
		sess.cancel()
		return
	}

	dhSecret, pq, dhErr := noiseutil.DeriveRekeySecrets(noiseutil.RekeyMaterial{
		LocalEphemeralPriv: ephemeralSeed,
		PeerEphemeralPub:   initiatorPub,
		PQSharedSecret:     pqSecret,
	})
	// DeriveRekeySecrets has consumed the ephemeral seed into the DH; wipe it
	// now so the fresh secret does not linger in memory past the agreement.
	// This is a real wipe: the seed is a caller-owned []byte, not an opaque
	// *ecdh.PrivateKey whose internal copy crypto/ecdh gives no way to zero.
	noiseutil.Zero(ephemeralSeed)
	if dhErr != nil {
		slog.Error("rekey DH derivation failed; closing channel",
			"channel_id", sess.ChannelID,
			"correlation_id", requestID,
			"error", dhErr,
		)
		noiseutil.Zero(pqSecret)
		sess.cancel()
		return
	}

	// Rotate receive first, retaining the previous key for the grace window so
	// any initiator data frame still in flight under the old key decrypts.
	// Give the send rotation its own copies: rekeyWithSecret zeroes its inputs,
	// and both directions must mix the SAME secrets.
	dhForSend := append([]byte(nil), dhSecret...)
	pqForSend := append([]byte(nil), pq...)
	sess.Session.RekeyReceiveWithSecret(dhSecret, pq)
	if err := sess.sender.sendEncryptedAfter(requestID, &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_RekeyAck{RekeyAck: &leapmuxv1.RekeyAck{
			DhPub: responderPub,
		}},
	}, func() {
		// Still holding the frame permit: no concurrent Encrypt can observe
		// a half-rotated Send CipherState. The send direction does not keep
		// a previous key (only the receiver needs the grace window).
		sess.Session.RekeySendWithSecret(dhForSend, pqForSend)
	}); err != nil {
		// Receive is already rotated; without an Ack the initiator will not
		// rotate Send and the session is desynced. Fail closed.
		slog.Error("failed to send rekey ack after receive rekey; closing channel",
			"channel_id", sess.ChannelID,
			"correlation_id", requestID,
			"error", err,
		)
		noiseutil.Zero(dhForSend)
		noiseutil.Zero(pqForSend)
		sess.cancel()
		return
	}
	sess.lastRekeyAt = time.Now()
}

// reactToReassembly acts on one reassembly outcome: it logs each arm with
// channel context, routes protocol-violation errors through the sender's
// error-send queue (never inline on the receive goroutine -- see
// channelSender.errorSends), and reports whether the frame completed a
// message to dispatch. chunkLen is the decrypted frame's size, logged on the
// buffered arm. The DECISION (buffer / drop / poison / deliver) belongs to
// reassembler.accept; only the reaction lives here.
func (m *Manager) reactToReassembly(
	sess *channelSession,
	channelID string,
	requestID uint64,
	chunkLen int,
	outcome reassemblyOutcome,
) (plaintext []byte, deliver bool) {
	switch outcome.action {
	case reassemblyDropPoisoned:
		slog.Debug("dropping chunk for a poisoned correlation id",
			"channel_id", channelID,
			"correlation_id", requestID,
		)
		return nil, false
	case reassemblyDropCapped:
		// Both the live and tombstone budgets are saturated, so the chunk was
		// dropped without an error or a tombstone. Logging at Debug (not Warn)
		// because a peer that has exhausted both budgets is misbehaving, and the
		// cap is working as designed -- there is nothing for the caller to do.
		slog.Debug("dropping chunk: reassembly budgets saturated",
			"channel_id", channelID,
			"correlation_id", requestID,
		)
		return nil, false
	case reassemblyTooManyIncomplete:
		slog.Warn("too many incomplete chunked messages",
			"channel_id", channelID,
			"correlation_id", requestID,
			"count", outcome.incomplete,
		)
		sess.sender.queueError(requestID, int32(codes.ResourceExhausted), "too many incomplete chunked messages")
		return nil, false
	case reassemblyTooLarge:
		slog.Warn("chunked message exceeds max size",
			"channel_id", channelID,
			"correlation_id", requestID,
			"size", outcome.size,
			"max", sess.sender.maxReassembled,
		)
		sess.sender.queueError(requestID, int32(codes.ResourceExhausted),
			fmt.Sprintf("chunked message too large: %d bytes exceeds %d byte limit", outcome.size, sess.sender.maxReassembled))
		return nil, false
	case reassemblyBuffered:
		slog.Debug("buffered chunk",
			"channel_id", channelID,
			"correlation_id", requestID,
			"chunk_size", chunkLen,
			"total", outcome.size,
		)
		return nil, false
	case reassemblyDeliver:
		return outcome.plaintext, true
	}
	return nil, false
}

// HandleClose removes a channel session and invokes the close callback.
func (m *Manager) HandleClose(channelID string) {
	m.mu.Lock()
	sess, ok := m.sessions[channelID]
	delete(m.sessions, channelID)
	m.mu.Unlock()
	// Do NOT nil sess.reassembly here: a concurrent receive-loop call
	// to HandleMessage may have already snapshotted *sess (its lookup at
	// the top takes only an RLock, then drops it) and is about to mutate
	// the reassembler's buffers outside m.mu. Clearing it from
	// HandleClose under m.mu.Lock races that write and panics with
	// `assignment to entry in nil map`. The reassembler will be
	// collected once the in-flight handler returns and sess is
	// unreferenced; the delete above ensures no future receive iteration
	// finds the session.

	if ok {
		// Release stream controllers BEFORE cancelling sess.ctx so a
		// controller that wants to run a synchronous teardown (e.g. the
		// watch session's UnwatchAll) still has a live session to do it
		// against.
		sess.streams.releaseAll()
	}

	// Cancel the session ctx after dropping the lock so handlers
	// blocked on subprocess wait can unwind without re-entering the
	// manager's lock from their cleanup paths.
	if ok && sess.cancel != nil {
		sess.cancel()
	}

	if ok && m.onReleasedMaxMessageSize != nil {
		m.onReleasedMaxMessageSize(channelID)
	}

	if m.closeCallback != nil {
		m.closeCallback(channelID)
	}

	slog.Info("channel closed", "channel_id", channelID)
}

// CloseAll removes all channel sessions. Stream controllers are released
// (OnCancel) before each session ctx is cancelled, matching HandleClose.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	channels := make([]string, 0, len(m.sessions))
	cancels := make([]context.CancelFunc, 0, len(m.sessions))
	sessions := make([]*channelSession, 0, len(m.sessions))
	for id, sess := range m.sessions {
		channels = append(channels, id)
		sessions = append(sessions, sess)
		if sess.cancel != nil {
			cancels = append(cancels, sess.cancel)
		}
	}
	m.sessions = make(map[string]*channelSession)
	m.mu.Unlock()

	// Release stream controllers before cancelling session ctxs (same
	// ordering as HandleClose).
	for _, sess := range sessions {
		sess.streams.releaseAll()
	}

	for _, cancel := range cancels {
		cancel()
	}

	if m.onReleasedAllMaxMessageSizes != nil {
		m.onReleasedAllMaxMessageSizes()
	} else if m.onReleasedMaxMessageSize != nil {
		// Fallback when only the per-channel hook is wired (unit tests).
		// Production bootstrap always sets onReleasedAllMaxMessageSizes so
		// agent refcount clears under one lock instead of N times.
		for _, id := range channels {
			m.onReleasedMaxMessageSize(id)
		}
	}

	if m.closeCallback != nil {
		for _, id := range channels {
			m.closeCallback(id)
		}
	}
}

// channelSender sends encrypted responses back through a channel.
//
// It owns the session's whole outbound side: the encryption SendGate (so
// ciphertext order equals wire order and a single-chunk frame can overtake a
// multi-chunk message), the Connect send, and the receive-goroutine error
// queue that drains through the same gate.
type channelSender struct {
	gate      channelwire.SendGate // zero value usable
	channelID string
	session   *noiseutil.Session
	sendFn    SendFunc
	// maxPayload is the negotiated application payload budget (min(hub, worker)).
	// maxReassembled is the send/reassembly ceiling (payload + envelope headroom).
	maxPayload     int
	maxReassembled int
	// lifetime is the session's context. It bounds a sender parked on the gate
	// (or on the connection writer's byte budget) so a torn-down session never
	// strands one. nil in focused unit tests, which the gate accepts.
	lifetime context.Context
	// streams is the session's client-frame registry. Bound after the session
	// is constructed so boundSender.BindStream can register without holding
	// the session itself.
	streams *streamRegistry
	// errorSends carries error responses issued from the worker's SHARED
	// Connect receive goroutine to this session's drainer. It stays even though
	// the connection writer removed network blocking, because the receive
	// goroutine faces a second blocking point: the encryption gate above,
	// which a handler parked on the writer's byte budget still holds. A
	// try-once gate acquire would drop error responses on ordinary momentary
	// contention; this 16-deep queue only drops when the link is genuinely
	// backed up. Enqueue is non-blocking; overflow drops the response.
	errorSends chan errorSend
}

// queueError hands an error response to the sender's drainer without
// blocking the receive loop. Drop-on-full is deliberate: the queue only
// overflows when the send path is already backpressured or the peer is
// streaming violations, and in both cases the error response is best-effort
// -- the caller's own RPC timeout is the backstop, exactly as if the frame
// had been lost in flight.
func (s *channelSender) queueError(requestID uint64, code int32, message string) {
	select {
	case s.errorSends <- errorSend{requestID: requestID, code: code, message: message}:
	default:
		slog.Debug("dropping error response: error-send queue is full",
			"channel_id", s.channelID,
			"correlation_id", requestID,
		)
	}
}

// drainErrorSends is the session's sole consumer of errorSends. It exits when
// the session lifetime is cancelled (HandleClose / CloseAll); anything still
// queued then is dropped with the session it belonged to.
func (s *channelSender) drainErrorSends() {
	var life <-chan struct{}
	if s.lifetime != nil {
		life = s.lifetime.Done()
	}
	for {
		select {
		case <-life:
			return
		case es := <-s.errorSends:
			if err := s.sendError(es.requestID, es.code, es.message); err != nil {
				// The one send path with no boundSender behind it, so nothing
				// else would report this. Classified like the others rather
				// than dropped: a marshal or encrypt failure here is a real
				// fault, and silence made it indistinguishable from the
				// disconnect that legitimately eats a queued error response.
				slog.Log(context.Background(), sendFailureLevel(err), "failed to send queued inner RPC error",
					"channel_id", s.channelID, "correlation_id", es.requestID, "error", err)
			}
		}
	}
}

// sendEncrypted marshals an InnerMessage envelope, encrypts it, and sends it as
// one or more channel frames. The chunk-split lives in channelwire.SendGate /
// SendChannelFrames, shared with the tunnel client's send path, so this sender
// stays responsible only for what is specific to the worker side: marshalling,
// the size cap, and wrapping each frame in the ConnectRequest the Hub relay
// expects (the tunnel writes raw ChannelMessages instead).
func (s *channelSender) sendEncrypted(requestID uint64, envelope *leapmuxv1.InnerMessage) error {
	return s.sendEncryptedAfter(requestID, envelope, nil)
}

// peekSendNeedsRekey reads Send.NeedsRekey under the frame permit so it cannot
// race a concurrent Encrypt incrementing the nonce.
func (s *channelSender) peekSendNeedsRekey() bool {
	var needs bool
	if err := s.gate.WithFrame(context.Background(), s.lifetime, func() error {
		needs = s.session.Send.NeedsRekey()
		return nil
	}); err != nil {
		// Lifetime ended or aborted — treat as not soft; age gate still applies.
		return false
	}
	return needs
}

// sendEncryptedAfter is sendEncrypted plus an optional hook that runs after the
// final chunk is on the wire, while the SendGate frame permit is still held.
func (s *channelSender) sendEncryptedAfter(requestID uint64, envelope *leapmuxv1.InnerMessage, afterFinal func()) error {
	data, err := proto.Marshal(envelope)
	if err != nil {
		// Deliberately NOT ErrMessageRejected. That sentinel means "this
		// message was refused, the channel is fine and the next one may
		// well succeed" -- true of the size cap below, which is a property
		// of the payload. A marshal failure is a defect in an envelope the
		// worker built itself, so it is neither client-attributable nor
		// likely to stop recurring; classifying it as a per-message
		// rejection would keep the subscriber, log a warning, and drop
		// every affected event forever with no transport error to trip a
		// reconnect.
		return fmt.Errorf("marshal inner message: %w", err)
	}

	if len(data) > s.maxReassembled {
		return fmt.Errorf("message too large: %d > %d: %w",
			len(data), s.maxReassembled, ErrMessageRejected)
	}

	return s.gate.Send(context.Background(), s.lifetime, data,
		func(chunk []byte, flags leapmuxv1.ChannelMessageFlags) error {
			ciphertext, err := s.session.Encrypt(chunk)
			if err != nil {
				return fmt.Errorf("encrypt inner message: %w", err)
			}
			slog.Debug("sending channel message",
				"channel_id", s.channelID,
				"correlation_id", requestID,
				"ciphertext_len", len(ciphertext),
				"flags", flags)
			if err := s.sendFn(&leapmuxv1.ConnectRequest{
				Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{
					ChannelMessageResp: channelwire.NewChannelMessage(
						s.channelID, requestID, flags, ciphertext),
				},
			}); err != nil {
				return fmt.Errorf("send channel message: %w", err)
			}
			if afterFinal != nil && flags != leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE {
				afterFinal()
			}
			return nil
		})
}

// sendResponse sends an InnerRpcResponse (encrypted) back to the frontend.
func (s *channelSender) sendResponse(requestID uint64, resp *leapmuxv1.InnerRpcResponse) error {
	return s.sendEncrypted(requestID, &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Response{Response: resp},
	})
}

// sendError sends an error InnerRpcResponse.
func (s *channelSender) sendError(requestID uint64, code int32, message string) error {
	return s.sendResponse(requestID, channelwire.NewErrorResponse(code, message))
}

// ChannelID returns the E2EE channel ID for this sender.
func (s *channelSender) ChannelID() string {
	return s.channelID
}

// MaxPayloadBudget returns the negotiated application payload budget for
// this channel (min(hub, worker) at open).
func (s *channelSender) MaxPayloadBudget() int {
	return s.maxPayload
}

// sendStream sends an InnerStreamMessage (encrypted) back to the frontend.
func (s *channelSender) sendStream(requestID uint64, msg *leapmuxv1.InnerStreamMessage) error {
	return s.sendEncrypted(requestID, &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Stream{Stream: msg},
	})
}

// boundSender wraps a channelSender with a fixed requestID and method name.
// This is needed because the channelSender is shared per channel but each
// incoming message has its own ID, and dispatch runs in goroutines concurrently.
type boundSender struct {
	sender    *channelSender
	requestID uint64
	method    string
}

// sendFailureLevel picks the level a failed send is reported at.
//
// A send that failed because this session or its connection was being torn down
// is the EXPECTED outcome of a disconnect, not a fault: every handler holding an
// open stream when the Hub link drops tries one last frame on the way out (the
// WatchEvents terminator is the reliable one), and reporting each of those as a
// warning turns one ordinary reconnect into a burst of alarms proportional to
// how many streams happened to be open.
//
// The two teardown errors are the same event a moment apart. ErrTransportGone
// means the writer was already gone when the frame reached it;
// channelwire.ErrSendAborted means the session lifetime was cancelled while the
// frame was at the gate. Anything else -- a marshal failure, an oversized
// frame, a transport error on a LIVE connection -- is still a warning, because
// nothing was tearing down and the frame was lost anyway.
func sendFailureLevel(err error) slog.Level {
	if IsTransportTeardown(err) {
		return slog.LevelDebug
	}
	return slog.LevelWarn
}

// IsTransportTeardown reports whether err means "this send failed because the
// connection was going away", as opposed to a genuine fault.
//
// Exported because the send methods here are NOT the only reporters of these
// errors: watcherRegistry.broadcast, the tunnel relay loops and the hub client's
// heartbeat all log their own line for the same failure. A private helper
// consulted by one of them left the others warning once per watcher, per tunnel
// and per heartbeat on an ordinary disconnect -- the exact per-stream noise this
// classification exists to remove, just under different message text.
//
// Deliberately NOT the same question as service.transportDead, whose job is
// "should this subscriber be retired?" and which answers true for a transport
// error on a LIVE connection too. That case is a real fault and must stay a
// warning.
func IsTransportTeardown(err error) bool {
	return errors.Is(err, ErrTransportGone) || errors.Is(err, channelwire.ErrSendAborted)
}

// logSendFailure logs a failed send with the channel/correlation/method
// attributes every boundSender send shares, and returns err unchanged so each
// method can `return b.logSendFailure(...)` on its error path. Named once here
// rather than triplicated so the attribute set cannot drift between the three
// send methods.
func (b *boundSender) logSendFailure(what string, err error) error {
	slog.Log(context.Background(), sendFailureLevel(err), "failed to send "+what,
		"channel_id", b.sender.channelID,
		"correlation_id", b.requestID,
		"method", b.method,
		"error", err,
	)
	return err
}

func (b *boundSender) SendResponse(resp *leapmuxv1.InnerRpcResponse) error {
	slog.Debug("sending inner RPC response",
		"channel_id", b.sender.channelID,
		"correlation_id", b.requestID,
		"method", b.method,
		"is_error", resp.GetIsError(),
		"error_code", resp.GetErrorCode(),
		"error_message", resp.GetErrorMessage(),
		"payload_len", len(resp.GetPayload()),
	)
	if err := b.sender.sendResponse(b.requestID, resp); err != nil {
		return b.logSendFailure("inner RPC response", err)
	}
	return nil
}

func (b *boundSender) SendError(code int32, message string) error {
	slog.Debug("sending inner RPC error",
		"channel_id", b.sender.channelID,
		"correlation_id", b.requestID,
		"method", b.method,
		"code", code,
		"message", message,
	)
	if err := b.sender.sendError(b.requestID, code, message); err != nil {
		return b.logSendFailure("inner RPC error", err)
	}
	return nil
}

// QueueError hands an error to the session's error-send drainer without
// blocking. Implements errorQueuer for DispatchAsync's unknown-method arm.
func (b *boundSender) QueueError(code int32, message string) {
	b.sender.queueError(b.requestID, code, message)
}

func (b *boundSender) SendStream(msg *leapmuxv1.InnerStreamMessage) error {
	slog.Debug("sending inner stream message",
		"channel_id", b.sender.channelID,
		"correlation_id", b.requestID,
		"method", b.method,
		"end", msg.GetEnd(),
		"is_error", msg.GetIsError(),
		"error_code", msg.GetErrorCode(),
		"error_message", msg.GetErrorMessage(),
		"payload_len", len(msg.GetPayload()),
	)
	if err := b.sender.sendStream(b.requestID, msg); err != nil {
		return b.logSendFailure("inner stream message", err)
	}
	return nil
}

func (b *boundSender) ChannelID() string {
	return b.sender.ChannelID()
}

// MaxPayloadBudget returns the negotiated application payload budget.
func (b *boundSender) MaxPayloadBudget() int {
	return b.sender.MaxPayloadBudget()
}

// BindStream installs ctrl as the receiver of client frames on this writer's
// correlation id. ok is false when the sender has no stream registry (tests
// stubs, or a miswired sender) — callers must not start a long-lived session
// that expects revise/cancel frames.
func (b *boundSender) BindStream(ctrl StreamController) (release func(), ok bool) {
	if b.sender.streams == nil {
		return nil, false
	}
	return b.sender.bindStream(b.requestID, ctrl), true
}

func (s *channelSender) bindStream(requestID uint64, ctrl StreamController) func() {
	return s.streams.bind(requestID, ctrl)
}
