//go:build windows

package procutil

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

type processOwnerPlatform struct {
	job *JobObject
}

// The command's handle retains the exact identity even when a short command
// exits before Start reads it. A PID lookup that requires a live process loses it.
func identifyStartedProcess(process *os.Process) (identity ProcessIdentity, identified bool) {
	var identityErr error
	handleErr := process.WithHandle(func(handle uintptr) {
		identity, _, identityErr = (windowsJobProcess{handle: windows.Handle(handle)}).Identity()
	})
	return identity, handleErr == nil && identityErr == nil && !identity.IsZero() && identity.StartTime > 0
}

func (p *processOwnerPlatform) attach(root ProcessIdentity) error {
	handle, err := assignOwnedJob(root, windowsJobDriver{})
	if err != nil {
		return err
	}
	p.job = &JobObject{}
	p.job.handle.Store(handle)
	return nil
}

func (p *processOwnerPlatform) cancel(ProcessIdentity) error {
	return p.job.Terminate()
}

func (*processOwnerPlatform) signalRoot(root ProcessIdentity, signal syscall.Signal) error {
	_, err := root.Signal(signal)
	return err
}

func (p *processOwnerPlatform) terminate(ProcessIdentity) error {
	return p.job.Terminate()
}
