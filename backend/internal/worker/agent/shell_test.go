package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildShellWrappedCommand_Bash_Interactive(t *testing.T) {
	cmd, delimiter, metaPrefix := buildShellWrappedCommand(
		context.Background(), "/bin/bash", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, []string{"--model", "opus"}, "/tmp",
	)
	require.NotEmpty(t, delimiter)
	assert.True(t, strings.HasPrefix(delimiter, "__LEAPMUX_READY_"))
	assert.True(t, strings.HasSuffix(delimiter, "__"))
	require.NotEmpty(t, metaPrefix)
	assert.True(t, strings.HasPrefix(metaPrefix, "__LEAPMUX_META_"))

	assert.Equal(t, "/bin/bash", cmd.Path)
	assert.Equal(t, "/tmp", cmd.Dir)
	require.Len(t, cmd.Args, 5) // bash -i -l -c <cmd>
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "echo '"+delimiter+"'")
	assert.Contains(t, cmd.Args[4], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "exec claude")
	assert.Contains(t, cmd.Args[4], "'--output-format'")
	assert.Contains(t, cmd.Args[4], "'--model'")
	assert.Contains(t, cmd.Args[4], "'opus'")
	// Verify conditional structure
	assert.Contains(t, cmd.Args[4], "CLAUDE_CODE_USE_BEDROCK")
	assert.Contains(t, cmd.Args[4], "can_change_model_and_effort=false")
	assert.Contains(t, cmd.Args[4], "can_change_model_and_effort=true")
	assert.Contains(t, cmd.Args[4], metaPrefix+"can_change_model_and_effort=")
}

func TestBuildShellWrappedCommand_Bash_NonInteractive(t *testing.T) {
	cmd, _, _ := buildShellWrappedCommand(
		context.Background(), "/bin/bash", false, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, []string{"--model", "opus"}, "/tmp",
	)
	assert.Equal(t, "/bin/bash", cmd.Path)
	require.Len(t, cmd.Args, 3) // bash -c <cmd>
	assert.Equal(t, "-c", cmd.Args[1])
	assert.Contains(t, cmd.Args[2], "exec claude")
}

func TestBuildShellWrappedCommand_Zsh(t *testing.T) {
	cmd, delimiter, _ := buildShellWrappedCommand(
		context.Background(), "/bin/zsh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--verbose"}, []string{"--model", "sonnet"}, "/home/user",
	)
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
	cmd, _, _ := buildShellWrappedCommand(
		context.Background(), "/usr/bin/fish", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--model", "sonnet"}, []string{}, "/tmp",
	)
	assert.Equal(t, "/usr/bin/fish", cmd.Path)
	require.Len(t, cmd.Args, 5) // fish -i -l -c <cmd>
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "exec claude")
	// No conditional (empty modelEffortArgs)
	assert.NotContains(t, cmd.Args[4], "CLAUDE_CODE_USE_BEDROCK")
}

func TestBuildShellWrappedCommand_Tcsh_Interactive(t *testing.T) {
	cmd, delimiter, _ := buildShellWrappedCommand(
		context.Background(), "/bin/tcsh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, []string{"--model", "opus"}, "/tmp",
	)
	assert.Equal(t, "/bin/tcsh", cmd.Path)
	require.Len(t, cmd.Args, 3) // tcsh -ic <cmd>
	assert.Equal(t, "-ic", cmd.Args[1])
	assert.Contains(t, cmd.Args[2], "echo '"+delimiter+"'")
	assert.Contains(t, cmd.Args[2], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[2], "exec claude")
}

func TestBuildShellWrappedCommand_Tcsh_NonInteractive(t *testing.T) {
	cmd, _, _ := buildShellWrappedCommand(
		context.Background(), "/bin/tcsh", false, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, []string{"--model", "opus"}, "/tmp",
	)
	assert.Equal(t, "/bin/tcsh", cmd.Path)
	require.Len(t, cmd.Args, 3) // tcsh -c <cmd>
	assert.Equal(t, "-c", cmd.Args[1])
}

