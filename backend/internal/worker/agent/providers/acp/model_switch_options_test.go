package acp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

// These tests pin what the ACP base does with a server-driven thought level when the
// user changes only the model. The worker merges the stored option values into each
// settings edit, so UpdateSettings receives {model: B, thinking_effort: X}. A server
// can reset the thought level when it changes the model, so the base must write X
// again after the model write, when the new model offers X. A server refuses a write
// to an axis that the new model lacks, and the refusal restarts the agent.

// switchServer returns a fake server with two models. model-a offers low, medium
// and high. The levels of model-b are the caller's.
func switchServer(levelsOfB ...string) *acptest.ModelSwitchServer {
	server := &acptest.ModelSwitchServer{
		EffortID: testThinkingEffort,
		Models:   []string{"model-a", "model-b"},
		Levels:   map[string][]string{"model-a": {"low", "medium", "high"}, "model-b": levelsOfB},
	}
	server.Start("model-a", "high")
	return server
}

// newSwitchAgent attaches a test agent to the server and seeds it with the state of the
// server, as a running session reports it.
func newSwitchAgent(t *testing.T, server *acptest.ModelSwitchServer, notify bool) (*testAgent, *agenttest.Sink, func() []agenttest.RecordedRequest) {
	t.Helper()
	ag, requests := acptest.NewAgentForRPCWithRequestResponder(t, newTestAgent, func(a *testAgent) *Base { return &a.Base }, server.Respond)
	sink := server.Seed(ag, notify)
	return ag, sink, requests
}

func TestACPModelSwitchWritesTheChosenThoughtLevelAfterTheServerReset(t *testing.T) {
	server := switchServer("low", "medium", "high")
	server.Reset = func(string, string) string { return "low" }
	ag, _, requests := newSwitchAgent(t, server, false)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", testThinkingEffort: "high"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=model-b", "thinking_effort=high"}, acptest.ConfigWrites(requests()),
		"the model write comes first, then the write that restores the thought level")
	assert.Equal(t, "high", optionids.GroupByID(ag.OptionGroups(), testThinkingEffort).GetCurrentValue())
	require.NotNil(t, result.Settlements[testThinkingEffort].Value)
	assert.Equal(t, "high", *result.Settlements[testThinkingEffort].Value)
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[testThinkingEffort].State)
}

func TestACPModelSwitchKeepsTheThoughtLevelThatTheServerKept(t *testing.T) {
	server := switchServer("low", "medium", "high")
	ag, _, requests := newSwitchAgent(t, server, false)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", testThinkingEffort: "high"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=model-b"}, acptest.ConfigWrites(requests()),
		"a server that kept the level needs no second write")
	assert.Equal(t, "high", *result.Settlements[testThinkingEffort].Value)
}

func TestACPModelSwitchSkipsAThoughtLevelThatTheNewModelDoesNotOffer(t *testing.T) {
	server := switchServer("low", "medium")
	ag, _, requests := newSwitchAgent(t, server, false)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", testThinkingEffort: "high"})

	require.True(t, result.AppliedLive)
	assert.Equal(t, []string{"model=model-b"}, acptest.ConfigWrites(requests()),
		"the base never pushes a level that the new model dropped")
	require.NotNil(t, result.Settlements[testThinkingEffort].Value)
	assert.Equal(t, "low", *result.Settlements[testThinkingEffort].Value,
		"the settled value is the level that the server runs")
}

// A model with no thought-level axis makes the server drop the option. The base
// must not write the stored level then: the server refuses the write, and the
// refusal makes UpdateSettings ask for a restart of the agent.
func TestACPModelSwitchWritesNoThoughtLevelToAModelWithoutTheAxis(t *testing.T) {
	server := switchServer()
	ag, _, requests := newSwitchAgent(t, server, false)

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", testThinkingEffort: "high"})

	require.True(t, result.AppliedLive, "a switch to a model without the axis needs no restart")
	assert.Equal(t, []string{"model=model-b"}, acptest.ConfigWrites(requests()))
	assert.Nil(t, optionids.GroupByID(ag.OptionGroups(), testThinkingEffort))
	_, surfaced := result.SurfacedOptions[testThinkingEffort]
	assert.False(t, surfaced, "the snapshot omits an axis that the new model does not offer")
}

// A server that sends config_option_update before it replies to the model write makes
// the base persist the level that the server reset. The write that restores the level
// sends no notification, so nothing persists the restored level unless the base does it.
func TestACPModelSwitchPersistsTheRestoredThoughtLevelAfterANotification(t *testing.T) {
	server := switchServer("low", "medium", "high")
	server.Reset = func(string, string) string { return "low" }
	ag, sink, _ := newSwitchAgent(t, server, true)
	before := sink.SettingsRefreshCount()

	result := ag.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", testThinkingEffort: "high"})

	require.True(t, result.AppliedLive)
	require.Equal(t, "high", *result.Settlements[testThinkingEffort].Value)
	require.Greater(t, sink.SettingsRefreshCount(), before, "the notification persists the model change")
	last := sink.LastSettingsRefresh()
	assert.Equal(t, "model-b", last.Model)
	assert.Equal(t, "high", last.Options[testThinkingEffort],
		"the last persisted refresh must carry the level that the session runs, not the level that the server reset to")
}

// A switch that no notification disturbs persists nothing itself: the worker persists the
// settled state. The extra refresh of the previous test must stay conditional.
func TestACPModelSwitchPersistsNothingWithoutANotification(t *testing.T) {
	server := switchServer("low", "medium", "high")
	server.Reset = func(string, string) string { return "low" }
	ag, sink, _ := newSwitchAgent(t, server, false)
	before := sink.SettingsRefreshCount()

	require.True(t, ag.UpdateSettings(map[string]string{agent.OptionIDModel: "model-b", testThinkingEffort: "high"}).AppliedLive)

	assert.Equal(t, before, sink.SettingsRefreshCount())
}
