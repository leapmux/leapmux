package crdt

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriberControllerSnapshotsAreDeeplyImmutable(t *testing.T) {
	c := newSubscriberController()
	sub := &Subscriber{
		UserID: "user-1",
		Filter: SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send:   func(*MarshaledEvent) error { return nil },
	}
	c.Add(sub)

	before := c.Snapshot()
	require.Len(t, before, 1)

	c.MutateEach(func(current *Subscriber) {
		current.Filter.WorkspaceIDs["w2"] = true
	})

	after := c.Snapshot()
	require.Len(t, after, 1)
	assert.False(t, before[0].Filter.IsAllowed("w2"),
		"a published snapshot must not observe later live-filter mutations")
	assert.True(t, after[0].Filter.IsAllowed("w2"),
		"a successful mutation must publish a replacement snapshot")
}

func TestSubscriberControllerAddReturnsImmutableBootstrapFilter(t *testing.T) {
	c := newSubscriberController()
	sub := &Subscriber{Filter: SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}}}
	bootstrapFilter := c.Add(sub)

	c.MutateEach(func(current *Subscriber) {
		current.Filter.WorkspaceIDs["w2"] = true
	})

	assert.False(t, bootstrapFilter.IsAllowed("w2"),
		"subscription bootstrap must not race later live-filter reconciliation")
	assert.True(t, sub.Filter.IsAllowed("w2"))
}

func TestSubscriberControllerConcurrentSnapshotAndFilterMutation(t *testing.T) {
	c := newSubscriberController()
	sub := &Subscriber{
		UserID: "user-1",
		Filter: SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
		Send:   func(*MarshaledEvent) error { return nil },
	}
	c.Add(sub)

	const iterations = 1_000
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range iterations {
			for _, current := range c.Snapshot() {
				_ = current.Filter.IsAllowed("w1")
				_ = current.Filter.IsAllowed("w2")
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := range iterations {
			c.MutateEach(func(current *Subscriber) {
				if i%2 == 0 {
					current.Filter.WorkspaceIDs["w2"] = true
				} else {
					delete(current.Filter.WorkspaceIDs, "w2")
				}
			})
		}
	}()
	close(start)
	wg.Wait()
}

func TestSubscriberControllerReusesImmutableClonesForUnchangedSubscribers(t *testing.T) {
	c := newSubscriberController()
	changed := &Subscriber{UserID: "changed", Filter: SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}}}
	unchanged := &Subscriber{UserID: "unchanged", Filter: SubscriberFilter{WorkspaceIDs: map[string]bool{"w2": true}}}
	c.Add(changed)
	c.Add(unchanged)

	var before *Subscriber
	for _, sub := range c.Snapshot() {
		if sub.UserID == "unchanged" {
			before = sub
		}
	}
	require.NotNil(t, before)
	c.MutateEach(func(sub *Subscriber) {
		if sub == changed {
			sub.Filter.WorkspaceIDs["w3"] = true
		}
	})
	var after *Subscriber
	for _, sub := range c.Snapshot() {
		if sub.UserID == "unchanged" {
			after = sub
		}
	}
	require.NotNil(t, after)
	assert.Same(t, before, after, "publishing one filter change must not re-clone every subscriber")
}
