// Package tunnel provides a public E2EE channel client for the desktop app
// to communicate with Workers via the Hub relay.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
)

// sessionVerifyTimeout bounds the open-time Ping round trip (see OpenChannel). It
// is the open's budget, not a normal RPC's: the claim exchange it replaced used the
// same 10s, and callers with an unbounded context rely on it to fail rather than
// hang on a worker whose session is wedged.
const sessionVerifyTimeout = 10 * time.Second

// Channel manages a single E2EE channel from the desktop app to a Worker
// via the Hub's WebSocket relay.
//
// Deliberately absent: the age- and nonce-based rotation the frontend does in
// channel.ts's getOrOpenChannel. This asymmetry is a decision, not an oversight.
//
//   - Max age is already enforced from the outside, and more tightly than a
//     client-side timer could manage: the Hub arms every channel to expire with
//     the credential that opened it (service.ChannelService.OpenChannel ->
//     ScheduleExpiry), and both auth.AccessTokenTTL (the `leapmux remote` CLI)
//     and auth.DelegationTokenTTL (crossworker) are one hour. Re-implementing a
//     ceiling here would duplicate a hub-enforced invariant with a weaker copy
//     that drifts.
//   - Rotating anyway would be destructive where it is not redundant. The
//     desktop pool keys one Channel per {hub, worker, revision}, so a single
//     channel multiplexes EVERY live tunnel.Conn to that worker. Close-and-
//     re-handshake -- which is all the frontend's "rekey" is -- would RST every
//     port-forward and SOCKS5 conn riding it. The frontend can afford that
//     because its only long-lived streams are cursor-resumable subscriptions
//     with a reconnect loop; a relayed TCP conn has no resume semantics.
//   - The nonce ceiling is unreachable in practice and fail-closed if reached:
//     tunnelflow.MaxChunkBytes is 32 KiB, so noise.HardNonceLimit (2^32)
//     is ~128 TiB through ONE uninterrupted channel, and the Encrypt error path
//     below cancels the channel so pooled callers re-open (see the comment
//     there). noise.Session.NeedsRekey exists to mirror the TS implementation
//     and has no caller here on purpose -- if you are reaching for it to add
//     rotation, re-read this list first. Bounding the desktop channel's key
//     lifetime would need an in-band rekey that preserves open conns, which is
//     a both-ends protocol change, not a close-and-reopen.
type Channel struct {
	channelID string
	// userID is the identity the Hub authenticated this channel's open as. It is
	// the one fact the Hub goes out of its way to return, and it is immutable for
	// the channel's life -- exposed via UserID so a caller that pools channels can
	// key on what the Hub actually said rather than on what it asked for.
	userID  string
	session *noiseutil.Session
	ws      *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc

	// sendPermit is a capacity-1 semaphore held for the entire per-message
	// encrypt+chunk+write loop (see sendInnerContext), so every conn multiplexed
	// onto this Channel serializes behind whichever send is in flight -- a large
	// RPC payload head-of-line-blocks a small tunnel frame. Contention only (see
	// read_credit.go and Conn.sendRemoteClose for the workarounds this already
	// forced); the fix is a single writer goroutine + bounded queue, tracked at
	// https://github.com/leapmux/leapmux/issues/276.
	sendPermit chan struct{}
	mu         sync.Mutex

	nextReqID  uint64 // last allocated correlation id; guarded by mu (see allocateReqIDLocked)
	pending    map[uint64]chan<- *leapmuxv1.InnerRpcResponse
	streamCbs  map[uint64]*streamCallback
	reassembly map[uint64]*channelwire.ChunkBuffer
	closed     atomic.Bool
}

// The per-message chunk buffer (accumulate, cap, poison) is the shared
// channelwire.ChunkBuffer. The worker session (internal/worker/channel.reassembler)
// wraps the SAME struct in a DELIBERATELY different state machine -- peer-initiated
// requests, no handler registry, tombstones counted toward the cap, terminal-chunk
// reaping, single-goroutine access. Read that type before changing the reassembly
// here: the struct is shared, but the machines around it encode each receiver's own
// trust model.

// liveReassemblyLocked counts the reassembly entries still accumulating bytes --
// reassembly minus its tombstones -- which, not len(reassembly), is what the
// DefaultMaxIncompleteChunked cap is applied to: a tombstone holds no bytes (poison
// releases its parts) and exists only so an errored id's remaining chunks are
// recognised and dropped, so letting one burn a cap slot would let four protocol
// violations permanently reject every subsequent chunked message on an otherwise
// healthy channel -- the exact denial of service the cap exists to prevent. The
// count is the shared channelwire.CountByState's live half (the tunnel client
// takes only live because its tombstones are reaped by handler unregistration,
// unlike the worker session which bounds tombstones itself). Derived rather than
// kept as a running total so it can never drift from the map it describes. Caller
// holds ch.mu. The browser client derives the same count
// (frontend/src/lib/channel.ts's Reassembler.liveCount).
func (ch *Channel) liveReassemblyLocked() int {
	live, _ := channelwire.CountByState(ch.reassembly)
	return live
}

// startReassemblyLocked registers a fresh accumulation buffer for correlationID.
// Caller holds ch.mu.
func (ch *Channel) startReassemblyLocked(correlationID uint64) *channelwire.ChunkBuffer {
	buf := &channelwire.ChunkBuffer{}
	ch.reassembly[correlationID] = buf
	return buf
}

// poisonReassemblyLocked turns correlationID's entry into a tombstone: it
// releases whatever bytes were accumulated but KEEPS the map entry so the id's
// remaining chunks are recognised and silently dropped instead of re-accumulating
// or re-erroring the request once per chunk. It creates the entry when the id has
// none, which is the over-cap case -- that violation is detected before any buffer
// exists, yet still needs a tombstone, or chunks 2..N of the rejected message each
// storm the channel's sole receive goroutine. Idempotent. Caller holds ch.mu.
func (ch *Channel) poisonReassemblyLocked(correlationID uint64) {
	buf, ok := ch.reassembly[correlationID]
	if !ok {
		buf = &channelwire.ChunkBuffer{}
		ch.reassembly[correlationID] = buf
	}
	buf.Poison()
}

