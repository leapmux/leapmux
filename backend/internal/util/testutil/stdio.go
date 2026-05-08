package testutil

import (
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
	defer func() {
		os.Stdout = oldStdout
		_ = w.Close()
		_ = r.Close()
	}()

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}
