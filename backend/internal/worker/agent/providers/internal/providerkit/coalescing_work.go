package providerkit

import "sync"

// CoalescingWork runs one goroutine for each key.
// Requests that arrive during a run cause one additional run.
//
// It owns a separate mutex. Each operation reads current state when it runs.
type CoalescingWork struct {
	mu sync.Mutex
	// pending maps a key with a run in flight to whether another request arrived
	// during it. Absence means no run is in flight for that key.
	pending map[string]bool
}

// Idle reports that no run is in flight for any key.
func (r *CoalescingWork) Idle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending) == 0
}

// InFlight reports the run of each key, and whether one more request arrived
// during it. It copies the map, so a caller never reads the runner's own state
// without the lock.
func (r *CoalescingWork) InFlight() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.pending))
	for key, queued := range r.pending {
		out[key] = queued
	}
	return out
}

// Run starts work for key, or marks the in-flight run to repeat once more.
// Calls for one key must use the same operation and read its current state.
func (r *CoalescingWork) Run(key string, work func()) {
	r.mu.Lock()
	if _, running := r.pending[key]; running {
		r.pending[key] = true
		r.mu.Unlock()
		return
	}
	if r.pending == nil {
		r.pending = make(map[string]bool)
	}
	r.pending[key] = false
	r.mu.Unlock()
	go func() {
		for {
			work()
			r.mu.Lock()
			if !r.pending[key] {
				delete(r.pending, key)
				r.mu.Unlock()
				return
			}
			r.pending[key] = false
			r.mu.Unlock()
		}
	}()
}
