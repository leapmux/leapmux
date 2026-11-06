package servicelaunch

import (
	"context"
	"errors"
	"fmt"
)

// ErrUnsupportedPlatform reports a platform with no native startup
// adapter. The controller explains the mechanism it cannot use and
// recommends the foreground `run` command instead of guessing.
var ErrUnsupportedPlatform = errors.New("servicelaunch: this platform has no native startup adapter")

// UnsupportedManager is the Manager a platform without an adapter
// supplies. Every operation states the unsupported platform, so a
// controller never mistakes "no adapter" for "not installed".
type UnsupportedManager struct {
	// Platform names the running platform, for the error.
	Platform string
}

var _ Manager = (*UnsupportedManager)(nil)

func (m *UnsupportedManager) Inspect(context.Context, LaunchSpec) (NativeStatus, error) {
	return NativeStatus{}, m.refuse()
}

func (m *UnsupportedManager) Install(context.Context, LaunchSpec) error { return m.refuse() }

func (m *UnsupportedManager) Start(context.Context, LaunchSpec) error { return m.refuse() }

func (m *UnsupportedManager) Stop(context.Context, LaunchSpec) error { return m.refuse() }

func (m *UnsupportedManager) Remove(context.Context, LaunchSpec) error { return m.refuse() }

func (m *UnsupportedManager) Diagnose(context.Context, LaunchSpec) ([]string, error) {
	return []string{fmt.Sprintf("the %s platform has no native startup adapter; run the mode in the foreground with its run command", m.Platform)}, nil
}

func (m *UnsupportedManager) refuse() error {
	return fmt.Errorf("%w: %s", ErrUnsupportedPlatform, m.Platform)
}
