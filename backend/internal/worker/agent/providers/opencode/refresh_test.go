//go:build unix

package opencode

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeClearContextRefreshesPrimaryAgent(t *testing.T) {
	t.Parallel()

	a, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"},
				"modes":  {"currentModeId": "plan"}
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("openai/gpt-4o")
	a.SetCurrentPrimaryAgentForTest("build")
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)
	a.SetRefreshFromSessionForTest(a.ApplySessionRefreshForTest)

	sessionID, clearErr := a.ClearContext()
	require.NoError(t, clearErr)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "openai/gpt-5", a.ModelForTest())
	assert.Equal(t, "plan", a.CurrentPrimaryAgentForTest())

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "openai/gpt-5", refresh.Model)
	assert.Equal(t, "plan", refresh.Options[agent.OptionIDPrimaryAgent])
}

// On ClearContext a primary-agent provider must drop a modes-channel currentModeId
// that the hidden filter removes (e.g. OpenCode's "compaction"), mirroring the
// handshake guard (configurePrimaryAgents) and the runtime guard
// (syncConfigOptionSelectLocked). Without the drop, the raw currentModeId write
// adopts the hidden pseudo-agent and persists it, seeding the picker with a
// selection that has no visible option. The response carries no configOptions `mode`
// to correct it, isolating the raw-write path.
func TestOpenCodeClearContextDropsHiddenCurrentPrimaryAgent(t *testing.T) {
	t.Parallel()

	ag, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"},
				"modes":  {"currentModeId": "compaction", "availableModes": [{"id":"build"},{"id":"plan"},{"id":"compaction"}]}
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.SetModelForTest("openai/gpt-4o")
	ag.SetCurrentPrimaryAgentForTest("build")
	sink := &agenttest.Sink{}
	ag.SetSinkForTest(agent.NewProviderServices(sink))
	ag.SetReapplySettingsForTest(ag.ReapplyModelAndSecondaryForTest)
	ag.SetRefreshFromSessionForTest(ag.ApplySessionRefreshForTest)

	_, clearErr := ag.ClearContext()
	require.NoError(t, clearErr)

	// The hidden pseudo-agent is dropped; the stored "build" (re-pushed by reapply)
	// is kept rather than overwritten with "compaction".
	assert.Equal(t, "build", ag.CurrentPrimaryAgentForTest(),
		"a hidden pseudo-agent must not be adopted as the current primary agent")
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "build", sink.LastSettingsRefresh().Options[agent.OptionIDPrimaryAgent],
		"the refresh carries the kept primary agent, not the dropped hidden current")
}

// S4: ClearContext refreshes the available-model list (not just the current id)
// from the new session, including the configOptions channel OpenCode/Kilo use.
func TestOpenCodeClearContextRefreshesAvailableModels(t *testing.T) {
	t.Parallel()

	a, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"modes": {"currentModeId": "build"},
				"configOptions": [{"id":"model","currentValue":"openai/gpt-5","options":[
					{"value":"openai/gpt-5","name":"GPT-5"},
					{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"}
				]}]
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("anthropic/claude-sonnet-4")
	a.SetCurrentPrimaryAgentForTest("build")
	a.SetAvailableModelsForTest([]*agent.ModelInfo{{Id: "stale/model"}})
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)
	a.SetRefreshFromSessionForTest(a.ApplySessionRefreshForTest)

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	// The stale handshake-time list is replaced by the new session's models.
	require.Len(t, a.AvailableModelsForTest(), 2)
	assert.Equal(t, "openai/gpt-5", a.AvailableModelsForTest()[0].GetId())
	assert.Equal(t, "anthropic/claude-sonnet-4", a.AvailableModelsForTest()[1].GetId())
	// The model the user had is preserved across the clear (re-pushed by reapply).
	assert.Equal(t, "anthropic/claude-sonnet-4", a.ModelForTest())
}

