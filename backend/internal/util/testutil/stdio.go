package testutil

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// CaptureStdout redirects os.Stdout for the duration of fn and returns
// everything fn wrote to it.
func CaptureStdout(t *testing.T, fn func()) string {
	t.Helper()

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	// Drain the pipe concurrently so writes never block on a full buffer.
	// Windows pipe buffers can be as small as ~4 KiB, well below the help
	// output some callers produce.
	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(&buf, r)
		done <- copyErr
	}()

	fn()

	os.Stdout = oldStdout
	require.NoError(t, w.Close())
	require.NoError(t, <-done)
	require.NoError(t, r.Close())
	return buf.String()
}
