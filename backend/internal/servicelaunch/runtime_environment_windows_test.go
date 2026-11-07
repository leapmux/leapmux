package servicelaunch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

func windowsRuntimeValidationManager(startup lifecycle.StartupKind) (Manager, *[]string) {
	var calls []string
	credential := func(context.Context, string) ([]byte, error) { calls = append(calls, "credential"); return nil, assert.AnError }
	inspect := func(context.Context, LaunchSpec) (NativeStatus, error) { calls = append(calls, "inspect"); return NativeStatus{}, nil }
	elevate := func(context.Context, nativeOperationRequest) error { calls = append(calls, "elevate"); return nil }
	start := func(context.Context, LaunchSpec) error { calls = append(calls, "start"); return nil }
	if startup == lifecycle.StartupBoot {
		manager := newWindowsServiceManager()
		manager.credential, manager.inspectService, manager.elevate, manager.startService = credential, inspect, elevate, start
		manager.stopService = func(context.Context, LaunchSpec) error { calls = append(calls, "stop"); return nil }
		return manager, &calls
	}
	manager := newWindowsTaskManager(credential)
	manager.inspectTask, manager.elevate, manager.startTask = inspect, elevate, start
	manager.upsertTask = func(context.Context, LaunchSpec, []byte) error { calls = append(calls, "upsert"); return nil }
	manager.removeTask = func(context.Context, LaunchSpec) error { calls = append(calls, "remove"); return nil }
	manager.stopRuntime = func(context.Context, lifecycle.Role, string) (bool, error) { calls = append(calls, "stop runtime"); return false, nil }
	return manager, &calls
}

func TestWindowsRuntimeValidationPrecedesCredentialsAndNativeActions(t *testing.T) {
	for kind, path := range invalidEnvironmentFiles(t) {
		for _, startup := range []lifecycle.StartupKind{lifecycle.StartupBoot, lifecycle.StartupLogin} {
			t.Run(kind+"/"+string(startup), func(t *testing.T) {
				spec := windowsSpec(t, startup)
				spec.EnvironmentFile = &path
				manager, calls := windowsRuntimeValidationManager(startup)
				for _, action := range []func(context.Context, LaunchSpec) error{manager.Install, manager.Start} {
					require.ErrorContains(t, action(t.Context(), spec), "environment file")
					assert.Empty(t, *calls)
				}
			})
		}
	}
}

func TestWindowsInspectionAndRemovalIgnoreBrokenRuntimeSelectors(t *testing.T) {
	for kind, path := range invalidEnvironmentFiles(t) {
		for _, startup := range []lifecycle.StartupKind{lifecycle.StartupBoot, lifecycle.StartupLogin} {
			t.Run(kind+"/"+string(startup), func(t *testing.T) {
				spec := windowsSpec(t, startup)
				spec.EnvironmentFile = &path
				manager, calls := windowsRuntimeValidationManager(startup)
				_, err := manager.Inspect(t.Context(), spec)
				require.NoError(t, err)
				assert.NotEmpty(t, *calls)
				for _, action := range []func(context.Context, LaunchSpec) error{manager.Stop, manager.Remove} {
					before := len(*calls)
					require.NoError(t, action(t.Context(), spec))
					assert.Greater(t, len(*calls), before)
				}
				assert.NotContains(t, *calls, "credential")
			})
		}
	}
}
