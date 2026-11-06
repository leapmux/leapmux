package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/filelock"
)

// A held runtime lock whose endpoint does not answer is reported as
// unresponsive, never stopped: the evidence says the process still
// exists, and a missing endpoint alone cannot outweigh it.
func TestHeldRuntimeWithoutEndpointIsUnresponsive(t *testing.T) {
	runtimeEnv(t)
	root := filepath.Join(t.TempDir(), "held")
	paths := pathsFor(mustCanonical(t, root))
	require.NoError(t, os.MkdirAll(paths.Dir, 0o700))

	// A published record and token, and a runtime lock this test holds.
	lock, err := filelock.Open(paths.RuntimeLock, false)
	require.NoError(t, err)
	held, err := filelock.TryLock(lock)
	require.NoError(t, err)
	require.True(t, held)
	t.Cleanup(func() {
		_ = filelock.Unlock(lock)
		_ = lock.Close()
	})
	require.NoError(t, WriteControlToken(paths.ControlToken, "token-material"))
	record := RuntimeRecord{
		InstanceID: "held0000000000001",
		AttemptID:  "b9a0425886d148f68a8f94716ed47a78",
		Mode:       RoleWorker,
		Execution:  ExecutionService,
		DataDir:    mustCanonical(t, root),
		PID:        4127,
		ControlURL: "unix:" + runtimeEnv(t) + "/nonexistent.sock",
	}
	require.NoError(t, WriteRuntimeRecord(paths.RuntimeRecord, record))

	status, err := Observe(context.Background(), root)
	require.ErrorIs(t, err, ErrUnresponsive)
	assert.Equal(t, ProcessUnresponsive, status.Process.State,
		"a held lock with a silent endpoint is unresponsive, never stopped")
	assert.Equal(t, record.AttemptID, status.Attempt.ID)
}

// A free lock with a record is stopped, whatever the record says.
func TestObserveReportsAReleasedLockAsStopped(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gone")
	paths := pathsFor(mustCanonical(t, root))
	require.NoError(t, os.MkdirAll(paths.Dir, 0o700))
	record := RuntimeRecord{
		InstanceID: "gone0000000000001",
		AttemptID:  "c9a0425886d148f68a8f94716ed47a78",
		Mode:       RoleWorker,
		Execution:  ExecutionService,
		DataDir:    mustCanonical(t, root),
		PID:        4127,
		ControlURL: "unix:/nonexistent.sock",
	}
	require.NoError(t, WriteRuntimeRecord(paths.RuntimeRecord, record))

	status, err := Observe(context.Background(), root)
	require.NoError(t, err)
	assert.Equal(t, ProcessStopped, status.Process.State)
}

// A directory nothing claims reads as stopped with no error.
func TestObserveAcceptsAnUnclaimedDirectory(t *testing.T) {
	status, err := Observe(context.Background(), filepath.Join(t.TempDir(), "nothing"))
	require.NoError(t, err)
	assert.Equal(t, ProcessStopped, status.Process.State)
	assert.Equal(t, RegistrationNotApplicable, status.Registration.State)
}
