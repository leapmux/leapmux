package service_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/service"
)

func TestPrivateEventsBus_PublishesToSubscribersOfSameWorkspace(t *testing.T) {
	bus := service.NewPrivateEventsBus()
	defer bus.Stop()

	var got atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		_ = bus.Subscribe(ctx, "ws-1", func(evt *leapmuxv1.WorkspacePrivateEvent) error {
			got.Add(1)
			return nil
		})
	}()
	// Tiny pause so the subscriber registers before publish.
	time.Sleep(50 * time.Millisecond)
	bus.PublishTabRenamed("ws-1", "tab-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "new title", "origin-X")
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), got.Load())
}

func TestPrivateEventsBus_DoesNotLeakAcrossWorkspaces(t *testing.T) {
	bus := service.NewPrivateEventsBus()
	defer bus.Stop()

	var got atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		_ = bus.Subscribe(ctx, "ws-1", func(evt *leapmuxv1.WorkspacePrivateEvent) error {
			got.Add(1)
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)

	// Publish to a different workspace — must not reach the ws-1 subscriber.
	bus.PublishTabRenamed("ws-2", "tab-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "title", "origin")
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), got.Load())
}

func TestPrivateEventsBus_StopClosesSubscribers(t *testing.T) {
	bus := service.NewPrivateEventsBus()

	done := make(chan struct{})
	go func() {
		_ = bus.Subscribe(context.Background(), "ws-1", func(evt *leapmuxv1.WorkspacePrivateEvent) error {
			return nil
		})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)

	bus.Stop()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Subscribe did not return after Stop")
	}
}

// TestPrivateEventsBus_MultipleSubscribersOnSameWorkspaceAllReceive
// pins that the bus fans out to every subscriber registered for a
// workspace. The hub-side plan calls for per-channel fan-out (one
// channel per user), so when two clients of the same user are
// subscribed to one workspace they must both receive each TabRenamed.
// Per-user enforcement happens BEFORE Subscribe in the worker
// handler (`requireAccessibleWorkspace`); inside the bus, every
// active subscriber is treated equally.
func TestPrivateEventsBus_MultipleSubscribersOnSameWorkspaceAllReceive(t *testing.T) {
	bus := service.NewPrivateEventsBus()
	defer bus.Stop()

	got1 := make(chan *leapmuxv1.WorkspacePrivateEvent, 1)
	got2 := make(chan *leapmuxv1.WorkspacePrivateEvent, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		_ = bus.Subscribe(ctx, "ws-shared", func(evt *leapmuxv1.WorkspacePrivateEvent) error {
			got1 <- evt
			return nil
		})
	}()
	go func() {
		_ = bus.Subscribe(ctx, "ws-shared", func(evt *leapmuxv1.WorkspacePrivateEvent) error {
			got2 <- evt
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)

	bus.PublishTabRenamed("ws-shared", "tab-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "T", "origin")

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
	bus := service.NewPrivateEventsBus()
	defer bus.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Subscriber A blocks indefinitely until releaseA fires; the bus
	// should still deliver to subscriber B.
	releaseA := make(chan struct{})
	gotA := make(chan *leapmuxv1.WorkspacePrivateEvent, 64)
	gotB := make(chan *leapmuxv1.WorkspacePrivateEvent, 1)
	go func() {
		_ = bus.Subscribe(ctx, "ws-slow", func(evt *leapmuxv1.WorkspacePrivateEvent) error {
			<-releaseA
			gotA <- evt
			return nil
		})
	}()
	go func() {
		_ = bus.Subscribe(ctx, "ws-slow", func(evt *leapmuxv1.WorkspacePrivateEvent) error {
			gotB <- evt
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)

	// Push enough events to definitely overflow A's buffer (default
	// bufSize=32). B keeps up.
	for i := 0; i < 64; i++ {
		bus.PublishTabRenamed("ws-slow", "tab-1", leapmuxv1.TabType_TAB_TYPE_AGENT, "T", "origin")
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
	bus := service.NewPrivateEventsBus()
	defer bus.Stop()

	var observedOrigin string
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = bus.Subscribe(ctx, "ws-1", func(evt *leapmuxv1.WorkspacePrivateEvent) error {
			tr := evt.GetTabRenamed()
			require.NotNil(t, tr, "expected TabRenamed event")
			observedOrigin = tr.GetOriginClientId()
			close(done)
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)

	bus.PublishTabRenamed("ws-1", "tab-1", leapmuxv1.TabType_TAB_TYPE_TERMINAL, "T", "session-42")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive event")
	}
	assert.Equal(t, "session-42", observedOrigin)
}
