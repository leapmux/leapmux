package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func newTestStartupCore(t *testing.T) startupCore {
	t.Helper()
	return newStartupCore(testutil.NewQuartzMock(t))
}

func TestNewStartupCoreRejectsNilClock(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { newStartupCore(nil) })
}

// beginForTest records an entry and returns a cleanup that pairs with
// finish() to keep WaitForInFlight happy. Tests use this to exercise
// the startupCore primitives without the full startup-goroutine
// machinery.
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

	core := newTestStartupCore(t)
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

	core := newTestStartupCore(t)
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

	clock := testutil.NewQuartzMock(t)
	r := newStartupCore(clock)
	ctx := testutil.DeadlineContext(t)
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

	newTimer := clock.Trap().NewTimer(startupPendingResizeTimerTag)
	defer newTimer.Close()
	stopTimer := clock.Trap().TimerStop(startupPendingResizeTimerTag)
	defer stopTimer.Close()

	result := make(chan bool, 1)
	go func() {
		_, _, ok := r.waitForPendingResize(id, 2*time.Second)
		result <- ok
	}()
	call := newTimer.MustWait(ctx)
	assert.Equal(t, 2*time.Second, call.Duration)
	call.MustRelease(ctx)
	// A check of `result` here would assert nothing: the waiter leaves through
	// the trapped Stop below, so `result` is empty on every run, drained signal
	// or not. The end-to-end claim rests on two facts instead. The drain is
	// exact above (len(ch) == 0 after clearPendingResize), and
	// TestStartupCore_WaitForPendingResize_WakesOnSignal proves that a value in
	// that channel DOES end a wait. An undrained channel therefore wakes the
	// next waiter, which is the regression this test refuses. The lines below
	// pin the other half: with the signal drained, only the timer ends the wait.

	advance := clock.Advance(2 * time.Second)
	stopTimer.MustWait(ctx).MustRelease(ctx)
	advance.MustWait(ctx)
	assert.False(t, <-result, "no resize was stashed")
	_, running := clock.Peek()
	assert.False(t, running, "the expired timer must be stopped")
}

// TestStartupCore_WaitForPendingResize_WakesOnSignal covers the normal
// path: a setPendingResize that arrives while a waiter is parked wakes
// it immediately via the chan signal.
func TestStartupCore_WaitForPendingResize_WakesOnSignal(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	r := newStartupCore(clock)
	ctx := testutil.DeadlineContext(t)
	id := "term-wake"
	beginForTest(t, &r, id)

	newTimer := clock.Trap().NewTimer(startupPendingResizeTimerTag)
	defer newTimer.Close()
	stopTimer := clock.Trap().TimerStop(startupPendingResizeTimerTag)
	defer stopTimer.Close()
	type resizeResult struct {
		cols uint16
		rows uint16
		ok   bool
	}
	result := make(chan resizeResult, 1)
	go func() {
		cols, rows, ok := r.waitForPendingResize(id, 30*time.Second)
		result <- resizeResult{cols: cols, rows: rows, ok: ok}
	}()
	call := newTimer.MustWait(ctx)
	assert.Equal(t, 30*time.Second, call.Duration)
	call.MustRelease(ctx)
	require.True(t, r.setPendingResize(id, 90, 30))
	stopTimer.MustWait(ctx).MustRelease(ctx)
	got := <-result
	cols, rows, ok := got.cols, got.rows, got.ok
	require.True(t, ok)
	assert.Equal(t, uint16(90), cols)
	assert.Equal(t, uint16(30), rows)
	_, running := clock.Peek()
	assert.False(t, running, "the signal path must stop its timer")
}

// TestStartupCore_WaitForPendingResize_AlreadyStashed covers the fast
// path where dims were already stashed before the wait starts — returns
// synchronously without touching the chan.
func TestStartupCore_WaitForPendingResize_AlreadyStashed(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	r := newStartupCore(clock)
	id := "term-prestashed"
	beginForTest(t, &r, id)

	require.True(t, r.setPendingResize(id, 100, 50))
	cols, rows, ok := r.waitForPendingResize(id, 500*time.Millisecond)
	require.True(t, ok)
	assert.Equal(t, uint16(100), cols)
	assert.Equal(t, uint16(50), rows)
	_, running := clock.Peek()
	assert.False(t, running, "the fast path must not create a timer")
}

