package crossworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/tunnel"
)

// channelOpenTimeout limits a single cross-worker E2EE channel open (the
// delegation mint plus the Noise_NK handshake). The open runs under the
// Client's lifetime context, which has no deadline, so without this a hub that
// accepts the connection but stalls the handshake could wedge the shared open
// -- and every caller waiting on it -- indefinitely.
//
// Sourced from the tunnel package rather than restated: it bounds tunnel.OpenChannel,
// so the figure belongs beside that call, where the open's own internal budgets
// (sessionVerifyTimeout) are reasoned against it. The desktop sidecar bounds the same
// call and had its own hand-copied 30s.
const channelOpenTimeout = tunnel.DefaultChannelOpenTimeout

// DelegationScope identifies the user the bearer is minted for, plus the
// spawn provenance (agent_id OR terminal_id — the hub uses these for the
// audit log). UserID is required; the spawn identifiers may be empty for
// hub-facing calls without a specific spawn provenance.
type DelegationScope struct {
	UserID     userid.UserID
	AgentID    string
	TerminalID string
}

// DelegationProvider supplies a fresh delegation-token bearer for the user
// the spawning worker needs to act as. The implementation calls the hub's
// /worker/delegation-tokens/mint endpoint with the worker's own AuthToken
// and caches the result.
type DelegationProvider interface {
	GetBearer(ctx context.Context, scope DelegationScope) (string, error)
}

// Client maintains a pool of E2EE channels keyed by (target_worker, user) so
// calls with different delegation scopes never share a Noise_NK session.
//
// All hub calls (GetWorkerHandshakeParams, OpenChannel, /ws/channel)
// authenticate with a delegation token obtained via DelegationProvider.
type Client struct {
	Pins       *PinStore
	Delegation DelegationProvider
	endpoint   *hubtransport.Endpoint
	// The two lanes an open needs, built ONCE. Every other consumer of the lane
	// API takes its clients at construction (NewHubUnaryBridge,
	// NewHubEventStreamer, NewDelegationStore, hub.New); this one built a pair
	// per channel open, on a path that already holds the pool mutex.
	unaryClient     *http.Client
	webSocketClient *http.Client
	ctx             context.Context
	cancel          context.CancelFunc

	mu       sync.Mutex
	channels map[clientKey]*tunnel.Channel
	// inflight single-flights concurrent opens for the same key: the first
	// caller to miss the cache starts the open and later callers wait on it.
	inflight map[clientKey]*channelOpen
}

type clientKey struct {
	WorkerID string
	UserID   string
}

// channelOpen is a single in-flight channel open shared by every caller that
// requested the same (worker, user) while it runs, so a burst of concurrent
// calls mints one delegation token and dials one Noise_NK handshake instead
// of N (all but one otherwise discarded).
type channelOpen struct {
	done chan struct{}
	ch   *tunnel.Channel
	err  error
}

// New returns a ready-to-use Client.
func New(lifetimeCtx context.Context, endpoint *hubtransport.Endpoint, pins *PinStore, dp DelegationProvider) *Client {
	if lifetimeCtx == nil {
		panic("crossworker.New: lifetime context is required")
	}
	ctx, cancel := context.WithCancel(lifetimeCtx)
	return &Client{
		Pins:            pins,
		Delegation:      dp,
		endpoint:        endpoint,
		unaryClient:     endpoint.UnaryClient(hubtransport.DefaultUnaryTimeout),
		webSocketClient: endpoint.WebSocketClient(),
		ctx:             ctx,
		cancel:          cancel,
		channels:        make(map[clientKey]*tunnel.Channel),
		inflight:        make(map[clientKey]*channelOpen),
	}
}