// dropReassemblyLocked removes correlationID's entry, live or tombstone. Caller
// holds ch.mu.
func (ch *Channel) dropReassemblyLocked(correlationID uint64) {
	delete(ch.reassembly, correlationID)
}

const streamCallbackQueueSize = 64

// RPCHandlers are installed atomically before an inner RPC is sent. Response
// must be buffered so delivery rarely blocks the channel's sole receive loop;
// when a saturated stream consumer does stall it, recvLoop applies backpressure
// (it stops reading) rather than tearing down the channel. Stream callbacks run
// on a per-request dispatcher so a slow consumer does not hold the channel lock
// or run on the receive goroutine; note the backpressure is channel-wide: a
// consumer stalled past its 64-message queue eventually stalls dispatch of
// unrelated responses too, until the channel closes or the consumer drains.
type RPCHandlers struct {
	Response chan<- *leapmuxv1.InnerRpcResponse
	Stream   func(*leapmuxv1.InnerStreamMessage)
}

type streamCallback struct {
	callback func(*leapmuxv1.InnerStreamMessage)
	messages chan *leapmuxv1.InnerStreamMessage
	done     chan struct{}
	// chDone is the owning channel's lifetime Done channel. deliver selects on
	// it so that a channel-wide cancel (a peer write failure calling ch.cancel,
	// or Close) unblocks recvLoop even when a saturated stream consumer has
	// stalled the dispatcher -- otherwise recvLoop would wedge forever with the
	// channel reporting healthy, leaking the channel and its goroutines.
	chDone   <-chan struct{}
	stopOnce sync.Once
}

func newStreamCallback(callback func(*leapmuxv1.InnerStreamMessage), chDone <-chan struct{}) *streamCallback {
	stream := &streamCallback{
		callback: callback,
		messages: make(chan *leapmuxv1.InnerStreamMessage, streamCallbackQueueSize),
		done:     make(chan struct{}),
		chDone:   chDone,
	}
	go stream.run()
	return stream
}

func (s *streamCallback) run() {
	for {
		select {
		case message := <-s.messages:
			s.callback(message)
		case <-s.done:
			return
		}
	}
}

// deliver queues a stream message for the callback's dispatcher goroutine.
// It blocks until the message is queued, the callback is stopped, or the
// owning channel's lifetime ends, applying backpressure to recvLoop (and
// through it the WebSocket/TCP read loop) so a slow stream consumer never
// tears down the shared channel. Selecting on chDone ensures a channel-wide
// cancel still unblocks recvLoop when a saturated consumer has stalled the
// dispatcher, so a wedged stream cannot pin a "healthy" channel forever.
func (s *streamCallback) deliver(message *leapmuxv1.InnerStreamMessage) {
	select {
	case s.messages <- message:
	case <-s.done:
	case <-s.chDone:
	}
}

func (s *streamCallback) stop() {
	s.stopOnce.Do(func() { close(s.done) })
}

// OpenChannelOptions configures how OpenChannel connects to the Hub.
type OpenChannelOptions struct {
	// HTTPClient is the HTTP client for ConnectRPC calls (GetWorkerHandshakeParams, etc.).
	// When nil, a default client with 30s timeout is used.
	HTTPClient *http.Client

	// WebSocketHTTPClient is the HTTP/1.1 client used for WebSocket upgrade.
	// When nil, websocket.Dial uses the default transport.
	WebSocketHTTPClient *http.Client

	// LifetimeContext owns the established channel after the handshake. It is
	// required so a request-scoped operation context can never accidentally own
	// a cached or shared channel.
	LifetimeContext context.Context

	// BearerToken, when non-empty, is sent as "Authorization: Bearer
	// <token>" on every hub call (GetWorkerHandshakeParams, OpenChannel,
	// /ws/channel upgrade). Used by the leapmux remote CLI and the
	// worker-side cross-worker client.
	//
	// Apply it with applyAuth rather than by hand: an open touches the Hub four
	// times and a fifth call that forgets the header would not fail loudly -- it
	// would authenticate as nobody and read as a permission bug at the Hub.
	BearerToken string

	// ExpectedUserID, when non-empty, is the identity the caller believes its
	// credential authenticates as. OpenChannel fails if the Hub authenticated the
	// open as anyone else.
	//
	// It is a CROSS-CHECK, not an assertion: the Hub's answer stays authoritative
	// and is never overridden by this value. What it catches is the caller and the
	// Hub silently disagreeing -- a pooled channel keyed by the identity the caller
	// ASKED for while the Hub authenticated the bearer as someone else would route
	// every later call on it as the wrong user, with nothing in the stack able to
	// notice. Callers that pool or key by identity (crossworker.Client) must set it,
	// as must any caller that resolves other state under its credential's identity
	// (remote.Client: the CLI resolves workspaces and workers under its creds'
	// user_id, so a hub that disagreed would have it resolving as X while running
	// channel RPCs as Y). It is left empty only when the caller genuinely has no
	// expectation — creds carrying no resolved user_id, where the cross-check is a
	// no-op by construction.
	ExpectedUserID string

	// KeyPin, when non-nil, verifies (and on first contact records) the
	// worker's public keys against a TOFU pin store. A mismatch aborts
	// the handshake — defends against a compromised hub substituting
	// keys.
	KeyPin KeyPinStore
}

// KeyPinStore captures the per-hub TOFU key-pinning behaviour the
// CLI / cross-worker callers need. Implementations are responsible for
// persistence (CLI: ~/.config/leapmux/remote/<hub-host>/pins.json;
// worker: <datadir>/cross_worker_pins.json).
type KeyPinStore interface {
	// Verify is called with the worker's freshly-fetched public keys.
	// On first contact (no pin yet) the implementation records the
	// pin (TOFU) and returns nil. On a mismatch it returns a non-nil
	// error and OpenChannel aborts.
	Verify(workerID string, publicKey, mlkemPublicKey, slhdsaPublicKey []byte) error
}

