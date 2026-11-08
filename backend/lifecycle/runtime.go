package lifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/util/filelock"
	"github.com/leapmux/leapmux/internal/util/id"
)

// ErrInstanceRunning reports a data directory whose runtime lock
// another process holds: one instance per account and configuration.
var ErrInstanceRunning = errors.New("an instance already runs for this configuration")

// Runtime is the process half every CLI mode shares: the root data
// lease, the runtime lock, the published records, the private control
// endpoint, and the status the mode runtime reports into it.
//
// StartRuntime follows the contract's sequence:
//   - The root lease is acquired before any runtime record, token, or endpoint exists.
//   - The lease transfers to the mode runtime when dispatch begins.
//   - A failure before that transfer releases the lease here.
type Runtime struct {
	record  RuntimeRecord
	paths   Paths
	lease   *DirectoryLease
	leaseMu sync.Mutex

	runtimeLock *os.File
	control     *controlServer

	// state is the live view the control endpoint serves. The mode
	// runtime reports transitions through SetReadiness and friends.
	stateMu      sync.Mutex
	state        liveState
	stopOnce     sync.Once
	stopCh       chan struct{}
	readyOnce    sync.Once
	readyCh      chan struct{}
	shutdownOnce sync.Once
	shutdownErr  error
}

// liveState is the mutable half of the control snapshot.
type liveState struct {
	readiness    string
	registration RegistrationStatus
	connection   ConnectionStatus
	processState string
	diagnostics  []Diagnostic
	endedAt      *time.Time
	exitCode     *int
	failure      string
}

// RuntimeConfig states what one process attempt is.
type RuntimeConfig struct {
	// Mode is the CLI mode this process runs: one of the four names.
	Mode Role
	// DataDir is the configured data directory of that mode.
	DataDir string
	// Execution states how this attempt was launched.
	Execution ExecutionKind
}

// NewAttemptID mints a random 128-bit identity as 32 lowercase hexadecimal
// characters. Runtime and durable registration attempts call it independently.
// Each identity identifies one attempt, so observing an earlier attempt cannot
// authorize a request against another process or registration.
func NewAttemptID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("lifecycle: mint an attempt identity: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// StartRuntime acquires the root lease for the configured mode and data
// directory, takes the runtime lock, publishes the runtime record and
// the control token, and serves the control endpoint.
//
// A held runtime lock is ErrInstanceRunning; a conflicting ownership
// record is an OwnershipConflict; both leave the directory untouched.
func StartRuntime(cfg RuntimeConfig) (*Runtime, error) {
	if _, err := ParseRole(string(cfg.Mode)); err != nil {
		return nil, err
	}
	if cfg.Execution != ExecutionForeground && cfg.Execution != ExecutionService {
		return nil, fmt.Errorf("lifecycle: execution must be foreground or service, got %q", cfg.Execution)
	}
	canonical, err := CanonicalDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if cfg.Execution == ExecutionService {
		spec, err := ReadServiceSpec(pathsFor(canonical).ServiceSpec)
		if err != nil {
			return nil, fmt.Errorf("lifecycle: load the service specification before starting: %w", err)
		}
		if spec.Mode != cfg.Mode || spec.DataDirectory != canonical {
			return nil, fmt.Errorf("lifecycle: the service specification belongs to another instance")
		}
		return StartServiceRuntime(spec)
	}
	owner := NewDirectoryOwner(cfg.Mode, canonical)
	lease, err := AcquireDirectory(owner, cfg.Mode, canonical)
	if err != nil {
		return nil, err
	}
	return startRuntimeOwned(cfg, lease)
}

func startRuntimeOwned(cfg RuntimeConfig, lease *DirectoryLease) (*Runtime, error) {
	canonical := lease.Path()
	owner := lease.Owner()
	r := &Runtime{
		paths:   pathsFor(canonical),
		lease:   lease,
		stopCh:  make(chan struct{}),
		readyCh: make(chan struct{}),
	}
	// A failure from here releases everything this runtime acquired;
	// once committed, the lease belongs to the mode runtime and the
	// lock and endpoint to Shutdown.
	committed := false
	defer func() {
		if committed {
			return
		}
		if r.lease != nil {
			_ = r.lease.Close()
		}
		if r.runtimeLock != nil {
			_ = filelock.Unlock(r.runtimeLock)
			_ = r.runtimeLock.Close()
		}
		if r.control != nil {
			_ = r.control.Close()
		}
	}()

	lock, err := filelock.Open(r.paths.RuntimeLock, false)
	if err != nil {
		return nil, fmt.Errorf("lifecycle: open the runtime lock: %w", err)
	}
	held, err := filelock.TryLock(lock)
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("lifecycle: take the runtime lock: %w", err)
	}
	if !held {
		_ = lock.Close()
		return nil, fmt.Errorf("%w: %s", ErrInstanceRunning, canonical)
	}
	r.runtimeLock = lock

	attempt, err := NewAttemptID()
	if err != nil {
		return nil, err
	}
	identity := NewInstanceID(owner.AccountID, cfg.Mode, canonical)
	token := id.Generate()
	record := RuntimeRecord{
		InstanceID: string(identity),
		AttemptID:  attempt,
		Mode:       cfg.Mode,
		Execution:  cfg.Execution,
		DataDir:    canonical,
		PID:        os.Getpid(),
		StartedAt:  time.Now().UTC(),
		ControlURL: ControlURL(identity),
	}
	if ident, ok := processIdentity(); ok {
		record.ProcessStart = ident.StartTime
	}
	r.record = record
	r.state = liveState{
		readiness:    ReadinessStarting,
		processState: ProcessRunning,
		registration: RegistrationStatus{State: RegistrationNotApplicable},
		connection:   ConnectionStatus{State: ConnectionNotApplicable},
		diagnostics:  []Diagnostic{},
	}

	if err := preparePrivateDir(r.paths.Logs); err != nil {
		return nil, err
	}
	if err := WriteControlToken(r.paths.ControlToken, token); err != nil {
		return nil, fmt.Errorf("lifecycle: publish the control token: %w", err)
	}
	if err := verifyPrivateFile(r.paths.ControlToken); err != nil {
		return nil, err
	}
	initialStatus := r.Status()
	record.Status = &initialStatus
	if err := WriteRuntimeRecord(r.paths.RuntimeRecord, record); err != nil {
		return nil, fmt.Errorf("lifecycle: publish the runtime record: %w", err)
	}
	if err := verifyPrivateFile(r.paths.RuntimeRecord); err != nil {
		return nil, err
	}

	control, err := serveControl(record, token, r)
	if err != nil {
		return nil, err
	}
	r.control = control
	committed = true
	return r, nil
}

