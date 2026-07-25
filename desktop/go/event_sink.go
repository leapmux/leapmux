package main

import (
	"sync"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

// eventSink is App's event pub-sub, extracted from the App god-object so App's
// mutex surface shrinks to the lifecycle/config locks a reader must reason
// about together. It carries two sinks behind one RWMutex: a generic
// event sink and a relay-aware variant that threads the emitting relay's owner
// id alongside the event. Both are installed by the RPCSession at session start
// and cleared on teardown. The zero value is a usable nil-safe sink, so a bare
// App{} keeps working without initialization.
//
// See https://github.com/leapmux/leapmux/issues/295.
type eventSink struct {
	mu        sync.RWMutex
	sink      func(*desktoppb.Event)
	relaySink func(owner uint64, event *desktoppb.Event)
}

// Set installs the generic event sink. Pass nil to clear it (e.g. on session
// teardown).
func (s *eventSink) Set(sink func(*desktoppb.Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sink = sink
}

// SetRelay installs the relay-aware sink the RPCSession provides alongside the
// generic sink. The relay read loops route their emits through it (via
// EmitRelay) so a frame that cannot be delivered carries the emitting relay's
// owner id forward to the close path. Pass nil to clear it.
func (s *eventSink) SetRelay(sink func(owner uint64, event *desktoppb.Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.relaySink = sink
}

// Emit delivers event through the generic sink when one is installed. A sink
// installed later than a relay's read loop starts (or no sink at all, as in a
// bare App{}) is the nil case this guards.
func (s *eventSink) Emit(event *desktoppb.Event) {
	s.mu.RLock()
	sink := s.sink
	s.mu.RUnlock()
	if sink != nil {
		sink(event)
	}
}

// EmitRelay routes a relay-sourced event through the relay-aware sink when one
// is installed, falling back to the generic Emit otherwise (e.g. when the relay
// emits before the RPCSession has wired its sink, or in a focused test that
// constructs a bare App). The fallback loses the owner id, but a session
// without a sink has no shell pipe to deliver an undeliverable frame over in
// the first place.
func (s *eventSink) EmitRelay(owner uint64, event *desktoppb.Event) {
	s.mu.RLock()
	relaySink := s.relaySink
	s.mu.RUnlock()
	if relaySink != nil {
		relaySink(owner, event)
		return
	}
	s.Emit(event)
}

// forOwner returns a func the relay read loops call in place of a bare Emit,
// capturing the emitting relay so the owner id at emit time rides alongside the
// event. The closure reads relay.owner at CALL time rather than capturing its
// value, because owner is stamped AFTER newWSRelay constructs the relay -- but
// it is stable for the relay's lifetime once stamped (a supersede replaces
// connection.relay, not this relay's owner field), so the read loop's emit
// always reports the id this relay was installed under.
func (s *eventSink) forOwner(relay *wsRelay) func(*desktoppb.Event) {
	return func(event *desktoppb.Event) { s.EmitRelay(relay.owner, event) }
}