// OpenChannel opens a new E2EE channel to the specified worker via Hub.
// Authentication is handled via cookies in the HTTP client's cookie jar
// (default) or, when opts.BearerToken is set, via "Authorization:
// Bearer <token>" on every hub call.
// DefaultChannelOpenTimeout bounds a single OpenChannel: two ConnectRPC calls, the
// Noise handshake, the /ws/channel WebSocket dial, and the open-time Ping.
//
// It lives here, beside the call it bounds, because every caller needs the same
// bound for the same reason and none of them can see each other's copy: an open runs
// under a lifetime context with no deadline (crossworker.Client's, the desktop
// TunnelManager's epoch context) and the clients carry no http.Client.Timeout, so a
// hub that accepts the connection but stalls the upgrade would wedge the open -- and
// every caller pooled behind it -- indefinitely.
//
// OpenChannel does NOT apply this itself: it takes the caller's context, and callers
// legitimately tighten it. It is the default a caller wraps with, and the figure this
// file's own budget reasoning (see sessionVerifyTimeout) is written against -- that
// reasoning previously cited "crossworker's 30s channelOpenTimeout" from a package
// that could not see it, which is exactly the coupling-by-comment this replaces.
const DefaultChannelOpenTimeout = 30 * time.Second

// applyAuth adds this client's Hub credential to h, if it has one.
//
// It is the single home of "how this client authenticates to the Hub" because one
// open touches the Hub four times -- the handshake-params call, the open call, the
// /ws/channel upgrade, and the rollback close -- and each spelled the same
// `if bearer != "" { set Authorization }` by hand. A fifth call that omits it does
// not fail loudly at the call site; it authenticates as nobody and surfaces as a
// permission error from the Hub, at a layer that cannot tell the two apart.
//
// Nil-receiver-safe so callers need no guard: OpenChannel rejects a nil opts up
// front, and rollbackRegisteredChannel runs on a failure path where defensiveness
// is cheaper than a nil-check at each site.
func (o *OpenChannelOptions) applyAuth(h http.Header) {
	if o == nil || o.BearerToken == "" {
		return
	}
	h.Set("Authorization", "Bearer "+o.BearerToken)
}

// initiatorHandshaker binds the two halves of a Noise_NK initiator handshake to ONE
// reading of the worker's EncryptionMode.
//
// Message 1 and message 2 are sent a Hub round trip apart, and branching on the mode
// at each of them made the pairing a convention rather than a fact: noiseutil exposes
// a single *HandshakeState for both modes, so finishing a classical handshake with the
// hybrid message-2 reader (or the reverse) compiles cleanly and fails only as a
// decrypt error at the far end, 50 lines from the branch that caused it. Selecting the
// matched pair once makes that mismatch unconstructible -- including for a third
// EncryptionMode wired into message 1 and forgotten at message 2.
type initiatorHandshaker struct {
	// start produces handshake message 1 and the state finish must be handed back.
	start func() (*noiseutil.HandshakeState, []byte, error)
	// finish consumes the worker's handshake message 2 and yields the session.
	finish func(hs *noiseutil.HandshakeState, message2 []byte) (*noiseutil.Session, error)
}

// newInitiatorHandshaker selects the handshake pair for the worker's encryption mode,
// reading params.EncryptionMode exactly once.
//
// Every mode but CLASSIC gets the hybrid pair. That is deliberate rather than a
// fallthrough: UNSPECIFIED already means POST_QUANTUM by the time it reaches a client
// (ChannelService.GetWorkerHandshakeParams normalises it), and defaulting an unknown
// future mode to the stronger handshake fails it closed on a handshake error rather
// than silently downgrading the channel to X25519-only.
func newInitiatorHandshaker(params *leapmuxv1.GetWorkerHandshakeParamsResponse) initiatorHandshaker {
	if params.GetEncryptionMode() == leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC {
		return initiatorHandshaker{
			start: func() (*noiseutil.HandshakeState, []byte, error) {
				return noiseutil.ClassicalInitiatorHandshake1(params.GetPublicKey())
			},
			finish: noiseutil.ClassicalInitiatorHandshake2,
		}
	}
	return initiatorHandshaker{
		start: func() (*noiseutil.HandshakeState, []byte, error) {
			return noiseutil.InitiatorHandshake1(params.GetPublicKey(), params.GetMlkemPublicKey())
		},
		finish: func(hs *noiseutil.HandshakeState, message2 []byte) (*noiseutil.Session, error) {
			return noiseutil.InitiatorHandshake2(hs, message2, params.GetSlhdsaPublicKey())
		},
	}
}

