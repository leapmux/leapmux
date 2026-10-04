package codewhale

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// threadRoute is the test thread's own route.
var threadRoute = threadPath(testThreadID, "")

// settledAgent is an agent whose thread runs deepseek-flash in agent mode with
// the ask posture, and whose catalog holds two models.
func settledAgent(t *testing.T, rt *fakeRuntime) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	a, sink := newTestAgent(t, rt)
	a.applyThreadRecordLocked(threadRecord{ID: testThreadID, Model: "deepseek-flash", ModelProvider: "deepseek", ModelProviderID: "deepseek", Mode: "agent", PermissionPosture: "ask"})
	a.applyModelCatalog([]providerModel{
		{ID: "deepseek-flash", ReasoningEffort: capabilitySupported, ReasoningEffortLevels: []string{"low", "high"}},
		{ID: "deepseek-pro", ReasoningEffort: capabilityUnsupported},
	})
	return a, sink
}

func TestOptionGroupsStateEveryAxis(t *testing.T) {
	t.Parallel()
	a, _ := settledAgent(t, nil)
	options := agent.CurrentOptions(a.OptionGroups())
	want := map[string]string{
		agent.OptionIDModel:           "deepseek-flash",
		agent.OptionIDEffort:          agent.EffortAuto,
		contracts.CodewhaleOptionMode: contracts.CodewhaleModeAgent,
		agent.OptionIDPermissionMode:  contracts.CodewhalePostureAsk,
	}
	assert.Equal(t, want, map[string]string(options))
	assert.Equal(t, want, map[string]string(a.SettingsSnapshot().SurfacedOptions))
}

func TestUpdateSettingsPatchesTheThreadOnce(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondJSON(http.MethodPatch, threadRoute, http.StatusOK, threadRecord{
		ID: testThreadID, Model: "deepseek-pro", ModelProvider: "deepseek", ModelProviderID: "deepseek", Mode: "plan", PermissionPosture: "full_access",
	})
	a, _ := settledAgent(t, rt)

	result := a.UpdateSettings(optionmap.Map{
		agent.OptionIDModel:           "deepseek-pro",
		contracts.CodewhaleOptionMode: "plan",
		agent.OptionIDPermissionMode:  "full_access",
		agent.OptionIDEffort:          "high",
	})

	require.Len(t, rt.requestsTo(http.MethodPatch, threadRoute), 1, "the three thread axes ride one update")
	assert.Equal(t, map[string]any{"model": "deepseek-pro", "mode": "plan", "permission_posture": "full_access"}, rt.lastBody(t, http.MethodPatch, threadRoute))
	assert.True(t, result.AppliedLive)
	for key, value := range map[string]string{agent.OptionIDModel: "deepseek-pro", contracts.CodewhaleOptionMode: "plan", agent.OptionIDPermissionMode: "full_access", agent.OptionIDEffort: "high"} {
		settlement := result.Settlements[key]
		assert.Equal(t, agent.OptionSettlementConfirmed, settlement.State, key)
		require.NotNil(t, settlement.Value, key)
		assert.Equal(t, value, *settlement.Value, key)
	}
	assert.Equal(t, "high", a.settings.effort, "the effort is LeapMux's own and rides the next turn")
}

func TestUpdateSettingsLeavesWhatTheRuntimeRefused(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodPatch, threadRoute, http.StatusBadRequest, "unknown model")
	a, _ := settledAgent(t, rt)

	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "a-model-nobody-has"})
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDModel].State)
	assert.Equal(t, "deepseek-flash", result.SurfacedOptions[agent.OptionIDModel])
}

func TestUpdateSettingsIgnoresAValueNoAxisOffers(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, _ := settledAgent(t, rt)
	result := a.UpdateSettings(optionmap.Map{
		contracts.CodewhaleOptionMode: "operate",
		agent.OptionIDPermissionMode:  "yolo",
		"an_unknown_axis":             "x",
		agent.OptionIDModel:           "",
	})
	assert.Empty(t, rt.requestsTo(http.MethodPatch, threadRoute), "nothing reached the runtime")
	assert.Empty(t, result.Settlements)
}

func TestAThreadUpdateFromAnywhereReachesTheSettings(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, sink := settledAgent(t, rt)
	before := sink.SettingsRefreshCount()

	// A remembered approval raised the posture.
	a.HandleOutput(runtimeEvent(1, "thread.updated", "", "", map[string]any{"thread": map[string]any{"id": testThreadID, "permission_posture": "full_access"}, "changes": map[string]any{"permission_posture": "full_access"}}))
	assert.Equal(t, "full_access", agent.CurrentOptions(a.OptionGroups())[agent.OptionIDPermissionMode])
	assert.Equal(t, before+1, sink.SettingsRefreshCount())

	// The same record again changes nothing, so it persists nothing.
	a.HandleOutput(runtimeEvent(2, "thread.updated", "", "", map[string]any{"thread": map[string]any{"id": testThreadID, "permission_posture": "full_access"}}))
	assert.Equal(t, before+1, sink.SettingsRefreshCount())

	// A record with no thread is ignored.
	a.HandleOutput(runtimeEvent(3, "thread.updated", "", "", map[string]any{}))
	assert.Equal(t, before+1, sink.SettingsRefreshCount())
}

