package procutil

import (
	"bytes"
	"os"
	"os/exec"
	"sync"
	"testing"

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