func OpenChannel(ctx context.Context, hubURL, workerID string, opts *OpenChannelOptions) (*Channel, error) {
	if ctx == nil {
		return nil, errors.New("open channel: operation context is required")
	}
	if opts == nil || opts.LifetimeContext == nil {
		return nil, errors.New("open channel: lifetime context is required")
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	lifetimeCtx := opts.LifetimeContext
	if opts.HTTPClient != nil {
		httpClient = opts.HTTPClient
	}
	pinStore := opts.KeyPin
	channelClient := leapmuxv1connect.NewChannelServiceClient(httpClient, hubURL)

	// 1. Get Worker's public key and encryption mode in one round trip.
	paramsReq := connect.NewRequest(&leapmuxv1.GetWorkerHandshakeParamsRequest{WorkerId: workerID})
	opts.applyAuth(paramsReq.Header())
	paramsResp, err := channelClient.GetWorkerHandshakeParams(ctx, paramsReq)
	if err != nil {
		return nil, fmt.Errorf("get worker handshake params: %w", err)
	}

	// TOFU key pinning — abort if the hub returns keys that don't match
	// the recorded pin. First contact records the pin.
	if pinStore != nil {
		if err := pinStore.Verify(workerID, paramsResp.Msg.GetPublicKey(), paramsResp.Msg.GetMlkemPublicKey(), paramsResp.Msg.GetSlhdsaPublicKey()); err != nil {
			return nil, fmt.Errorf("worker key pin: %w", err)
		}
	}

	// 2. Perform Noise_NK handshake (message 1). The mode is read once here; the
	// matched message-2 half travels with it (see initiatorHandshaker).
	handshaker := newInitiatorHandshaker(paramsResp.Msg)
	hs, msg1, err := handshaker.start()
	if err != nil {
		return nil, fmt.Errorf("handshake1: %w", err)
	}

	// 3. Open channel via Hub.
	openReq := connect.NewRequest(&leapmuxv1.OpenChannelRequest{
		WorkerId:         workerID,
		HandshakePayload: msg1,
	})
	opts.applyAuth(openReq.Header())
	openResp, err := channelClient.OpenChannel(ctx, openReq)
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}
	channelID := openResp.Msg.GetChannelId()
	if channelID == "" {
		return nil, errors.New("open channel: hub returned an empty channel id")
	}
	rollback := true
	defer func() {
		if rollback {
			rollbackRegisteredChannel(channelClient, opts, channelID)
		}
	}()
	// The Hub establishes channel identity and names it here; this end never
	// asserts a locally-known id, because a stale one (an account or impersonation
	// switch) must never be able to speak for the channel.
	userID := openResp.Msg.GetUserId()
	if userID == "" {
		return nil, errors.New("open channel: hub returned an empty authenticated user id")
	}
	// Cross-check it against what the caller expected, when the caller has an
	// expectation. Taking the Hub's answer and DISCARDING it -- which is what this
	// did before -- means a caller that pools or keys by identity can hold a channel
	// the Hub authenticated as someone else and never find out: every later call on
	// it silently runs as the wrong user. Comparing is not asserting; the Hub still
	// wins, the open just fails instead of proceeding on a disagreement.
	if opts.ExpectedUserID != "" && userID != opts.ExpectedUserID {
		return nil, fmt.Errorf(
			"open channel: hub authenticated this channel as %q, not the expected %q",
			userID, opts.ExpectedUserID)
	}

	// 4. Complete handshake (message 2), with the half that matches the message-1
	// this handshaker already sent -- no second reading of the encryption mode.
	session, err := handshaker.finish(hs, openResp.Msg.GetHandshakePayload())
	if err != nil {
		return nil, fmt.Errorf("handshake2: %w", err)
	}

	// 5. Connect to Hub's WebSocket relay.
	wsURL := channelwire.HTTPToWS(hubURL) + "/ws/channel"

	wsDialOpts := &websocket.DialOptions{
		Subprotocols: []string{"channel-relay"},
		HTTPHeader:   http.Header{},
	}
	if opts.WebSocketHTTPClient != nil {
		wsDialOpts.HTTPClient = opts.WebSocketHTTPClient
	}
	opts.applyAuth(wsDialOpts.HTTPHeader)
	wsConn, _, err := websocket.Dial(ctx, wsURL, wsDialOpts)
	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}
	wsConn.SetReadLimit(channelwire.WSReadLimit)

	if err := checkOpenCanceled(ctx, lifetimeCtx, func() { _ = wsConn.CloseNow() }); err != nil {
		return nil, err
	}
	chCtx, chCancel := context.WithCancel(lifetimeCtx)
	ch := &Channel{
		channelID:  channelID,
		userID:     userID,
		session:    session,
		ws:         wsConn,
		ctx:        chCtx,
		cancel:     chCancel,
		sendPermit: make(chan struct{}, 1),
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}

	// 6. Start receiving. The Hub already told both ends who the caller is (this
	// side reads it from OpenChannelResponse.user_id, the Worker from
	// ChannelOpenRequest.user_id), so there is no identity to claim over the channel --
	// and with no claim there is no reserved correlation id and no pre-recvLoop
	// pending[0] registration.
	go ch.recvLoop()

	// 7. Prove the session works end to end before handing the channel out.
	//
	// The Noise_NK handshake only proves THIS side can encrypt to the worker's
	// static key; it proves nothing about the worker's session decrypting, or its
	// replies decrypting back here. Without a round trip now, a session broken in
	// either direction opens "successfully" and fails on the caller's first real
	// call -- and channels are POOLED (crossworker.Client, the desktop
	// TunnelManager), so the broken one is cached and handed to every later caller
	// until something evicts it. One Ping keeps the failure at the open, where it
	// is attributable and the caller re-resolves.
	//
	// Bounded by sessionVerifyTimeout rather than CallRPC's generic 30s: this runs
	// INSIDE the open, so its budget is the open's, not a normal RPC's. Callers whose
	// own context is unbounded (the `leapmux remote` commands) would otherwise hang
	// for 30s on a wedged worker, and a caller's DefaultChannelOpenTimeout would be
	// consumed entirely by this one round trip. The claim exchange this replaced
	// bounded itself at 10s for the same reason.
	pingCtx, cancelPing := context.WithTimeout(ctx, sessionVerifyTimeout)
	defer cancelPing()
	if _, err := ch.CallRPC(pingCtx, channelwire.PingMethod, nil); err != nil {
		ch.Close()
		return nil, fmt.Errorf("verify channel session: %w", err)
	}

	if err := checkOpenCanceled(ctx, lifetimeCtx, ch.Close); err != nil {
		return nil, err
	}

	slog.Info("tunnel E2EE channel opened", "channel_id", channelID, "worker_id", workerID)
	rollback = false
	return ch, nil
}

// checkOpenCanceled aborts an in-flight OpenChannel when either the caller's
// open context or the channel's lifetime context has been canceled, running
// cleanup (tear down whatever the open has built so far) before reporting the
// cancellation. OpenChannel runs it after each blocking step; one helper keeps
// the pair of contexts -- and the order they are consulted in -- from drifting
// between the exits.
func checkOpenCanceled(ctx, lifetimeCtx context.Context, cleanup func()) error {
	if err := ctx.Err(); err != nil {
		cleanup()
		return err
	}
	if err := lifetimeCtx.Err(); err != nil {
		cleanup()
		return err
	}
	return nil
}

func rollbackRegisteredChannel(client leapmuxv1connect.ChannelServiceClient, opts *OpenChannelOptions, channelID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := connect.NewRequest(&leapmuxv1.CloseChannelRequest{ChannelId: channelID})
	opts.applyAuth(req.Header())
	if _, err := client.CloseChannel(ctx, req); err != nil {
		slog.Warn("failed to roll back registered channel", "channel_id", channelID, "error", err)
	}
}