// Record returns the published runtime record.
func (r *Runtime) Record() RuntimeRecord { return r.record }

// Paths returns the lifecycle paths of the data directory.
func (r *Runtime) Paths() Paths { return r.paths }

// Lease returns the root lease without transferring it. A caller that
// will own storage must TakeLease instead.
func (r *Runtime) Lease() *DirectoryLease {
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	return r.lease
}

// TakeLease transfers the root lease to the mode runtime. After the
// transfer the receiver releases nothing: the taker closes the lease
// after its storage closes.
func (r *Runtime) TakeLease() *DirectoryLease {
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	lease := r.lease
	r.lease = nil
	return lease
}

// SetReadiness reports a readiness transition.
func (r *Runtime) SetReadiness(state string) {
	r.stateMu.Lock()
	r.state.readiness = state
	r.stateMu.Unlock()
	if state == ReadinessReady || state == ReadinessReadyForSetup {
		r.readyOnce.Do(func() { close(r.readyCh) })
	}
}

// Ready closes once required application initialization or usable fresh
// setup is ready. Opening the control listener does not close it.
func (r *Runtime) Ready() <-chan struct{} { return r.readyCh }

// SetRegistration reports the registration snapshot.
func (r *Runtime) SetRegistration(state RegistrationStatus) {
	r.stateMu.Lock()
	r.state.registration = state.Clone()
	r.stateMu.Unlock()
}

// SetConnection reports the connection snapshot.
func (r *Runtime) SetConnection(state ConnectionStatus) {
	r.stateMu.Lock()
	r.state.connection = state.Clone()
	r.stateMu.Unlock()
}

// SetProcessState reports the process state the runtime observes for
// itself, such as stopping.
func (r *Runtime) SetProcessState(state string) {
	r.stateMu.Lock()
	r.state.processState = state
	r.stateMu.Unlock()
}

// AddDiagnostic appends one diagnostic to the live snapshot.
func (r *Runtime) AddDiagnostic(d Diagnostic) {
	r.stateMu.Lock()
	r.state.diagnostics = append(r.state.diagnostics, d.clone())
	r.stateMu.Unlock()
}

// Status snapshots the live view for the control endpoint.
func (r *Runtime) Status() Status {
	r.stateMu.Lock()
	state := r.state
	state.registration = state.registration.Clone()
	state.connection = state.connection.Clone()
	state.endedAt, state.exitCode = cloneScalar(state.endedAt), cloneScalar(state.exitCode)
	state.diagnostics = append([]Diagnostic(nil), state.diagnostics...)
	for index := range state.diagnostics {
		state.diagnostics[index] = state.diagnostics[index].clone()
	}
	r.stateMu.Unlock()
	started := r.record.StartedAt
	pid := r.record.PID
	spec, specErr := ReadServiceSpecOf(r.paths)
	status := Status{
		InstanceID:   new(r.record.InstanceID),
		Mode:         new(string(r.record.Mode)),
		Execution:    new(r.record.Execution),
		Registration: state.registration,
		Connection:   state.connection,
		Attempt: AttemptStatus{
			ID:        new(r.record.AttemptID),
			StartedAt: &started,
			LogPath:   new(r.paths.AttemptLogPath(r.record.AttemptID)),
			EndedAt:   state.endedAt,
			ExitCode:  state.exitCode,
		},
		Diagnostics: state.diagnostics,
	}
	if state.failure != "" {
		failure := state.failure
		status.Attempt.Error = &failure
	}
	if specErr == nil {
		kind := spec.Startup
		status.Startup = &kind
		status.Configured = true
	}
	if state.processState != "" {
		status.Process = ProcessStatus{State: state.processState, PID: &pid, StartedAt: &started}
	} else {
		status.Process = ProcessStatus{State: ProcessRunning, PID: &pid, StartedAt: &started}
	}
	if state.readiness != "" {
		status.Readiness = new(state.readiness)
	}
	if status.Diagnostics == nil {
		status.Diagnostics = []Diagnostic{}
	}
	return status
}

