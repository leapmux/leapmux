package qoder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
)

func TestSDKSettingsUseAPrivateManagedFile(t *testing.T) {
	t.Parallel()
	base := filepath.Join(t.TempDir(), "private settings with spaces")
	require.NoError(t, os.MkdirAll(base, 0o700))
	dirs := agentdirtest.NewDirs(t, []agentdir.Spec{agentDirSpec()}, base)
	directory, path, err := prepareSDKSettings(t.Context(), dirs)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, directory.Close()) })
	assert.Equal(t, directory.Path(), filepath.Dir(path))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var settings struct {
		MCP struct {
			LazyLoad *bool `json:"lazyLoad"`
		} `json:"mcp"`
	}
	require.NoError(t, json.Unmarshal(raw, &settings))
	require.NotNil(t, settings.MCP.LazyLoad)
	assert.False(t, *settings.MCP.LazyLoad)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	require.NoError(t, directory.Close())
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestSDKSettingsRequireManagedDirectories(t *testing.T) {
	t.Parallel()
	directory, path, err := prepareSDKSettings(t.Context(), nil)
	require.ErrorContains(t, err, "private agent directories")
	assert.Nil(t, directory)
	assert.Empty(t, path)
}