// channelFor returns a (cached) E2EE channel to targetWorkerID for
// scope.UserID. Mints a fresh delegation token + channel on cache miss.
func (c *Client) channelFor(ctx context.Context, targetWorkerID string, scope DelegationScope) (*tunnel.Channel, error) {
	if err := c.validateOpen(targetWorkerID, scope); err != nil {
		return nil, err
	}
	key := clientKey{WorkerID: targetWorkerID, UserID: scope.UserID.String()}

	c.mu.Lock()
	if existing, ok := c.channels[key]; ok && existing != nil {
		if !existing.Closed() {
			c.mu.Unlock()
			return existing, nil
		}
		// Evict the dead channel now rather than leaving it referenced until a
		// later successful open overwrites it: runChannelOpen only writes
		// c.channels[key] on success, so a persistent open failure would keep the
		// torn-down *tunnel.Channel pinned in the map. Mirrors desktop
		// channelPool.getOrOpen, which deletes a closed entry inline. The two
		// single-flight opener skeletons are deliberately duplicated: desktop
		// owns epoch/revision cancel + eager eviction; this client owns
		// ExpectedUserID identity pinning + lazy eviction. A shared generic
		// opener was rejected — see https://github.com/leapmux/leapmux/issues/281.
		delete(c.channels, key)
	}
	// Single-flight: reuse an in-flight open for this key instead of minting a
	// second delegation token and running a redundant Noise_NK handshake. The
	// open runs on its own goroutine under the Client's lifetime context (not any
	// one caller's), so a caller cancelling only unblocks that caller -- the
	// shared open, and every other waiter, keeps going.
	open, inFlight := c.inflight[key]
	if !inFlight {
		open = &channelOpen{done: make(chan struct{})}
		c.inflight[key] = open
		go c.runChannelOpen(open, key, targetWorkerID, scope)
	}
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	case <-open.done:
		if open.err != nil {
			return nil, open.err
		}
		return open.ch, nil
	}
}

// dedicatedChannel opens an unpooled channel to targetWorkerID. The
// caller owns it and must Close it.
//
// It shares channelFor's argument validation but deliberately not its
// cache: see StreamInner for why a subscriber needs a channel id nobody
// else is registering against.
func (c *Client) dedicatedChannel(ctx context.Context, targetWorkerID string, scope DelegationScope) (*tunnel.Channel, error) {
	if err := c.validateOpen(targetWorkerID, scope); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Bound the open by the CALLER's context as well as the Client's.
	//
	// The pooled path deliberately does not do this -- its open is shared,
	// so one caller walking away must not abort it for the others -- but
	// this channel has exactly one caller by construction. Without the
	// join, a cancelled StreamInner stayed parked here for up to
	// channelOpenTimeout completing a handshake it then immediately
	// closed, holding the worker-side stream handler goroutine open long
	// after the client was gone.
	openCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(c.ctx, cancel)()

	return c.openChannel(openCtx, targetWorkerID, scope)
}

// validateOpen rejects the arguments no channel open can proceed without,
// and the already-shut-down Client. Shared by both open paths so a new
// required field cannot be enforced on one and forgotten on the other.
func (c *Client) validateOpen(targetWorkerID string, scope DelegationScope) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if targetWorkerID == "" {
		return errors.New("crossworker: target_worker_id required")
	}
	if scope.UserID.IsZero() {
		return errors.New("crossworker: user_id required")
	}
	return nil
}

// runChannelOpen performs a single shared channel open and publishes the result
// to every waiter. It caches a successful channel and always clears the
// in-flight marker so the next cache miss starts a fresh open.
func (c *Client) runChannelOpen(open *channelOpen, key clientKey, targetWorkerID string, scope DelegationScope) {
	ch, err := c.openChannel(c.ctx, targetWorkerID, scope)

	c.mu.Lock()
	defer c.mu.Unlock()
	if cur := c.inflight[key]; cur == open {
		delete(c.inflight, key)
	}
	if err == nil {
		if cerr := c.ctx.Err(); cerr != nil {
			// The Client was closed while dialing; do not pool a channel whose
			// lifetime context is already cancelled.
			ch.Close()
			ch, err = nil, cerr
		} else {
			c.channels[key] = ch
		}
	}
	open.ch, open.err = ch, err
	close(open.done)
}