func TestStartupCore_WaitForPendingResizeStopsWhenStartupIsCancelled(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	core := newStartupCore(clock)
	ctx := testutil.DeadlineContext(t)
	cancelledEntry := core.begin("term-cancelled", func() {})
	require.NotNil(t, cancelledEntry)
	newTimer := clock.Trap().NewTimer(startupPendingResizeTimerTag)
	defer newTimer.Close()
	stopTimer := clock.Trap().TimerStop(startupPendingResizeTimerTag)
	defer stopTimer.Close()

	result := make(chan bool, 1)
	go func() {
		_, _, ok := core.waitForPendingResize("term-cancelled", time.Hour)
		result <- ok
	}()
	call := newTimer.MustWait(ctx)
	assert.Equal(t, time.Hour, call.Duration)
	call.MustRelease(ctx)
	core.cancelAndClear("term-cancelled", keepWorktreeOnClose)
	stopTimer.MustWait(ctx).MustRelease(ctx)
	select {
	case ok := <-result:
		assert.False(t, ok)
	case <-ctx.Done():
		require.FailNow(t, "the pending-resize wait ignored startup cancellation")
	}
	core.finishEntry(cancelledEntry)
	_, running := clock.Peek()
	assert.False(t, running, "startup cancellation must stop the pending-resize timer")
}

func TestStartupCore_OldWaitCannotTakeReplacementResize(t *testing.T) {
	t.Parallel()

	core := newTestStartupCore(t)
	oldEntry := core.begin("term-replaced", func() {})
	require.NotNil(t, oldEntry)
	core.fail(oldEntry, "first startup failed")
	core.finishEntry(oldEntry)

	newEntry := core.begin("term-replaced", func() {})
	require.NotNil(t, newEntry)
	require.True(t, core.setPendingResize("term-replaced", 120, 50))
	_, _, ok := core.takePendingResize(oldEntry)
	assert.False(t, ok, "an old wait must not take a replacement startup's resize")
	cols, rows, ok := core.takePendingResize(newEntry)
	require.True(t, ok, "the replacement startup must retain its resize")
	assert.Equal(t, uint16(120), cols)
	assert.Equal(t, uint16(50), rows)

	core.cancelAndClear("term-replaced", keepWorktreeOnClose)
	core.finishEntry(newEntry)
	core.WaitForInFlight()
}

// TestCancelAndClear_StampsTheHandleTheGoroutineHolds pins how a close reaches
// an in-flight startup: begin() hands the goroutine its entry, cancelAndClear
// removes that entry from the map and stamps it, and the goroutine reads its
// own object afterwards.
func TestCancelAndClear_StampsTheHandleTheGoroutineHolds(t *testing.T) {
	t.Parallel()

	core := newTestStartupCore(t)
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

	core := newTestStartupCore(t)
	h := core.begin("a-failed", func() {})
	require.NotNil(t, h)
	core.fail(h, "boom")
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

	core := newTestStartupCore(t)
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

	core := newTestStartupCore(t)
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

	clock := testutil.NewQuartzMock(t)
	core := newStartupCore(clock)
	ctx := testutil.DeadlineContext(t)
	first := core.begin("tab-1", func() {})
	require.NotNil(t, first)
	core.fail(first, "claude: command not found")
	core.finishEntry(first)
	stopTimer := clock.Trap().TimerStop(startupFailedEvictionTag)
	defer stopTimer.Close()

	hCh := make(chan *startupEntry, 1)
	go func() { hCh <- core.begin("tab-1", func() {}) }()
	stopTimer.MustWait(ctx).MustRelease(ctx)
	h := <-hCh
	require.NotNil(t, h, "a retry after a failed startup must be able to claim the tab again")
	_, running := clock.Peek()
	assert.False(t, running, "replacing a failed entry must stop its eviction timer")
	core.cancelAndClear("tab-1", keepWorktreeOnClose)
	core.finishEntry(h)
	core.WaitForInFlight()
}

func TestStartupCore_FailedEntryExpiresExactlyAtTTL(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	core := newStartupCore(clock)
	ctx := testutil.DeadlineContext(t)
	afterFunc := clock.Trap().AfterFunc(startupFailedEvictionTag)
	defer afterFunc.Close()

	h := core.begin("tab-1", func() {})
	require.NotNil(t, h)
	core.finishEntry(h)
	failed := make(chan struct{})
	go func() {
		core.fail(h, "boom")
		close(failed)
	}()
	call := afterFunc.MustWait(ctx)
	assert.Equal(t, failedEntryTTL, call.Duration)
	call.MustRelease(ctx)
	<-failed
	_, errText, _, ok := core.snapshot("tab-1")
	require.True(t, ok)
	assert.Equal(t, "boom", errText)

	clock.Advance(failedEntryTTL - 1).MustWait(ctx)
	_, _, _, ok = core.snapshot("tab-1")
	assert.True(t, ok, "the failed entry must remain before its time-to-live ends")
	clock.Advance(1).MustWait(ctx)
	_, _, _, ok = core.snapshot("tab-1")
	assert.False(t, ok, "the failed entry must leave at its exact time-to-live")
	_, running := clock.Peek()
	assert.False(t, running, "the expired eviction timer must leave no event")
}

