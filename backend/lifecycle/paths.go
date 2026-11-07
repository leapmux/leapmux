package lifecycle

import (
	"fmt"
	"path/filepath"
)

// lifecycleDirName is the private area under each data directory that
// holds the ownership record, the locks, the persisted records, the
// control token, and the attempt logs.
const lifecycleDirName = ".lifecycle"

// accountRuntimePath separates private scratch resources beneath a shared parent.
func accountRuntimePath(parent, account string) string {
	return filepath.Join(parent, "leapmux-"+hashID(account))
}

// File names inside the lifecycle directory. The locks are
// directory-scoped on purpose: neither name includes the mode or the
// instance identity, so a second mode resolving to the same directory
// contends for the very same file and cannot proceed by spelling its
// identity differently.
const (
	ownershipRecordName = "directory.yaml"
	dataLockName        = "data.lock"
	serviceSpecName     = "service.yaml"
	runtimeRecordName   = "runtime.yaml"
	controlTokenName    = "control.token"
	operationLockName   = "operation.lock"
	runtimeLockName     = "runtime.lock"
	logsDirName         = "logs"
)

// Paths names every lifecycle resource of one data directory.
type Paths struct {
	// DataDir is the canonical data directory the resources belong to.
	DataDir string
	// Dir is its lifecycle directory.
	Dir string

	OwnershipRecord string
	DataLock        string
	ServiceSpec     string
	RuntimeRecord   string
	ControlToken    string
	OperationLock   string
	RuntimeLock     string
	Logs            string
}

// pathsFor names the lifecycle resources of one canonical data
// directory.
func pathsFor(canonicalDataDir string) Paths {
	dir := filepath.Join(canonicalDataDir, lifecycleDirName)
	return Paths{
		DataDir:         canonicalDataDir,
		Dir:             dir,
		OwnershipRecord: filepath.Join(dir, ownershipRecordName),
		DataLock:        filepath.Join(dir, dataLockName),
		ServiceSpec:     filepath.Join(dir, serviceSpecName),
		RuntimeRecord:   filepath.Join(dir, runtimeRecordName),
		ControlToken:    filepath.Join(dir, controlTokenName),
		OperationLock:   filepath.Join(dir, operationLockName),
		RuntimeLock:     filepath.Join(dir, runtimeLockName),
		Logs:            filepath.Join(dir, logsDirName),
	}
}

// AttemptLogPath names one process attempt's log file.
func (p Paths) AttemptLogPath(attemptID string) string {
	return filepath.Join(p.Logs, attemptID+".log")
}

// ControlURL returns the local transport URL the instance's control
// interface answers on: a Unix socket under the short account-owned
// runtime directory, or a named pipe on Windows. The name derives from
// the instance identity, never from the data directory, so a long data
// path cannot exceed the socket path limit and two instances never
// share an endpoint.
func ControlURL(id InstanceID) string {
	return controlURLFor(id)
}

// controlSocketPathError guards the Unix socket path length: the bind
// fails at a limit the kernel states, and the error names the path so an
// operator can move the runtime directory rather than guess.
func controlSocketPathError(path string) error {
	return fmt.Errorf("lifecycle: the control socket path %s is too long for the kernel; set LEAPMUX_RUNTIME_DIR to a shorter directory", path)
}

// PathsOf names the lifecycle resources of one canonical data
// directory, for callers that already canonicalized it.
func PathsOf(canonical string) Paths { return pathsFor(canonical) }

// ProbeRuntimeLock reports whether any process holds the runtime lock
// of the named paths.
func ProbeRuntimeLock(paths Paths) (bool, error) {
	return probeExclusiveLock(paths.RuntimeLock)
}

// ProbeLockAt reports whether any process holds the lock file at the
// exact path. Callers that resolved the path themselves use this; the
// Paths-based probe composes the lifecycle layout they may not want.
func ProbeLockAt(path string) (bool, error) {
	return probeExclusiveLock(path)
}
