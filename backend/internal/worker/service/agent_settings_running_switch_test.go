package service

import (
	"context"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// switchingAgent is a running provider that follows the rule that Claude Code, Codex, and Command
// Code share. It always holds an effort when its model offers an effort axis: the requested tier
// when the model offers it, else the default tier of the model. It holds none for a model with no
// effort axis. Effort auto over a held tier needs a relaunch, because no native setter accepts
// auto. A launch with effort auto reports the default tier of the model, as the native CLI does.
// It reports every change through the real snapshot shape, and it records each request that it
// receives.
type switchingAgent struct {
	agenttest.IdleAgent

	mu         sync.Mutex
	models     []*agent.ModelInfo
	model      string
	effort     string
	permission string
	updates    []OptionMap
	stopOnce   sync.Once
	done       chan struct{}
	// relaunchOnModel makes a model change need a relaunch, as Command Code does for a model
	// that its native setter refuses, and as Claude Code does for the account default.
	relaunchOnModel bool
}

func (a *switchingAgent) Wait() error {
	<-a.done
	return nil
}

func (a *switchingAgent) Stop() { a.stopOnce.Do(func() { close(a.done) }) }

func (a *switchingAgent) IsStopped() bool {
	select {
	case <-a.done:
		return true
	default:
		return false
	}
}

// modelInfo returns the catalog entry of model, or an entry with no effort axis when the catalog
// lists none.
func modelInfo(models []*agent.ModelInfo, model string) *agent.ModelInfo {
	if found := agent.FindAvailableModel(models, model); found != nil {
		return found
	}
	return &agent.ModelInfo{Id: model}
}

func (a *switchingAgent) offersEffortLocked(effort string) bool {
	for _, tier := range modelInfo(a.models, a.model).SupportedEfforts {
		if tier.GetId() == effort {
			return true
		}
	}
	return false
}

// settleEffortLocked keeps the held tier when the model offers it, and otherwise takes the
// default tier of the model. A model with no effort axis has no default tier, so it holds none.
func (a *switchingAgent) settleEffortLocked() {
	if !a.offersEffortLocked(a.effort) {
		a.effort = modelInfo(a.models, a.model).DefaultEffort
	}
}

func (a *switchingAgent) OptionGroups() []*leapmuxv1.AvailableOptionGroup {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.optionGroupsLocked()
}

func (a *switchingAgent) optionGroupsLocked() []*leapmuxv1.AvailableOptionGroup {
	groups := []*leapmuxv1.AvailableOptionGroup{agent.ModelOptionGroup(a.models, a.model, agent.EffortSubGroups)}
	if effort := agent.EffortGroupForModel(agent.FindAvailableModel(a.models, a.model), a.effort, agent.EffortGroupLabel); effort != nil {
		groups = append(groups, effort)
	}
	if a.permission != "" {
		groups = append(groups, &leapmuxv1.AvailableOptionGroup{
			Id: agent.OptionIDPermissionMode, CurrentValue: a.permission, Mutable: true,
			Options: []*leapmuxv1.AvailableOption{{Id: a.permission}},
		})
	}
	return groups
}

func (a *switchingAgent) SettingsSnapshot() agent.SettingsApplyResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	return agent.ConfirmedSettings(agent.CurrentOptions(a.optionGroupsLocked()))
}

func (a *switchingAgent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updates = append(a.updates, maps.Clone(options))
	effort := options[agent.OptionIDEffort]
	if effort == agent.EffortAuto && a.effort != "" && a.effort != agent.EffortAuto {
		return agent.RestartRequiredSettings(options)
	}
	if model := options[agent.OptionIDModel]; a.relaunchOnModel && model != "" && model != a.model {
		return agent.RestartRequiredSettings(options)
	}
	if model := options[agent.OptionIDModel]; model != "" {
		a.model = model
	}
	if effort != "" && effort != agent.EffortAuto {
		a.effort = effort
	}
	a.settleEffortLocked()
	if mode := options[agent.OptionIDPermissionMode]; mode != "" {
		a.permission = mode
	}
	return agent.ConfirmedSettings(agent.CurrentOptions(a.optionGroupsLocked()))
}

// switchingStarter starts switchingAgent processes and records the options of each relaunch.
type switchingStarter struct {
	models          []*agent.ModelInfo
	relaunchOnModel bool

	mu       sync.Mutex
	launches []agent.Options
	current  *switchingAgent
}

