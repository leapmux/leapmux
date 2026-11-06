//go:build linux

package servicelaunch

// newPlatformManager builds the systemd adapter.
func newPlatformManager() Manager { return newSystemdManager() }

// nativeMechanismLabel names what systemd configuration writes.
func nativeMechanismLabel() string { return "systemd service" }
