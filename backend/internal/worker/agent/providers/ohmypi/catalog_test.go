package ohmypi

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelIDs(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "anthropic/claude-sonnet-4-5", joinModelID("anthropic", "claude-sonnet-4-5"))
	assert.Equal(t, "bare", joinModelID("", "bare"))

	cases := []struct {
		model, provider, id string
	}{
		{model: "anthropic/claude-sonnet-4-5", provider: "anthropic", id: "claude-sonnet-4-5"},
		{model: "openrouter/anthropic/claude-sonnet", provider: "openrouter", id: "anthropic/claude-sonnet"},
		{model: "bare", provider: "", id: "bare"},
		{model: "", provider: "", id: ""},
	}
	for _, tc := range cases {
		provider, id := splitModelID(tc.model)
		assert.Equal(t, tc.provider, provider, tc.model)
		assert.Equal(t, tc.id, id, tc.model)
		if tc.provider != "" {
			assert.Equal(t, tc.model, joinModelID(provider, id), "the split joins back")
		}
	}
}

func TestModelInfos(t *testing.T) {
	t.Parallel()
	models, err := modelInfos(json.RawMessage(availableModels))
	require.NoError(t, err)
	require.Len(t, models, 2)

	plain := models[0]
	assert.Equal(t, "mock/mock-model-2", plain.Id)
	assert.Equal(t, "Mock Model Two", plain.DisplayName)
	assert.Equal(t, "mock/mock-model-2", plain.Description, "the description states the provider")
	assert.Equal(t, agent.EffortAuto, plain.DefaultEffort)
	assert.Equal(t, int64(32000), plain.ContextWindow)
	assert.Equal(t, []string{"auto", "off"}, effortIDs(plain.SupportedEfforts), "a model that does not reason offers only off")

	reasoning := models[1]
	assert.Equal(t, []string{"auto", "xhigh", "high", "medium", "low", "minimal", "off"}, effortIDs(reasoning.SupportedEfforts))
	assert.Equal(t, "Auto", reasoning.SupportedEfforts[0].Name)
}

func TestModelInfosSkipsUnusableEntries(t *testing.T) {
	t.Parallel()
	models, err := modelInfos(json.RawMessage(`{"models":[` +
		`{"id":"","provider":"x"},` +
		`{"id":"m","provider":"a","name":""},` +
		`{"id":"m","provider":"a","name":"Duplicate"},` +
		`{"id":"m","provider":"b","name":"Other provider"},` +
		`{"id":"r","provider":"a","reasoning":true,"thinking":{"efforts":["","off","auto","high","quantum"]}}]}`))
	require.NoError(t, err)
	require.Len(t, models, 3)
	assert.Equal(t, "a/m", models[0].Id)
	assert.Equal(t, "m", models[0].DisplayName, "a model with no name shows its id")
	assert.Equal(t, "b/m", models[1].Id, "two providers can offer the same id")
	assert.Equal(t, []string{"auto", "high", "off", "quantum"}, effortIDs(models[2].SupportedEfforts),
		"a level omp states twice or LeapMux already lists is skipped; an unranked level sorts last")
}

// omp offers levels beyond "off" only for a model that reasons AND states its
// levels. Either one alone offers "off" only.
func TestModelEffortsNeedAReasoningModelWithLevels(t *testing.T) {
	t.Parallel()
	models, err := modelInfos(json.RawMessage(`{"models":[` +
		`{"id":"levels-only","provider":"a","reasoning":false,"thinking":{"efforts":["high","low"]}},` +
		`{"id":"reasons-only","provider":"a","reasoning":true},` +
		`{"id":"no-levels","provider":"a","reasoning":true,"thinking":{"efforts":[]}}]}`))
	require.NoError(t, err)
	require.Len(t, models, 3)
	for _, model := range models {
		assert.Equal(t, []string{"auto", "off"}, effortIDs(model.SupportedEfforts), model.Id)
	}
}

func TestModelInfosOfAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{}`, `{"models":[]}`, `{"models":null}`} {
		models, err := modelInfos(json.RawMessage(raw))
		require.NoError(t, err, raw)
		assert.Empty(t, models, raw)
	}
}

func TestModelInfosRefusesMalformedJSON(t *testing.T) {
	t.Parallel()
	_, err := modelInfos(json.RawMessage(`{"models":7}`))
	assert.Error(t, err)
}

func TestApplyAvailableModelsKeepsTheCatalogOnAnEmptyAnswer(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	r.agent.applyAvailableModels(json.RawMessage(`{"models":[]}`))
	r.agent.applyAvailableModels(json.RawMessage(`not json`))
	r.agent.Mu.Lock()
	defer r.agent.Mu.Unlock()
	assert.Len(t, r.agent.availableModels, 2, "the picker is never blanked")
}

func TestProviderForModel(t *testing.T) {
	t.Parallel()
	r := withCatalog(newRig(t))
	assert.Equal(t, "mock", r.agent.providerForModel("mock-model"))
	assert.Empty(t, r.agent.providerForModel("unknown"))
}

func effortIDs(efforts []*agent.EffortInfo) []string {
	ids := make([]string, 0, len(efforts))
	for _, effort := range efforts {
		ids = append(ids, effort.Id)
	}
	return ids
}
