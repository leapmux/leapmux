//go:build windows

package servicelaunch

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"maps"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"

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

func TestServiceConfigurationCarriesTheWholeDefinition(t *testing.T) {
	spec := windowsSpec(t, lifecycle.StartupBoot)
	configuration := windowsServiceConfiguration(spec, []byte("private-password"))
	assert.Equal(t, uint32(windows.SERVICE_WIN32_OWN_PROCESS), configuration.ServiceType)
	assert.Equal(t, uint32(mgr.StartAutomatic), configuration.StartType)
	assert.Equal(t, spec.AccountName, configuration.ServiceStartName)
	assert.Equal(t, "private-password", configuration.Password)
	assert.Equal(t, "LeapMux "+spec.Mode.String(), configuration.DisplayName)
	assert.False(t, configuration.DelayedAutoStart)
	// A failed start during system startup reaches the System event log.
	assert.Equal(t, uint32(windows.SERVICE_ERROR_NORMAL), configuration.ErrorControl)
	assert.Equal(t, uint32(windows.SERVICE_SID_TYPE_NONE), configuration.SidType)
	// UpdateConfig writes the description with the rest of the definition,
	// and windowsServiceMatches compares it, so a changed working directory
	// drifts.
	assert.Equal(t, windowsDefinitionDescription(spec), configuration.Description)
	assert.Contains(t, configuration.Description, spec.WorkingDirectory)
	changed := spec
	changed.WorkingDirectory = filepath.Join(spec.WorkingDirectory, "other")
	assert.NotEqual(t, configuration.Description, windowsServiceConfiguration(changed, nil).Description)
}

func TestServiceConfigurationMatchesComparesEveryWrittenField(t *testing.T) {
	desired := windowsServiceConfiguration(windowsSpec(t, lifecycle.StartupBoot), []byte("private-password"))
	assert.True(t, windowsServiceConfigurationMatches(desired, desired))
	// Each field of mgr.Config, changed alone, and whether the change is drift.
	changes := map[string]struct {
		mutate func(*mgr.Config)
		drift  bool
	}{
		"ServiceType":      {func(c *mgr.Config) { c.ServiceType = windows.SERVICE_WIN32_SHARE_PROCESS }, true},
		"StartType":        {func(c *mgr.Config) { c.StartType = mgr.StartManual }, true},
		"DelayedAutoStart": {func(c *mgr.Config) { c.DelayedAutoStart = true }, true},
		"ErrorControl":     {func(c *mgr.Config) { c.ErrorControl = mgr.ErrorIgnore }, true},
		"SidType":          {func(c *mgr.Config) { c.SidType = windows.SERVICE_SID_TYPE_UNRESTRICTED }, true},
		"DisplayName":      {func(c *mgr.Config) { c.DisplayName += " (changed)" }, true},
		"Description":      {func(c *mgr.Config) { c.Description += " (changed)" }, true},
		// mgr.UpdateConfig cannot clear these, so enable could not repair them.
		"LoadOrderGroup": {func(c *mgr.Config) { c.LoadOrderGroup = "NetworkProvider" }, false},
		"Dependencies":   {func(c *mgr.Config) { c.Dependencies = []string{"Tcpip"} }, false},
		// The installation never writes a tag, and Windows reads one only for
		// a driver.
		"TagId": {func(c *mgr.Config) { c.TagId = 1 }, false},
		// windowsServiceMatches compares these two through their own checks.
		"BinaryPathName":   {func(c *mgr.Config) { c.BinaryPathName = `C:\Other\leapmux.exe` }, false},
		"ServiceStartName": {func(c *mgr.Config) { c.ServiceStartName = `.\other` }, false},
		// QueryServiceConfig never returns the password.
		"Password": {func(c *mgr.Config) { c.Password = "other-password" }, false},
	}
	// A field that a new version of x/sys adds fails here until the table
	// states whether a change to it is drift.
	require.ElementsMatch(t, exportedFieldNames[mgr.Config](), slices.Collect(maps.Keys(changes)), "classify every field of mgr.Config")
	for field, change := range changes {
		t.Run(field, func(t *testing.T) {
			actual := desired
			change.mutate(&actual)
			require.NotEqual(t, desired, actual, "the mutator must change %s", field)
			assert.Equal(t, !change.drift, windowsServiceConfigurationMatches(actual, desired))
		})
	}
}

func TestServiceDefinitionCarriesEveryLaunchField(t *testing.T) {
	base, err := LaunchSpecFromService(testSpec())
	require.NoError(t, err)
	base.Startup = lifecycle.StartupBoot
	base.AccountID = "S-1-5-21-1001"
	render := func(spec LaunchSpec) []byte {
		definition, err := json.Marshal(struct {
			Name          string
			BinaryPath    string
			Configuration mgr.Config
		}{
			Name:          serviceName(spec),
			BinaryPath:    windows.ComposeCommandLine(append([]string{spec.Executable}, windowsServiceArguments(spec)...)),
			Configuration: windowsServiceConfiguration(spec, nil),
		})
		require.NoError(t, err)
		return definition
	}
	assertDefinitionCarries(t, base, render, fieldsCarriedBy(map[string]bool{
		"InstanceID": true,
		"Mode":       true,
		// The startup kind selects the mechanism: windowsAdapter sends a
		// login specification to the scheduled task, so a service always
		// has boot startup, and validateWindowsLaunch refuses any other kind.
		"Startup":     false,
		"AccountID":   true,
		"AccountName": true,
		"Executable":  true,
		// The SCM definition has no working-directory field, so the
		// description carries it.
		"WorkingDirectory": true,
		"DataDirectory":    true,
	}))
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
