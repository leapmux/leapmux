package drain

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// assertClosed fails the test unless ch is already closed. Counter channels are
// only ever closed, never sent on, so a ready receive means closed.
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

func TestCounterIdleDoneChanIsAlreadyClosed(t *testing.T) {
	var c Counter
	assertClosed(t, c.DoneChan(), "idle DoneChan must return an already-closed channel")
}

func TestCounterDoneChanClosesOnlyAtZero(t *testing.T) {
	var c Counter
	c.Add()
	c.Add()
	done := c.DoneChan()
	c.Done()
	assertOpen(t, done, "DoneChan must stay open while the counter is still positive")
	c.Done()
	assertClosed(t, done, "DoneChan must close when the counter reaches zero")
}

func TestCounterDoneChanReuseAfterZeroCrossing(t *testing.T) {
	var c Counter
	c.Add()
	a := c.DoneChan()
	c.Done()
	assertClosed(t, a, "channel A must close on the first zero-crossing")

	c.Add()
	b := c.DoneChan()
	assert.True(t, a != b, "a new cycle must return a fresh channel, not reopen A")
	assertOpen(t, b, "channel B must start open")
	c.Done()
	assertClosed(t, b, "channel B must close on the second zero-crossing")
	assertClosed(t, a, "channel A must stay closed after the later cycle")
}

func TestCounterDoneChanSameWhileBusy(t *testing.T) {
	var c Counter
	c.Add()
	a := c.DoneChan()
	b := c.DoneChan()
	assert.True(t, a == b, "both drain phases must observe one signal channel")
	c.Done()
}

func TestCounterDoneOnIdlePanics(t *testing.T) {
	var idle Counter
	assert.PanicsWithValue(t, "drain.Counter: negative counter", func() { idle.Done() })

	var cycled Counter
	cycled.Add()
	cycled.Done()
	assert.PanicsWithValue(t, "drain.Counter: negative counter", func() { cycled.Done() })

	// The panic fires before the counter mutates, so a caller that recovers it
	// (a future recover-middleware) finds the counter consistent: still idle, and
	// a fresh Add/Done cycle still works. A decrement-then-panic ordering would
	// leave n at -1 here, where one Add() masks the corruption as "idle" and a
	// concurrent drain would report completion under live work.
	assertClosed(t, cycled.DoneChan(), "a recovered negative-counter panic must leave the counter idle")
	cycled.Add()
	relatch := cycled.DoneChan()
	assertOpen(t, relatch, "the counter must still track new work after a recovered panic")
	cycled.Done()
	assertClosed(t, relatch, "the counter must still close at zero after a recovered panic")
}

// TestCounterWaitReportsCompletionWithinBudget pins the Wait facade every caller
// drains through: an idle counter wins even on an exhausted budget
// (WaitBounded's fast path), a held counter times out with false, and a counter
// released mid-wait reports true promptly.
func TestCounterWaitReportsCompletionWithinBudget(t *testing.T) {
	var idle Counter
	assert.True(t, idle.Wait(0, ""), "an idle counter must win even on an exhausted budget")

	var busy Counter
	busy.Add()
	assert.False(t, busy.Wait(time.Millisecond, ""), "a held counter must report a timed-out wait")
	assert.False(t, busy.Wait(0, ""),
		"a held counter must fail fast, not hang, on an exhausted budget -- the shape drainHandlers' shared deadline feeds it")
	busy.Done()

	var joined Counter
	joined.Add()
	result := make(chan bool, 1)
	go func() { result <- joined.Wait(2*time.Second, "") }()
	joined.Done()
	select {
	case ok := <-result:
		assert.True(t, ok, "Wait must report success once the straggler finishes")
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after the counter reached zero")
	}
}

