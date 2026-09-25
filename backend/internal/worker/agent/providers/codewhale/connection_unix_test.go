//go:build unix

package codewhale

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// orphanRuntime starts the fake runtime process as a runtime an earlier worker
// left behind: in a group of its own, on a port, with nobody waiting for it.
// leader controls whether the process leads its own group, which is what lets a
// group kill reach it.
func orphanRuntime(t *testing.T, leader bool) (*exec.Cmd, int) {
	t.Helper()
	port, err := providerkit.ReserveLoopbackPort()
	require.NoError(t, err)
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperCodewhaleRuntime", "--", "app-server", "--http", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Env = append(os.Environ(), fakeProcessWantEnv+"=1", envRuntimeDir+"="+t.TempDir(), envRuntimeToken+"=orphan")
	if leader {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	require.NoError(t, cmd.Start())
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	// The runtime is up once its identity reads back.
	require.Eventually(t, func() bool {
		_, ok := processIdentity(context.Background(), int32(cmd.Process.Pid))
		return ok
	}, 30*time.Second, 10*time.Millisecond)
	return cmd, port
}

// deadProcess returns the identity of a process that already exited.
func deadProcess(t *testing.T) (int32, int64) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	require.NoError(t, cmd.Start())
	pid := int32(cmd.Process.Pid)
	created, _ := processIdentity(context.Background(), pid)
	require.NoError(t, cmd.Wait())
	return pid, created
}

// liveProcess returns the identity of a process that keeps running for the test.
func liveProcess(t *testing.T) (int32, int64) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "sleep 60")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	pid := int32(cmd.Process.Pid)
	var created int64
	require.Eventually(t, func() bool {
		var ok bool
		created, ok = processIdentity(context.Background(), pid)
		return ok
	}, 30*time.Second, 10*time.Millisecond)
	return pid, created
}

// writeOwner writes a store's ownership record.
func writeOwner(t *testing.T, store codewhaleStore, owner storeOwner) {
	t.Helper()
	encoded, err := json.Marshal(owner)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(store.dir, ownerFileName), encoded, 0o600))
}

func runtimeOwner(t *testing.T, cmd *exec.Cmd, port int) storeOwner {
	t.Helper()
	created, ok := processIdentity(context.Background(), int32(cmd.Process.Pid))
	require.True(t, ok)
	return storeOwner{RuntimePID: int32(cmd.Process.Pid), RuntimeCreateTime: created, Port: port}
}

func processRuns(cmd *exec.Cmd, owner storeOwner) bool {
	return processMatches(context.Background(), int32(cmd.Process.Pid), owner.RuntimeCreateTime, owner.Port)
}

func TestReclaimStoreEndsTheRuntimeADeadWorkerLeft(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	cmd, port := orphanRuntime(t, true)
	owner := runtimeOwner(t, cmd, port)
	owner.WorkerPID, owner.WorkerCreateTime = deadProcess(t)
	writeOwner(t, store, owner)
	require.True(t, processRuns(cmd, owner))

	require.NoError(t, reclaimStore(context.Background(), store, quartz.NewReal()))
	assert.False(t, processRuns(cmd, owner), "the orphan no longer holds the store")
}

func TestReclaimStoreRefusesAStoreALiveWorkerOwns(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	cmd, port := orphanRuntime(t, true)
	owner := runtimeOwner(t, cmd, port)
	owner.WorkerPID, owner.WorkerCreateTime = liveProcess(t)
	writeOwner(t, store, owner)

	assert.ErrorIs(t, reclaimStore(context.Background(), store, quartz.NewReal()), errStoreHeldByLiveWorker)
	assert.True(t, processRuns(cmd, owner), "another worker's runtime is left alone")
}

func TestReclaimStoreLeavesThisWorkersOwnRuntime(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	cmd, port := orphanRuntime(t, true)
	owner := runtimeOwner(t, cmd, port)
	owner.WorkerPID = int32(os.Getpid())
	writeOwner(t, store, owner)

	require.NoError(t, reclaimStore(context.Background(), store, quartz.NewReal()))
	assert.True(t, processRuns(cmd, owner), "the runtime's own lock error states a second agent of this worker")
}

