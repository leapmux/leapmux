package servicelaunch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

// DescribeElevation states the authorization each startup kind needs,
// before any native tool runs: boot startup elevates with the reason,
// login startup does not.
func TestDescribeElevation(t *testing.T) {
	login := DescribeElevation(lifecycle.StartupLogin, "darwin")
	require.False(t, login.Required)

	boot := DescribeElevation(lifecycle.StartupBoot, "darwin")
	require.True(t, boot.Required)
	assert.NotEmpty(t, boot.Reason, "the reason is stated before the tool runs")
	assert.Equal(t, "sudo", boot.Mechanism)

	windows := DescribeElevation(lifecycle.StartupBoot, "windows")
	assert.Equal(t, "UAC", windows.Mechanism)
}

// The unsupported manager refuses every operation with the sentinel,
// and its diagnose output names the foreground alternative.
func TestUnsupportedManager(t *testing.T) {
	mgr := &UnsupportedManager{Platform: "plan9"}
	ctx := context.Background()
	_, err := mgr.Inspect(ctx, LaunchSpec{})
	require.ErrorIs(t, err, ErrUnsupportedPlatform)
	require.ErrorIs(t, mgr.Install(ctx, LaunchSpec{}), ErrUnsupportedPlatform)
	require.ErrorIs(t, mgr.Start(ctx, LaunchSpec{}), ErrUnsupportedPlatform)
	require.ErrorIs(t, mgr.Stop(ctx, LaunchSpec{}), ErrUnsupportedPlatform)
	require.ErrorIs(t, mgr.Remove(ctx, LaunchSpec{}), ErrUnsupportedPlatform)

	notes, err := mgr.Diagnose(ctx, LaunchSpec{})
	require.NoError(t, err)
	assert.NotEmpty(t, notes)
	assert.Contains(t, notes[0], "foreground")
}
