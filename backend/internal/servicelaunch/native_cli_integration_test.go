//go:build serviceintegration

package servicelaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/lifecycle"
)

// The real CLI owns definition and specification publication in this suite.
// Managers observe the result and remove only the test's identity on failure.
func TestNativeServiceCLIConfiguration(t *testing.T) {
	fixture := readServiceAccountFixture(t)
	binary := testBinaryPath(t)
	assertNativeCLIPrerequisites(t)
	account, err := user.Current()
	require.NoError(t, err)
	for _, mode := range []lifecycle.Role{lifecycle.RoleHub, lifecycle.RoleWorker, lifecycle.RoleSolo, lifecycle.RoleDev} {
		for _, startup := range []lifecycle.StartupKind{lifecycle.StartupLogin, lifecycle.StartupBoot} {
			t.Run(mode.String()+"/"+string(startup), func(t *testing.T) {
				root, err := lifecycle.CanonicalDir(t.TempDir())
				require.NoError(t, err)
				configuration := filepath.Join(t.TempDir(), mode.String()+".yaml")
				require.NoError(t, atomicfile.WriteFileDurable(configuration, []byte("{}\n"), 0o600))
				applicationPath := filepath.Join(root, "application-state.bin")
				application := []byte("preserve application data through native configuration")
				require.NoError(t, atomicfile.WriteFileDurable(applicationPath, application, 0o600))
				expected := lifecycle.ServiceSpec{
					InstanceID: lifecycle.NewInstanceID(fixture.AccountID, mode, root).String(), Mode: mode, Startup: startup,
					AccountID: fixture.AccountID, AccountName: account.Username, Executable: binary,
					WorkingDirectory: account.HomeDir, ConfigFile: configuration, DataDirectory: root,
				}
				launch, err := LaunchSpecFromService(expected)
				require.NoError(t, err)
				manager := nativeIntegrationManager(t, fixture, binary)
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
					defer cancel()
					native, inspectErr := manager.Inspect(ctx, launch)
					if inspectErr != nil {
						t.Errorf("inspect test-owned native configuration %s: %v", expected.InstanceID, inspectErr)
						return
					}
					if native.Running {
						if err := manager.Stop(ctx, launch); err != nil {
							t.Errorf("stop test-owned native configuration %s: %v", expected.InstanceID, err)
						}
					}
					if native.Installed {
						if err := manager.Remove(ctx, launch); err != nil {
							t.Errorf("remove test-owned native configuration %s: %v", expected.InstanceID, err)
						}
					}
				})
				environment := nativeServiceCLIEnvironment(account)
				run := func(verb string) {
					t.Helper()
					args := []string{mode.String(), verb, "--config", configuration, "--data-dir", root}
					if verb == "enable" {
						args = append(args, "--startup="+string(startup), "--yes")
					}
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
					defer cancel()
					result := runNativeCLICommand(ctx, nativeCLICommand{
						program: binary, args: args, directory: root, environment: environment,
						account: account.Username, password: nativeCLIFixturePassword(fixture),
					})
					require.NoError(t, result.err, "%s %s %s: %s", mode, startup, verb, result.output)
				}
				run("enable")
				saved, err := lifecycle.ReadServiceSpec(lifecycle.PathsOf(root).ServiceSpec)
				require.NoError(t, err)
				assert.Equal(t, expected.InstanceID, saved.InstanceID)
				assert.Equal(t, expected.Mode, saved.Mode)
				assert.Equal(t, expected.Startup, saved.Startup)
				assert.Equal(t, expected.AccountID, saved.AccountID)
				assert.Equal(t, expected.AccountName, saved.AccountName)
				assert.Equal(t, expected.Executable, saved.Executable)
				assert.Equal(t, expected.WorkingDirectory, saved.WorkingDirectory)
				assert.Equal(t, expected.ConfigFile, saved.ConfigFile)
				assert.Equal(t, expected.DataDirectory, saved.DataDirectory)
				ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				native, err := manager.Inspect(ctx, launch)
				require.NoError(t, err)
				assert.True(t, native.Installed)
				if !native.DefinitionMatches {
					diagnostic, diagnosticErr := nativeCLIDefinitionDiagnostic(ctx, manager, launch)
					t.Logf("native definition evidence: %s (diagnostic error: %v)", diagnostic, diagnosticErr)
				}
				assert.True(t, native.DefinitionMatches)
				assert.False(t, native.Running, "enable must not start the application")
				assert.Empty(t, native.Failure, "fresh configuration must not report an application failure")
				assert.NoFileExists(t, lifecycle.PathsOf(root).RuntimeRecord)
				statusCommand := exec.CommandContext(ctx, binary, mode.String(), "status", "--config", configuration, "--data-dir", root, "--json")
				statusCommand.Dir, statusCommand.Env = root, environment
				var statusOutput, statusDiagnostic bytes.Buffer
				statusCommand.Stdout, statusCommand.Stderr = &statusOutput, &statusDiagnostic
				statusErr := statusCommand.Run()
				var exit *exec.ExitError
				require.ErrorAs(t, statusErr, &exit, statusDiagnostic.String())
				assert.Equal(t, 3, exit.ExitCode(), "fresh enabled configuration reports the stopped exit code: %s", statusDiagnostic.String())
				var status lifecycle.Status
				require.NoError(t, json.Unmarshal(statusOutput.Bytes(), &status), statusOutput.String())
				assert.True(t, status.Configured)
				assert.Equal(t, lifecycle.ProcessStopped, status.Process.State)
				run("disable")
				run("disable")
				native, err = manager.Inspect(ctx, launch)
				require.NoError(t, err)
				assert.False(t, native.Installed)
				assert.False(t, native.Running)
				assert.NoFileExists(t, lifecycle.PathsOf(root).ServiceSpec)
				assert.NoFileExists(t, lifecycle.PathsOf(root).RuntimeRecord)
				actual, err := os.ReadFile(applicationPath)
				require.NoError(t, err)
				assert.Equal(t, application, actual)
				configBytes, err := os.ReadFile(configuration)
				require.NoError(t, err)
				assert.Equal(t, "{}\n", string(configBytes))
			})
		}
	}
}

func nativeServiceCLIEnvironment(account *user.User) []string {
	var environment []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "LEAPMUX_") || strings.EqualFold(key, "HOME") || strings.EqualFold(key, "USERPROFILE") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "HOME="+account.HomeDir, "USERPROFILE="+account.HomeDir)
}
