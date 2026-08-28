//go:build !windows

package procutil

import (
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetachFromTerminalSetsSetsid(t *testing.T) {
	cmd := exec.Command("true")
	DetachFromTerminal(cmd)
	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.Setsid)
}

func TestDetachFromTerminalClearsSetpgidAndNoctty(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Noctty: true, Pgid: 1, Foreground: true}
	DetachFromTerminal(cmd)
	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.Setsid)
	assert.False(t, cmd.SysProcAttr.Setpgid, "Setpgid after setsid is EPERM")
	assert.False(t, cmd.SysProcAttr.Noctty, "Noctty after setsid is ENOTTY")
	assert.False(t, cmd.SysProcAttr.Foreground)
	assert.Equal(t, 0, cmd.SysProcAttr.Pgid)
}

func TestDetachFromTerminalIsIdempotent(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setpgid: true}
	DetachFromTerminal(cmd)
	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.Setsid)
	assert.False(t, cmd.SysProcAttr.Setpgid)
}

func TestDetachFromTerminalStartSucceedsAfterSetpgidWasSet(t *testing.T) {
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Noctty: true}
	DetachFromTerminal(cmd)
	require.NoError(t, cmd.Run(), "Setsid must not combine with Setpgid/Noctty at Start")
}

func TestAssignCmdRecordsThePidAsPgid(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	DetachFromTerminal(cmd)
	require.NoError(t, cmd.Start())
	job, err := AssignCmd(cmd)
	require.NoError(t, err)
	require.NotNil(t, job)
	require.NoError(t, job.Terminate())
	err = cmd.Wait()
	require.Error(t, err, "Terminate must kill the Setsid child")
}

func TestSignalProcessGroupIsNoopOnNilProcess(t *testing.T) {
	require.NoError(t, SignalProcessGroup(nil, syscall.SIGTERM))
	require.NoError(t, SignalProcessGroup(exec.Command("true"), syscall.SIGTERM))
}