func TestBuildShellWrappedCommand_Csh(t *testing.T) {
	cmd, _, _ := buildShellWrappedCommand(
		context.Background(), "/bin/csh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--verbose"}, []string{"--model", "opus"}, "/tmp",
	)
	assert.Equal(t, "/bin/csh", cmd.Path)
	require.Len(t, cmd.Args, 3) // csh -ic <cmd>
	assert.Equal(t, "-ic", cmd.Args[1])
	assert.Contains(t, cmd.Args[2], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[2], "exec claude")
}

func TestBuildShellWrappedCommand_Nu_Interactive(t *testing.T) {
	cmd, delimiter, _ := buildShellWrappedCommand(
		context.Background(), "/usr/bin/nu", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, []string{"--model", "opus"}, "/tmp",
	)
	assert.Equal(t, "/usr/bin/nu", cmd.Path)
	require.Len(t, cmd.Args, 5) // nu -i -l -c <cmd>
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "echo '"+delimiter+"'")
	assert.Contains(t, cmd.Args[4], "hide-env CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "^claude")
	assert.NotContains(t, cmd.Args[4], "exec")
	assert.Contains(t, cmd.Args[4], "CLAUDE_CODE_USE_BEDROCK")
	// Args should be double-quoted (nuQuote), not single-quoted (posixQuote)
	assert.Contains(t, cmd.Args[4], `"--output-format"`)
	assert.Contains(t, cmd.Args[4], `"stream-json"`)
	assert.Contains(t, cmd.Args[4], `"--model"`)
	assert.Contains(t, cmd.Args[4], `"opus"`)
}

func TestBuildNuCommand_SingleQuoteInArgs(t *testing.T) {
	inner := buildNuCommand("claude", []string{"CLAUDECODE"}, "__DELIM__", "__META__ ",
		[]string{"--output-format", "stream-json"},
		[]string{"--model", "it's-a-model"})
	// Single quotes in args must be safely double-quoted, not POSIX-quoted.
	assert.Contains(t, inner, `"it's-a-model"`)
	assert.NotContains(t, inner, `'\''`)
}

func TestBuildShellWrappedCommand_Nu_NonInteractive(t *testing.T) {
	cmd, _, _ := buildShellWrappedCommand(
		context.Background(), "/usr/bin/nu", false, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, []string{"--model", "opus"}, "/tmp",
	)
	assert.Equal(t, "/usr/bin/nu", cmd.Path)
	require.Len(t, cmd.Args, 3) // nu -c <cmd>
	assert.Equal(t, "-c", cmd.Args[1])
}

