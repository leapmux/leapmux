//go:build windows

package procutil

import "syscall"

type processOwnerPlatform struct {
	job *JobObject
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
