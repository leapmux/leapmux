package drain

import (
	"sync"
	"sync/atomic"
	"testing"
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
	assert.NotPanics(t, func() { g.Drain(0, "") })
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
			if !ok {
				return
			}
			// A stray extra done() per goroutine -- the scenario the wrapper prevents.
			done()
			done()
		}()
	}
	wg.Wait()
	assert.NotPanics(t, func() { g.Drain(0, "") })
}

// TestGateDrainJoinsOperationReleasedMidWait covers the released-mid-drain
// path every other gate test skips by draining an already-idle gate with
// timeout 0: Begin, start Drain in a goroutine, release, assert Drain returns
// promptly.
func TestGateDrainJoinsOperationReleasedMidWait(t *testing.T) {
	var g Gate
	done, ok := g.Begin(nil)
	require.True(t, ok)

	finished := make(chan bool, 1)
	go func() { finished <- g.Drain(2*time.Second, "") }()

	// Wait until the drain has observably parked: Pending reports a live
	// completion channel only while the counter is positive, which proves the
	// drain goroutine holds the channel done() must close. A bare sleep could
	// lose this race -- done() would zero the counter first and the drain would
	// take the idle pre-closed fast path, passing without ever exercising the
	// mid-wait join.
	parkDeadline := time.Now().Add(time.Second)
	for !g.wc.Pending() {
		if time.Now().After(parkDeadline) {
			t.Fatal("drain goroutine never parked on the counter's zero channel")
		}
		time.Sleep(time.Millisecond)
	}
	done()

	select {
	case joined := <-finished:
		assert.True(t, joined, "the operation finished inside the budget")
	case <-time.After(time.Second):
		t.Fatal("drain must return promptly once the in-flight operation finishes")
	}
}

// The onAdmit hook exists so a tracked registration is atomic with the admit:
// once Begin returns, a concurrent Close must not be able to finish first, or
// an admitted operation would escape both the tracking and the cancel.
func TestGateBeginRunsOnAdmitUnderTheLock(t *testing.T) {
	var g Gate
	admitted := make(chan struct{}, 1)
	var admissions atomic.Int32
	beginDone := make(chan struct{})
	go func() {
		defer close(beginDone)
		done, ok := g.Begin(func() {
			admissions.Add(1)
			admitted <- struct{}{}
			// Hold the admission lock while the test closes the gate, so the
			// Close below cannot return before this hook did.
			time.Sleep(50 * time.Millisecond)
		})
		require.True(t, ok)
		done()
	}()

	// Wait until Begin holds the lock inside onAdmit, then close: Close must
	// wait for the hook behind the admission lock.
	<-admitted
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		g.Close(nil)
	}()

	select {
	case <-closed:
		t.Fatal("Close returned while onAdmit was still running under the admission lock")
	case <-time.After(10 * time.Millisecond):
	}

	select {
	case <-beginDone:
	case <-time.After(time.Second):
		t.Fatal("the admitted operation never finished")
	}
	<-closed
	assert.Equal(t, int32(1), admissions.Load())
}
