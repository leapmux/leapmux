package workermgr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/util/nilcheck"
)

// ErrRegistryFenced is returned by Register once the Hub has fenced every
// connection. The worker is not rejected because anything is wrong with it --
// the Hub is going away -- so callers should map it to a retryable status.
var ErrRegistryFenced = errors.New("worker registry fenced: hub is shutting down")

// ErrTooManyWorkers is returned by Register when publishing the connection
// would put more of one account's Workers in the registry -- and therefore in
// the worker send queue pool -- than max_workers_per_user allows. Nothing is
// wrong with the worker or its credentials, and the condition clears the moment
// any of that account's other connections goes away, so callers should map it
// to an exhausted-resource status rather than an authentication failure.
var ErrTooManyWorkers = errors.New("worker connection refused: account is at its live worker limit")

// Manager tracks connected workers. Thread-safe.
type Manager struct {
	mu            sync.RWMutex
	conns         map[string]*Conn // workerID -> Conn
	deregistering map[string]bool  // workerID -> true if deregistering
	// connsByOwner counts the live connections each account holds, so the
	// per-account cap can be tested in the SAME critical section that publishes
	// to conns -- see Register.
	//
	// Not a second source of truth: it is derived from conns, flows one way
	// (Register and Unregister are its only writers, both under mu), and would
	// be rebuilt exactly by walking conns. An emptied bucket is deleted rather
	// than left at zero, so the index cannot grow one entry per account the Hub
	// has ever seen.
	connsByOwner map[string]int // owner -> live connection count
	// fenced latches true the first time every connection is fenced, and never
	// clears: a Manager whose Hub is shutting down must not accept a worker
	// again. Guarded by mu so Register's check and FenceAll's snapshot cannot
	// interleave -- see Register.
	fenced bool
	// maxWorkersPerUser bounds how many live connections one account may hold.
	// Zero (the default) and any negative value are unlimited. Atomic rather
	// than mu-guarded so the startup setter needs no knowledge of the registry's
	// locking; the load that matters happens under mu, with the tally it is
	// compared against. See SetMaxWorkersPerUser.
	maxWorkersPerUser atomic.Int64

	regMu      sync.Mutex
	regWaiters map[string]chan struct{} // regToken -> notify channel

	// reachAuth gates every USER-DIRECTED read of the registry. It is supplied
	// at construction by the component that owns the ownership +
	// delegation-scope rules, because those need the store and this package
	// must not. Immutable after New, so "is this registry gated?" is a fact
	// about the value rather than a runtime state a later caller can change.
	reachAuth ReachAuthorizer
}

// ReachAuthorizer answers "may this user reach this worker".
//
// Making the registry hold this -- rather than trusting each entrypoint to call
// a check first -- is what moves the gate from convention into structure: there
// is no exported accessor that takes a user-supplied worker id and skips it.
type ReachAuthorizer interface {
	AuthorizeWorkerReach(ctx context.Context, user *auth.UserInfo, workerID string) error
}

// ErrReachDenied is the deny a registry with no user-directed reach returns,
// and the deny ConnForUser returns for a nil principal. It is an answer, not a
// fault.
//
// It carries a connect code because requireOnlineWorker forwards it verbatim to
// the RPC boundary, alongside the coded denials the real authorizer returns
// (NotFound / PermissionDenied). A bare error there maps to CodeUnknown, which
// tells a client "something went wrong, try again" about a decision that will
// never change -- so a permanent deny would drive a permanent retry loop.
// errors.Is still matches on identity, so callers testing for the sentinel are
// unaffected.
var ErrReachDenied = connect.NewError(connect.CodePermissionDenied,
	errors.New("workermgr: worker reach is not authorized on this registry"))

// denyAllReach refuses every user-directed reach.
type denyAllReach struct{}

func (denyAllReach) AuthorizeWorkerReach(context.Context, *auth.UserInfo, string) error {
	return ErrReachDenied
}

// DenyAllReach is the authorizer for a registry that serves no user-directed
// reach at all (a relay-only composition, or a test that only exercises
// Register/trusted-path accessors). Naming it keeps the fail-closed intent
// legible and greppable, mirroring auth.DenyAllScope -- and, because New
// requires SOME authorizer, choosing it is deliberate rather than an omission.
func DenyAllReach() ReachAuthorizer { return denyAllReach{} }

