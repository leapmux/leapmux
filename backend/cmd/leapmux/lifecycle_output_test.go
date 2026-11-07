package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
)

type failingAttemptWriter struct{ err error }

func (w failingAttemptWriter) Write([]byte) (int, error) { return 0, w.err }

func TestOutputCaptureFailureCancelsAndPreservesCause(t *testing.T) {
	for _, execution := range []lifecycle.ExecutionKind{lifecycle.ExecutionForeground, lifecycle.ExecutionService} {
		t.Run(string(execution), func(t *testing.T) {
			ctx, cancel := context.WithCancel(testutil.DeadlineContext(t))
			defer cancel()
			failure := errors.New("attempt log write failed")
			stdout, stderr := os.Stdout, os.Stderr
			capture, err := startOutputCapture(failingAttemptWriter{err: failure}, cancel, execution)
			require.NoError(t, err)
			t.Cleanup(func() { _ = capture.Close() })
			_, err = fmt.Fprintln(os.Stderr, "capture failure test")
			require.NoError(t, err)
			<-ctx.Done()
			require.ErrorIs(t, ctx.Err(), context.Canceled)
			require.ErrorIs(t, capture.Close(), failure)
			require.ErrorIs(t, capture.Close(), failure)
			assert.Same(t, stdout, os.Stdout)
			assert.Same(t, stderr, os.Stderr)
		})
	}
}

func TestManagedOutputDoesNotRequireConsoleHandles(t *testing.T) {
	spec := managedTestSpec(t)
	saveManagedTestSpec(t, spec)
	runtime, err := lifecycle.StartServiceRuntime(spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.Finish(nil) })
	closed, err := os.OpenFile(filepath.Join(t.TempDir(), "closed-console"), os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, closed.Close())
	stdout, stderr := os.Stdout, os.Stderr
	t.Cleanup(func() { os.Stdout, os.Stderr = stdout, stderr })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	readyDone := make(chan struct{})
	go func() {
		defer close(readyDone)
		select {
		case <-runtime.Ready():
			_, _ = fmt.Fprintln(os.Stdout, "managed stdout marker")
			log.Print("managed logger marker")
			cancel()
		case <-ctx.Done():
		}
	}()
	os.Stdout, os.Stderr = closed, closed
	args := append([]string{"--config", spec.ConfigFile, "--data-dir", spec.DataDirectory}, spec.RuntimeArguments...)
	result := executeForegroundAttempt(ctx, cancel, "hub", runtime, args)
	<-readyDone
	os.Stdout, os.Stderr = stdout, stderr
	require.NoError(t, result)
	data, err := os.ReadFile(runtime.Paths().AttemptLogPath(runtime.Record().AttemptID))
	require.NoError(t, err)
	assert.Contains(t, string(data), "managed stdout marker")
	assert.Contains(t, string(data), "managed logger marker")
}
