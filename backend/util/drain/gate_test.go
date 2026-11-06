package drain

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGateBeginDoneIsIdempotent pins FOOTGUNS-2: the func returned by
// Begin must be safe to call more than once. A caller that defers done() AND
// invokes it on a manual cleanup path (or any refactor that reaches done twice)
// must not drive the counter negative and panic. Routing through a
// per-admission sync.Once makes "exactly once" mechanical rather than a
// caller-discipline rule.
func TestGateBeginDoneIsIdempotent(t *testing.T) {
	var g Gate

	done, ok := g.Begin(nil)
	assert.True(t, ok, "an open gate must admit the operation")

	// Calling done() more than once must not panic. Under the raw
	// Counter.Done method value this would crash with "drain.Counter:
	// negative counter".
	assert.NotPanics(t, func() {
		done()
		done()
		done()
	})

	// Drain still returns promptly: the counter reached zero on the first call.
	assert.True(t, g.Drain(0, ""), "every admitted operation must complete")
}

// TestGateBeginRejectsAfterClose verifies the gate flips closed under the same
// lock that Begin checks, so an operation cannot be admitted after Close ran.
func TestGateBeginRejectsAfterClose(t *testing.T) {
	var g Gate
	var cancelRan bool
	g.Close(func() { cancelRan = true })

	done, ok := g.Begin(nil)
	assert.False(t, ok, "an operation must not be admitted once the gate is closed")
	assert.Nil(t, done, "a refused Begin returns no done func")
	assert.True(t, cancelRan, "Close runs the cancel under the admission lock")
}

// TestGateNilCallbacksAreAdmitted shows the two callbacks are optional: a
// caller with nothing to cancel and nothing to track still gets the protocol.
func TestGateNilCallbacksAreAdmitted(t *testing.T) {
	var g Gate

	done, ok := g.Begin(nil)
	require.True(t, ok)
	assert.NotPanics(t, func() { g.Close(nil) })
	done()
	assert.True(t, g.Drain(time.Second, ""), "a closed gate with no in-flight operations drains at once")
}

// TestGateConcurrentBeginDone exercises the once-wrapper under concurrent
// Begin/done pairs to confirm the counter never goes negative under the racing
// done() invocations the wrapper exists to make safe.
func TestGateConcurrentBeginDone(t *testing.T) {
	var g Gate
	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			done, ok := g.Begin(nil)
			if !assert.True(t, ok, "an open gate must admit every operation") {
				return
			}
			// A stray extra done() per goroutine -- the scenario the wrapper prevents.
			done()
			done()
		}()
	}
	wg.Wait()
	assert.True(t, g.Drain(0, ""), "every admitted operation must complete")
}

// TestGateDrainJoinsOperationReleasedMidWait covers the released-mid-drain
// path every other gate test skips by draining an already-idle gate with
// timeout 0: Begin, start Drain in a goroutine, release, assert Drain returns
// promptly.
func TestGateDrainJoinsOperationReleasedMidWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var g Gate
		done, ok := g.Begin(nil)
		require.True(t, ok)
		defer done()
		g.Close(nil)
		finished := make(chan bool, 1)
		go func() { finished <- g.Drain(2*time.Second, "") }()
		synctest.Wait()
		select {
		case <-finished:
			t.Error("the drain returned before the admitted operation completed")
		default:
		}
		done()
		assert.True(t, <-finished)
	})
}

// Registration and cancellation both run under the admission mutex.
func TestGateBeginRunsOnAdmitUnderTheLock(t *testing.T) {
	var g Gate
	admitted, cancelled := false, false
	assertLocked := func() {
		unlocked := g.mu.TryLock()
		if unlocked {
			g.mu.Unlock()
		}
		assert.False(t, unlocked, "the admission mutex must remain held during callbacks")
	}
	done, ok := g.Begin(func() {
		admitted = true
		assertLocked()
	})
	require.True(t, ok)
	assert.True(t, admitted)
	g.Close(func() {
		cancelled = true
		assertLocked()
	})
	assert.True(t, cancelled)
	done()
	assert.True(t, g.Drain(0, ""))
	_, ok = g.Begin(nil)
	assert.False(t, ok)
}