func (s *switchingStarter) start(_ context.Context, opts agent.Options, _ agent.ProviderServices) (agent.Agent, error) {
	a := &switchingAgent{
		models:     s.models,
		model:      opts.Model(),
		effort:     opts.Effort(),
		permission: opts.PermissionMode(),
		done:       make(chan struct{}),

		relaunchOnModel: s.relaunchOnModel,
	}
	if a.effort == agent.EffortAuto {
		a.effort = ""
	}
	a.settleEffortLocked()
	s.mu.Lock()
	s.launches = append(s.launches, opts)
	s.current = a
	s.mu.Unlock()
	return a, nil
}

func (s *switchingStarter) launched() []agent.Options {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.launches)
}

func (s *switchingStarter) process() *switchingAgent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

// runningSwitchFixture starts a service with a running switchingAgent on fixtureModelA at the
// effort "low", for the provider under test. The agent row holds the same options. It returns the
// service, the response writer, a function that sends one model-only UpdateAgentSettings, and the
// starter that records each relaunch. relaunchOnModel makes every model change need a relaunch.
func runningSwitchFixture(t *testing.T, provider leapmuxv1.AgentProvider, relaunchOnModel bool) (*Service, *testResponseWriter, func(model string), *switchingStarter) {
	t.Helper()
	ctx := context.Background()
	svc, d, w := setupTestService(t)
	starter := &switchingStarter{models: fixtureModels(), relaunchOnModel: relaunchOnModel}
	options := map[string]string{agent.OptionIDModel: fixtureModelA, agent.OptionIDEffort: "low"}
	if mode := testRegistry.PermissionModeOrDefault(provider, ""); mode != "" {
		options[agent.OptionIDPermissionMode] = mode
	}
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: provider,
		Options: marshalOptions(options),
	}))
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: "agent-1", AgentProvider: provider, WorkingDir: t.TempDir(), Options: options,
	}, svc.Output.NewSink("agent-1", provider), starter.start)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent("agent-1") })
	svc.startAgentFn = startWith(svc.Agents, starter.start)
	// The first start is not a relaunch.
	starter.mu.Lock()
	starter.launches = nil
	starter.mu.Unlock()
	registerAgentWatch(svc, w.channelID, "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	switchTo := func(model string) {
		dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
			AgentId:  "agent-1",
			Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDModel: model}},
		}, w)
		require.Empty(t, w.errors)
	}
	return svc, w, switchTo, starter
}

// lastSettlements decodes the settlements of the most recent UpdateAgentSettings response.
func lastSettlements(t *testing.T, w *testResponseWriter) map[string]*leapmuxv1.AgentOptionSettlement {
	t.Helper()
	require.NotEmpty(t, w.responses)
	var resp leapmuxv1.UpdateAgentSettingsResponse
	require.NoError(t, proto.Unmarshal(w.responses[len(w.responses)-1].GetPayload(), &resp))
	return resp.GetOptionSettlements()
}

func storedOptions(t *testing.T, svc *Service, provider leapmuxv1.AgentProvider) OptionMap {
	t.Helper()
	row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	return loadOptions(testRegistry, row.Options, provider)
}

// TestUpdateAgentSettings_RunningModelSwitchKeepsTheEffortWithoutARestart pins the live path for
// every provider that manages its effort. The client sends the model alone. The new model offers
// the stored tier, so the provider receives the model and the same tier in one update, applies
// both without a relaunch, and confirms both. The row, the response, and the notification agree
// with the provider: the response and the notification report the model alone.
func TestUpdateAgentSettings_RunningModelSwitchKeepsTheEffortWithoutARestart(t *testing.T) {
	t.Parallel()

	for _, provider := range effortManagingProviders(t) {
		t.Run(provider.String(), func(t *testing.T) {
			t.Parallel()

			svc, w, switchTo, starter := runningSwitchFixture(t, provider, false)

			switchTo(fixtureModelB)

			updates := starter.process().updates
			require.Len(t, updates, 1, "one live update")
			assert.Equal(t, fixtureModelB, updates[0][agent.OptionIDModel])
			assert.Equal(t, "low", updates[0][agent.OptionIDEffort], "the update carries the kept tier")
			assert.Empty(t, starter.launched(), "the switch needs no relaunch")

			stored := storedOptions(t, svc, provider)
			assert.Equal(t, fixtureModelB, stored[agent.OptionIDModel])
			assert.Equal(t, "low", stored[agent.OptionIDEffort])
			assert.Equal(t, "low", starter.process().effort, "the provider holds the stored tier")

			settlements := lastSettlements(t, w)
			assert.Contains(t, settlements, agent.OptionIDModel)
			assert.NotContains(t, settlements, agent.OptionIDEffort, "the effort did not change, so the response reports none")
			changes := lastSettingsChangedChanges(t, w)
			assert.Contains(t, changes, agent.OptionIDModel)
			assert.NotContains(t, changes, agent.OptionIDEffort)
		})
	}
}

