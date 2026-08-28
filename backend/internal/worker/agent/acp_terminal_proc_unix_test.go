//go:build unix

package agent

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigureACPTerminalCmdStartsANewSession(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "true")
	configureACPTerminalCmd(cmd)
	require.NotNil(t, cmd.SysProcAttr)
	assert.True(t, cmd.SysProcAttr.Setsid, "ACP host commands must drop the worker ctty")
	assert.False(t, cmd.SysProcAttr.Setpgid, "Setsid+Setpgid is EPERM at Start")
	require.NotNil(t, cmd.Cancel)
	require.NoError(t, cmd.Run(), "Setsid without Setpgid must be a valid Start")
}
