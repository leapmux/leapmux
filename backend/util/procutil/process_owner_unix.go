//go:build !windows

package procutil

import (
	"errors"
	"syscall"
)

type processOwnerPlatform struct{}

func (*processOwnerPlatform) attach(ProcessIdentity) error { return nil }

func (*processOwnerPlatform) signalRoot(root ProcessIdentity, signal syscall.Signal) error {
	if root.IsZero() || !root.Runs() {
		return nil
	}
	group, err := syscall.Getpgid(root.PID)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	if group != root.PID {
		_, err := root.Signal(signal)
		return err
	}
	if err := syscall.Kill(-group, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (p *processOwnerPlatform) cancel(root ProcessIdentity) error {
	return p.signalRoot(root, syscall.SIGTERM)
}

func (p *processOwnerPlatform) terminate(root ProcessIdentity) error {
	return p.signalRoot(root, syscall.SIGKILL)
}
