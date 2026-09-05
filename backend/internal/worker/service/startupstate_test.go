package service

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// beginForTest records an entry and registers the finishEntry that keeps
// WaitForInFlight happy. Tests use this to exercise the startupCore primitives
// without the full startup-goroutine machinery.
func beginForTest(t *testing.T, r *startupCore, id string) {
	t.Helper()
	entry := r.begin(id, func() {})
	require.NotNil(t, entry)
	t.Cleanup(func() {
		r.cancelAndClear(id, keepWorktreeOnClose)
		r.finishEntry(entry)
	})
}

func TestStartupCore_ArchiveCancellationIsDistinctFromCloseAndFailure(t *testing.T) {
	t.Parallel()

	core := newStartupCore()
	cancelled := false
	handle := core.begin("archive-tab", func() { cancelled = true })
	require.NotNil(t, handle)
	core.markPhase0Complete(handle)

	archivedHandle := core.cancelForArchive("archive-tab")
	require.Same(t, handle, archivedHandle)
	archived, phase0Complete := core.archiveStopped(handle), core.phase0Complete(handle)
	assert.True(t, archived)
	assert.True(t, phase0Complete)
	assert.True(t, cancelled)
	assert.Equal(t, startupWait{settled: true}, core.awaitInFlight("archive-tab", time.Hour),
		"an archived stop must not report a permanent close")
	_, _, _, present := core.snapshot("archive-tab")
	assert.False(t, present, "an archived stop must not leave a startup failure")

	core.finishEntry(handle)
	core.waitForFinished(handle)
	core.WaitForInFlight()
}

// TestStartupCore_ArchiveFindsNoHandleOnceTheStartupFinished pins the invariant
// that makes finishEntry the ONLY exit a startup goroutine may take.
//
// finishEntry drops the in-flight counter AND takes the entry out of the map
// that cancelForArchive resolves a tab id through. An exit that dropped the
// counter alone would leave a live-looking entry whose `finished` channel
// nobody ever closes -- so the next archive of that tab would resolve it, call
// waitForFinished, and block for ever inside the archive RPC.
func TestStartupCore_ArchiveFindsNoHandleOnceTheStartupFinished(t *testing.T) {
	t.Parallel()

	core := newStartupCore()
	handle := core.begin("finished-tab", func() {})
	require.NotNil(t, handle)
	core.succeed("finished-tab", nil)
	core.finishEntry(handle)

	assert.Nil(t, core.cancelForArchive("finished-tab"),
		"a startup that already returned leaves nothing for an archive to wait on")
	core.WaitForInFlight()
}

// TestStartupCore_ClearPendingResize_DrainsSignal pins the contract
// that clearPendingResize empties the buffered resizeSignal along with
// the hasPendingResize bool. Without the drain, a later
// waitForPendingResize on the same entry would wake immediately on the
// stale signal — behavior is still functionally safe because
// takePendingResize re-checks under lock, but the spurious wake
// short-circuits the timeout that callers rely on as the "no resize
// arrived" signal.
func TestStartupCore_ClearPendingResize_DrainsSignal(t *testing.T) {
	t.Parallel()

	r := newStartupCore()
	id := "term-clear-drain"
	beginForTest(t, &r, id)

	require.True(t, r.setPendingResize(id, 120, 40))

	// Capture the chan reference before clearing so we can inspect its
	// buffer independently of later takePendingResize calls.
	r.mu.Lock()
	ch := r.entries[id].resizeSignal
	r.mu.Unlock()
	require.NotNil(t, ch)
	require.Equal(t, 1, len(ch), "setPendingResize should have buffered a signal")

	r.clearPendingResize(id)
	assert.Equal(t, 0, len(ch),
		"clearPendingResize must drain the buffered signal so a later waiter can't wake on it")

	// A wait after clear must block for its full timeout rather than waking
	// immediately on a stale signal. `ok` cannot tell those apart -- both return
	// false -- so the duration is the discriminator, and it is a LOWER bound,
	// which machine load cannot break: load only ever makes this slower, and the
	// failure being guarded against is waking EARLY.
	//
	// The two outcomes are pushed far apart all the same, so the assertion does
	// not depend on a 10ms cushion: a drained-signal wake returns in
	// microseconds, while the timeout path cannot return before 2s.
	start := time.Now()
	_, _, ok := r.waitForPendingResize(id, 2*time.Second)
	assert.False(t, ok, "no resize stashed, should time out")
	assert.GreaterOrEqual(t, time.Since(start), time.Second,
		"wait should block for roughly the full timeout, not wake on a drained signal")
}

