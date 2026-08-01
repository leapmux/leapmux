package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

func TestPreTouchPollOAuthError_ExpiresAtBoundary(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	h := &APIAuthHandler{}

	code, _, rejected := h.preTouchPollOAuthError(&store.DeviceAuthorization{ExpiresAt: now}, now)

	require.True(t, rejected)
	assert.Equal(t, "expired_token", code)
}

func TestPreTouchPollOAuthError_ThrottleUsesSuppliedNow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	lastPoll := now.Add(-time.Second)
	h := &APIAuthHandler{}
	row := &store.DeviceAuthorization{
		ExpiresAt:       now.Add(time.Hour),
		LastPolledAt:    &lastPoll,
		IntervalSeconds: 5,
	}

	code, _, rejected := h.preTouchPollOAuthError(row, now)

	require.True(t, rejected)
	assert.Equal(t, "slow_down", code)
}

// TestAPIAuthHandlerNow_DefaultsToTheWallClock pins the PRODUCTION arm of the
// Now seam.
//
// Every handler the tests build sets Now, so without this the nil branch --
// the only one hub/server.go ever takes -- has no coverage at all. A seam whose
// default is broken is worse than no seam: the tests keep passing on their own
// injected clock while the shipped handler stamps garbage.
func TestAPIAuthHandlerNow_DefaultsToTheWallClock(t *testing.T) {
	h := &APIAuthHandler{}

	before := time.Now()
	got := h.now()
	after := time.Now()

	assert.False(t, got.Before(before), "a nil seam must not read behind the wall clock")
	assert.False(t, got.After(after), "a nil seam must not read ahead of the wall clock")
}

// ...and when the seam IS set, every instant the handler compares must come
// from it, or a test that moves the clock forward and a handler that did not
// move with it disagree about the same request.
func TestAPIAuthHandlerNow_UsesTheInjectedClock(t *testing.T) {
	fixed := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	h := &APIAuthHandler{Now: func() time.Time { return fixed }}

	assert.Equal(t, fixed, h.now())

	// The seam reaches the decisions, not just the accessor: an expiry the
	// injected clock has passed must read as expired even though the real
	// clock is nowhere near it.
	code, _, rejected := h.preTouchPollOAuthError(
		&store.DeviceAuthorization{ExpiresAt: fixed.Add(-time.Second)}, h.now())
	require.True(t, rejected)
	assert.Equal(t, "expired_token", code)
}
