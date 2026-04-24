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
		r.cancelAndClear(id)
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

	// A wait after clear should block for roughly the full timeout,
	// not wake immediately on a stale signal.
	start := time.Now()
	_, _, ok := r.waitForPendingResize(id, 50*time.Millisecond)
	assert.False(t, ok, "no resize stashed, should time out")
	assert.GreaterOrEqual(t, time.Since(start), 40*time.Millisecond,
		"wait should block for roughly the full timeout, not wake on a drained signal")
}

// TestStartupCore_WaitForPendingResize_WakesOnSignal covers the normal
// path: a setPendingResize that arrives while a waiter is parked wakes
// it immediately via the chan signal.
func TestStartupCore_WaitForPendingResize_WakesOnSignal(t *testing.T) {
	r := newStartupCore()
	id := "term-wake"
	beginForTest(t, &r, id)

	go func() {
		time.Sleep(5 * time.Millisecond)
		r.setPendingResize(id, 90, 30)
	}()

	start := time.Now()
	cols, rows, ok := r.waitForPendingResize(id, 500*time.Millisecond)
	elapsed := time.Since(start)
	require.True(t, ok)
	assert.Equal(t, uint16(90), cols)
	assert.Equal(t, uint16(30), rows)
	assert.Less(t, elapsed, 200*time.Millisecond,
		"chan signal should wake the waiter in ms, not hit the timeout")
}

// TestStartupCore_WaitForPendingResize_AlreadyStashed covers the fast
// path where dims were already stashed before the wait starts — returns
// synchronously without touching the chan.
func TestStartupCore_WaitForPendingResize_AlreadyStashed(t *testing.T) {
	r := newStartupCore()
	id := "term-prestashed"
	beginForTest(t, &r, id)

	require.True(t, r.setPendingResize(id, 100, 50))
	cols, rows, ok := r.waitForPendingResize(id, 500*time.Millisecond)
	require.True(t, ok)
	assert.Equal(t, uint16(100), cols)
	assert.Equal(t, uint16(50), rows)
}
