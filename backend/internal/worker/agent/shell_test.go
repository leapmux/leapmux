package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildShellWrappedCommand_Bash(t *testing.T) {
	cmd, delimiter := buildShellWrappedCommand(context.Background(), "/bin/bash", []string{"--model", "opus"}, "/tmp")
	require.NotEmpty(t, delimiter)
	assert.True(t, strings.HasPrefix(delimiter, "__LEAPMUX_READY_"))
	assert.True(t, strings.HasSuffix(delimiter, "__"))

	assert.Equal(t, "/bin/bash", cmd.Path)
	assert.Equal(t, "/tmp", cmd.Dir)
	require.Len(t, cmd.Args, 5) // bash -i -l -c <cmd>
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "echo '"+delimiter+"'")
	assert.Contains(t, cmd.Args[4], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "exec claude")
	assert.Contains(t, cmd.Args[4], "'--model'")
	assert.Contains(t, cmd.Args[4], "'opus'")
}

func TestBuildShellWrappedCommand_Zsh(t *testing.T) {
	cmd, delimiter := buildShellWrappedCommand(context.Background(), "/bin/zsh", []string{"--verbose"}, "/home/user")
	assert.Equal(t, "/bin/zsh", cmd.Path)
	require.Len(t, cmd.Args, 5) // zsh -i -l -c <cmd>
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "exec claude")
	assert.Contains(t, cmd.Args[4], delimiter)
}

func TestBuildShellWrappedCommand_Fish(t *testing.T) {
	cmd, _ := buildShellWrappedCommand(context.Background(), "/usr/bin/fish", []string{"--model", "sonnet"}, "/tmp")
	assert.Equal(t, "/usr/bin/fish", cmd.Path)
	require.Len(t, cmd.Args, 5) // fish -i -l -c <cmd>
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "exec claude")
}

func TestBuildShellWrappedCommand_Tcsh(t *testing.T) {
	cmd, delimiter := buildShellWrappedCommand(context.Background(), "/bin/tcsh", []string{"--model", "opus"}, "/tmp")
	assert.Equal(t, "/bin/tcsh", cmd.Path)
	require.Len(t, cmd.Args, 3) // tcsh -ic <cmd>
	assert.Equal(t, "-ic", cmd.Args[1])
	assert.Contains(t, cmd.Args[2], "echo '"+delimiter+"'")
	assert.Contains(t, cmd.Args[2], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[2], "exec claude")
}

func TestBuildShellWrappedCommand_Csh(t *testing.T) {
	cmd, _ := buildShellWrappedCommand(context.Background(), "/bin/csh", []string{"--verbose"}, "/tmp")
	assert.Equal(t, "/bin/csh", cmd.Path)
	require.Len(t, cmd.Args, 3) // csh -ic <cmd>
	assert.Equal(t, "-ic", cmd.Args[1])
	assert.Contains(t, cmd.Args[2], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[2], "exec claude")
}

func TestBuildShellWrappedCommand_Nu(t *testing.T) {
	cmd, delimiter := buildShellWrappedCommand(context.Background(), "/usr/bin/nu", []string{"--model", "opus"}, "/tmp")
	assert.Equal(t, "/usr/bin/nu", cmd.Path)
	require.Len(t, cmd.Args, 5) // nu -i -l -c <cmd>
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "echo '"+delimiter+"'")
	assert.Contains(t, cmd.Args[4], "hide-env CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "^claude")
	assert.NotContains(t, cmd.Args[4], "exec")
}

func TestBuildShellWrappedCommand_Pwsh(t *testing.T) {
	for _, shell := range []string{"/usr/bin/pwsh", "/usr/bin/powershell", "/usr/bin/pwsh-preview", "/usr/bin/powershell-preview"} {
		t.Run(shell, func(t *testing.T) {
			cmd, delimiter := buildShellWrappedCommand(context.Background(), shell, []string{"--model", "opus"}, "/tmp")
			assert.Equal(t, shell, cmd.Path)
			require.Len(t, cmd.Args, 4) // pwsh -Login -Command <cmd>
			assert.Equal(t, "-Login", cmd.Args[1])
			assert.Equal(t, "-Command", cmd.Args[2])
			assert.Contains(t, cmd.Args[3], "Write-Output '"+delimiter+"'")
			assert.Contains(t, cmd.Args[3], "Remove-Item Env:CLAUDECODE")
			assert.Contains(t, cmd.Args[3], "& claude")
			assert.NotContains(t, cmd.Args[3], "exec")
		})
	}
}

func TestBuildShellWrappedCommand_UnknownShell(t *testing.T) {
	cmd, _ := buildShellWrappedCommand(context.Background(), "/usr/bin/xonsh", []string{"--verbose"}, "/tmp")
	assert.Equal(t, "/usr/bin/xonsh", cmd.Path)
	require.Len(t, cmd.Args, 5) // defaults to -i -l -c
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "exec claude")
}

func TestPosixQuote(t *testing.T) {
	assert.Equal(t, "'hello'", posixQuote("hello"))
	assert.Equal(t, "'it'\\''s'", posixQuote("it's"))
	assert.Equal(t, "''", posixQuote(""))
}

func TestPwshQuote(t *testing.T) {
	assert.Equal(t, "'hello'", pwshQuote("hello"))
	assert.Equal(t, "'it''s'", pwshQuote("it's"))
	assert.Equal(t, "''", pwshQuote(""))
}

func TestIsPwsh(t *testing.T) {
	assert.True(t, isPwsh("pwsh"))
	assert.True(t, isPwsh("powershell"))
	assert.True(t, isPwsh("pwsh-preview"))
	assert.True(t, isPwsh("powershell-preview"))
	assert.False(t, isPwsh("bash"))
	assert.False(t, isPwsh("zsh"))
	assert.False(t, isPwsh("pwsh-extra-stuff"))
}