func TestReclaimStoreIgnoresARecordThatNamesNoRuntime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := codewhaleStore{dir: t.TempDir()}
	require.NoError(t, reclaimStore(ctx, store, quartz.NewReal()), "a store with no record")

	require.NoError(t, os.WriteFile(filepath.Join(store.dir, ownerFileName), []byte("{broken"), 0o600))
	require.NoError(t, reclaimStore(ctx, store, quartz.NewReal()), "an unreadable record")

	pid, created := deadProcess(t)
	writeOwner(t, store, storeOwner{RuntimePID: pid, RuntimeCreateTime: created, Port: 1})
	require.NoError(t, reclaimStore(ctx, store, quartz.NewReal()), "a runtime that already exited")

	// A process that is not a runtime on that port is never ended.
	cmd, port := orphanRuntime(t, true)
	owner := runtimeOwner(t, cmd, port)
	owner.Port = port + 1
	owner.WorkerPID, owner.WorkerCreateTime = deadProcess(t)
	writeOwner(t, store, owner)
	require.NoError(t, reclaimStore(ctx, store, quartz.NewReal()))
	assert.True(t, processMatches(ctx, int32(cmd.Process.Pid), owner.RuntimeCreateTime, port))
}

func TestReclaimStoreGivesUpOnARuntimeThatDoesNotExit(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	// A process outside a group of its own survives the group kill, which is how
	// a runtime that does not exit looks to the wait.
	cmd, port := orphanRuntime(t, false)
	owner := runtimeOwner(t, cmd, port)
	owner.WorkerPID, owner.WorkerCreateTime = deadProcess(t)
	writeOwner(t, store, owner)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(orphanTimerTag)
	t.Cleanup(newTimer.Close)
	ctx := testutil.DeadlineContext(t)

	done := make(chan error, 1)
	go func() { done <- reclaimStore(ctx, store, clock) }()
	waited := time.Duration(0)
	for waited < orphanExitWait {
		call := newTimer.MustWait(ctx)
		assert.Equal(t, orphanExitPoll, call.Duration)
		call.MustRelease(ctx)
		clock.Advance(orphanExitPoll).MustWait(ctx)
		waited += orphanExitPoll
	}
	err := <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not exit")
}

// A start that ends while it waits for an orphan to exit ends the wait at once.
func TestReclaimStoreStopsWaitingWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	// A process outside a group of its own survives the group kill, so the wait
	// has something to wait for.
	cmd, port := orphanRuntime(t, false)
	owner := runtimeOwner(t, cmd, port)
	owner.WorkerPID, owner.WorkerCreateTime = deadProcess(t)
	writeOwner(t, store, owner)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(orphanTimerTag)
	t.Cleanup(newTimer.Close)
	deadline := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- reclaimStore(ctx, store, clock) }()
	call := newTimer.MustWait(deadline)
	assert.Equal(t, orphanExitPoll, call.Duration)
	cancel()
	call.MustRelease(deadline)
	assert.ErrorIs(t, <-done, context.Canceled)
}

func TestRecordStoreOwnerNamesThisWorkerAndTheRuntime(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	recordStoreOwner(store, os.Getpid(), 4242)

	data, err := os.ReadFile(filepath.Join(store.dir, ownerFileName))
	require.NoError(t, err)
	var owner storeOwner
	require.NoError(t, json.Unmarshal(data, &owner))
	assert.Equal(t, int32(os.Getpid()), owner.RuntimePID)
	assert.Equal(t, 4242, owner.Port)
	assert.Equal(t, int32(os.Getpid()), owner.WorkerPID)
	assert.NotZero(t, owner.WorkerCreateTime)
	_, err = os.Stat(filepath.Join(store.dir, ownerFileName+".tmp"))
	assert.ErrorIs(t, err, os.ErrNotExist, "the record is written by rename")

	// A store that vanished takes no record and raises nothing.
	recordStoreOwner(codewhaleStore{dir: filepath.Join(store.dir, "absent")}, os.Getpid(), 1)
}
