package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadLine(t *testing.T) {
	t.Run("returns the line without its line ending", func(t *testing.T) {
		for input, want := range map[string]string{
			"yes\n":        "yes",
			"yes\r\n":      "yes",
			"\n":           "",
			"two\nlines\n": "two",
		} {
			line, err := ReadLine(t.Context(), strings.NewReader(input))
			require.NoError(t, err, "input %q", input)
			assert.Equal(t, want, line, "input %q", input)
		}
	})

	t.Run("returns an incomplete line with the error of the read", func(t *testing.T) {
		line, err := ReadLine(t.Context(), strings.NewReader("ye"))
		require.ErrorIs(t, err, io.EOF)
		assert.Equal(t, "ye", line)

		line, err = ReadLine(t.Context(), strings.NewReader(""))
		require.ErrorIs(t, err, io.EOF)
		assert.Empty(t, line)
	})

	t.Run("returns the cause of a context that ended before the call", func(t *testing.T) {
		cause := errors.New("ended first")
		ctx, cancel := context.WithCancelCause(t.Context())
		cancel(cause)
		reader, writer := io.Pipe()
		t.Cleanup(func() { _ = writer.Close() })
		_, err := ReadLine(ctx, reader)
		require.ErrorIs(t, err, cause)
	})

	// A read that waits for a person does not stop, so the context ends the
	// prompt instead, with its cause.
	t.Run("returns the cause of a context that ends during the read", func(t *testing.T) {
		cause := errors.New("interrupted")
		ctx, cancel := context.WithCancelCause(t.Context())
		reader, writer := io.Pipe()
		t.Cleanup(func() { _ = writer.Close() })
		done := make(chan error, 1)
		go func() {
			_, err := ReadLine(ctx, reader)
			done <- err
		}()
		cancel(cause)
		require.ErrorIs(t, <-done, cause)
	})
}
