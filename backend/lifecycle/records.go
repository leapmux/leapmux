package lifecycle

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/util/useronly"
)

// OwnershipConflict reports a data directory whose ownership record
// identifies another instance. It is a conflict even when the prior instance
// is stopped: the record is the directory's identity, not its liveness.
type OwnershipConflict struct {
	// OwningMode is the instance mode the record identifies.
	OwningMode Role
	// OwningRoot is the canonical containing root the record identifies.
	OwningRoot string
	// Component is the storage component the record identifies.
	Component Role
	// Path is the directory the record sits in.
	Path string
}

// Error identifies the owning mode and recommends the command or a separate
// data directory.
func (c *OwnershipConflict) Error() string {
	return fmt.Sprintf(
		"the data directory %s belongs to a %s instance (root %s); use that instance's commands, or choose a separate data directory",
		c.Path, c.OwningMode, c.OwningRoot)
}

// ownershipRecord is the private marker that ties one storage directory
// to the instance that owns it. mode identifies this storage component
// and owner_mode its containing instance; a direct Hub or Worker uses
// itself as the owner, and a combined instance uses solo or dev and its
// canonical root for the root and both components.
//
// The owning instance identity derives from the account, the owner
// mode, and the owner root, so no second copy of it is stored.
type ownershipRecord struct {
	AccountID     string `yaml:"account_id"`
	Mode          Role   `yaml:"mode"`
	DataDirectory string `yaml:"data_directory"`
	OwnerMode     Role   `yaml:"owner_mode"`
	OwnerDataDir  string `yaml:"owner_data_directory"`
}

// writeOwnershipRecord durably publishes the ownership record at path.
func writeOwnershipRecord(path string, rec ownershipRecord) error {
	if err := verifyExistingPrivateFile(path); err != nil {
		return err
	}
	data, err := yaml.Marshal(rec)
	if err != nil {
		return err
	}
	return atomicfile.WriteFileDurable(path, data, 0o600)
}

// readOwnershipRecord loads and validates the record at path: every
// field must parse, the four mode names must be legal, and a record
// that belongs to another account is a conflict, not a format error.
func readOwnershipRecord(path string) (ownershipRecord, error) {
	var rec ownershipRecord
	data, err := useronly.ReadFile(path)
	if err != nil {
		return rec, err
	}
	if err := yaml.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("parse the ownership record %s: %w", path, err)
	}
	for _, mode := range []Role{rec.Mode, rec.OwnerMode} {
		if _, modeErr := ParseRole(string(mode)); modeErr != nil {
			return rec, fmt.Errorf("the ownership record %s identifies an unknown mode %q", path, mode)
		}
	}
	if rec.AccountID == "" || rec.DataDirectory == "" || rec.OwnerDataDir == "" {
		return rec, fmt.Errorf("the ownership record %s misses a required field", path)
	}
	return rec, nil
}

// StartupKind is when a native startup configuration runs the instance.
type StartupKind string

const (
	// StartupLogin runs at the account's interactive login.
	StartupLogin StartupKind = "login"
	// StartupBoot runs at computer start, under the selected account.
	StartupBoot StartupKind = "boot"
)

// ParseStartupKind validates one startup kind.
func ParseStartupKind(s string) (StartupKind, error) {
	switch StartupKind(s) {
	case StartupLogin, StartupBoot:
		return StartupKind(s), nil
	default:
		return "", fmt.Errorf("lifecycle: unknown startup kind %q", s)
	}
}

// ServiceSpec is the saved service specification: the source every
// native launch definition is generated from. It records explicit
// runtime overrides only -- never the whole calling shell environment,
// and never a registration key.
type ServiceSpec struct {
	InstanceID         string            `yaml:"instance_id"`
	Mode               Role              `yaml:"mode"`
	Startup            StartupKind       `yaml:"startup"`
	AccountID          string            `yaml:"account_id"`
	AccountName        string            `yaml:"account_name"`
	Executable         string            `yaml:"executable"`
	WorkingDirectory   string            `yaml:"working_directory"`
	ConfigFile         string            `yaml:"config_file"`
	DataDirectory      string            `yaml:"data_directory"`
	RuntimeArguments   []string          `yaml:"runtime_arguments"`
	RuntimeEnvironment map[string]string `yaml:"runtime_environment"`
	// EnvironmentFile is an absolute path or nil.
	EnvironmentFile *string `yaml:"environment_file"`
}

// Equal compares saved launch values. Empty argument lists and environment
// maps have the same meaning whether YAML decoded them as nil or empty.
func (s ServiceSpec) Equal(other ServiceSpec) bool {
	if len(s.RuntimeArguments) == 0 {
		s.RuntimeArguments = nil
	}
	if len(other.RuntimeArguments) == 0 {
		other.RuntimeArguments = nil
	}
	if len(s.RuntimeEnvironment) == 0 {
		s.RuntimeEnvironment = nil
	}
	if len(other.RuntimeEnvironment) == 0 {
		other.RuntimeEnvironment = nil
	}
	return reflect.DeepEqual(s, other)
}

