package main

import (
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDrainDoesNotLeakWaiterOnAbandonedStraggler is the operationGate side of
// the #297 reproducer (rpc_test.go's
// TestDrainHandlersDoesNotLeakWaiterOnAbandonedStraggler covers the
// drainHandlers side the leak was reported on): a permanently-stuck operation
// (released only in Cleanup) must not leave a waiter goroutine parked per
// timed-out drain. Pre-fix waitGroupDone spawned one such waiter per drain;
// post-fix waitCounter removes the spawn entirely.
//
// The scan keys on sync.(*WaitGroup).Wait / drain.go frames -- not the
// waitGroupDone symbol -- so the assertion stays meaningful after the fix
// deletes that helper.
func TestDrainDoesNotLeakWaiterOnAbandonedStraggler(t *testing.T) {
	var g operationGate
	done, ok := g.begin()
	require.True(t, ok)
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		done()
	})
	// Keep a live reference to release so the stuck admission is "an operation"
	// that never returns until Cleanup -- mirroring a non-cancellable exec.
	go func() { <-release }()

	const drains = 8
	for range drains {
		g.drain(time.Millisecond, "")
	}

	deadline := time.Now().Add(2 * time.Second)
	var lastDump string
	for {
		lastDump = allGoroutineStacks()
		leaked := countDrainWaiterFrames(lastDump)
		if leaked == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected no drain waiter goroutines after abandoning a straggler %d times; found %d\n%s",
				drains, leaked, lastDump)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func allGoroutineStacks() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

// countDrainWaiterFrames counts goroutines whose stacks show a leaked drain
// waiter: parked in sync.(*WaitGroup).Wait (the shape the pre-fix waitGroupDone
// bridge leaked), or carrying a desktop/go/drain.go frame (insurance against a
// reintroduced waiter of any other shape -- nothing legitimately runs drain.go
// code once every drain has returned, and the leak tests scan only then, with
// no concurrent test executing a drain). The stuck operation itself waits on a
// channel and does not match.
func countDrainWaiterFrames(dump string) int {
	count := 0
	for _, block := range strings.Split(dump, "\n\n") {
		if strings.Contains(block, "sync.(*WaitGroup).Wait") ||
			strings.Contains(block, "desktop/go/drain.go") {
			count++
		}
	}
	return count
}

// assertClosed fails the test unless ch is already closed. waitCounter channels
// are only ever closed, never sent on, so a ready receive means closed.
func assertClosed(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatal(msg)
	}
}

// assertOpen fails the test if ch is already closed.
func assertOpen(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal(msg)
	default:
	}
}

func TestWaitCounterIdleDoneChanIsAlreadyClosed(t *testing.T) {
	var c waitCounter
	assertClosed(t, c.doneChan(), "idle doneChan must return an already-closed channel")
}

func TestWaitCounterDoneChanClosesOnlyAtZero(t *testing.T) {
	var c waitCounter
	c.add()
	c.add()
	done := c.doneChan()
	c.done()
	assertOpen(t, done, "doneChan must stay open while the counter is still positive")
	c.done()
	assertClosed(t, done, "doneChan must close when the counter reaches zero")
}

func TestWaitCounterDoneChanReuseAfterZeroCrossing(t *testing.T) {
	var c waitCounter
	c.add()
	a := c.doneChan()
	c.done()
	assertClosed(t, a, "channel A must close on the first zero-crossing")

	c.add()
	b := c.doneChan()
	assert.True(t, a != b, "a new cycle must return a fresh channel, not reopen A")
	assertOpen(t, b, "channel B must start open")
	c.done()
	assertClosed(t, b, "channel B must close on the second zero-crossing")
	assertClosed(t, a, "channel A must stay closed after the later cycle")
}

func TestWaitCounterDoneChanSameWhileBusy(t *testing.T) {
	var c waitCounter
	c.add()
	a := c.doneChan()
	b := c.doneChan()
	assert.True(t, a == b, "both drain phases must observe one signal channel")
	c.done()
}

func TestWaitCounterDoneOnIdlePanics(t *testing.T) {
	var idle waitCounter
	assert.PanicsWithValue(t, "waitCounter: negative counter", func() { idle.done() })

	var cycled waitCounter
	cycled.add()
	cycled.done()
	assert.PanicsWithValue(t, "waitCounter: negative counter", func() { cycled.done() })

	// The panic fires before the counter mutates, so a caller that recovers it
	// (a future recover-middleware) finds the counter consistent: still idle, and
	// a fresh add/done cycle still works. A decrement-then-panic ordering would
	// leave n at -1 here, where one add() masks the corruption as "idle" and a
	// concurrent drain would report completion under live work.
	assertClosed(t, cycled.doneChan(), "a recovered negative-counter panic must leave the counter idle")
	cycled.add()
	relatch := cycled.doneChan()
	assertOpen(t, relatch, "the counter must still track new work after a recovered panic")
	cycled.done()
	assertClosed(t, relatch, "the counter must still close at zero after a recovered panic")
}