// ConnForUser is the ONLY user-directed way to reach a worker connection.
//
// It runs the ReachAuthorizer the Manager was constructed with before touching
// the map, so an entrypoint that takes a worker id off a request cannot read
// the registry without the ownership + delegation-scope check -- previously a
// convention each new entrypoint had to remember. A nil connection with a nil
// error means the worker is not reachable -- authorized but offline, or being
// torn down.
func (m *Manager) ConnForUser(ctx context.Context, user *auth.UserInfo, workerID string) (*Conn, error) {
	// A nil principal is a deny, not a panic. This accessor is the fail-closed
	// gate, so its own degenerate input has to be refused HERE -- every
	// authorizer dereferences user.ID, so passing nil through would crash the
	// request goroutine instead of answering "no".
	if user == nil {
		return nil, ErrReachDenied
	}
	if err := m.reachAuth.AuthorizeWorkerReach(ctx, user, workerID); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	// A worker the operator has deregistered is not reachable by its user, even
	// while its connection is still open.
	//
	// Deregistration is asynchronous: MarkDeregistering runs when the notification
	// is sent, and ClearDeregistering only after the worker ACKS it. Without this
	// check the whole of that window -- unbounded, since an offline worker's
	// notification sits queued until it reconnects -- still handed the user a live
	// conn for a machine being torn down. Deregistering is the operator's
	// containment action; a containment action that leaves the thing reachable
	// until it politely acknowledges is not one.
	//
	// The trusted path is deliberately NOT gated: ConnForTrustedPath is how the
	// deregister notification itself reaches the worker, so gating it would make
	// the teardown unable to complete and the flag permanent.
	if m.deregistering[workerID] {
		return nil, nil
	}
	return m.conns[workerID], nil
}

// New creates a new Manager gated by a. Pass DenyAllReach() for a registry that
// serves no user-directed reach.
//
// The authorizer is required rather than wired afterwards so a hub that forgets
// the gate cannot be built at all: "unwired" is not a reachable state, and two
// components cannot silently repoint one registry's gate at each other.
func New(a ReachAuthorizer) *Manager {
	// nilcheck, not `a == nil`: a nil concrete value converted to the interface
	// is a NON-nil interface value, and it would panic on the first reach
	// instead of being caught at construction -- exactly the failure this
	// constructor exists to make impossible. The shared helper covers every
	// nilable kind; a Pointer-only check would still admit a nil func- or
	// map-typed authorizer, which is an ordinary shape for a policy hook.
	if nilcheck.IsNilDependency(a) {
		panic("workermgr: New requires a ReachAuthorizer (use DenyAllReach() for an ungated-by-design registry)")
	}
	return &Manager{
		conns:         make(map[string]*Conn),
		connsByOwner:  make(map[string]int),
		deregistering: make(map[string]bool),
		regWaiters:    make(map[string]chan struct{}),
		reachAuth:     a,
	}
}

// SetMaxWorkersPerUser bounds how many LIVE worker connections one account may
// hold at once. Zero (the default) is unlimited, and so is a negative value.
// Call once at startup before serving requests.
//
// A setter rather than a constructor argument, mirroring
// auth.AuthContextRegistry.SetMaxConnectionsPerUser: New has call sites all over
// this repo's tests, and a registry built by one of them must come out
// unlimited, which the zero value gives for free.
//
// This is the bound on POOL MEMBERSHIP, and it is not redundant with its twin
// WorkerConnectorService.SetMaxWorkersPerUser, which bounds how many registered
// worker ROWS an account may hold. A row cap cannot bound membership on its own:
// a DEREGISTERING row keeps its Connect stream -- that stream is how the worker
// is told to tear itself down -- while no longer counting against the row cap,
// so register/deregister/register cycles would grow the worker pool without ever
// exceeding it. The row cap stays because it refuses at registration time, where
// the operator can be told which key to raise; this one is what the pool's
// per-member floors are actually multiplied by.
func (m *Manager) SetMaxWorkersPerUser(n int64) {
	m.maxWorkersPerUser.Store(n)
}

