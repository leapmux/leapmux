package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// dynamicCatalogProviders are the providers that read the model list, and the effort tiers of
// each model, from the running session or the server. Each one registers no static model
// catalog (Cline registers only the account-default placeholder), and each one sets
// Registration.ManagesEffort, so a model switch passes through resetEffortToAutoIfUnsupported.
var dynamicCatalogProviders = []struct {
	name     string
	provider leapmuxv1.AgentProvider
}{
	{"copilot", leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT},
	{"cline", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLINE},
	{"codewhale", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEWHALE},
	{"kimi", leapmuxv1.AgentProvider_AGENT_PROVIDER_KIMI_CODE},
	{"mimo", leapmuxv1.AgentProvider_AGENT_PROVIDER_MIMO_CODE},
}

// dynamicCatalogModels is a catalog of the shape that these providers report: every model
// carries its own effort tiers, and the session reports all of them.
//   - alpha and beta both offer high and low.
//   - gamma offers low only.
//   - plain has no effort axis.
func dynamicCatalogModels() []*agent.ModelInfo {
	tiers := func(ids ...string) []*agent.EffortInfo {
		out := make([]*agent.EffortInfo, 0, len(ids))
		for _, id := range ids {
			out = append(out, &agent.EffortInfo{Id: id, Name: id})
		}
		return out
	}
	return []*agent.ModelInfo{
		{Id: "alpha", DisplayName: "Alpha", SupportedEfforts: tiers("high", "low")},
		{Id: "beta", DisplayName: "Beta", SupportedEfforts: tiers("high", "low")},
		{Id: "gamma", DisplayName: "Gamma", SupportedEfforts: tiers("low")},
		{Id: "plain", DisplayName: "Plain"},
	}
}

// dynamicCatalogGroups builds the groups that a provider reports while it runs model current:
// the model group with per-model effort sub_groups, and the effort group of the current model.
// The providers build it with providerkit.ModelAndEffortGroups, which wraps the same calls.
func dynamicCatalogGroups(current, effort string) []*leapmuxv1.AvailableOptionGroup {
	models := dynamicCatalogModels()
	groups := []*leapmuxv1.AvailableOptionGroup{
		agent.ModelOptionGroup(models, current, agent.EffortSubGroupsLabeled(agent.EffortGroupLabel)),
	}
	if group := agent.EffortGroupForModel(agent.FindAvailableModel(models, current), effort, agent.EffortGroupLabel); group != nil {
		groups = append(groups, group)
	}
	return groups
}

// storedEffortAfterModelSwitch sends a model-only edit for a STOPPED agent of provider and reads
// the stored effort back. The row holds the catalog that the last run persisted, as it does in
// production (agents.option_groups), unless persisted is false: a row that never ran holds none.
// warm states whether the manager cache holds that catalog: PreloadCache fills it when a client
// watches the agent, and the exit of the process empties it.
func storedEffortAfterModelSwitch(t *testing.T, provider leapmuxv1.AgentProvider, persisted, warm bool, from, to, effort string) string {
	t.Helper()
	return storedEffortAfterEdit(t, provider, persisted, warm, from, effort, map[string]string{agent.OptionIDModel: to})
}

// storedEffortAfterEdit sends the options in sent as an edit of a stopped agent that holds the model
// from and the effort, then reads the stored effort back. The flags persisted and warm have the
// meaning that storedEffortAfterModelSwitch gives them.
func storedEffortAfterEdit(t *testing.T, provider leapmuxv1.AgentProvider, persisted, warm bool, from, effort string, sent map[string]string) string {
	t.Helper()
	ctx := context.Background()
	svc, d, w := setupTestService(t)
	const agentID = "agent-1"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: provider,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: from, agent.OptionIDEffort: effort}),
	}))
	if persisted {
		require.NoError(t, svc.Queries.SetAgentOptionGroups(ctx, db.SetAgentOptionGroupsParams{
			OptionGroups: mustMarshalOptionGroups(t, dynamicCatalogGroups(from, effort)), ID: agentID,
		}))
	}
	registerAgentWatch(svc, w.channelID, agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, w)
	if warm {
		svc.Agents.PreloadCache(agentID, dynamicCatalogGroups(from, effort))
	}

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  agentID,
		Settings: &leapmuxv1.AgentSettings{Options: sent},
	}, w)
	require.Empty(t, w.errors)

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, sent[agent.OptionIDModel], parseOptions(row.Options)[agent.OptionIDModel], "the edit stores the new model")
	return loadOptions(testRegistry, row.Options, provider)[agent.OptionIDEffort]
}

