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
	subs     map[*Subscriber]*Subscriber
	snapshot atomic.Pointer[[]*Subscriber]
}

func newSubscriberController() *SubscriberController {
	return &SubscriberController{
		subs: map[*Subscriber]*Subscriber{},
	}
}

// Add registers s, caches its immutable broadcast clone, and returns the
// filter captured at registration for connection bootstrap.
func (c *SubscriberController) Add(s *Subscriber) SubscriberFilter {
	c.mu.Lock()
	published := cloneSubscriber(s)
	c.subs[s] = published
	c.refreshSnapshotLocked()
	c.mu.Unlock()
	return published.Filter
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
// holding any lock: both the slice and each subscriber's filter map are
// deep copies owned by this publication and never mutated after Store.
func (c *SubscriberController) Snapshot() []*Subscriber {
	if p := c.snapshot.Load(); p != nil {
		return *p
	}
	return nil
}

// ForEachLocked invokes fn for each subscriber under the controller's
// read lock. The callback must not mutate a subscriber; callers that update
// filters must use MutateEach so the immutable broadcast snapshot is refreshed.
func (c *SubscriberController) ForEachLocked(fn func(*Subscriber)) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for s := range c.subs {
		fn(s)
	}
}

// MutateEach invokes fn for every live subscriber under the exclusive lock,
// then publishes a new deeply immutable snapshot. It is the only supported
// path for changing subscriber filters after Add.
func (c *SubscriberController) MutateEach(fn func(*Subscriber)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for s, published := range c.subs {
		fn(s)
		if !subscriberFilterEqual(s.Filter, published.Filter) {
			c.subs[s] = cloneSubscriber(s)
		}
	}
	c.refreshSnapshotLocked()
}

// Len returns the current subscriber count. Helpful for cheap
// early-return guards on broadcast paths.
func (c *SubscriberController) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.subs)
}

// refreshSnapshotLocked must be called with c.mu held (write lock).
// Replaces the published slice with the cached immutable clones. Only a
// subscriber whose filter changed is re-cloned by MutateEach.
func (c *SubscriberController) refreshSnapshotLocked() {
	snap := make([]*Subscriber, 0, len(c.subs))
	for _, published := range c.subs {
		snap = append(snap, published)
	}
	c.snapshot.Store(&snap)
}

func subscriberFilterEqual(a, b SubscriberFilter) bool {
	if (a.WorkspaceIDs == nil) != (b.WorkspaceIDs == nil) || len(a.WorkspaceIDs) != len(b.WorkspaceIDs) {
		return false
	}
	for workspaceID, allowed := range a.WorkspaceIDs {
		if b.WorkspaceIDs[workspaceID] != allowed {
			return false
		}
	}
	return true
}

func cloneSubscriber(s *Subscriber) *Subscriber {
	if s == nil {
		return nil
	}
	out := *s
	if s.RequestedWorkspaceIDs != nil {
		out.RequestedWorkspaceIDs = make(map[string]bool, len(s.RequestedWorkspaceIDs))
		for workspaceID, requested := range s.RequestedWorkspaceIDs {
			out.RequestedWorkspaceIDs[workspaceID] = requested
		}
	}
	if s.Filter.WorkspaceIDs != nil {
		out.Filter.WorkspaceIDs = make(map[string]bool, len(s.Filter.WorkspaceIDs))
		for workspaceID, allowed := range s.Filter.WorkspaceIDs {
			out.Filter.WorkspaceIDs[workspaceID] = allowed
		}
	}
	return &out
}
