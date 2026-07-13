package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/leapmux/leapmux/util/ctxutil"
)

const (
	// relayDialTimeout bounds a relay WebSocket handshake. The distributed hub
	// path has no other dial deadline, so without this a non-responsive hub
	// could hold the relay open path (and, before the lock was released across
	// the dial, all lifecycle readers) indefinitely.
	relayDialTimeout = 10 * time.Second
)

// relayDrainTimeout bounds closeChannelRelay/closeOrgEventsRelay's wait for the
// read loop to exit. The loop's in-flight emit can block on a full shell pipe;
// bounding the wait prevents a permanently stalled peer from deadlocking every
// lifecycle operation behind lifecycleMu. It is a var so tests can shorten it.
var relayDrainTimeout time.Duration = 5 * time.Second

// wsRelay is the shared lifecycle core of the channel and org-events relays: a
// single WebSocket bound to a lifetime context, a done channel closed when the
// read loop exits, and a bounded shutdown that does not wedge lifecycleMu when
// the peer stops draining the shell pipe. Subtypes embed it and supply their
// own read loop, which emits the relay-specific events.
type wsRelay struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	emit   func(*desktoppb.Event)
	// owner is the frontend relay wrapper this relay was last handed to.
	//
	// It exists because a close request cannot be trusted to mean "close the relay
	// that is here now": the frontend dispatches open and close as separate RPCs and
	// RPCSession runs every request on its own goroutine with no ordering, so a close
	// sent for wrapper A can execute after a later open handed the relay to wrapper
	// B. Closing then tears down the relay B just took -- and because shutdown cancels
	// the relay context before the read loop can emit, no close event ever reaches the
	// frontend: the channel relay sits at readyState OPEN forever with every send
	// failing, and the org-events page sits bootstrapped on a dead relay with nothing
	// reopening it until a reload.
	//
	// Comparing the id makes a stale close a no-op regardless of how the goroutines
	// are scheduled, which is the only way to get ordering here: the JS side's
	// wrapper-id claim orders the DISPATCH of the two requests but has no say over
	// when the sidecar runs them.
	//
	// It lives on the relay, not on desktopConnection, so ownership is a property of
	// "a relay has an owner" -- both relays get the same fence (closeIfOwner), and the
	// id cannot outlive the relay it named.
	owner uint64
}

// newWSRelay constructs the shared wsRelay core both ChannelRelay and
// OrgEventsRelay embed, wiring the per-relay lifetime context + cancel the
// dial-and-install core obtained and the done channel the read loop closes on
// exit. Centralising the literal keeps the five-field initialization (and the
// make(chan struct{}) the done channel needs) at one site, so a sixth field or
// a changed constructor lands once instead of drifting between the two relays'
// commit closures.
func newWSRelay(ws *websocket.Conn, ctx context.Context, cancel context.CancelFunc, emit func(*desktoppb.Event)) wsRelay {
	return wsRelay{ws: ws, ctx: ctx, cancel: cancel, done: make(chan struct{}), emit: emit}
}

// closeIfOwner runs closeRelay only while relayID still owns relay, returning the
// done channel it produced (nil when the close was stale) so the caller can drain
// it OUTSIDE lifecycleMu. Both exported closers route through it so the stale-close
// rule is defined once for every relay (see wsRelay.owner). Caller holds lifecycleMu
// for writing.
func closeIfOwner(relay *wsRelay, relayID uint64, closeRelay func() <-chan struct{}) <-chan struct{} {
	if relay.owner != relayID {
		// Stale: the relay has already been handed to someone else. Not an error --
		// the caller's intent, "my relay should be closed", is already satisfied.
		return nil
	}
	return closeRelay()
}

// run signals completion via done when readLoop returns. Subtypes call it from
// their runReadLoop: r.run(r.readLoop).
func (r *wsRelay) run(readLoop func()) {
	defer close(r.done)
	readLoop()
}

// detach cancels the relay's lifetime and force-closes its socket, then returns the
// done channel to drain WITHOUT waiting. Splitting the fast teardown (cancel +
// CloseNow) from the bounded drain (drainRelay) lets a caller hold lifecycleMu only
// across the detach and drain AFTER releasing it: the read loop's in-flight emit (a
// synchronous write to the shell pipe) can block when the peer stops draining, and
// holding the write lock across that drain would freeze every
// SidecarInfo/ProxyHTTP/SendChannelMessage reader for relayDrainTimeout. cancel and
// CloseNow are idempotent. Caller holds lifecycleMu for writing.
func (r *wsRelay) detach() <-chan struct{} {
	r.cancel()
	_ = r.ws.CloseNow()
	return r.done
}

