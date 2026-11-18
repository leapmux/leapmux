//go:build windows

package procutil

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"

	pty "github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// suspendCountCommand measures the initial thread before the owner assigns its job.
type suspendCountCommand struct {
	processCommand
	keepExtraSuspension bool
	previousCount       uint32
	probeErr            error
}

func (c *suspendCountCommand) start() error {
	if err := c.processCommand.start(); err != nil {
		return err
	}
	c.previousCount, c.probeErr = probeInitialThreadSuspendCount(uint32(c.process().Pid), c.keepExtraSuspension)
	return nil
}

func probeInitialThreadSuspendCount(pid uint32, keepExtraSuspension bool) (count uint32, result error) {
	threadID, err := initialThreadID(pid)
	if err != nil {
		return 0, err
	}
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, threadID)
	if err != nil {
		return 0, err
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(thread)) }()
	suspendThread := windows.NewLazySystemDLL("kernel32.dll").NewProc("SuspendThread")
	previous, _, suspendErr := suspendThread.Call(uintptr(thread))
	if uint32(previous) == ^uint32(0) {
		return 0, suspendErr
	}
	if !keepExtraSuspension {
		// Restore the suspend count so the owner still controls the first instruction.
		_, err = windows.ResumeThread(thread)
	}
	return uint32(previous), err
}

func TestWindowsProcessOwnerSuspendsTheChildUntilJobAssignment(t *testing.T) {
	owner, probe, stdin, output := prepareSuspensionProbe(t, false)

	require.NoError(t, owner.Start())
	require.NoError(t, probe.probeErr)
	require.EqualValues(t, 1, probe.previousCount, "the child must be suspended before the owner assigns its job")
	require.NoError(t, stdin.Close())
	require.NoError(t, owner.Wait())
	require.Equal(t, "ready\n", output.String(), "the child must run after job assignment")
	require.NoError(t, owner.Close())
}

func TestWindowsProcessOwnerReapsTheChildWhenResumeLeavesItSuspended(t *testing.T) {
	owner, probe, _, output := prepareSuspensionProbe(t, true)

	startErr := owner.Start()
	require.NoError(t, probe.probeErr)
	require.EqualValues(t, 1, probe.previousCount)
	require.ErrorContains(t, startErr, "unexpected suspend count of 2")
	require.NotNil(t, owner.ProcessState(), "failed start must reap the child")
	require.False(t, owner.ProcessState().Success())
	require.Empty(t, output.String(), "the child must never run")
	require.Zero(t, owner.platform.job.handle.Load(), "failed start must release the job")
	require.ErrorIs(t, owner.Close(), startErr)
}

func TestWindowsPTYProcessSuspendsTheChildUntilJobAssignment(t *testing.T) {
	terminal, err := pty.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = terminal.Close() })
	owner, err := PreparePTYProcess(testutil.DeadlineContext(t), terminal, PTYLaunch{
		Program: os.Args[0],
		Args:    []string{"-test.run=^TestHelperProcessOwnerExitsBeforeIdentity$"},
		Env:     append(os.Environ(), "LEAPMUX_TEST_OWNER_EXIT_CODE=0"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = owner.Close()
		_ = owner.Wait()
	})
	probe := &suspendCountCommand{processCommand: owner.cmd}
	owner.cmd = probe

	require.NoError(t, owner.Start())
	require.NoError(t, probe.probeErr)
	require.EqualValues(t, 1, probe.previousCount)
	require.NoError(t, owner.Wait())
	require.True(t, owner.ProcessState().Success())
	require.NoError(t, owner.Close())
}

func prepareSuspensionProbe(t *testing.T, keepExtraSuspension bool) (*ProcessOwner, *suspendCountCommand, io.WriteCloser, *bytes.Buffer) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperProcessWaitsForStdin$")
	cmd.Env = append(os.Environ(), waitHelperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	var output bytes.Buffer
	cmd.Stdout = &output
	owner := PrepareProcess(cmd)
	probe := &suspendCountCommand{
		processCommand:      owner.cmd,
		keepExtraSuspension: keepExtraSuspension,
	}
	owner.cmd = probe
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = owner.Close()
		_ = owner.Wait()
	})
	return owner, probe, stdin, &output
}

func TestHelperProcessOwnerExitsBeforeIdentity(t *testing.T) {
	value, ok := os.LookupEnv("LEAPMUX_TEST_OWNER_EXIT_CODE")
	if !ok {
		return
	}
	code, err := strconv.Atoi(value)
	require.NoError(t, err)
	_, _ = os.Stdout.WriteString("command completed\n")
	os.Exit(code)
}

type exitedBeforeIdentityCommand struct{ execProcessCommand }

func (c exitedBeforeIdentityCommand) start() error {
	if err := c.command.Start(); err != nil {
		return err
	}
	// Wait for native exit without reaping the command or releasing its handle.
	// This test starts the helper directly so it can exit before identity capture.
	var waitErr error
	handleErr := c.command.Process.WithHandle(func(handle uintptr) {
		status, err := windows.WaitForSingleObject(windows.Handle(handle), 30_000)
		if err != nil {
			waitErr = err
		} else if status != windows.WAIT_OBJECT_0 {
			waitErr = fmt.Errorf("the helper process did not exit: wait status %d", status)
		}
	})
	if err := errors.Join(handleErr, waitErr); err != nil {
		return errors.Join(err, c.command.Process.Kill(), c.execProcessCommand.wait())
	}
	return nil
}

func TestWindowsProcessOwnerAcceptsExitBeforeIdentityCapture(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessOwnerExitsBeforeIdentity$")
			cmd.Env = append(os.Environ(), "LEAPMUX_TEST_OWNER_EXIT_CODE="+strconv.Itoa(code))
			var output bytes.Buffer
			cmd.Stdout = &output
			owner := PrepareProcess(cmd)
			owner.cmd = exitedBeforeIdentityCommand{execProcessCommand{command: cmd}}
			t.Cleanup(func() {
				_ = owner.Close()
				_ = owner.Wait()
			})
			require.NoError(t, owner.Start())
			require.Equal(t, cmd.Process.Pid, owner.PID())
			require.Positive(t, owner.root.StartTime)
			require.False(t, owner.root.Runs())
			waitErr := owner.Wait()
			if code == 0 {
				require.NoError(t, waitErr)
			} else {
				var exitErr *exec.ExitError
				require.ErrorAs(t, waitErr, &exitErr)
				require.Equal(t, code, exitErr.ExitCode())
			}
			require.NotNil(t, owner.ProcessState())
			require.Equal(t, code, owner.ProcessState().ExitCode())
			require.Equal(t, "command completed\n", output.String())
			require.NoError(t, owner.Close())
			require.NoError(t, owner.Err())
		})
	}
}
