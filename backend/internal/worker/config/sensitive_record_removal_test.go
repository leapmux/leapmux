package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSensitiveRemovalPublishesAfterEveryRemovalAndPreservesAllErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	destinationFailure, copyFailure, flushFailure := errors.New("destination failed"), errors.New("copy failed"), errors.New("directory flush failed")
	var sequence []string
	err := removeSensitiveRecord(path, func(string) error { sequence = append(sequence, "destination"); return destinationFailure }, func(string) error { sequence = append(sequence, "copies"); return copyFailure }, func(directory string) error {
		sequence = append(sequence, "directory flush")
		assert.Equal(t, filepath.Dir(path), directory)
		return flushFailure
	})
	assert.Equal(t, []string{"destination", "copies", "directory flush"}, sequence)
	require.ErrorIs(t, err, destinationFailure)
	require.ErrorIs(t, err, copyFailure)
	require.ErrorIs(t, err, flushFailure)
}

func TestSensitiveRemovalRetriesDirectoryBarrierAfterFilesAreGone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	require.NoError(t, os.WriteFile(path, []byte("credentials"), 0o600))
	require.NoError(t, os.WriteFile(path+".tmp-copy", []byte("credentials"), 0o600))
	failure := errors.New("directory flush failed")
	err := removeSensitiveRecord(path, os.Remove, atomicfile.RemoveTempFiles, func(string) error { return failure })
	require.ErrorIs(t, err, failure)
	assert.NoFileExists(t, path)
	assert.NoFileExists(t, path+".tmp-copy")
	confirmed := false
	require.NoError(t, removeSensitiveRecord(path, os.Remove, atomicfile.RemoveTempFiles, func(directory string) error { confirmed = true; return atomicfile.SyncDirectory(directory) }))
	assert.True(t, confirmed, "a fresh cleanup confirms removal even without surviving files")
	require.NoError(t, RemoveSensitiveRecord(filepath.Join(t.TempDir(), "missing-parent", "state.json")))
}
