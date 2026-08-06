package testutil

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// CaptureStdout redirects os.Stdout for the duration of fn and returns
// everything fn wrote to it.
func CaptureStdout(t *testing.T, fn func()) string {
	return captureFile(t, &os.Stdout, fn)
}

// CaptureStderr redirects os.Stderr for the duration of fn and returns
// everything fn wrote to it.
//
// The stream, not the slog handler, is the seam for anything that calls
// logging.Setup: that installs a fresh default logger, so a test holding a
// captured slog.Default() silently ends up asserting on an empty buffer.
func CaptureStderr(t *testing.T, fn func()) string {
	return captureFile(t, &os.Stderr, fn)
}

func captureFile(t *testing.T, target **os.File, fn func()) string {
	t.Helper()

	old := *target
	r, w, err := os.Pipe()
	require.NoError(t, err)
	*target = w

	// Drain the pipe concurrently so writes never block on a full buffer.
	// Windows pipe buffers can be as small as ~4 KiB, well below the help
	// output some callers produce.
	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&buf, r)
		done <- copyErr
	}()

	// Restored even when fn panics or calls t.Fatal -- which unwinds through
	// runtime.Goexit, so only deferred work runs. Without this the process's
	// stdout/stderr would stay pointed at a closed pipe and every later test in
	// the binary would have its output silently swallowed. Idempotent, because
	// the normal path restores before asserting so require/t.Fatal output
	// reaches the real terminal rather than the pipe.
	var out string
	var once sync.Once
	restore := func() {
		once.Do(func() {
			*target = old
			require.NoError(t, w.Close())
			require.NoError(t, <-done)
			require.NoError(t, r.Close())
			out = buf.String()
		})
	}
	t.Cleanup(restore)

	func() {
		defer restore()
		fn()
	}()
	return out
}
