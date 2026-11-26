package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
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
	}, 30*time.Second, 20*time.Millisecond)

	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("streamed-line\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.Eventually(t, func() bool {
		return strings.Contains(stdout.String(), "streamed-line\n")
	}, 30*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case code := <-done:
		assert.Equal(t, cli.ExitOK, code, stderr.String())
	case <-time.After(30 * time.Second):
		t.Fatal("daemon logs -f did not stop after context cancellation")
	}
}

func TestDaemonLogsNDJSONConsistentAcrossFollowModes(t *testing.T) {
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
	_, err = logFile.WriteString("alpha\nbeta\n")
	require.NoError(t, err)
	require.NoError(t, logFile.Close())
	require.NoError(t, rt.Finish(nil))

	t.Run("non-follow ndjson emits path and line objects", func(t *testing.T) {
		o := runTestCLI(t, "daemon", "hub", "logs", "--data-dir", root, "-n", "2", "--format", "ndjson")
		require.Equal(t, cli.ExitOK, o.code, o.stderr)
		lines := strings.Split(strings.TrimSpace(o.stdout), "\n")
		require.Len(t, lines, 2)
		var first, second logLineEvent
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
		require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
		assert.Equal(t, logLineEvent{Path: logPath, Line: "alpha"}, first)
		assert.Equal(t, logLineEvent{Path: logPath, Line: "beta"}, second)
	})

	t.Run("follow ndjson buffers partial lines until newline", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var stdout, stderr testutil.LogBuffer
		done := make(chan int, 1)
		go func() {
			done <- runCLI(ctx, []string{"daemon", "hub", "logs", "--data-dir", root, "-n", "1", "-f", "--format", "ndjson"}, strings.NewReader(""), &stdout, &stderr, testCLIRunners(t))
		}()

		require.Eventually(t, func() bool {
			return strings.Contains(stdout.String(), `"line":"beta"`)
		}, 30*time.Second, 20*time.Millisecond)

		// Write a partial line without a trailing newline, then complete it.
		// Deterministic cross-tick partial buffering is covered by
		// TestLogFollowerDeterministicWithQuartzClock.
		f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString("part-one-")
		require.NoError(t, err)
		require.NoError(t, f.Close())

		f, err = os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString("part-two\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())

		require.Eventually(t, func() bool {
			return strings.Contains(stdout.String(), `"line":"part-one-part-two"`)
		}, 30*time.Second, 20*time.Millisecond)

		cancel()
		select {
		case code := <-done:
			assert.Equal(t, cli.ExitOK, code, stderr.String())
		case <-time.After(30 * time.Second):
			t.Fatal("daemon logs -f --format ndjson did not stop after cancellation")
		}

		// Verify every emitted NDJSON line has the same {path, line} schema and
		// never emitted an incomplete "part-one-" record.
		for _, raw := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
			var ev logLineEvent
			require.NoError(t, json.Unmarshal([]byte(raw), &ev), raw)
			assert.Equal(t, logPath, ev.Path)
			assert.NotEqual(t, "part-one-", ev.Line)
		}
	})
}