// TestStartupCore_WaitForPendingResize_WakesOnSignal covers the normal
// path: a setPendingResize that arrives while a waiter is parked wakes
// it immediately via the chan signal.
func TestStartupCore_WaitForPendingResize_WakesOnSignal(t *testing.T) {
	t.Parallel()

	r := newStartupCore()
	id := "term-wake"
	beginForTest(t, &r, id)

	go func() {
		time.Sleep(5 * time.Millisecond)
		r.setPendingResize(id, 90, 30)
	}()

	// The two outcomes are deliberately FAR apart. `ok` alone cannot tell them
	// apart -- `waitForPendingResize` calls `takePendingResize` after either
	// select arm, so a broken chan signal still returns the right dims once the
	// timer fires -- which makes elapsed time the only discriminator. With a
	// 500ms timeout and a 200ms bound the margin was 300ms, close enough to
	// machine jitter to fail on a loaded box while the signal worked fine.
	//
	// A 30s timeout against a 5s bound keeps exactly the same proof with ~25s of
	// margin instead: the signal path completes in single-digit ms, and the
	// timeout path cannot finish under 30s, so nothing load can do reaches the
	// boundary. The test still exits in milliseconds when it passes.
	start := time.Now()
	cols, rows, ok := r.waitForPendingResize(id, 30*time.Second)
	elapsed := time.Since(start)
	require.True(t, ok)
	assert.Equal(t, uint16(90), cols)
	assert.Equal(t, uint16(30), rows)
	assert.Less(t, elapsed, 5*time.Second,
		"chan signal should wake the waiter in ms, not hit the timeout")
}

// TestStartupCore_WaitForPendingResize_AlreadyStashed covers the fast
// path where dims were already stashed before the wait starts — returns
// synchronously without touching the chan.
func TestStartupCore_WaitForPendingResize_AlreadyStashed(t *testing.T) {
	t.Parallel()

	r := newStartupCore()
	id := "term-prestashed"
	beginForTest(t, &r, id)

	require.True(t, r.setPendingResize(id, 100, 50))
	cols, rows, ok := r.waitForPendingResize(id, 500*time.Millisecond)
	require.True(t, ok)
	assert.Equal(t, uint16(100), cols)
	assert.Equal(t, uint16(50), rows)
}

// TestCancelAndClear_StampsTheHandleTheGoroutineHolds pins how a close reaches
// an in-flight startup: begin() hands the goroutine its entry, cancelAndClear
// removes that entry from the map and stamps it, and the goroutine reads its
// own object afterwards.
func TestCancelAndClear_StampsTheHandleTheGoroutineHolds(t *testing.T) {
	t.Parallel()

	core := newStartupCore()
	h := core.begin("a-live", func() {})

	_, raced := core.dispositionOf(h)
	require.False(t, raced, "nothing has closed this startup yet")

	core.cancelAndClear("a-live", removeWorktreeOnClose)

	got, raced := core.dispositionOf(h)
	require.True(t, raced, "a close that raced a live startup must reach its goroutine")
	assert.Equal(t, removeWorktreeOnClose, got)
	core.finishEntry(h)
}

