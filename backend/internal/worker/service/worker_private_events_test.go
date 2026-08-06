package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// subscribeReady registers a bus subscriber and returns once the bus actually
// HAS it.
//
// The seam is the snapshot callback: SnapshotAndSubscribe invokes it
// immediately after committing the registration under the bus's lock, so a
// caller that waits for it is waiting on the state that ends the race. Every
// test here used to sleep 50 ms instead, which is a window sized by a real
// timer over two goroutine handoffs -- it holds on an idle laptop and misses
// under a loaded -race run, where the publish lands before the subscriber
// exists and the test reports a broken bus.
func subscribeReady(
	t *testing.T,
	ctx context.Context,
	bus *service.PrivateEventsBus,
	owner string,
	send func(*leapmuxv1.WorkerPrivateEvent) error,
) {
	t.Helper()
	registered := make(chan struct{})
	go func() {
		_ = bus.SnapshotAndSubscribe(ctx, userid.MustNew(owner),
			func(userid.UserID) []*leapmuxv1.WorkerPrivateEvent {
				close(registered)
				return nil
			}, send)
	}()
	select {
	case <-registered:
	case <-time.After(10 * time.Second):
		t.Fatalf("subscriber for %s never registered", owner)
	}
}

func TestPrivateEventsBus_PublishesToSubscribersOfSameOwner(t *testing.T) {
	t.Parallel()

	bus := service.NewPrivateEventsBus()
	defer bus.Stop()

	got := make(chan *leapmuxv1.WorkerPrivateEvent, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	subscribeReady(t, ctx, bus, "user-1", func(evt *leapmuxv1.WorkerPrivateEvent) error {
		got <- evt
		return nil
	})
	bus.PublishTabRenamed(userid.MustNew("user-1"), "tab-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "new title", "origin-X")

	select {
	case evt := <-got:
		assert.Equal(t, "new title", evt.GetTabRenamed().GetTitle())
	case <-time.After(10 * time.Second):
		t.Fatal("subscriber did not receive the published event")
	}
}

func TestPrivateEventsBus_DoesNotLeakAcrossOwners(t *testing.T) {
	t.Parallel()

	bus := service.NewPrivateEventsBus()
	defer bus.Stop()

	got := make(chan *leapmuxv1.WorkerPrivateEvent, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	subscribeReady(t, ctx, bus, "user-1", func(evt *leapmuxv1.WorkerPrivateEvent) error {
		got <- evt
		return nil
	})

	// Publish for a different owner — must not reach user-1's subscriber.
	bus.PublishTabRenamed(userid.MustNew("user-2"), "tab-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "leaked", "origin")
	// Then one for user-1. The bus delivers in order, so this arriving proves
	// the other was already handled -- and dropped. Sleeping instead made the
	// negative assertion pass by simply not having waited long enough, which is
	// the same result a genuinely leaking bus would have produced.
	bus.PublishTabRenamed(userid.MustNew("user-1"), "tab-2", leapmuxv1.TabType_TAB_TYPE_AGENT, "mine", "origin")

	select {
	case evt := <-got:
		assert.Equal(t, "mine", evt.GetTabRenamed().GetTitle(),
			"the first event this subscriber sees must be its OWN owner's")
	case <-time.After(10 * time.Second):
		t.Fatal("subscriber did not receive its own owner's event")
	}
	assert.Empty(t, got, "no further event may arrive")
}

func TestPrivateEventsBus_StopClosesSubscribers(t *testing.T) {
	t.Parallel()

	bus := service.NewPrivateEventsBus()

	registered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_ = bus.SnapshotAndSubscribe(context.Background(), userid.MustNew("user-1"),
			func(userid.UserID) []*leapmuxv1.WorkerPrivateEvent {
				close(registered)
				return nil
			}, func(evt *leapmuxv1.WorkerPrivateEvent) error {
				return nil
			})
		close(done)
	}()
	select {
	case <-registered:
	case <-time.After(10 * time.Second):
		t.Fatal("subscriber never registered")
	}

	bus.Stop()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Subscribe did not return after Stop")
	}
}

// TestPrivateEventsBus_MultipleSubscribersOnSameOwnerAllReceive pins that the
// bus fans out to every subscriber registered for an owner. Two browser tabs of
// the same user each hold their own stream, so both must receive each
// TabRenamed. The owner gate runs BEFORE Subscribe in the worker handler
// (`requireWorkerOwner`); inside the bus every active subscriber for that owner
// is treated equally.
func TestPrivateEventsBus_MultipleSubscribersOnSameOwnerAllReceive(t *testing.T) {
	t.Parallel()

	bus := service.NewPrivateEventsBus()
	defer bus.Stop()

	got1 := make(chan *leapmuxv1.WorkerPrivateEvent, 1)
	got2 := make(chan *leapmuxv1.WorkerPrivateEvent, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	subscribeReady(t, ctx, bus, "user-1", func(evt *leapmuxv1.WorkerPrivateEvent) error {
		got1 <- evt
		return nil
	})
	subscribeReady(t, ctx, bus, "user-1", func(evt *leapmuxv1.WorkerPrivateEvent) error {
		got2 <- evt
		return nil
	})

	bus.PublishTabRenamed(userid.MustNew("user-1"), "tab-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "T", "origin")

	select {
	case <-got1:
	case <-time.After(time.Second):
		t.Fatal("first subscriber did not receive event")
	}
	select {
	case <-got2:
	case <-time.After(time.Second):
		t.Fatal("second subscriber did not receive event")
	}
}

// TestPrivateEventsBus_DropsOnSlowConsumer pins the contract that a
// blocked subscriber doesn't stall the rest of the bus. The
// production code drops on the non-blocking send path; if a future
// optimisation turns this into a blocking send (e.g. "fairness for
// slow tabs"), the bus would deadlock under load.
func TestPrivateEventsBus_DropsOnSlowConsumer(t *testing.T) {
	t.Parallel()

	bus := service.NewPrivateEventsBus()
	defer bus.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Subscriber A blocks indefinitely until releaseA fires; the bus
	// should still deliver to subscriber B.
	releaseA := make(chan struct{})
	gotA := make(chan *leapmuxv1.WorkerPrivateEvent, 64)
	gotB := make(chan *leapmuxv1.WorkerPrivateEvent, 1)
	subscribeReady(t, ctx, bus, "user-1", func(evt *leapmuxv1.WorkerPrivateEvent) error {
		<-releaseA
		gotA <- evt
		return nil
	})
	subscribeReady(t, ctx, bus, "user-1", func(evt *leapmuxv1.WorkerPrivateEvent) error {
		gotB <- evt
		return nil
	})

	// Push enough events to definitely overflow A's buffer (default
	// bufSize=32). B keeps up.
	for i := 0; i < 64; i++ {
		bus.PublishTabRenamed(userid.MustNew("user-1"), "tab-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "T", "origin")
	}

	// B must receive at least one event without waiting on A.
	select {
	case <-gotB:
	case <-time.After(time.Second):
		t.Fatal("fast subscriber starved by slow subscriber — slow-consumer drop broken")
	}

	close(releaseA)
}

func TestPrivateEventsBus_EventCarriesOriginClientId(t *testing.T) {
	t.Parallel()

	bus := service.NewPrivateEventsBus()
	defer bus.Stop()

	var observedOrigin string
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	subscribeReady(t, ctx, bus, "user-1", func(evt *leapmuxv1.WorkerPrivateEvent) error {
		tr := evt.GetTabRenamed()
		require.NotNil(t, tr, "expected TabRenamed event")
		observedOrigin = tr.GetOriginClientId()
		close(done)
		return nil
	})

	bus.PublishTabRenamed(userid.MustNew("user-1"), "tab-1", leapmuxv1.TabType_TAB_TYPE_TERMINAL, "T", "session-42")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive event")
	}
	assert.Equal(t, "session-42", observedOrigin)
}

// TestPrivateEventsBus_StopRacesSubscribeAndPublish is the regression for two
// panics that a lock-free `closed` flag guarding lock-protected state made
// reachable: a nil-map write in SnapshotAndSubscribe (it read the flag before
// taking the mutex, so Stop could empty the map in between) and a send on a
// closed channel in publish (it copied the subscriber channels, released the
// read lock, and only then sent, so Stop's close could land in the gap).
//
// The second is the dangerous one: one publish path -- the orphan reconciler's
// RevokeRow, reached from a bare goroutine -- has no recover above it, so that
// panic took the whole worker process down.
//
// Run under `-race`, which is where the unsynchronized flag read shows up even
// on an interleaving that happens not to panic.
func TestPrivateEventsBus_StopRacesSubscribeAndPublish(t *testing.T) {
	t.Parallel()

	const goroutines = 24
	bus := service.NewPrivateEventsBus()
	owner := userid.MustNew("user-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	start := make(chan struct{})

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = bus.SnapshotAndSubscribe(ctx, owner, nil, func(*leapmuxv1.WorkerPrivateEvent) error { return nil })
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			bus.PublishTabRenamed(owner, "tab-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "T", "c1")
		}()
	}
	// Two Stops, to pin idempotence as well: the CAS this replaced was what
	// made a second Stop a no-op, and a plain bool under the mutex has to keep
	// that property or the second close(b.done) panics.
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			<-start
			bus.Stop()
		}()
	}

	close(start)
	wg.Wait()

	// Every subscriber must have been released by Stop rather than left parked:
	// nothing closes a subscriber channel any more, so `done` is the only
	// signal that can end those goroutines, and wg.Wait() above returning at
	// all is the assertion. A post-Stop subscribe must refuse immediately.
	require.NoError(t, bus.SnapshotAndSubscribe(ctx, owner, nil, func(*leapmuxv1.WorkerPrivateEvent) error {
		t.Error("a subscriber registered after Stop must never receive an event")
		return nil
	}))
}
