//go:build windows

package procutil

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processOwnerPlatform struct {
	job *JobObject
}

func startProcessCommand(attributes **syscall.SysProcAttr, start func() error) error {
	// Run no child code until its job can contain the processes it creates.
	original := *attributes
	var launchAttributes syscall.SysProcAttr
	if original != nil {
		launchAttributes = *original
	}
	launchAttributes.CreationFlags |= windows.CREATE_SUSPENDED
	*attributes = &launchAttributes
	defer func() { *attributes = original }()
	return start()
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

func (p *processOwnerPlatform) attachAndResume(root ProcessIdentity) error {
	if err := p.attach(root); err != nil {
		return err
	}
	if p.job.handle.Load() == 0 {
		// The process ended during startup and has no thread to resume.
		return nil
	}
	if err := resumeInitialThread(uint32(root.PID)); err != nil {
		return errors.Join(err, p.job.Terminate())
	}
	return nil
}

var getProcessIDOfThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIdOfThread")

func resumeInitialThread(pid uint32) (result error) {
	// os/exec and go-pty close the initial thread handle. Find the thread while
	// the command still holds the process handle, which prevents PID reuse.
	threadID, err := initialThreadID(pid)
	if err != nil {
		return err
	}
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME|windows.THREAD_QUERY_LIMITED_INFORMATION, false, threadID)
	if err != nil {
		return fmt.Errorf("open the initial thread: %w", err)
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(thread)) }()
	ownerPID, _, err := getProcessIDOfThread.Call(uintptr(thread))
	if ownerPID == 0 {
		return fmt.Errorf("read the initial thread's process ID: %w", err)
	}
	if ownerPID != uintptr(pid) {
		return errors.New("the initial thread no longer belongs to the owned process")
	}
	previousCount, err := windows.ResumeThread(thread)
	if err != nil {
		return fmt.Errorf("resume the initial thread: %w", err)
	}
	if previousCount != 1 {
		return fmt.Errorf("the initial thread had an unexpected suspend count of %d", previousCount)
	}
	return nil
}

func initialThreadID(pid uint32) (id uint32, result error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, fmt.Errorf("snapshot Windows threads: %w", err)
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(snapshot)) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		if id != 0 {
			return 0, errors.New("the suspended process has more than one thread")
		}
		id = entry.ThreadID
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return 0, fmt.Errorf("read the Windows thread snapshot: %w", err)
	}
	if id == 0 {
		return 0, errors.New("the suspended process has no initial thread")
	}
	return id, nil
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
