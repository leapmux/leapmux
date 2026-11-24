package pi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiSteerBridgeWritesSessionPrivateCapabilities(t *testing.T) {
	t.Parallel()
	firstDir, secondDir := t.TempDir(), t.TempDir()
	firstPath, first, err := writePiSteerBridge(firstDir)
	require.NoError(t, err)
	secondPath, second, err := writePiSteerBridge(secondDir)
	require.NoError(t, err)
	assert.NotEqual(t, first.Capability, second.Capability)
	assert.NotEqual(t, first.Prefix, second.Prefix)

	for _, session := range []struct {
		directory string
		path      string
		bridge    *piSteerBridge
	}{{firstDir, firstPath, first}, {secondDir, secondPath, second}} {
		assert.Equal(t, session.directory, filepath.Dir(session.path))
		raw, err := os.ReadFile(session.path)
		require.NoError(t, err)
		firstLine, program, found := strings.Cut(string(raw), "\n")
		require.True(t, found)
		const declaration = "const configuration = "
		require.True(t, strings.HasPrefix(firstLine, declaration))
		var config piSteerBridge
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(firstLine, declaration)), &config))
		assert.Equal(t, *session.bridge, config)
		assert.Equal(t, 48, config.IDLength)
		assert.Contains(t, program, "export default function")
	}
}

func TestPiSteerBridgeRefusesAnUnavailableDirectory(t *testing.T) {
	t.Parallel()
	path, bridge, err := writePiSteerBridge(filepath.Join(t.TempDir(), "missing"))
	require.ErrorContains(t, err, "write the Pi steer identity extension")
	assert.Empty(t, path)
	assert.Nil(t, bridge)
}
