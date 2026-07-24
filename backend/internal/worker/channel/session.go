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

// SendFunc sends a ConnectRequest (containing ChannelMessage) to the Hub.
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
	// accessibleWorkspaceIDs is mutated AFTER channel open (CreateWorkspace
	// adds entries on demand) while WatchEvents and other handlers
	// concurrently read the same set on every event broadcast. Guard the
	// map under awsMu — Manager.mu only protects the sessions registry,
	// not the inner per-session map.
	awsMu                  sync.RWMutex
	accessibleWorkspaceIDs map[string]bool // workspaces the user can access (set from ChannelOpenRequest)
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
	maxMessageSize       int                         // maximum reassembled message size
	maxIncompleteChunked int                         // maximum in-flight chunked sequences per channel
}

// NewManager creates a new channel Manager.
// Pass 0 for maxIncompleteChunked to use the default.
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
	maxIncompleteChunked int,
) *Manager {
	// The reassembled-message ceiling is a fixed protocol constant shared with
	// the tunnel client and the browser (channelwire.DefaultMaxMessageSize), not
	// an operator knob: a worker configured with any other value would reject a
	// message the other receivers accept, or accept one they reject. Only the
	// in-flight-chunk cap remains tunable. Reintroducing the knob with
	// end-to-end propagation is tracked in
	// https://github.com/leapmux/leapmux/issues/291.
	if maxIncompleteChunked <= 0 {
		maxIncompleteChunked = channelwire.DefaultMaxIncompleteChunked
	}
	return &Manager{
		sessions:             make(map[string]*channelSession),
		compositeKey:         compositeKey,
		encryptionMode:       encryptionMode,
		sendFn:               sendFn,
		trySendFn:            trySendFn,
		maxMessageSize:       channelwire.DefaultMaxMessageSize,
		maxIncompleteChunked: maxIncompleteChunked,
	}
}

// SetDispatcher sets the inner RPC dispatcher for handling decrypted requests.
func (m *Manager) SetDispatcher(d *Dispatcher) {
	m.dispatcher = d
}

// SetOnChannelClose registers the callback fired when a channel closes,
// which is what retires that channel's event subscriptions. Call it
// after the service exists and before the Manager is reachable from the
// connect loop (SetChannelMgr); like SetDispatcher, it is a bootstrap
// write, not a runtime one.
func (m *Manager) SetOnChannelClose(cb CloseCallback) {
	m.closeCallback = cb
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
	return &leapmuxv1.ChannelOpenResponse{
		ChannelId: channelID,
		Error:     "channel id already active",
	}
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
	// A session's UserID is the only identity the workspace-scoped families (agent,
	// terminal, tab moves, cleanup) ever see, and they gate on the Hub-supplied
	// accessible-workspace set rather than on the id -- so an empty one is not
	// self-limiting there the way it is for the machine-scoped families, which
	// requireWorkerOwner separately fails closed on. It would simply run as nobody,
	// and every audit line for the session would record nobody.
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
		return &leapmuxv1.ChannelOpenResponse{
			ChannelId: req.GetChannelId(),
			Error:     "no authenticated user id",
		}
	}

	var handshakeResp []byte
	var session *noiseutil.Session
	var err error

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
		return &leapmuxv1.ChannelOpenResponse{
			ChannelId: req.GetChannelId(),
			Error:     fmt.Sprintf("handshake failed: %v", err),
		}
	}

	// Build accessible workspace ID set from the Hub-provided list.
	awsIDs := make(map[string]bool, len(req.GetAccessibleWorkspaceIds()))
	for _, wsID := range req.GetAccessibleWorkspaceIds() {
		awsIDs[wsID] = true
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
			maxMessageSize: m.maxMessageSize,
			lifetime:       ctx,
			errorSends:     make(chan errorSend, errorSendQueueSize),
		},
		ctx:                    ctx,
		cancel:                 cancel,
		reassembly:             newReassembler(m.maxMessageSize, m.maxIncompleteChunked),
		accessibleWorkspaceIDs: awsIDs,
	}
	m.sessions[req.GetChannelId()] = sess
	m.mu.Unlock()
	// One drainer per session; it exits when HandleClose/CloseAll cancel
	// sess.ctx (see channelSender.errorSends).
	go sess.sender.drainErrorSends()

	slog.Info("channel opened",
		"channel_id", req.GetChannelId(),
		"user_id", req.GetUserId(),
		"encryption_mode", m.encryptionMode,
	)

	return &leapmuxv1.ChannelOpenResponse{
		ChannelId:        req.GetChannelId(),
		HandshakePayload: handshakeResp,
	}
}

// getSession looks up a channel's session under the manager's read lock and
// releases the lock before returning, so callers hold only the per-session
// locks (awsMu, the reassembler's implicit single-goroutine access) while they
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

