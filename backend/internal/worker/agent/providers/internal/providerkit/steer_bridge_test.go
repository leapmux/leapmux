package providerkit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func assertBridgeFile(t *testing.T, path string, bridge SteerIdentityBridge, wantBody string, includeChildPrefix bool) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	nonce, ok := strings.CutPrefix(bridge.Capability, "leapmux-steer-proof-")
	require.True(t, ok, "capability must carry the steer proof prefix")
	require.NotEmpty(t, nonce)
	assert.Equal(t, "leapmux-steer:"+nonce+":", bridge.Prefix)
	assert.Equal(t, SteerIdentityMessageField, bridge.MessageField)
	assert.Equal(t, len(nonce), bridge.IDLength)
	if includeChildPrefix {
		assert.Equal(t, "leapmux-child-input:"+nonce+":", bridge.ChildPrefix)
	} else {
		assert.Empty(t, bridge.ChildPrefix)
	}

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	firstLine, body, ok := strings.Cut(string(raw), "\n")
	require.True(t, ok)
	assert.Equal(t, wantBody, body)
	if includeChildPrefix {
		assert.Contains(t, firstLine, `"childPrefix"`)
	} else {
		assert.NotContains(t, firstLine, "childPrefix")
	}
	var decoded SteerIdentityBridge
	require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(firstLine, "const configuration = ")), &decoded))
	assert.Equal(t, bridge, decoded)
}

func TestWriteSteerIdentityBridge(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, bridge, err := WriteSteerIdentityBridge(dir, "Pi", []byte("export default function() {}\n"), false)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "leapmux-steer-identity.mjs"), path)
	assertBridgeFile(t, path, bridge, "export default function() {}\n", false)

	childDir := t.TempDir()
	childPath, childBridge, err := WriteSteerIdentityBridge(childDir, "Oh My Pi", []byte("export default function() {}\n"), true)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(childDir, "leapmux-steer-identity.mjs"), childPath)
	assert.NotEqual(t, bridge.Capability, childBridge.Capability)
	assertBridgeFile(t, childPath, childBridge, "export default function() {}\n", true)

	// Overwriting in the same directory replaces the previous file cleanly with a fresh nonce.
	overwritePath, overwriteBridge, err := WriteSteerIdentityBridge(dir, "Pi", []byte("export {};\n"), true)
	require.NoError(t, err)
	assert.Equal(t, path, overwritePath)
	assert.NotEqual(t, bridge.Capability, overwriteBridge.Capability)
	assertBridgeFile(t, overwritePath, overwriteBridge, "export {};\n", true)

	// Nil and empty program slices write only the configuration header line.
	for name, program := range map[string][]byte{"nil program": nil, "empty program": {}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			emptyDir := t.TempDir()
			emptyPath, emptyBridge, err := WriteSteerIdentityBridge(emptyDir, "Pi", program, false)
			require.NoError(t, err)
			assertBridgeFile(t, emptyPath, emptyBridge, "", false)
		})
	}

	missingPath, missingBridge, err := WriteSteerIdentityBridge(filepath.Join(t.TempDir(), "missing"), "Pi", []byte("x"), false)
	require.ErrorContains(t, err, "write the Pi steer identity extension")
	assert.Empty(t, missingPath)
	assert.Equal(t, SteerIdentityBridge{}, missingBridge)
}
