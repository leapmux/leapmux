package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateFileDurablePublishesWithoutReplacement(t *testing.T) {
	for _, data := range [][]byte{{}, {0, 1, 255}} {
		path := filepath.Join(t.TempDir(), "ring")
		require.NoError(t, CreateFileDurable(path, data, 0o600))
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, data, got)
		err = CreateFileDurable(path, []byte("another writer"), 0o600)
		require.ErrorIs(t, err, os.ErrExist)
		got, err = os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, data, got)
		entries, err := os.ReadDir(filepath.Dir(path))
		require.NoError(t, err)
		require.Len(t, entries, 1)
		if runtime.GOOS != "windows" {
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		}
	}
	require.ErrorIs(t, CreateFileDurable(filepath.Join(t.TempDir(), "absent", "ring"), nil, 0o600), os.ErrNotExist)
}

func TestCreateFileDurableReportsEveryPublicationFailure(t *testing.T) {
	stages := []string{"createTemp", "chmod", "write", "sync", "close", "publication"}
	if runtime.GOOS != "windows" {
		stages = append(stages, "syncDir")
	}
	for _, stage := range stages {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "ring")
			withOps(t, &failingOps{failAt: stage})
			publish := func(source, target string) error {
				if stage == "publication" {
					return os.ErrPermission
				}
				return renameNoReplace(source, target)
			}
			err := createFileDurable(path, []byte("complete ring"), 0o600, publish)
			require.ErrorIs(t, err, os.ErrPermission)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			if stage == "syncDir" {
				require.Len(t, entries, 1)
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, "complete ring", string(data))
			} else {
				assert.Empty(t, entries, "failure before publication must remove private temporary data")
			}
		})
	}
}

type partialPublicationWrite struct{ defaultFileOps }

func (partialPublicationWrite) write(file *os.File, data []byte) error {
	_, err := file.Write(data[:1])
	return errors.Join(err, os.ErrPermission)
}

func TestDurableWritersRemovePartialPrivateWrites(t *testing.T) {
	for _, writer := range []struct {
		name  string
		write func(string, []byte, os.FileMode) error
	}{{"create", CreateFileDurable}, {"replace", WriteFileDurable}} {
		t.Run(writer.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "ring")
			if writer.name == "replace" {
				require.NoError(t, os.WriteFile(path, []byte("original ring"), 0o600))
			}
			withOps(t, partialPublicationWrite{})
			require.ErrorIs(t, writer.write(path, []byte("new private ring"), 0o600), os.ErrPermission)
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			if writer.name == "create" {
				require.Empty(t, entries)
			} else {
				require.Len(t, entries, 1)
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, "original ring", string(data))
			}
		})
	}
}

func TestCreateFileDurablePublishesOnlyCompleteSyncedData(t *testing.T) {
	recorder := &recordingOps{}
	withOps(t, recorder)
	dir := t.TempDir()
	path := filepath.Join(dir, "ring")
	require.NoError(t, createFileDurable(path, []byte("complete ring"), 0o600, func(source, destination string) error {
		require.Equal(t, []string{"createTemp", "chmod", "write", "sync", "close"}, recorder.calls)
		require.NoFileExists(t, destination)
		data, err := os.ReadFile(source)
		require.NoError(t, err)
		require.Equal(t, "complete ring", string(data))
		recorder.calls = append(recorder.calls, "exclusive publication")
		return renameNoReplace(source, destination)
	}))
	expected := []string{"createTemp", "chmod", "write", "sync", "close", "exclusive publication"}
	if runtime.GOOS != "windows" {
		expected = append(expected, "syncDir:"+dir)
	}
	assert.Equal(t, expected, recorder.calls)
}
