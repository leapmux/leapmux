package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// Model ids of effortTierFixtureCatalog. No provider lists them in its static catalog, so the
// catalog on the agent row is the only source that describes them. Each id has its own run of
// letters, because the Claude normalizer reduces an id to its family word, and two ids with one
// family word would read as one model.
const (
	fixtureModelA     = "aurora"
	fixtureModelB     = "borealis"
	fixtureModelPlain = "plain"
)

// fixtureModels returns the models of effortTierFixtureCatalog. fixtureModelA and fixtureModelB
// share the tiers "low" and "high". Only fixtureModelB offers "max". fixtureModelPlain offers no
// effort axis.
func fixtureModels() []*agent.ModelInfo {
	tiers := func(ids ...string) []*agent.EffortInfo {
		out := make([]*agent.EffortInfo, 0, len(ids))
		for _, id := range ids {
			out = append(out, &agent.EffortInfo{Id: id, Name: id})
		}
		return out
	}
	return []*agent.ModelInfo{
		{Id: fixtureModelA, DisplayName: "Tiered A", IsDefault: true, DefaultEffort: "high", SupportedEfforts: tiers("high", "low")},
		{Id: fixtureModelB, DisplayName: "Tiered B", DefaultEffort: "high", SupportedEfforts: tiers("max", "high", "low")},
		{Id: fixtureModelPlain, DisplayName: "Plain"},
	}
}

// effortTierFixtureCatalog returns the live catalog shape that every provider with a
// model-dependent effort axis builds through providerkit.ModelAndEffortGroups: a model group in
// which each model carries its own effort sub_group, then the effort group of the current model.
func effortTierFixtureCatalog(current, currentEffort string) []*leapmuxv1.AvailableOptionGroup {
	models := fixtureModels()
	groups := []*leapmuxv1.AvailableOptionGroup{agent.ModelOptionGroup(models, current, agent.EffortSubGroups)}
	if effort := agent.EffortGroupForModel(agent.FindAvailableModel(models, current), currentEffort, agent.EffortGroupLabel); effort != nil {
		groups = append(groups, effort)
	}
	return groups
}

// groupOptionIDs lists the option ids of the group with the given id, in catalog order.
func groupOptionIDs(groups []*leapmuxv1.AvailableOptionGroup, id string) []string {
	var ids []string
	for _, option := range optionids.GroupByID(groups, id).GetOptions() {
		ids = append(ids, option.GetId())
	}
	return ids
}

// effortManagingProviders lists every provider whose effort tiers belong to the model. The
// registry states the set, so a provider added later joins the tests that use this list.
func effortManagingProviders(t *testing.T) []leapmuxv1.AgentProvider {
	t.Helper()
	var providers []leapmuxv1.AgentProvider
	for _, provider := range testRegistry.Providers() {
		if testRegistry.ManagesEffort(provider) {
			providers = append(providers, provider)
		}
	}
	require.NotEmpty(t, providers)
	return providers
}

// TestUpdateAgentSettings_StoppedAgentAcceptsAnOptionOfThePersistedCatalog pins the same source
// for an axis other than the effort. A server-driven option has no static template. The client
// only sends an option of the catalog that it holds, and that catalog is the row catalog of a
// stopped agent. The worker must accept the option although no process runs and the manager cache
// is empty.
func TestUpdateAgentSettings_StoppedAgentAcceptsAnOptionOfThePersistedCatalog(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		Options:       `{"opencode_mode":"safe"}`,
	}))
	require.NoError(t, svc.Queries.SetAgentOptionGroups(ctx, db.SetAgentOptionGroupsParams{
		OptionGroups: mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{{
			Id:           "opencode_mode",
			CurrentValue: "safe",
			Options:      []*leapmuxv1.AvailableOption{{Id: "safe"}, {Id: "fast"}},
		}}),
		ID: "agent-1",
	}))

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  "agent-1",
		Settings: &leapmuxv1.AgentSettings{Options: map[string]string{"opencode_mode": "fast"}},
	}, w)

	require.Empty(t, w.errors)
	row, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "fast", parseOptions(row.Options)["opencode_mode"])
}

// TestUpdateAgentSettings_StoppedModelSwitchUsesThePersistedCatalog pins the model switch on an
// agent that has no process. The client sends the model alone. The row holds the catalog of the
// last live run. That catalog lists the new model and states that it offers the stored tier.
// The live list differs from the static seed of the provider, and the five providers with no
// static model list have no seed at all. So the persisted catalog is the only source that can
// show that the new model offers the tier.
//
// The two cache states cover both ways to reach the switch. A browser that watches the agent
// preloads the manager cache from the row. A process exit empties the cache, and the remote CLI
// can reach the agent before any browser does.
func TestUpdateAgentSettings_StoppedModelSwitchUsesThePersistedCatalog(t *testing.T) {
	t.Parallel()

	for _, provider := range effortManagingProviders(t) {
		for _, warm := range []bool{false, true} {
			name := provider.String() + "/cold cache"
			if warm {
				name = provider.String() + "/warm cache"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				ctx := context.Background()
				svc, d, w := setupTestService(t)
				require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
					ID:            "agent-1",
					WorkingDir:    t.TempDir(),
					HomeDir:       t.TempDir(),
					AgentProvider: provider,
					Options:       marshalOptions(map[string]string{agent.OptionIDModel: fixtureModelA, agent.OptionIDEffort: "low"}),
				}))
				catalog := effortTierFixtureCatalog(fixtureModelA, "low")
				require.NoError(t, svc.Queries.SetAgentOptionGroups(ctx, db.SetAgentOptionGroupsParams{
					OptionGroups: mustMarshalOptionGroups(t, catalog),
					ID:           "agent-1",
				}))
				if warm {
					svc.Agents.PreloadCache("agent-1", catalog)
				}

				dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
					AgentId:  "agent-1",
					Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDModel: fixtureModelB}},
				}, w)

				require.Empty(t, w.errors)
				row, err := svc.Queries.GetAgentByID(ctx, "agent-1")
				require.NoError(t, err)
				stored := loadOptions(testRegistry, row.Options, provider)
				assert.Equal(t, fixtureModelB, stored[agent.OptionIDModel])
				assert.Equal(t, "low", stored[agent.OptionIDEffort],
					"the persisted catalog shows that %s offers low, so the switch keeps it", fixtureModelB)

				// The picker that the next read serves still lists the live models, and its
				// effort group offers the tiers of the new model.
				view := optionGroupsView(svc.Agents, &row, nil)
				assert.Equal(t, []string{fixtureModelA, fixtureModelB, fixtureModelPlain}, groupOptionIDs(view, agent.OptionIDModel),
					"the switch must not replace the live model list with the static seed")
				assert.Equal(t, []string{"max", "high", "low"}, groupOptionIDs(view, agent.OptionIDEffort))
			})
		}
	}
}