func TestAProviderChangeReadsTheNewCatalog(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondJSON(http.MethodGet, routeProviders+"/openai"+providerRouteModels, http.StatusOK, map[string]any{"models": []map[string]any{{"id": "gpt-x"}}})
	a, _ := settledAgent(t, rt)
	a.settings.defaultModel = "deepseek-flash"

	a.HandleOutput(runtimeEvent(1, "thread.updated", "", "", map[string]any{"thread": map[string]any{"id": testThreadID, "model": "gpt-x", "model_provider": "openai", "model_provider_id": "openai"}}))
	require.Eventually(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return len(a.models) == 1 && a.models[0].Id == "gpt-x"
	}, 30*time.Second, 5*time.Millisecond)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Empty(t, a.settings.defaultModel, "the default belonged to the old provider")
	assert.False(t, a.models[0].IsDefault)
}

func TestPostureIsKnown(t *testing.T) {
	t.Parallel()
	for _, posture := range []string{contracts.CodewhalePostureAsk, contracts.CodewhalePostureAutoReview, contracts.CodewhalePostureFullAccess} {
		assert.True(t, postureIsKnown(posture), posture)
	}
	assert.False(t, postureIsKnown(""))
	assert.False(t, postureIsKnown("auto_approve"))
}

func TestOptionsEqual(t *testing.T) {
	t.Parallel()
	assert.True(t, optionsEqual(optionmap.Map{"a": "1"}, optionmap.Map{"a": "1"}))
	assert.False(t, optionsEqual(optionmap.Map{"a": "1"}, optionmap.Map{"a": "2"}))
	assert.False(t, optionsEqual(optionmap.Map{"a": "1"}, optionmap.Map{"a": "1", "b": "2"}))
	assert.True(t, optionsEqual(nil, optionmap.Map{}))
}

// The reply states what the runtime settled on. A value that differs from the
// request stays unresolved, and the surfaced options state the runtime's.
func TestUpdateSettingsLeavesAValueTheRuntimeSettledDifferently(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondJSON(http.MethodPatch, threadRoute, http.StatusOK, threadRecord{
		ID: testThreadID, Model: "deepseek-flash", ModelProvider: "deepseek", ModelProviderID: "deepseek", Mode: "agent", PermissionPosture: "ask",
	})
	a, sink := settledAgent(t, rt)

	result := a.UpdateSettings(optionmap.Map{contracts.CodewhaleOptionMode: "plan"})
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[contracts.CodewhaleOptionMode].State)
	assert.Equal(t, "agent", result.SurfacedOptions[contracts.CodewhaleOptionMode])
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "agent", refresh.Options[contracts.CodewhaleOptionMode], "the refresh states the runtime's value")
	assert.Equal(t, "deepseek-flash", refresh.Model)
	assert.Equal(t, contracts.CodewhalePostureAsk, refresh.PermissionMode)
}

// A model on another provider moves the thread to that provider, so the update
// reads the new provider's catalog before it answers.
func TestUpdateSettingsReadsTheCatalogOfANewProvider(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondJSON(http.MethodPatch, threadRoute, http.StatusOK, threadRecord{
		ID: testThreadID, Model: "gpt-x", ModelProvider: "openai", ModelProviderID: "openai", Mode: "agent", PermissionPosture: "ask",
	})
	catalog := routeProviders + "/openai" + providerRouteModels
	rt.respondJSON(http.MethodGet, catalog, http.StatusOK, map[string]any{"models": []map[string]any{{"id": "gpt-x"}, {"id": "gpt-y"}}})
	a, _ := settledAgent(t, rt)

	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "gpt-x"})
	require.Len(t, rt.requestsTo(http.MethodGet, catalog), 1, "the update reads the catalog before it answers")
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDModel].State)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	require.Len(t, a.models, 2)
	assert.Equal(t, "gpt-x", a.models[0].Id)
	assert.Equal(t, "openai", a.settings.provider)
}

// The effort is LeapMux's own, so a change of the effort alone reaches no route.
func TestUpdateSettingsOfTheEffortAloneSendsNoUpdate(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, sink := settledAgent(t, rt)
	before := sink.SettingsRefreshCount()

	result := a.UpdateSettings(optionmap.Map{agent.OptionIDEffort: "low"})
	assert.Empty(t, rt.requestsTo(http.MethodPatch, threadRoute))
	settlement := result.Settlements[agent.OptionIDEffort]
	assert.Equal(t, agent.OptionSettlementConfirmed, settlement.State)
	require.NotNil(t, settlement.Value)
	assert.Equal(t, "low", *settlement.Value)
	assert.Equal(t, "low", result.SurfacedOptions[agent.OptionIDEffort])
	assert.Equal(t, before+1, sink.SettingsRefreshCount())
}
