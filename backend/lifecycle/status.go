package lifecycle

import "time"

// Status is the one status model the local control response, the
// persisted history, and `status --json` share. It carries no
// credentials and no private keys.
type Status struct {
	InstanceID string        `json:"instance_id"`
	Mode       string        `json:"mode"`
	Execution  ExecutionKind `json:"execution"`
	// Startup is the saved native configuration's kind, or null for an
	// unconfigured foreground instance.
	Startup *StartupKind `json:"startup"`
	// Configured reports whether a saved service specification exists.
	Configured   bool               `json:"configured"`
	Process      ProcessStatus      `json:"process"`
	Readiness    string             `json:"readiness"`
	Registration RegistrationStatus `json:"registration"`
	Connection   ConnectionStatus   `json:"connection"`
	Attempt      AttemptStatus      `json:"attempt"`
	Diagnostics  []Diagnostic       `json:"diagnostics"`
}

// ProcessStatus is the process evidence the reconciler collected.
type ProcessStatus struct {
	// State is one of starting, running, stopping, stopped, failed,
	// unresponsive.
	State string `json:"state"`
	PID   *int   `json:"pid"`
	// StartedAt is the attempt's start, or null.
	StartedAt *time.Time `json:"started_at"`
}

// RegistrationStatus reports worker registration separately from Hub
// connectivity.
type RegistrationStatus struct {
	// State is one of not_applicable, unregistered, pending,
	// registered, invalid.
	State    string `json:"state"`
	WorkerID string `json:"worker_id"`
	HubURL   string `json:"hub_url"`
}

// ConnectionStatus reports the worker's Hub connection.
type ConnectionStatus struct {
	// State is one of not_applicable, pending_setup, connecting,
	// connected, disconnected.
	State     string `json:"state"`
	LastError string `json:"last_error"`
}

// AttemptStatus is the persisted current attempt and its log.
type AttemptStatus struct {
	ID        string     `json:"id"`
	StartedAt *time.Time `json:"started_at"`
	// EndedAt, ExitCode, Error and the log path are null while the
	// attempt runs.
	EndedAt  *time.Time `json:"ended_at"`
	ExitCode *int       `json:"exit_code"`
	Error    *string    `json:"error"`
	LogPath  string     `json:"log_path"`
}

// Diagnostic is one relevant log excerpt or native failure the status
// report carries.
type Diagnostic struct {
	// Kind states where the excerpt came from: log, native, or error.
	Kind string `json:"kind"`
	// Title names what the excerpt shows.
	Title string `json:"title"`
	// Body is the bounded excerpt itself.
	Body string `json:"body"`
	// Source is the complete log location, kept so the operator can
	// read past the excerpt.
	Source string `json:"source"`
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