// drainRelay waits, bounded by relayDrainTimeout, for a detached relay's read loop
// to exit. Call it AFTER releasing lifecycleMu (see wsRelay.detach). A nil done
// means no relay was installed, so it is a no-op. The bound keeps a permanently
// stalled emit (a peer that stopped draining the shell pipe) from hanging the close.
func drainRelay(done <-chan struct{}) {
	if done == nil {
		return
	}
	waitBounded(done, relayDrainTimeout, "relay read loop did not drain during close")
}

// emitClose cancels the relay's lifetime, then emits a close event built from
// the read error's WebSocket close details. It is the shared terminal sequence of
// BOTH read loops, so the "cancel before emit" invariant the wsRelay core
// promises for both relays lives once here rather than copy-pasted into each
// subtype's read loop -- a future third relay, or a fix to the cancel/emit order,
// cannot land in one loop and silently miss the other.
//
// Cancelling BEFORE the emit is load-bearing: a concurrent adopt that gates on
// ctx.Err()==nil must not adopt a relay whose read loop has already failed, and
// the emit can block on a full shell pipe (the Rust reader thread not draining),
// widening that window unless the cancel precedes it. Each read loop's deferred
// r.cancel() is idempotent, and the emit writes to the shell via EmitEvent,
// independent of r.ctx. build turns the close details into the relay-specific
// Event payload -- the ONLY part that differs between the two relays.
func (r *wsRelay) emitClose(err error, build func(code uint32, reason string, wasClean bool) *desktoppb.Event) {
	r.cancel()
	code, reason, wasClean := channelwire.WebSocketCloseDetails(err)
	r.emit(build(code, reason, wasClean))
}

// relayOpenSpec bundles openRelay's per-relay policy closures under named
// fields, so a call-site transposition of two same-typed closures -- which the
// compiler cannot catch positionally -- is unrepresentable.
//
// All fields are invoked under the write lock (except dial, which runs
// unlocked):
type relayOpenSpec struct {
	// policy is the relay-specific decision about the relay that is ALREADY
	// installed -- adopt it (handled=true: openRelay stops and returns nil),
	// refuse a superseded open (err), or proceed (false, nil). It runs TWICE:
	// once before the pre-dial teardown, and again at install, because
	// whatever it decided before the dial may have been overtaken while the
	// lock was released. One field rather than a pre/install pair: both
	// relays pass the same decision at both points, so a second field only
	// re-created the transposition hazard the named fields exist to remove.
	// Per-relay argument checks live in the caller, before openRelay. On a
	// post-dial handled/err, openRelay cancels the lifetime and closes the
	// freshly dialed socket.
	policy func(connection *desktopConnection) (handled bool, err error)
	// closePrior tears down the prior relay. Called before the dial and
	// again at install (a concurrent open may have installed a fresh relay
	// while the lock was released). It DETACHES the prior relay (cancel +
	// force-close) without draining its read loop: openRelay holds the write
	// lock across it, so a bounded drain here would reintroduce the reader
	// freeze detach exists to avoid. The superseded relay's read loop
	// self-cleans; its socket is already force-closed.
	closePrior func()
	// dial opens the WebSocket and wraps its own error; it owns URL,
	// options, and read limits.
	dial func(dialCtx context.Context, proxy *HubProxy) (*websocket.Conn, error)
	// commit builds the relay around the dialed socket, stamps its owner
	// BEFORE installing (so no close can ever observe it unowned), launches
	// the read loop, and assigns the connection field.
	commit func(connection *desktopConnection, ws *websocket.Conn, ctx context.Context, cancel context.CancelFunc)
}

