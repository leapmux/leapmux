package crdt

import (
	"sync"
	"sync/atomic"
)

// SubscriberController owns the live subscriber set for one org's
// CRDT manager and publishes a lock-free snapshot for the broadcast
// hot path.
//
// Why a separate controller? Subscribe / unsub need to update two
// concerns at once (the subscriber set and the per-client refcount
// the PresenceController consumes). Splitting them into named types
// makes the two locks explicit so the Manager's Subscribe sequence
// reads as one ordered acquisition instead of "the m.mu does
// everything." The lock-free snapshot publisher means broadcast
// hot paths just load an atomic pointer — no contention with
// Subscribe / unsub churn.
type SubscriberController struct {
	mu       sync.RWMutex
	subs     map[*Subscriber]struct{}
	snapshot atomic.Pointer[[]*Subscriber]
}

func newSubscriberController() *SubscriberController {
	return &SubscriberController{
		subs: map[*Subscriber]struct{}{},
	}
}

// Add registers `s` and refreshes the broadcast snapshot.
func (c *SubscriberController) Add(s *Subscriber) {
	c.mu.Lock()
	c.subs[s] = struct{}{}
	c.refreshSnapshotLocked()
	c.mu.Unlock()
}

// Remove drops `s` from the active set and refreshes the snapshot.
// No-op when `s` is not registered.
func (c *SubscriberController) Remove(s *Subscriber) {
	c.mu.Lock()
	if _, ok := c.subs[s]; ok {
		delete(c.subs, s)
		c.refreshSnapshotLocked()
	}
	c.mu.Unlock()
}

// Snapshot returns the current subscriber slice. Safe to call without
// holding any lock — the slice is owned by the snapshot publisher
// and replaced (not mutated) on every Add/Remove.
func (c *SubscriberController) Snapshot() []*Subscriber {
	if p := c.snapshot.Load(); p != nil {
		return *p
	}
	return nil
}

// ForEachLocked invokes fn for each subscriber under the controller's
// read lock. Used by callers that need an iteration with the
// guarantee no Add/Remove fires concurrently (e.g. expansion of a
// new workspace into every subscriber's filter, which mutates each
// subscriber's Filter map). Most callers should prefer Snapshot.
func (c *SubscriberController) ForEachLocked(fn func(*Subscriber)) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for s := range c.subs {
		fn(s)
	}
}

// Len returns the current subscriber count. Helpful for cheap
// early-return guards on broadcast paths.
func (c *SubscriberController) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.subs)
}

// refreshSnapshotLocked must be called with c.mu held (write lock).
// Replaces the published slice with a fresh copy of c.subs.
func (c *SubscriberController) refreshSnapshotLocked() {
	snap := make([]*Subscriber, 0, len(c.subs))
	for s := range c.subs {
		snap = append(snap, s)
	}
	c.snapshot.Store(&snap)
}
