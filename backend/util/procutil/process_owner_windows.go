//go:build windows

package procutil

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

func (p *processOwnerPlatform) terminate(ProcessIdentity) error {
	return p.job.Terminate()
}