// Register adds a worker connection, replacing any existing one, and reports
// whether it replaced one.
//
// A non-nil c.Greeting is enqueued via SendControl BEFORE the connection is
// published. With a single handler drain that makes it mechanically the first
// frame written: until this returns, no other goroutine can look the conn up
// to enqueue after it, so the greeting stays at the head of the queue. The
// Hub greets a worker with its identity (leapmuxv1.WorkerIdentity), which the
// worker needs before the first ChannelOpen creates a session, since every
// machine-scoped handler gates on it. Handing it to Register rather than
// sending it from the caller is what makes that ordering impossible to get
// wrong.
//
// A connection that would take its account past max_workers_per_user is
// refused with ErrTooManyWorkers, fenced, and NOT published -- see
// SetMaxWorkersPerUser for why the bound lives here, on live membership, and
// not only on the registered rows.
//
// A greeting that cannot be enqueued (fenced / closed queue) is returned and
// the conn is NOT published. A stream that cannot carry its greeting on the
// wire is a narrower case: the greeting sits at the head of the queue, the
// conn is published, and the first Drain that fails fences it microseconds
// later. Publishing a connection already known to be broken on the wire is
// accepted because the alternative is a synchronous write on the registration
// path, which is the bug this queue exists to eliminate.
func (m *Manager) Register(c *Conn) (bool, error) {
	// Cheap pre-check so a Hub that is already fencing does not enqueue a
	// greeting onto a stream it is about to refuse -- the worker would
	// otherwise read its identity and only then see the stream end. NOT the
	// authoritative check: the one that closes the race is under the lock
	// below, and this one can go stale the instant it returns.
	if m.isFenced() {
		c.Fence()
		return false, ErrRegistryFenced
	}
	if c.Greeting != nil {
		if err := c.SendControl(c.Greeting); err != nil {
			return false, err
		}
		c.Greeting = nil
	}
	m.mu.Lock()
	// The fenced check and the map write share this critical section, which is
	// what makes the fence TOTAL rather than point-in-time: either Register
	// sees the flag, or FenceAll's snapshot sees this conn. There is no third
	// outcome. Without it a handler that passed the shutdown interceptor before
	// shutdownCh closed -- it is a one-shot check, and a store round trip plus
	// the greeting enqueue sit between it and here -- would publish a connection
	// nothing ever fences, and hold the Hub's drain until workerIdleTimeout.
	if m.fenced {
		m.mu.Unlock()
		c.Fence()
		return false, ErrRegistryFenced
	}
	replaced := m.conns[c.WorkerID]
	// addsMember is false exactly when this connection takes over a slot the
	// same account already holds. A reconnecting Worker replaces its own
	// connection (see the fence below) rather than adding one, so counting it
	// again would refuse a Worker that is already inside the bound -- and
	// permanently, because its predecessor's handler only lets go once this one
	// has taken over.
	//
	// The owner comparison, rather than a bare `replaced == nil`, keeps the
	// tally correct by construction if a worker id ever reappears under a
	// different registrant: the slot moves between buckets instead of being
	// counted in one and released from the other.
	addsMember := replaced == nil || replaced.owner != c.owner
	if addsMember {
		// Tested against connsByOwner and committed by the map write below
		// inside ONE critical section, which is what makes this a bound rather
		// than a suggestion: a check taken before the lock would let every
		// connection in a reconnect burst read the same under-cap count and all
		// of them publish. Same reason the per-user connection cap counts under
		// the lock it already held.
		held := int64(m.connsByOwner[c.owner])
		if limit := m.maxWorkersPerUser.Load(); limit > 0 && held >= limit {
			m.mu.Unlock()
			// Fenced for the reason the fenced path above fences: a refused
			// Register returns before the handler installs its teardown defer,
			// so nothing else closes the queue the greeting was already
			// enqueued onto.
			c.Fence()
			// After the unlock, so a slow log handler cannot hold the registry
			// lock, and at Warn because it is the operator's to act on: either
			// an account is leaking Connect streams or the cap is under what its
			// machines legitimately hold.
			slog.Warn("refusing worker connection: account is at its live worker cap",
				"user_id", c.owner, "worker_id", c.WorkerID, "held", held, "limit", limit)
			return false, fmt.Errorf("%w: %d live workers is the configured maximum "+
				"(max_workers_per_user); disconnect or deregister one first", ErrTooManyWorkers, limit)
		}
		m.connsByOwner[c.owner]++
		if replaced != nil {
			m.releaseOwnerLocked(replaced.owner)
		}
	}
	m.conns[c.WorkerID] = c
	if replaced == nil {
		metrics.ActiveWorkers.Inc()
	}
	m.mu.Unlock()
	if replaced != nil && replaced != c {
		// Fence so a wedged predecessor cannot delay publication of the
		// connection replacing it. The predecessor's own handler reaps the
		// queue -- see Unregister.
		replaced.Fence()
	}
	return replaced != nil, nil
}