// Closed returns true if the channel is no longer usable -- either Close has
// run, OR its lifetime context has been cancelled. A shared-transport write
// failure calls ch.cancel() (see sendInnerContext) and only recvLoop's deferred
// Close later sets `closed`, so checking `closed` alone would briefly report a
// dead channel as live. A pooled/cached caller (crossworker.channelFor, the
// desktop TunnelManager) that reused it in that window would dial through a
// channel whose next SendRPCNoWait fails "channel closed" instead of
// re-resolving a fresh one -- exactly the self-heal this predicate gates.
func (ch *Channel) Closed() bool {
	return ch.closed.Load() || ch.ctx.Err() != nil
}

// Close closes the channel.
//
// It cancels ch.ctx and DROPS the pending map; it deliberately does not close each
// pending response channel. Closing them would be a send-on-closed-channel panic:
// recvLoop delivers into those channels without holding ch.mu across the send, so a
// close here races a delivery in flight. Dropping the map is what makes the send
// safe -- a delivery to a channel nobody will read is a buffered no-op.
//
// The contract that follows, and that EVERY waiter must honour: a pending response
// channel is never closed and never receives nil, so a bare `<-respCh` after Close
// blocks forever. Every wait MUST also select on ch.Context().Done(), which Close
// cancels. That is the only wake-up Close provides.
func (ch *Channel) Close() {
	if ch.closed.CompareAndSwap(false, true) {
		ch.cancel()
		_ = ch.ws.CloseNow()

		ch.mu.Lock()
		streams := make([]*streamCallback, 0, len(ch.streamCbs))
		for _, stream := range ch.streamCbs {
			streams = append(streams, stream)
		}
		ch.pending = make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse)
		ch.streamCbs = make(map[uint64]*streamCallback)
		ch.reassembly = make(map[uint64]*channelwire.ChunkBuffer)
		ch.mu.Unlock()
		for _, stream := range streams {
			stream.stop()
		}
	}
}

// CallRPC sends a unary inner RPC and waits for the response.
func (ch *Channel) CallRPC(ctx context.Context, method string, payload []byte) (*leapmuxv1.InnerRpcResponse, error) {
	if ctx == nil {
		return nil, errors.New("rpc operation context is required")
	}
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	reqID, err := ch.SendRPCNoWait(ctx, method, payload, RPCHandlers{Response: respCh})
	if err != nil {
		return nil, err
	}
	defer func() {
		ch.UnregisterPending(reqID)
	}()
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case resp := <-respCh:
		// No closed/nil arm: respCh is never closed and recvLoop never delivers nil
		// (see Close). A close-time wake-up arrives on ch.ctx.Done() below instead --
		// an arm here claiming otherwise would tell the next reader that a bare
		// receive is safe, which is the one thing that would hang forever.
		if resp.GetIsError() {
			return nil, fmt.Errorf("rpc error (code %d): %s", resp.GetErrorCode(), resp.GetErrorMessage())
		}
		return resp, nil
	case <-waitCtx.Done():
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("rpc timeout: %w", waitCtx.Err())
	case <-ch.ctx.Done():
		return nil, ch.ctx.Err()
	}
}

// SendRPCNoWait sends an inner RPC after atomically installing all handlers.
func (ch *Channel) SendRPCNoWait(ctx context.Context, method string, payload []byte, handlers RPCHandlers) (uint64, error) {
	if ctx == nil {
		return 0, errors.New("rpc operation context is required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := ch.ctx.Err(); err != nil {
		return 0, fmt.Errorf("channel closed: %w", err)
	}
	// deliverResponse delivers with a non-blocking send so one slow caller can
	// never wedge the sole receive goroutine, which makes buffering the caller's
	// contract: an unbuffered channel would have its ONLY response dropped -- and
	// logged as a spurious "duplicate" -- rather than delivered. Enforce it here
	// instead of leaving it to a doc comment, so a future caller fails loudly at
	// the call rather than silently losing a response at delivery.
	if handlers.Response != nil && cap(handlers.Response) < 1 {
		return 0, errors.New("rpc response channel must be buffered (cap >= 1)")
	}
	var stream *streamCallback
	if handlers.Stream != nil {
		stream = newStreamCallback(handlers.Stream, ch.ctx.Done())
	}
	ch.mu.Lock()
	if ch.closed.Load() {
		ch.mu.Unlock()
		if stream != nil {
			stream.stop()
		}
		return 0, netClosedError()
	}
	reqID := ch.allocateReqIDLocked()
	if handlers.Response != nil {
		ch.pending[reqID] = handlers.Response
	}
	if stream != nil {
		ch.streamCbs[reqID] = stream
	}
	ch.mu.Unlock()
	innerReq := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Request{
			Request: &leapmuxv1.InnerRpcRequest{
				Method:  method,
				Payload: payload,
			},
		},
	}

	if err := ch.sendInnerContext(ctx, reqID, innerReq); err != nil {
		ch.unregisterRequest(reqID, true, true)
		return 0, err
	}
	return reqID, nil
}

// UnregisterPending removes a pending response channel.
func (ch *Channel) UnregisterPending(reqID uint64) {
	ch.unregisterRequest(reqID, true, false)
}

// UnregisterStream removes a stream callback.
func (ch *Channel) UnregisterStream(reqID uint64) {
	ch.unregisterRequest(reqID, false, true)
}

// allocateReqIDLocked returns the next outbound correlation id, skipping any id
// whose handler is still registered.
//
// The counter itself cannot wrap -- correlation_id is uint64, so at the ~640
// ids/sec a saturated tunnel burns (one per 32 KiB write, one per credit grant)
// exhausting it takes longer than the heat death of anything that matters. This
// skip is therefore not the wrap defence; the WIRE TYPE is (see channel.proto).
//
// What the skip still buys is an in-window guarantee: a live registration is never
// handed to a second request, so the invariant holds by construction rather than by
// arithmetic about counter ranges. 0 is skipped too: it is a legal id, but leaving
// it out keeps a zero-valued correlation id distinguishable from an allocated one.
//
// Terminates by pigeonhole: at most len(pending)+len(streamCbs)+1 values are
// skipped. Caller holds ch.mu.
func (ch *Channel) allocateReqIDLocked() uint64 {
	for {
		ch.nextReqID++
		if ch.nextReqID == 0 {
			continue
		}
		if !ch.hasHandlerLocked(ch.nextReqID) {
			return ch.nextReqID
		}
	}
}

