//go:build linux

package servicelaunch

// newPlatformManager builds the systemd adapter.
func newPlatformManager() Manager { return newSystemdManager() }

func newPlatformSessionMonitor() SessionMonitor { return newLinuxSessionMonitor() }
