package procutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestHelperProcessOwnerPlainCommand(t *testing.T) {
	if os.Getenv("LEAPMUX_TEST_PLAIN_OWNER_COMMAND") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString("plain-command-completed\n")
	os.Exit(0)
}

func TestPrepareProcessStartsPlainCommand(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessOwnerPlainCommand$")
	cmd.Env = append(os.Environ(), "LEAPMUX_TEST_PLAIN_OWNER_COMMAND=1")
	var output bytes.Buffer
	cmd.Stdout = &output
	owner := PrepareProcess(cmd)
	t.Cleanup(func() { _ = owner.Close() })
	require.NoError(t, owner.Start(), "the owner must support a plain command without adding an invalid Cancel function")
	require.NoError(t, owner.Wait())
	require.NoError(t, owner.Close())
	require.Equal(t, "plain-command-completed\n", output.String())
}

func TestVerifiedDescendantsKeepsAnEndedRootAsNoNewOwnership(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperProcessOwnerPlainCommand$")
	cmd.Env = append(os.Environ(), "LEAPMUX_TEST_PLAIN_OWNER_COMMAND=1")
	owner := PrepareProcess(cmd)
	t.Cleanup(func() { _ = owner.Close() })
	require.NoError(t, owner.Start())
	root := owner.root
	require.NoError(t, owner.Wait())
	table := &ProcessTable{}
	children, err := table.VerifiedDescendants(t.Context(), root)
	require.NoError(t, err, "a naturally ended root must not become an ownership error")
	require.Empty(t, children)
}

func TestProcessOwnerWaitUsesOneNativeWaitForConcurrentCallers(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperProcessOwnerPlainCommand$")
	cmd.Env = append(os.Environ(), "LEAPMUX_TEST_PLAIN_OWNER_COMMAND=1")
	owner := PrepareProcess(cmd)
	t.Cleanup(func() { _ = owner.Close() })
	require.NoError(t, owner.Start())
	require.Nil(t, owner.ProcessState(), "native process state is absent before the wait completes")
	results := make(chan error, 8)
	var callers sync.WaitGroup
	for range 8 {
		callers.Add(1)
		go func() { defer callers.Done(); results <- owner.Wait() }()
	}
	callers.Wait()
	for range 8 {
		require.NoError(t, <-results)
	}
	require.NotNil(t, owner.ProcessState())
	require.True(t, owner.ProcessState().Success())
	require.NoError(t, owner.Wait())
}

func TestProcessOwnerWaitRejectsAbsentAndUnstartedCommands(t *testing.T) {
	require.ErrorContains(t, (*ProcessOwner)(nil).Wait(), "owner is absent")
	owner := PrepareProcess(nil)
	require.ErrorContains(t, owner.Wait(), "no started command")
	require.Nil(t, owner.ProcessState())
	require.Zero(t, owner.PID())
	require.NoError(t, owner.Close())
}

func startObservedOwner(t *testing.T, observer ProcessExitObserver) *ProcessOwner {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessWaitsForStdin$")
	cmd.Env = append(os.Environ(), waitHelperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	owner := PrepareProcessWithExitObserver(cmd, observer)
	t.Cleanup(func() {
		_ = owner.Close()
		_ = stdin.Close()
		_ = owner.Wait()
	})
	require.NoError(t, owner.Start())
	ready := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		ready <- line
	}()
	select {
	case line := <-ready:
		require.Equal(t, "ready\n", line)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the observed owner did not start its native child")
	}
	return owner
}

func TestPrepareProcessWithNilObserverKeepsNativeLifecycle(t *testing.T) {
	owner := startObservedOwner(t, nil)
	require.Positive(t, owner.PID())
	require.NoError(t, owner.Close())
	var exitErr *exec.ExitError
	require.ErrorAs(t, owner.Wait(), &exitErr)
	require.NotNil(t, owner.ProcessState())
	require.False(t, owner.ProcessState().Success())
	require.NoError(t, owner.Err())
}

