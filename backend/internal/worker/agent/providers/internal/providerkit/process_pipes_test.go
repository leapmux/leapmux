package providerkit

import (
	"bytes"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Stat can return a native invalid-handle error for a closed file on Windows.
// A zero-byte write checks Go's closed-file state without emitting data.
func requireClosedPipeEnd(t *testing.T, file *os.File) {
	t.Helper()
	_, err := file.Write(nil)
	require.ErrorIs(t, err, os.ErrClosed, "the parent copy of child pipe %q must be closed", file.Name())
}

func TestClosedPipeEndProbeDistinguishesOpenAndClosedFiles(t *testing.T) {
	t.Parallel()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	files := []*os.File{reader, writer}
	for _, file := range files {
		_, err := file.Write(nil)
		assert.NotErrorIs(t, err, os.ErrClosed, "an open pipe must not satisfy the closed-file assertion")
	}
	for _, file := range files {
		require.NoError(t, file.Close())
		requireClosedPipeEnd(t, file)
	}
}

func TestSetupProcessPipesClosesChildEndsWhenALaterPipeFails(t *testing.T) {
	t.Parallel()
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("unused-command")
			if stream == "stdout" {
				cmd.Stdout = &bytes.Buffer{}
			} else {
				cmd.Stderr = &bytes.Buffer{}
			}
			cancelled := 0
			pipes, err := SetupProcessPipes(cmd, func() { cancelled++ })
			require.Error(t, err)
			assert.Nil(t, pipes)
			assert.Equal(t, 1, cancelled)
			requireClosedPipeEnd(t, cmd.Stdin.(*os.File))
			if stream == "stderr" {
				requireClosedPipeEnd(t, cmd.Stdout.(*os.File))
			}
		})
	}
}
