package main

import (
	"sync"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

// eventSink forwards events to the current RPC session. The zero value drops
// events until the session installs its sink.
type eventSink struct {
	mu   sync.RWMutex
	sink func(*desktoppb.Event)
}

// Set installs the event sink. Pass nil when the session ends.
func (s *eventSink) Set(sink func(*desktoppb.Event)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sink = sink
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

// EmitRelay stamps a freshly built relay event with its owner before delivery.
// The shell and the undeliverable-frame handler read the same owner from it.
func (s *eventSink) EmitRelay(owner uint64, event *desktoppb.Event) {
	event.RelayId = owner
	s.Emit(event)
}

// forOwner captures the emitting relay. It reads the owner on each emit so the
// next event observes a channel relay's adoption by another wrapper.
func (s *eventSink) forOwner(relay *wsRelay) func(*desktoppb.Event) {
	return func(event *desktoppb.Event) { s.EmitRelay(relay.ownerNow(), event) }
}
