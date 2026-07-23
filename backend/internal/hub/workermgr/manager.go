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

// Send sends a message to the worker via the bidi stream.
// The mutex serializes writes to prevent concurrent HTTP/2 frame corruption.
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
	if c.Greeting != nil {
		if err := c.Send(c.Greeting); err != nil {
			return false, err
		}
	}
	m.mu.Lock()
	replaced := m.conns[c.WorkerID]
	m.conns[c.WorkerID] = c
	if replaced == nil {
		metrics.ActiveWorkers.Inc()
	}
	m.mu.Unlock()
	if replaced != nil && replaced != c {
		replaced.Fence()
	}
	return replaced != nil, nil
}

// Unregister removes the given worker connection only if it is still the
// registered connection for that workerID. This prevents a stale connection's
// deferred cleanup from accidentally removing a newer replacement connection.
// Returns true if the connection was actually removed.
func (m *Manager) Unregister(workerID string, conn *Conn) bool {
	m.mu.Lock()
	removed := false
	if m.conns[workerID] == conn {
		delete(m.conns, workerID)
		metrics.ActiveWorkers.Dec()
		removed = true
	}
	m.mu.Unlock()
	if removed {
		conn.Close()
	} else {
		conn.Fence()
	}
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

// NotifyShutdown sends a HubShuttingDownNotification to all connected workers.
// Best-effort: errors are logged but do not abort the shutdown sequence.
func (m *Manager) NotifyShutdown(ctx context.Context, retryDelaySeconds int32) {
	m.mu.RLock()
	connections := make(map[string]*Conn, len(m.conns))
	for workerID, conn := range m.conns {
		connections[workerID] = conn
	}
	m.mu.RUnlock()

	// done carries per-worker delivery success so the completion tally reflects
	// notifications that were actually sent, not merely attempted.
	done := make(chan bool, len(connections))
	for workerID, conn := range connections {
		go func() {
			err := conn.Send(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_HubShuttingDown{
					HubShuttingDown: &leapmuxv1.HubShuttingDownNotification{
						RetryDelaySeconds: retryDelaySeconds,
					},
				},
			})
			if err != nil {
				slog.Warn("failed to send shutdown notification to worker", "worker_id", workerID, "error", err)
			}
			done <- err == nil
		}()
	}

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

// NotifyRegistrationChange wakes up any waiter blocked on the given regToken.
func (m *Manager) NotifyRegistrationChange(regToken string) {
	m.regMu.Lock()
	defer m.regMu.Unlock()

	if ch, ok := m.regWaiters[regToken]; ok {
		close(ch)
		delete(m.regWaiters, regToken)
	}
}