// TestStartupCore_CancelAndClearStopsAFailedEntryEviction pins the one
// transition that still reaches a failed entry. fail() installs a FRESH entry
// that no handle points at, so only the id-keyed close can retire it -- a
// retry's begin() is the other route, and TestStartupCore_BeginReclaimsAFailedEntry
// covers that one.
func TestStartupCore_CancelAndClearStopsAFailedEntryEviction(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	core := newStartupCore(clock)
	ctx := testutil.DeadlineContext(t)
	h := core.begin("tab-1", func() {})
	require.NotNil(t, h)
	core.fail(h, "first failure")
	core.finishEntry(h)
	stopTimer := clock.Trap().TimerStop(startupFailedEvictionTag)
	defer stopTimer.Close()

	done := make(chan struct{})
	go func() {
		core.cancelAndClear("tab-1", keepWorktreeOnClose)
		close(done)
	}()
	stopTimer.MustWait(ctx).MustRelease(ctx)
	<-done
	_, running := clock.Peek()
	assert.False(t, running, "a close must stop the failed entry's eviction timer")
	core.WaitForInFlight()
}

// TestStartupCore_StaleTransitionsCannotDisturbAFailedEntry is the other half
// of the identity guard: the handle a startup holds stops matching the map the
// moment fail() replaces the entry, so that goroutine's own succeed() and a
// second fail() must both do nothing.
//
// Without the guard, succeed() deleted whatever the id held and fail() replaced
// it, so a goroutine that already reported its failure could erase the record
// the user is about to read.
func TestStartupCore_StaleTransitionsCannotDisturbAFailedEntry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		stale func(core *startupCore, h *startupEntry)
	}{
		{"succeed", func(core *startupCore, h *startupEntry) { core.succeed(h.id, h) }},
		{"fail again", func(core *startupCore, h *startupEntry) { core.fail(h, "second failure") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			clock := testutil.NewQuartzMock(t)
			core := newStartupCore(clock)
			h := core.begin("tab-1", func() {})
			require.NotNil(t, h)
			core.fail(h, "first failure")
			core.finishEntry(h)

			tc.stale(&core, h)

			_, errText, _, ok := core.snapshot("tab-1")
			require.True(t, ok, "a stale transition must not remove the failed entry")
			assert.Equal(t, "first failure", errText,
				"a stale transition must not overwrite the error the user reads")
			delay, running := clock.Peek()
			assert.True(t, running, "the failed entry must keep its eviction timer")
			assert.Equal(t, failedEntryTTL, delay)

			core.cancelAndClear("tab-1", keepWorktreeOnClose)
			core.WaitForInFlight()
		})
	}
}

// TestStartupCore_StaleTransitionsCannotRetireAReplacement is the reachable
// production case the identity guard exists for. A close retires the entry
// several steps before it stamps closed_at, so a startup goroutine whose
// post-spawn re-read still sees an open row runs its tail AFTER a replacement
// startup claimed the same id.
//
// An id-keyed succeed() then deleted the replacement's entry, and the close
// that followed found nothing to cancel -- so the replacement's context stayed
// live and its process outlived the tab.
func TestStartupCore_StaleTransitionsCannotRetireAReplacement(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		stale func(core *startupCore, h *startupEntry)
	}{
		{"succeed", func(core *startupCore, h *startupEntry) { core.succeed(h.id, h) }},
		{"fail", func(core *startupCore, h *startupEntry) { core.fail(h, "the retired startup failed") }},
		{"take pending resize", func(core *startupCore, h *startupEntry) { core.takePendingResize(h) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			core := newTestStartupCore(t)
			retired := core.begin("tab-1", func() {})
			require.NotNil(t, retired)
			core.cancelAndClear("tab-1", keepWorktreeOnClose)
			core.finishEntry(retired)

			var replacementCancelled bool
			replacement := core.begin("tab-1", func() { replacementCancelled = true })
			require.NotNil(t, replacement, "the close freed the id, so the replacement claims it")
			require.True(t, core.setPendingResize("tab-1", 120, 50))

			tc.stale(&core, retired)

			// The replacement is still the claim holder, so a second startup
			// cannot spawn beside it and its dims are still there to apply.
			assert.Nil(t, core.begin("tab-1", func() {}),
				"a stale transition must not free the id the replacement holds")
			cols, rows, ok := core.takePendingResize(replacement)
			require.True(t, ok, "a stale transition must not consume the replacement's resize")
			assert.Equal(t, uint16(120), cols)
			assert.Equal(t, uint16(50), rows)

			// The close that follows must still reach the replacement's cancel.
			core.cancelAndClear("tab-1", keepWorktreeOnClose)
			assert.True(t, replacementCancelled,
				"the close must still cancel the replacement's startup context")
			core.finishEntry(replacement)
			core.WaitForInFlight()
		})
	}
}