// WriteServiceSpec durably publishes the service specification.
func WriteServiceSpec(path string, spec ServiceSpec) error {
	if err := verifyExistingPrivateFile(path); err != nil {
		return err
	}
	data, err := yaml.Marshal(spec)
	if err != nil {
		return err
	}
	return atomicfile.WriteFileDurable(path, data, 0o600)
}

// ReadServiceSpec loads the saved specification at path.
// ReadServiceSpecOf reads the service specification saved in a data
// directory. A specification for another data directory belongs to the path
// that the directory moved from, and no service exists for this path yet, so
// it reads as absent (os.ErrNotExist).
func ReadServiceSpecOf(paths Paths) (ServiceSpec, error) {
	spec, err := ReadServiceSpec(paths.ServiceSpec)
	if err == nil && spec.DataDirectory != paths.DataDir {
		return ServiceSpec{}, fmt.Errorf("the service specification %s belongs to %s: %w", paths.ServiceSpec, spec.DataDirectory, os.ErrNotExist)
	}
	return spec, err
}

func ReadServiceSpec(path string) (ServiceSpec, error) {
	var spec ServiceSpec
	data, err := useronly.ReadFile(path)
	if err != nil {
		return spec, err
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return spec, fmt.Errorf("parse the service specification %s: %w", path, err)
	}
	return spec, nil
}

// ExecutionKind states how the current attempt runs.
type ExecutionKind string

const (
	// ExecutionForeground is a `run` command in a terminal.
	ExecutionForeground ExecutionKind = "foreground"
	// ExecutionService is a native mechanism's instance.
	ExecutionService ExecutionKind = "service"
)

// RuntimeRecord is the persisted state of the current process attempt:
// what runs, since when, and where its log is. The registration
// command's process identifier never appears here; process evidence
// comes from the runtime lock and the authenticated endpoint.
type RuntimeRecord struct {
	InstanceID   string        `json:"instance_id" yaml:"instance_id"`
	AttemptID    string        `json:"attempt_id" yaml:"attempt_id"`
	Mode         Role          `json:"mode" yaml:"mode"`
	Execution    ExecutionKind `json:"execution" yaml:"execution"`
	DataDir      string        `json:"data_dir" yaml:"data_dir"`
	PID          int           `json:"pid" yaml:"pid"`
	ProcessStart int64         `json:"process_start_ms" yaml:"process_start_ms"`
	StartedAt    time.Time     `json:"started_at" yaml:"started_at"`
	ControlURL   string        `json:"control_url" yaml:"control_url"`
	Status       *Status       `json:"status,omitempty" yaml:"status,omitempty"`
}

// WriteRuntimeRecord durably publishes the runtime record.
func WriteRuntimeRecord(path string, rec RuntimeRecord) error {
	if err := verifyExistingPrivateFile(path); err != nil {
		return err
	}
	data, err := yaml.Marshal(rec)
	if err != nil {
		return err
	}
	return atomicfile.WriteFileDurable(path, data, 0o600)
}

// ReadRuntimeRecord loads the runtime record at path. A missing record
// is reported as os.ErrNotExist, which callers treat as "no running
// instance claims this directory".
// readRuntimeRecordOf reads the runtime record of a data directory. A record
// for another data directory belongs to the path that the directory moved
// from, so it reads as absent (os.ErrNotExist).
func readRuntimeRecordOf(paths Paths) (RuntimeRecord, error) {
	record, err := ReadRuntimeRecord(paths.RuntimeRecord)
	if err == nil && record.DataDir != paths.DataDir {
		return RuntimeRecord{}, fmt.Errorf("the runtime record %s belongs to %s: %w", paths.RuntimeRecord, record.DataDir, os.ErrNotExist)
	}
	return record, err
}

func ReadRuntimeRecord(path string) (RuntimeRecord, error) {
	var rec RuntimeRecord
	data, err := useronly.ReadFile(path)
	if err != nil {
		return rec, err
	}
	if err := yaml.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("parse the runtime record %s: %w", path, err)
	}
	return rec, nil
}

// ReadControlToken loads the per-run bearer token. A missing token
// reads as an empty string, which authenticates nothing.
func ReadControlToken(path string) (string, error) {
	data, err := useronly.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// WriteControlToken durably publishes a fresh bearer token.
func WriteControlToken(path, token string) error {
	if err := verifyExistingPrivateFile(path); err != nil {
		return err
	}
	return atomicfile.WriteFileDurable(path, []byte(token), 0o600)
}
