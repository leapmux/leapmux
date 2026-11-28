package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
)

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestReadFileOrStdin(t *testing.T) {
	t.Run("reads bytes verbatim from file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "input.txt")
		want := []byte("  line one\nline two\r\n")
		require.NoError(t, os.WriteFile(p, want, 0o600))

		got, err := readFileOrStdin(strings.NewReader("ignored"), p, "--file")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("reads empty file without error", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "empty.txt")
		require.NoError(t, os.WriteFile(p, nil, 0o600))

		got, err := readFileOrStdin(strings.NewReader("ignored"), p, "--file")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("wraps missing file as usage error with flag name", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "does-not-exist.txt")

		_, err := readFileOrStdin(strings.NewReader(""), missing, "--file")
		require.Error(t, err)
		ce := cli.FromError(err)
		assert.Equal(t, cli.CodeUsage, ce.Code)
		assert.Contains(t, ce.Message, "read --file")

		_, err = readFileOrStdin(strings.NewReader(""), missing, "--output-schema")
		require.Error(t, err)
		ce = cli.FromError(err)
		assert.Equal(t, cli.CodeUsage, ce.Code)
		assert.Contains(t, ce.Message, "read --output-schema")
	})

	t.Run("wraps empty path as usage error", func(t *testing.T) {
		_, err := readFileOrStdin(strings.NewReader(""), "", "--file")
		require.Error(t, err)
		ce := cli.FromError(err)
		assert.Equal(t, cli.CodeUsage, ce.Code)
		assert.Contains(t, ce.Message, "read --file")
	})

	t.Run("wraps directory path as usage error with flag name", func(t *testing.T) {
		_, err := readFileOrStdin(strings.NewReader(""), t.TempDir(), "--file")
		require.Error(t, err)
		ce := cli.FromError(err)
		assert.Equal(t, cli.CodeUsage, ce.Code)
		assert.Contains(t, ce.Message, "read --file")
	})

	t.Run("reads stdin when path is dash", func(t *testing.T) {
		got, err := readFileOrStdin(strings.NewReader("from stdin\n"), "-", "--file")
		require.NoError(t, err)
		assert.Equal(t, []byte("from stdin\n"), got)
	})

	t.Run("reads empty stdin without error", func(t *testing.T) {
		got, err := readFileOrStdin(strings.NewReader(""), "-", "--file")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("returns internal error when stdin read fails", func(t *testing.T) {
		_, err := readFileOrStdin(failingReader{err: errors.New("broken pipe")}, "-", "--file")
		require.Error(t, err)
		ce := cli.FromError(err)
		assert.Equal(t, cli.CodeInternal, ce.Code)
		assert.Contains(t, ce.Message, "read stdin")
	})
}

func TestIsTerminalIO_NonFileAndNil(t *testing.T) {
	var nilFile *os.File
	assert.False(t, isTerminalIO(nil))
	assert.False(t, isTerminalIO(nilFile))
	assert.False(t, isTerminalIO(strings.NewReader("hi")))
	assert.False(t, isTerminalIO(&bytes.Buffer{}))

	regular, err := os.CreateTemp(t.TempDir(), "not-a-tty-*")
	require.NoError(t, err)
	defer func() { _ = regular.Close() }()
	assert.False(t, isTerminalIO(regular))
}
