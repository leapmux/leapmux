package main

import (
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

// EmitRelay routes through the relay-aware sink when one is installed,
// forwarding the emitting relay's owner id alongside the event.
func TestEventSink_EmitRelayUsesTheRelaySinkWhenInstalled(t *testing.T) {
	var s eventSink
	type captured struct {
		owner uint64
	}
	var got captured
	s.SetRelay(func(owner uint64, _ *desktoppb.Event) { got = captured{owner: owner} })

	s.EmitRelay(7, &desktoppb.Event{})
	if got.owner != 7 {
		t.Fatalf("relay sink received owner %d, want 7", got.owner)
	}
}

// When no relay sink is installed, EmitRelay must fall back to the generic
// sink -- this is the path a relay hits when it emits before the RPCSession
// has wired its relay sink, and it is why EmitRelay cannot just drop the event.
func TestEventSink_EmitRelayFallsBackToTheGenericSink(t *testing.T) {
	var s eventSink
	var genericCalls atomic.Int32
	s.Set(func(*desktoppb.Event) { genericCalls.Add(1) })
	// No SetRelay: EmitRelay must fall back.

	s.EmitRelay(99, &desktoppb.Event{})
	if genericCalls.Load() != 1 {
		t.Fatalf("fallback to generic sink called it %d times, want 1", genericCalls.Load())
	}
}

// forOwner returns a closure that threads the emitting relay's owner id into
// EmitRelay, reading relay.owner at CALL time (so a stamp after construction is
// observed). This pins the deferred-read contract the relay read loops rely on.
func TestEventSink_ForOwnerReadsOwnerAtCallTime(t *testing.T) {
	var s eventSink
	var got atomic.Uint64
	s.SetRelay(func(owner uint64, _ *desktoppb.Event) { got.Store(owner) })

	relay := &wsRelay{} // owner starts at its zero value
	emit := s.forOwner(relay)
	emit(&desktoppb.Event{})
	if got.Load() != 0 {
		t.Fatalf("owner before stamp = %d, want 0", got.Load())
	}

	// Stamping owner AFTER forOwner captured the relay must still be observed,
	// because the closure reads relay.owner when invoked, not when built.
	relay.owner = 123
	emit(&desktoppb.Event{})
	if got.Load() != 123 {
		t.Fatalf("owner after stamp = %d, want 123 (must read at call time)", got.Load())
	}
}