// TestCancelAndClear_FailedStartupNeverReachesItsGoroutine is the property the
// parallel id-keyed map needed two hand-maintained guards to hold, and that the
// handle gives for free.
//
// fail() installs a FRESH entry for the same id, which lingers for
// failedEntryTTL. A close arriving in that window stamps THAT entry -- not the
// one the (already-returned) goroutine holds -- so it can neither be honoured
// by a goroutine that is gone nor stranded anywhere: it dies with the entry the
// evict timer drops.
func TestCancelAndClear_FailedStartupNeverReachesItsGoroutine(t *testing.T) {
	t.Parallel()

	core := newStartupCore()
	h := core.begin("a-failed", func() {})
	core.fail("a-failed", "boom")
	core.finishEntry(h)

	core.cancelAndClear("a-failed", removeWorktreeOnClose)

	_, raced := core.dispositionOf(h)
	assert.False(t, raced,
		"a close arriving after a failed startup must not reach the returned goroutine's handle")
}

// TestDispositionOf_NilHandleIsAnUncontestedStartup covers the caller that
// never began one (a synchronous prologue failure): it must read as "no close
// raced", which is what leaves the failing startup owning its own rollback.
func TestDispositionOf_NilHandleIsAnUncontestedStartup(t *testing.T) {
	t.Parallel()

	core := newStartupCore()
	got, raced := core.dispositionOf(nil)
	assert.False(t, raced)
	assert.Equal(t, keepWorktreeOnClose, got)
}

// TestStartupCore_BeginClaimsTheIDAgainstASecondStartup pins the claim that
// makes one registry slot belong to one startup.
//
// begin used to overwrite, so a second startup for a tab stranded the first
// one's handle: a later cancelAndClear then cancelled the wrong context and
// stamped its close disposition on the wrong entry, so the first goroutine read
// "no close raced me" and kept a worktree the user had asked to delete.
func TestStartupCore_BeginClaimsTheIDAgainstASecondStartup(t *testing.T) {
	t.Parallel()

	core := newStartupCore()
	firstCancelled := false
	first := core.begin("tab-1", func() { firstCancelled = true })
	require.NotNil(t, first)

	secondCancelled := false
	assert.Nil(t, core.begin("tab-1", func() { secondCancelled = true }),
		"a second startup took the slot; the first one's handle is now unreachable")

	// The FIRST handle must still be the registered one.
	core.cancelAndClear("tab-1", keepWorktreeOnClose)
	assert.True(t, firstCancelled, "the close must reach the startup that owns the slot")
	assert.False(t, secondCancelled)

	core.finishEntry(first)
	core.WaitForInFlight()
}

// TestStartupCore_BeginReclaimsAFailedEntry pins the other half. A failed entry
// is a FINISHED startup that lingers for failedEntryTTL so a status query can
// still read the error. Refusing on it would lock the tab out of every retry for
// those five minutes.
func TestStartupCore_BeginReclaimsAFailedEntry(t *testing.T) {
	t.Parallel()

	core := newStartupCore()
	core.fail("tab-1", "claude: command not found")

	h := core.begin("tab-1", func() {})
	require.NotNil(t, h, "a retry after a failed startup must be able to claim the tab again")
	core.finishEntry(h)
	core.WaitForInFlight()
}

// fakeStartupClock is a deterministic startupTimerClock. NewTimer records the
// requested delay and hands back a channel the test fires explicitly, so
// nothing in awaitInFlight is timed by the wall clock: a test can hold a wait
// in its armed-but-not-expired window for as long as it likes, and a test about
// the give-up path spends no wall time reaching it.
//
// Timers fire in arm order, one per fire() call, which is also the order a real
// clock would fire them in for the one-wait-at-a-time shape awaitInFlight has.
type fakeStartupClock struct {
	mu     sync.Mutex
	timers []chan time.Time
	fired  int
	delays []time.Duration
	armed  chan struct{} // buffered(1); pinged on every NewTimer
}

func newFakeStartupClock() *fakeStartupClock {
	return &fakeStartupClock{armed: make(chan struct{}, 1)}
}