// openRelay is the shared open sequence of the channel and org-events relays.
// The scaffolding it owns is exactly the part whose invariants are easy to fix
// in one relay and silently miss in the other:
//
//   - the write lock is RELEASED across the dial: holding lifecycleMu across
//     websocket.Dial would block every SidecarInfo/ProxyHTTP (read lock) for
//     the handshake, and the distributed hub path has no dial deadline of its
//     own;
//   - the dial is bounded by relayDialTimeout and bridged to the caller's
//     request context only for the dial's duration (cancelBridge);
//   - every abandon path after the lifetime context is created cancels it, and
//     every abandon path after the dial succeeds also closes the socket, so an
//     abandoned open leaks neither a context, nor a goroutine parked on one,
//     nor a WebSocket;
//   - after the dial, the connection is re-acquired and re-checked
//     (reacquireConnectionForInstall), so an open that raced a mode transition
//     abandons itself instead of installing onto a dead or replaced
//     connection, and the install policy runs AGAIN -- whatever it decided
//     before the dial may have been overtaken while the lock was released.
//
// What genuinely differs per relay arrives via spec (see relayOpenSpec).
func (a *App) openRelay(requestCtx context.Context, spec relayOpenSpec) error {
	done, err := a.beginOperation()
	if err != nil {
		return err
	}
	defer done()

	// Decide and tear down any prior relay under the write lock, then RELEASE
	// it for the dial (see the doc above).
	unlock, err := a.acquireLifecycleLock()
	if err != nil {
		return err
	}
	connection := a.connection
	if connection == nil {
		unlock()
		return fmt.Errorf("not connected")
	}
	if handled, perr := spec.policy(connection); handled || perr != nil {
		unlock()
		return perr
	}
	spec.closePrior()
	baseCtx := connection.ctx
	proxy := connection.proxy
	unlock()

	ctx, cancel := context.WithCancel(baseCtx)
	cancelCtx, cancelBridge := ctxutil.WithLinkedCancel(ctx, requestCtx)
	defer cancelBridge() // cancelCtx only bridges the request context during the dial
	dialCtx, cancelDial := context.WithTimeout(cancelCtx, relayDialTimeout)
	defer cancelDial() // released after the post-dial Err() check
	ws, err := spec.dial(dialCtx, proxy)
	if err != nil {
		cancel()
		return err
	}
	if err := dialCtx.Err(); err != nil {
		cancel()
		_ = ws.CloseNow()
		return err
	}

	// Re-acquire the write lock to install. Abandon if a transition tore the
	// connection down while we dialed. On success unlock holds the write lock
	// this function now owes.
	unlock, err = a.reacquireConnectionForInstall(connection)
	if err != nil {
		cancel()
		_ = ws.CloseNow()
		return err
	}
	defer unlock()
	// Re-run the policy: whatever it decided before the dial may have been
	// overtaken while the lock was released.
	if handled, err := spec.policy(connection); handled || err != nil {
		cancel()
		_ = ws.CloseNow()
		return err
	}
	spec.closePrior()
	spec.commit(connection, ws, ctx, cancel)
	return nil
}

// dialChannelRelay opens the channel WebSocket. A package var (mirroring
// dialOrgEvents) so tests can hold a dial open and drive the adopt-vs-supersede
// fence deterministically -- the concurrent-open window is otherwise a race no
// test could pin down.
var dialChannelRelay = func(ctx context.Context, proxy *HubProxy) (*websocket.Conn, error) {
	wsURL := channelwire.HTTPToWS(proxy.baseURL) + "/ws/channel"
	// Fail closed on a missing WS client (see HubProxy.requireWSClient): a nil
	// wsClient makes websocket.Dial fall back to http.DefaultClient, which
	// carries neither the cookie jar nor pinRedirectsToOrigin and would let a
	// hub-side off-origin 3xx on /ws/channel lead the CORS-free desktop process
	// off-origin.
	if err := proxy.requireWSClient("channel relay"); err != nil {
		return nil, err
	}
	opts := &websocket.DialOptions{
		Subprotocols: []string{"channel-relay"},
		HTTPHeader:   proxy.cookieHeader(),
		HTTPClient:   proxy.wsClient,
	}
	ws, _, err := websocket.Dial(ctx, wsURL, opts)
	if err != nil {
		return nil, fmt.Errorf("connect to channel relay: %w", err)
	}
	ws.SetReadLimit(channelwire.WSReadLimit)
	return ws, nil
}

// ChannelRelay bridges WebSocket channel relay traffic between the sidecar
// and the Tauri shell.
type ChannelRelay struct {
	wsRelay
	mu sync.Mutex
}