// TestUpdateAgentSettings_RunningModelSwitchRelaunchCarriesTheKeptEffort pins the restart path
// when the model change itself needs the relaunch, as for a model that a native setter refuses.
// The new model offers the stored tier, so the relaunch must carry that tier and not the launch
// default of the model. The relaunched provider holds it, and the row, the response, and the
// notification report the model alone.
func TestUpdateAgentSettings_RunningModelSwitchRelaunchCarriesTheKeptEffort(t *testing.T) {
	t.Parallel()

	for _, provider := range effortManagingProviders(t) {
		t.Run(provider.String(), func(t *testing.T) {
			t.Parallel()

			svc, w, switchTo, starter := runningSwitchFixture(t, provider, true)

			switchTo(fixtureModelB)

			launches := starter.launched()
			require.Len(t, launches, 1, "the provider asked for one relaunch")
			assert.Equal(t, fixtureModelB, launches[0].Model())
			assert.Equal(t, "low", launches[0].Effort(), "the relaunch carries the kept tier, not the default tier of the model")

			stored := storedOptions(t, svc, provider)
			assert.Equal(t, fixtureModelB, stored[agent.OptionIDModel])
			assert.Equal(t, "low", stored[agent.OptionIDEffort])
			assert.Equal(t, "low", starter.process().effort, "the relaunched provider holds the kept tier")
			assert.NotContains(t, lastSettlements(t, w), agent.OptionIDEffort)
			assert.NotContains(t, lastSettingsChangedChanges(t, w), agent.OptionIDEffort)
		})
	}
}

// TestUpdateAgentSettings_RunningModelSwitchToAModelWithoutEffortRelaunchesWithAuto pins the
// restart path for every provider that manages its effort. The new model offers no effort axis,
// so the worker resets the effort to auto, and the provider needs a relaunch to drop a held
// effort. The relaunch carries the new model and effort auto, never the old tier. The relaunched
// provider reports no effort, so the row, the response, and the provider agree that the axis is gone.
func TestUpdateAgentSettings_RunningModelSwitchToAModelWithoutEffortRelaunchesWithAuto(t *testing.T) {
	t.Parallel()

	for _, provider := range effortManagingProviders(t) {
		t.Run(provider.String(), func(t *testing.T) {
			t.Parallel()

			svc, w, switchTo, starter := runningSwitchFixture(t, provider, false)

			switchTo(fixtureModelPlain)

			launches := starter.launched()
			require.Len(t, launches, 1, "the provider asked for one relaunch")
			assert.Equal(t, fixtureModelPlain, launches[0].Model())
			assert.Equal(t, agent.EffortAuto, launches[0].Effort(), "the relaunch must not carry the old tier")

			stored := storedOptions(t, svc, provider)
			assert.Equal(t, fixtureModelPlain, stored[agent.OptionIDModel])
			assert.Equal(t, agent.EffortAuto, stored[agent.OptionIDEffort], "the row holds no concrete tier")
			assert.Empty(t, starter.process().effort)

			effort := lastSettlements(t, w)[agent.OptionIDEffort]
			require.NotNil(t, effort, "the response reports the dropped effort")
			assert.Equal(t, leapmuxv1.AgentOptionSettlementState_AGENT_OPTION_SETTLEMENT_STATE_CONFIRMED, effort.GetState())
			assert.Nil(t, effort.Value, "a confirmed settlement without a value removes the axis")
		})
	}
}

// TestUpdateAgentSettings_RunningModelSwitchesInARow pins two switches in a row. The first switch
// drops the effort, because the model offers none. The second switch returns to a model that offers
// tiers, and the client again sends the model alone. The row holds no tier any more, so the
// provider reports the default tier of the model. The first tier does not come back.
func TestUpdateAgentSettings_RunningModelSwitchesInARow(t *testing.T) {
	t.Parallel()

	for _, provider := range effortManagingProviders(t) {
		t.Run(provider.String(), func(t *testing.T) {
			t.Parallel()

			svc, _, switchTo, starter := runningSwitchFixture(t, provider, false)

			switchTo(fixtureModelB)
			assert.Equal(t, "low", storedOptions(t, svc, provider)[agent.OptionIDEffort])
			switchTo(fixtureModelPlain)
			switchTo(fixtureModelA)

			stored := storedOptions(t, svc, provider)
			assert.Equal(t, fixtureModelA, stored[agent.OptionIDModel])
			assert.Equal(t, "high", stored[agent.OptionIDEffort], "the default tier of the model, as the provider reports it")
			assert.Equal(t, stored[agent.OptionIDEffort], starter.process().effort, "the row and the provider agree")
			assert.Equal(t, fixtureModelA, starter.process().model)
		})
	}
}