// On ClearContext a primary-agent provider rebuilds availablePrimaryAgents from the
// NATIVE modes channel (not only the configOptions select), so a new session whose
// agent list grew/shrank is reflected instead of freezing at the handshake list while
// the model list refreshes. Here the new session adds "review" through the modes
// channel and carries no configOptions. [S4]
func TestOpenCodeClearContextRefreshesPrimaryAgentListFromNativeModes(t *testing.T) {
	t.Parallel()

	a, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"},
				"modes":  {"currentModeId": "plan", "availableModes": [{"id":"build"},{"id":"plan"},{"id":"review"}]}
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("openai/gpt-4o")
	a.SetCurrentPrimaryAgentForTest("build")
	a.SetAvailablePrimaryAgentsForTest([]*leapmuxv1.AvailableOption{
		{Id: "build", Name: "Build"},
		{Id: "plan", Name: "Plan"},
	})
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)
	a.SetRefreshFromSessionForTest(a.ApplySessionRefreshForTest)

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	// The stale handshake list is replaced by the new session's native modes channel.
	require.Len(t, a.AvailablePrimaryAgentsForTest(), 3)
	assert.Equal(t, "review", a.AvailablePrimaryAgentsForTest()[2].GetId(),
		"the native modes channel refreshes the primary-agent list on ClearContext")
	// The reported current ("plan") is adopted against the refreshed list.
	assert.Equal(t, "plan", a.CurrentPrimaryAgentForTest())
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "plan", sink.LastSettingsRefresh().Options[agent.OptionIDPrimaryAgent])
}

// On ClearContext a primary-agent provider whose new session DROPS the stored agent
// from the rebuilt list (and reports no valid replacement) re-seeds the current to the
// default-or-first option rather than keep an orphan -- the ClearContext mirror of the
// runtime re-seed, resolving the current the same way the handshake does. [S2]
func TestOpenCodeClearContextReseedsOrphanedPrimaryAgent(t *testing.T) {
	t.Parallel()

	ag, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			// The new session lists [build, review] (dropping the stored "plan") and
			// reports no current agent, with no configOptions to correct it.
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"},
				"modes":  {"availableModes": [{"id":"build"},{"id":"review"}]}
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.SetModelForTest("openai/gpt-4o")
	ag.SetCurrentPrimaryAgentForTest("plan")
	sink := &agenttest.Sink{}
	ag.SetSinkForTest(agent.NewProviderServices(sink))
	ag.SetReapplySettingsForTest(ag.ReapplyModelAndSecondaryForTest)
	ag.SetRefreshFromSessionForTest(ag.ApplySessionRefreshForTest)

	_, clearErr := ag.ClearContext()
	require.NoError(t, clearErr)

	// "plan" is gone from the rebuilt list, so the current re-seeds to the first option.
	require.Len(t, ag.AvailablePrimaryAgentsForTest(), 2)
	assert.Equal(t, "build", ag.CurrentPrimaryAgentForTest(),
		"a stored agent dropped from the new session re-seeds to the first available option")
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "build", sink.LastSettingsRefresh().Options[agent.OptionIDPrimaryAgent])
}

// A ClearContext whose new session reports no primary agent (empty
// currentModeId) while the stored primary agent is empty must not emit a
// primary-agent extras key: applySessionRefresh's snapshot passes
// primaryAgentOptions(""), i.e. nil, so PersistSettingsRefresh keeps the stored
// extras rather than clearing them to "{}" via a map{primaryAgent:""}.
func TestOpenCodeClearContextEmptyPrimaryAgentPreservesExtras(t *testing.T) {
	t.Parallel()

	a, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"}
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("openai/gpt-4o")
	a.SetCurrentPrimaryAgentForTest("")
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)
	a.SetRefreshFromSessionForTest(a.ApplySessionRefreshForTest)

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.NotContains(t, refresh.Options, agent.OptionIDPrimaryAgent,
		"empty primary agent must not emit the extras key, else it clears the stored value")
}