// Unregister removes the given worker connection only if it is still the
// registered connection for that workerID. This prevents a stale connection's
// deferred cleanup from accidentally removing a newer replacement connection.
// Returns true if the connection was actually removed.
//
// Unregister does NOT Fence: the Connect handler removes the conn from the
// registry first (so no new Hub sender can look it up), then drains the
// remaining queue while it still owns the stream, then Fences. That order
// closes the window where SendControl could return nil and then be discarded
// by Close. See WorkerConnectorService.Connect.
func (m *Manager) Unregister(workerID string, conn *Conn) bool {
	// A nil conn is refused up front because it MATCHES: reading an absent key
	// out of conns yields nil too, so `m.conns[workerID] == conn` would be true
	// for a worker that was never registered, and the body would then report
	// success, decrement the live-worker gauge, and release a slot for a
	// connection that never existed.
	if conn == nil {
		return false
	}
	m.mu.Lock()
	removed := false
	if m.conns[workerID] == conn {
		delete(m.conns, workerID)
		// Released here and nowhere else: the predicate above is what makes the
		// tally survive replacement. A superseded connection's deferred cleanup
		// reaches this method too, and releasing unconditionally would give its
		// account back a slot the replacement still occupies -- which is the cap
		// leaking one slot per reconnect.
		m.releaseOwnerLocked(conn.owner)
		metrics.ActiveWorkers.Dec()
		removed = true
	}
	m.mu.Unlock()
	return removed
}

// releaseOwnerLocked drops one live connection from owner's tally, deleting the
// bucket once it empties so connsByOwner holds only accounts that currently have
// a connection. Caller holds m.mu.
//
// A count that has already reached zero is left deleted rather than driven
// negative: a negative bucket would silently hand that account extra slots, and
// the failure it would come from (an unbalanced release) is a bug to notice, not
// one to compensate for.
func (m *Manager) releaseOwnerLocked(owner string) {
	remaining := m.connsByOwner[owner] - 1
	if remaining <= 0 {
		delete(m.connsByOwner, owner)
		return
	}
	m.connsByOwner[owner] = remaining
}

// ConnForTrustedPath returns a worker connection by ID for a caller whose
// worker id did NOT come from a user request -- a server-initiated flow
// (notification delivery, revocation teardown) or an already-authorized
// channel record.
//
// It performs no authorization, which is why the name says so. Anything
// holding a user-supplied worker id must use ConnForUser instead.
func (m *Manager) ConnForTrustedPath(workerID string) *Conn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conns[workerID]
}

// OnlineForTrustedPath reports whether a worker is currently connected, for a
// caller whose worker id did not come from a user request. The online/offline
// bit is a cross-tenant liveness oracle when probed with an arbitrary id, so a
// user-supplied id must go through ConnForUser (nil conn == offline) instead.
func (m *Manager) OnlineForTrustedPath(workerID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.conns[workerID]
	return ok
}

// MarkDeregistering marks a worker as being deregistered, which makes it
// unreachable through ConnForUser until the flag is cleared. The trusted path
// stays open so the deregister notification itself can be delivered.
func (m *Manager) MarkDeregistering(workerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deregistering[workerID] = true
}

// IsDeregistering returns true if the worker is in the deregistering state.
func (m *Manager) IsDeregistering(workerID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.deregistering[workerID]
}

// ClearDeregistering removes the deregistering flag for a worker.
func (m *Manager) ClearDeregistering(workerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.deregistering, workerID)
}

// WaitForRegistrationChange blocks until the registration identified by
// regToken is notified, the context is cancelled, or the timeout expires.
// Returns nil on notification, ctx.Err() on cancel, or a timeout error.
func (m *Manager) WaitForRegistrationChange(ctx context.Context, regToken string, timeout time.Duration) error {
	ch := make(chan struct{})

	m.regMu.Lock()
	m.regWaiters[regToken] = ch
	m.regMu.Unlock()

	defer func() {
		m.regMu.Lock()
		delete(m.regWaiters, regToken)
		m.regMu.Unlock()
	}()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(timeout):
		return fmt.Errorf("wait for registration change timed out")
	}
}

// ShutdownNotifyResult tallies what became of one NotifyShutdownAndFence
// broadcast. AlreadyGone is deliberately its own bucket rather than folded into
// Failed: a worker that tore its own connection down a moment before the Hub
// reached it has nothing left to be told, so counting it as a failure would
// report a problem on every ordinary co-shutdown. Delivered + AlreadyGone +
// Failed equals Total only when the deadline did not cut the wait short.
type ShutdownNotifyResult struct {
	Delivered   int
	AlreadyGone int
	Failed      int
	Total       int
}

