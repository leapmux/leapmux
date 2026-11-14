//go:build windows

package procutil

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

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
	if err := c.execProcessCommand.start(); err != nil {
		return err
	}
	// Wait for native exit without reaping the command or releasing its handle.
	// Start must accept a command that ends before it captures the identity.
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
