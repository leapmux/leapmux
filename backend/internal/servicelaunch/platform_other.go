//go:build !darwin && !linux && !windows

package servicelaunch

import "runtime"

// newPlatformManager supplies the refusing adapter elsewhere.
func newPlatformManager() Manager {
	return &UnsupportedManager{Platform: runtime.GOOS}
}

// nativeMechanismLabel names the mechanism generically.
func nativeMechanismLabel() string { return "native startup mechanism" }
