//go:build serviceintegration

// This file holds the SHARED half of the explicit native integration
// target (`task test-cli-services`): one lifecycle test and the account
// fixture reader. Platform files supply native managers and access probes.
//
// Ordinary `task test` never reaches this file: the target builds the
// CLI binary first and runs with -tags=serviceintegration only.
package servicelaunch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/servicelaunch/servicefixture"
	"github.com/leapmux/leapmux/lifecycle"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/leapmux/leapmux/util/useronly"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// TestNativeServiceLifecycle is reached only by the explicit, fixture-guarded
// native target. Each case owns a unique data root and derived definition.
func TestNativeServiceLifecycle(t *testing.T) {
	fixture := readServiceAccountFixture(t)
	binary := testBinaryPath(t)
	for _, startup := range []lifecycle.StartupKind{lifecycle.StartupLogin, lifecycle.StartupBoot} {
		t.Run(string(startup), func(t *testing.T) {
			root, err := lifecycle.CanonicalDir(t.TempDir())
			require.NoError(t, err)
			saved := lifecycle.ServiceSpec{
				InstanceID: lifecycle.NewInstanceID(fixture.AccountID, lifecycle.RoleSolo, root).String(),
				Mode:       lifecycle.RoleSolo, Startup: startup, AccountID: fixture.AccountID, AccountName: fixture.AccountName,
				Executable: binary, WorkingDirectory: root, ConfigFile: filepath.Join(root, "solo.yaml"), DataDirectory: root,
				RuntimeArguments:   []string{"--listen", locallistentest.UniqueListenURL(t, "lmx-native-acceptance")},
				RuntimeEnvironment: map[string]string{"PATH": os.Getenv("PATH")},
			}
			lease, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(saved.Mode, root), saved.Mode, root)
			require.NoError(t, err)
			require.NoError(t, lifecycle.WriteServiceSpec(lifecycle.PathsOf(root).ServiceSpec, saved))
			require.NoError(t, lease.Close())
			spec, err := LaunchSpecFromService(saved)
			require.NoError(t, err)
			manager := nativeIntegrationManager(t, fixture, binary)
			removed := false
			t.Cleanup(func() {
				if removed {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				stopErr := manager.Stop(ctx, spec)
				removeErr := manager.Remove(ctx, spec)
				if removeErr != nil {
					t.Errorf("remove the test-owned native definition %s: %v (stop: %v)", spec.InstanceID, removeErr, stopErr)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			require.NoError(t, manager.Install(ctx, spec))
			native, err := manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.True(t, native.Installed)
			assert.True(t, native.DefinitionMatches)
			assert.False(t, native.Running, "enable must not start the application")
			assert.NoFileExists(t, lifecycle.PathsOf(root).RuntimeRecord)
			require.NoError(t, manager.Start(ctx, spec))
			var ready lifecycle.Status
			require.Eventually(t, func() bool {
				status, err := lifecycle.Observe(ctx, root)
				if err != nil || status.Execution == nil || *status.Execution != lifecycle.ExecutionService || status.Readiness == nil || *status.Readiness != lifecycle.ReadinessReady {
					return false
				}
				ready = status
				return status.Registration.State == lifecycle.RegistrationRegistered && status.Connection.State == lifecycle.ConnectionConnected
			}, time.Minute, 100*time.Millisecond, "the native process must reach actual Hub/Worker readiness")
			assert.NotNil(t, ready.Registration.WorkerID)
			record, err := lifecycle.ReadRuntimeRecord(lifecycle.PathsOf(root).RuntimeRecord)
			require.NoError(t, err)
			assertForeignNativeControlDenied(t, record.ControlURL)
			require.NoError(t, manager.Stop(ctx, spec))
			require.Eventually(t, func() bool { status, err := manager.Inspect(ctx, spec); return err == nil && !status.Running }, 30*time.Second, 100*time.Millisecond)
			held, err := lifecycle.ProbeRuntimeLock(lifecycle.PathsOf(root))
			require.NoError(t, err)
			assert.False(t, held, "native stop must join the complete application")
			changed := spec
			changed.WorkingDirectory = t.TempDir()
			require.NoError(t, manager.Install(ctx, changed))
			native, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.False(t, native.DefinitionMatches)
			require.NoError(t, manager.Install(ctx, spec))
			native, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.True(t, native.DefinitionMatches)
			assert.False(t, native.Running)
			require.NoError(t, manager.Remove(ctx, spec))
			removed = true
			native, err = manager.Inspect(ctx, spec)
			require.NoError(t, err)
			assert.False(t, native.Installed)
		})
	}
}

// readServiceAccountFixture loads and validates the fixture. A missing
// fixture, a non-disposable one, or one naming another account than the
// test process runs as is refused BEFORE any native state changes.
func readServiceAccountFixture(t *testing.T) servicefixture.Account {
	t.Helper()
	path := os.Getenv("LEAPMUX_SERVICE_TEST_ACCOUNT_FILE")
	require.NotEmpty(t, path,
		"LEAPMUX_SERVICE_TEST_ACCOUNT_FILE must name the private native-account fixture; run this through `task test-cli-services SERVICE_ACCOUNT_FILE=...`")

	data, err := useronly.ReadFile(path)
	require.NoError(t, err, "read the private fixture")
	current, err := user.Current()
	require.NoError(t, err)
	fixture, err := parseServiceAccountFixture(data, current, runtime.GOOS)
	require.NoError(t, err)
	return fixture
}

func parseServiceAccountFixture(data []byte, current *user.User, platform string) (servicefixture.Account, error) {
	var fixture servicefixture.Account
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fixture, fmt.Errorf("the account fixture must contain one YAML map")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return fixture, fmt.Errorf("the account fixture must contain exactly one YAML document")
	}
	seen := make(map[string]bool)
	mapping := document.Content[0]
	for index := 0; index < len(mapping.Content); index += 2 {
		key, value := mapping.Content[index], mapping.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
			return fixture, fmt.Errorf("the account fixture contains an invalid or duplicate field")
		}
		seen[key.Value] = true
		switch key.Value {
		case "account_id", "account_name":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
				return fixture, fmt.Errorf("%s must be a nonempty string", key.Value)
			}
		case "disposable_environment":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!bool" {
				return fixture, fmt.Errorf("disposable_environment must be a boolean")
			}
		case "credential_reference":
			if platform == "windows" {
				if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || value.Value == "" {
					return fixture, fmt.Errorf("the Windows fixture requires a nonempty operating system credential reference")
				}
			} else if value.Tag != "!!null" {
				return fixture, fmt.Errorf("the Unix fixture requires credential_reference: null")
			}
		default:
			return fixture, fmt.Errorf("the account fixture contains an unsupported field")
		}
	}
	for _, field := range []string{"account_id", "account_name", "credential_reference", "disposable_environment"} {
		if !seen[field] {
			return fixture, fmt.Errorf("the account fixture requires %s", field)
		}
	}
	if err := document.Decode(&fixture); err != nil {
		return fixture, fmt.Errorf("the account fixture has an invalid value")
	}
	if !fixture.DisposableEnvironment {
		return fixture, fmt.Errorf("the fixture must declare a disposable environment")
	}
	if current == nil || fixture.AccountID != current.Uid {
		return fixture, fmt.Errorf("the fixture account identifier differs from the test process account")
	}
	sameName := fixture.AccountName == current.Username
	if platform == "windows" {
		sameName = strings.EqualFold(fixture.AccountName, current.Username)
	}
	if !sameName {
		return fixture, fmt.Errorf("the fixture account name differs from the test process account")
	}
	return fixture, nil
}

// testBinaryPath identifies the just-built CLI the tests install.
func testBinaryPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("LEAPMUX_SERVICE_TEST_BINARY")
	require.NotEmpty(t, path,
		"LEAPMUX_SERVICE_TEST_BINARY must name the just-built leapmux executable; run this through `task test-cli-services`")
	require.True(t, filepath.IsAbs(path), "the built executable path must be absolute")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	return filepath.Clean(path)
}

func TestServiceFixtureBinaryPathUsesNativeSeparators(t *testing.T) {
	binary, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("LEAPMUX_SERVICE_TEST_BINARY", filepath.ToSlash(binary))
	assert.Equal(t, filepath.Clean(binary), testBinaryPath(t))
}
