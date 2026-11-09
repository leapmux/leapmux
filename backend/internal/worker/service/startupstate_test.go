package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/util/drain"
)

func newTestStartupCore(t *testing.T) startupCore {
	t.Helper()
	return newStartupCore(testutil.NewQuartzMock(t))
}

func TestNewStartupCoreRejectsNilClock(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() { newStartupCore(nil) })
}

func TestStartupCoreTracksUnfinishedClaimsAfterStatusCompletion(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		core := newTestStartupCore(t)
		assert.False(t, core.hasUnfinishedClaim("tab"))
		entry := core.begin("tab", func() {})
		require.NotNil(t, entry)
		if failed {
			core.fail(entry, "failed")
		} else {
			core.succeed("tab", entry)
		}
		assert.True(t, core.hasUnfinishedClaim("tab"), "a completed status can still have an unfinished goroutine")
		core.finishEntry(entry)
		assert.False(t, core.hasUnfinishedClaim("tab"))
		core.cancelAndClear("tab", keepWorktreeOnClose)
	}
}

func TestStartupCoreCloseAdmissionCountsOverlappingCloses(t *testing.T) {
	t.Parallel()
	core := newTestStartupCore(t)
	first := core.holdCloseAdmission("tab-1")
	second := core.holdCloseAdmission("tab-1")
	defer first()
	defer second()
	entry, outcome := core.beginColdStart("tab-1", func() {})
	assert.Nil(t, entry)
	assert.Equal(t, claimRefusedClosing, outcome)

	first()
	entry, outcome = core.beginColdStart("tab-1", func() {})
	assert.Nil(t, entry, "the second close still prevents a new start")
	assert.Equal(t, claimRefusedClosing, outcome)

	second()
	entry, outcome = core.beginColdStart("tab-1", func() {})
	require.NotNil(t, entry, "a failed close that leaves a tab open permits a later start")
	assert.Equal(t, claimAdmitted, outcome)
	core.succeed("tab-1", entry)
	core.finishEntry(entry)
}

// TestStartupCore_ColdStartRefusesAFailedEntry pins the one difference between
// beginColdStart and begin. A failed entry records a startup that failed for
// good. A cold start that replaced it would launch the refused startup again
// and erase the failure that the next input must find. begin keeps replacing
// it, because an open and a terminal restart are starts that a user asks for.
func TestStartupCore_ColdStartRefusesAFailedEntry(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	core := newStartupCore(clock)
	first := core.begin("tab-1", func() {})
	require.NotNil(t, first)
	core.fail(first, "could not resume session")
	core.finishEntry(first)

	cancelled := false
	entry, outcome := core.beginColdStart("tab-1", func() { cancelled = true })
	assert.Nil(t, entry)
	assert.Equal(t, claimRefusedFailed, outcome)
	assert.False(t, cancelled, "a refused claim owns no context, so it must not touch the caller's")
	failed, startupError, _, tracked := core.snapshot("tab-1")
	assert.True(t, tracked, "the refused cold start must leave the failure in place")
	assert.True(t, failed)
	assert.Equal(t, "could not resume session", startupError)
	delay, running := clock.Peek()
	assert.True(t, running, "the refusal must keep the failure's eviction timer")
	assert.Equal(t, failedEntryTTL, delay)

	core.cancelAndClear("tab-1", keepWorktreeOnClose)
	core.WaitForInFlight()
}

// TestStartupCore_ColdStartRefusesAStartupInFlight pins the claim's other
// refusal for a cold start, so the caller can report it apart from a failure.
func TestStartupCore_ColdStartRefusesAStartupInFlight(t *testing.T) {
	t.Parallel()

	core := newTestStartupCore(t)
	holder := core.begin("tab-1", func() {})
	require.NotNil(t, holder)

	entry, outcome := core.beginColdStart("tab-1", func() {})
	assert.Nil(t, entry)
	assert.Equal(t, claimRefusedInFlight, outcome)

	core.succeed("tab-1", holder)
	core.finishEntry(holder)
	entry, outcome = core.beginColdStart("tab-1", func() {})
	require.NotNil(t, entry, "a cold start claims a tab whose startup succeeded and released the id")
	assert.Equal(t, claimAdmitted, outcome)
	core.succeed("tab-1", entry)
	core.finishEntry(entry)
	core.WaitForInFlight()
}

