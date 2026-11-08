package lifecycle

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/filelock"
	"github.com/leapmux/leapmux/locallisten"
)

func TestObserveHeldLockWithUnreadableTokenIsUnresponsive(t *testing.T) {
	for _, fault := range []string{"missing", "directory"} {
		t.Run(fault, func(t *testing.T) {
			runtimeEnv(t)
			r := startTestRuntime(t, RoleWorker, t.TempDir())
			require.NoError(t, os.Remove(r.Paths().ControlToken))
			if fault == "directory" {
				require.NoError(t, os.Mkdir(r.Paths().ControlToken, 0o700))
			}
			status, err := Observe(t.Context(), r.Paths().DataDir)
			require.ErrorIs(t, err, ErrUnresponsive)
			assert.Equal(t, ProcessUnresponsive, status.Process.State)
		})
	}
}

func TestObserveClosesItsControlConnection(t *testing.T) {
	runtimeEnv(t)
	root := mustCanonical(t, t.TempDir())
	lease, err := AcquireDirectory(NewDirectoryOwner(RoleWorker, root), RoleWorker, root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lease.Close() })
	paths := pathsFor(root)
	lock, err := filelock.Open(paths.RuntimeLock, false)
	require.NoError(t, err)
	held, err := filelock.TryLock(lock)
	require.NoError(t, err)
	require.True(t, held)
	t.Cleanup(func() { _ = lock.Close() })
	attempt, err := NewAttemptID()
	require.NoError(t, err)
	instance := NewInstanceID(CurrentAccountID(), RoleWorker, root)
	record := RuntimeRecord{
		InstanceID: instance.String(), AttemptID: attempt, Mode: RoleWorker,
		Execution: ExecutionForeground, DataDir: root, PID: os.Getpid(),
		StartedAt: time.Now().UTC(), ControlURL: ControlURL(instance),
	}
	require.NoError(t, WriteRuntimeRecord(paths.RuntimeRecord, record))
	require.NoError(t, WriteControlToken(paths.ControlToken, "control-token"))
	listener, err := locallisten.Listen(record.ControlURL)
	require.NoError(t, err)
	closed := make(chan struct{}, 4)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			status := Status{InstanceID: new(instance.String()), Attempt: AttemptStatus{ID: new(attempt)}}
			if encodeErr := json.NewEncoder(w).Encode(status); encodeErr != nil {
				t.Errorf("encode control status: %v", encodeErr)
			}
		}),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				closed <- struct{}{}
			}
		},
		ReadHeaderTimeout: 30 * time.Second,
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		require.ErrorIs(t, <-served, http.ErrServerClosed)
	})
	for range 3 {
		_, err := Observe(context.Background(), root)
		require.NoError(t, err)
		select {
		case <-closed:
		case <-time.After(30 * time.Second):
			t.Fatal("status observation retained an idle control connection")
		}
	}
}

func TestHeldRuntimeWithoutRecordIsUnresponsive(t *testing.T) {
	root := mustCanonical(t, t.TempDir())
	directory, err := AcquireDirectory(NewDirectoryOwner(RoleHub, root), RoleHub, root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = directory.Close() })
	lock, err := filelock.Open(pathsFor(root).RuntimeLock, false)
	require.NoError(t, err)
	held, err := filelock.TryLock(lock)
	require.NoError(t, err)
	require.True(t, held)
	t.Cleanup(func() { _ = lock.Close() })
	status, err := Observe(context.Background(), root)
	require.ErrorIs(t, err, ErrUnresponsive)
	assert.Equal(t, ProcessUnresponsive, status.Process.State)
}

func TestShutdownWaitDoesNotAcceptProbeFailure(t *testing.T) {
	root := mustCanonical(t, t.TempDir())
	paths := pathsFor(root)
	require.NoError(t, os.MkdirAll(paths.RuntimeLock, 0o700))
	// The deadline only keeps a regression from hanging the test: a probe
	// failure must end the wait at once, not when the deadline passes.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := waitForLockRelease(ctx, root)
	require.Error(t, err, "an unreadable or invalid lock is not proof of completed shutdown")
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
}

func TestShutdownWaitEndsWithTheContextWhileTheLockIsHeld(t *testing.T) {
	root := mustCanonical(t, t.TempDir())
	paths := pathsFor(root)
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.RuntimeLock), 0o700))
	lock, err := filelock.Open(paths.RuntimeLock, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })
	held, err := filelock.TryLock(lock)
	require.NoError(t, err)
	require.True(t, held)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, waitForLockRelease(ctx, root), context.Canceled)
}

func TestShutdownWaitReturnsOnceTheLockIsFree(t *testing.T) {
	for _, test := range []struct {
		name     string
		lockFile bool
	}{
		{"no lock file", false},
		{"an unlocked lock file", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := mustCanonical(t, t.TempDir())
			paths := pathsFor(root)
			require.NoError(t, os.MkdirAll(filepath.Dir(paths.RuntimeLock), 0o700))
			if test.lockFile {
				lock, err := filelock.Open(paths.RuntimeLock, false)
				require.NoError(t, err)
				require.NoError(t, lock.Close())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, waitForLockRelease(ctx, root))
		})
	}
}

func TestObserveRejectsDifferentInstanceResponse(t *testing.T) {
	runtimeEnv(t)
	first := startTestRuntime(t, RoleWorker, t.TempDir())
	second := startTestRuntime(t, RoleWorker, t.TempDir())
	firstLease, secondLease := first.Lease(), second.Lease()
	t.Cleanup(func() {
		_ = firstLease.Close()
		_ = secondLease.Close()
	})
	record := first.Record()
	record.ControlURL = second.Record().ControlURL
	require.NoError(t, WriteRuntimeRecord(first.Paths().RuntimeRecord, record))
	otherToken, err := ReadControlToken(second.Paths().ControlToken)
	require.NoError(t, err)
	require.NoError(t, WriteControlToken(first.Paths().ControlToken, otherToken))
	status, err := Observe(context.Background(), firstLease.Path())
	require.Error(t, err, "an authenticated response from another instance is not this instance's status")
	assert.Equal(t, ProcessUnresponsive, status.Process.State)
	assert.Equal(t, new(first.Record().InstanceID), status.InstanceID)
	assert.False(t, first.StopRequested())
	assert.False(t, second.StopRequested())
}

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
	assert.Equal(t, new(record.AttemptID), status.Attempt.ID)
}

// A released lock without a saved outcome reports stopped.
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
