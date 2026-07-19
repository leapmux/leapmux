package crossworker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/tunnel"
)

// channelOpenTimeout bounds a single cross-worker E2EE channel open (the
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

// DelegationScope identifies the (user, workspace) the bearer is
// minted against, plus the spawn provenance (agent_id OR terminal_id —
// the hub uses these for the audit log). UserID and WorkspaceID are
// required; the spawn identifiers may be empty for hub-facing calls
// without a specific spawn provenance.
type DelegationScope struct {
	UserID      string
	WorkspaceID string
	AgentID     string
	TerminalID  string
}

// DelegationProvider supplies a fresh delegation-token bearer for the
// (user, workspace) pair the spawning worker needs to act on. The
// implementation calls the hub's /worker/delegation-tokens/mint
// endpoint with the worker's own AuthToken and caches the result.
type DelegationProvider interface {
	GetBearer(ctx context.Context, scope DelegationScope) (string, error)
}

// Client maintains a pool of E2EE channels keyed by
// (target_worker, user, workspace) so calls with different delegation scopes
// never share a Noise_NK session.
//
// All hub calls (GetWorkerHandshakeParams, OpenChannel, /ws/channel)
// authenticate with a delegation token obtained via DelegationProvider.
type Client struct {
	HubURL     string
	Pins       *PinStore
	Delegation DelegationProvider
	ctx        context.Context
	cancel     context.CancelFunc

	mu       sync.Mutex
	channels map[clientKey]*tunnel.Channel
	// inflight single-flights concurrent opens for the same key: the first
	// caller to miss the cache starts the open and later callers wait on it.
	inflight map[clientKey]*channelOpen
}

type clientKey struct {
	WorkerID    string
	UserID      string
	WorkspaceID string
}

// channelOpen is a single in-flight channel open shared by every caller that
// requested the same (worker, user, workspace) while it runs, so a burst of
// concurrent calls mints one delegation token and dials one Noise_NK handshake
// instead of N (all but one otherwise discarded).
type channelOpen struct {
	done chan struct{}
	ch   *tunnel.Channel
	err  error
}

// New returns a ready-to-use Client.
func New(lifetimeCtx context.Context, hubURL string, pins *PinStore, dp DelegationProvider) *Client {
	if lifetimeCtx == nil {
		panic("crossworker.New: lifetime context is required")
	}
	ctx, cancel := context.WithCancel(lifetimeCtx)
	return &Client{
		HubURL:     hubURL,
		Pins:       pins,
		Delegation: dp,
		ctx:        ctx,
		cancel:     cancel,
		channels:   make(map[clientKey]*tunnel.Channel),
		inflight:   make(map[clientKey]*channelOpen),
	}
}

// channelFor returns a (cached) E2EE channel to targetWorkerID for
// scope.UserID. Mints a fresh delegation token + channel on cache miss.
//
// scope.WorkspaceID is forwarded to the delegation mint call so the
// token's scope matches the eventual call site.
func (c *Client) channelFor(ctx context.Context, targetWorkerID string, scope DelegationScope) (*tunnel.Channel, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if targetWorkerID == "" {
		return nil, errors.New("crossworker: target_worker_id required")
	}
	if scope.UserID == "" {
		return nil, errors.New("crossworker: user_id required")
	}
	if scope.WorkspaceID == "" {
		return nil, errors.New("crossworker: workspace_id required")
	}
	key := clientKey{WorkerID: targetWorkerID, UserID: scope.UserID, WorkspaceID: scope.WorkspaceID}

	c.mu.Lock()
	if existing, ok := c.channels[key]; ok && existing != nil {
		if !existing.Closed() {
			c.mu.Unlock()
			return existing, nil
		}
		// Evict the dead channel now rather than leaving it referenced until a
		// later successful open overwrites it: runChannelOpen only writes
		// c.channels[key] on success, so a persistent open failure would keep the
		// torn-down *tunnel.Channel pinned in the map. Mirrors the desktop
		// TunnelManager.getOrOpenChannel, which deletes a closed entry inline --
		// the two single-flight opener skeletons are structurally duplicated; see
		// https://github.com/leapmux/leapmux/issues/281 for the dedup assessment
		// (a full generic opener is likely the wrong trade -- read before acting).
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

// runChannelOpen performs a single shared channel open and publishes the result
// to every waiter. It caches a successful channel and always clears the
// in-flight marker so the next cache miss starts a fresh open.
func (c *Client) runChannelOpen(open *channelOpen, key clientKey, targetWorkerID string, scope DelegationScope) {
	ch, err := c.openChannel(targetWorkerID, scope)

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
func (c *Client) openChannel(targetWorkerID string, scope DelegationScope) (*tunnel.Channel, error) {
	openCtx, cancel := context.WithTimeout(c.ctx, channelOpenTimeout)
	defer cancel()

	bearer, err := c.Delegation.GetBearer(openCtx, scope)
	if err != nil {
		return nil, fmt.Errorf("delegation token: %w", err)
	}
	openOpts := &tunnel.OpenChannelOptions{
		LifetimeContext: c.ctx,
		BearerToken:     bearer,
		KeyPin:          c.Pins,
		// The pool keys on scope.UserID, so the channel MUST be the identity this
		// scope names. A DelegationProvider that returns a bearer minted for another
		// scope -- a cache keyed on too few fields, a mint response that does not
		// match its request -- would otherwise pool a channel the Hub authenticated
		// as X under the key for Y, and every later CallInner on it would silently
		// run as X with nothing in the stack able to detect it.
		ExpectedUserID: scope.UserID,
	}
	return tunnel.OpenChannel(openCtx, c.HubURL, targetWorkerID, openOpts)
}

// CallInner sends a unary inner RPC to a sibling worker. workspaceID
// is the delegation scope used both for minting the bearer and for
// keying the channel pool — the same `(user, worker)` pair on a
// different workspace gets a separate Noise_NK session.
func (c *Client) CallInner(ctx context.Context, targetWorkerID, userID, workspaceID, method string, payload []byte) ([]byte, error) {
	ch, err := c.channelFor(ctx, targetWorkerID, DelegationScope{UserID: userID, WorkspaceID: workspaceID})
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
// cancelled. workspaceID semantics match CallInner.
func (c *Client) StreamInner(ctx context.Context, targetWorkerID, userID, workspaceID, method string, payload []byte, onMsg func(*leapmuxv1.InnerStreamMessage)) error {
	ch, err := c.channelFor(ctx, targetWorkerID, DelegationScope{UserID: userID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
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
