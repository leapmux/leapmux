package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type removalFailureOps struct {
	defaultFileOps
	failures map[string]error
	attempts []string
}

func (f *removalFailureOps) remove(path string) error {
	f.attempts = append(f.attempts, path)
	if err := f.failures[path]; err != nil {
		return err
	}
	return f.defaultFileOps.remove(path)
}

func TestRemoveTempFilesReportsAllFailures(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "credentials.json")
	first, second := destination+".tmp-first", destination+".tmp-second"
	for _, path := range []string{first, second} {
		require.NoError(t, os.WriteFile(path, []byte("private credentials"), 0o600))
	}
	firstErr, secondErr := errors.New("first copy removal failed"), errors.New("second copy removal failed")
	failing := &removalFailureOps{failures: map[string]error{first: firstErr, second: secondErr}}
	withOps(t, failing)
	err := RemoveTempFiles(destination)
	assert.ErrorIs(t, err, firstErr)
	assert.ErrorIs(t, err, secondErr)
	assert.ElementsMatch(t, []string{first, second}, failing.attempts)
	assert.FileExists(t, first)
	assert.FileExists(t, second)
}

type writerCleanupOps struct {
	defaultFileOps
	primaryStage                         string
	primary, closeFailure, removeFailure error
	calls                                []string
	temporary                            string
	removeBeforeCleanup                  bool
}

func (f *writerCleanupOps) createTemp(dir, pattern string, mode os.FileMode) (*os.File, error) {
	file, err := f.defaultFileOps.createTemp(dir, pattern, mode)
	if err == nil {
		f.temporary = file.Name()
	}
	return file, err
}
func (f *writerCleanupOps) sync(file *os.File) error {
	f.calls = append(f.calls, "sync")
	if f.primaryStage == "sync" {
		return f.primary
	}
	return f.defaultFileOps.sync(file)
}
func (f *writerCleanupOps) close(file *os.File) error {
	f.calls = append(f.calls, "close")
	err := f.defaultFileOps.close(file)
	if f.removeBeforeCleanup {
		err = errors.Join(err, os.Remove(file.Name()))
	}
	return errors.Join(err, f.closeFailure)
}
func (f *writerCleanupOps) rename(old, destination string) error {
	f.calls = append(f.calls, "rename")
	if f.primaryStage == "rename" {
		return f.primary
	}
	return f.defaultFileOps.rename(old, destination)
}
func (f *writerCleanupOps) remove(path string) error {
	f.calls = append(f.calls, "remove")
	if f.removeFailure != nil {
		return f.removeFailure
	}
	return f.defaultFileOps.remove(path)
}

func TestWritePreservesPrimaryAndCleanupFailures(t *testing.T) {
	for _, stage := range []string{"sync", "close", "rename"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.json")
			require.NoError(t, os.WriteFile(path, []byte("old credentials"), 0o600))
			primary := errors.New("primary failure")
			closeFailure := errors.New("close failure")
			removeFailure := errors.New("remove failure")
			injected := &writerCleanupOps{primaryStage: stage, primary: primary, removeFailure: removeFailure}
			if stage != "rename" {
				injected.closeFailure = closeFailure
			}
			withOps(t, injected)
			err := WriteFile(path, []byte("private replacement"), 0o600)
			if stage != "close" {
				assert.ErrorIs(t, err, primary)
			}
			if stage != "rename" {
				assert.ErrorIs(t, err, closeFailure)
			}
			assert.ErrorIs(t, err, removeFailure)
			assert.NotErrorIs(t, err, os.ErrClosed)
			expected := []string{"sync", "close", "remove"}
			if stage == "rename" {
				expected = []string{"sync", "close", "rename", "remove"}
			}
			assert.Equal(t, expected, injected.calls)
			data, readErr := os.ReadFile(injected.temporary)
			require.NoError(t, readErr)
			assert.Equal(t, "private replacement", string(data))
			data, readErr = os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, "old credentials", string(data))
		})
	}
}

func TestWriteCleanupAcceptsAnAlreadyRemovedTemporaryFile(t *testing.T) {
	primary := errors.New("sync failed")
	injected := &writerCleanupOps{primaryStage: "sync", primary: primary, removeBeforeCleanup: true}
	withOps(t, injected)
	err := WriteFile(filepath.Join(t.TempDir(), "credentials.json"), []byte("private"), 0o600)
	require.ErrorIs(t, err, primary)
	assert.NotErrorIs(t, err, os.ErrNotExist)
	assert.Equal(t, []string{"sync", "close", "remove"}, injected.calls)
	assert.NoFileExists(t, injected.temporary)
}