func TestBuildShellWrappedCommand_Pwsh_Interactive(t *testing.T) {
	for _, shell := range []string{"/usr/bin/pwsh", "/usr/bin/powershell", "/usr/bin/pwsh-preview", "/usr/bin/powershell-preview"} {
		t.Run(shell, func(t *testing.T) {
			cmd, delimiter, _ := buildShellWrappedCommand(
				context.Background(), shell, true, "claude",
				[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, []string{"--model", "opus"}, "/tmp",
			)
			assert.Equal(t, shell, cmd.Path)
			require.Len(t, cmd.Args, 4) // pwsh -Login -Command <cmd>
			assert.Equal(t, "-Login", cmd.Args[1])
			assert.Equal(t, "-Command", cmd.Args[2])
			assert.Contains(t, cmd.Args[3], "Write-Output '"+delimiter+"'")
			assert.Contains(t, cmd.Args[3], "Remove-Item Env:CLAUDECODE")
			assert.Contains(t, cmd.Args[3], "& claude")
			assert.NotContains(t, cmd.Args[3], "exec")
			assert.Contains(t, cmd.Args[3], "CLAUDE_CODE_USE_BEDROCK")
		})
	}
}

func TestBuildShellWrappedCommand_Pwsh_NonInteractive(t *testing.T) {
	cmd, _, _ := buildShellWrappedCommand(
		context.Background(), "/usr/bin/pwsh", false, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, []string{"--model", "opus"}, "/tmp",
	)
	assert.Equal(t, "/usr/bin/pwsh", cmd.Path)
	require.Len(t, cmd.Args, 3) // pwsh -Command <cmd>
	assert.Equal(t, "-Command", cmd.Args[1])
}

func TestBuildShellWrappedCommand_UnknownShell(t *testing.T) {
	cmd, _, _ := buildShellWrappedCommand(
		context.Background(), "/usr/bin/xonsh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--verbose"}, []string{"--model", "opus"}, "/tmp",
	)
	assert.Equal(t, "/usr/bin/xonsh", cmd.Path)
	require.Len(t, cmd.Args, 5) // defaults to -i -l -c
	assert.Equal(t, "-i", cmd.Args[1])
	assert.Equal(t, "-l", cmd.Args[2])
	assert.Equal(t, "-c", cmd.Args[3])
	assert.Contains(t, cmd.Args[4], "unset CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "exec claude")
}

func TestBuildShellWrappedCommand_NoModelEffort(t *testing.T) {
	// When third-party provider was detected from settings, modelEffortArgs is nil.
	// No conditional logic should be generated.
	cmd, _, metaPrefix := buildShellWrappedCommand(
		context.Background(), "/bin/bash", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, nil, "/tmp",
	)
	assert.Empty(t, metaPrefix)
	assert.Contains(t, cmd.Args[4], "'--output-format'")
	assert.NotContains(t, cmd.Args[4], "'--model'")
	assert.NotContains(t, cmd.Args[4], "CLAUDE_CODE_USE_BEDROCK")
	assert.NotContains(t, cmd.Args[4], "can_change_model_and_effort")
}

func TestBuildShellWrappedCommand_NoModelEffort_Nu(t *testing.T) {
	cmd, _, metaPrefix := buildShellWrappedCommand(
		context.Background(), "/usr/bin/nu", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, nil, "/tmp",
	)
	assert.Empty(t, metaPrefix)
	assert.NotContains(t, cmd.Args[4], "CLAUDE_CODE_USE_BEDROCK")
}

func TestBuildShellWrappedCommand_NoModelEffort_Pwsh(t *testing.T) {
	cmd, _, metaPrefix := buildShellWrappedCommand(
		context.Background(), "/usr/bin/pwsh", true, "claude",
		[]string{"CLAUDECODE"}, []string{"--output-format", "stream-json"}, nil, "/tmp",
	)
	assert.Empty(t, metaPrefix)
	assert.NotContains(t, cmd.Args[3], "CLAUDE_CODE_USE_BEDROCK")
}

func TestBuildShellWrappedCommand_ModelEffortInElseBranch(t *testing.T) {
	inner := buildPosixCommand("claude", []string{"CLAUDECODE"}, "__DELIM__", "__META__ ", []string{"--output-format", "stream-json"}, []string{"--model", "opus", "--effort", "high"}, true)

	// The else branch should contain model/effort args
	parts := strings.SplitN(inner, "else", 2)
	require.Len(t, parts, 2, "expected if/else structure")
	assert.Contains(t, parts[1], "'--model'")
	assert.Contains(t, parts[1], "'--effort'")
	assert.Contains(t, parts[1], "'opus'")
	assert.Contains(t, parts[1], "'high'")

	// The if branch (third-party provider) should NOT contain model/effort args
	assert.NotContains(t, parts[0], "'--model'")
	assert.NotContains(t, parts[0], "'--effort'")
}

func TestBuildShellWrappedCommand_CodexUsesCodexEnvMarkers(t *testing.T) {
	cmd, delimiter, metaPrefix := buildShellWrappedCommand(
		context.Background(), "/bin/zsh", true, "codex",
		[]string{"CODEX_CI"}, []string{"app-server"}, nil, "/tmp",
	)
	assert.Empty(t, metaPrefix)
	assert.Contains(t, cmd.Args[4], "unset CODEX_CI")
	assert.NotContains(t, cmd.Args[4], "CLAUDECODE")
	assert.Contains(t, cmd.Args[4], "echo '"+delimiter+"'")
	assert.Contains(t, cmd.Args[4], "exec codex")
}

func TestBuildShellWrappedCommand_CodexUsesCodexEnvMarkers_NuAndPwsh(t *testing.T) {
	nuCmd, _, _ := buildShellWrappedCommand(
		context.Background(), "/usr/bin/nu", true, "codex",
		[]string{"CODEX_CI"}, []string{"app-server"}, nil, "/tmp",
	)
	assert.Contains(t, nuCmd.Args[4], "hide-env CODEX_CI")
	assert.NotContains(t, nuCmd.Args[4], "CLAUDECODE")

	pwshCmd, _, _ := buildShellWrappedCommand(
		context.Background(), "/usr/bin/pwsh", true, "codex",
		[]string{"CODEX_CI"}, []string{"app-server"}, nil, "/tmp",
	)
	assert.Contains(t, pwshCmd.Args[3], "Remove-Item Env:CODEX_CI")
	assert.NotContains(t, pwshCmd.Args[3], "CLAUDECODE")
}

func TestBuildModelEffortArgs(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		effort   string
		expected []string
	}{
		{
			name:     "opus with max effort",
			model:    "opus",
			effort:   "max",
			expected: []string{"--model", "opus", "--effort", "max"},
		},
		{
			name:     "opus[1m] with max effort",
			model:    "opus[1m]",
			effort:   "max",
			expected: []string{"--model", "opus[1m]", "--effort", "max"},
		},
		{
			name:     "sonnet with max effort falls back to high",
			model:    "sonnet",
			effort:   "max",
			expected: []string{"--model", "sonnet", "--effort", "high"},
		},
		{
			name:     "sonnet[1m] with max effort falls back to high",
			model:    "sonnet[1m]",
			effort:   "max",
			expected: []string{"--model", "sonnet[1m]", "--effort", "high"},
		},
		{
			name:     "sonnet with high effort unchanged",
			model:    "sonnet",
			effort:   "high",
			expected: []string{"--model", "sonnet", "--effort", "high"},
		},
		{
			name:     "haiku omits effort entirely",
			model:    "haiku",
			effort:   "high",
			expected: []string{"--model", "haiku"},
		},
		{
			name:     "haiku omits max effort",
			model:    "haiku",
			effort:   "max",
			expected: []string{"--model", "haiku"},
		},
		{
			name:     "empty effort omitted",
			model:    "sonnet",
			effort:   "",
			expected: []string{"--model", "sonnet"},
		},
		{
			name:     "auto effort",
			model:    "sonnet",
			effort:   "auto",
			expected: []string{"--model", "sonnet", "--effort", "auto"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildModelEffortArgs(tt.model, tt.effort)
			assert.Equal(t, tt.expected, args)
		})
	}
}