// beginForTest owns a synthetic startup claim until test cleanup. A test that
// runs an actual startup body leaves finishEntry to that body instead.
func beginForTest(t *testing.T, r *startupCore, id string) *startupEntry {
	t.Helper()
	entry := r.begin(id, func() {})
	trackStartupForTest(t, r, entry)
	return entry
}

// trackStartupForTest pairs a synthetic claim with cancellation and completion
// before the service fixture joins startup work or closes its database.
func trackStartupForTest(t *testing.T, r *startupCore, entry *startupEntry) {
	t.Helper()
	require.NotNil(t, entry)
	t.Cleanup(func() { r.cancelAndClear(entry.id, keepWorktreeOnClose); r.finishEntry(entry) })
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

	require.True(t, r.setPendingResize(r.entries[id], 120, 40))

	// Capture the chan reference before clearing so we can inspect its
	// buffer independently of later takePendingResize calls.
	r.mu.Lock()
	ch := r.entries[id].resizeSignal
	r.mu.Unlock()
	require.NotNil(t, ch)
	require.Equal(t, 1, len(ch), "setPendingResize should have buffered a signal")

	r.clearPendingResize(r.entries[id])
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
	require.True(t, r.setPendingResize(r.entries[id], 90, 30))
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

	require.True(t, r.setPendingResize(r.entries[id], 100, 50))
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
	require.True(t, core.setPendingResize(core.entries["term-replaced"], 120, 50))
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
			require.True(t, core.setPendingResize(core.entries["tab-1"], 120, 50))

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
	// can start one after a startup that ENDED, and must not after one a close
	// TORE DOWN -- the tab is going away, and the row that says so is not
	// written yet. A startup that FAILED needs no flag: its entry stays in the
	// map, and beginColdStart refuses it.
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
			newTimer, stopTimer := testutil.NewTimerTraps(t, clock, startupAwaitTimerTag)

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
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, startupAwaitTimerTag)

	done := make(chan startupWait, 1)
	go func() { done <- core.awaitInFlight("tab-1", 90*time.Second) }()

	call := newTimer.MustWait(ctx)
	assert.Equal(t, 90*time.Second, call.Duration, "the wait must use its supplied limit")
	call.MustRelease(ctx)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, 90*time.Second, stopTimer)
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
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, startupAwaitTimerTag)

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

