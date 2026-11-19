//go:build windows

package procutil

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
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

type jobProcessListHeader struct {
	assigned uint32
	returned uint32
}

const jobProcessHeaderSize = int(unsafe.Sizeof(jobProcessListHeader{}))
const jobProcessIDSize = int(unsafe.Sizeof(uintptr(0)))
const jobProcessHeaderWords = jobProcessHeaderSize / jobProcessIDSize

func readJobProcessIDs(ctx context.Context, query func([]uintptr) error) ([]uintptr, error) {
	const maxProcessIDs = (min(uint64(math.MaxUint32), uint64(math.MaxInt)) - uint64(jobProcessHeaderSize)) / uint64(jobProcessIDSize)
	for capacity := 16; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		buffer := make([]uintptr, jobProcessHeaderWords+capacity)
		err := query(buffer)
		if err != nil && !errors.Is(err, windows.ERROR_MORE_DATA) {
			return nil, errors.Join(err, ctx.Err())
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header := (*jobProcessListHeader)(unsafe.Pointer(&buffer[0]))
		if header.returned > uint32(capacity) || header.returned > header.assigned {
			return nil, errors.New("the Windows job returned an invalid process count")
		}
		// Members can exit during a successful query. With room for all
		// assigned processes, the returned count identifies the valid IDs.
		if err == nil && header.assigned <= uint32(capacity) {
			return buffer[jobProcessHeaderWords : jobProcessHeaderWords+int(header.returned)], nil
		}
		if header.assigned <= uint32(capacity) {
			return nil, errors.Join(errors.New("the Windows job returned an incomplete process list"), err)
		}
		nextCapacity := max(uint64(header.assigned), uint64(capacity)*2)
		if nextCapacity > maxProcessIDs {
			return nil, errors.New("the Windows job process list exceeds the system buffer limit")
		}
		capacity = int(nextCapacity)
	}
}

func readWindowsJobMembers(ctx context.Context, job windows.Handle) ([]ProcessIdentity, error) {
	pids, err := readJobProcessIDs(ctx, func(buffer []uintptr) error {
		err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(&buffer[0])), uint32(len(buffer)*jobProcessIDSize), nil)
		runtime.KeepAlive(buffer)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read the owned Windows job: %w", err)
	}
	var failures error
	members := make([]ProcessIdentity, 0, len(pids))
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return members, errors.Join(failures, err)
		}
		if pid == 0 || pid > math.MaxInt32 {
			failures = errors.Join(failures, fmt.Errorf("the Windows job supplied an invalid PID %d", pid))
			continue
		}
		identity, live, err := identifyWindowsJobMember(job, uint32(pid))
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("verify Windows job member %d: %w", pid, err))
		}
		if live {
			members = append(members, identity)
		}
	}
	return members, errors.Join(failures, ctx.Err())
}

var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func identifyWindowsJobMember(job windows.Handle, pid uint32) (identity ProcessIdentity, live bool, result error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return ProcessIdentity{}, false, nil // The process exited after the job query.
	}
	if err != nil {
		return ProcessIdentity{}, false, err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(handle)) }()
	// A job PID can be reused by an unrelated process before OpenProcess.
	// Check job membership and creation time on the same held handle.
	var member int32
	ok, _, err := isProcessInJob.Call(uintptr(handle), uintptr(job), uintptr(unsafe.Pointer(&member)))
	if ok == 0 {
		return ProcessIdentity{}, false, err
	}
	if member == 0 {
		return ProcessIdentity{}, false, nil
	}
	return (windowsJobProcess{handle: handle}).Identity()
}