func TestReadLastLinesHandlesLargeCountMultiChunkAndAll(t *testing.T) {
	t.Parallel()

	t.Run("huge n does not panic or over-allocate", func(t *testing.T) {
		t.Parallel()
		r := strings.NewReader("first\nsecond\n")
		lines, err := readLastLines(r, 9999999999)
		require.NoError(t, err)
		assert.Equal(t, []string{"first", "second"}, lines)
	})

	t.Run("multi-chunk file reads trailing lines accurately", func(t *testing.T) {
		t.Parallel()
		var b strings.Builder
		const totalLines = 3000
		for i := 1; i <= totalLines; i++ {
			fmt.Fprintf(&b, "log-entry-%05d-%s\n", i, strings.Repeat("x", 40))
		}
		require.Greater(t, b.Len(), logTailChunkSize*2, "fixture must span multiple chunks")
		lines, err := readLastLines(strings.NewReader(b.String()), 3)
		require.NoError(t, err)
		require.Len(t, lines, 3)
		assert.True(t, strings.HasPrefix(lines[0], "log-entry-02998-"))
		assert.True(t, strings.HasPrefix(lines[1], "log-entry-02999-"))
		assert.True(t, strings.HasPrefix(lines[2], "log-entry-03000-"))
	})

	t.Run("n == 0 returns all lines", func(t *testing.T) {
		t.Parallel()
		lines, err := readLastLines(strings.NewReader("a\nb\nc"), 0)
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b", "c"}, lines)
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		t.Parallel()
		lines, err := readLastLines(strings.NewReader(""), 10)
		require.NoError(t, err)
		assert.Equal(t, []string{}, lines)
	})

	t.Run("no trailing newline with positive n", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name  string
			input string
			n     int
			want  []string
		}{
			{name: "last 1 of 3", input: "one\ntwo\nthree", n: 1, want: []string{"three"}},
			{name: "last 2 of 3", input: "one\ntwo\nthree", n: 2, want: []string{"two", "three"}},
			{name: "all 3 of 3", input: "one\ntwo\nthree", n: 3, want: []string{"one", "two", "three"}},
			{name: "n exceeds line count", input: "one\ntwo\nthree", n: 10, want: []string{"one", "two", "three"}},
			{name: "single line without newline", input: "only", n: 1, want: []string{"only"}},
			{name: "crlf endings", input: "alpha\r\nbeta\r\ngamma\r\n", n: 2, want: []string{"beta", "gamma"}},
			{name: "trailing blank line preserved", input: "alpha\n\n", n: 1, want: []string{""}},
			{name: "trailing blank line with preceding line", input: "alpha\n\n", n: 2, want: []string{"alpha", ""}},
			{name: "single newline only", input: "\n", n: 1, want: []string{""}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				lines, err := readLastLines(strings.NewReader(tc.input), tc.n)
				require.NoError(t, err)
				assert.Equal(t, tc.want, lines)
			})
		}
	})

	t.Run("exact chunk boundaries and line longer than chunk size", func(t *testing.T) {
		t.Parallel()

		// Exact chunk size with the nth newline landing at offset 0 of the final chunk.
		prefix := strings.Repeat("a", logTailChunkSize-5) + "\n"
		tail := "last\n"
		// Total length = logTailChunkSize + 1 bytes so second read spans the boundary.
		exactPlusOne := prefix + tail
		require.Equal(t, logTailChunkSize+1, len(exactPlusOne))
		lines, err := readLastLines(strings.NewReader(exactPlusOne), 1)
		require.NoError(t, err)
		assert.Equal(t, []string{"last"}, lines)

		// Exact chunk size total length with newline at the chunk boundary.
		exact := strings.Repeat("b", logTailChunkSize-5) + "\nend\n"
		require.Equal(t, logTailChunkSize, len(exact))
		lines, err = readLastLines(strings.NewReader(exact), 1)
		require.NoError(t, err)
		assert.Equal(t, []string{"end"}, lines)

		// Newline right at byte logTailChunkSize-1 of a two-chunk file.
		atBoundary := strings.Repeat("c", logTailChunkSize-1) + "\n" + strings.Repeat("d", logTailChunkSize) + "\n"
		lines, err = readLastLines(strings.NewReader(atBoundary), 1)
		require.NoError(t, err)
		assert.Equal(t, []string{strings.Repeat("d", logTailChunkSize)}, lines)

		// Single line exceeding logTailChunkSize verifies no fixed scanner limit.
		longLine := strings.Repeat("z", logTailChunkSize+4096)
		lines, err = readLastLines(strings.NewReader("short\n"+longLine+"\n"), 1)
		require.NoError(t, err)
		assert.Equal(t, []string{longLine}, lines)
	})
}