// TestStartupCore_AbandonFreesTheIDAndTheInFlightCount pins what an open that
// fails before it launches a goroutine relies on. The id must settle and be
// claimable again, the waiters must wake with no close recorded, the context
// that begin received must end, and the in-flight count must return to zero, or
// Shutdown's WaitForInFlight blocks for ever.
func TestStartupCore_AbandonFreesTheIDAndTheInFlightCount(t *testing.T) {
	t.Parallel()

	core := newTestStartupCore(t)
	cancelled := false
	h := core.begin("tab-1", func() { cancelled = true })
	require.NotNil(t, h)

	core.abandon(h)

	assert.True(t, cancelled, "the context of a startup that never ran must end")
	_, _, _, tracked := core.snapshot("tab-1")
	assert.False(t, tracked, "an abandoned startup must not report STARTING")
	assert.Equal(t, startupWait{settled: true}, core.awaitInFlight("tab-1", time.Hour),
		"nothing is in flight once the startup is abandoned")
	assert.Nil(t, core.cancelForArchive("tab-1"), "an abandoned startup leaves no handle for an archive to find")
	select {
	case <-h.finished:
	default:
		t.Fatal("an abandoned startup must mark its handle as finished")
	}
	drained := make(chan struct{})
	go func() { core.WaitForInFlight(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(inputQueueWait):
		t.Fatal("WaitForInFlight must return once the startup is abandoned")
	}
	again := core.begin("tab-1", func() {})
	require.NotNil(t, again, "an abandoned id must be claimable again")
	core.abandon(again)
}

// A waiter that joined the startup stops waiting when the startup is abandoned,
// and no close is recorded: nobody closed the tab.
func TestStartupCore_AbandonWakesAWaiterWithoutRecordingAClose(t *testing.T) {
	t.Parallel()

	clock := testutil.NewQuartzMock(t)
	core := newStartupCore(clock)
	ctx := testutil.DeadlineContext(t)
	h := core.begin("tab-1", func() {})
	require.NotNil(t, h)
	newTimer, stopTimer := testutil.NewTimerTraps(t, clock, startupAwaitTimerTag)

	done := make(chan startupWait, 1)
	go func() { done <- core.awaitInFlight("tab-1", time.Hour) }()
	newTimer.MustWait(ctx).MustRelease(ctx)

	core.abandon(h)

	stopTimer.MustWait(ctx).MustRelease(ctx)
	select {
	case got := <-done:
		assert.Equal(t, startupWait{settled: true}, got, "the waiter must settle with no close recorded")
	case <-ctx.Done():
		require.FailNow(t, "the wait never returned after the startup was abandoned")
	}
	core.WaitForInFlight()
}

// An abandon through a stale handle leaves the replacement startup alone: the
// slot belongs to whoever claimed it last.
func TestStartupCore_AbandonThroughAStaleHandleKeepsTheReplacement(t *testing.T) {
	t.Parallel()

	core := newTestStartupCore(t)
	stale := core.begin("tab-1", func() {})
	require.NotNil(t, stale)
	core.cancelAndClear("tab-1", keepWorktreeOnClose)
	replacement := core.begin("tab-1", func() {})
	require.NotNil(t, replacement)

	core.abandon(stale)

	_, _, _, tracked := core.snapshot("tab-1")
	assert.True(t, tracked, "a stale handle must not retire the replacement startup")
	core.abandon(replacement)
	core.WaitForInFlight()
}

// A nil handle means that begin claimed nothing, so abandon has nothing to give
// back. It must not touch the in-flight count of another startup.
func TestStartupCore_AbandonOfNilHandleChangesNothing(t *testing.T) {
	t.Parallel()

	core := newTestStartupCore(t)
	h := core.begin("tab-1", func() {})
	require.NotNil(t, h)

	assert.NotPanics(t, func() { core.abandon(nil) })

	core.abandon(h)
	core.WaitForInFlight()
}

// A replacement entry makes the agent read STARTING, as every entry does, and it
// is no startup window: the settings handoff that the window defers a refresh to
// belongs to an open and to a cold start. Every other entry is a window, and so is
// a failed one.
func TestStartupCore_ReplacementIsNoStartupWindow(t *testing.T) {
	t.Parallel()

	core := newTestStartupCore(t)
	replacement := core.beginReplacement("tab-1")
	require.NotNil(t, replacement)
	_, _, _, tracked := core.snapshot("tab-1")
	assert.True(t, tracked, "a replacement must make the agent read STARTING")
	assert.False(t, core.startupWindowOpen("tab-1"), "a replacement must not defer the refresh of a new process")
	core.abandon(replacement)
	assert.False(t, core.startupWindowOpen("tab-1"), "no entry is no window")

	open := core.begin("tab-1", func() {})
	require.NotNil(t, open)
	assert.True(t, core.startupWindowOpen("tab-1"), "an open is a window")
	core.fail(open, "boom")
	core.finishEntry(open)
	assert.True(t, core.startupWindowOpen("tab-1"), "a failed startup stays a window until it is evicted or replaced")
	core.cancelAndClear("tab-1", keepWorktreeOnClose)
	core.WaitForInFlight()
}

// A replacement refuses the id when anything else holds it, and it leaves that
// holder in place.
func TestStartupCore_ReplacementRefusesAnIDThatIsHeld(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		hold func(core *startupCore) (end func())
	}{
		{"a startup in flight", func(core *startupCore) func() {
			h := core.begin("tab-1", func() {})
			require.NotNil(t, h)
			return func() { core.abandon(h) }
		}},
		{"a startup that failed", func(core *startupCore) func() {
			h := core.begin("tab-1", func() {})
			require.NotNil(t, h)
			core.fail(h, "boom")
			core.finishEntry(h)
			return func() { core.cancelAndClear("tab-1", keepWorktreeOnClose) }
		}},
		{"a close in progress", func(core *startupCore) func() {
			return core.holdCloseAdmission("tab-1")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			core := newTestStartupCore(t)
			end := tc.hold(&core)
			wasTracked := func() bool { _, _, _, tracked := core.snapshot("tab-1"); return tracked }
			before := wasTracked()

			assert.Nil(t, core.beginReplacement("tab-1"))

			assert.Equal(t, before, wasTracked(), "a refused replacement must leave the holder's entry alone")
			end()
			core.WaitForInFlight()
		})
	}
}

