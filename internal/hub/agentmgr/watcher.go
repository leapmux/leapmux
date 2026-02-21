package agentmgr

import (
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/metrics"
)

// Watcher represents a single Frontend stream watching an agent.
type Watcher struct {
	ch chan *leapmuxv1.AgentEvent
}

// C returns the channel that receives agent events.
func (w *Watcher) C() <-chan *leapmuxv1.AgentEvent {
	return w.ch
}

// Manager tracks active agent watchers and fans out events.
type Manager struct {
	mu       sync.RWMutex
	watchers map[string]map[*Watcher]struct{} // agentID -> set of watchers
}

// New creates a new agent watcher Manager.
func New() *Manager {
	return &Manager{
		watchers: make(map[string]map[*Watcher]struct{}),
	}
}

// Watch registers a new watcher for the given agent.
// The returned Watcher should be removed with Unwatch when done.
func (m *Manager) Watch(agentID string) *Watcher {
	w := &Watcher{
		ch: make(chan *leapmuxv1.AgentEvent, 64),
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.watchers[agentID] == nil {
		m.watchers[agentID] = make(map[*Watcher]struct{})
	}
	m.watchers[agentID][w] = struct{}{}
	if len(m.watchers[agentID]) == 1 {
		metrics.ActiveAgents.Inc()
	}

	return w
}

// Unwatch removes a watcher. Safe to call multiple times.
func (m *Manager) Unwatch(agentID string, w *Watcher) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ws, ok := m.watchers[agentID]; ok {
		delete(ws, w)
		if len(ws) == 0 {
			delete(m.watchers, agentID)
			metrics.ActiveAgents.Dec()
		}
	}
}

// Broadcast sends an event to all watchers of the given agent.
// Non-blocking: drops messages if a watcher's buffer is full.
func (m *Manager) Broadcast(agentID string, event *leapmuxv1.AgentEvent) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for w := range m.watchers[agentID] {
		select {
		case w.ch <- event:
		default:
			// Watcher buffer full -- drop message to avoid blocking.
		}
	}
}

// AgentBroadcast pairs an agent ID with the event to broadcast.
type AgentBroadcast struct {
	AgentID string
	Event   *leapmuxv1.AgentEvent
}

// BroadcastMany sends events to watchers of multiple agents in a single
// lock acquisition. Non-blocking: drops messages if a watcher's buffer is full.
func (m *Manager) BroadcastMany(events []AgentBroadcast) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, e := range events {
		for w := range m.watchers[e.AgentID] {
			select {
			case w.ch <- e.Event:
			default:
			}
		}
	}
}
