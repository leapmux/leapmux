//go:build windows

package servicelaunch

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

func windowsSpec(t *testing.T, startup lifecycle.StartupKind) LaunchSpec {
	t.Helper()
	account, err := user.Current()
	require.NoError(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	root, err := lifecycle.CanonicalDir(t.TempDir())
	require.NoError(t, err)
	return LaunchSpec{
		InstanceID: lifecycle.NewInstanceID(account.Uid, lifecycle.RoleWorker, root).String(),
		Mode:       lifecycle.RoleWorker, Startup: startup, AccountID: account.Uid, AccountName: account.Username,
		Executable: executable, WorkingDirectory: account.HomeDir, ConfigFile: filepath.Join(root, "worker.yaml"), DataDirectory: root,
	}
}

func TestServiceDefinitionArguments(t *testing.T) {
	spec := windowsSpec(t, lifecycle.StartupBoot)
	root := filepath.Join(spec.DataDirectory, "data & literal's folder")
	require.NoError(t, os.Mkdir(root, 0o700))
	canonical, err := lifecycle.CanonicalDir(root)
	require.NoError(t, err)
	spec.DataDirectory = canonical
	spec.InstanceID = lifecycle.NewInstanceID(spec.AccountID, spec.Mode, spec.DataDirectory).String()
	manager := newWindowsServiceManager()
	password := []byte("private-password")
	manager.credential = func(context.Context, string) ([]byte, error) { return password, nil }
	installs := 0
	manager.elevate = func(_ context.Context, request nativeOperationRequest) error {
		installs++
		assert.Equal(t, nativeInstallService, request.Operation)
		assert.Equal(t, spec, request.Spec)
		assert.Equal(t, "private-password", string(request.Password))
		assert.NotContains(t, strings.Join(spec.RunArguments(), " "), "private-password")
		encoded := windows.ComposeCommandLine(append([]string{spec.Executable}, windowsServiceArguments(request.Spec)...))
		decoded, err := windows.DecomposeCommandLine(encoded)
		require.NoError(t, err)
		assert.Equal(t, append([]string{spec.Executable}, windowsServiceArguments(spec)...), decoded)
		assert.Contains(t, decoded, root)
		assert.Contains(t, decoded, lifecycle.PathsOf(root).ServiceSpec)
		return nil
	}
	manager.startService = func(context.Context, LaunchSpec) error {
		t.Fatal("configuration must not start the service")
		return nil
	}
	require.NoError(t, manager.Install(t.Context(), spec))
	assert.Equal(t, 1, installs)
	assert.Equal(t, make([]byte, len(password)), password)
}

func TestServiceRefusesDifferentAccountBeforeCredentialOrMutation(t *testing.T) {
	spec := windowsSpec(t, lifecycle.StartupBoot)
	spec.AccountID = "S-1-5-18"
	manager := newWindowsServiceManager()
	manager.credential = func(context.Context, string) ([]byte, error) {
		t.Fatal("a refused account must not read credentials")
		return nil, nil
	}
	manager.elevate = func(context.Context, nativeOperationRequest) error {
		t.Fatal("a refused account must not mutate native state")
		return nil
	}
	require.Error(t, manager.Install(t.Context(), spec))
}

func TestServiceRemovalDoesNotNeedOldExecutableOrEnvironment(t *testing.T) {
	spec := windowsSpec(t, lifecycle.StartupBoot)
	spec.Executable = filepath.Join(t.TempDir(), "removed-leapmux.exe")
	missing := filepath.Join(t.TempDir(), "removed-environment.yaml")
	spec.EnvironmentFile = &missing
	manager := newWindowsServiceManager()
	removed := false
	manager.elevate = func(_ context.Context, request nativeOperationRequest) error {
		removed = true
		assert.Equal(t, nativeRemoveService, request.Operation)
		assert.Empty(t, request.Password)
		return validateNativeOperation(request, spec.AccountID)
	}
	require.NoError(t, manager.Remove(t.Context(), spec))
	assert.True(t, removed)
}

func TestTaskDefinitionArguments(t *testing.T) {
	spec := windowsSpec(t, lifecycle.StartupLogin)
	spec.DataDirectory = filepath.Join(spec.DataDirectory, "literal & folder's name")
	require.NoError(t, os.Mkdir(spec.DataDirectory, 0o700))
	root, err := lifecycle.CanonicalDir(spec.DataDirectory)
	require.NoError(t, err)
	spec.DataDirectory = root
	spec.InstanceID = lifecycle.NewInstanceID(spec.AccountID, spec.Mode, root).String()
	password := []byte("private-password")
	manager := newWindowsTaskManager(func(context.Context, string) ([]byte, error) { return password, nil })
	calls := 0
	manager.upsertTask = func(_ context.Context, actual LaunchSpec, credential []byte) error {
		calls++
		assert.Equal(t, spec, actual)
		assert.Equal(t, "private-password", string(credential))
		arguments := windows.ComposeCommandLine(actual.RunArguments())
		data, err := taskDefinitionXML(actual, arguments)
		require.NoError(t, err)
		var definition windowsTaskDefinition
		require.NoError(t, xml.Unmarshal(data, &definition))
		require.Len(t, definition.Actions.Items, 1)
		decoded, err := windows.DecomposeCommandLine(definition.Actions.Items[0].Arguments)
		require.NoError(t, err)
		assert.Equal(t, actual.RunArguments(), decoded)
		assert.Contains(t, decoded, root)
		assert.Contains(t, decoded, lifecycle.PathsOf(root).ServiceSpec)
		assert.Equal(t, actual.Executable, definition.Actions.Items[0].Command)
		assert.Equal(t, actual.WorkingDirectory, definition.Actions.Items[0].WorkingDirectory)
		assert.NotContains(t, string(data), "private-password")
		return nil
	}
	manager.startTask = func(context.Context, LaunchSpec) error { t.Fatal("configuration must not start the task"); return nil }
	require.NoError(t, manager.Install(t.Context(), spec))
	assert.Equal(t, 1, calls)
	assert.Equal(t, make([]byte, len(password)), password)
}

func TestTaskRequestsOnlyTheRequiredAuthorization(t *testing.T) {
	for _, code := range []uint32{5, 1385, 1326} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			spec := windowsSpec(t, lifecycle.StartupLogin)
			manager := newWindowsTaskManager(func(context.Context, string) ([]byte, error) { return []byte("password"), nil })
			calls := 0
			manager.upsertTask = func(context.Context, LaunchSpec, []byte) error {
				calls++
				if calls == 1 {
					return syscall.Errno(code)
				}
				return nil
			}
			var operations []nativeOperation
			manager.elevate = func(_ context.Context, request nativeOperationRequest) error {
				operations = append(operations, request.Operation)
				if request.Operation == nativeGrantBatchRight {
					assert.Empty(t, request.Password)
				} else {
					assert.NotEmpty(t, request.Password)
				}
				return nil
			}
			err := manager.Install(t.Context(), spec)
			switch code {
			case 5:
				require.NoError(t, err)
				assert.Equal(t, []nativeOperation{nativeInstallTask}, operations)
			case 1385:
				require.NoError(t, err)
				assert.Equal(t, []nativeOperation{nativeGrantBatchRight}, operations)
				assert.Equal(t, 2, calls)
			default:
				require.Error(t, err)
				assert.Empty(t, operations)
				assert.Equal(t, 1, calls)
			}
		})
	}
}

func TestWindowsServiceExitCodesMatchSDK(t *testing.T) {
	assert.Equal(t, uint32(windows.ERROR_SERVICE_NEVER_STARTED), windowsServiceNeverStarted)
	assert.Equal(t, uint32(windows.ERROR_SERVICE_SPECIFIC_ERROR), windowsServiceSpecificError)
}