// beginEndedStartup claims id as the startup of an open does, and ends the claim
// as runAgentStartup does before its tail. The entry leaves the map, and the
// goroutine of the startup still runs, so it did not return. cancelled reports
// whether the context of that startup ended.
func beginEndedStartup(t *testing.T, core *startupCore, id string) (open *startupEntry, cancelled *bool) {
	t.Helper()
	cancelled = new(bool)
	open = core.begin(id, func() { *cancelled = true })
	require.NotNil(t, open)
	core.succeed(id, open)
	return open, cancelled
}

// A process replacement claims the id while the goroutine of an earlier startup
// still runs its tail. A close reaches both. The goroutine of the startup reads
// the disposition of the close from its own entry, and a caller that joined the
// replacement reads it from the entry of the replacement.
func TestStartupCore_ACloseReachesTheStartupBehindAReplacement(t *testing.T) {
	t.Parallel()

	core := newTestStartupCore(t)
	open, openCancelled := beginEndedStartup(t, &core, "tab-1")
	replacement := core.beginReplacement("tab-1")
	require.NotNil(t, replacement, "an ended startup leaves the id free for a replacement")

	core.cancelAndClear("tab-1", removeWorktreeOnClose)

	disposition, raced := core.dispositionOf(open)
	assert.True(t, raced, "the goroutine of the startup that ended must learn that a close landed")
	assert.Equal(t, removeWorktreeOnClose, disposition)
	disposition, raced = core.dispositionOf(replacement)
	assert.True(t, raced, "a caller that joined the replacement must learn that a close landed")
	assert.Equal(t, removeWorktreeOnClose, disposition)
	assert.True(t, *openCancelled, "a close ends the context of every startup that it reaches")

	core.abandon(replacement)
	core.finishEntry(open)
	requireStartupsReleased(t, &core)
}

// An archive stops the startup behind a replacement too, and the caller waits
// for both before it clears the runtime state of the tab. The wait ends only
// after the goroutine of the earlier startup returned.
func TestStartupCore_AnArchiveStopsAndWaitsForTheStartupBehindAReplacement(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		core := newTestStartupCore(t)
		open, _ := beginEndedStartup(t, &core, "tab-1")
		replacement := core.beginReplacement("tab-1")
		require.NotNil(t, replacement)

		archived := core.cancelForArchive("tab-1")

		require.Same(t, replacement, archived, "the archive resolves the newest claim of the id")
		assert.True(t, core.archiveStopped(open), "the goroutine of the startup that ended must learn that an archive landed")
		core.abandon(replacement)
		waited := make(chan struct{})
		go func() {
			core.waitForFinished(archived)
			close(waited)
		}()
		synctest.Wait()
		select {
		case <-waited:
			require.FailNow(t, "the wait ended while the goroutine of the earlier startup still ran")
		default:
		}

		core.finishEntry(open)
		synctest.Wait()
		select {
		case <-waited:
		default:
			require.FailNow(t, "the wait did not end after every claim of the id returned")
		}
		requireStartupsReleased(t, &core)
	})
}

// The end of a replacement hands the id back to the startup behind it, which
// still runs its tail. A close or an archive that lands afterwards must find
// that startup, and no claim of the id may outlive its goroutine.
func TestStartupCore_AReplacementThatEndsLeavesTheStartupBehindItReachable(t *testing.T) {
	t.Parallel()

	core := newTestStartupCore(t)
	open, _ := beginEndedStartup(t, &core, "tab-1")
	replacement := core.beginReplacement("tab-1")
	require.NotNil(t, replacement)
	core.abandon(replacement)

	assert.Same(t, open, core.cancelForArchive("tab-1"), "the startup that still runs its tail must stay reachable")

	core.finishEntry(open)
	assert.Nil(t, core.cancelForArchive("tab-1"), "a startup that returned is not reachable")
	requireStartupsReleased(t, &core)
}

// The goroutine of the earlier startup can return before the replacement ends.
// The replacement then holds the id alone, and its own end leaves nothing behind.
func TestStartupCore_ClaimsOfOneIDThatReturnOutOfOrderLeaveNothingBehind(t *testing.T) {
	t.Parallel()

	core := newTestStartupCore(t)
	open, _ := beginEndedStartup(t, &core, "tab-1")
	replacement := core.beginReplacement("tab-1")
	require.NotNil(t, replacement)

	core.finishEntry(open)
	assert.Same(t, replacement, core.cancelForArchive("tab-1"), "the replacement still holds the id")
	core.abandon(replacement)

	assert.Nil(t, core.cancelForArchive("tab-1"))
	requireStartupsReleased(t, &core)
}

