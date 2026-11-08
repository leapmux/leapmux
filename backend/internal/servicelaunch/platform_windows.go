//go:build windows

package servicelaunch

import (
	"context"

	"github.com/leapmux/leapmux/lifecycle"
)

// windowsAdapter dispatches between the task (login) and service
// (boot) adapters by the specification's startup kind.
type windowsAdapter struct {
	task    *windowsTaskManager
	service *windowsServiceManager
}

func (m *windowsAdapter) Inspect(ctx context.Context, spec LaunchSpec) (NativeStatus, error) {
	return m.forSpec(spec).Inspect(ctx, spec)
}
func (m *windowsAdapter) Install(ctx context.Context, spec LaunchSpec) error {
	return m.forSpec(spec).Install(ctx, spec)
}
func (m *windowsAdapter) Start(ctx context.Context, spec LaunchSpec) error {
	return m.forSpec(spec).Start(ctx, spec)
}
func (m *windowsAdapter) Stop(ctx context.Context, spec LaunchSpec) error {
	return m.forSpec(spec).Stop(ctx, spec)
}
func (m *windowsAdapter) Remove(ctx context.Context, spec LaunchSpec) error {
	return m.forSpec(spec).Remove(ctx, spec)
}
func (m *windowsAdapter) Diagnose(ctx context.Context, spec LaunchSpec) ([]string, error) {
	return m.forSpec(spec).Diagnose(ctx, spec)
}
func (m *windowsAdapter) forSpec(spec LaunchSpec) Manager {
	if spec.Startup == lifecycle.StartupBoot {
		return m.service
	}
	return m.task
}

// newPlatformManager builds the Windows adapter pair.
func newPlatformManager() Manager {
	return &windowsAdapter{task: newWindowsTaskManager(nil), service: newWindowsServiceManager()}
}

func newPlatformSessionMonitor() SessionMonitor { return newWindowsSessionMonitor() }