// OpenChannelRelay establishes (or adopts) the channel relay for relayID, the
// frontend wrapper making the request. See desktopConnection.relayOwner for why the
// id travels with the request.
func (a *App) OpenChannelRelay(requestCtx context.Context, relayID uint64) error {
	// Reuse an existing healthy relay -- both before the dial and again at
	// install, where a concurrent open may have beaten us to it. The sidecar
	// persists across dev refreshes, so the frontend's reconnect attempt would
	// otherwise tear down the hub-side binding and trigger a cleanup race that
	// wipes channels the freshly-loaded page is about to use.
	//
	// Adopting it makes this caller the owner ONLY when this open is at least as
	// new as the relay's current owner: a stale open (an earlier wrapper whose
	// request the sidecar ran late) must abandon itself rather than stealing
	// ownership back -- the same fence OpenOrgEventsRelay's rejectIfSuperseded
	// applies. Without it a slow open from wrapper 1 re-stamps owner=1 after
	// wrapper 2 already committed, and wrapper 1's later Close then tears the
	// relay down out from under the active wrapper 2.
	adoptLiveRelay := func(connection *desktopConnection) (handled bool, err error) {
		current := connection.relay
		if current == nil || current.ctx.Err() != nil {
			return false, nil
		}
		if current.owner > relayID {
			return false, fmt.Errorf("channel relay superseded by a newer open")
		}
		current.owner = relayID
		return true, nil
	}
	return a.openRelay(requestCtx, relayOpenSpec{
		policy:     adoptLiveRelay,
		closePrior: func() { _ = a.closeChannelRelay() }, // detach without draining under the lock
		dial:       dialChannelRelay,
		commit: func(connection *desktopConnection, ws *websocket.Conn, ctx context.Context, cancel context.CancelFunc) {
			relay := &ChannelRelay{
				wsRelay: newWSRelay(ws, ctx, cancel, a.EmitEvent),
			}
			// Every path that tells a caller "you have a relay" records it as the
			// owner (adoptLiveRelay stamps the adopt paths), so ownership always
			// names whoever the frontend last handed the relay to -- which is
			// exactly who a legitimate close can come from. Stamped before the
			// relay is installed, so no close can ever observe it unowned.
			relay.owner = relayID
			// Route the read loop's emits through the relay-aware sink so an
			// undeliverable frame carries this relay's owner id forward to the
			// close path (the closure reads relay.owner at call time; it is
			// stable once stamped above).
			relay.emit = a.emitForOwner(&relay.wsRelay)
			go relay.runReadLoop()
			connection.relay = relay
		},
	})
}

func (a *App) SendChannelMessage(requestCtx context.Context, data []byte) error {
	done, err := a.beginOperation()
	if err != nil {
		return err
	}
	defer done()
	unlock, err := a.acquireLifecycleRLock()
	if err != nil {
		return err
	}
	connection := a.connection
	if connection == nil || connection.relay == nil {
		unlock()
		return fmt.Errorf("channel relay not open")
	}
	relay := connection.relay
	unlock()

	// Gate entry on the request context, then write under the relay's LIFETIME
	// context. coder/websocket force-closes the underlying connection for any
	// cancellable context passed to Write that cancels -- it registers
	// context.AfterFunc(ctx, c.close) -- so binding the write to the per-RPC
	// requestCtx would let one caller's cancel or an expired deadline tear down
	// the shared relay for every subscriber. This mirrors the E2EE channel
	// (tunnel.Channel.sendInnerContext), which writes under the channel lifetime
	// for the same reason. A write blocked on a slow peer is bounded by the read
	// loop, which cancels relay.ctx once the relay stops reading.
	if err := requestCtx.Err(); err != nil {
		return err
	}
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if err := relay.ctx.Err(); err != nil {
		return fmt.Errorf("channel relay closed: %w", err)
	}
	if err := relay.ws.Write(relay.ctx, websocket.MessageBinary, data); err != nil {
		// A write failure is terminal for a WebSocket, and coder/websocket does NOT
		// force-close the conn on a data-frame write error. Close the socket so the
		// parked read loop unblocks into its emit path and delivers channel:close to
		// the frontend, which re-resolves. Otherwise a half-open connection (writes
		// error while the read stays blocked with no inbound frame to fail on) leaves
		// the relay at readyState OPEN with every later send -- including a fresh
		// channel's Noise handshake -- failing silently until a manual reload. The
		// E2EE channel tears down on the same signal (tunnel.Channel.sendInnerContext).
		// CloseNow, not r.cancel(): cancelling the lifetime makes the read loop return
		// WITHOUT emitting (the stale-close gap the owner fence guards against), so the
		// frontend would never learn the relay died.
		_ = relay.ws.CloseNow()
		return err
	}
	return nil
}

