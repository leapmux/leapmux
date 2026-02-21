package terminalmgr

import (
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/metrics"
)

// Watcher represents a single Frontend stream watching a terminal.
type Watcher struct {
	ch chan *leapmuxv1.TerminalEvent
}

// C returns the channel that receives terminal events.
func (w *Watcher) C() <-chan *leapmuxv1.TerminalEvent {
	return w.ch
}

// Manager tracks active terminal watchers and fans out events.
type Manager struct {
	mu       sync.RWMutex
	watchers map[string]map[*Watcher]struct{} // terminalID -> set of watchers
}

// New creates a new terminal watcher Manager.
func New() *Manager {
	return &Manager{
		watchers: make(map[string]map[*Watcher]struct{}),
	}
}

// Watch registers a new watcher for the given terminal.
func (m *Manager) Watch(terminalID string) *Watcher {
	w := &Watcher{
		ch: make(chan *leapmuxv1.TerminalEvent, 256),
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.watchers[terminalID] == nil {
		m.watchers[terminalID] = make(map[*Watcher]struct{})
	}
	m.watchers[terminalID][w] = struct{}{}
	if len(m.watchers[terminalID]) == 1 {
		metrics.ActiveTerminals.Inc()
	}

	return w
}

// Unwatch removes a watcher.
func (m *Manager) Unwatch(terminalID string, w *Watcher) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ws, ok := m.watchers[terminalID]; ok {
		delete(ws, w)
		if len(ws) == 0 {
			delete(m.watchers, terminalID)
			metrics.ActiveTerminals.Dec()
		}
	}
}

// HasWatchers returns true if any watchers are registered for the terminal.
func (m *Manager) HasWatchers(terminalID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.watchers[terminalID]) > 0
}

// Broadcast sends an event to all watchers of the given terminal.
func (m *Manager) Broadcast(terminalID string, event *leapmuxv1.TerminalEvent) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for w := range m.watchers[terminalID] {
		select {
		case w.ch <- event:
		default:
			// Buffer full — drop to avoid blocking.
		}
	}
}
