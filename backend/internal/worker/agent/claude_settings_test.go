package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectThirdPartyProvider_NoSettings(t *testing.T) {
	homeDir := t.TempDir()
	workingDir := t.TempDir()
	assert.False(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestDetectThirdPartyProvider_UserSettings_Bedrock(t *testing.T) {
	homeDir := t.TempDir()
	workingDir := t.TempDir()

	claudeDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
		"env": {"CLAUDE_CODE_USE_BEDROCK": "true"}
	}`), 0o644))

	assert.True(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestDetectThirdPartyProvider_UserSettings_Vertex(t *testing.T) {
	homeDir := t.TempDir()
	workingDir := t.TempDir()

	claudeDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
		"env": {"CLAUDE_CODE_USE_VERTEX": "1"}
	}`), 0o644))

	assert.True(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestDetectThirdPartyProvider_UserSettings_Foundry(t *testing.T) {
	homeDir := t.TempDir()
	workingDir := t.TempDir()

	claudeDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
		"env": {"CLAUDE_CODE_USE_FOUNDRY": "true"}
	}`), 0o644))

	assert.True(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestDetectThirdPartyProvider_ProjectSettings(t *testing.T) {
	homeDir := t.TempDir()
	workingDir := t.TempDir()

	claudeDir := filepath.Join(workingDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
		"env": {"CLAUDE_CODE_USE_BEDROCK": "true"}
	}`), 0o644))

	assert.True(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestDetectThirdPartyProvider_LocalSettings(t *testing.T) {
	homeDir := t.TempDir()
	workingDir := t.TempDir()

	claudeDir := filepath.Join(workingDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), []byte(`{
		"env": {"CLAUDE_CODE_USE_VERTEX": "true"}
	}`), 0o644))

	assert.True(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestDetectThirdPartyProvider_EmptyValue(t *testing.T) {
	homeDir := t.TempDir()
	workingDir := t.TempDir()

	claudeDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
		"env": {"CLAUDE_CODE_USE_BEDROCK": ""}
	}`), 0o644))

	assert.False(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestDetectThirdPartyProvider_MalformedJSON(t *testing.T) {
	homeDir := t.TempDir()
	workingDir := t.TempDir()

	claudeDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`not json`), 0o644))

	assert.False(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestDetectThirdPartyProvider_NoEnvSection(t *testing.T) {
	homeDir := t.TempDir()
	workingDir := t.TempDir()

	claudeDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{
		"permissions": {"allow": []}
	}`), 0o644))

	assert.False(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestDetectThirdPartyProvider_ProcessEnv(t *testing.T) {
	homeDir := t.TempDir()
	workingDir := t.TempDir()

	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	assert.True(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestDetectThirdPartyProvider_Precedence_HigherOverrides(t *testing.T) {
	// User sets BEDROCK=true, but local project settings clears it.
	homeDir := t.TempDir()
	workingDir := t.TempDir()

	userDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(userDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "settings.json"), []byte(`{
		"env": {"CLAUDE_CODE_USE_BEDROCK": "true"}
	}`), 0o644))

	localDir := filepath.Join(workingDir, ".claude")
	require.NoError(t, os.MkdirAll(localDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "settings.local.json"), []byte(`{
		"env": {"CLAUDE_CODE_USE_BEDROCK": ""}
	}`), 0o644))

	// Local settings override user settings — empty string means not set.
	assert.False(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestDetectThirdPartyProvider_Precedence_LowerSetHigherAbsent(t *testing.T) {
	// User sets BEDROCK=true, project settings don't mention it.
	homeDir := t.TempDir()
	workingDir := t.TempDir()

	userDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(userDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(userDir, "settings.json"), []byte(`{
		"env": {"CLAUDE_CODE_USE_BEDROCK": "true"}
	}`), 0o644))

	// Project settings exist but don't mention BEDROCK.
	projDir := filepath.Join(workingDir, ".claude")
	require.NoError(t, os.MkdirAll(projDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "settings.json"), []byte(`{
		"env": {"OTHER_VAR": "value"}
	}`), 0o644))

	// User setting persists since project doesn't override it.
	assert.True(t, detectThirdPartyProvider(homeDir, workingDir))
}

func TestMergeSettingsEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"env": {"KEY1": "val1", "KEY2": "val2"}
	}`), 0o644))

	dst := make(map[string]string)
	mergeSettingsEnv(dst, path)
	assert.Equal(t, "val1", dst["KEY1"])
	assert.Equal(t, "val2", dst["KEY2"])
}

func TestMergeSettingsEnv_Override(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
		"env": {"KEY1": "new"}
	}`), 0o644))

	dst := map[string]string{"KEY1": "old", "KEY2": "keep"}
	mergeSettingsEnv(dst, path)
	assert.Equal(t, "new", dst["KEY1"])
	assert.Equal(t, "keep", dst["KEY2"])
}

func TestMergeSettingsEnv_MissingFile(t *testing.T) {
	dst := make(map[string]string)
	mergeSettingsEnv(dst, "/nonexistent/path/settings.json")
	assert.Empty(t, dst)
}

func TestManagedSettingsPath(t *testing.T) {
	path := managedSettingsPath()
	// Should return a non-empty path on supported OSes.
	assert.NotEmpty(t, path)
}
