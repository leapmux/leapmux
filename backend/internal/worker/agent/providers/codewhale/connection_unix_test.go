//go:build unix

package codewhale

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/util/procutil"
)

// orphanRuntimePort is the port in the arguments of an orphan runtime. A reclaim
// reads only the arguments of a runtime, so the fake binds no port, and a
// listener of another test cannot take this port away from it.
const orphanRuntimePort = 40123

// orphanRuntime starts the fake runtime process as a runtime an earlier worker
// left behind: in a group of its own, with nobody waiting for it. leader
// controls whether the process leads its own group, which is what lets a group
// kill reach it.
//
// It returns after the process itself reports that it runs. exec.Cmd.Start can
// return before the kernel finishes loading the new program. The start time of
// the process reads back at that moment, but its arguments read as empty, and
// processMatches then reports no runtime. The line comes from the new program,
// so the arguments are readable once it arrives.
func orphanRuntime(t *testing.T, leader bool) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperCodewhaleRuntime", "--", "app-server", "--http", "--host", "127.0.0.1", "--port", strconv.Itoa(orphanRuntimePort))
	cmd.Env = append(os.Environ(), fakeProcessWantEnv+"=1", fakeProcessScenarioEnv+"=orphan", envRuntimeDir+"="+t.TempDir(), envRuntimeToken+"=orphan")
	if leader {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	// A pipe of this test's own, because exec.Cmd.StdoutPipe forbids the Wait
	// below until the read ends, and that read must run with the cleanup in place.
	stdout, stdoutWriter, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = stdout.Close() })
	cmd.Stdout, cmd.Stderr = stdoutWriter, os.Stderr
	require.NoError(t, cmd.Start())
	require.NoError(t, stdoutWriter.Close())
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err, "the fake runtime ended before it reported that it runs")
	require.Equal(t, fakeOrphanReady, strings.TrimSpace(line))
	return cmd
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

// The reclaim reads the arguments of a runtime, so a runtime that this fixture
// returns must already show them. A reclaim that finds other arguments ends at
// once, and a test that waits for its timer then fails only after a deadline.
func TestOrphanRuntimeShowsItsArgumentsOnceStarted(t *testing.T) {
	t.Parallel()
	for attempt := range 20 {
		t.Run(strconv.Itoa(attempt), func(t *testing.T) {
			t.Parallel()
			cmd := orphanRuntime(t, false)
			assert.True(t, processRuns(cmd, runtimeOwner(t, cmd, orphanRuntimePort)))
		})
	}
}

func TestReclaimStoreEndsTheRuntimeADeadWorkerLeft(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	cmd := orphanRuntime(t, true)
	owner := runtimeOwner(t, cmd, orphanRuntimePort)
	owner.WorkerPID, owner.WorkerCreateTime = deadProcess(t)
	writeOwner(t, store, owner)
	require.True(t, processRuns(cmd, owner))

	require.NoError(t, reclaimStore(context.Background(), store, quartz.NewReal()))
	assert.False(t, processRuns(cmd, owner), "the orphan no longer holds the store")
}

func TestReclaimStoreRefusesAStoreALiveWorkerOwns(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	cmd := orphanRuntime(t, true)
	owner := runtimeOwner(t, cmd, orphanRuntimePort)
	owner.WorkerPID, owner.WorkerCreateTime = liveProcess(t)
	writeOwner(t, store, owner)

	assert.ErrorIs(t, reclaimStore(context.Background(), store, quartz.NewReal()), errStoreHeldByLiveWorker)
	assert.True(t, processRuns(cmd, owner), "another worker's runtime is left alone")
}

func TestReclaimStoreLeavesThisWorkersOwnRuntime(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	cmd := orphanRuntime(t, true)
	owner := runtimeOwner(t, cmd, orphanRuntimePort)
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
	cmd := orphanRuntime(t, true)
	owner := runtimeOwner(t, cmd, orphanRuntimePort)
	owner.Port = orphanRuntimePort + 1
	owner.WorkerPID, owner.WorkerCreateTime = deadProcess(t)
	writeOwner(t, store, owner)
	require.NoError(t, reclaimStore(ctx, store, quartz.NewReal()))
	assert.True(t, processMatches(ctx, int32(cmd.Process.Pid), owner.RuntimeCreateTime, orphanRuntimePort))
}

func TestReclaimStoreGivesUpOnARuntimeThatDoesNotExit(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	// A native process cannot ignore SIGKILL. Keep it live through an explicit stop refusal.
	cmd := orphanRuntime(t, false)
	owner := runtimeOwner(t, cmd, orphanRuntimePort)
	owner.WorkerPID, owner.WorkerCreateTime = deadProcess(t)
	writeOwner(t, store, owner)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(orphanTimerTag)
	t.Cleanup(newTimer.Close)
	ctx := testutil.DeadlineContext(t)

	done := make(chan error, 1)
	go func() {
		done <- reclaimStoreWithStop(ctx, store, clock, func(identity procutil.ProcessIdentity) error {
			assert.Equal(t, int(owner.RuntimePID), identity.PID)
			assert.Equal(t, owner.RuntimeCreateTime, identity.StartTime)
			return nil
		})
	}()
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
	// The stop operation leaves the runtime live so cancellation ends the wait.
	cmd := orphanRuntime(t, false)
	owner := runtimeOwner(t, cmd, orphanRuntimePort)
	owner.WorkerPID, owner.WorkerCreateTime = deadProcess(t)
	writeOwner(t, store, owner)
	clock := testutil.NewQuartzMock(t)
	newTimer := clock.Trap().NewTimer(orphanTimerTag)
	t.Cleanup(newTimer.Close)
	deadline := testutil.DeadlineContext(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- reclaimStoreWithStop(ctx, store, clock, func(procutil.ProcessIdentity) error { return nil })
	}()
	call := newTimer.MustWait(deadline)
	assert.Equal(t, orphanExitPoll, call.Duration)
	cancel()
	call.MustRelease(deadline)
	assert.ErrorIs(t, <-done, context.Canceled)
}

func TestReclaimStorePreservesVerifiedStopErrors(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	cmd := orphanRuntime(t, true)
	owner := runtimeOwner(t, cmd, orphanRuntimePort)
	owner.WorkerPID, owner.WorkerCreateTime = deadProcess(t)
	writeOwner(t, store, owner)
	stopErr := errors.New("the verified native signal failed")
	err := reclaimStoreWithStop(t.Context(), store, quartz.NewReal(), func(identity procutil.ProcessIdentity) error {
		assert.Equal(t, int(owner.RuntimePID), identity.PID)
		assert.Equal(t, owner.RuntimeCreateTime, identity.StartTime)
		return stopErr
	})
	require.ErrorIs(t, err, stopErr)
	assert.True(t, processRuns(cmd, owner))
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
