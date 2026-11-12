package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ownership record round-trips its exact shape, and a malformed or
// half-written marker is a refusal, never a silent default.
func TestOwnershipRecordRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "record")
	paths := pathsFor(mustCanonical(t, root))
	require.NoError(t, atomicfile.MkdirAllPrivate(paths.Dir))
	rec := ownershipRecord{
		AccountID:     "501",
		Mode:          RoleWorker,
		DataDirectory: mustCanonical(t, root),
		OwnerMode:     RoleSolo,
		OwnerDataDir:  filepath.Join(mustCanonical(t, root), "..", "solo"),
	}
	require.NoError(t, writeOwnershipRecord(paths.OwnershipRecord, rec))
	stored, err := readOwnershipRecord(paths.OwnershipRecord)
	require.NoError(t, err)
	assert.Equal(t, rec, stored)

	raw, err := os.ReadFile(paths.OwnershipRecord)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `account_id: "501"`)

	// A malformed marker refuses rather than adopting a default.
	require.NoError(t, os.WriteFile(paths.OwnershipRecord, []byte("account_id: [broken\n"), 0o600))
	_, err = readOwnershipRecord(paths.OwnershipRecord)
	require.Error(t, err)
}

// The saved service specification round-trips, and its absence reads as
// ErrNotExist so callers can distinguish unconfigured from broken.
func TestServiceSpecRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spec")
	paths := pathsFor(mustCanonical(t, root))
	require.NoError(t, atomicfile.MkdirAllPrivate(paths.Dir))

	_, err := ReadServiceSpec(paths.ServiceSpec)
	require.ErrorIs(t, err, os.ErrNotExist)

	envFile := "/etc/leapmux/worker.env"
	spec := ServiceSpec{
		InstanceID:         "5fe1000000000000",
		Mode:               RoleWorker,
		Startup:            StartupLogin,
		AccountID:          "501",
		AccountName:        "trustin",
		Executable:         "/opt/homebrew/bin/leapmux",
		WorkingDirectory:   "/Users/trustin",
		ConfigFile:         "/Users/trustin/.config/leapmux/worker/worker.yaml",
		DataDirectory:      "/Users/trustin/.config/leapmux/worker",
		RuntimeArguments:   []string{},
		RuntimeEnvironment: map[string]string{"PATH": "/opt/homebrew/bin:/usr/bin:/bin"},
		EnvironmentFile:    nil,
	}
	require.NoError(t, WriteServiceSpec(paths.ServiceSpec, spec))
	stored, err := ReadServiceSpec(paths.ServiceSpec)
	require.NoError(t, err)
	assert.Equal(t, spec, stored)

	// The environment file slot round-trips through a non-nil value.
	withFile := spec
	withFile.EnvironmentFile = &envFile
	require.NoError(t, WriteServiceSpec(paths.ServiceSpec, withFile))
	stored, err = ReadServiceSpec(paths.ServiceSpec)
	require.NoError(t, err)
	require.NotNil(t, stored.EnvironmentFile)
	assert.Equal(t, envFile, *stored.EnvironmentFile)
}

// Paths identifies every file the plan's layout states.
func TestPathsLayout(t *testing.T) {
	root := mustCanonical(t, filepath.Join(t.TempDir(), "worker"))
	paths := pathsFor(root)
	assert.Equal(t, root, paths.DataDir)
	assert.Equal(t, filepath.Join(root, ".lifecycle"), paths.Dir)
	assert.Equal(t, filepath.Join(root, ".lifecycle", "directory.yaml"), paths.OwnershipRecord)
	assert.Equal(t, filepath.Join(root, ".lifecycle", "data.lock"), paths.DataLock)
	assert.Equal(t, filepath.Join(root, ".lifecycle", "service.yaml"), paths.ServiceSpec)
	assert.Equal(t, filepath.Join(root, ".lifecycle", "runtime.yaml"), paths.RuntimeRecord)
	assert.Equal(t, filepath.Join(root, ".lifecycle", "control.token"), paths.ControlToken)
	assert.Equal(t, filepath.Join(root, ".lifecycle", "operation.lock"), paths.OperationLock)
	assert.Equal(t, filepath.Join(root, ".lifecycle", "runtime.lock"), paths.RuntimeLock)
	assert.Equal(t, filepath.Join(root, ".lifecycle", "logs"), paths.Logs)
	assert.Equal(t, filepath.Join(root, ".lifecycle", "logs", "a9a0425886d148f68a8f94716ed47a78.log"),
		paths.AttemptLogPath("a9a0425886d148f68a8f94716ed47a78"))
}

// The control endpoint URL derives from the identity, and the two
// transport spellings are the ones locallisten parses.
func TestControlURLDerivesFromIdentity(t *testing.T) {
	first := ControlURL(NewInstanceID("501", RoleWorker, "/data/worker"))
	second := ControlURL(NewInstanceID("501", RoleWorker, "/data/worker"))
	assert.Equal(t, first, second, "one identity, one endpoint")
	other := ControlURL(NewInstanceID("501", RoleHub, "/data/worker"))
	assert.NotEqual(t, first, other, "another identity, another endpoint")
}

// RuntimeRecord round-trips through its YAML form with every field
// intact, and a zero-value record survives too: the stop verb reads
// records a crashed writer may have left half-defaulted.
func TestRuntimeRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.yaml")

	in := RuntimeRecord{
		InstanceID:   "5fe1000000000000",
		AttemptID:    "a9a0425886d148f68a8f94716ed47a78",
		Mode:         RoleWorker,
		Execution:    ExecutionService,
		DataDir:      dir,
		PID:          4127,
		ProcessStart: 1762343530000,
		StartedAt:    time.Date(2026, 11, 5, 14, 32, 10, 0, time.UTC),
		ControlURL:   "unix:/tmp/leapmux-501/5fe1.sock",
	}
	require.NoError(t, WriteRuntimeRecord(path, in))
	out, err := ReadRuntimeRecord(path)
	require.NoError(t, err)
	assert.Equal(t, in, out)

	// The zero-value record: every field empty, the time zero.
	require.NoError(t, WriteRuntimeRecord(path, RuntimeRecord{}))
	out, err = ReadRuntimeRecord(path)
	require.NoError(t, err)
	assert.Equal(t, RuntimeRecord{}, out)

	// A missing record reports os.ErrNotExist, which callers treat as
	// "no instance claims this directory".
	_, err = ReadRuntimeRecord(filepath.Join(dir, "absent.yaml"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
