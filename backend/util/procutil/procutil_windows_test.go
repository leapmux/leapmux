//go:build windows

package procutil

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetachFromTerminalDoesNotClearHideConsoleWindow(t *testing.T) {
	cmd := exec.Command("true")
	HideConsoleWindow(cmd)
	require.NotNil(t, cmd.SysProcAttr)
	require.True(t, cmd.SysProcAttr.HideWindow)

	DetachFromTerminal(cmd)
	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.HideWindow,
		"DetachFromTerminal must not replace SysProcAttr on Windows")
}

func TestDetachFromTerminalDoesNotAllocateSysProcAttr(t *testing.T) {
	cmd := exec.Command("true")
	DetachFromTerminal(cmd)
	assert.Nil(t, cmd.SysProcAttr, "the Windows no-op must not allocate SysProcAttr")
}

func TestSignalProcessGroupIsNoopOnNilProcess(t *testing.T) {
	require.NoError(t, SignalProcessGroup(nil, 0))
	require.NoError(t, SignalProcessGroup(exec.Command("true"), 0))
}