func (c *fakeStartupClock) NewTimer(d time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.timers = append(c.timers, ch)
	c.delays = append(c.delays, d)
	c.mu.Unlock()
	// Buffered(1) and non-blocking: a pending ping already covers the waiter's
	// next check, so a dropped send loses nothing.
	select {
	case c.armed <- struct{}{}:
	default:
	}
	return ch, func() {}
}

// waitArmed blocks until a timer is armed, which is the signal that the code
// under test chose to WAIT rather than answer at once.
func (c *fakeStartupClock) waitArmed(t *testing.T) time.Duration {
	t.Helper()
	select {
	case <-c.armed:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "no wait was armed; the caller answered without waiting")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.delays[len(c.delays)-1]
}

// fire expires the oldest timer that has not fired yet.
func (c *fakeStartupClock) fire(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.Less(t, c.fired, len(c.timers), "no armed timer left to fire")
	c.timers[c.fired] <- time.Now()
	c.fired++
}

// TestStartupCore_AwaitInFlightReturnsAtOnceWhenNothingHoldsTheID pins the two
// states in which begin hands out the claim: an id nobody holds, and one whose
// entry already FAILED. A wait in either is a wait for a startup that is not
// running, and the caller would pay the whole limit for nothing.
func TestStartupCore_AwaitInFlightReturnsAtOnceWhenNothingHoldsTheID(t *testing.T) {
	t.Parallel()

	core := newStartupCore()
	clock := newFakeStartupClock()
	core.clock = clock

	assert.Equal(t, startupWait{settled: true}, core.awaitInFlight("tab-unclaimed", time.Hour))

	core.fail("tab-failed", "claude: command not found")
	assert.Equal(t, startupWait{settled: true}, core.awaitInFlight("tab-failed", time.Hour))

	clock.mu.Lock()
	defer clock.mu.Unlock()
	assert.Empty(t, clock.timers, "neither state may arm a timer")
}

// TestStartupCore_AwaitInFlightWaitsForTheStartupThatHoldsTheID is the wait
// itself: it must not return while the claim holder is still running, and it
// must return the moment that holder is done.
func TestStartupCore_AwaitInFlightWaitsForTheStartupThatHoldsTheID(t *testing.T) {
	t.Parallel()

	// `closed` tells the three transitions apart, and it is the whole reason the
	// wait reports more than "it settled": a caller that must have a process
	// starts one on a startup that ENDED, and must not on one a close TORE
	// DOWN -- the tab is going away, and the row that says so is not written
	// yet.
	for _, tc := range []struct {
		name string
		end  func(core *startupCore)
		want startupWait
	}{
		{"succeed", func(core *startupCore) { core.succeed("tab-1", nil) }, startupWait{settled: true}},
		{"fail", func(core *startupCore) { core.fail("tab-1", "boom") }, startupWait{settled: true}},
		{
			"cancelAndClear",
			func(core *startupCore) { core.cancelAndClear("tab-1", keepWorktreeOnClose) },
			startupWait{settled: true, closed: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			core := newStartupCore()
			clock := newFakeStartupClock()
			core.clock = clock
			holder := core.begin("tab-1", func() {})
			require.NotNil(t, holder)

			done := make(chan startupWait, 1)
			go func() { done <- core.awaitInFlight("tab-1", time.Hour) }()

			clock.waitArmed(t)
			select {
			case <-done:
				require.FailNow(t, "the wait returned while the startup still held the id")
			default:
			}

			tc.end(&core)
			select {
			case got := <-done:
				assert.Equal(t, tc.want, got, "the holder finished, so the wait must report how")
			case <-time.After(10 * time.Second):
				require.FailNow(t, "the wait never returned after the holder finished")
			}
			core.finishEntry(holder)
			core.WaitForInFlight()
		})
	}
}