func TestProcessExitObserverConstructorsKeepAbsentProcessChecks(t *testing.T) {
	var calls atomic.Int32
	observer := processExitObserverFunc(func(context.Context, ProcessIdentity, error) error {
		calls.Add(1)
		return nil
	})
	owner := PrepareProcessWithExitObserver(nil, observer)
	require.ErrorContains(t, owner.Start(), "cannot start this command")
	require.ErrorContains(t, owner.Wait(), "no started command")
	require.Zero(t, owner.PID())
	require.Nil(t, owner.ProcessState())
	require.NoError(t, owner.Close())
	for _, identity := range []ProcessIdentity{{}, {PID: -1, StartTime: 1}, {PID: os.Getpid(), StartTime: 1}} {
		adopted, err := OwnStartedProcessWithExitObserver(identity, observer)
		require.Error(t, err)
		require.Nil(t, adopted)
	}
	require.Zero(t, calls.Load())
}

func TestProcessOwnerPendingTerminationKeepsNativeWaitAndOneResult(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	failure := errors.New("the exit observer failed after native completion")
	var calls atomic.Int32
	observer := processExitObserverFunc(func(ctx context.Context, _ ProcessIdentity, nativeErr error) error {
		calls.Add(1)
		if nativeErr != nil {
			return nativeErr
		}
		close(entered)
		select {
		case <-release:
			return failure
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	owner := startObservedOwner(t, observer)
	pid := owner.PID()
	results := make(chan error, 2)
	go func() { results <- owner.Close() }()
	select {
	case <-entered:
	case result := <-results:
		t.Fatalf("termination returned before the observer started: %v", result)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the native exit observer did not start")
	}
	require.Equal(t, pid, owner.PID())
	require.NoError(t, owner.Err())
	require.Nil(t, owner.ProcessState())
	require.NoError(t, owner.Capture(t.Context()))
	require.NoError(t, owner.Cancel())
	require.ErrorContains(t, owner.Start(), "cannot start this command")
	require.ErrorContains(t, owner.BindDescendants(t.Context()), "before its descendants could be bound")
	var exitErr *exec.ExitError
	require.ErrorAs(t, owner.Wait(), &exitErr)
	require.NotNil(t, owner.ProcessState())
	go func() { results <- owner.Terminate() }()
	select {
	case <-results:
		t.Fatal("a termination caller returned before the held observer")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	readResult := func() error {
		select {
		case result := <-results:
			return result
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("termination did not finish after the observer released")
			return nil
		}
	}
	first, second := readResult(), readResult()
	require.ErrorIs(t, first, failure)
	require.Same(t, first, second)
	require.NoError(t, owner.Capture(t.Context()))
	require.NoError(t, owner.Cancel())
	require.Same(t, first, owner.Err())
	require.Same(t, first, owner.Close())
	require.EqualValues(t, 1, calls.Load())
}

type endedStartCommand struct {
	command     *exec.Cmd
	waitEntered chan struct{}
	waitRelease chan struct{}
	waitErr     error
}

func (*endedStartCommand) start() error                    { return nil }
func (command *endedStartCommand) process() *os.Process    { return command.command.Process }
func (command *endedStartCommand) state() *os.ProcessState { return command.command.ProcessState }
func (command *endedStartCommand) wait() error {
	close(command.waitEntered)
	<-command.waitRelease
	return command.waitErr
}

func TestProcessOwnerTerminationRetainsAnInFlightFailedStartResult(t *testing.T) {
	child := startWaitingProcess(t)
	require.True(t, child.endedByItself(t))
	waitFailure := errors.New("the native failed-start wait returned an error")
	command := &endedStartCommand{
		command: child.cmd, waitEntered: make(chan struct{}), waitRelease: make(chan struct{}), waitErr: waitFailure,
	}
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(command.waitRelease) })
	owner := PrepareProcess(nil)
	owner.cmd = command
	started := make(chan error, 1)
	go func() { started <- owner.Start() }()
	select {
	case <-command.waitEntered:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("Start did not wait for its ended native child")
	}
	closed := make(chan error, 1)
	closeStarted := make(chan struct{})
	go func() { close(closeStarted); closed <- owner.Close() }()
	<-closeStarted
	select {
	case <-closed:
		t.Fatal("Close returned before the failed Start result settled")
	default:
	}
	releaseOnce.Do(func() { close(command.waitRelease) })
	readResult := func(result <-chan error) error {
		select {
		case err := <-result:
			return err
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("the failed Start or termination did not finish")
			return nil
		}
	}
	startErr := readResult(started)
	require.ErrorContains(t, startErr, "no creation identity")
	require.ErrorIs(t, startErr, waitFailure)
	closeErr := readResult(closed)
	require.ErrorIs(t, closeErr, waitFailure)
	require.Same(t, closeErr, owner.Close())
	require.Same(t, closeErr, owner.Err())
}