// TestWaitCounterWaitReportsCompletionWithinBudget pins the wait() facade both
// callers drain through: an idle counter wins even on an exhausted budget
// (waitBounded's fast path), a held counter times out with false, and a counter
// released mid-wait reports true promptly.
func TestWaitCounterWaitReportsCompletionWithinBudget(t *testing.T) {
	var idle waitCounter
	assert.True(t, idle.wait(0, ""), "an idle counter must win even on an exhausted budget")

	var busy waitCounter
	busy.add()
	assert.False(t, busy.wait(time.Millisecond, ""), "a held counter must report a timed-out wait")
	assert.False(t, busy.wait(0, ""),
		"a held counter must fail fast, not hang, on an exhausted budget -- the shape drainHandlers' shared deadline feeds it")
	busy.done()

	var joined waitCounter
	joined.add()
	result := make(chan bool, 1)
	go func() { result <- joined.wait(2*time.Second, "") }()
	joined.done()
	select {
	case ok := <-result:
		assert.True(t, ok, "wait must report success once the straggler finishes")
	case <-time.After(time.Second):
		t.Fatal("wait did not return after the counter reached zero")
	}
}

func TestWaitCounterConcurrentAddDoneWithWaiter(t *testing.T) {
	var c waitCounter
	const n = 64
	for range n {
		c.add()
	}
	done := c.doneChan()
	var started sync.WaitGroup
	started.Add(n)
	for range n {
		go func() {
			started.Done()
			c.done()
		}()
	}
	started.Wait()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("doneChan must close exactly once after concurrent dones")
	}
	// A second receive must not block: closed exactly once.
	assertClosed(t, done, "doneChan must stay closed after the single close")
}

// TestWaitCounterConcurrentDoneChanGrabsShareOneChannel drives many concurrent
// doneChan grabs while the counter is held positive: all of them must return
// the one shared channel (the lazily-created c.zero), and all must observe the
// close once the final done() fires AFTER every grab has completed. The grabs
// race each other, not the close -- that interleaving is
// TestWaitCounterDoneChanRacesFinalDone's job.
func TestWaitCounterConcurrentDoneChanGrabsShareOneChannel(t *testing.T) {
	var c waitCounter
	c.add()
	const grabbers = 32
	ch := make(chan (<-chan struct{}), grabbers)
	var ready sync.WaitGroup
	ready.Add(grabbers)
	for range grabbers {
		go func() {
			got := c.doneChan()
			ready.Done()
			ch <- got
		}()
	}
	ready.Wait()
	c.done()
	// One stopped timer shared across all receives, not a time.After per
	// iteration: each of those would pin a live 2s timer on the runtime heap
	// (the cost waitBounded's own doc warns about) for receives that are
	// expected to be instantly ready.
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	var first <-chan struct{}
	for range grabbers {
		got := <-ch
		if first == nil {
			first = got
		} else {
			assert.True(t, first == got)
		}
		select {
		case <-got:
		case <-timeout.C:
			t.Fatal("every concurrent doneChan grab must observe the close")
		}
	}
}

// TestWaitCounterDoneChanRacesFinalDone interleaves doneChan grabs with the
// final done()'s zero-crossing itself: a grabber may win the lock before the
// crossing (shared c.zero, closed by done) or after it (a fresh pre-closed
// channel). Either way the channel it holds must be closed -- no interleaving
// may hand out a channel that never closes. Iterated to actually hit both
// sides of the crossing under -race.
func TestWaitCounterDoneChanRacesFinalDone(t *testing.T) {
	for range 200 {
		var c waitCounter
		c.add()
		const grabbers = 8
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(grabbers)
		for range grabbers {
			go func() {
				defer wg.Done()
				<-start
				got := c.doneChan()
				// NewTimer+Stop, not time.After: 200 iterations x 8 grabbers
				// would otherwise strand 1,600 live 2s timers on the runtime
				// heap for receives that are almost always instantly ready.
				timeout := time.NewTimer(2 * time.Second)
				defer timeout.Stop()
				select {
				case <-got:
				case <-timeout.C:
					t.Error("a doneChan grabbed while the final done raced must still close")
				}
			}()
		}
		close(start)
		c.done()
		wg.Wait()
	}
}