// CloseChannelRelay tears down the relay IF relayID still owns it.
//
// A close names the relay it means, because it cannot rely on arriving before a
// successor's open: RPCSession runs every request on its own goroutine with no
// ordering, so this can execute after a later OpenChannelRelay handed the relay to
// another wrapper. Closing that relay would wedge the successor silently -- the
// teardown cancels the relay context before the read loop can emit, so no
// channel:close ever reaches the frontend and the socket stays OPEN forever with
// every send failing. Ignoring a stale close makes the outcome independent of how
// the sidecar happens to schedule the two requests.
func (a *App) CloseChannelRelay(relayID uint64) error {
	return a.closeRelayIfOwner(relayID, func(c *desktopConnection) *wsRelay {
		if c.relay == nil {
			return nil
		}
		return &c.relay.wsRelay
	}, a.closeChannelRelay)
}

// closeRelayIfOwner is the shared body of CloseChannelRelay and
// CloseOrgEventsRelay: begin an operation, detach the relay under lifecycleMu
// only while relayID still owns it, then drain OUTSIDE the write lock -- the
// detach (cancel + force-close) is fast and safe under the lock, but the read
// loop's wedged emit can take up to relayDrainTimeout to unblock, and waiting
// for it under lifecycleMu would freeze every SidecarInfo/ProxyHTTP/
// SendChannelMessage reader for that window. The open path already routes both
// relays through one dial-and-install core (openRelay); this is the close
// side's counterpart, so the ownership fence and the drain-off-the-lock rule
// land once. getRelay picks the relay slot this close names (nil when none is
// installed); closeRelay is the internal per-relay teardown.
func (a *App) closeRelayIfOwner(relayID uint64, getRelay func(*desktopConnection) *wsRelay, closeRelay func() <-chan struct{}) error {
	opDone, err := a.beginOperation()
	if err != nil {
		return err
	}
	defer opDone()
	a.lifecycleMu.Lock()
	var drain <-chan struct{}
	if a.connection != nil {
		if relay := getRelay(a.connection); relay != nil {
			drain = closeIfOwner(relay, relayID, closeRelay)
		}
	}
	a.lifecycleMu.Unlock()
	drainRelay(drain)
	return nil
}

// closeChannelRelay detaches the current relay (cancel + force-close its socket) and
// clears the connection slot, returning the done channel for the caller to drainRelay
// AFTER releasing lifecycleMu (nil if no relay was installed). It is the internal
// teardown used by the open paths (replacing a dead relay) and by connection
// teardown; the ownership fence lives in the exported CloseChannelRelay, which is the
// only entry a frontend wrapper can reach.
func (a *App) closeChannelRelay() <-chan struct{} {
	// Caller holds a.lifecycleMu for writing.
	connection := a.connection
	if connection == nil || connection.relay == nil {
		return nil
	}
	done := connection.relay.detach()
	// Ownership is dropped with the relay it named: the id lives on the relay itself,
	// so a close from that owner cannot reach a LATER relay -- the exact confusion the
	// id exists to prevent.
	connection.relay = nil
	return done
}

func (r *ChannelRelay) runReadLoop() { r.run(r.readLoop) }

func (r *ChannelRelay) readLoop() {
	defer r.cancel()
	for {
		_, data, err := r.ws.Read(r.ctx)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			slog.Debug("channel relay read error", "error", err)
			// emitClose cancels before emitting; see wsRelay.emitClose for why the
			// order matters (a concurrent adoptLiveRelay gates on ctx.Err()==nil).
			r.emitClose(err, func(code uint32, reason string, wasClean bool) *desktoppb.Event {
				return &desktoppb.Event{
					Payload: &desktoppb.Event_ChannelClose{
						ChannelClose: &desktoppb.ChannelCloseEvent{
							Code:     code,
							Reason:   reason,
							WasClean: wasClean,
						},
					},
				}
			})
			return
		}

		r.emit(&desktoppb.Event{
			Payload: &desktoppb.Event_ChannelMessage{
				ChannelMessage: &desktoppb.ChannelMessageEvent{
					Data: data,
				},
			},
		})
	}
}
