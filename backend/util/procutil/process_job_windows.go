//go:build windows

package procutil

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsJobDriver struct{}

type windowsJobProcess struct{ handle windows.Handle }

func (windowsJobDriver) CreateJob() (uintptr, error) {
	handle, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create the Windows job: %w", err)
	}
	return uintptr(handle), nil
}

func (windowsJobDriver) ConfigureJob(job uintptr) error {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE}}
	if _, err := windows.SetInformationJobObject(windows.Handle(job), windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return fmt.Errorf("configure the Windows job: %w", err)
	}
	return nil
}

func (windowsJobDriver) OpenProcess(pid int) (heldJobProcess, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil, fmt.Errorf("open the Windows process: %w", err)
	}
	return windowsJobProcess{handle: handle}, nil
}

func (windowsJobDriver) CloseJob(handle uintptr) error {
	return windows.CloseHandle(windows.Handle(handle))
}

func (windowsJobDriver) TerminateJob(handle uintptr) error {
	return windows.TerminateJobObject(windows.Handle(handle), 1)
}

func (p windowsJobProcess) Identity() (ProcessIdentity, bool, error) {
	pid, err := windows.GetProcessId(p.handle)
	if err != nil {
		return ProcessIdentity{}, false, fmt.Errorf("read the held process PID: %w", err)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(p.handle, &created, &exited, &kernel, &user); err != nil {
		return ProcessIdentity{}, false, fmt.Errorf("read the held process creation time: %w", err)
	}
	status, err := windows.WaitForSingleObject(p.handle, 0)
	if err != nil {
		return ProcessIdentity{}, false, fmt.Errorf("read the held process state: %w", err)
	}
	if status != uint32(windows.WAIT_TIMEOUT) && status != windows.WAIT_OBJECT_0 {
		return ProcessIdentity{}, false, fmt.Errorf("the held process supplied an invalid wait state: %d", status)
	}
	return ProcessIdentity{PID: int(pid), StartTime: created.Nanoseconds() / 1_000_000}, status == uint32(windows.WAIT_TIMEOUT), nil
}

func (p windowsJobProcess) Assign(job uintptr) error {
	return windows.AssignProcessToJobObject(windows.Handle(job), p.handle)
}
func (p windowsJobProcess) Close() error { return windows.CloseHandle(p.handle) }