// record folds one worker's outcome into the tally. It lives next to the struct
// so the enum and the buckets it feeds cannot drift: adding an outcome is a
// compile-time visit here rather than a switch somewhere else that silently
// drops the new case on the floor.
func (r *ShutdownNotifyResult) record(o notifyOutcome) {
	switch o {
	case notifyDelivered:
		r.Delivered++
	case notifyAlreadyGone:
		r.AlreadyGone++
	case notifyFailed:
		r.Failed++
	}
}

// logAttrs is the tally as slog arguments, shared by the completion line and the
// deadline line so the two can never report different fields for the same thing.
func (r *ShutdownNotifyResult) logAttrs() []any {
	return []any{
		"delivered", r.Delivered, "already_gone", r.AlreadyGone,
		"failed", r.Failed, "total", r.Total,
	}
}

// notifyOutcome is one worker's result, kept separate from the log line so the
// classification is a value the caller can assert on rather than log text.
type notifyOutcome int

const (
	notifyDelivered notifyOutcome = iota
	notifyAlreadyGone
	notifyFailed
)

// classifyNotifyErr says whether err means "this worker was already gone" or
// "the Hub could not tell a live worker".
//
// Order matters. ErrControlSaturated must be tested FIRST because SendControl
// fences the conn before returning it (see Conn.SendControl), which closes
// Done() -- so the conn-is-done check below would otherwise file a control
// queue the Hub itself overran as a worker that had left on its own. That is
// exactly backwards: saturation is the one case here that says something is
// wrong on this side.
//
// Every HUB-side cause is therefore tested before any worker-side one, because
// all of them converge on the same observable state -- a closed Done() and an
// ErrConnectionClosed from later sends -- and only the side that caused it knows:
//
//   - ErrControlSaturated: SendControl fences before returning it.
//   - Conn.GaveUp: the queue's give-up callback fenced on a write timeout, a
//     blown budget, or pool pressure. Without this the Hub reclaiming a slow
//     worker reads as that worker having left.
//   - ctx.Err(): the caller's own budget expired, and NotifyShutdownAndFence
//     fences everything on its way out -- so past this point Done() is a channel
//     the Hub closed itself and says nothing about the worker.
//
// Only then does a closed Done() mean what it looks like. It covers the case the
// error alone cannot name: Flush surfaces the CONN's context error verbatim
// (sendq.Writer.Flush selects on it), so a worker whose stream died
// mid-notification reports a bare context.Canceled.
func classifyNotifyErr(ctx context.Context, conn *Conn, err error) notifyOutcome {
	switch {
	case err == nil:
		return notifyDelivered
	case errors.Is(err, ErrControlSaturated), conn.GaveUp(), ctx.Err() != nil:
		return notifyFailed
	case errors.Is(err, ErrConnectionClosed):
		return notifyAlreadyGone
	default:
		select {
		case <-conn.Done():
			return notifyAlreadyGone
		default:
			return notifyFailed
		}
	}
}