// hasHandlerLocked reports whether reqID still has a live response or stream
// handler -- i.e. whether an inbound message for it can still be delivered.
// Caller holds ch.mu.
func (ch *Channel) hasHandlerLocked(reqID uint64) bool {
	if _, ok := ch.pending[reqID]; ok {
		return true
	}
	_, ok := ch.streamCbs[reqID]
	return ok
}

func (ch *Channel) unregisterRequest(reqID uint64, pending, stream bool) {
	var callback *streamCallback
	ch.mu.Lock()
	if pending {
		delete(ch.pending, reqID)
	}
	if stream {
		callback = ch.streamCbs[reqID]
		delete(ch.streamCbs, reqID)
	}
	// Drop any partial reassembly once the request has no handler left to receive
	// it: the buffer exists only to feed this request, so outliving it would pin
	// its bytes (up to DefaultMaxMessageSize) and consume a slot of the
	// incomplete-chunked cap for the channel's whole life.
	if !ch.hasHandlerLocked(reqID) {
		ch.dropReassemblyLocked(reqID)
	}
	ch.mu.Unlock()
	if callback != nil {
		callback.stop()
	}
}

// Context returns the channel's context.
func (ch *Channel) Context() context.Context {
	return ch.ctx
}

// UserID returns the identity the Hub authenticated this channel's open as.
//
// This is what the channel actually IS, as opposed to what its opener asked for.
// A caller that pools or keys channels by identity should reconcile against this
// rather than its own request -- OpenChannel's ExpectedUserID does exactly that at
// open time, and this exposes the same fact afterwards.
func (ch *Channel) UserID() string {
	return ch.userID
}