// TestUpdateAgentSettings_ModelSwitchKeepsEffortOnAStoppedDynamicCatalogAgent pins the stopped-agent
// path of a model switch for the providers whose catalog comes from the session. The persisted
// catalog describes the new model and offers the tier, so the stored effort must stay, exactly as
// it stays on a running agent and exactly as the picker of the browser shows it.
//
// The cold case is the agent that stops while a client watches it: the exit of the process drops the
// cache, and only a new watch fills it again. A CLI edit of an agent that nobody watches is cold too.
func TestUpdateAgentSettings_ModelSwitchKeepsEffortOnAStoppedDynamicCatalogAgent(t *testing.T) {
	t.Parallel()

	for _, p := range dynamicCatalogProviders {
		for _, warm := range []bool{true, false} {
			name := p.name + "/cold cache"
			if warm {
				name = p.name + "/warm cache"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, "high", storedEffortAfterModelSwitch(t, p.provider, true, warm, "alpha", "beta", "high"),
					"the new model offers high, so the switch keeps it")
			})
		}
	}
}

// TestUpdateAgentSettings_ModelSwitchResetsAnUnofferedEffortOnAStoppedDynamicCatalogAgent is the
// counterpart: the tier stays only when the new model offers it.
func TestUpdateAgentSettings_ModelSwitchResetsAnUnofferedEffortOnAStoppedDynamicCatalogAgent(t *testing.T) {
	t.Parallel()

	for _, p := range dynamicCatalogProviders {
		for _, target := range []string{"gamma", "plain"} {
			for _, warm := range []bool{true, false} {
				name := p.name + "/" + target + "/cold cache"
				if warm {
					name = p.name + "/" + target + "/warm cache"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					assert.Equal(t, agent.EffortAuto, storedEffortAfterModelSwitch(t, p.provider, true, warm, "alpha", target, "high"),
						"the new model does not offer high, so the effort returns to auto")
				})
			}
		}
	}
}

// TestUpdateAgentSettings_ExplicitEffortOnANeverRunDynamicCatalogAgentSurvives guards the one case
// that a stopped agent with NO catalog at all must keep: nothing describes the new model, so the
// running session has to judge an effort that the edit states (a CLI `agent set --model X
// --effort high`). A catalog that the worker invents for the display would call the model known,
// find no tier for it, and reset the effort.
func TestUpdateAgentSettings_ExplicitEffortOnANeverRunDynamicCatalogAgentSurvives(t *testing.T) {
	t.Parallel()

	for _, p := range dynamicCatalogProviders {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			sent := map[string]string{agent.OptionIDModel: "beta", agent.OptionIDEffort: "high"}
			assert.Equal(t, "high", storedEffortAfterEdit(t, p.provider, false, false, "alpha", agent.EffortAuto, sent),
				"no catalog describes the new model, so the stated effort stays for the session to judge")
		})
	}
}

// TestOptionGroupsForRow_OfflineModelEditKeepsTheDynamicCatalogModels pins what the browser lists for a
// stopped agent after an offline model edit. The row holds the new model, and the persisted
// catalog is stamped with the old one. The model list must still hold every model.
func TestOptionGroupsForRow_OfflineModelEditKeepsTheDynamicCatalogModels(t *testing.T) {
	t.Parallel()

	for _, p := range dynamicCatalogProviders {
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			manager := agent.NewManager(testRegistry, nil)
			persisted := dynamicCatalogGroups("alpha", "high")

			groups := manager.OptionGroupsForRow("agent-1", p.provider, "beta", persisted)

			modelGroup := optionids.GroupByID(groups, agent.OptionIDModel)
			require.NotNil(t, modelGroup)
			var ids []string
			for _, option := range modelGroup.GetOptions() {
				ids = append(ids, option.GetId())
			}
			assert.Equal(t, []string{"alpha", "beta", "gamma", "plain"}, ids,
				"the offline edit keeps every model that the last run reported")
		})
	}
}

// TestOptionGroupsForRow_OfflineModelEditShowsTheDynamicCatalogEffortTiersOfTheNewModel pins the effort group
// that the browser lists after an offline model edit. The persisted catalog carries the effort
// tiers of every model as sub_groups of the model options, so the top-level effort group follows the
// new model. A stale group would list the tiers of the old model for the new one.
func TestOptionGroupsForRow_OfflineModelEditShowsTheDynamicCatalogEffortTiersOfTheNewModel(t *testing.T) {
	t.Parallel()

	for _, p := range dynamicCatalogProviders {
		for _, test := range []struct {
			target string
			want   []string
		}{
			{target: "gamma", want: []string{"low"}},
			{target: "plain", want: nil},
		} {
			t.Run(p.name+"/"+test.target, func(t *testing.T) {
				t.Parallel()
				manager := agent.NewManager(testRegistry, nil)
				persisted := dynamicCatalogGroups("alpha", "high")

				groups := manager.OptionGroupsForRow("agent-1", p.provider, test.target, persisted)

				var ids []string
				for _, option := range optionids.GroupByID(groups, agent.OptionIDEffort).GetOptions() {
					ids = append(ids, option.GetId())
				}
				assert.Equal(t, test.want, ids, "the effort group lists the tiers of %s", test.target)
			})
		}
	}
}