func TestDaemonLogsJSONMissingFileAndQuietRejection(t *testing.T) {
	t.Run("non-follow json returns typed envelope and empty array for empty log", func(t *testing.T) {
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
		require.NoError(t, logFile.Close())
		require.NoError(t, rt.Finish(nil))

		// Empty log file must encode "lines": [] rather than null.
		o := runTestCLI(t, "daemon", "hub", "logs", "--data-dir", root, "--json")
		require.Equal(t, cli.ExitOK, o.code, o.stderr)
		assert.Contains(t, o.stdout, `"lines":[]`)

		// Populate lines and check trailing selection in JSON envelope.
		require.NoError(t, os.WriteFile(logPath, []byte("l1\nl2\nl3\n"), 0o600))
		o = runTestCLI(t, "daemon", "hub", "logs", "--data-dir", root, "-n", "2", "--json")
		require.Equal(t, cli.ExitOK, o.code, o.stderr)
		var env struct {
			Data struct {
				Mode  string   `json:"mode"`
				Path  string   `json:"path"`
				Lines []string `json:"lines"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(o.stdout), &env), o.stdout)
		assert.Equal(t, "hub", env.Data.Mode)
		assert.Equal(t, logPath, env.Data.Path)
		assert.Equal(t, []string{"l2", "l3"}, env.Data.Lines)
	})

	t.Run("follow json emits per-line data envelopes", func(t *testing.T) {
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
		_, err = logFile.WriteString("boot-line\n")
		require.NoError(t, err)
		require.NoError(t, logFile.Close())
		require.NoError(t, rt.Finish(nil))

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var stdout, stderr testutil.LogBuffer
		done := make(chan int, 1)
		go func() {
			done <- runCLI(ctx, []string{"daemon", "hub", "logs", "--data-dir", root, "-n", "1", "-f", "--json"}, strings.NewReader(""), &stdout, &stderr, testCLIRunners(t))
		}()

		require.Eventually(t, func() bool {
			return strings.Contains(stdout.String(), `"boot-line"`)
		}, 30*time.Second, 20*time.Millisecond)

		cancel()
		select {
		case code := <-done:
			assert.Equal(t, cli.ExitOK, code, stderr.String())
		case <-time.After(30 * time.Second):
			t.Fatal("daemon logs -f --json did not stop after cancellation")
		}

		var env struct {
			Data logLineEvent `json:"data"`
		}
		require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &env), stdout.String())
		assert.Equal(t, logLineEvent{Path: logPath, Line: "boot-line"}, env.Data)
	})

	t.Run("deleted attempt log file fails with ExitNotFound", func(t *testing.T) {
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
		require.NoError(t, logFile.Close())
		require.NoError(t, rt.Finish(nil))
		require.NoError(t, os.Remove(logPath))

		o := runTestCLI(t, "daemon", "hub", "logs", "--data-dir", root)
		assert.Equal(t, cli.ExitNotFound, o.code)
		assert.Contains(t, o.stderr, "does not exist")
	})

	t.Run("quiet flag is rejected", func(t *testing.T) {
		o := runTestCLI(t, "daemon", "hub", "logs", "--data-dir", t.TempDir(), "-q")
		assert.Equal(t, cli.ExitUsage, o.code)
		assert.Contains(t, o.stderr, "--quiet")
	})
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
		{"up", "--hub", "https://hub.example"},
		{"down", "--hub", "https://hub.example"},
		{"recover", "db", "path", "--hub", "https://hub.example"},
		{"--hub", "https://hub.example", "recover", "db", "path"},
		{"commands", "--hub", "https://hub.example"},
		{"completion", "bash", "--hub", "https://hub.example"},
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

func TestDaemonLogsFormatTSVAndVerbatimTextTabs(t *testing.T) {
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
	_, err = logFile.WriteString("col1\tcol2\nsecond-line\n")
	require.NoError(t, err)
	require.NoError(t, logFile.Close())
	require.NoError(t, rt.Finish(nil))

	t.Run("text format preserves raw tabs verbatim", func(t *testing.T) {
		o := runTestCLI(t, "daemon", "hub", "logs", "--data-dir", root, "-n", "2")
		require.Equal(t, cli.ExitOK, o.code, o.stderr)
		assert.Equal(t, "col1\tcol2\nsecond-line\n", o.stdout)
	})

	t.Run("tsv format emits path and sanitized line columns", func(t *testing.T) {
		o := runTestCLI(t, "daemon", "hub", "logs", "--data-dir", root, "-n", "2", "--format", "tsv")
		require.Equal(t, cli.ExitOK, o.code, o.stderr)
		lines := strings.Split(strings.TrimSuffix(o.stdout, "\n"), "\n")
		require.Len(t, lines, 2)
		assert.Equal(t, logPath+"\tcol1 col2", lines[0])
		assert.Equal(t, logPath+"\tsecond-line", lines[1])
	})
}

func TestLogFollowerDeterministicWithQuartzClock(t *testing.T) {
	type emittedLine struct {
		path string
		text string
	}

	startFollower := func(t *testing.T, logPath string, paths lifecycle.Paths) (
		clock *quartz.Mock,
		events <-chan emittedLine,
		step func(),
		cancel context.CancelFunc,
		done <-chan error,
	) {
		t.Helper()
		f, err := os.Open(logPath)
		require.NoError(t, err)
		_, err = f.Seek(0, 2)
		require.NoError(t, err)

		mClock := testutil.NewQuartzMock(t)
		tickerTrap := mClock.Trap().NewTicker(logsFollowTimerTag)
		t.Cleanup(tickerTrap.Close)

		evCh := make(chan emittedLine, 16)
		drainedCh := make(chan struct{}, 16)
		follower := newLogFollower(f, logPath, paths, mClock, func(path, text string) error {
			evCh <- emittedLine{path: path, text: text}
			return nil
		})
		follower.onAfterDrain = func() {
			drainedCh <- struct{}{}
		}
		ctx, cancelFn := context.WithCancel(testutil.DeadlineContext(t))
		doneCh := make(chan error, 1)
		go func() {
			defer func() { _ = follower.close() }()
			doneCh <- follower.follow(ctx)
		}()
		assert.Equal(t, logsPollInterval, testutil.WaitForTimer(t, ctx, tickerTrap))
		select {
		case <-drainedCh:
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for initial drain")
		}
		stepFn := func() {
			t.Helper()
			mClock.Advance(logsPollInterval).MustWait(testutil.DeadlineContext(t))
			select {
			case <-drainedCh:
			case <-time.After(30 * time.Second):
				t.Fatal("timed out waiting for follower poll and drain")
			}
		}
		return mClock, evCh, stepFn, cancelFn, doneCh
	}

	waitEvent := func(t *testing.T, ch <-chan emittedLine) emittedLine {
		t.Helper()
		select {
		case ev := <-ch:
			return ev
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for emitted log line")
			return emittedLine{}
		}
	}

	appendText := func(t *testing.T, path, text string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString(text)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	t.Run("buffers partial line across poll ticks until newline", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "attempt.log")
		require.NoError(t, os.WriteFile(logPath, []byte("existing\n"), 0o600))

		_, events, step, cancel, done := startFollower(t, logPath, lifecycle.Paths{})
		defer cancel()

		appendText(t, logPath, "first-half-")
		step()

		select {
		case ev := <-events:
			t.Fatalf("partial line must not emit before newline, got %+v", ev)
		default:
		}

		appendText(t, logPath, "second-half\n")
		step()

		ev := waitEvent(t, events)
		assert.Equal(t, logPath, ev.path)
		assert.Equal(t, "first-half-second-half\n", ev.text)

		cancel()
		require.NoError(t, <-done)
	})

	t.Run("flushes partial line and switches file on rotation", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "attempt.log")
		require.NoError(t, os.WriteFile(logPath, []byte("old\n"), 0o600))

		_, events, step, cancel, done := startFollower(t, logPath, lifecycle.Paths{})
		defer cancel()

		appendText(t, logPath, "tail-before-rotate")
		step()

		require.NoError(t, os.Rename(logPath, logPath+".1"))
		require.NoError(t, os.WriteFile(logPath, []byte("after-rotate\n"), 0o600))
		step()

		ev1 := waitEvent(t, events)
		assert.Equal(t, "tail-before-rotate", ev1.text)
		ev2 := waitEvent(t, events)
		assert.Equal(t, "after-rotate\n", ev2.text)

		cancel()
		require.NoError(t, <-done)
	})

	t.Run("flushes partial line and rewinds on truncation", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "attempt.log")
		require.NoError(t, os.WriteFile(logPath, []byte(strings.Repeat("padding-line\n", 10)), 0o600))

		_, events, step, cancel, done := startFollower(t, logPath, lifecycle.Paths{})
		defer cancel()

		appendText(t, logPath, "partial-before-trunc")
		step()

		require.NoError(t, os.Truncate(logPath, 0))
		appendText(t, logPath, "fresh-after-trunc\n")
		step()

		ev1 := waitEvent(t, events)
		assert.Equal(t, "partial-before-trunc", ev1.text)
		ev2 := waitEvent(t, events)
		assert.Equal(t, "fresh-after-trunc\n", ev2.text)

		cancel()
		require.NoError(t, <-done)
	})

	t.Run("rolls over to new attempt log when runtime.yaml updates", func(t *testing.T) {
		root := mustCanonicalDir(t, t.TempDir())
		rt1, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{
			Mode:      lifecycle.RoleHub,
			DataDir:   root,
			Execution: lifecycle.ExecutionForeground,
		})
		require.NoError(t, err)
		log1Path := rt1.Paths().AttemptLogPath(rt1.Record().AttemptID)
		log1, err := lifecycle.AttemptLog(rt1.Paths(), rt1.Record().AttemptID)
		require.NoError(t, err)
		_, err = log1.WriteString("attempt-1-init\n")
		require.NoError(t, err)
		require.NoError(t, log1.Close())
		require.NoError(t, rt1.Finish(nil))

		_, events, step, cancel, done := startFollower(t, log1Path, rt1.Paths())
		defer cancel()

		appendText(t, log1Path, "attempt-1-partial")
		step()

		rt2, err := lifecycle.StartRuntime(lifecycle.RuntimeConfig{
			Mode:      lifecycle.RoleHub,
			DataDir:   root,
			Execution: lifecycle.ExecutionForeground,
		})
		require.NoError(t, err)
		log2Path := rt2.Paths().AttemptLogPath(rt2.Record().AttemptID)
		log2, err := lifecycle.AttemptLog(rt2.Paths(), rt2.Record().AttemptID)
		require.NoError(t, err)
		_, err = log2.WriteString("attempt-2-line\n")
		require.NoError(t, err)
		require.NoError(t, log2.Close())
		require.NoError(t, rt2.Finish(nil))

		step()

		ev1 := waitEvent(t, events)
		assert.Equal(t, log1Path, ev1.path)
		assert.Equal(t, "attempt-1-partial", ev1.text)
		ev2 := waitEvent(t, events)
		assert.Equal(t, log2Path, ev2.path)
		assert.Equal(t, "attempt-2-line\n", ev2.text)

		cancel()
		require.NoError(t, <-done)
	})

	t.Run("propagates emit error during drain and survives missing file during poll", func(t *testing.T) {
		dir := t.TempDir()
		logPath := filepath.Join(dir, "attempt.log")
		require.NoError(t, os.WriteFile(logPath, []byte("initial\n"), 0o600))

		// Verify emit error stops follow immediately.
		f, err := os.Open(logPath)
		require.NoError(t, err)
		_, err = f.Seek(0, 2)
		require.NoError(t, err)
		emitErr := errors.New("broken pipe")
		mClock := testutil.NewQuartzMock(t)
		tickerTrap := mClock.Trap().NewTicker(logsFollowTimerTag)
		t.Cleanup(tickerTrap.Close)
		follower := newLogFollower(f, logPath, lifecycle.Paths{}, mClock, func(string, string) error {
			return emitErr
		})
		appendText(t, logPath, "trigger-error\n")
		ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
		defer cancel()
		errCh := make(chan error, 1)
		go func() {
			defer func() { _ = follower.close() }()
			errCh <- follower.follow(ctx)
		}()
		testutil.WaitForTimer(t, ctx, tickerTrap)
		select {
		case gotErr := <-errCh:
			require.ErrorIs(t, gotErr, emitErr)
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for emit error propagation")
		}

		// Verify temporarily missing file during poll does not fail and recovers once recreated.
		require.NoError(t, os.WriteFile(logPath, []byte("before\n"), 0o600))
		_, events, step, cancel2, done2 := startFollower(t, logPath, lifecycle.Paths{})
		defer cancel2()

		require.NoError(t, os.Remove(logPath))
		step()
		require.NoError(t, os.WriteFile(logPath, []byte("recreated\n"), 0o600))
		step()

		ev := waitEvent(t, events)
		assert.Equal(t, "recreated\n", ev.text)
		cancel2()
		require.NoError(t, <-done2)
	})
}