// On ClearContext a primary-agent provider (OpenCode/Kilo) rebuilds
// availablePrimaryAgents and applies the configOptions primary-agent override --
// the mirror of TestACPClearContextAppliesConfigOptionModeOverride for the
// permission-mode side. Without it the picker would freeze at the handshake list and
// a session reporting its current agent only through the configOptions select (empty
// modes-channel currentModeId) would keep the stale selection.
func TestOpenCodeClearContextAppliesConfigOptionPrimaryAgentOverride(t *testing.T) {
	t.Parallel()

	a, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			// The modes channel says "build" but the configOptions `mode` says "plan",
			// and the configOptions list adds an agent ("review") absent from the
			// pre-seeded handshake list.
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "openai/gpt-5"},
				"modes":  {"currentModeId": "build"},
				"configOptions": [{"id":"mode","currentValue":"plan","options":[
					{"value":"build","name":"Build"},
					{"value":"plan","name":"Plan"},
					{"value":"review","name":"Review"}]}]
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("openai/gpt-4o")
	a.SetCurrentPrimaryAgentForTest("build")
	a.SetAvailablePrimaryAgentsForTest([]*leapmuxv1.AvailableOption{
		{Id: "build", Name: "Build"},
		{Id: "plan", Name: "Plan"},
	})
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)
	a.SetRefreshFromSessionForTest(a.ApplySessionRefreshForTest)

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	// The configOptions override wins over the modes-channel "build".
	assert.Equal(t, "plan", a.CurrentPrimaryAgentForTest())
	// The available list is rebuilt from the new session, surfacing "review".
	require.Len(t, a.AvailablePrimaryAgentsForTest(), 3)
	require.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Equal(t, "plan", sink.LastSettingsRefresh().Options[agent.OptionIDPrimaryAgent])
}

// On ClearContext the new session's unmapped config options are surfaced as
// mutable option groups and their values ride along in the settings-refresh
// extras, next to (without clobbering) the primaryAgent key. This is the
// ClearContext seam of option surfacing, mirroring the handshake and runtime seams.
func TestOpenCodeClearContextRefreshesOptions(t *testing.T) {
	t.Parallel()

	a, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"modes":  {"currentModeId": "build"},
				"configOptions": [
					{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]},
					{"id":"model","currentValue":"openai/gpt-5","options":[{"value":"openai/gpt-5","name":"GPT-5"}]},
					{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}
				]
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetCurrentPrimaryAgentForTest("build")
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)
	a.SetRefreshFromSessionForTest(a.ApplySessionRefreshForTest)

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	// The option group is surfaced alongside the mapped primary-agent group.
	groups := a.OptionGroups()
	assert.NotNil(t, optionids.GroupByID(groups, agent.OptionIDPrimaryAgent))
	assert.NotNil(t, optionids.GroupByID(groups, "thoughtLevel"))

	// The refresh carries both the primary agent and the option value.
	require.Equal(t, 1, sink.SettingsRefreshCount())
	extras := sink.LastSettingsRefresh().Options
	assert.Equal(t, "build", extras[agent.OptionIDPrimaryAgent], "the primaryAgent key is not clobbered")
	assert.Equal(t, "high", extras["thoughtLevel"])
}

// A ClearContext whose new session parses but reports no models in either channel
// must leave BOTH availableModels and the remembered models-field catalog at their
// prior values. Resetting modelsFieldInfos to empty while the stale list lingers
// would drop models-field-only entries on the next config_option_update re-union.
func TestOpenCodeClearContextEmptyModelsKeepsCatalog(t *testing.T) {
	t.Parallel()

	a, _ := newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId": "session-2", "modes": {"currentModeId": "build"}}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("anthropic/claude-sonnet-4")
	a.SetCurrentPrimaryAgentForTest("build")
	a.SetAvailableModelsForTest([]*agent.ModelInfo{{Id: "kept/model"}})
	a.SetModelsFieldInfosForTest([]acp.ModelInfo{{ModelID: "kept/model"}})
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)
	a.SetRefreshFromSessionForTest(a.ApplySessionRefreshForTest)

	_, clearErr := a.ClearContext()
	require.NoError(t, clearErr)

	// No models in the response -> both mirrors keep their prior values.
	require.Len(t, a.AvailableModelsForTest(), 1)
	assert.Equal(t, "kept/model", a.AvailableModelsForTest()[0].GetId())
	require.Len(t, a.ModelsFieldInfosForTest(), 1)
	assert.Equal(t, "kept/model", a.ModelsFieldInfosForTest()[0].ModelID)
}
