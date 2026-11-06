//go:build serviceintegration && linux

package servicelaunch

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

// TestNativeServiceLifecycle installs, starts, inspects, stops,
// repairs, and removes a REAL systemd definition of the built CLI, as
// the disposable account with a prepared graphical session. Only
// definitions this test created are touched; the cleanup is
// unconditional.
func TestNativeServiceLifecycle(t *testing.T) {
	fixture := readServiceAccountFixture(t)
	binary := testBinaryPath(t)
	current, err := user.Current()
	require.NoError(t, err)

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dataDir := filepath.Join(home, ".config", "leapmux", "integration-test")
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })

	manager := newSystemdManager()
	for _, startup := range []lifecycle.StartupKind{lifecycle.StartupLogin, lifecycle.StartupBoot} {
		t.Run(string(startup)+" startup", func(t *testing.T) {
			spec := LaunchSpec{
				InstanceID:       "itst0000000000001",
				Mode:             lifecycle.RoleWorker,
				Startup:          startup,
				AccountID:        fixture.AccountID,
				AccountName:      fixture.AccountName,
				Executable:       binary,
				WorkingDirectory: home,
				ConfigFile:       filepath.Join(dataDir, "worker.yaml"),
				DataDirectory:    dataDir,
			}
			t.Cleanup(func() { _ = manager.Remove(context.Background(), spec) })

			ctx := context.Background()

			require.NoError(t, manager.Install(ctx, spec))
			status, err := manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.True(t, status.Installed)
			assert.False(t, status.Running, "enable never starts the service")

			require.NoError(t, manager.Start(ctx, spec))
			status, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			require.True(t, status.Running)

			require.NoError(t, manager.Stop(ctx, spec))
			status, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.False(t, status.Running)
			assert.True(t, status.Installed, "stop preserves the enabled definition")

			path, err := manager.definitionPath(spec)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, []byte("# rewritten\n"), 0o644))
			status, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.False(t, status.DefinitionMatches, "a changed definition is drift")
			require.NoError(t, manager.Install(ctx, spec))
			status, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.True(t, status.DefinitionMatches, "the repair restores the definition")

			require.NoError(t, manager.Remove(ctx, spec))
			status, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.False(t, status.Installed)
		})
	}
	assert.NotEmpty(t, current.Username)
}
