package main

import (
	"sync"
	"sync/atomic"
	"testing"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

// eventSink was extracted from App's god-object in #295. These tests pin its
// contract directly so a regression in the pub-sub's own semantics (rather
// than App's forwarding of them) fails here instead of only surfacing through
// an integration path.

// A bare eventSink -- as constructed by a bare App{} or before the RPCSession
// installs a sink -- must drop events silently rather than panic. The relay
// read loops can emit before SetEventSink runs (see rpc_test.go), so this
// nil-safety is load-bearing.
func TestEventSink_EmitsAreNoOpsBeforeASinkIsInstalled(t *testing.T) {
	var s eventSink
	s.Emit(&desktoppb.Event{})          // must not panic
	s.EmitRelay(42, &desktoppb.Event{}) // must not panic, must not recurse into a nil sink
}

// Emit routes through the generic sink installed by Set, and clearing it
// (Set(nil), as the RPCSession does on teardown) makes Emits no-ops again.
func TestEventSink_EmitRoutesThroughTheInstalledSink(t *testing.T) {
	var s eventSink
	var got atomic.Int32
	s.Set(func(*desktoppb.Event) { got.Add(1) })

	s.Emit(&desktoppb.Event{})
	s.Emit(&desktoppb.Event{})
	if got.Load() != 2 {
		t.Fatalf("generic sink called %d times, want 2", got.Load())
	}

	s.Set(nil)
	s.Emit(&desktoppb.Event{})
	if got.Load() != 2 {
		t.Fatalf("Emits after Set(nil) must be dropped, got %d calls", got.Load())
	}
}

// forOwner returns a closure that threads the emitting relay's owner id into
// EmitRelay, reading relay.owner at CALL time (so a stamp after construction is
// observed). This pins the deferred-read contract the relay read loops rely on.
func TestEventSink_ForOwnerReadsOwnerAtCallTime(t *testing.T) {
	var s eventSink
	var got atomic.Uint64
	s.Set(func(event *desktoppb.Event) { got.Store(event.GetRelayId()) })

	relay := &wsRelay{} // owner starts at its zero value
	emit := s.forOwner(relay)
	emit(&desktoppb.Event{})
	if got.Load() != 0 {
		t.Fatalf("owner before stamp = %d, want 0", got.Load())
	}

	// Stamping owner AFTER forOwner captured the relay must still be observed,
	// because the closure reads relay.owner when invoked, not when built.
	relay.stampOwner(123)
	emit(&desktoppb.Event{})
	if got.Load() != 123 {
		t.Fatalf("owner after stamp = %d, want 123 (must read at call time)", got.Load())
	}
}

// The relay read loop can emit while another wrapper adopts the relay.
// Run with -race to check that owner reads and writes are synchronized.
func TestEventSink_ForOwnerReadsOwnerConcurrentlyWithReStamp(t *testing.T) {
	var s eventSink
	s.Set(func(*desktoppb.Event) {})

	relay := &wsRelay{}
	emit := s.forOwner(relay)

	var wg sync.WaitGroup
	wg.Add(2)

	const iterations = 200
	// Reader: the relay read loop emits with the current owner.
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			emit(&desktoppb.Event{})
		}
	}()
	// Writer: another wrapper takes ownership.
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			relay.stampOwner(uint64(i % 8))
		}
	}()

	wg.Wait()
}
