package main

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestOperationGateBeginDoneIsIdempotent pins FOOTGUNS-2: the func returned by
// begin() must be safe to call more than once. A caller that defers done() AND
// invokes it on a manual cleanup path (or any refactor that reaches done twice)
// must not drive the waitCounter negative and panic the sidecar. Routing through a
// per-admission sync.Once makes "exactly once" mechanical rather than a
// caller-discipline rule.
func TestOperationGateBeginDoneIsIdempotent(t *testing.T) {
	var g operationGate

	done, ok := g.begin()
	assert.True(t, ok, "an open gate must admit the operation")

	// Calling done() more than once must not panic. Under the raw g.wc.done
	// method value this would crash with "waitCounter: negative counter".
	assert.NotPanics(t, func() {
		done()
		done()
		done()
	})

	// drain still returns promptly: the waitCounter reached zero on the first call.
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
// concurrent begin/done pairs to confirm the waitCounter never goes negative under
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

// TestOperationGateDrainJoinsOperationReleasedMidWait covers the released-mid-drain
// path every other gate test skips by draining an already-idle gate with timeout 0:
// begin, start drain in a goroutine, release, assert drain returns promptly.
func TestOperationGateDrainJoinsOperationReleasedMidWait(t *testing.T) {
	var g operationGate
	done, ok := g.begin()
	assert.True(t, ok)

	finished := make(chan struct{})
	go func() {
		g.drain(2*time.Second, "")
		close(finished)
	}()

	// Wait until the drain has observably parked: doneChan creates wc.zero only
	// while the counter is positive, so a non-nil zero proves the drain goroutine
	// holds the live channel done() must close. A bare sleep could lose this race
	// -- done() would zero the counter first and the drain would take the idle
	// pre-closed fast path, passing without ever exercising the mid-wait join.
	parkDeadline := time.Now().Add(time.Second)
	for {
		g.wc.mu.Lock()
		parked := g.wc.zero != nil
		g.wc.mu.Unlock()
		if parked {
			break
		}
		if time.Now().After(parkDeadline) {
			t.Fatal("drain goroutine never parked on the counter's zero channel")
		}
		time.Sleep(time.Millisecond)
	}
	done()

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("drain must return promptly once the in-flight operation finishes")
	}
}
