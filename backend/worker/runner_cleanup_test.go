package worker

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/lifecycle"
)

func TestRunJoinsCleanupFailuresAfterDatabaseClose(t *testing.T) {
	root, err := lifecycle.CanonicalDir(t.TempDir())
	require.NoError(t, err)
	owner := lifecycle.NewDirectoryOwner(lifecycle.RoleWorker, root)
	lease, err := lifecycle.AcquireDirectory(owner, lifecycle.RoleWorker, root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lease.Close() })
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	waitCtx, stopWaiting := context.WithTimeout(t.Context(), 30*time.Second)
	defer stopWaiting()
	ctx, cancel := context.WithCancel(waitCtx)
	databaseFailure, directoryFailure := errors.New("database cleanup failed"), errors.New("directory cleanup failed")
	closing, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var released sync.Once
	var databaseCalls, directoryCalls atomic.Int32
	var result, nativeDatabaseClose error
	cleanup := runCleanup{
		closeDatabase: func(database *sql.DB) error {
			nativeDatabaseClose = database.Close()
			databaseCalls.Add(1)
			close(closing)
			<-release
			return errors.Join(nativeDatabaseClose, databaseFailure)
		},
		closeDirectory: func(directory *lifecycle.DirectoryLease) error {
			directoryCalls.Add(1)
			return errors.Join(directory.Close(), directoryFailure)
		},
	}
	go func() {
		defer close(done)
		result = run(ctx, RunConfig{HubURL: "http://127.0.0.1:1", Directory: lease, CompositeKey: key, AuthToken: "token", WorkerID: "worker", EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM, OnReady: cancel}, cleanup)
	}()
	t.Cleanup(func() { cancel(); released.Do(func() { close(release) }); <-done })
	select {
	case <-closing:
	case <-done:
		t.Fatalf("Run returned before database cleanup: %v", result)
	case <-waitCtx.Done():
		t.Fatal("database cleanup did not begin")
	}
	assert.NoError(t, nativeDatabaseClose)
	held, err := lifecycle.ProbeLockAt(lifecycle.PathsOf(root).DataLock)
	require.NoError(t, err)
	assert.True(t, held, "the directory must remain owned while database cleanup is parked")
	assert.Zero(t, directoryCalls.Load())
	select {
	case <-done:
		t.Error("Run returned before database cleanup completed")
	default:
	}
	released.Do(func() { close(release) })
	select {
	case <-done:
	case <-waitCtx.Done():
		t.Fatal("Run did not finish cleanup")
	}
	require.ErrorIs(t, result, databaseFailure)
	require.ErrorIs(t, result, directoryFailure)
	assert.EqualValues(t, 1, databaseCalls.Load())
	assert.EqualValues(t, 1, directoryCalls.Load())
	replacement, err := lifecycle.AcquireDirectory(owner, lifecycle.RoleWorker, root)
	require.NoError(t, err)
	require.NoError(t, replacement.Close())
}

func TestRunRetainsCleanupFailureWhenStartupIsRejected(t *testing.T) {
	root, err := lifecycle.CanonicalDir(t.TempDir())
	require.NoError(t, err)
	lease, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(lifecycle.RoleWorker, root), lifecycle.RoleWorker, root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lease.Close() })
	failure := errors.New("rejected worker directory cleanup failed")
	err = run(t.Context(), RunConfig{Directory: lease}, runCleanup{
		closeDatabase:  func(*sql.DB) error { t.Error("rejected key material must not open a database"); return nil },
		closeDirectory: func(directory *lifecycle.DirectoryLease) error { return errors.Join(directory.Close(), failure) },
	})
	require.ErrorIs(t, err, failure)
	assert.ErrorContains(t, err, "CompositeKey")
	assert.True(t, lease.Closed())
}
