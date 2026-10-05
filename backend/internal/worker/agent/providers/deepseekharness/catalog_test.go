package deepseekharness

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeNativeCatalog(t *testing.T, text string) nativeModelCatalog {
	t.Helper()
	var native nativeModelCatalog
	require.NoError(t, json.Unmarshal([]byte(text), &native))
	return native
}

const catalogFixture = `{"default":{"provider":"deepseek-official","model":"deepseek-flash"},"groups":[{"id":"deepseek-official","name":"DeepSeek","models":[{"id":"deepseek-flash","name":"Flash","reasoning":{"defaultEffort":"high","efforts":[{"id":"off","name":"Off"},{"id":"high","name":"High"}]}},{"id":"model/no-reasoning","name":""}]}]}`

func TestConvertModelCatalog(t *testing.T) {
	catalog, err := convertModelCatalog(decodeNativeCatalog(t, catalogFixture))
	require.NoError(t, err)
	require.Len(t, catalog.models, 2)
	assert.Equal(t, "deepseek-official/deepseek-flash", catalog.models[0].Id)
	assert.Equal(t, "Flash", catalog.models[0].DisplayName)
	assert.True(t, catalog.models[0].IsDefault)
	assert.Equal(t, "high", catalog.models[0].DefaultEffort)
	require.Len(t, catalog.models[0].SupportedEfforts, 2)
	assert.Equal(t, "off", catalog.models[0].SupportedEfforts[0].Id)
	assert.Equal(t, "model/no-reasoning", catalog.models[1].DisplayName)
	assert.False(t, catalog.models[1].IsDefault)
	assert.Empty(t, catalog.models[1].SupportedEfforts)
}

func TestConvertModelCatalogRejectsInvalidNativeCatalog(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"default":{"provider":"p","model":"m"},"groups":[]}`,
		`{"default":{"provider":"p","model":"m"},"groups":[{"id":"","models":[{"id":"m"}]}]}`,
		`{"default":{"provider":"p","model":"m"},"groups":[{"id":"p","models":[{"id":""}]}]}`,
		`{"default":{"provider":"p","model":"m"},"groups":[{"id":"p","models":[{"id":"m"},{"id":"m"}]}]}`,
		`{"default":{"provider":"p","model":"m"},"groups":[{"id":"p","models":[{"id":"m","reasoning":{"defaultEffort":"absent","efforts":[{"id":"high"}]}}]}]}`,
		`{"default":{"provider":"p","model":"m"},"groups":[{"id":"p","models":[{"id":"m","reasoning":{"efforts":[{"id":""}]}}]}]}`,
		`{"default":{"provider":"p","model":"m"},"groups":[{"id":"p","models":[{"id":"m","reasoning":{"efforts":[{"id":"high"},{"id":"high"}]}}]}]}`,
	} {
		t.Run(raw, func(t *testing.T) { _, err := convertModelCatalog(decodeNativeCatalog(t, raw)); require.Error(t, err) })
	}
}

func TestModelSelectionUsesNativeDefaultsAndExactEffort(t *testing.T) {
	c, err := convertModelCatalog(decodeNativeCatalog(t, catalogFixture))
	require.NoError(t, err)
	selection, err := c.resolve(c.defaultSelection, "", "")
	require.NoError(t, err)
	assert.Equal(t, "high", selection.Effort)
	selection, err = c.resolve(selection, "", "off")
	require.NoError(t, err)
	assert.Equal(t, "off", selection.Effort)
	selection, err = c.resolve(selection, "deepseek-official/model/no-reasoning", "")
	require.NoError(t, err)
	assert.Empty(t, selection.Effort)
	for _, tc := range []struct{ model, effort string }{{"missing", ""}, {"deepseek-official/missing", ""}, {"", "unknown"}, {"deepseek-official/model/no-reasoning", "high"}} {
		_, err := c.resolve(c.defaultSelection, tc.model, tc.effort)
		require.Error(t, err)
	}
}

// The Worker service resets the effort of a model switch to agent.EffortAuto for a provider that
// manages a model-dependent effort catalog, because Auto is valid for every model. The provider
// must read Auto as "the effort that the selected model declares as its default".
const twoReasoningModelsFixture = `{"default":{"provider":"deepseek-official","model":"deepseek-flash"},"groups":[{"id":"deepseek-official","name":"DeepSeek","models":[{"id":"deepseek-flash","name":"Flash","reasoning":{"defaultEffort":"high","efforts":[{"id":"off"},{"id":"high"},{"id":"max"}]}},{"id":"deepseek-pro","name":"Pro","reasoning":{"defaultEffort":"off","efforts":[{"id":"off"},{"id":"low"}]}},{"id":"plain","name":"Plain"}]}]}`

func TestModelSelectionReadsEffortAutoAsTheDefaultOfTheSelectedModel(t *testing.T) {
	c, err := convertModelCatalog(decodeNativeCatalog(t, twoReasoningModelsFixture))
	require.NoError(t, err)
	flash := modelSelection{Provider: "deepseek-official", Model: "deepseek-flash", Effort: "max"}
	for _, tc := range []struct {
		name, model, effort, want string
	}{
		{"a switch to a model that has another default effort", "deepseek-official/deepseek-pro", agent.EffortAuto, "off"},
		{"the same model", "", agent.EffortAuto, "high"},
		{"a switch to a model without reasoning", "deepseek-official/plain", agent.EffortAuto, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection, err := c.resolve(flash, tc.model, tc.effort)
			require.NoError(t, err)
			assert.Equal(t, tc.want, selection.Effort)
		})
	}
}

func TestModelSelectionStillRefusesAnEffortThatTheModelDoesNotOffer(t *testing.T) {
	c, err := convertModelCatalog(decodeNativeCatalog(t, twoReasoningModelsFixture))
	require.NoError(t, err)
	flash := modelSelection{Provider: "deepseek-official", Model: "deepseek-flash", Effort: "high"}
	for _, tc := range []struct{ model, effort string }{
		{"deepseek-official/deepseek-pro", "max"},
		{"deepseek-official/plain", "high"},
		{"", "automatic"},
	} {
		_, err := c.resolve(flash, tc.model, tc.effort)
		require.Error(t, err, tc.model+" "+tc.effort)
	}
}
