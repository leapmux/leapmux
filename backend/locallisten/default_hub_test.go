package locallisten

import (
	"os/user"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultHubSocketPath(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "hub.sock", HubSocketName)
	assert.Equal(t, "hub.sock", DefaultHubSocketPath(""))
	dataDir := filepath.Join(t.TempDir(), "hub")
	assert.Equal(t, filepath.Join(dataDir, "hub.sock"), DefaultHubSocketPath(dataDir))
}

func TestDefaultHubAddress(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		u, err := user.Current()
		require.NoError(t, err)
		require.NotEmpty(t, u.Uid)

		expected := "npipe:leapmux-hub-" + u.Uid

		addr, err := DefaultHubAddress("")
		require.NoError(t, err)
		assert.Equal(t, expected, addr)
		assert.True(t, IsLocal(addr))

		scheme, target, err := Parse(addr)
		require.NoError(t, err)
		assert.Equal(t, SchemeNpipe, scheme)
		assert.Equal(t, "leapmux-hub-"+u.Uid, target)

		addrWithDir, err := DefaultHubAddress(t.TempDir())
		require.NoError(t, err)
		assert.Equal(t, expected, addrWithDir)
		return
	}

	t.Run("valid data directory", func(t *testing.T) {
		t.Parallel()
		dataDir := filepath.Join(t.TempDir(), "hub")
		addr, err := DefaultHubAddress(dataDir)
		require.NoError(t, err)
		assert.Equal(t, "unix:"+filepath.Join(dataDir, "hub.sock"), addr)
		assert.True(t, IsLocal(addr))

		scheme, target, err := Parse(addr)
		require.NoError(t, err)
		assert.Equal(t, SchemeUnix, scheme)
		assert.Equal(t, DefaultHubSocketPath(dataDir), target)
	})

	t.Run("trailing separator is normalized", func(t *testing.T) {
		t.Parallel()
		dataDir := filepath.Join(t.TempDir(), "hub")
		addr, err := DefaultHubAddress(dataDir + string(filepath.Separator))
		require.NoError(t, err)
		assert.Equal(t, "unix:"+filepath.Join(dataDir, "hub.sock"), addr)
	})

	t.Run("parent segments are cleaned", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		unclean := root + string(filepath.Separator) + "sub" + string(filepath.Separator) + ".." + string(filepath.Separator) + "hub"
		addr, err := DefaultHubAddress(unclean)
		require.NoError(t, err)
		assert.Equal(t, "unix:"+filepath.Join(root, "hub", "hub.sock"), addr)
	})

	t.Run("empty data directory returns error", func(t *testing.T) {
		t.Parallel()
		addr, err := DefaultHubAddress("")
		require.Error(t, err)
		assert.ErrorContains(t, err, "data directory is required")
		assert.Empty(t, addr)
	})
}
