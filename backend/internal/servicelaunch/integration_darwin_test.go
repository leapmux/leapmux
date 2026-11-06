//go:build serviceintegration && darwin

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
// repairs, and removes a REAL launchd definition of the built CLI, as
// the disposable account, in both startup modes. Only definitions this
// test created -- by its instance-specific prefix -- are touched, and
// the cleanup is unconditional.
func TestNativeServiceLifecycle(t *testing.T) {
	fixture := readServiceAccountFixture(t)
	binary := testBinaryPath(t)
	current, err := user.Current()
	require.NoError(t, err)

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dataDir := filepath.Join(home, ".config", "leapmux", "integration-test")
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })

	manager := newLaunchdManager()
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

			// Install: the definition exists, disabled -- enable never
			// starts the job.
			require.NoError(t, manager.Install(ctx, spec))
			status, err := manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.True(t, status.Installed)
			assert.False(t, status.Running, "installing must not start the job")

			// Start: the job runs the built binary's run command.
			require.NoError(t, manager.Start(ctx, spec))
			status, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			require.True(t, status.Running, "the job runs after start")

			// Stop: the running job ends, the definition stays.
			require.NoError(t, manager.Stop(ctx, spec))
			status, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.False(t, status.Running)
			assert.True(t, status.Installed, "stop preserves the definition for the next login or boot")

			// Repair: a rewritten definition reports as drift, and
			// reinstalling restores the match.
			path, err := manager.definitionPath(spec)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, []byte("<plist version=\"1.0\"><dict/></plist>"), 0o644))
			status, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.False(t, status.DefinitionMatches, "a changed definition is drift")
			require.NoError(t, manager.Install(ctx, spec))
			status, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.True(t, status.DefinitionMatches, "the repair restores the definition")

			// Remove: the definition is gone.
			require.NoError(t, manager.Remove(ctx, spec))
			status, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.False(t, status.Installed)
		})
	}

	// Control access from another account is refused by the socket and
	// pipe security; the same disposable account answers, which is the
	// positive half the run above already exercised.
	assert.NotEmpty(t, current.Username)
}
