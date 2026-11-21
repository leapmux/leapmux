//go:build windows

package keystore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestLoadOrGenerateReadsRingWhilePublisherHasDeleteAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ring")
	want, err := LoadOrGenerate(path)
	require.NoError(t, err)

	// A rename can expose the file at its new path before the publisher
	// closes its handle. Hold the same delete access without racing a rename.
	name, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)
	handle, err := windows.CreateFile(name, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, windows.CloseHandle(handle)) })

	got, err := LoadOrGenerate(path)
	require.NoError(t, err)
	assert.Equal(t, want.keys, got.keys)
	assert.Equal(t, want.Pepper(), got.Pepper())
}

func TestLoadFromFileReadsLongWindowsPaths(t *testing.T) {
	dir := t.TempDir()
	shortPath := filepath.Join(dir, "ring")
	want, err := LoadOrGenerate(shortPath)
	require.NoError(t, err)
	data, err := os.ReadFile(shortPath)
	require.NoError(t, err)

	segment := strings.Repeat("a", 128)
	relative := filepath.Join(segment, segment, "ring")
	absolute := filepath.Join(dir, relative)
	require.NoError(t, os.MkdirAll(filepath.Dir(absolute), 0o700))
	require.NoError(t, os.WriteFile(absolute, data, 0o600))
	t.Chdir(dir)
	for _, path := range []string{relative, absolute} {
		got, err := LoadFromFile(path)
		require.NoError(t, err)
		assert.Equal(t, want.keys, got.keys)
		assert.Equal(t, want.Pepper(), got.Pepper())
	}
}
