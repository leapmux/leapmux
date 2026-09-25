package kilo

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
)

func newKiloAgentForRPC(t *testing.T) (*Agent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPC(t,
		func() *Agent {
			a := &Agent{}
			a.HooksForTest().ModeChannel = acp.ModeChannelPrimaryAgent
			a.HooksForTest().PrimaryAgentHiddenFilter = opencode.IsHiddenPrimaryAgent
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
	)
}

func newKiloAgentForRPCWithResponder(t *testing.T, respond func(method string) agenttest.RPCReply) (*Agent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPCWithResponder(t,
		func() *Agent {
			a := &Agent{}
			a.HooksForTest().ModeChannel = acp.ModeChannelPrimaryAgent
			a.HooksForTest().PrimaryAgentHiddenFilter = opencode.IsHiddenPrimaryAgent
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
		respond,
	)
}

// TestKiloClearContextRefreshesPrimaryAgent covers ClearContext adopting the new
// session's model and primary agent (Kilo reports both on session/new) and
// broadcasting one settings refresh carrying them. It lives here, beside the
// os.Pipe-based Kilo RPC helpers, rather than in the unix-tagged ACP refresh suite:
// nothing in it spawns a shell, so it runs on every platform.
func TestKiloClearContextRefreshesPrimaryAgent(t *testing.T) {
	t.Parallel()

	a, _ := newKiloAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{
				"sessionId": "session-2",
				"models": {"currentModelId": "anthropic/claude-sonnet-4"},
				"modes":  {"currentModeId": "code"}
			}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("anthropic/claude-opus-4")
	a.SetCurrentPrimaryAgentForTest("plan")
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)
	a.SetRefreshFromSessionForTest(a.ApplySessionRefreshForTest)

	sessionID, clearErr := a.ClearContext()
	require.NoError(t, clearErr)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "anthropic/claude-sonnet-4", a.ModelForTest())
	assert.Equal(t, "code", a.CurrentPrimaryAgentForTest())

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Equal(t, "anthropic/claude-sonnet-4", refresh.Model)
	assert.Equal(t, "code", refresh.Options[agent.OptionIDPrimaryAgent])
}