// TestStartupCore_AwaitInFlightReturnsAtOnceWhenNothingHoldsTheID pins the two
// states in which begin hands out the claim: an id nobody holds, and one whose
// entry already FAILED. A wait in either is a wait for a startup that is not
// running, and the caller would pay the whole limit for nothing.
func TestStartupCore_AwaitInFlightReturnsAtOnceWhenNothingHoldsTheID(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	core := newStartupCore(clock)

	assert.Equal(t, startupWait{settled: true}, core.awaitInFlight("tab-unclaimed", time.Hour))
	_, running := clock.Peek()
	assert.False(t, running, "an unclaimed ID must not create a timer")

	h := core.begin("tab-failed", func() {})
	require.NotNil(t, h)
	core.fail(h, "claude: command not found")
	core.finishEntry(h)
	assert.Equal(t, startupWait{settled: true}, core.awaitInFlight("tab-failed", time.Hour))
	delay, running := clock.Peek()
	assert.True(t, running, "the failed entry keeps its eviction timer")
	assert.Equal(t, failedEntryTTL, delay)

	// Peek reports the NEAREST deadline only, so the five-minute eviction timer
	// would hide an hour-long await timer behind it. cancelAndClear stops the
	// eviction timer, and this clock holds no other one, so an empty Peek after
	// it is the exact statement that awaitInFlight armed nothing.
	core.cancelAndClear("tab-failed", keepWorktreeOnClose)
	_, running = clock.Peek()
	assert.False(t, running, "a failed entry must not leave an await timer behind")
	core.WaitForInFlight()
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
		end  func(core *startupCore, h *startupEntry)
		want startupWait
	}{
		{"succeed", func(core *startupCore, h *startupEntry) { core.succeed(h.id, h) }, startupWait{settled: true}},
		{"fail", func(core *startupCore, h *startupEntry) { core.fail(h, "boom") }, startupWait{settled: true}},
		{
			"cancelAndClear",
			func(core *startupCore, _ *startupEntry) { core.cancelAndClear("tab-1", keepWorktreeOnClose) },
			startupWait{settled: true, closed: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			clock := testutil.NewQuartzMock(t)
			core := newStartupCore(clock)
			ctx := testutil.DeadlineContext(t)
			h := core.begin("tab-1", func() {})
			require.NotNil(t, h)
			newTimer := clock.Trap().NewTimer(startupAwaitTimerTag)
			defer newTimer.Close()
			stopTimer := clock.Trap().TimerStop(startupAwaitTimerTag)
			defer stopTimer.Close()

			done := make(chan startupWait, 1)
			go func() { done <- core.awaitInFlight("tab-1", time.Hour) }()

			call := newTimer.MustWait(ctx)
			assert.Equal(t, time.Hour, call.Duration)
			call.MustRelease(ctx)
			select {
			case <-done:
				require.FailNow(t, "the wait returned while the startup still held the id")
			default:
			}

			tc.end(&core, h)
			stopTimer.MustWait(ctx).MustRelease(ctx)
			select {
			case got := <-done:
				assert.Equal(t, tc.want, got, "the holder finished, so the wait must report how")
			case <-ctx.Done():
				require.FailNow(t, "the wait never returned after the holder finished")
			}
			core.cancelAndClear("tab-1", keepWorktreeOnClose)
			core.finishEntry(h)
			core.WaitForInFlight()
		})
	}
}