// NotifyShutdownAndFence tells every connected worker the Hub is going down,
// then fences them all on the way out -- deadline path included.
//
// The name carries both effects because both are load-bearing and the order
// between them is the whole point: the notification has to be on the wire
// first, or a worker reconnects on its ordinary backoff instead of the delay
// the Hub asked for. Each per-conn goroutine does SendControl THEN Flush;
// success means the frame reached the transport before the deadline. Flush
// never writes, so this adds no second writer alongside the handler pump. Its
// only escape besides a drain is the caller's deadline or a fence -- which is
// correct in production (server.go bounds it) and means every test MUST pass
// a bounded context. Delivery is best-effort; errors are logged but do not
// abort the sequence. Fencing is not optional -- see FenceAll.
//
// The returned tally is the seam tests assert on: the per-worker classification
// is otherwise observable only through the default logger.
func (m *Manager) NotifyShutdownAndFence(ctx context.Context, retryDelaySeconds int32) ShutdownNotifyResult {
	connections := m.snapshotConns()

	// done carries each worker's classified outcome so the tally reflects
	// notifications that reached the wire, not merely queued.
	done := make(chan notifyOutcome, len(connections))
	for _, conn := range connections {
		go func() {
			err := conn.SendControl(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_HubShuttingDown{
					HubShuttingDown: &leapmuxv1.HubShuttingDownNotification{
						RetryDelaySeconds: retryDelaySeconds,
					},
				},
			})
			if err == nil {
				err = conn.Flush(ctx)
			}
			outcome := classifyNotifyErr(ctx, conn, err)
			switch outcome {
			case notifyAlreadyGone:
				// Not a warning: the worker left before the Hub could speak, and
				// the only thing the notification carries is a reconnect delay
				// that a departed worker has no use for.
				slog.Debug("worker disconnected before the shutdown notification",
					"worker_id", conn.WorkerID, "error", err)
			case notifyFailed:
				slog.Warn("failed to send shutdown notification to worker", "worker_id", conn.WorkerID, "error", err)
			case notifyDelivered:
			}
			done <- outcome
		}()
	}

	// Deferred so it runs on EVERY exit, deadline included: a worker that could
	// not be notified inside the budget is exactly the one that would otherwise
	// hold the drain open for the full idle timeout.
	defer m.FenceAll()

	result := ShutdownNotifyResult{Total: len(connections)}
	completed := 0
	for completed < len(connections) {
		select {
		case outcome := <-done:
			completed++
			result.record(outcome)
		case <-ctx.Done():
			slog.Warn("worker shutdown notification deadline reached", result.logAttrs()...)
			return result
		}
	}
	// A Hub that reaches shutdown with nothing connected has nothing to report;
	// an ordered solo teardown makes that the NORMAL case, and "count 0 of 0" is
	// precisely the non-event this log should not manufacture.
	if result.Total > 0 {
		slog.Info("sent shutdown notifications to workers", result.logAttrs()...)
	}
	return result
}

// isFenced reports whether FenceAll has latched the registry closed.
func (m *Manager) isFenced() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fenced
}

// snapshotConns copies the live connections so the caller can do blocking work
// on them without holding the registry lock. Both broadcasts need that: Flush
// parks until the drain hands frames to the transport, and Fence wakes a
// handler whose defer takes the WRITE lock -- so fencing under the read lock
// would make the Hub's own shutdown the thing that wedges it.
func (m *Manager) snapshotConns() []*Conn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connsLocked()
}

// connsLocked copies the connection map into a slice. Callers hold m.mu.
// Conn.WorkerID carries the key, so the slice loses nothing the map had.
func (m *Manager) connsLocked() []*Conn {
	conns := make([]*Conn, 0, len(m.conns))
	for _, conn := range m.conns {
		conns = append(conns, conn)
	}
	return conns
}

// FenceAll cancels every registered worker connection, ending its Connect
// handler, and latches the registry closed so a later Register is refused
// rather than published behind the fence. NotifyShutdownAndFence defers it, so
// every worker the notification pass saw learns to delay its reconnect first.
//
// The Hub cannot leave these streams to the workers to close. A worker keeps
// its stream open until it decides to reconnect, and the Hub only learns it is
// gone from a frame or from workerIdleTimeout; meanwhile an
// unencrypted-HTTP/2 connection counts as ACTIVE for its whole life (net/http
// marks it so in maybeServeUnencryptedHTTP2), so http.Server.Shutdown's drain
// waits on the handler holding it. A remote worker that is wedged, or merely
// polite, would therefore spend the Hub's entire shutdown budget -- and past it
// the drain reports a deadline the operator sees as a failed shutdown.
//
// One residual remains: a handler parked inside its OWN drain write is still
// freed only by server.Close() after the drain deadline. connWriteTimeout
// bounds the queue (give up, discard, fence) but cannot unpark the handler --
// connect.BidiStream.Send has no context and bottoms out in
// http.ResponseWriter.Write; cancelling connCtx does not interrupt an
// in-flight HTTP/2 DATA write blocked on peer flow control. Spawning a
// second Write against ctx.Done would race the transport. The cross-sender
// pile-up, the deadline-ignoring block, and the Unregister deadlock that
// motivated https://github.com/leapmux/leapmux/issues/344 are gone.
func (m *Manager) FenceAll() {
	m.mu.Lock()
	m.fenced = true
	conns := m.connsLocked()
	m.mu.Unlock()

	for _, conn := range conns {
		conn.Fence()
	}
}

// NotifyRegistrationChange wakes up any waiter blocked on the given regToken.
func (m *Manager) NotifyRegistrationChange(regToken string) {
	m.regMu.Lock()
	defer m.regMu.Unlock()

	if ch, ok := m.regWaiters[regToken]; ok {
		close(ch)
		delete(m.regWaiters, regToken)
	}
}