// closeClaims must refuse every claim form, add nothing to the WaitGroup, and
// leave the registry without an in-flight claim, so the WaitForInFlight that
// Shutdown runs directly after it has nothing new to wait for.
func TestClaimRefusedAfterCloseClaims(t *testing.T) {
	clock := testutil.NewQuartzMock(t)
	core := newStartupCore(clock)
	core.closeClaims()

	assert.Nil(t, core.begin("a", nil), "begin must be refused")
	assert.Nil(t, core.beginReplacement("b"), "beginReplacement must be refused")
	entry, outcome := core.beginColdStart("c", nil)
	assert.Nil(t, entry, "beginColdStart must be refused")
	assert.Equal(t, claimRefusedShuttingDown, outcome)

	core.mu.Lock()
	assert.Empty(t, core.inflight, "a refused claim adds nothing in flight")
	core.mu.Unlock()

	done := make(chan struct{})
	go func() {
		core.WaitForInFlight()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForInFlight must return at once after closeClaims")
	}
}

func TestTerminalResizeCompletionPreservesReplacementPending(t *testing.T) {
	core := newTestStartupCore(t)
	old := core.begin("resize-owner", func() {})
	require.NotNil(t, old)
	defer core.finishEntry(old)
	core.cancelAndClear(old.id, keepWorktreeOnClose)
	replacement := core.begin(old.id, func() {})
	require.NotNil(t, replacement)
	defer core.finishEntry(replacement)
	defer core.cancelAndClear(replacement.id, keepWorktreeOnClose)
	require.True(t, core.setPendingResize(core.entries[replacement.id], 180, 50))
	core.clearPendingResize(old)
	cols, rows, ok := core.takePendingResize(replacement)
	require.True(t, ok, "old resize completion must preserve replacement dimensions")
	assert.Equal(t, uint16(180), cols)
	assert.Equal(t, uint16(50), rows)
}

func TestTerminalResizeSerializesPendingApply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		core := newTestStartupCore(t)
		h := core.begin("resize", func() {})
		defer core.finishEntry(h)
		defer core.cancelAndClear(h.id, keepWorktreeOnClose)
		entered, release := make(chan struct{}), make(chan struct{})
		releaseOnce := sync.OnceFunc(func() { close(release) })
		defer releaseOnce()
		require.True(t, core.setPendingResize(h, 100, 30))
		var physical atomic.Uint32
		startupDone := make(chan error, 1)
		go func() {
			startupDone <- core.withTerminalResizeEntry(context.Background(), h, func() error {
				cols, _, ok := core.takePendingResize(h)
				if !ok {
					return fmt.Errorf("startup lost its pending dimensions")
				}
				close(entered)
				<-release
				physical.Store(uint32(cols))
				return nil
			})
		}()
		<-entered
		directDone := make(chan error, 1)
		go func() {
			directDone <- core.withTerminalResize(context.Background(), h.id, func(*startupEntry) error {
				physical.Store(180)
				core.clearPendingResize(h)
				return nil
			})
		}()
		synctest.Wait()
		completed := false
		select {
		case err := <-directDone:
			require.NoError(t, err)
			completed = true
			t.Error("direct resize completed inside pending apply")
		default:
		}
		releaseOnce()
		require.NoError(t, <-startupDone)
		if !completed {
			require.NoError(t, <-directDone)
		}
		synctest.Wait()
		assert.Equal(t, uint32(180), physical.Load())
	})
}

func TestTerminalResizeRejectsCanceledAndRetiredOwners(t *testing.T) {
	for _, mode := range []string{"canceled", "retired", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			core := newTestStartupCore(t)
			h := core.begin("resize", func() {})
			defer core.finishEntry(h)
			defer core.cancelAndClear(h.id, keepWorktreeOnClose)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "canceled":
				cancel()
			case "retired":
				core.cancelAndClear(h.id, keepWorktreeOnClose)
			case "shutdown":
				core.closeClaims()
			}
			called := false
			err := core.withTerminalResizeEntry(ctx, h, func() error { called = true; return nil })
			assert.Error(t, err)
			assert.False(t, called, "invalid owner must not perform physical I/O")
		})
	}
}