// TestStartupCore_AwaitInFlightGivesUpOnItsLimit pins the limit. A startup
// goroutine can stop on a provider that never answers, and the caller answers a
// user; a wait with no limit would hold that answer for ever.
func TestStartupCore_AwaitInFlightGivesUpOnItsLimit(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	core := newStartupCore(clock)
	ctx := testutil.DeadlineContext(t)
	h := core.begin("tab-1", func() {})
	require.NotNil(t, h)
	newTimer := clock.Trap().NewTimer(startupAwaitTimerTag)
	defer newTimer.Close()
	stopTimer := clock.Trap().TimerStop(startupAwaitTimerTag)
	defer stopTimer.Close()

	done := make(chan startupWait, 1)
	go func() { done <- core.awaitInFlight("tab-1", 90*time.Second) }()

	call := newTimer.MustWait(ctx)
	assert.Equal(t, 90*time.Second, call.Duration, "the wait must use its supplied limit")
	call.MustRelease(ctx)
	advance := clock.Advance(90 * time.Second)
	stopTimer.MustWait(ctx).MustRelease(ctx)
	advance.MustWait(ctx)
	select {
	case got := <-done:
		assert.Equal(t, startupWait{}, got,
			"a wait that expired must report that it settled nothing, and must not claim a close")
	case <-ctx.Done():
		require.FailNow(t, "the wait outlived its own limit")
	}

	core.cancelAndClear("tab-1", keepWorktreeOnClose)
	core.finishEntry(h)
	core.WaitForInFlight()
}

// TestStartupCore_AwaitInFlightIsSafeForManyWaiters pins that the done signal is
// a broadcast, not a handoff: every send that lands in one startup window waits
// on the same entry, and closing the channel is what wakes all of them.
func TestStartupCore_AwaitInFlightIsSafeForManyWaiters(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	core := newStartupCore(clock)
	ctx := testutil.DeadlineContext(t)
	h := core.begin("tab-1", func() {})
	require.NotNil(t, h)
	newTimer := clock.Trap().NewTimer(startupAwaitTimerTag)
	defer newTimer.Close()
	stopTimer := clock.Trap().TimerStop(startupAwaitTimerTag)
	defer stopTimer.Close()

	const waiters = 8
	done := make(chan startupWait, waiters)
	for range waiters {
		go func() { done <- core.awaitInFlight("tab-1", time.Hour) }()
	}
	for range waiters {
		call := newTimer.MustWait(ctx)
		assert.Equal(t, time.Hour, call.Duration)
		call.MustRelease(ctx)
	}

	core.succeed(h.id, h)
	for range waiters {
		stopTimer.MustWait(ctx).MustRelease(ctx)
	}
	for range waiters {
		select {
		case got := <-done:
			assert.Equal(t, startupWait{settled: true}, got)
		case <-ctx.Done():
			require.FailNow(t, "a waiter was left behind by the wake-up")
		}
	}
	core.finishEntry(h)
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
		first func(core *startupCore, h *startupEntry)
		then  func(core *startupCore, h *startupEntry)
	}{
		{"succeed then succeed",
			func(c *startupCore, h *startupEntry) { c.succeed(h.id, h) },
			func(c *startupCore, h *startupEntry) { c.succeed(h.id, h) }},
		{"succeed then cancelAndClear",
			func(c *startupCore, h *startupEntry) { c.succeed(h.id, h) },
			func(c *startupCore, _ *startupEntry) { c.cancelAndClear("tab-1", keepWorktreeOnClose) }},
		{"succeed then fail",
			func(c *startupCore, h *startupEntry) { c.succeed(h.id, h) },
			func(c *startupCore, h *startupEntry) { c.fail(h, "boom") }},
		{"cancelAndClear then succeed",
			func(c *startupCore, _ *startupEntry) { c.cancelAndClear("tab-1", keepWorktreeOnClose) },
			func(c *startupCore, h *startupEntry) { c.succeed(h.id, h) }},
		{"fail then fail",
			func(c *startupCore, h *startupEntry) { c.fail(h, "boom") },
			func(c *startupCore, h *startupEntry) { c.fail(h, "boom again") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			core := newTestStartupCore(t)
			h := core.begin("tab-1", func() {})
			require.NotNil(t, h)

			tc.first(&core, h)
			assert.NotPanics(t, func() { tc.then(&core, h) })
			// Whatever the pair did, the id must settle again without a wait:
			// nothing is in flight for it any more. It settles as UNCLAIMED
			// rather than as closed, because the entry the close stamped is
			// out of the map -- a later caller sees a free id, not a stale
			// close.
			assert.Equal(t, startupWait{settled: true}, core.awaitInFlight("tab-1", time.Hour))

			core.finishEntry(h)
			core.WaitForInFlight()
		})
	}
}