// liveCatalogAgent is a running agent that reports the dynamic catalog of the model that it runs, and
// records the options that the worker sends to it. After an update it runs the new model and keeps
// the effort, as a provider that confirms what it received does.
type liveCatalogAgent struct {
	agenttest.IdleAgent
	stopOnce sync.Once
	stopped  chan struct{}

	mu       sync.Mutex
	model    string
	effort   string
	received []optionmap.Map
}

func newLiveCatalogAgent(model, effort string) *liveCatalogAgent {
	return &liveCatalogAgent{stopped: make(chan struct{}), model: model, effort: effort}
}

func (a *liveCatalogAgent) Stop() { a.stopOnce.Do(func() { close(a.stopped) }) }

func (a *liveCatalogAgent) IsStopped() bool {
	select {
	case <-a.stopped:
		return true
	default:
		return false
	}
}

func (a *liveCatalogAgent) Wait() error {
	<-a.stopped
	return nil
}

func (a *liveCatalogAgent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	defer a.mu.Unlock()
	return dynamicCatalogGroups(a.model, a.effort)
}

func (a *liveCatalogAgent) SettingsSnapshot() agent.SettingsApplyResult {
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

func (a *liveCatalogAgent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.mu.Lock()
	a.received = append(a.received, options.Clone())
	if model := options[agent.OptionIDModel]; model != "" {
		a.model = model
	}
	if effort := options[agent.OptionIDEffort]; effort != "" {
		a.effort = effort
	}
	a.mu.Unlock()
	return agent.ConfirmedSettings(agent.CurrentOptions(a.OptionGroups()))
}

func (a *liveCatalogAgent) receivedUpdates() []optionmap.Map {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]optionmap.Map(nil), a.received...)
}

// TestUpdateAgentSettings_ModelSwitchOnARunningDynamicCatalogAgentSendsTheKeptEffort pins the running
// path. The browser sends the model alone. The live catalog describes the new model, so the provider
// receives the model with the kept effort, in one update, and the row keeps the effort. A model that
// does not offer the tier makes the worker send auto.
func TestUpdateAgentSettings_ModelSwitchOnARunningDynamicCatalogAgentSendsTheKeptEffort(t *testing.T) {
	t.Parallel()

	for _, p := range dynamicCatalogProviders {
		for _, test := range []struct {
			target string
			want   string
		}{
			{target: "beta", want: "high"},
			{target: "gamma", want: agent.EffortAuto},
			{target: "plain", want: agent.EffortAuto},
		} {
			t.Run(p.name+"/"+test.target, func(t *testing.T) {
				t.Parallel()
				ctx := context.Background()
				svc, d, w := setupTestService(t)
				const agentID = "agent-1"
				launch := map[string]string{agent.OptionIDModel: "alpha", agent.OptionIDEffort: "high"}
				require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
					ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
					AgentProvider: p.provider, Options: marshalOptions(launch),
				}))
				live := newLiveCatalogAgent("alpha", "high")
				_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
					AgentID: agentID, AgentProvider: p.provider, WorkingDir: t.TempDir(), Options: launch,
				}, svc.Output.NewSink(agentID, p.provider),
					func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return live, nil })
				require.NoError(t, err)
				t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })
				registerAgentWatch(svc, w.channelID, agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

				dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
					AgentId:  agentID,
					Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDModel: test.target}},
				}, w)
				require.Empty(t, w.errors)

				updates := live.receivedUpdates()
				require.Len(t, updates, 1, "one update carries the whole edit")
				assert.Equal(t, test.target, updates[0][agent.OptionIDModel])
				assert.Equal(t, test.want, updates[0][agent.OptionIDEffort], "the effort that the provider receives with the model")
				row, err := svc.Queries.GetAgentByID(ctx, agentID)
				require.NoError(t, err)
				assert.Equal(t, test.want, loadOptions(testRegistry, row.Options, p.provider)[agent.OptionIDEffort])
			})
		}
	}
}