func TestTerminalResizeQueuedStartupJoinsBeforeActiveRPC(t *testing.T) {
	for _, mode := range []string{"cancel", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				core := newTestStartupCore(t)
				h := core.begin("resize", func() {})
				entered, release := make(chan struct{}), make(chan struct{})
				releaseOnce := sync.OnceFunc(func() { close(release) })
				defer releaseOnce()
				activeDone := make(chan error, 1)
				go func() {
					activeDone <- core.withTerminalResize(context.Background(), h.id, func(*startupEntry) error {
						close(entered)
						<-release
						return nil
					})
				}()
				<-entered
				startupDone := make(chan error, 1)
				go func() {
					defer core.finishEntry(h)
					startupDone <- core.withTerminalResizeEntry(context.Background(), h, func() error {
						t.Error("retired queued startup performed physical I/O")
						return nil
					})
				}()
				synctest.Wait()
				// A different terminal needs neither the occupied permit nor a global lock.
				require.NoError(t, core.withTerminalResize(context.Background(), "other", func(*startupEntry) error { return nil }))
				if mode == "cancel" {
					core.cancelAndClear(h.id, keepWorktreeOnClose)
				} else {
					core.closeClaims()
					core.closeClaims()
				}
				require.Error(t, <-startupDone)
				core.WaitForInFlight()
				select {
				case err := <-activeDone:
					t.Fatalf("active RPC returned before its physical I/O was released: %v", err)
				default:
				}
				releaseOnce()
				require.NoError(t, <-activeDone)
				core.cancelAndClear(h.id, keepWorktreeOnClose)
			})
		})
	}
}

func TestTerminalResizeMissingOwnerCannotMutateALaterStartup(t *testing.T) {
	core := newTestStartupCore(t)
	var replacement *startupEntry
	err := core.withTerminalResize(context.Background(), "resize", func(owner *startupEntry) error {
		require.Nil(t, owner)
		replacement = core.begin("resize", func() {})
		require.True(t, core.setPendingResize(replacement, 180, 50))
		assert.False(t, core.setPendingResize(owner, 100, 30))
		core.clearPendingResize(owner)
		return nil
	})
	require.NoError(t, err)
	defer core.finishEntry(replacement)
	defer core.cancelAndClear(replacement.id, keepWorktreeOnClose)
	cols, rows, ok := core.takePendingResize(replacement)
	require.True(t, ok)
	assert.Equal(t, uint16(180), cols)
	assert.Equal(t, uint16(50), rows)
}

// resizeStateContext observes an Err snapshot without changing its value.
// The permit test pauses the registry lock after that snapshot.
type resizeStateContext struct {
	context.Context
	calls        atomic.Int32
	observeAt    int32
	snapshots    chan error
	resume       <-chan struct{}
	doneObserved func()
}

func (c *resizeStateContext) Err() error {
	err := c.Context.Err()
	if c.calls.Add(1) == c.observeAt {
		c.snapshots <- err
		if c.resume != nil {
			<-c.resume
		}
	}
	return err
}

func (c *resizeStateContext) Done() <-chan struct{} {
	if c.doneObserved != nil {
		c.doneObserved()
	}
	return c.Context.Done()
}

func TestTerminalResizeCancellationWhileCheckingStateSkipsIO(t *testing.T) {
	for _, withOwner := range []bool{true, false} {
		t.Run(fmt.Sprint(withOwner), func(t *testing.T) {
			core := newTestStartupCore(t)
			parent, cancel := context.WithCancel(testutil.DeadlineContext(t))
			defer cancel()
			observedDone := make(chan struct{})
			resume := make(chan struct{})
			ctx := &resizeStateContext{Context: parent, observeAt: 1, snapshots: make(chan error, 1), doneObserved: sync.OnceFunc(func() { close(observedDone) })}
			var h *startupEntry
			if withOwner {
				h = core.begin("resize", func() {})
				defer core.finishEntry(h)
				defer core.cancelAndClear(h.id, keepWorktreeOnClose)
				h.resizePermit <- struct{}{}
				ctx.observeAt = 2
			} else {
				ctx.resume = resume
			}
			called := false
			done := make(chan error, 1)
			go func() {
				done <- core.withTerminalResize(ctx, "resize", func(*startupEntry) error { called = true; return nil })
			}()
			if withOwner {
				<-observedDone
				core.mu.Lock()
				<-h.resizePermit
				require.NoError(t, <-ctx.snapshots)
			} else {
				require.NoError(t, <-ctx.snapshots)
				core.mu.Lock()
				close(resume)
			}
			cancel()
			core.mu.Unlock()
			assert.ErrorIs(t, <-done, context.Canceled)
			assert.False(t, called, "cancellation during state validation must precede physical I/O")
			if h != nil {
				require.NoError(t, core.withTerminalResizeEntry(context.Background(), h, func() error { return nil }), "canceled request must release the permit")
			}
		})
	}
}