// TestStartupCore_AwaitInFlightGivesUpOnItsLimit pins the limit. A startup
// goroutine can stop on a provider that never answers, and the caller answers a
// user; a wait with no limit would hold that answer for ever.
func TestStartupCore_AwaitInFlightGivesUpOnItsLimit(t *testing.T) {
	t.Parallel()

	core := newStartupCore()
	clock := newFakeStartupClock()
	core.clock = clock
	holder := core.begin("tab-1", func() {})
	require.NotNil(t, holder)

	done := make(chan startupWait, 1)
	go func() { done <- core.awaitInFlight("tab-1", 90*time.Second) }()

	assert.Equal(t, 90*time.Second, clock.waitArmed(t), "the wait must be armed for the limit it was given")
	clock.fire(t)
	select {
	case got := <-done:
		assert.Equal(t, startupWait{}, got,
			"a wait that expired must report that it settled nothing, and must not claim a close")
	case <-time.After(10 * time.Second):
		require.FailNow(t, "the wait outlived its own limit")
	}

	core.cancelAndClear("tab-1", keepWorktreeOnClose)
	core.finishEntry(holder)
	core.WaitForInFlight()
}

// TestStartupCore_AwaitInFlightIsSafeForManyWaiters pins that the done signal is
// a broadcast, not a handoff: every send that lands in one startup window waits
// on the same entry, and closing the channel is what wakes all of them.
func TestStartupCore_AwaitInFlightIsSafeForManyWaiters(t *testing.T) {
	t.Parallel()

	core := newStartupCore()
	clock := newFakeStartupClock()
	core.clock = clock
	holder := core.begin("tab-1", func() {})
	require.NotNil(t, holder)

	const waiters = 8
	done := make(chan startupWait, waiters)
	for range waiters {
		go func() { done <- core.awaitInFlight("tab-1", time.Hour) }()
	}
	clock.waitArmed(t)

	core.succeed("tab-1", nil)
	for range waiters {
		select {
		case got := <-done:
			assert.Equal(t, startupWait{settled: true}, got)
		case <-time.After(10 * time.Second):
			require.FailNow(t, "a waiter was left behind by the wake-up")
		}
	}
	core.finishEntry(holder)
	core.WaitForInFlight()
}

// TestStartupCore_ReleasingTheSameStartupTwiceIsSafe pins that the done signal
// is closed once per entry. The three state transitions all release, and each
// takes the entry out of the map first -- so a second transition for the same
// id finds nothing and closes nothing. Closing a closed channel panics, and
// that panic would land on whichever of them ran second.
func TestStartupCore_ReleasingTheSameStartupTwiceIsSafe(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		first func(core *startupCore)
		then  func(core *startupCore)
	}{
		{"succeed then succeed",
			func(c *startupCore) { c.succeed("tab-1", nil) },
			func(c *startupCore) { c.succeed("tab-1", nil) }},
		{"succeed then cancelAndClear",
			func(c *startupCore) { c.succeed("tab-1", nil) },
			func(c *startupCore) { c.cancelAndClear("tab-1", keepWorktreeOnClose) }},
		{"succeed then fail",
			func(c *startupCore) { c.succeed("tab-1", nil) },
			func(c *startupCore) { c.fail("tab-1", "boom") }},
		{"cancelAndClear then succeed",
			func(c *startupCore) { c.cancelAndClear("tab-1", keepWorktreeOnClose) },
			func(c *startupCore) { c.succeed("tab-1", nil) }},
		{"fail then fail",
			func(c *startupCore) { c.fail("tab-1", "boom") },
			func(c *startupCore) { c.fail("tab-1", "boom again") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			core := newStartupCore()
			core.clock = newFakeStartupClock()
			holder := core.begin("tab-1", func() {})
			require.NotNil(t, holder)

			tc.first(&core)
			assert.NotPanics(t, func() { tc.then(&core) })
			// Whatever the pair did, the id must settle again without a wait:
			// nothing is in flight for it any more. It settles as UNCLAIMED
			// rather than as closed, because the entry the close stamped is
			// out of the map -- a later caller sees a free id, not a stale
			// close.
			assert.Equal(t, startupWait{settled: true}, core.awaitInFlight("tab-1", time.Hour))

			core.finishEntry(holder)
			core.WaitForInFlight()
		})
	}
}