func TestPosixQuote(t *testing.T) {
	assert.Equal(t, "'hello'", posixQuote("hello"))
	assert.Equal(t, "'it'\\''s'", posixQuote("it's"))
	assert.Equal(t, "''", posixQuote(""))
}

func TestNuQuote(t *testing.T) {
	assert.Equal(t, `"hello"`, nuQuote("hello"))
	assert.Equal(t, `"it's"`, nuQuote("it's"))
	assert.Equal(t, `"say \"hi\""`, nuQuote(`say "hi"`))
	assert.Equal(t, `"back\\slash"`, nuQuote(`back\slash`))
	assert.Equal(t, `""`, nuQuote(""))
}

func TestPwshQuote(t *testing.T) {
	assert.Equal(t, "'hello'", pwshQuote("hello"))
	assert.Equal(t, "'it''s'", pwshQuote("it's"))
	assert.Equal(t, "''", pwshQuote(""))
}

func TestIsPwsh(t *testing.T) {
	assert.True(t, terminal.IsPwsh("pwsh"))
	assert.True(t, terminal.IsPwsh("powershell"))
	assert.True(t, terminal.IsPwsh("pwsh-preview"))
	assert.True(t, terminal.IsPwsh("powershell-preview"))
	assert.False(t, terminal.IsPwsh("bash"))
	assert.False(t, terminal.IsPwsh("zsh"))
	assert.False(t, terminal.IsPwsh("pwsh-extra-stuff"))
}
