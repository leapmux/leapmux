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

// Conn represents a connected worker's bidirectional stream.
type Conn struct {
	WorkerID       string
	EncryptionMode leapmuxv1.EncryptionMode // Set from the initial heartbeat.
	Stream         *connect.BidiStream[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse]
	SendFn         func(*leapmuxv1.ConnectResponse) error // Optional: overrides Stream.Send for testing.
	Cancel         context.CancelFunc

	// Greeting, when non-nil, is sent by Register BEFORE the connection is
	// published -- so it is guaranteed to reach the worker ahead of anything any
	// other goroutine can send, because until Register returns nothing else can find
	// this conn to send on.
	//
	// It is DATA on the conn rather than a call the caller sequences itself because
	// that ordering is the whole value: the Hub greets a worker with its identity
	// (leapmuxv1.WorkerIdentity), which the worker needs before the first ChannelOpen
	// creates a session, since every machine-scoped handler gates on it. A caller that
	// sent the greeting itself would have to remember to do so before Register, and a
	// later edit reordering the two lines would turn a permanent, obvious outage into
	// an intermittent race -- strictly worse. Here it cannot be reordered.
	Greeting *leapmuxv1.ConnectResponse

	mu     sync.Mutex
	closed atomic.Bool
}

// ErrConnectionClosed is returned when a sender races worker disconnect.
var ErrConnectionClosed = errors.New("worker connection closed")

// ErrRegistryFenced is returned by Register once the Hub has fenced every
// connection. The worker is not rejected because anything is wrong with it --
// the Hub is going away -- so callers should map it to a retryable status.
var ErrRegistryFenced = errors.New("worker registry fenced: hub is shutting down")

// Send sends a message to the worker via the bidi stream.
// The mutex serializes writes to prevent concurrent HTTP/2 frame corruption.
//
// It is held across the wire write, so a worker that stalls its HTTP/2
// flow-control window blocks every other sender to it until the process exits,
// and Send honours no caller deadline because it takes no context. Replacing
// this with a handler-owned send queue is tracked in
// https://github.com/leapmux/leapmux/issues/344
func (c *Conn) Send(msg *leapmuxv1.ConnectResponse) error {
	if c.closed.Load() {
		return ErrConnectionClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return ErrConnectionClosed
	}

	if c.SendFn != nil {
		return c.SendFn(msg)
	}
	if c.Stream == nil {
		return fmt.Errorf("stream is nil")
	}
	return c.Stream.Send(msg)
}

// Close prevents new sends and waits for any in-flight send to finish. Worker
// handlers call this before returning so background senders cannot retain and
// write through a completed Connect stream.
//
// Fence stops sends that have not started. The LOCK is what waits for one
// already inside Stream.Send, which holds c.mu for the whole write -- so this
// critical section is the barrier, not the flag it sets. Storing closed again
// under the lock is idempotent (nothing ever clears it) and kept only because
// an empty critical section reads like a mistake; deleting the lock would
// silently delete the wait.
func (c *Conn) Close() {
	c.Fence()
	c.mu.Lock()
	c.closed.Store(true)
	c.mu.Unlock()
}

// Fence rejects future sends and cancels the connection handler without
// waiting for a send already in progress. Manager replacement uses this so a
// wedged old stream cannot delay publication of its successor.
func (c *Conn) Fence() {
	c.closed.Store(true)
	if c.Cancel != nil {
		c.Cancel()
	}
}

// Manager tracks connected workers. Thread-safe.
type Manager struct {
	mu            sync.RWMutex
	conns         map[string]*Conn // workerID -> Conn
	deregistering map[string]bool  // workerID -> true if deregistering
	// fenced latches true the first time every connection is fenced, and never
	// clears: a Manager whose Hub is shutting down must not accept a worker
	// again. Guarded by mu so Register's check and FenceAll's snapshot cannot
	// interleave -- see Register.
	fenced bool

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
		deregistering: make(map[string]bool),
		regWaiters:    make(map[string]chan struct{}),
		reachAuth:     a,
	}
}

