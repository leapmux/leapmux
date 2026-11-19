package providerkit

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputPipeRetainsTheCallerCloseWhenTheDrainTimerFiresLater(t *testing.T) {
	file, writer, err := os.Pipe()
	require.NoError(t, err)
	pipe := newProcessOutputPipe(file)
	t.Cleanup(func() { _ = pipe.Close(); _ = writer.Close() })

	require.NoError(t, pipe.Close())
	pipe.stopDraining()
	var buffer [1]byte
	_, err = pipe.Read(buffer[:])
	require.ErrorIs(t, err, os.ErrClosed)
	var pathErr *os.PathError
	require.ErrorAs(t, err, &pathErr)
	assert.Equal(t, "read", pathErr.Op)
}

func TestOutputPipePreservesQueuedBytesAndTheWriterStateAtCutoff(t *testing.T) {
	for _, writerClosed := range []bool{false, true} {
		name := "inherited writer remains open"
		if writerClosed {
			name = "native EOF"
		}
		t.Run(name, func(t *testing.T) {
			file, writer, err := os.Pipe()
			require.NoError(t, err)
			pipe := newProcessOutputPipe(file)
			t.Cleanup(func() { _ = pipe.Close(); _ = writer.Close() })

			const payload = "first queued line\nlast queued line\n"
			_, err = writer.WriteString(payload)
			require.NoError(t, err)
			if writerClosed {
				require.NoError(t, writer.Close())
			}
			pipe.stopDraining()
			output, err := io.ReadAll(pipe)
			require.Equal(t, payload, string(output))
			if writerClosed {
				require.NoError(t, err)
			} else {
				assert.True(t, DrainLimitReached(err), "%v", err)
			}
			require.NoError(t, pipe.Close())
			requireClosedPipeEnd(t, file)
		})
	}
}