// openChannel mints a delegation token and opens a fresh E2EE channel, bounded
// by channelOpenTimeout so a stalled hub cannot wedge the shared open.
//
// parent decides what else can abort it: the pooled path passes the
// Client's lifetime, because that open is shared and no single waiter may
// cancel it, while the dedicated path passes a context joined with its
// one caller's.
func (c *Client) openChannel(parent context.Context, targetWorkerID string, scope DelegationScope) (*tunnel.Channel, error) {
	openCtx, cancel := context.WithTimeout(parent, channelOpenTimeout)
	defer cancel()

	bearer, err := c.Delegation.GetBearer(openCtx, scope)
	if err != nil {
		return nil, fmt.Errorf("delegation token: %w", err)
	}
	openOpts := &tunnel.OpenChannelOptions{
		LifetimeContext: c.ctx,
		BearerToken:     bearer,
		KeyPin:          c.Pins,
		// Both clients come from the endpoint. Left nil, tunnel.OpenChannel
		// falls back to http.DefaultClient, which knows nothing about a
		// `unix:`/`npipe:` hub -- so a cross-worker call on a socket hub could
		// not open a channel at all -- and which shares the process-global
		// pool with every other caller.
		HTTPClient:          c.unaryClient,
		WebSocketHTTPClient: c.webSocketClient,
		// The pool keys on scope.UserID, so the channel MUST be the identity this
		// scope names. A DelegationProvider that returns a bearer minted for another
		// scope -- a cache keyed on too few fields, a mint response that does not
		// match its request -- would otherwise pool a channel the Hub authenticated
		// as X under the key for Y, and every later CallInner on it would silently
		// run as X with nothing in the stack able to detect it.
		ExpectedUserID: scope.UserID.String(),
	}
	// BaseURL, not HubURL: a `unix:`/`npipe:` hub presents the placeholder
	// origin that its transport dials, and every other scheme is unchanged.
	return tunnel.OpenChannel(openCtx, c.endpoint.BaseURL(), targetWorkerID, openOpts)
}

// CallInner sends a unary inner RPC to a sibling worker. userID is the
// delegation scope used both for minting the bearer and for keying the
// channel pool.
func (c *Client) CallInner(ctx context.Context, targetWorkerID string, userID userid.UserID, method string, payload []byte) ([]byte, error) {
	ch, err := c.channelFor(ctx, targetWorkerID, DelegationScope{UserID: userID})
	if err != nil {
		return nil, err
	}
	resp, err := ch.CallRPC(ctx, method, payload)
	if err != nil {
		return nil, err
	}
	return resp.GetPayload(), nil
}

