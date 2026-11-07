package lifecycle

import "time"

// Status is the one status model the local control response, the
// persisted history, and `status --json` share. It carries no
// credentials and no private keys.
type Status struct {
	InstanceID *string        `json:"instance_id" yaml:"instance_id"`
	Mode       *string        `json:"mode" yaml:"mode"`
	Execution  *ExecutionKind `json:"execution" yaml:"execution"`
	// Startup is the saved native configuration's kind, or null for an
	// unconfigured foreground instance.
	Startup *StartupKind `json:"startup" yaml:"startup"`
	// Configured reports whether the native definition matches the saved
	// service specification when the CLI reconciles native state.
	Configured   bool               `json:"configured" yaml:"configured"`
	Process      ProcessStatus      `json:"process" yaml:"process"`
	Readiness    *string            `json:"readiness" yaml:"readiness"`
	Registration RegistrationStatus `json:"registration" yaml:"registration"`
	Connection   ConnectionStatus   `json:"connection" yaml:"connection"`
	Attempt      AttemptStatus      `json:"attempt" yaml:"attempt"`
	Diagnostics  []Diagnostic       `json:"diagnostics" yaml:"diagnostics"`
}

func (s Status) matchesAttempt(record RuntimeRecord) bool {
	return s.InstanceID != nil && *s.InstanceID == record.InstanceID &&
		s.Attempt.ID != nil && *s.Attempt.ID == record.AttemptID
}

// ProcessStatus is the process evidence the reconciler collected.
type ProcessStatus struct {
	// State is one of starting, running, stopping, stopped, failed,
	// unresponsive.
	State string `json:"state" yaml:"state"`
	PID   *int   `json:"pid" yaml:"pid"`
	// StartedAt is the attempt's start, or null.
	StartedAt *time.Time `json:"started_at" yaml:"started_at"`
}

// RegistrationStatus reports worker registration separately from Hub
// connectivity.
type RegistrationStatus struct {
	// State is one of not_applicable, unregistered, pending,
	// registered, invalid.
	State    string  `json:"state" yaml:"state"`
	WorkerID *string `json:"worker_id" yaml:"worker_id"`
	HubURL   *string `json:"hub_url" yaml:"hub_url"`
}

// Clone copies nullable values so the result cannot mutate its source.
func (s RegistrationStatus) Clone() RegistrationStatus {
	s.WorkerID, s.HubURL = cloneScalar(s.WorkerID), cloneScalar(s.HubURL)
	return s
}

// ConnectionStatus reports the worker's Hub connection.
type ConnectionStatus struct {
	// State is one of not_applicable, pending_setup, connecting,
	// connected, disconnected.
	State     string  `json:"state" yaml:"state"`
	LastError *string `json:"last_error" yaml:"last_error"`
}

// Clone copies nullable values so the result cannot mutate its source.
func (s ConnectionStatus) Clone() ConnectionStatus {
	s.LastError = cloneScalar(s.LastError)
	return s
}

// AttemptStatus is the persisted current attempt and its log.
type AttemptStatus struct {
	ID        *string    `json:"id" yaml:"id"`
	StartedAt *time.Time `json:"started_at" yaml:"started_at"`
	// EndedAt, ExitCode, and Error are null while the attempt runs.
	// LogPath is null when no attempt log is available.
	EndedAt  *time.Time `json:"ended_at" yaml:"ended_at"`
	ExitCode *int       `json:"exit_code" yaml:"exit_code"`
	Error    *string    `json:"error" yaml:"error"`
	LogPath  *string    `json:"log_path" yaml:"log_path"`
}

// Diagnostic is one relevant log excerpt or native failure the status
// report carries.
type Diagnostic struct {
	// Kind states where the excerpt came from: log, native, or error.
	Kind string `json:"kind" yaml:"kind"`
	// Title identifies what the excerpt shows.
	Title string `json:"title" yaml:"title"`
	// Body is the excerpt, limited to the automatic diagnostic caps.
	Body string `json:"body" yaml:"body"`
	// Source is the complete log location, kept so the operator can
	// read past the excerpt.
	Source *string `json:"source" yaml:"source"`
}

func (d Diagnostic) clone() Diagnostic {
	d.Source = cloneScalar(d.Source)
	return d
}

func cloneScalar[T any](value *T) *T {
	if value == nil {
		return nil
	}
	return new(*value)
}

// The process state vocabulary.
const (
	ProcessStarting     = "starting"
	ProcessRunning      = "running"
	ProcessStopping     = "stopping"
	ProcessStopped      = "stopped"
	ProcessFailed       = "failed"
	ProcessUnresponsive = "unresponsive"
)

// The readiness vocabulary.
const (
	ReadinessStarting      = "starting"
	ReadinessReady         = "ready"
	ReadinessReadyForSetup = "ready_for_setup"
	ReadinessFailed        = "failed"
)

// The registration state vocabulary.
const (
	RegistrationNotApplicable = "not_applicable"
	RegistrationUnregistered  = "unregistered"
	RegistrationPending       = "pending"
	RegistrationRegistered    = "registered"
	RegistrationInvalid       = "invalid"
)

// The connection state vocabulary.
const (
	ConnectionNotApplicable = "not_applicable"
	ConnectionPendingSetup  = "pending_setup"
	ConnectionConnecting    = "connecting"
	ConnectionConnected     = "connected"
	ConnectionDisconnected  = "disconnected"
)

// Reporter is the live view a running instance publishes to its control
// interface. The mode runtime owns the transitions; the control server
// only reads.
type Reporter interface {
	// Status returns the current snapshot.
	Status() Status
	// RequestStop asks the runtime to shut down. The returned channel
	// closes when the shutdown was accepted; the caller still waits for
	// the actual shutdown through the runtime lock.
	RequestStop() <-chan struct{}
}
