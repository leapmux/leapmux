package providerkit

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"bytes"
	"errors"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFailedProcessStartCompletesItsLifecycle(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label    string
		prepared bool
	}{
		{label: "absent owner"},
		{label: "absent executable", prepared: true},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			process := NewProcessFrom(ProcessConfig{Ctx: ctx, Cancel: cancel})
			if tc.prepared {
				cmd := exec.CommandContext(ctx, filepath.Join(t.TempDir(), "absent-native-command"))
				pipes, err := SetupProcessPipes(cmd, cancel)
				require.NoError(t, err)
				process = NewProcess(agent.Options{AgentID: "failed-start"}, ProcessLaunch{ProviderName: "probe"}, pipes, ctx, cancel)
			}
			startErr := process.StartCmd()
			require.Error(t, startErr)
			if tc.prepared {
				require.ErrorIs(t, startErr, fs.ErrNotExist)
			}
			select {
			case <-process.ProcessDone():
			default:
				t.Fatal("the failed start leaves the completion signal open, so Stop and Wait cannot finish")
			}
			assert.Error(t, ctx.Err(), "a failed start must cancel the process context")
			assert.Equal(t, agent.MessageCompletionError, process.ProcessExitCompletion())
			waitErr := process.Wait()
			require.Error(t, waitErr)
			if tc.prepared {
				assert.ErrorIs(t, waitErr, fs.ErrNotExist)
			}
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				var calls sync.WaitGroup
				for range 8 {
					calls.Add(1)
					go func() {
						defer calls.Done()
						process.Stop()
						assert.Error(t, process.Wait())
					}()
				}
				calls.Wait()
			}()
			select {
			case <-finished:
			case <-testutil.DeadlineContext(t).Done():
				t.Fatal("the failed start prevents concurrent Stop and Wait calls from completing")
			}
			assert.Equal(t, agent.MessageCompletionError, process.ProcessExitCompletion(), "cleanup must preserve the native startup failure")
			require.Error(t, process.StartCmd(), "a completed attempt must refuse another start")
		})
	}
}

func TestConcurrentProcessStartKeepsTheOneSuccessfulChildAlive(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestHelperProcessStartState$")
	cmd.Env = append(os.Environ(), "LEAPMUX_TEST_PROCESS_START_STATE=1")
	pipes, err := SetupProcessPipes(cmd, cancel)
	require.NoError(t, err)
	process := NewProcess(agent.Options{AgentID: "concurrent-start"}, ProcessLaunch{ProviderName: "probe"}, pipes, ctx, cancel)
	results := make(chan error, 8)
	start := make(chan struct{})
	for range 8 {
		go func() {
			<-start
			results <- process.StartCmd()
		}()
	}
	close(start)
	var successes int
	for range 8 {
		select {
		case err := <-results:
			if err == nil {
				successes++
			}
		case <-testutil.DeadlineContext(t).Done():
			t.Fatal("the concurrent start attempt did not return")
		}
	}
	require.Equal(t, 1, successes)
	process.DrainStderr(pipes.Stderr())
	ready := make(chan string, 1)
	go process.ReadLines(bufio.NewScanner(pipes.Stdout()), func(line []byte) { ready <- string(line) })
	t.Cleanup(func() { process.Stop(); _ = process.Wait() })
	require.NoError(t, ctx.Err(), "a refused second attempt must preserve the successful child's context")
	select {
	case line := <-ready:
		assert.Equal(t, "native-child-ready", line)
	case <-process.ProcessDone():
		t.Fatal("a refused second attempt closed the successful child")
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the successful native child did not report readiness")
	}
	process.Stop()
	require.NoError(t, process.Wait())
}

func TestHelperProcessStartState(t *testing.T) {
	if os.Getenv("LEAPMUX_TEST_PROCESS_START_STATE") != "1" {
		return
	}
	fmt.Println("native-child-ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestZeroProcessCompletesARefusedStart(t *testing.T) {
	t.Parallel()
	var process Process
	require.Error(t, process.StartCmd())
	select {
	case <-process.ProcessDone():
	default:
		t.Fatal("the refused zero-value start must complete")
	}
	process.Stop()
	require.Error(t, process.Wait())
}

func TestProcessFormatStartupErrorPreservesItsCausesAndExactDiagnostics(t *testing.T) {
	t.Parallel()
	first := errors.New("native setup refused")
	second := &fs.PathError{Op: "close", Path: "owned-host", Err: fs.ErrPermission}
	for _, tc := range []struct {
		label    string
		cause    error
		stderr   string
		preamble []string
		want     string
	}{
		{label: "single cause", cause: first, want: "initialize: native setup refused"},
		{label: "joined causes", cause: errors.Join(first, second), want: "initialize: native setup refused\nclose owned-host: permission denied"},
		{label: "stderr and shell preamble", cause: first, stderr: " \n native stderr\n ", preamble: []string{" ", "shell output", " "}, want: "initialize: native setup refused; stderr: native stderr; shell preamble: shell output"},
		{label: "blank diagnostics", cause: first, stderr: " \t\n", preamble: []string{" ", "\t"}, want: "initialize: native setup refused"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			drained := make(chan struct{})
			close(drained)
			process := &Process{stderrDone: drained, stderrBuf: *bytes.NewBufferString(tc.stderr), preambleOutput: tc.preamble}
			result := process.FormatStartupError("initialize", tc.cause)
			require.EqualError(t, result, tc.want)
			assert.ErrorIs(t, result, first)
			if tc.label == "joined causes" {
				assert.ErrorIs(t, result, fs.ErrPermission)
				var pathError *fs.PathError
				require.ErrorAs(t, result, &pathError)
				assert.Same(t, second, pathError)
			}
		})
	}
}

func TestProcessFormatStartupErrorReportsReadableNilDetails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label    string
		phase    string
		stderr   string
		preamble []string
		want     string
	}{
		{label: "empty diagnostics", phase: "initialize", want: "initialize: no error details"},
		{label: "empty phase", want: "no error details"},
		{label: "all diagnostics", phase: "initialize", stderr: "native stderr", preamble: []string{"shell output"}, want: "initialize: no error details; stderr: native stderr; shell preamble: shell output"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			drained := make(chan struct{})
			close(drained)
			process := &Process{stderrDone: drained, stderrBuf: *bytes.NewBufferString(tc.stderr), preambleOutput: tc.preamble}
			result := process.FormatStartupError(tc.phase, nil)
			require.EqualError(t, result, tc.want)
			assert.Nil(t, errors.Unwrap(result))
		})
	}
}

type customStartupDiagnosticCause struct{}

func (*customStartupDiagnosticCause) Error() string { return "the native fallback diagnostic" }

func (*customStartupDiagnosticCause) Format(state fmt.State, verb rune) {
	if verb == 's' {
		_, _ = io.WriteString(state, "the native string diagnostic")
		return
	}
	_, _ = io.WriteString(state, "the native value diagnostic")
}

func TestProcessFormatStartupErrorPreservesTheOriginalStringFormatter(t *testing.T) {
	t.Parallel()
	drained := make(chan struct{})
	close(drained)
	cause := &customStartupDiagnosticCause{}
	process := &Process{stderrDone: drained}
	result := process.FormatStartupError("initialize", cause)
	require.EqualError(t, result, "initialize: the native string diagnostic")
	assert.ErrorIs(t, result, cause)
	var typedCause *customStartupDiagnosticCause
	require.ErrorAs(t, result, &typedCause)
	assert.Same(t, cause, typedCause)
}