// StreamInner subscribes to a server-streaming inner RPC and invokes
// onMsg for every message. Returns when the stream ends or ctx is
// cancelled. userID semantics match CallInner.
//
// Unlike CallInner this does NOT use the pooled channel: it opens a
// dedicated one and closes it when the stream ends.
//
// Subscription state on the target worker is keyed by channel id -- the
// watcher registry holds one registration per (entity, channel) -- and a
// WatchEvents request states that channel's whole current interest. Two
// concurrent streams sharing a pooled channel therefore overwrite each
// other: the second subscription silently unsubscribes every entity the
// first one uniquely held, with no error on either side, so the first
// CLI simply stops receiving events and never learns why. Pooling is a
// connection-reuse optimisation; it must not make two independent
// subscribers share an identity.
func (c *Client) StreamInner(ctx context.Context, targetWorkerID string, userID userid.UserID, method string, payload []byte, onMsg func(*leapmuxv1.InnerStreamMessage), bindCtrl func(channel.StreamController)) error {
	ch, err := c.dedicatedChannel(ctx, targetWorkerID, DelegationScope{UserID: userID})
	if err != nil {
		return err
	}
	defer ch.Close()
	done := make(chan struct{})
	var doneOnce sync.Once
	var streamErr error
	// `closed` drops a frame the per-stream dispatcher delivers after StreamInner
	// has already returned: the dispatcher runs the callback on its OWN goroutine,
	// so a frame queued before teardown (a stalled ctx cancel, or a server that
	// sends past End) can race the return and invoke onMsg on a caller that has
	// finished. The flag is checked ONLY before onMsg, deliberately NOT
	// synchronized with it -- holding a lock across a back-pressured onMsg would
	// let the callback block the teardown that sets `closed`. Guarding a lone bool
	// with no compound invariant is exactly an atomic.Bool, so it is one. This
	// mirrors streamevents.ChannelTransport's identical guard.
	var closed atomic.Bool
	// A streaming RPC can terminate with an error envelope (InnerRpcResponse)
	// instead of a stream frame -- "only the worker owner may use this", "no dispatcher
	// configured", "too many incomplete chunked messages", or any handler
	// SendError before its first stream frame. Without a Response handler,
	// recvLoop finds no pending entry and drops it, so StreamInner would hang
	// until the caller's context expired. Register respCh so a terminal error
	// is surfaced; this mirrors streamevents.ChannelTransport, which watches a
	// response channel for exactly this case.
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	reqID, err := ch.SendRPCNoWait(ctx, method, payload, tunnel.RPCHandlers{
		Response: respCh,
		Stream: func(m *leapmuxv1.InnerStreamMessage) {
			if closed.Load() {
				return
			}
			if m.GetIsError() {
				// Assign streamErr inside the Once so it is written exactly once,
				// happens-before close(done); a trailing error frame after the
				// terminating one is a no-op and never races `return streamErr`.
				doneOnce.Do(func() {
					streamErr = fmt.Errorf("stream error (code %d): %s", m.GetErrorCode(), m.GetErrorMessage())
					close(done)
				})
				return
			}
			onMsg(m)
			if m.GetEnd() {
				doneOnce.Do(func() { close(done) })
			}
		},
	})
	if err != nil {
		return err
	}
	defer ch.UnregisterStream(reqID)
	defer ch.UnregisterPending(reqID)
	// Flip `closed` BEFORE unregistering (defers run LIFO, so this runs first) so
	// any frame the dispatcher delivers from here on drops itself instead of
	// calling onMsg after StreamInner returns.
	defer func() {
		closed.Store(true)
	}()
	// Expose a controller so the local-IPC router can forward UpdateStream /
	// CancelStream onto this dedicated channel's correlation id. Without it,
	// a sibling-worker WatchEvents subscription can never revise its interest.
	if bindCtrl != nil {
		bindCtrl(newCrossWorkerStreamCtrl(ch, reqID))
	}
	select {
	case <-done:
		return streamErr
	case resp := <-respCh:
		// Server returned a non-stream response: for a streaming RPC this is an
		// error envelope. Surface it instead of hanging.
		//
		// No nil arm: recvLoop never delivers nil and respCh is never closed, so a
		// closed channel wakes the ch.Context().Done() arm below (see
		// tunnel.Channel.Close), never this one.
		if resp.GetIsError() {
			return fmt.Errorf("rpc error (code %d): %s", resp.GetErrorCode(), resp.GetErrorMessage())
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-ch.Context().Done():
		return ch.Context().Err()
	}
}

// crossWorkerStreamCtrl forwards client→worker stream frames onto a dedicated
// cross-worker E2EE channel. Implements channel.StreamController so the
// controlipc Router's UpdateStream / CancelStream paths reach the remote
// watchSession the same way a local BindStream does.
//
// Updates are enqueued to a dedicated goroutine so OnClientFrame never blocks
// the StreamController hot path (StreamController contract).
//
// The loop goroutine is the SINGLE OWNER of the updates channel: it is the only
// thing that ranges over it, and OnCancel is the only thing that closes it. A
// send on a closed channel panics regardless of select/default, so senders
// (OnClientFrame) never send on the channel directly — they post the payload
// into a mutex-guarded pending slot that the loop drains. This makes the
// send-vs-close race mechanically impossible rather than guarded by a comment.
// OnCancel sets a closed flag under mu so a concurrent OnClientFrame observes
// retirement and drops without posting; the loop exits once it drains the slot
// and observes the close.
type crossWorkerStreamCtrl struct {
	ch    *tunnel.Channel
	reqID uint64

	mu      sync.Mutex
	pending []byte // newest pending revision; nil when nothing parked
	wake    chan struct{}
	closed  bool

	doneOnce sync.Once
	done     chan struct{} // closed when the loop has exited
}

func newCrossWorkerStreamCtrl(ch *tunnel.Channel, reqID uint64) *crossWorkerStreamCtrl {
	c := &crossWorkerStreamCtrl{
		ch:    ch,
		reqID: reqID,
		wake:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
	go c.loop()
	return c
}

func (c *crossWorkerStreamCtrl) loop() {
	defer close(c.done)
	chDone := c.ch.Context().Done()
	for {
		// Drain the pending slot under the lock so a post that races the drain
		// is observed on the next wake rather than lost.
		c.mu.Lock()
		payload := c.pending
		c.pending = nil
		closed := c.closed
		c.mu.Unlock()

		if payload != nil {
			if err := c.ch.SendStreamRequest(c.ch.Context(), c.reqID, payload, false); err != nil {
				slog.Warn("cross-worker stream update failed",
					"correlation_id", c.reqID, "error", err)
			}
		}

		if closed {
			// Drain anything posted between the snapshot above and the closed
			// flag being observed — a final revision is still useful, and the
			// cancel frame that follows supersedes it anyway.
			c.mu.Lock()
			payload = c.pending
			c.pending = nil
			c.mu.Unlock()
			if payload != nil {
				if err := c.ch.SendStreamRequest(c.ch.Context(), c.reqID, payload, false); err != nil {
					// The channel may already be tearing down on the cancel path;
					// a send error here is expected and not actionable.
					slog.Debug("cross-worker stream final update dropped",
						"correlation_id", c.reqID, "error", err)
				}
			}
			return
		}

		select {
		case <-c.wake:
		case <-chDone:
			// Channel gone; retire quietly. OnCancel (if it runs later) will
			// see c.closed and skip the CancelStream send below.
			c.mu.Lock()
			c.closed = true
			c.mu.Unlock()
			return
		}
	}
}

func (c *crossWorkerStreamCtrl) OnClientFrame(payload []byte) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.pending = payload
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *crossWorkerStreamCtrl) OnCancel() {
	c.doneOnce.Do(func() {
		// Mark closed so OnClientFrame stops posting and the loop exits after
		// draining its final pending slot. The loop owns wake; we never close
		// it, avoiding the send-on-closed-channel hazard entirely.
		c.mu.Lock()
		alreadyClosed := c.closed
		c.closed = true
		c.mu.Unlock()
		// Wake the loop so it observes the close promptly.
		select {
		case c.wake <- struct{}{}:
		default:
		}
		// Wait for the loop to finish sending any in-flight revision before
		// issuing the cancel; this keeps the ordering (updates-then-cancel)
		// the remote watchSession expects. Skip the cancel if the channel
		// context is already gone — the remote retires via channel teardown,
		// and a send on a torn-down channel only logs noise.
		<-c.done
		if alreadyClosed {
			return
		}
		ctx := c.ch.Context()
		if ctx.Err() != nil {
			return
		}
		if err := c.ch.CancelStream(ctx, c.reqID); err != nil {
			slog.Debug("cross-worker stream cancel failed",
				"correlation_id", c.reqID, "error", err)
		}
	})
}

// Close terminates all pooled channels.
func (c *Client) Close() {
	c.mu.Lock()
	c.cancel()
	channels := c.channels
	c.channels = make(map[clientKey]*tunnel.Channel)
	c.mu.Unlock()
	for _, ch := range channels {
		if ch != nil {
			ch.Close()
		}
	}
}
