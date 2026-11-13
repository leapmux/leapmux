//go:build windows

package controlipc

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestSocketDirectoryWindowsRetainsPipeIdentityWithoutUnixState(t *testing.T) {
	data := t.TempDir()
	directory, err := prepareSocketDirectory(data, filepath.Join(data, "must-not-create"))
	require.NoError(t, err)
	assert.Equal(t, "npipe:leapmux-worker-full-worker-agent-full-agent", SocketURL(directory.Path(), "full-worker", SocketKindAgent, "full-agent"))
	assert.Equal(t, "npipe:leapmux-worker-full-worker-terminal-full-terminal", SocketURL(directory.Path(), "full-worker", SocketKindTerminal, "full-terminal"))
	require.NoError(t, directory.Sweep())
	require.NoError(t, directory.Close())
	require.NoError(t, directory.Close())
	_, err = os.Lstat(filepath.Join(data, "run"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Lstat(filepath.Join(data, "must-not-create"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