// Register adds a worker connection, replacing any existing one, and reports
// whether it replaced one.
//
// A non-nil c.Greeting is sent FIRST, before the connection is published. That
// ordering is the point: until this returns, no other goroutine can look the conn up
// to send on it, so the greeting is mechanically the worker's first message. A failed
// greeting is returned and the conn is NOT published -- a stream that cannot carry
// its greeting cannot carry a channel either, and publishing it would advertise a
// worker as reachable on a connection already known to be broken.
func (m *Manager) Register(c *Conn) (bool, error) {
	// Cheap pre-check so a Hub that is already fencing does not write a
	// greeting onto a stream it is about to refuse -- the worker would
	// otherwise read its identity and only then see the stream end. NOT the
	// authoritative check: the one that closes the race is under the lock
	// below, and this one can go stale the instant it returns.
	if m.isFenced() {
		c.Fence()
		return false, ErrRegistryFenced
	}
	if c.Greeting != nil {
		if err := c.Send(c.Greeting); err != nil {
			return false, err
		}
	}
	m.mu.Lock()
	// The fenced check and the map write share this critical section, which is
	// what makes the fence TOTAL rather than point-in-time: either Register
	// sees the flag, or FenceAll's snapshot sees this conn. There is no third
	// outcome. Without it a handler that passed the shutdown interceptor before
	// shutdownCh closed -- it is a one-shot check, and a store round trip plus
	// the greeting write sit between it and here -- would publish a connection
	// nothing ever fences, and hold the Hub's drain until workerIdleTimeout.
	if m.fenced {
		m.mu.Unlock()
		c.Fence()
		return false, ErrRegistryFenced
	}
	replaced := m.conns[c.WorkerID]
	m.conns[c.WorkerID] = c
	if replaced == nil {
		metrics.ActiveWorkers.Inc()
	}
	m.mu.Unlock()
	if replaced != nil && replaced != c {
		// Fence, not Close: this runs on the SUCCESSOR's goroutine, and a wedged
		// predecessor must not delay publication of the connection replacing it.
		// The predecessor's own handler does the waiting -- see Unregister.
		replaced.Fence()
	}
	return replaced != nil, nil
}

// Unregister removes the given worker connection only if it is still the
// registered connection for that workerID. This prevents a stale connection's
// deferred cleanup from accidentally removing a newer replacement connection.
// Returns true if the connection was actually removed.
//
// Close runs on BOTH paths, replaced or not. The caller is the connection's own
// handler, about to return, and Close is what waits for a send already inside
// Stream.Send. Returning while one is in flight hands net/http a write against
// a finished handler, which panics ("Write called after Handler finished") on
// the sender's goroutine -- and, worse, the response-writer state has by then
// been recycled into a pool. A conn that lost its slot to a replacement is
// exactly the one likely to have a wedged send outstanding, so it is the last
// one that may skip the wait.
func (m *Manager) Unregister(workerID string, conn *Conn) bool {
	m.mu.Lock()
	removed := false
	if m.conns[workerID] == conn {
		delete(m.conns, workerID)
		metrics.ActiveWorkers.Dec()
		removed = true
	}
	m.mu.Unlock()
	conn.Close()
	return removed
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

// NotifyShutdownAndFence tells every connected worker the Hub is going down,
// then fences them all on the way out -- deadline path included.
//
// The name carries both effects because both are load-bearing and the order
// between them is the whole point: the notification has to be on the wire
// first, or a worker reconnects on its ordinary backoff instead of the delay
// the Hub asked for. Delivery is best-effort; errors are logged but do not
// abort the sequence. Fencing is not optional -- see FenceAll.
func (m *Manager) NotifyShutdownAndFence(ctx context.Context, retryDelaySeconds int32) {
	connections := m.snapshotConns()

	// done carries per-worker delivery success so the completion tally reflects
	// notifications that were actually sent, not merely attempted.
	done := make(chan bool, len(connections))
	for _, conn := range connections {
		go func() {
			err := conn.Send(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_HubShuttingDown{
					HubShuttingDown: &leapmuxv1.HubShuttingDownNotification{
						RetryDelaySeconds: retryDelaySeconds,
					},
				},
			})
			if err != nil {
				slog.Warn("failed to send shutdown notification to worker", "worker_id", conn.WorkerID, "error", err)
			}
			done <- err == nil
		}()
	}

	// Deferred so it runs on EVERY exit, deadline included: a worker that could
	// not be notified inside the budget is exactly the one that would otherwise
	// hold the drain open for the full idle timeout.
	defer m.FenceAll()

	completed, sent := 0, 0
	for completed < len(connections) {
		select {
		case ok := <-done:
			completed++
			if ok {
				sent++
			}
		case <-ctx.Done():
			slog.Warn("worker shutdown notification deadline reached", "sent", sent, "total", len(connections))
			return
		}
	}
	slog.Info("sent shutdown notifications to workers", "count", sent, "total", len(connections))
}

// isFenced reports whether FenceAll has latched the registry closed.
func (m *Manager) isFenced() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fenced
}

// snapshotConns copies the live connections so the caller can do blocking work
// on them without holding the registry lock. Both broadcasts need that: Send
// parks on the wire, and Cancel wakes a handler whose defer takes the WRITE
// lock -- so fencing under the read lock would make the Hub's own shutdown the
// thing that wedges it.
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
// One dependency on the worker survives, deliberately: Conn.Send holds the
// conn's mutex across the wire write, and the handler's Unregister waits for
// that send before returning. A peer that has stopped draining its socket is
// bounded by the server's HTTP/2 write timeout (see hub.Server); a peer that
// merely stalls its HTTP/2 flow-control window is not, because no socket write
// is attempted. Making that impossible needs the handler to own every Send --
// tracked in https://github.com/leapmux/leapmux/issues/344
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
