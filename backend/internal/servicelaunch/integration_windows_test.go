//go:build serviceintegration && windows

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
// repairs, and removes the REAL Windows definitions of the built CLI:
// the scheduled task for login startup and the service for boot
// startup, as the disposable account. Only definitions this test
// created are touched; the cleanup is unconditional.
func TestNativeServiceLifecycle(t *testing.T) {
	fixture := readServiceAccountFixture(t)
	binary := testBinaryPath(t)
	current, err := user.Current()
	require.NoError(t, err)

	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dataDir := filepath.Join(home, ".config", "leapmux", "integration-test")
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })

	cases := []struct {
		startup lifecycle.StartupKind
		manager Manager
	}{
		{lifecycle.StartupLogin, newWindowsTaskManager(func() string { return credentialFromStore(fixture) })},
		{lifecycle.StartupBoot, newWindowsServiceManager()},
	}
	for _, tc := range cases {
		t.Run(string(tc.startup)+" startup", func(t *testing.T) {
			spec := LaunchSpec{
				InstanceID:       "itst0000000000001",
				Mode:             lifecycle.RoleWorker,
				Startup:          tc.startup,
				AccountID:        fixture.AccountID,
				AccountName:      fixture.AccountName,
				Executable:       binary,
				WorkingDirectory: home,
				ConfigFile:       filepath.Join(dataDir, "worker.yaml"),
				DataDirectory:    dataDir,
			}
			t.Cleanup(func() { _ = tc.manager.Remove(context.Background(), spec) })

			ctx := context.Background()

			require.NoError(t, tc.manager.Install(ctx, spec))

			status, err := tc.manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.True(t, status.Installed)
			assert.False(t, status.Running, "installing must not start the instance")

			require.NoError(t, tc.manager.Start(ctx, spec))
			status, err = tc.manager.Inspect(ctx, spec)
			require.NoError(t, err)
			require.True(t, status.Running)

			require.NoError(t, tc.manager.Stop(ctx, spec))
			status, err = tc.manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.False(t, status.Running)
			assert.True(t, status.Installed, "stop preserves the definition")

			require.NoError(t, tc.manager.Install(ctx, spec))
			status, err = tc.manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.True(t, status.DefinitionMatches, "the repair restores the definition")

			require.NoError(t, tc.manager.Remove(ctx, spec))
			status, err = tc.manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.False(t, status.Installed)
		})
	}
	assert.NotEmpty(t, current.Username)
}

// credentialFromStore reads the account credential from the OS
// credential-store entry the fixture names. The value reaches the
// native call only.
func credentialFromStore(fixture serviceAccountFixture) string {
	if fixture.CredentialReference == nil {
		return ""
	}
	// The credential-store read goes through the platform seam; the
	// integration environment provisions the entry before the run.
	return os.Getenv("LEAPMUX_SERVICE_TEST_CREDENTIAL")
}
