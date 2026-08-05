package service

import "time"

// ParkedFrameCapForTest exposes the bound so an external test can state its
// precondition AS the bound rather than as a round number beside it -- a guard
// written as "> 100" passes at 101 commits, where nothing overflows, and the
// test then fails forty lines later at the overflow assertion, reporting a
// broken feature for what is really a short burst.
//
// Here rather than in subscriber_queue.go so the package's shipped API does not
// carry a constant only tests read -- the same reason the worker's reconciler
// seam lives in its own export_test.go.
const ParkedFrameCapForTest = parkedFrameCap

// WithForcedKeepaliveProbesForTest replaces this handler's interval ticker with
// a channel the test sends on, so a case about WHEN the probe loop is armed is
// bounded by an event rather than by a duration a loaded CI box overshoots.
//
// The channel is UNBUFFERED on purpose at the call sites that use it: a send
// completes only once the loop is parked on the receive, which is what lets a
// test tell "a probe loop exists" from "one does not" without waiting on
// anything. `timeout` still bounds one probe, so a forced probe on a peer that
// cannot answer fails rather than hanging.
//
// Here rather than in ws_userevents.go for the reason ParkedFrameCapForTest is:
// the shipped API should not carry a seam only tests reach.
func (h *UserEventsHandler) WithForcedKeepaliveProbesForTest(ticks <-chan time.Time, timeout time.Duration) *UserEventsHandler {
	h.keepalive = wsKeepalivePace{timeout: timeout, ticks: ticks}
	return h
}

// FillBootstrapGateForTest occupies every concurrent-build slot and returns a
// release. An external test can then assert what a connect gets when the Hub is
// already building as many opening frames as its budget could hold, without
// having to race real connects into that state.
//
// Reports how many slots it took, so a test states its precondition as the
// bound rather than as a number beside it.
func (h *UserEventsHandler) FillBootstrapGateForTest() (held int, release func()) {
	for {
		select {
		case h.bootstrapGate <- struct{}{}:
			held++
		default:
			return held, func() {
				for range held {
					<-h.bootstrapGate
				}
			}
		}
	}
}