func TestKiloBuildSessionRequest_NewSession(t *testing.T) {
	t.Parallel()

	method, params := acp.BuildSessionRequestForTest("", "/workspace", acp.MethodSessionNew, acp.MethodSessionResume)
	assert.Equal(t, acp.MethodSessionNew, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.NotContains(t, parsed, "sessionId")
}

func TestKiloBuildSessionRequest_ResumeSession(t *testing.T) {
	t.Parallel()

	method, params := acp.BuildSessionRequestForTest("session-123", "/workspace", acp.MethodSessionNew, acp.MethodSessionResume)
	assert.Equal(t, acp.MethodSessionResume, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.Equal(t, "session-123", parsed["sessionId"])
}

func TestKiloConfigurePrimaryAgentsUsesSessionCurrentMode(t *testing.T) {
	t.Parallel()

	agent := &Agent{}
	agent.HooksForTest().PrimaryAgentHiddenFilter = opencode.IsHiddenPrimaryAgent
	err := agent.ConfigurePrimaryAgentsForTest([]acp.ModeInfo{
		{ID: PrimaryAgentCode, Name: PrimaryAgentCode},
		{ID: opencode.PrimaryAgentPlan, Name: opencode.PrimaryAgentPlan},
		{ID: opencode.HiddenCompaction, Name: opencode.HiddenCompaction},
	}, opencode.PrimaryAgentPlan, "", fallbackKiloPrimaryAgents(), PrimaryAgentCode)
	require.NoError(t, err)

	require.Equal(t, opencode.PrimaryAgentPlan, agent.CurrentPrimaryAgentForTest())
	require.Len(t, agent.AvailablePrimaryAgentsForTest(), 2)
}

// A server reporting a hidden pseudo-agent (e.g. "compaction") as the current mode
// must not seed the picker with a selection that has no matching visible option;
// configurePrimaryAgents drops the hidden current and falls back to the first visible
// agent, mirroring the runtime syncConfigOptionSelectLocked guard.
func TestKiloConfigurePrimaryAgentsDropsHiddenCurrentMode(t *testing.T) {
	t.Parallel()

	agent := &Agent{}
	agent.HooksForTest().PrimaryAgentHiddenFilter = opencode.IsHiddenPrimaryAgent
	err := agent.ConfigurePrimaryAgentsForTest([]acp.ModeInfo{
		{ID: PrimaryAgentCode, Name: PrimaryAgentCode},
		{ID: opencode.PrimaryAgentPlan, Name: opencode.PrimaryAgentPlan},
		{ID: opencode.HiddenCompaction, Name: opencode.HiddenCompaction},
	}, opencode.HiddenCompaction, "", fallbackKiloPrimaryAgents(), PrimaryAgentCode)
	require.NoError(t, err)

	require.Equal(t, PrimaryAgentCode, agent.CurrentPrimaryAgentForTest(),
		"the hidden current is dropped; the first visible agent is selected")
	require.Len(t, agent.AvailablePrimaryAgentsForTest(), 2)
}

func TestKiloConfigurePrimaryAgentsRestoresSavedPrimaryAgent(t *testing.T) {
	t.Parallel()

	agent, requests := newKiloAgentForRPC(t)
	err := agent.ConfigurePrimaryAgentsForTest([]acp.ModeInfo{
		{ID: PrimaryAgentCode, Name: PrimaryAgentCode},
		{ID: opencode.PrimaryAgentPlan, Name: opencode.PrimaryAgentPlan},
	}, PrimaryAgentCode, opencode.PrimaryAgentPlan, fallbackKiloPrimaryAgents(), PrimaryAgentCode)
	require.NoError(t, err)

	require.Equal(t, opencode.PrimaryAgentPlan, agent.CurrentPrimaryAgentForTest())
	recorded := requests()
	require.Len(t, recorded, 1)
	require.Equal(t, acp.MethodSessionSetMode, recorded[0].Method)
	require.Equal(t, opencode.PrimaryAgentPlan, recorded[0].Params["modeId"])
}

func TestKiloConfigurePrimaryAgentsIgnoresUnknownSavedPrimaryAgent(t *testing.T) {
	t.Parallel()

	agent, requests := newKiloAgentForRPC(t)
	err := agent.ConfigurePrimaryAgentsForTest([]acp.ModeInfo{
		{ID: PrimaryAgentCode, Name: PrimaryAgentCode},
		{ID: opencode.PrimaryAgentPlan, Name: opencode.PrimaryAgentPlan},
	}, PrimaryAgentCode, "unknown", fallbackKiloPrimaryAgents(), PrimaryAgentCode)
	require.NoError(t, err)

	require.Equal(t, PrimaryAgentCode, agent.CurrentPrimaryAgentForTest())
	require.Empty(t, requests())
}

// When the server reports no modes, configurePrimaryAgents must fall back to the
// provider-specific fallback list and default -- proving the shared base method
// honors the fallback/defaultAgent arguments (Kilo's, not OpenCode's).
func TestKiloConfigurePrimaryAgentsFallsBackWhenServerReportsNoModes(t *testing.T) {
	t.Parallel()

	agent := &Agent{}
	err := agent.ConfigurePrimaryAgentsForTest(nil, "", "", fallbackKiloPrimaryAgents(), PrimaryAgentCode)
	require.NoError(t, err)

	require.Equal(t, PrimaryAgentCode, agent.CurrentPrimaryAgentForTest())
	require.Len(t, agent.AvailablePrimaryAgentsForTest(), 2)
	assert.Equal(t, PrimaryAgentCode, agent.AvailablePrimaryAgentsForTest()[0].GetId())
}

func TestKiloUpdateSettingsSendsSessionSetMode(t *testing.T) {
	t.Parallel()

	a, requests := newKiloAgentForRPC(t)
	a.SetAvailablePrimaryAgentsForTest([]*leapmuxv1.AvailableOption{
		{Id: PrimaryAgentCode, Name: PrimaryAgentCode},
		{Id: opencode.PrimaryAgentPlan, Name: opencode.PrimaryAgentPlan},
	})
	a.SetCurrentPrimaryAgentForTest(PrimaryAgentCode)

	updated := a.UpdateSettings(map[string]string{agent.OptionIDPrimaryAgent: opencode.PrimaryAgentPlan})
	require.True(t, updated.AppliedLive)
	require.Equal(t, opencode.PrimaryAgentPlan, a.CurrentPrimaryAgentForTest())
	recorded := requests()
	require.Len(t, recorded, 1)
	require.Equal(t, acp.MethodSessionSetMode, recorded[0].Method)
}

func TestKiloCurrentSettingsExposesPrimaryAgent(t *testing.T) {
	t.Parallel()

	a := &Agent{}
	a.HooksForTest().ModeChannel = acp.ModeChannelPrimaryAgent
	a.SetModelForTest("openai/gpt-5")
	a.SetAvailableModelsForTest([]*agent.ModelInfo{{Id: "openai/gpt-5", DisplayName: "GPT-5"}})
	a.SetCurrentPrimaryAgentForTest(opencode.PrimaryAgentPlan)
	groups := a.OptionGroups()
	require.Equal(t, "openai/gpt-5", optionids.CurrentValue(groups, agent.OptionIDModel))
	require.Equal(t, opencode.PrimaryAgentPlan, optionids.CurrentValue(groups, agent.OptionIDPrimaryAgent))
}

func TestKiloAvailablePrimaryAgentGroupFallsBack(t *testing.T) {
	t.Parallel()

	// configure sets the channel and acp.Start seeds the static fallback list from the provider's
	// registration; OptionGroups serves that fallback before the session reports a primary-agent
	// catalog. This test wires the Base directly, so it sets secondaryFallback itself.
	ag := &Agent{}
	ag.HooksForTest().ModeChannel = acp.ModeChannelPrimaryAgent
	ag.SetSecondaryFallbackForTest(fallbackKiloPrimaryAgents())
	groups := ag.OptionGroups()
	require.Len(t, groups, 1)
	require.Equal(t, agent.OptionIDPrimaryAgent, groups[0].GetId())
	require.Len(t, groups[0].Options, 2)
	require.Equal(t, PrimaryAgentCode, groups[0].Options[0].Id)
	require.Equal(t, PrimaryAgentCode, groups[0].GetDefaultValue())
}