// RequestStop accepts one shutdown request. Idempotent; the returned
// channel is closed once the request was taken.
func (r *Runtime) RequestStop() <-chan struct{} {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		r.SetProcessState(ProcessStopping)
	})
	return r.stopCh
}

// StopRequested reports whether a stop was requested.
func (r *Runtime) StopRequested() bool {
	select {
	case <-r.stopCh:
		return true
	default:
		return false
	}
}

// StopSignal is the channel a signal handler selects on together with
// the process context.
func (r *Runtime) StopSignal() <-chan struct{} { return r.stopCh }

// Finish records the process outcome and releases runtime resources.
// The caller retains the original cause; Finish returns cleanup failures.
func (r *Runtime) Finish(cause error) error {
	if cause != nil {
		r.stateMu.Lock()
		r.state.failure = cause.Error()
		r.state.processState = ProcessFailed
		r.state.readiness = ReadinessFailed
		r.stateMu.Unlock()
	}
	return r.Shutdown()
}

// Shutdown closes the control endpoint and releases the runtime lock.
// The mode runtime releases transferred storage leases before this call.
// Shutdown releases a root lease that dispatch never consumed.
func (r *Runtime) Shutdown() error {
	r.shutdownOnce.Do(func() {
		r.stopOnce.Do(func() { close(r.stopCh) })
		var errs []error
		if r.control != nil {
			errs = append(errs, r.control.Close())
		}
		if lease := r.TakeLease(); lease != nil {
			errs = append(errs, lease.Close())
		}
		r.finishStatus(errors.Join(errs...))
		record := r.record
		status := r.Status()
		record.Status = &status
		errs = append(errs, WriteRuntimeRecord(r.paths.RuntimeRecord, record))
		if r.runtimeLock != nil {
			errs = append(errs, filelock.Unlock(r.runtimeLock), r.runtimeLock.Close())
			r.runtimeLock = nil
		}
		r.shutdownErr = errors.Join(errs...)
	})
	return r.shutdownErr
}

func (r *Runtime) finishStatus(cleanupErr error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	ended := time.Now().UTC()
	r.state.endedAt = &ended
	code := 0
	if cleanupErr != nil {
		r.state.failure = errors.Join(nonemptyError(r.state.failure), cleanupErr).Error()
	}
	if r.state.failure != "" || r.state.processState == ProcessFailed || r.state.readiness == ReadinessFailed {
		code = 1
		r.state.processState = ProcessFailed
		r.state.readiness = ReadinessFailed
		if r.state.failure == "" {
			for _, diagnostic := range r.state.diagnostics {
				if diagnostic.Kind == "error" {
					r.state.failure = diagnostic.Body
				}
			}
		}
		if r.state.failure == "" {
			r.state.failure = "the process failed"
		}
	} else {
		r.state.processState = ProcessStopped
	}
	r.state.exitCode = &code
}

func nonemptyError(message string) error {
	if message == "" {
		return nil
	}
	return errors.New(message)
}

// OperationLock takes the directory-scoped operation lock that
// serializes lifecycle commands, registration changes, and service
// configuration. The returned release function is safe to call once.
// The lock path holds no mode or instance identity, so every command
// for the directory contends for the same file.
func OperationLock(dataDirectory string) (release func(), err error) {
	canonical, err := CanonicalDir(dataDirectory)
	if err != nil {
		return nil, err
	}
	paths := pathsFor(canonical)
	if err := atomicfile.MkdirAllDurable(canonical); err != nil {
		return nil, err
	}
	if err := preparePrivateDir(paths.Dir); err != nil {
		return nil, err
	}
	lock, err := filelock.Open(paths.OperationLock, false)
	if err != nil {
		return nil, fmt.Errorf("lifecycle: open the operation lock: %w", err)
	}
	held, err := filelock.TryLock(lock)
	if err == nil && !held {
		err = errors.New("another lifecycle command holds the operation lock")
	}
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	return func() {
		_ = filelock.Unlock(lock)
		_ = lock.Close()
	}, nil
}

// processIdentity returns this process's identity where the platform
// states one; ok is false when start times are unavailable, and callers
// treat that as "no extra evidence" rather than an error.
func processIdentity() (identity processIdent, ok bool) {
	return currentProcessIdentity()
}
