package workermgr

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/metrics"
)

// Conn represents a connected worker's bidirectional stream.
type Conn struct {
	WorkerID string
	OrgID    string
	Stream   *connect.BidiStream[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse]
	SendFn   func(*leapmuxv1.ConnectResponse) error // Optional: overrides Stream.Send for testing.
	mu       sync.Mutex
}

// Send sends a message to the worker via the bidi stream.
// The mutex serializes writes to prevent concurrent HTTP/2 frame corruption.
func (c *Conn) Send(msg *leapmuxv1.ConnectResponse) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.SendFn != nil {
		return c.SendFn(msg)
	}
	if c.Stream == nil {
		return fmt.Errorf("stream is nil")
	}
	return c.Stream.Send(msg)
}

// Manager tracks connected workers. Thread-safe.
type Manager struct {
	mu            sync.RWMutex
	conns         map[string]*Conn // workerID -> Conn
	deregistering map[string]bool  // workerID -> true if deregistering

	regMu      sync.Mutex
	regWaiters map[string]chan struct{} // regToken -> notify channel
}

// New creates a new Manager.
func New() *Manager {
	return &Manager{
		conns:         make(map[string]*Conn),
		deregistering: make(map[string]bool),
		regWaiters:    make(map[string]chan struct{}),
	}
}

// Register adds a worker connection. Replaces any existing connection.
func (m *Manager) Register(c *Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.conns[c.WorkerID]
	m.conns[c.WorkerID] = c
	if !exists {
		metrics.ActiveWorkers.Inc()
	}
}

// Unregister removes the given worker connection only if it is still the
// registered connection for that workerID. This prevents a stale connection's
// deferred cleanup from accidentally removing a newer replacement connection.
// Returns true if the connection was actually removed.
func (m *Manager) Unregister(workerID string, conn *Conn) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conns[workerID] == conn {
		delete(m.conns, workerID)
		metrics.ActiveWorkers.Dec()
		return true
	}
	return false
}

// Get returns a worker connection by ID, or nil if not connected.
func (m *Manager) Get(workerID string) *Conn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.conns[workerID]
}

// IsOnline returns true if the worker is currently connected.
func (m *Manager) IsOnline(workerID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.conns[workerID]
	return ok
}

// MarkDeregistering marks a worker as being deregistered.
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
func (m *Manager) NotifyShutdown(retryDelaySeconds int32) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for workerID, conn := range m.conns {
		if err := conn.Send(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_HubShuttingDown{
				HubShuttingDown: &leapmuxv1.HubShuttingDownNotification{
					RetryDelaySeconds: retryDelaySeconds,
				},
			},
		}); err != nil {
			slog.Warn("failed to send shutdown notification to worker", "worker_id", workerID, "error", err)
		}
	}
	slog.Info("sent shutdown notifications to workers", "count", len(m.conns))
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
