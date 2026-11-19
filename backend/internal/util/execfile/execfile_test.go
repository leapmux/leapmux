package execfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeString(text string) func(io.Writer) error {
	return func(w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	}
}

func TestWrite_CreatesTheProgramWithItsContentAndMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "program")
	require.NoError(t, Write(path, 0o700, true, writeString("#!/bin/sh\necho hi\n")))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\necho hi\n", string(data))
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	}
}

// Write holds the read side of syscall.ForkLock while the file is open for
// writing, so no fork of this process can inherit the descriptor. The write
// side that each fork takes therefore waits until Write returns.
//
// Not parallel: a parallel Write of another test would hold the read side as
// well, and the test could then pass without the lock of this Write.
func TestWrite_KeepsEveryForkOutWhileTheFileIsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "program")
	forkWaited := false
	require.NoError(t, Write(path, 0o700, true, func(w io.Writer) error {
		if syscall.ForkLock.TryLock() {
			syscall.ForkLock.Unlock()
		} else {
			forkWaited = true
		}
		_, err := io.WriteString(w, "#!/bin/sh\n")
		return err
	}))
	assert.True(t, forkWaited, "a fork could start while the file was open for writing")
	require.True(t, syscall.ForkLock.TryLock(), "Write releases the lock when it returns")
	syscall.ForkLock.Unlock()
}

func TestWrite_ExclusiveRefusesAnExistingFileAndKeepsIt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "program")
	require.NoError(t, os.WriteFile(path, []byte("original"), 0o600))

	err := Write(path, 0o700, true, writeString("replacement"))
	require.ErrorIs(t, err, os.ErrExist)
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "original", string(data), "a refused write leaves the existing file as it was")
}

func TestWrite_NonExclusiveTruncatesAnExistingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "program")
	require.NoError(t, os.WriteFile(path, []byte("a much longer original"), 0o600))

	require.NoError(t, Write(path, 0o755, false, writeString("short")))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "short", string(data))
}

func TestWrite_AFailedFillRemovesAnExclusiveFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "program")
	failure := errors.New("the source ended early")

	err := Write(path, 0o700, true, func(w io.Writer) error {
		_, _ = io.WriteString(w, "partial")
		return failure
	})
	require.ErrorIs(t, err, failure)
	_, statErr := os.Stat(path)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "no partial program stays behind")
}

func TestWrite_AFailedFillKeepsANonExclusiveFile(t *testing.T) {
	t.Parallel()
	// Write did not create the file, so it never removes it.
	path := filepath.Join(t.TempDir(), "program")
	require.NoError(t, os.WriteFile(path, []byte("original"), 0o600))
	failure := errors.New("no content")

	require.ErrorIs(t, Write(path, 0o755, false, func(io.Writer) error { return failure }), failure)
	_, err := os.Stat(path)
	assert.NoError(t, err)
}

func TestWrite_RefusesAPathInAMissingDirectory(t *testing.T) {
	t.Parallel()
	err := Write(filepath.Join(t.TempDir(), "missing", "program"), 0o700, true, writeString("x"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestWrite_ALargeProgramArrivesWhole(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "program")
	content := strings.Repeat("0123456789abcdef", 1<<16)
	require.NoError(t, Write(path, 0o700, true, func(w io.Writer) error {
		_, err := io.Copy(w, strings.NewReader(content))
		return err
	}))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Len(t, data, len(content))
}
