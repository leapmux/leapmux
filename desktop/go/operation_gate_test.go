package main

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestOperationGateBeginDoneIsIdempotent pins FOOTGUNS-2: the func returned by
// begin() must be safe to call more than once. A caller that defers done() AND
// invokes it on a manual cleanup path (or any refactor that reaches done twice)
// must not drive the WaitGroup negative and panic the sidecar. Routing through a
// per-admission sync.Once makes "exactly once" mechanical rather than a
// caller-discipline rule.
func TestOperationGateBeginDoneIsIdempotent(t *testing.T) {
	var g operationGate

	done, ok := g.begin()
	assert.True(t, ok, "an open gate must admit the operation")

	// Calling done() more than once must not panic. Under the raw g.wg.Done
	// method value this would crash with "sync: negative WaitGroup counter".
	assert.NotPanics(t, func() {
		done()
		done()
		done()
	})

	// drain still returns promptly: the WaitGroup reached zero on the first call.
	assert.NotPanics(t, func() { g.drain(0, "") })
}

// TestOperationGateBeginRejectsAfterClose verifies the gate flips closed under
// the same lock that begin checks, so an operation cannot be admitted after
// close ran.
func TestOperationGateBeginRejectsAfterClose(t *testing.T) {
	var g operationGate
	var cancelRan bool
	g.close(func() { cancelRan = true })

	_, ok := g.begin()
	assert.False(t, ok, "an operation must not be admitted once the gate is closed")
	assert.True(t, cancelRan, "close runs the cancel under the admission lock")
}

// TestOperationGateConcurrentBeginDone exercises the once-wrapper under
// concurrent begin/done pairs to confirm the WaitGroup never goes negative under
// the racing done() invocations the wrapper exists to make safe.
func TestOperationGateConcurrentBeginDone(t *testing.T) {
	var g operationGate
	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			done, ok := g.begin()
			if !ok {
				return
			}
			// A stray extra done() per goroutine -- the scenario the wrapper prevents.
			done()
			done()
		}()
	}
	wg.Wait()
	assert.NotPanics(t, func() { g.drain(0, "") })
}