// AccessibleWorkspaceIDs returns the set of workspace IDs accessible to the
// user on the given channel. Returns nil if the channel is not found.
//
// Returns a defensive copy: AddAccessibleWorkspaceID mutates the underlying
// map and we can't hand a live map reference to callers under nothing but
// an RLock — once they release it, a concurrent Add would race their next
// iteration. Callers iterate the result without further synchronisation.
//
// Prefer IsWorkspaceAccessible for a single membership check: it does one map
// lookup under the RLock with no copy, where this method allocates and copies
// the whole set, which the per-RPC access gates used to do on every request.
func (m *Manager) AccessibleWorkspaceIDs(channelID string) map[string]bool {
	sess, ok := m.getSession(channelID)
	if !ok {
		return nil
	}
	sess.awsMu.RLock()
	defer sess.awsMu.RUnlock()
	out := make(map[string]bool, len(sess.accessibleWorkspaceIDs))
	for id, v := range sess.accessibleWorkspaceIDs {
		out[id] = v
	}
	return out
}

// IsWorkspaceAccessible reports whether workspaceID is in the channel's
// accessible set, with a single map lookup and no copy. It is the per-RPC
// membership check the access gates (requireAccessibleWorkspace and friends)
// call on virtually every workspace-scoped request, so it must stay O(1).
func (m *Manager) IsWorkspaceAccessible(channelID, workspaceID string) bool {
	sess, ok := m.getSession(channelID)
	if !ok {
		return false
	}
	sess.awsMu.RLock()
	defer sess.awsMu.RUnlock()
	return sess.accessibleWorkspaceIDs[workspaceID]
}

// AddAccessibleWorkspaceID adds a workspace ID to the channel's accessible
// set. This is needed when a workspace is created after the channel was
// opened, so that subsequent WatchEvents calls can see the new workspace.
func (m *Manager) AddAccessibleWorkspaceID(channelID, workspaceID string) {
	sess, ok := m.getSession(channelID)
	if !ok {
		return
	}
	sess.awsMu.Lock()
	sess.accessibleWorkspaceIDs[workspaceID] = true
	sess.awsMu.Unlock()
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
		// TrySend enqueues ahead of HandleClose on the connection's FIFO
		// writer, which is what flush-before-teardown actually needs; a drop
		// is possible only when the link is already past its byte budget, in
		// which case the connection is about to reset anyway.
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

	default:
		slog.Warn("unexpected inner message kind",
			"channel_id", msg.GetChannelId(),
			"kind", fmt.Sprintf("%T", envelope.GetKind()),
		)
	}
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
			"max", m.maxMessageSize,
		)
		sess.sender.queueError(requestID, int32(codes.ResourceExhausted),
			fmt.Sprintf("chunked message too large: %d bytes exceeds %d byte limit", outcome.size, m.maxMessageSize))
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

	// Cancel the session ctx after dropping the lock so handlers
	// blocked on subprocess wait can unwind without re-entering the
	// manager's lock from their cleanup paths.
	if ok && sess.cancel != nil {
		sess.cancel()
	}

	if m.closeCallback != nil {
		m.closeCallback(channelID)
	}

	slog.Info("channel closed", "channel_id", channelID)
}

// CloseAll removes all channel sessions and invokes the close callback
// for each one, allowing associated resources (e.g. watchers) to be
// cleaned up.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	channels := make([]string, 0, len(m.sessions))
	cancels := make([]context.CancelFunc, 0, len(m.sessions))
	for id, sess := range m.sessions {
		channels = append(channels, id)
		if sess.cancel != nil {
			cancels = append(cancels, sess.cancel)
		}
	}
	m.sessions = make(map[string]*channelSession)
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
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
	gate           channelwire.SendGate // zero value usable
	channelID      string
	session        *noiseutil.Session
	sendFn         SendFunc
	maxMessageSize int
	// lifetime is the session's context. It bounds a sender parked on the gate
	// (or on the connection writer's byte budget) so a torn-down session never
	// strands one. nil in focused unit tests, which the gate accepts.
	lifetime context.Context
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
	for {
		select {
		case <-s.lifetime.Done():
			return
		case es := <-s.errorSends:
			_ = s.sendError(es.requestID, es.code, es.message)
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

	if len(data) > s.maxMessageSize {
		return fmt.Errorf("message too large: %d > %d: %w",
			len(data), s.maxMessageSize, ErrMessageRejected)
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

// logSendFailure logs a failed send with the channel/correlation/method
// attributes every boundSender send shares, and returns err unchanged so each
// method can `return b.logSendFailure(...)` on its error path. Named once here
// rather than triplicated so the attribute set cannot drift between the three
// send methods.
func (b *boundSender) logSendFailure(what string, err error) error {
	slog.Warn("failed to send "+what,
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
