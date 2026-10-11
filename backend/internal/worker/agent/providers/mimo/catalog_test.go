package mimo

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modelIDs(models []*agent.ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.Id)
	}
	return ids
}

func effortIDs(model *agent.ModelInfo) []string {
	ids := make([]string, 0, len(model.SupportedEfforts))
	for _, effort := range model.SupportedEfforts {
		ids = append(ids, effort.GetId())
	}
	return ids
}

func TestBuildMiMoCatalog(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t)

	assert.Equal(t, []string{"mock/alpha", "mock/beta", "other/old"}, modelIDs(catalog.models),
		"the providers keep the server's order, and each provider's models sort by name")

	alpha := agent.FindAvailableModel(catalog.models, "mock/alpha")
	require.NotNil(t, alpha)
	assert.Equal(t, "Mock / Alpha", alpha.DisplayName, "two providers can serve models of one name")
	assert.Equal(t, int64(128000), alpha.ContextWindow)
	assert.Equal(t, []string{agent.EffortAuto, "high", "low"}, effortIDs(alpha),
		"Auto comes first, the strongest variant next, and the `default` variant is Auto already")
	assert.Equal(t, agent.EffortAuto, alpha.DefaultEffort)
	assert.False(t, alpha.Hidden)

	beta := agent.FindAvailableModel(catalog.models, "mock/beta")
	require.NotNil(t, beta)
	assert.Empty(t, beta.SupportedEfforts, "a model with no variant has no effort axis")
	assert.Empty(t, beta.DefaultEffort)
	assert.True(t, beta.IsDefault, "the configured model is the default")

	old := agent.FindAvailableModel(catalog.models, "other/old")
	require.NotNil(t, old)
	assert.True(t, old.Hidden, "a deprecated model stays resolvable and leaves the picker")

	assert.Equal(t, "mock/beta", catalog.defaultModel)
	assert.Equal(t, []string{contracts.MiMoModeBuild, contracts.MiMoModePlan, "max"}, modeIDs(catalog.modes),
		"only a visible primary agent runs a prompt")
}

// A catalog id is `<provider>/<model>`, so a provider or a model that states
// no id cannot have one. A model that states no id of its own takes its key in
// the reply's object.
func TestBuildMiMoCatalogSkipsWhatItCannotIdentify(t *testing.T) {
	t.Parallel()
	providers := mimoConfigProviders{Providers: []mimoProviderInfo{
		{Name: "No id", Models: map[string]mimoModelInfo{"orphan": {ID: "orphan", Name: "Orphan"}}},
		{ID: "mock", Models: map[string]mimoModelInfo{
			"zeta-key": {Name: "zeta"},
			"b":        {ID: "b", Name: "Alpha"},
			"a":        {ID: "a", Name: "alpha"},
			"":         {Name: "Blank"},
		}},
	}}

	catalog := buildMiMoCatalog(providers, mimoConfig{}, nil)
	assert.Equal(t, []string{"mock/a", "mock/b", "mock/zeta-key"}, modelIDs(catalog.models),
		"the names sort without case, and the id orders two equal names")
	zeta := agent.FindAvailableModel(catalog.models, "mock/zeta-key")
	require.NotNil(t, zeta)
	assert.Equal(t, "mock / zeta", zeta.DisplayName, "a provider with no name is labeled by its id")
	assert.Equal(t, "mock/a", catalog.defaultModel, "with no ranking, the first visible model is the default")
	assert.True(t, agent.FindAvailableModel(catalog.models, "mock/a").IsDefault)
	assert.Equal(t, 1, countDefaults(catalog.models), "exactly one model is the default")
}

func countDefaults(models []*agent.ModelInfo) int {
	defaults := 0
	for _, model := range models {
		if model.IsDefault {
			defaults++
		}
	}
	return defaults
}

// MiMo ranks a model for each provider. A rank that names a model the catalog
// lacks passes to the next provider's rank, in the server's provider order.
func TestMiMoDefaultModelSkipsARankTheCatalogLacks(t *testing.T) {
	t.Parallel()
	providers := mimoConfigProviders{
		Providers: []mimoProviderInfo{
			{ID: "mock", Models: map[string]mimoModelInfo{"alpha": {ID: "alpha"}}},
			{ID: "other", Models: map[string]mimoModelInfo{"new": {ID: "new"}}},
		},
		Default: map[string]string{"mock": "gone", "other": "new"},
	}
	assert.Equal(t, "other/new", buildMiMoCatalog(providers, mimoConfig{Model: "  "}, nil).defaultModel,
		"a blank configured model states no choice")
}