func TestTerminalResizeCallerCancellationReleasesQueuedPermit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		core := newTestStartupCore(t)
		h := core.begin("resize", func() {})
		defer core.finishEntry(h)
		defer core.cancelAndClear(h.id, keepWorktreeOnClose)
		h.resizePermit <- struct{}{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		called := false
		done := make(chan error, 1)
		go func() { done <- core.withTerminalResizeEntry(ctx, h, func() error { called = true; return nil }) }()
		synctest.Wait()
		cancel()
		assert.ErrorIs(t, <-done, context.Canceled)
		assert.False(t, called)
		<-h.resizePermit
		require.NoError(t, core.withTerminalResizeEntry(context.Background(), h, func() error { return nil }))
	})
}

func TestTerminalResizeRejectsFailedAndNilEntries(t *testing.T) {
	core := newTestStartupCore(t)
	h := core.begin("resize", func() {})
	defer core.finishEntry(h)
	defer core.cancelAndClear(h.id, keepWorktreeOnClose)
	core.fail(h, "startup failed")
	core.mu.Lock()
	failed := core.entries[h.id]
	core.mu.Unlock()
	require.NotNil(t, failed)
	require.Nil(t, failed.resizePermit)
	for _, entry := range []*startupEntry{nil, failed} {
		called := false
		assert.ErrorIs(t, core.withTerminalResizeEntry(context.Background(), entry, func() error { called = true; return nil }), errStartupEntryChanged)
		assert.False(t, called)
	}
}

func TestTerminalResizeActiveCompletionCannotMutateReplacement(t *testing.T) {
	for _, clear := range []bool{true, false} {
		t.Run(fmt.Sprint(clear), func(t *testing.T) {
			core := newTestStartupCore(t)
			old := core.begin("resize", func() {})
			defer core.finishEntry(old)
			entered, release := make(chan struct{}), make(chan struct{})
			finish := sync.OnceFunc(func() { close(release) })
			defer finish()
			done := make(chan error, 1)
			go func() {
				done <- core.withTerminalResize(context.Background(), old.id, func(owner *startupEntry) error {
					close(entered)
					<-release
					if clear {
						core.clearPendingResize(owner)
					} else if core.setPendingResize(owner, 100, 30) {
						return fmt.Errorf("old completion changed pending dimensions")
					}
					return nil
				})
			}()
			<-entered
			core.cancelAndClear(old.id, keepWorktreeOnClose)
			replacement := core.begin(old.id, func() {})
			defer core.finishEntry(replacement)
			defer core.cancelAndClear(replacement.id, keepWorktreeOnClose)
			require.NoError(t, core.withTerminalResize(context.Background(), replacement.id, func(owner *startupEntry) error {
				if !core.setPendingResize(owner, 180, 50) {
					return fmt.Errorf("replacement lost resize ownership")
				}
				return nil
			}), "replacement must progress while old physical I/O remains blocked")
			finish()
			require.NoError(t, <-done)
			cols, rows, ok := core.takePendingResize(replacement)
			require.True(t, ok)
			assert.Equal(t, uint16(180), cols)
			assert.Equal(t, uint16(50), rows)
		})
	}
}

func TestTerminalResizeSerializesPendingUpdates(t *testing.T) {
	for _, clear := range []bool{true, false} {
		t.Run(fmt.Sprint(clear), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				core := newTestStartupCore(t)
				h := core.begin("resize", func() {})
				defer core.finishEntry(h)
				defer core.cancelAndClear(h.id, keepWorktreeOnClose)
				require.True(t, core.setPendingResize(h, 90, 24))
				entered, release := make(chan struct{}), make(chan struct{})
				finish := sync.OnceFunc(func() { close(release) })
				defer finish()
				first, second := make(chan error, 1), make(chan error, 1)
				go func() {
					first <- core.withTerminalResize(context.Background(), h.id, func(owner *startupEntry) error {
						close(entered)
						<-release
						if clear {
							core.clearPendingResize(owner)
						} else if !core.setPendingResize(owner, 100, 30) {
							return fmt.Errorf("first resize lost ownership")
						}
						return nil
					})
				}()
				<-entered
				go func() {
					second <- core.withTerminalResize(context.Background(), h.id, func(owner *startupEntry) error {
						if !core.setPendingResize(owner, 180, 50) {
							return fmt.Errorf("second resize lost ownership")
						}
						return nil
					})
				}()
				synctest.Wait()
				completed := false
				select {
				case err := <-second:
					require.NoError(t, err)
					completed = true
					t.Error("second pending update bypassed the first operation")
				default:
				}
				finish()
				require.NoError(t, <-first)
				if !completed {
					require.NoError(t, <-second)
				}
				cols, rows, ok := core.takePendingResize(h)
				require.True(t, ok)
				assert.Equal(t, uint16(180), cols)
				assert.Equal(t, uint16(50), rows)
			})
		})
	}
}

