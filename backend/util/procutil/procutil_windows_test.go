//go:build windows

package procutil

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"

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

func TestDetachManagedServicePreservesAttributesAndRequestsJobBreakaway(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("service.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	original := cmd.SysProcAttr
	DetachManagedService(cmd)
	DetachManagedService(cmd)
	assert.Same(t, original, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.HideWindow)
	assert.Equal(t, uint32(windows.CREATE_NO_WINDOW|windows.CREATE_BREAKAWAY_FROM_JOB|windows.CREATE_NEW_PROCESS_GROUP), cmd.SysProcAttr.CreationFlags)
}

func TestDetachManagedServiceAllocatesMissingWindowsAttributes(t *testing.T) {
	t.Parallel()
	cmd := exec.Command("service.exe")
	DetachManagedService(cmd)
	require.NotNil(t, cmd.SysProcAttr)
	assert.Equal(t, uint32(windows.CREATE_BREAKAWAY_FROM_JOB|windows.CREATE_NEW_PROCESS_GROUP), cmd.SysProcAttr.CreationFlags)
}
