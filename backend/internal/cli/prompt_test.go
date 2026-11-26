package cli

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingReader struct {
	entered chan struct{}
	release chan struct{}
}

func (r *blockingReader) Read([]byte) (int, error) {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-r.release
	return 0, io.EOF
}

type failingReader struct {
	data string
	off  int
	err  error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	return 0, r.err
}

type failingByteReader struct {
	data string
	off  int
	err  error
}

func (r *failingByteReader) Read(p []byte) (int, error) {
	if r.off < len(r.data) {
		n := copy(p, r.data[r.off:])
		r.off += n
		return n, nil
	}
	return 0, r.err
}

func (r *failingByteReader) ReadByte() (byte, error) {
	if r.off < len(r.data) {
		b := r.data[r.off]
		r.off++
		return b, nil
	}
	return 0, r.err
}

type readChunk struct {
	data []byte
	err  error
}

type chunkedReader struct {
	chunks []readChunk
	idx    int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.chunks) {
		return 0, io.EOF
	}
	c := r.chunks[r.idx]
	r.idx++
	n := copy(p, c.data)
	return n, c.err
}

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

	t.Run("preserves remaining bytes across sequential calls", func(t *testing.T) {
		readers := map[string]func() io.Reader{
			"plain io.Reader": func() io.Reader { return struct{ io.Reader }{strings.NewReader("1\ny\n")} },
			"io.ByteReader":   func() io.Reader { return strings.NewReader("1\ny\n") },
			"bufio.Reader":    func() io.Reader { return bufio.NewReader(strings.NewReader("1\ny\n")) },
			"io.Pipe": func() io.Reader {
				pr, pw := io.Pipe()
				go func() {
					_, _ = pw.Write([]byte("1\ny\n"))
					_ = pw.Close()
				}()
				return pr
			},
		}
		for name, makeReader := range readers {
			t.Run(name, func(t *testing.T) {
				in := makeReader()
				first, err := ReadLine(t.Context(), in)
				require.NoError(t, err)
				assert.Equal(t, "1", first)

				second, err := ReadLine(t.Context(), in)
				require.NoError(t, err)
				assert.Equal(t, "y", second)
			})
		}
	})

	t.Run("returns an incomplete line with the error of the read across all reader kinds", func(t *testing.T) {
		makeReaders := func(input string) map[string]io.Reader {
			return map[string]io.Reader{
				"plain io.Reader": struct{ io.Reader }{strings.NewReader(input)},
				"io.ByteReader":   strings.NewReader(input),
				"bufio.Reader":    bufio.NewReader(strings.NewReader(input)),
			}
		}
		for name, r := range makeReaders("ye") {
			t.Run(name+" partial at EOF", func(t *testing.T) {
				line, err := ReadLine(t.Context(), r)
				require.ErrorIs(t, err, io.EOF)
				assert.Equal(t, "ye", line)
			})
		}
		for name, r := range makeReaders("ye\r") {
			t.Run(name+" strips trailing CR at EOF", func(t *testing.T) {
				line, err := ReadLine(t.Context(), r)
				require.ErrorIs(t, err, io.EOF)
				assert.Equal(t, "ye", line)
			})
		}
		for name, r := range makeReaders("") {
			t.Run(name+" empty EOF", func(t *testing.T) {
				line, err := ReadLine(t.Context(), r)
				require.ErrorIs(t, err, io.EOF)
				assert.Empty(t, line)
			})
		}

		readErr := errors.New("read failed")
		errReaders := map[string]io.Reader{
			"plain io.Reader": &failingReader{data: "ab", err: readErr},
			"io.ByteReader":   &failingByteReader{data: "ab", err: readErr},
			"bufio.Reader":    bufio.NewReader(&failingReader{data: "ab", err: readErr}),
		}
		for name, r := range errReaders {
			t.Run(name+" read error returns partial line and error", func(t *testing.T) {
				line, err := ReadLine(t.Context(), r)
				require.ErrorIs(t, err, readErr)
				assert.Equal(t, "ab", line)
			})
		}

		t.Run("plain io.Reader returning newline with EOF succeeds", func(t *testing.T) {
			r := &chunkedReader{chunks: []readChunk{
				{data: nil, err: nil},
				{data: []byte("o"), err: nil},
				{data: []byte("k"), err: nil},
				{data: []byte("\n"), err: io.EOF},
			}}
			line, err := ReadLine(t.Context(), r)
			require.NoError(t, err)
			assert.Equal(t, "ok", line)
		})
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
		reader := &blockingReader{
			entered: make(chan struct{}, 1),
			release: make(chan struct{}),
		}
		t.Cleanup(func() { close(reader.release) })
		done := make(chan error, 1)
		go func() {
			_, err := ReadLine(ctx, reader)
			done <- err
		}()
		select {
		case <-reader.entered:
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for ReadLine to enter Read")
		}
		cancel(cause)
		select {
		case err := <-done:
			require.ErrorIs(t, err, cause)
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for ReadLine to return after cancel")
		}
	})
}
