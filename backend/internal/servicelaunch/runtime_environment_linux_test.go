package servicelaunch

import (
	"context"
	"testing"
)

func runtimeValidationManager(t *testing.T, spec LaunchSpec) (Manager, *[]string) {
	t.Helper()
	var calls []string
	manager := newSystemdManager()
	manager.runSystemctl = func(context.Context, []string, bool, bool) ([]byte, error) { calls = append(calls, "systemctl"); return nil, nil }
	manager.readFile = func(string) ([]byte, error) { calls = append(calls, "read"); return manager.definitionUnit(spec), nil }
	manager.writeFile = func(context.Context, LaunchSpec, string, []byte) error { calls = append(calls, "write"); return nil }
	manager.remove = func(context.Context, LaunchSpec, string) error { calls = append(calls, "remove"); return nil }
	manager.groupOf = func(string) string { calls = append(calls, "group"); return "service-group" }
	return manager, &calls
}
