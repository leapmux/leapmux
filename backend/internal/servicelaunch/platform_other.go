//go:build !darwin && !linux && !windows

package servicelaunch

import (
	"context"
	"fmt"
	"runtime"
)

// newPlatformManager supplies the refusing adapter elsewhere.
func newPlatformManager() Manager {
	return &UnsupportedManager{Platform: runtime.GOOS}
}

// nativeMechanismLabel identifies the mechanism generically.
func nativeMechanismLabel() string { return "native startup mechanism" }

type unsupportedSessionMonitor struct{}

func (unsupportedSessionMonitor) Sessions(context.Context, AccountRef) ([]Session, error) {
	return nil, fmt.Errorf("%w: login-session monitoring on %s", ErrUnsupportedPlatform, runtime.GOOS)
}

func newPlatformSessionMonitor() SessionMonitor { return unsupportedSessionMonitor{} }
