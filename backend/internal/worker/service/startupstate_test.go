package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// beginForTest records an entry and returns a cleanup that pairs with
// finish() to keep WaitForInFlight happy. Tests use this to exercise
// the startupCore primitives without the full startup-goroutine
// machinery.
func beginForTest(t *testing.T, r *startupCore, id string) {
	t.Helper()
	r.begin(id, func() {})
	t.Cleanup(func() {
		r.cancelAndClear(id, keepWorktreeOnClose)
		r.finish()
	})
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
	core.finish()
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
	core.finish()

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
