package cline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestTheCatalogHoldsDistinctModelsOfEachProvider(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, entry := range staticCatalog {
		key := entry.provider + "\x00" + entry.id
		assert.False(t, seen[key], "%s/%s appears twice", entry.provider, entry.id)
		seen[key] = true
		assert.NotEmpty(t, entry.provider)
		assert.NotEmpty(t, entry.id)
		for _, effort := range entry.efforts {
			assert.NotEqual(t, agent.EffortAuto, effort, "Auto is LeapMux's entry, never Cline's")
		}
	}
	assert.NotEmpty(t, providerCatalog("cline"), "Cline's own provider has models")
	assert.Nil(t, providerCatalog("no-such-provider"))
}

func TestModelEffortsPutAutoFirst(t *testing.T) {
	t.Parallel()
	efforts := modelEfforts([]string{"low", "high", effortOff, "max"})
	require.NotEmpty(t, efforts)
	assert.Equal(t, agent.EffortAuto, efforts[0].GetId())
	ids := make([]string, 0, len(efforts))
	for _, effort := range efforts {
		ids = append(ids, effort.GetId())
	}
	assert.Equal(t, []string{agent.EffortAuto, "max", "high", "low", effortOff}, ids, "strongest first, Off last")
	assert.Nil(t, modelEfforts(nil), "a model with no ladder takes no effort")
	assert.Nil(t, modelEfforts([]string{" "}))
}

func TestSessionCatalog(t *testing.T) {
	t.Parallel()
	models := sessionCatalog("anthropic", "claude-opus-5")
	require.NotEmpty(t, models)
	defaults := 0
	for _, m := range models {
		if m.IsDefault {
			defaults++
			assert.Equal(t, "claude-opus-5", m.Id)
		}
	}
	assert.Equal(t, 1, defaults, "the configured model is the default")

	models = sessionCatalog("anthropic", "claude-custom")
	assert.Equal(t, "claude-custom", models[0].Id, "a configured model the table lacks leads")
	assert.True(t, models[0].IsDefault)
	assert.Len(t, models, len(providerCatalog("anthropic"))+1)
}

func TestDefaultModelFor(t *testing.T) {
	t.Parallel()
	assert.Equal(t, providerCatalog("anthropic")[0].Id, defaultModelFor("anthropic"))
	assert.Equal(t, fallbackModel, defaultModelFor("no-such-provider"), "Cline's own fallback")
}

func TestModelInfoStatesTheWindow(t *testing.T) {
	t.Parallel()
	info := catalogEntry{provider: "p", id: "m", name: "", contextWindow: 42, efforts: []string{"high"}}.modelInfo()
	assert.Equal(t, "m", info.DisplayName, "a nameless model shows its id")
	assert.EqualValues(t, 42, info.ContextWindow)
	assert.Equal(t, agent.EffortAuto, info.DefaultEffort)
}

// A session whose settings state no model offers the table's models, and none
// of them is the default.
func TestSessionCatalogWithNoConfiguredModel(t *testing.T) {
	t.Parallel()
	models := sessionCatalog("anthropic", "")
	assert.Len(t, models, len(providerCatalog("anthropic")))
	for _, m := range models {
		assert.False(t, m.IsDefault, m.Id)
	}
	assert.Empty(t, sessionCatalog("no-such-provider", ""), "no table and no configured model offer nothing")
}
