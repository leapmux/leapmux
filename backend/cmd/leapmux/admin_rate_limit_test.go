package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// rateLimitOf reads one operation's stored override the way the hub reads
// it: through the CLI's own settings manager, so the value asserted is the
// value the next command or a running hub would enforce. Omitted fields
// resolve to the catalogue default, so the absent-row case is asserted
// against the raw settings row instead.
func rateLimitOf(t *testing.T, dir, operation string) ratelimit.LimitValue {
	t.Helper()
	key, ok := ratelimit.LimitKey(ratelimit.Operation(operation))
	require.True(t, ok)
	m, _ := adminSettings(t, dir)
	return key.Of(m.Snapshot(context.Background()))
}

func TestCLI_RateLimitSetListEnableDisableReset(t *testing.T) {
	dir := setupTestDataDir(t)

	require.NoError(t, runRateLimitList(testAdminCtx, []string{"--data-dir", dir}))

	// set persists the override.
	require.NoError(t, runRateLimitSet(testAdminCtx, []string{
		"--operation", "change-password", "--max-attempts", "3", "--window", "5m",
		"--data-dir", dir,
	}))
	v := rateLimitOf(t, dir, "change-password")
	assert.True(t, v.Enabled)
	assert.EqualValues(t, 3, v.MaxAttempts)
	assert.EqualValues(t, 300, v.WindowSeconds)

	// unknown operations and invalid limits are refused.
	err := runRateLimitSet(testAdminCtx, []string{"--operation", "nope", "--max-attempts", "3", "--data-dir", dir})
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
	v = rateLimitOf(t, dir, "change-password")
	assert.False(t, v.Enabled)
	assert.EqualValues(t, 3, v.MaxAttempts)

	// reset returns to code-side defaults (row gone).
	require.NoError(t, runRateLimitReset(testAdminCtx, []string{"--operation", "change-password", "--data-dir", dir}))
	st := openAdminStore(t, dir)
	_, err = st.Settings().Get(context.Background(), "rate_limit.change-password")
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

	require.NoError(t, runRateLimitSet(testAdminCtx, []string{
		"--operation", "change-password", "--max-attempts", "9", "--window", "10m",
		"--data-dir", dir,
	}))
	require.NoError(t, runRateLimitSet(testAdminCtx, []string{
		"--operation", "change-password", "--max-attempts", "0", "--data-dir", dir,
	}))
	v := rateLimitOf(t, dir, "change-password")
	assert.EqualValues(t, 5, v.MaxAttempts, "explicit 0 must restore the default")
	assert.EqualValues(t, 600, v.WindowSeconds, "untouched fields keep their stored value")
}