func TestCounterConcurrentAddDoneWithWaiter(t *testing.T) {
	var c Counter
	const n = 64
	for range n {
		c.Add()
	}
	done := c.DoneChan()
	var started sync.WaitGroup
	started.Add(n)
	for range n {
		go func() {
			started.Done()
			c.Done()
		}()
	}
	started.Wait()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DoneChan must close exactly once after concurrent dones")
	}
	// A second receive must not block: closed exactly once.
	assertClosed(t, done, "DoneChan must stay closed after the single close")
}

// TestCounterConcurrentDoneChanGrabsShareOneChannel drives many concurrent
// DoneChan grabs while the counter is held positive: all of them must return the
// one shared channel (the lazily-created c.zero), and all must observe the close
// once the final Done() fires AFTER every grab has completed. The grabs race
// each other, not the close -- that interleaving is
// TestCounterDoneChanRacesFinalDone's job.
func TestCounterConcurrentDoneChanGrabsShareOneChannel(t *testing.T) {
	var c Counter
	c.Add()
	const grabbers = 32
	ch := make(chan (<-chan struct{}), grabbers)
	var ready sync.WaitGroup
	ready.Add(grabbers)
	for range grabbers {
		go func() {
			got := c.DoneChan()
			ready.Done()
			ch <- got
		}()
	}
	ready.Wait()
	c.Done()
	// One stopped timer shared across all receives, not a time.After per
	// iteration: each of those would pin a live 2s timer on the runtime heap
	// (the cost WaitBounded's own doc warns about) for receives that are
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
			t.Fatal("every concurrent DoneChan grab must observe the close")
		}
	}
}

// TestCounterDoneChanRacesFinalDone interleaves DoneChan grabs with the final
// Done()'s zero-crossing itself: a grabber may win the lock before the crossing
// (shared c.zero, closed by Done) or after it (a fresh pre-closed channel).
// Either way the channel it holds must be closed -- no interleaving may hand out
// a channel that never closes. Iterated to actually hit both sides of the
// crossing under -race.
func TestCounterDoneChanRacesFinalDone(t *testing.T) {
	for range 200 {
		var c Counter
		c.Add()
		const grabbers = 8
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(grabbers)
		for range grabbers {
			go func() {
				defer wg.Done()
				<-start
				got := c.DoneChan()
				// NewTimer+Stop, not time.After: 200 iterations x 8 grabbers
				// would otherwise strand 1,600 live 2s timers on the runtime
				// heap for receives that are almost always instantly ready.
				timeout := time.NewTimer(2 * time.Second)
				defer timeout.Stop()
				select {
				case <-got:
				case <-timeout.C:
					t.Error("a DoneChan grabbed while the final Done raced must still close")
				}
			}()
		}
		close(start)
		c.Done()
		wg.Wait()
	}
}

// TestWaitBoundedFastPathBeatsAnExhaustedBudget pins the guard that makes the
// shared-deadline callers correct: a already-closed channel must win outright
// even at timeout <= 0, where a bare select would pick between two ready cases
// at random and report a straggler that had in fact finished.
func TestWaitBoundedFastPathBeatsAnExhaustedBudget(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	for range 100 {
		assert.True(t, WaitBounded(closed, 0, ""),
			"a finished waiter must win even on an exhausted budget, every time")
	}

	open := make(chan struct{})
	assert.False(t, WaitBounded(open, time.Millisecond, ""),
		"an unfinished waiter must report the timeout")
}

// Pending is what lets a drain test prove the waiter has actually PARKED before
// releasing the work it waits on -- the alternative is a sleep that loses the
// race and silently exercises the pre-closed fast path instead of the mid-wait
// join.
func TestCounterPendingTracksALiveCompletionChannel(t *testing.T) {
	var c Counter
	assert.False(t, c.Pending(), "an idle counter has no waiter to release")

	c.Add()
	assert.False(t, c.Pending(),
		"work alone is not a waiter: the channel is created lazily, by DoneChan")

	done := c.DoneChan()
	assert.True(t, c.Pending(), "a handed-out channel that has not closed is pending")

	c.Done()
	assertClosed(t, done, "sanity: the wait completed")
	assert.False(t, c.Pending(), "the zero-crossing clears it")
}