func (ch *Channel) sendInnerContext(ctx context.Context, correlationID uint64, msg *leapmuxv1.InnerMessage) error {
	plaintext, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal inner message: %w", err)
	}
	if len(plaintext) > channelwire.DefaultMaxMessageSize {
		return fmt.Errorf("inner message too large: %d > %d", len(plaintext), channelwire.DefaultMaxMessageSize)
	}

	select {
	case ch.sendPermit <- struct{}{}:
		defer func() { <-ch.sendPermit }()
	case <-ctx.Done():
		return ctx.Err()
	case <-ch.ctx.Done():
		return fmt.Errorf("channel closed: %w", ch.ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ch.ctx.Err(); err != nil {
		return fmt.Errorf("channel closed: %w", err)
	}

	// The chunk-split / per-chunk-encrypt / ChannelMessage-build sequence lives in
	// channelwire.SendChannelFrames, shared with the worker's sendEncrypted; this
	// sender stays responsible only for what is specific to the tunnel side --
	// writing each raw ChannelMessage under the channel's LIFETIME context (NOT
	// the caller's ctx: see the WriteChannelMessage note below), and cancelling
	// the channel on any encrypt or write failure.
	//
	// Write under the channel's lifetime context, NOT the caller's ctx.
	// coder/websocket registers context.AfterFunc(ctx, c.close) for any
	// cancellable context passed to Write, force-closing the underlying
	// connection on cancel OR deadline. Routing a per-RPC/deadline context
	// here would let one caller's cancel or one stream's expired write
	// deadline tear down the shared E2EE transport for every unrelated
	// in-flight RPC and stream. The caller's ctx still gates entry: permit
	// acquisition and the pre-write liveness checks above honor it, so a
	// cancelled caller unwinds before (or right after) its turn at the
	// serialized write without ever interrupting an in-flight write.
	if err := channelwire.SendChannelFrames(
		ch.session.Encrypt, ch.channelID, correlationID, plaintext,
		func(chMsg *leapmuxv1.ChannelMessage) error {
			return channelwire.WriteChannelMessage(ch.ctx, ch.ws, chMsg)
		},
	); err != nil {
		// An encrypt failure (nonce exhaustion) abandons this message BETWEEN
		// chunks: the peer is left holding a partial reassembly it can never
		// complete, and the session can never encrypt again. A write failure means
		// the transport is broken. Either way the channel must be cancelled --
		// without it the channel keeps reporting healthy, so a pooled or cached
		// caller neither re-resolves nor re-handshakes and every later send fails
		// forever.
		ch.cancel()
		return err
	}
	return nil
}

// reassembleOutcome is what reassembleLocked decided about one inbound chunk,
// carried back to reassemble so every dispatch happens with ch.mu RELEASED.
//
// reassembleAction is what reassembleLocked tells reassemble to do with the
// chunk it was handed. Replacing the prior bag of optional fields (assembled /
// parts / ok / unknownID / tooLarge string) with a typed action makes the
// outcome read as the decision it is, and stops the dispatch in reassemble from
// reasoning its way out of a set of booleans and a string. Mirrors the worker's
// reassemblyAction in internal/worker/channel/chunker.go.
type reassembleAction int

const (
	// reassembleActBuffered: the chunk was buffered, or a poisoned id's chunk was
	// dropped. reassemble moves on to the next frame.
	reassembleActBuffered reassembleAction = iota
	// reassembleActDropUnknownID: a first chunk arrived for a correlation id with
	// no live handler. reassemble logs it and drops the chunk.
	reassembleActDropUnknownID
	// reassembleActTooLarge: the message breached the size cap or the in-flight
	// cap. reassemble delivers the error to the offending request only.
	reassembleActTooLarge
	// reassembleActDeliver: a complete message is ready to dispatch. Exactly one
	// of assembled (a message that was never chunked, returned as-is) or parts (a
	// chunked message's pieces in order, for reassemble to JOIN outside the lock)
	// is set; the join copies up to DefaultMaxMessageSize, and every RPC, stream
	// and tunnel on this channel contends for ch.mu.
	reassembleActDeliver
)

// reassembleOutcome carries reassembleAction plus the payload reassemble needs to
// act on a Deliver or TooLarge decision.
type reassembleOutcome struct {
	action reassembleAction
	// assembled is the complete message when it was never chunked (Deliver only).
	assembled []byte
	// buf carries a chunked message's accumulating buffer for reassemble to JOIN
	// outside the lock (Deliver only). The pointer survives the buffer-map delete
	// reassembleLocked issues before returning, so buf.Join() reads the same slices
	// AppendChunk filled without holding the reassembly lock through the join. nil
	// for an unchunked delivery.
	buf *channelwire.ChunkBuffer
	// tooLargeMessage is the error a TooLarge outcome must deliver to its request.
	tooLargeMessage string
}

// reassembleLocked folds one inbound chunk into the message it belongs to and
// reports what should happen next. It owns ch.mu for its whole body (defer
// Unlock) and performs NO dispatch, because deliverTooLarge/deliverRPCError run
// handler code and must not be called under the lock: hand-unlocking at each of
// the seven exits was the only reason those exits existed separately, and one
// missed unlock would deadlock recvLoop and every RPC, stream and tunnel
// multiplexed on the channel. Splitting the decision from the dispatch makes the
// discipline mechanical instead of a rule each new exit must remember.
func (ch *Channel) reassembleLocked(correlationID uint64, more bool, plaintext []byte) reassembleOutcome {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	buf, buffering := ch.reassembly[correlationID]

	// An id already errored for breaching a limit: drop the rest of its message
	// without re-buffering a byte, and without erroring the request again.
	//
	// Deleting the buffer instead (as this once did) only works when the failure
	// also unregisters the request. It does not for a stream handler -- a tunnel
	// Conn's onStreamMessage latches its terminal error but never unregisters -- so
	// hasHandlerLocked stayed true, the next MORE chunk allocated a fresh buffer,
	// and the peer could drive re-accumulation to the inner-message ceiling
	// (channelwire.DefaultMaxMessageSize) indefinitely,
	// burning allocate-and-discard cycles on the channel's sole receive goroutine
	// and stalling every other RPC, stream and tunnel multiplexed on it. The entry
	// is cheap (its parts are released by poison, and it holds no cap slot) and
	// unregisterRequest reaps it.
	if buffering && buf.Poisoned {
		if !more {
			ch.dropReassemblyLocked(correlationID)
		}
		return reassembleOutcome{action: reassembleActBuffered}
	}

	if more {
		if !buffering {
			// Reassembly state belongs to the request that will consume it. Every
			// inbound chunked message correlates to a request THIS side registered
			// (recvLoop dispatches no peer-initiated requests), so a first chunk for
			// an id with no live handler can never be completed: buffering it would
			// pin up to DefaultMaxMessageSize until the channel died, and four such
			// orphans would exhaust the cap and permanently reject every subsequent
			// chunked message on an otherwise healthy channel. Tying the buffer's
			// lifetime to the registration (created only for a live request here,
			// dropped by unregisterRequest) keeps the cap a genuine backstop rather
			// than the only bound.
			if !ch.hasHandlerLocked(correlationID) {
				return reassembleOutcome{action: reassembleActDropUnknownID}
			}
			if ch.liveReassemblyLocked() >= channelwire.DefaultMaxIncompleteChunked {
				// Tombstone the rejected id BEFORE erroring it, exactly as the oversize
				// path below does. Erroring without one left chunks 2..N of the message
				// unrecognised: each re-entered this branch and errored the request
				// again (a stream handler latches its error without unregistering, so it
				// never stops arriving), storming the channel's sole receive goroutine
				// and stalling every unrelated RPC ack on it -- including the tunnel
				// send-window releases that unrelated writes are parked on.
				ch.poisonReassemblyLocked(correlationID)
				return reassembleOutcome{action: reassembleActTooLarge, tooLargeMessage: "too many incomplete chunked messages"}
			}
			buf = ch.startReassemblyLocked(correlationID)
		}
		total, breached := buf.AppendChunk(plaintext, channelwire.DefaultMaxMessageSize)
		if breached {
			ch.poisonReassemblyLocked(correlationID)
			return reassembleOutcome{action: reassembleActTooLarge, tooLargeMessage: fmt.Sprintf(
				"chunked message too large: %d bytes exceeds %d byte limit", total, channelwire.DefaultMaxMessageSize)}
		}
		return reassembleOutcome{action: reassembleActBuffered}
	}

	// Final chunk, or a message that was never chunked at all.
	if !buffering {
		if len(plaintext) > channelwire.DefaultMaxMessageSize {
			return reassembleOutcome{action: reassembleActTooLarge, tooLargeMessage: fmt.Sprintf(
				"message too large: %d bytes exceeds %d byte limit", len(plaintext), channelwire.DefaultMaxMessageSize)}
		}
		return reassembleOutcome{action: reassembleActDeliver, assembled: plaintext}
	}

	_, breached := buf.AppendChunk(plaintext, channelwire.DefaultMaxMessageSize)
	// buf leaves the buffer map here, but the *ChunkBuffer pointer is carried in
	// the outcome so reassemble can join it outside the lock -- the slices
	// AppendChunk filled are still live on the heap, and nothing else still reads
	// buf (its map entry is gone).
	ch.dropReassemblyLocked(correlationID)
	if breached {
		return reassembleOutcome{action: reassembleActTooLarge, tooLargeMessage: fmt.Sprintf(
			"chunked message too large: exceeds %d byte limit", channelwire.DefaultMaxMessageSize)}
	}
	return reassembleOutcome{action: reassembleActDeliver, buf: buf}
}

// reassemble folds one inbound chunk into the message it belongs to.
//
// Returns (payload, true) when a complete message is ready to dispatch, and
// (nil, false) when the chunk was buffered, dropped, or errored to its own request
// -- in which case recvLoop simply moves on to the next frame.
//
// A cap/oversize violation errors ONLY the offending request (via deliverRPCError)
// rather than tearing down the shared channel, mirroring the worker's per-request
// sendError: otherwise one oversized message would abort every unrelated in-flight
// RPC, stream, and tunnel sharing this transport. Each violation is errored ONCE:
// reassembleLocked tombstones the id, so the message's remaining chunks are
// dropped silently.
//
// It is separate from recvLoop because it is a state machine with its own locking
// discipline, and burying that inside the transport read loop made both harder to
// follow than either is alone. This half runs entirely with ch.mu released: it
// dispatches into handler code and joins multi-megabyte messages, neither of which
// may happen under the channel's lock.
func (ch *Channel) reassemble(correlationID uint64, flags leapmuxv1.ChannelMessageFlags, plaintext []byte) ([]byte, bool) {
	// An out-of-spec flags value (e.g. MORE|CLOSE combined) is a protocol
	// violation dropped here rather than misread as a final chunk, which
	// would hand a truncated assembly to proto.Unmarshal (see
	// channelwire.ChunkContinuation). The frame was already decrypted by
	// recvLoop, so dropping it does not desync the session.
	more, validFlags := channelwire.ChunkContinuation(flags)
	if !validFlags {
		slog.Warn("tunnel channel dropped message with out-of-spec flags",
			"channel_id", ch.channelID, "correlation_id", correlationID, "flags", flags)
		return nil, false
	}
	out := ch.reassembleLocked(correlationID, more, plaintext)

	switch out.action {
	case reassembleActBuffered:
		return nil, false
	case reassembleActDropUnknownID:
		slog.Warn("tunnel channel dropped chunk for an unknown correlation id",
			"channel_id", ch.channelID, "correlation_id", correlationID)
		return nil, false
	case reassembleActTooLarge:
		ch.deliverTooLarge(correlationID, out.tooLargeMessage)
		return nil, false
	case reassembleActDeliver:
		if out.buf == nil {
			return out.assembled, true
		}
		return out.buf.Join(), true
	}
	return nil, false
}

// recvLoop reads messages from the WebSocket and dispatches them.
func (ch *Channel) recvLoop() {
	defer ch.Close()

	for {
		chMsg, err := channelwire.ReadChannelMessage(ch.ctx, ch.ws)
		if err != nil {
			if ch.closed.Load() || ch.ctx.Err() != nil {
				return
			}
			slog.Error("tunnel channel recv error", "channel_id", ch.channelID, "error", err)
			return
		}

		correlationID := chMsg.GetCorrelationId()
		plaintext, decErr := ch.session.Decrypt(chMsg.GetCiphertext())
		if decErr != nil {
			slog.Error("tunnel channel decrypt error", "channel_id", ch.channelID, "error", decErr)
			return
		}

		assembled, complete := ch.reassemble(correlationID, chMsg.GetFlags(), plaintext)
		if !complete {
			continue
		}
		plaintext = assembled

		var inner leapmuxv1.InnerMessage
		if err := proto.Unmarshal(plaintext, &inner); err != nil {
			slog.Error("tunnel channel unmarshal error", "channel_id", ch.channelID, "error", err)
			continue
		}

		switch kind := inner.GetKind().(type) {
		case *leapmuxv1.InnerMessage_Response:
			ch.deliverResponse(correlationID, kind.Response)

		case *leapmuxv1.InnerMessage_Stream:
			ch.mu.Lock()
			stream, ok := ch.streamCbs[correlationID]
			ch.mu.Unlock()
			if ok {
				stream.deliver(kind.Stream)
			}
		}
	}
}

func netClosedError() error {
	return fmt.Errorf("channel closed: %w", net.ErrClosed)
}

// errCodeResourceExhausted mirrors gRPC's RESOURCE_EXHAUSTED (8), which the
// worker uses for the same chunked-size/cap conditions. Inlined as a constant
// rather than importing grpc/codes because the channel client otherwise has no
// gRPC dependency.
const errCodeResourceExhausted int32 = 8

// deliverResponse routes resp to correlationID's pending response handler and
// reports whether one was registered. respCh is buffered (size 1) and each
// correlation id expects EXACTLY ONE response, and recvLoop is the only sender,
// so the first (and only legitimate) response always finds an empty buffer and
// is delivered -- never dropped, even for a caller that has not drained it yet.
// The non-blocking send therefore only ever discards a SECOND response for the
// same id: a peer protocol violation the caller would never read (it consumes
// the first response and unregisters). Dropping-and-logging that duplicate is
// deliberate: a blocking send would wedge recvLoop -- the sole receive goroutine
// -- until channel teardown, letting one misbehaving id starve every other
// in-flight RPC/stream/tunnel multiplexed on the shared channel.
func (ch *Channel) deliverResponse(correlationID uint64, resp *leapmuxv1.InnerRpcResponse) bool {
	ch.mu.Lock()
	respCh, ok := ch.pending[correlationID]
	ch.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case respCh <- resp:
	default:
		// Buffer already holds this id's response: a duplicate. Surface it (it
		// signals a misbehaving peer) instead of dropping silently.
		slog.Warn("tunnel channel dropped duplicate response for correlation id",
			"channel_id", ch.channelID, "correlation_id", correlationID)
	}
	return true
}