func TestMiMoModesSkipsAnAgentThatCannotRunAPrompt(t *testing.T) {
	t.Parallel()
	modes := mimoModes([]mimoAgentInfo{
		{Name: "", Mode: agentModePrimary},
		{Name: "general", Mode: "subagent"},
		{Name: "max", Mode: agentModeAll, Description: "Maximum effort."},
		{Name: "compaction", Mode: agentModePrimary, Hidden: true},
	})
	assert.Equal(t, []string{"max"}, modeIDs(modes), "an agent with no name, a subagent and a hidden agent run no prompt")
	assert.False(t, modes[0].Default, "only build is the default mode")
	assert.Equal(t, "Max", modes[0].Name)
}

func modeIDs(modes []agent.OptionDef) []string {
	ids := make([]string, 0, len(modes))
	for _, mode := range modes {
		ids = append(ids, mode.Id)
	}
	return ids
}

func TestMiMoDefaultModel(t *testing.T) {
	t.Parallel()
	var providers mimoConfigProviders
	require.NoError(t, json.Unmarshal([]byte(fakeProviders), &providers))

	assert.Equal(t, "mock/beta", buildMiMoCatalog(providers, mimoConfig{Model: "mock/beta"}, nil).defaultModel,
		"the configured model wins")
	assert.Equal(t, "mock/alpha", buildMiMoCatalog(providers, mimoConfig{Model: "gone/model"}, nil).defaultModel,
		"a configured model the catalog lacks falls to the first provider's top-ranked model")
	assert.Equal(t, "mock/alpha", buildMiMoCatalog(providers, mimoConfig{}, nil).defaultModel)

	unranked := providers
	unranked.Default = nil
	assert.Equal(t, "mock/alpha", buildMiMoCatalog(unranked, mimoConfig{}, nil).defaultModel,
		"with no ranking, the first visible model is the default")

	onlyDeprecated := mimoConfigProviders{Providers: []mimoProviderInfo{{ID: "other", Models: map[string]mimoModelInfo{
		"old": {ID: "old", Status: mimoModelStatusDeprecated},
	}}}}
	assert.Empty(t, buildMiMoCatalog(onlyDeprecated, mimoConfig{}, nil).defaultModel,
		"a hidden model is never picked as the default by itself")

	assert.Empty(t, buildMiMoCatalog(mimoConfigProviders{}, mimoConfig{}, nil).defaultModel)
}

func TestMiMoModesFallBackToTheStaticSeed(t *testing.T) {
	t.Parallel()
	assert.Equal(t, mimoStaticModes, mimoModes(nil))
	assert.Equal(t, mimoStaticModes, mimoModes([]mimoAgentInfo{{Name: "general", Mode: "subagent"}, {Name: "hidden", Mode: "primary", Hidden: true}}))

	modes := mimoModes([]mimoAgentInfo{{Name: "build", Mode: "primary", Description: " Build. "}, {Name: "plan", Mode: "primary"}})
	require.Len(t, modes, 2)
	assert.Equal(t, "Build", modes[0].Name)
	assert.Equal(t, "Build.", modes[0].Description)
	assert.True(t, modes[0].Default)
	assert.False(t, modes[1].Default)
}

func TestMiMoCatalogResolve(t *testing.T) {
	t.Parallel()
	catalog := testCatalog(t)

	assert.Equal(t, "mock/alpha", catalog.resolveModel("mock/alpha"))
	assert.Equal(t, "other/old", catalog.resolveModel("other/old"), "a deprecated model still resolves")
	assert.Empty(t, catalog.resolveModel("mock/gone"))
	assert.Empty(t, catalog.resolveModel(""))
	assert.Empty(t, catalog.resolveModel(agent.DefaultModelSentinel), "the account default names no model of the catalog")

	assert.Equal(t, "high", catalog.resolveEffort("mock/alpha", "high"))
	assert.Empty(t, catalog.resolveEffort("mock/alpha", agent.EffortAuto), "Auto sends no variant")
	assert.Empty(t, catalog.resolveEffort("mock/alpha", "extreme"), "a variant the model lacks sends none")
	assert.Empty(t, catalog.resolveEffort("mock/beta", "high"))
	assert.Empty(t, catalog.resolveEffort("mock/gone", "high"))
	assert.Empty(t, catalog.resolveEffort("mock/alpha", ""))

	assert.Equal(t, int64(128000), catalog.contextWindow("mock/alpha"))
	assert.Zero(t, catalog.contextWindow("mock/gone"))

	assert.True(t, catalog.hasMode("max"))
	assert.False(t, catalog.hasMode("general"), "a subagent cannot run a prompt")
}

func TestMiMoEfforts(t *testing.T) {
	t.Parallel()
	assert.Nil(t, mimoEfforts(nil))
	assert.Nil(t, mimoEfforts(map[string]json.RawMessage{"default": nil}), "only the default variant is no effort axis")
	efforts := mimoEfforts(map[string]json.RawMessage{"minimal": nil, "high": nil, "medium": nil})
	ids := make([]string, 0, len(efforts))
	for _, effort := range efforts {
		ids = append(ids, effort.GetId())
	}
	assert.Equal(t, []string{agent.EffortAuto, "high", "medium", "minimal"}, ids)
}
