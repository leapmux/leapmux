package solo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/lifecycle"
)

func cleanupRootLease(t *testing.T) *lifecycle.DirectoryLease {
	t.Helper()
	path, err := lifecycle.CanonicalDir(t.TempDir())
	require.NoError(t, err)
	root, err := lifecycle.AcquireDirectory(lifecycle.NewDirectoryOwner(lifecycle.RoleSolo, path), lifecycle.RoleSolo, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func completedCleanupSignal() chan struct{} {
	signal := make(chan struct{})
	close(signal)
	return signal
}

func TestStopRetainsLeaseFailuresForWorkerThatNeverStarted(t *testing.T) {
	root := cleanupRootLease(t)
	workerLease := root.TakeComponent(lifecycle.RoleWorker)
	workerFailure, rootFailure := errors.New("unstarted worker cleanup failed"), errors.New("combined root cleanup failed")
	instance := &Instance{rootLease: root, workerLease: workerLease, hubDone: completedCleanupSignal(), closeDirectory: func(lease *lifecycle.DirectoryLease) error {
		if err := lease.Close(); err != nil {
			return err
		}
		if lease == workerLease {
			return workerFailure
		}
		return rootFailure
	}}
	result := instance.Stop()
	require.ErrorIs(t, result, workerFailure)
	require.ErrorIs(t, result, rootFailure)
	for range 3 {
		require.ErrorIs(t, instance.Stop(), rootFailure)
	}
	replacement, err := lifecycle.AcquireDirectory(root.Owner(), lifecycle.RoleSolo, root.Path())
	require.NoError(t, err)
	require.NoError(t, replacement.Close())
}

func TestStopJoinsLateCleanupAndKeepsWaitContract(t *testing.T) {
	root := cleanupRootLease(t)
	workerLease := root.TakeComponent(lifecycle.RoleWorker)
	hubLease := root.TakeComponent(lifecycle.RoleHub)
	workerFailure, rootFailure := errors.New("transferred worker cleanup failed"), errors.New("root release failed")
	workerCtx, cancelWorker := context.WithCancel(t.Context())
	defer cancelWorker()
	workerCancelled, releaseWorker, releaseWatcher := make(chan struct{}), make(chan struct{}), make(chan struct{})
	watcherEntered, stopped := make(chan struct{}), make(chan struct{})
	var workerRelease, watcherRelease sync.Once
	instance := &Instance{rootLease: root, workerLease: workerLease, hubDone: make(chan struct{}), watcherDone: make(chan struct{}), failureCh: make(chan struct{})}
	instance.closeDirectory = func(lease *lifecycle.DirectoryLease) error {
		if lease == root {
			select {
			case <-instance.workerWork.DoneChan():
			default:
				t.Error("root released before worker completion")
			}
			select {
			case <-instance.watcherDone:
			default:
				t.Error("root released before watcher completion")
			}
		}
		if err := lease.Close(); err != nil {
			return err
		}
		if lease == workerLease {
			return workerFailure
		}
		return rootFailure
	}
	instance.cancelWorker = func() { cancelWorker(); close(workerCancelled) }
	instance.cancelHub = func() { instance.hubErr = hubLease.Close(); close(instance.hubDone) }
	instance.workerWork.Add()
	instance.workerDrained.Add()
	go func() {
		defer instance.workerWork.Done()
		<-workerCancelled
		instance.workerDrained.Done()
		<-releaseWorker
		instance.workerExited(workerCtx, instance.closeDirectory(workerLease))
	}()
	go func() { <-instance.hubDone; close(watcherEntered); <-releaseWatcher; close(instance.watcherDone) }()
	var result error
	go func() { result = instance.Stop(); close(stopped) }()
	t.Cleanup(func() {
		workerRelease.Do(func() { close(releaseWorker) })
		watcherRelease.Do(func() { close(releaseWatcher) })
		<-stopped
	})
	guard, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	select {
	case <-instance.hubDone:
	case <-guard.Done():
		t.Fatal("Hub shutdown did not complete")
	}
	waitResult, waitDone := make(chan error, 1), make(chan struct{})
	go func() { defer close(waitDone); waitResult <- instance.Wait() }()
	t.Cleanup(func() {
		workerRelease.Do(func() { close(releaseWorker) })
		watcherRelease.Do(func() { close(releaseWatcher) })
		<-waitDone
	})
	select {
	case err := <-waitResult:
		require.NoError(t, err)
	case <-guard.Done():
		t.Fatal("Wait must retain its Hub-completion contract")
	}
	held, err := lifecycle.ProbeLockAt(lifecycle.PathsOf(root.Path()).DataLock)
	require.NoError(t, err)
	assert.True(t, held)
	workerRelease.Do(func() { close(releaseWorker) })
	select {
	case <-instance.workerWork.DoneChan():
	case <-guard.Done():
		t.Fatal("worker cleanup did not finish")
	}
	<-watcherEntered
	held, err = lifecycle.ProbeLockAt(lifecycle.PathsOf(root.Path()).DataLock)
	require.NoError(t, err)
	assert.True(t, held, "retain the root until the watcher joins")
	watcherRelease.Do(func() { close(releaseWatcher) })
	select {
	case <-stopped:
	case <-guard.Done():
		t.Fatal("Stop did not finish")
	}
	require.ErrorIs(t, result, workerFailure)
	require.ErrorIs(t, result, rootFailure)
	assert.Equal(t, 1, strings.Count(result.Error(), workerFailure.Error()), "transferred lease failure must not be counted twice")
	var callers sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		callers.Go(func() { results <- instance.Stop() })
	}
	callers.Wait()
	close(results)
	for err := range results {
		assert.ErrorIs(t, err, workerFailure)
		assert.ErrorIs(t, err, rootFailure)
		assert.Equal(t, 1, strings.Count(err.Error(), workerFailure.Error()))
	}
	replacement, err := lifecycle.AcquireDirectory(root.Owner(), lifecycle.RoleSolo, root.Path())
	require.NoError(t, err)
	require.NoError(t, replacement.Close())
}

func TestRejectedPreparedStartupRetainsReleaseFailure(t *testing.T) {
	for _, kind := range []string{"missing preparation", "wrong mode", "invalid log level"} {
		t.Run(kind, func(t *testing.T) {
			root := cleanupRootLease(t)
			path := filepath.Join(root.Path(), "hub", "hub.db")
			original := []byte("preserve existing application data")
			require.NoError(t, os.WriteFile(path, original, 0o600))
			failure := errors.New("rejected startup release failed")
			d := defaultDeps()
			d.closeDirectory = func(lease *lifecycle.DirectoryLease) error {
				if err := lease.Close(); err != nil {
					return err
				}
				return failure
			}
			var prepared *PreparedConfig
			switch kind {
			case "wrong mode":
				prepared = &PreparedConfig{role: lifecycle.RoleDev, dataDir: root.Path()}
			case "invalid log level":
				prepared = &PreparedConfig{role: lifecycle.RoleSolo, dataDir: root.Path(), hubCfg: &hubconfig.Config{LogLevel: "invalid-level", DataDir: root.Path()}}
			}
			instance, err := startPrepared(t.Context(), prepared, root, d)
			require.Nil(t, instance)
			require.ErrorIs(t, err, failure)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, original, after)
			replacement, err := lifecycle.AcquireDirectory(root.Owner(), lifecycle.RoleSolo, root.Path())
			require.NoError(t, err)
			require.NoError(t, replacement.Close())
		})
	}
}

func TestFailedStartupRetainsReleaseFailure(t *testing.T) {
	root := cleanupRootLease(t)
	cause, failure := errors.New("startup failed"), errors.New("startup cleanup failed")
	instance := &Instance{rootLease: root, hubDone: completedCleanupSignal(), closeDirectory: func(lease *lifecycle.DirectoryLease) error {
		if err := lease.Close(); err != nil {
			return err
		}
		return failure
	}}
	err := instance.failStartup(cause)
	require.ErrorIs(t, err, cause)
	require.ErrorIs(t, err, failure)
}
