package providerkit

import (
	"errors"
	"math"
	"os"

	"github.com/shirou/gopsutil/v4/process"
)

// ProcessIdentity identifies one process: its pid and the time when it
// started. The system gives a pid to a new process after the first one ends,
// so a pid alone can point at a process that the worker never saw. The start
// time tells the two apart.
//
// The zero value identifies no process: Runs reports false for it, and Kill
// kills nothing.
//
// On Linux the system states the start time relative to the boot time, which
// moves when an administrator sets the wall clock. After such a change Runs
// reports false for the process that still runs, and Kill kills nothing. That
// is the safe direction: the worker then stops to wait for the process, and it
// never kills a process that it did not identify.
type ProcessIdentity struct {
	PID int
	// StartTime is when the process started, in milliseconds since the Unix
	// epoch, as the system reports it.
	StartTime int64
}

// IdentifyProcess returns the identity of the process that has pid now. It
// reports false when no process has pid, or when the system states no start
// time for it.
//
// The result identifies the process that the caller means only when the caller
// knows that this process held pid when IdentifyProcess ran. A caller learns
// that, for example, from an authenticated answer of the process that states
// pid and arrives after IdentifyProcess returned.
func IdentifyProcess(pid int) (ProcessIdentity, bool) {
	if pid <= 0 || pid > math.MaxInt32 || !ProcessRuns(pid) {
		return ProcessIdentity{}, false
	}
	start, err := (&process.Process{Pid: int32(pid)}).CreateTime()
	if err != nil || start <= 0 {
		return ProcessIdentity{}, false
	}
	return ProcessIdentity{PID: pid, StartTime: start}, true
}

// IsZero reports whether p identifies no process.
func (p ProcessIdentity) IsZero() bool {
	return p.PID <= 0
}

// Runs reports whether the process still runs: its pid belongs to a process
// with the same start time.
func (p ProcessIdentity) Runs() bool {
	if p.IsZero() {
		return false
	}
	current, ok := IdentifyProcess(p.PID)
	return ok && current == p
}

// Kill kills the process when it still runs. It reports true when it sent the
// kill. It kills nothing and reports false when the pid now belongs to another
// process or to none, and it never kills the worker's own process.
//
// On Linux and Windows the check and the kill reach one process:
// os.FindProcess holds a pidfd or a process handle, and the system gives the
// pid to no other process while that is open. On macOS and the BSDs,
// os.FindProcess holds nothing. A process that ends, and whose pid a new
// process takes, between the check and the kill would get the kill. That
// window lasts two system calls.
func (p ProcessIdentity) Kill() (bool, error) {
	if p.IsZero() || p.PID == os.Getpid() {
		return false, nil
	}
	proc, err := os.FindProcess(p.PID)
	if err != nil {
		return false, nil
	}
	defer func() { _ = proc.Release() }()
	if !p.Runs() {
		return false, nil
	}
	if err := proc.Kill(); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
