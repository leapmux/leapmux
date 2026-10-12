package commandcode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeNativeCatalog(t *testing.T, config string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".commandcode")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "providers.json"), []byte(config), 0o600))
	return home
}

func TestNativeCatalogIncludesSelectableGatewayModels(t *testing.T) {
	t.Setenv("CMD_LOCAL_ONLY", "")
	models := loadModels(t.TempDir(), "claude-sonnet-5-5", false)
	model := agent.FindAvailableModel(models, "gpt-6.1-sol")
	require.NotNil(t, model)
	assert.Equal(t, int64(1050000), model.ContextWindow)
	require.Len(t, model.SupportedEfforts, 5)
	assert.Nil(t, agent.FindAvailableModel(models, "minimax/minimax-m3-free"))
	assert.NotEmpty(t, Registration().DefaultModels)
}

func TestNativeLocalOnlyCatalogOmitsGatewayChoices(t *testing.T) {
	home := writeNativeCatalog(t, `{"provider":{"native-local":{"baseURL":"http://localhost:1234/v1","api":"openai-completions","models":{"thinking":{"contextWindow":131072,"reasoningEfforts":["low","high"]}}}}}`)
	models := loadModels(home, "native-local/thinking", true)
	require.Len(t, models, 1)
	assert.Equal(t, "native-local/thinking", models[0].Id)
	t.Setenv("CMD_LOCAL_ONLY", "1")
	assert.Empty(t, Registration().DefaultModels)
	assert.Empty(t, Registration().DefaultModel())
}

func TestNativeGatewayMetadataDoesNotShareMutableModelsOrEfforts(t *testing.T) {
	first, second := gatewayModels(), gatewayModels()
	require.Len(t, first, 86)
	require.Len(t, second, 86)
	first[0].ContextWindow = 0
	first[0].SupportedEfforts[0].Id = "changed"
	assert.Equal(t, int64(1000000), second[0].ContextWindow)
	assert.Equal(t, "max", second[0].SupportedEfforts[0].Id)
}

func TestNativeCatalogReadsExactBYOKModelsAndEfforts(t *testing.T) {
	home := writeNativeCatalog(t, `{"provider":{"native-local":{"baseURL":"http://localhost:1234/v1","api":"openai-completions","apiKey":false,"models":{"thinking":{"contextWindow":131072,"reasoningEfforts":["low","high","high","invalid"]},"plain":{"contextWindow":32768}}}}}`)
	models := loadModels(home, "native-local/thinking", true)
	thinking := agent.FindAvailableModel(models, "native-local/thinking")
	require.NotNil(t, thinking)
	assert.Equal(t, int64(131072), thinking.ContextWindow)
	require.Len(t, thinking.SupportedEfforts, 2)
	plain := agent.FindAvailableModel(models, "native-local/plain")
	require.NotNil(t, plain)
	assert.Empty(t, plain.SupportedEfforts)
}

func TestNativeCatalogRetainsAnUnknownCurrentModel(t *testing.T) {
	for _, content := range []string{`{`, `null`, `{}`, `{"provider":{"invalid":{"models":{"bad":null}}}}`} {
		home := writeNativeCatalog(t, content)
		model := agent.FindAvailableModel(loadModels(home, "unlisted/native-model", true), "unlisted/native-model")
		require.NotNil(t, model)
		assert.Empty(t, model.SupportedEfforts)
	}
}
