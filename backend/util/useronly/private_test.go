package useronly

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivateFileCreationAndReading(t *testing.T) {
	parent := t.TempDir()
	file, err := CreateTemp(parent, "record-*")
	require.NoError(t, err)
	_, err = file.WriteString("private credentials")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	require.NoError(t, VerifyFile(file.Name()))
	data, err := ReadFile(file.Name())
	require.NoError(t, err)
	assert.Equal(t, "private credentials", string(data))
	other, err := CreateTemp(parent, "record-*")
	require.NoError(t, err)
	require.NoError(t, other.Close())
	assert.NotEqual(t, file.Name(), other.Name())
	_, err = OpenFile(parent)
	require.Error(t, err, "a directory cannot be read as a private record")
	_, err = ReadFile(filepath.Join(parent, "missing"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestPrivateDirectoryCreation(t *testing.T) {
	parent := t.TempDir()
	directory, err := MkdirTemp(parent, "stage-*")
	require.NoError(t, err)
	require.NoError(t, VerifyDirectory(directory))
	child := filepath.Join(directory, "child")
	require.NoError(t, Mkdir(child))
	require.NoError(t, VerifyDirectory(child))
	assert.ErrorIs(t, Mkdir(child), os.ErrExist)
	_, err = MkdirTemp(filepath.Join(parent, "missing"), "stage-*")
	require.Error(t, err)
	_, err = CreateTemp(parent, "invalid/name")
	require.Error(t, err)
}

func TestPrivateRecordSizeLimit(t *testing.T) {
	file, err := CreateTemp(t.TempDir(), "large-*")
	require.NoError(t, err)
	require.NoError(t, file.Truncate(maxPrivateRecordBytes+1))
	require.NoError(t, file.Close())
	_, err = ReadFile(file.Name())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}