// deliverTooLarge logs a rejected oversized/over-cap frame and fails only its
// own request via deliverRPCError, mirroring the worker's per-request sendError,
// so one bad frame never tears down the shared channel.
func (ch *Channel) deliverTooLarge(correlationID uint64, message string) {
	slog.Warn("tunnel channel frame rejected; erroring request",
		"channel_id", ch.channelID, "correlation_id", correlationID, "reason", message)
	ch.deliverRPCError(correlationID, errCodeResourceExhausted, message)
}

// deliverRPCError fails a single request with an error, so a malformed or
// oversized message aborts only its own RPC instead of the shared E2EE channel
// (which would abort every unrelated in-flight RPC, stream, and tunnel on it).
// It prefers the pending response handler; a request that has already dropped
// its pending handler and left only a stream handler -- a tunnel Conn after
// OpenTunnelConn succeeds -- is failed through the stream handler instead, so
// its Conn.Read gets a terminal error rather than blocking until the whole
// channel dies. Mirrors the worker-side per-request sendError.
func (ch *Channel) deliverRPCError(correlationID uint64, code int32, message string) {
	errResp := channelwire.NewErrorResponse(code, message)
	if ch.deliverResponse(correlationID, errResp) {
		return
	}
	ch.mu.Lock()
	stream, ok := ch.streamCbs[correlationID]
	ch.mu.Unlock()
	if ok {
		stream.deliver(&leapmuxv1.InnerStreamMessage{IsError: true, ErrorCode: code, ErrorMessage: message})
	}
}
