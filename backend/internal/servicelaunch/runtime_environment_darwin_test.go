package servicelaunch

import (
	"context"
	"testing"
)

func runtimeValidationManager(t *testing.T, spec LaunchSpec) (Manager, *[]string) {
	t.Helper()
	var calls []string
	manager := newLaunchdManager()
	manager.runLaunchctl = func(context.Context, []string, bool) ([]byte, error) {
		calls = append(calls, "launchctl")
		return nil, nil
	}
	manager.readFile = func(string) ([]byte, error) { calls = append(calls, "read"); return manager.definitionPlist(spec), nil }
	manager.writeFile = func(context.Context, LaunchSpec, string, []byte) error { calls = append(calls, "write"); return nil }
	manager.remove = func(context.Context, LaunchSpec, string) error { calls = append(calls, "remove"); return nil }
	return manager, &calls
}
