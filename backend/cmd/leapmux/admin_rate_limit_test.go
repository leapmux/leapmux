package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/store"
)

func TestCLI_RateLimitSetListEnableDisableReset(t *testing.T) {
	dir := setupTestDataDir(t)

	st := openAdminStore(t, dir)
	require.NoError(t, runRateLimitList(testAdminCtx, []string{"--data-dir", dir}))

	// set persists the override.
	require.NoError(t, runRateLimitSet(testAdminCtx, []string{
		"--operation", "change-password", "--max-attempts", "3", "--window", "5m",
		"--data-dir", dir,
	}))
	row, err := st.RateLimitConfig().Get(context.Background(), "change-password")
	require.NoError(t, err)
	assert.True(t, row.Enabled)
	assert.EqualValues(t, 3, row.MaxAttempts)
	assert.EqualValues(t, 300, row.WindowSeconds)

	// unknown operations and invalid limits are refused.
	err = runRateLimitSet(testAdminCtx, []string{"--operation", "nope", "--max-attempts", "3", "--data-dir", dir})
	require.Error(t, err)
	err = runRateLimitSet(testAdminCtx, []string{"--operation", "change-password", "--max-attempts", "2", "--window", "1s", "--data-dir", dir})
	require.Error(t, err)
	err = runRateLimitSet(testAdminCtx, []string{"--operation", "change-password", "--data-dir", dir})
	require.Error(t, err, "set without any value field must be refused")
	// Sub-second windows are refused, not silently truncated.
	err = runRateLimitSet(testAdminCtx, []string{"--operation", "change-password", "--window", "90.9s", "--data-dir", dir})
	require.ErrorContains(t, err, "not a whole number of seconds")

	// disable keeps numbers, flips the switch.
	require.NoError(t, runRateLimitSetEnabled(testAdminCtx, []string{"--operation", "change-password", "--data-dir", dir}, false))
	row, err = st.RateLimitConfig().Get(context.Background(), "change-password")
	require.NoError(t, err)
	assert.False(t, row.Enabled)
	assert.EqualValues(t, 3, row.MaxAttempts)

	// reset returns to code-side defaults (row gone).
	require.NoError(t, runRateLimitReset(testAdminCtx, []string{"--operation", "change-password", "--data-dir", dir}))
	_, err = st.RateLimitConfig().Get(context.Background(), "change-password")
	assert.ErrorIs(t, err, store.ErrNotFound)
	limits, ok := ratelimit.DefaultLimits("change-password")
	require.True(t, ok)
	assert.EqualValues(t, 5, limits.MaxAttempts)
}

// TestCLI_RateLimitSetExplicitZeroRestoresDefault pins the explicit-flag
// semantics: `--max-attempts 0` restores the default instead of being
// indistinguishable from an unset flag.
func TestCLI_RateLimitSetExplicitZeroRestoresDefault(t *testing.T) {
	dir := setupTestDataDir(t)
	st := openAdminStore(t, dir)

	require.NoError(t, runRateLimitSet(testAdminCtx, []string{
		"--operation", "change-password", "--max-attempts", "9", "--window", "10m",
		"--data-dir", dir,
	}))
	require.NoError(t, runRateLimitSet(testAdminCtx, []string{
		"--operation", "change-password", "--max-attempts", "0", "--data-dir", dir,
	}))
	row, err := st.RateLimitConfig().Get(context.Background(), "change-password")
	require.NoError(t, err)
	assert.EqualValues(t, 5, row.MaxAttempts, "explicit 0 must restore the default")
	assert.EqualValues(t, 600, row.WindowSeconds, "untouched fields keep their stored value")
}
