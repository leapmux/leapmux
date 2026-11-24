package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
)

func TestDaemonLogsPrintsTrailingAndAllLines(t *testing.T) {
	root := mustCanonicalDir(t, t.TempDir())
	rt, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{
		Mode:      lifecycle.RoleHub,
		DataDir:   root,
		Execution: lifecycle.ExecutionForeground,
	})
	require.NoError(t, err)
	logFile, err := lifecycle.AttemptLog(rt.Paths(), rt.Record().AttemptID)
	require.NoError(t, err)
	_, err = logFile.WriteString("line-1\nline-2\nline-3\nline-4\n")
	require.NoError(t, err)
	require.NoError(t, logFile.Close())
	require.NoError(t, rt.Finish(nil))

	t.Run("trailing N lines", func(t *testing.T) {
		o := runTestCLI(t, "daemon", "hub", "logs", "--data-dir", root, "-n", "2")
		require.Equal(t, cli.ExitOK, o.code, o.stderr)
		assert.Equal(t, "line-3\nline-4\n", o.stdout)
	})

	t.Run("all lines with -n 0", func(t *testing.T) {
		o := runTestCLI(t, "daemon", "hub", "logs", "--data-dir", root, "-n", "0")
		require.Equal(t, cli.ExitOK, o.code, o.stderr)
		assert.Equal(t, "line-1\nline-2\nline-3\nline-4\n", o.stdout)
	})

	t.Run("negative lines fails with ExitUsage", func(t *testing.T) {
		o := runTestCLI(t, "daemon", "hub", "logs", "--data-dir", root, "--lines", "-1")
		assert.Equal(t, cli.ExitUsage, o.code)
		assert.Contains(t, o.stderr, "--lines must not be negative")
	})
}

func TestDaemonLogsNotFoundWhenNoAttemptExists(t *testing.T) {
	o := runTestCLI(t, "daemon", "solo", "logs", "--data-dir", t.TempDir())
	assert.Equal(t, cli.ExitNotFound, o.code)
	assert.Contains(t, o.stderr, "no log file found for solo")
	assert.Contains(t, o.stderr, "leapmux daemon solo start")
}

func TestDaemonLogsFollowStreamsNewLinesUntilCanceled(t *testing.T) {
	root := mustCanonicalDir(t, t.TempDir())
	rt, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{
		Mode:      lifecycle.RoleHub,
		DataDir:   root,
		Execution: lifecycle.ExecutionForeground,
	})
	require.NoError(t, err)
	logPath := rt.Paths().AttemptLogPath(rt.Record().AttemptID)
	logFile, err := lifecycle.AttemptLog(rt.Paths(), rt.Record().AttemptID)
	require.NoError(t, err)
	_, err = logFile.WriteString("initial-line\n")
	require.NoError(t, err)
	require.NoError(t, logFile.Close())
	require.NoError(t, rt.Finish(nil))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stdout, stderr testutil.LogBuffer
	done := make(chan int, 1)
	go func() {
		done <- runCLI(ctx, []string{"daemon", "hub", "logs", "--data-dir", root, "-n", "1", "-f"}, strings.NewReader(""), &stdout, &stderr, testCLIRunners(t))
	}()

	require.Eventually(t, func() bool {
		return strings.Contains(stdout.String(), "initial-line\n")
	}, 5*time.Second, 20*time.Millisecond)

	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("streamed-line\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.Eventually(t, func() bool {
		return strings.Contains(stdout.String(), "streamed-line\n")
	}, 5*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case code := <-done:
		assert.Equal(t, cli.ExitOK, code, stderr.String())
	case <-time.After(5 * time.Second):
		t.Fatal("daemon logs -f did not stop after context cancellation")
	}
}

func TestUpAndDownShortcutsRouteToSoloLifecycle(t *testing.T) {
	up := runTestCLI(t, "up", "--help")
	require.Equal(t, cli.ExitOK, up.code, up.stderr)
	assert.Contains(t, up.stdout, "Usage:\n  leapmux up [flags]\n")
	assert.Contains(t, up.stdout, "--startup")
	assert.Contains(t, up.stdout, "--yes")

	down := runTestCLI(t, "down", "--help")
	require.Equal(t, cli.ExitOK, down.code, down.stderr)
	assert.Contains(t, down.stdout, "Usage:\n  leapmux down [flags]\n")
	assert.Contains(t, down.stdout, "--timeout")
	assert.NotContains(t, down.stdout, "--hub")
}

func TestLocalCommandsRejectInheritedHubFlag(t *testing.T) {
	for _, args := range [][]string{
		{"daemon", "worker", "deregister", "--hub", "https://hub.example"},
		{"recover", "db", "path", "--hub", "https://hub.example"},
		{"--hub", "https://hub.example", "recover", "db", "path"},
		{"commands", "--hub", "https://hub.example"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			o := runTestCLI(t, args...)
			assert.Equal(t, cli.ExitUsage, o.code)
			assert.Contains(t, o.stderr, "unknown flag: --hub")
		})
	}

	// daemon worker register defines its own required --hub flag and keeps it.
	reg := runTestCLI(t, "daemon", "worker", "register", "--hub", "https://hub.example")
	assert.Equal(t, cli.ExitUsage, reg.code)
	assert.Contains(t, reg.stderr, "missing required flag --registration-key")
}

func TestCompletionRegistersLmxAliasInEveryShell(t *testing.T) {
	expected := map[string]string{
		"bash":       "complete -o default -F __start_leapmux lmx",
		"zsh":        "compdef _leapmux lmx",
		"fish":       "complete -c lmx -w leapmux",
		"powershell": "Register-ArgumentCompleter -CommandName 'lmx'",
		"elvish":     "set edit:completion:arg-completer[lmx]",
		"nushell":    "export extern lmx",
	}
	for _, shell := range shells {
		t.Run(shell, func(t *testing.T) {
			o := runTestCLI(t, "completion", shell)
			require.Equal(t, cli.ExitOK, o.code, o.stderr)
			assert.Contains(t, o.stdout, expected[shell])
		})
	}
}