// drainForShutdown waits for a startup until the shutdown deadline, and only
// then cancels it: a startup that finishes before the deadline must not be
// cancelled for the shutdown.
func TestStartupCoreDrainForShutdownCancelsOnlyAtTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const budget = time.Minute
		core := newTestStartupCore(t)
		startupCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		entry := core.begin("agent-1", cancel)
		require.NotNil(t, entry)
		startupReturned := make(chan struct{})
		go func() {
			defer close(startupReturned)
			defer core.finishEntry(entry)
			<-startupCtx.Done()
		}()
		core.closeClaims()

		// The real clock reads the bubble's fake time.
		deadline := drain.NewDeadline(quartz.NewReal(), budget)
		deadline.Start()
		drained := make(chan []string, 1)
		go func() { drained <- core.drainForShutdown(deadline) }()

		synctest.Wait()
		require.NoError(t, startupCtx.Err(), "the drain must not cancel a startup before the deadline")
		select {
		case ids := <-drained:
			t.Fatalf("the drain returned before the deadline: %v", ids)
		default:
		}

		time.Sleep(budget)
		synctest.Wait()
		select {
		case ids := <-drained:
			assert.Equal(t, []string{"agent-1"}, ids, "the drain must report the startup it cancelled")
		default:
			t.Fatal("the drain did not return at the deadline")
		}
		require.ErrorIs(t, startupCtx.Err(), context.Canceled, "the deadline must cancel the startup still in flight")
		assert.True(t, core.shutdownStopped(entry), "the cancelled startup must learn that the shutdown stopped it")
		assert.False(t, core.archiveStopped(entry), "a shutdown is not an archive")
		core.mu.Lock()
		_, held := core.entries["agent-1"]
		core.mu.Unlock()
		assert.False(t, held, "the cancelled startup must leave the registry")
		<-startupReturned
	})
}

// A startup that finished before the deadline is joined, not cancelled, and
// the drain reports nothing.
func TestStartupCoreDrainForShutdownJoinsAFinishedStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		core := newTestStartupCore(t)
		entry := core.begin("agent-1", func() { t.Error("a finished startup must not be cancelled") })
		require.NotNil(t, entry)
		release := make(chan struct{})
		go func() {
			defer core.finishEntry(entry)
			<-release
			core.succeed("agent-1", entry)
		}()
		core.closeClaims()

		deadline := drain.NewDeadline(quartz.NewReal(), time.Minute)
		deadline.Start()
		drained := make(chan []string, 1)
		go func() { drained <- core.drainForShutdown(deadline) }()
		synctest.Wait()
		close(release)
		synctest.Wait()
		select {
		case ids := <-drained:
			assert.Empty(t, ids, "a drain that joined every startup must report none")
		default:
			t.Fatal("the drain did not return when the startup finished")
		}
		assert.False(t, core.shutdownStopped(entry))
	})
}

// cancelForShutdown also reaches a claim whose goroutine still runs behind the
// newest claim of the same id, as a close and an archive do, and it stamps
// each one.
func TestStartupCoreCancelForShutdownReachesEnclosingClaims(t *testing.T) {
	t.Parallel()

	core := newTestStartupCore(t)
	open, openCancelled := beginEndedStartup(t, &core, "tab-1")
	replacement := core.beginReplacement("tab-1")
	require.NotNil(t, replacement)

	ids := core.cancelForShutdown()

	assert.Equal(t, []string{"tab-1"}, ids)
	assert.True(t, core.shutdownStopped(replacement), "the newest claim must learn that the shutdown stopped it")
	assert.True(t, core.shutdownStopped(open), "the claim behind it must learn that too")
	assert.True(t, *openCancelled, "the goroutine behind the newest claim must be cancelled too")
}
