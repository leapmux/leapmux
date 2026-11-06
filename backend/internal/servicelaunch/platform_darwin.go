//go:build darwin

package servicelaunch

// newPlatformManager builds the macOS launchd adapter.
func newPlatformManager() Manager { return newLaunchdManager() }

// nativeMechanismLabel names what launchd configuration writes.
func nativeMechanismLabel() string { return "launchd user agent" }
