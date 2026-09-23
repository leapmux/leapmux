package opencode

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
)

func newOpenCodeAgentForRPC(t *testing.T) (*Agent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPC(t,
		func() *Agent {
			a := &Agent{}
			a.HooksForTest().ModeChannel = acp.ModeChannelPrimaryAgent
			a.HooksForTest().PrimaryAgentHiddenFilter = IsHiddenPrimaryAgent
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
	)
}

func newOpenCodeAgentForRPCWithResponder(t *testing.T, respond func(method string) agenttest.RPCReply) (*Agent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPCWithResponder(t,
		func() *Agent {
			a := &Agent{}
			a.HooksForTest().ModeChannel = acp.ModeChannelPrimaryAgent
			a.HooksForTest().PrimaryAgentHiddenFilter = IsHiddenPrimaryAgent
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
		respond,
	)
}

func newOpenCodeAgentForRPCWithRequestResponder(t *testing.T, respond func(req agenttest.RecordedRequest) agenttest.RPCReply) (*Agent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent {
			a := &Agent{}
			a.HooksForTest().ModeChannel = acp.ModeChannelPrimaryAgent
			a.HooksForTest().PrimaryAgentHiddenFilter = IsHiddenPrimaryAgent
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
		respond,
	)
}

func TestBuildSessionRequest_NewSession(t *testing.T) {
	t.Parallel()

	method, params := acp.BuildSessionRequestForTest("", "/workspace", acp.MethodSessionNew, MethodSessionResume)
	assert.Equal(t, acp.MethodSessionNew, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.NotContains(t, parsed, "sessionId")
}

func TestBuildSessionRequest_ResumeSession(t *testing.T) {
	t.Parallel()

	method, params := acp.BuildSessionRequestForTest("session-123", "/workspace", acp.MethodSessionNew, MethodSessionResume)
	assert.Equal(t, MethodSessionResume, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.Equal(t, "session-123", parsed["sessionId"])
}

func TestOpenCodeConfigurePrimaryAgentsUsesSessionCurrentMode(t *testing.T) {
	t.Parallel()

	agent := &Agent{}
	agent.HooksForTest().PrimaryAgentHiddenFilter = IsHiddenPrimaryAgent
	err := agent.ConfigurePrimaryAgentsForTest([]acp.ModeInfo{
		{ID: PrimaryAgentBuild, Name: PrimaryAgentBuild},
		{ID: PrimaryAgentPlan, Name: PrimaryAgentPlan},
		{ID: HiddenCompaction, Name: HiddenCompaction},
	}, PrimaryAgentPlan, "", fallbackOpenCodePrimaryAgents(), PrimaryAgentBuild)
	require.NoError(t, err)

	require.Equal(t, PrimaryAgentPlan, agent.CurrentPrimaryAgentForTest())
	require.Len(t, agent.AvailablePrimaryAgentsForTest(), 2)
}

func TestOpenCodeConfigurePrimaryAgentsRestoresSavedPrimaryAgent(t *testing.T) {
	t.Parallel()

	agent, requests := newOpenCodeAgentForRPC(t)
	err := agent.ConfigurePrimaryAgentsForTest([]acp.ModeInfo{
		{ID: PrimaryAgentBuild, Name: PrimaryAgentBuild},
		{ID: PrimaryAgentPlan, Name: PrimaryAgentPlan},
	}, PrimaryAgentBuild, PrimaryAgentPlan, fallbackOpenCodePrimaryAgents(), PrimaryAgentBuild)
	require.NoError(t, err)

	require.Equal(t, PrimaryAgentPlan, agent.CurrentPrimaryAgentForTest())
	recorded := requests()
	require.Len(t, recorded, 1)
	require.Equal(t, acp.MethodSessionSetMode, recorded[0].Method)
	require.Equal(t, PrimaryAgentPlan, recorded[0].Params["modeId"])
}

func TestOpenCodeConfigurePrimaryAgentsIgnoresUnknownSavedPrimaryAgent(t *testing.T) {
	t.Parallel()

	agent, requests := newOpenCodeAgentForRPC(t)
	err := agent.ConfigurePrimaryAgentsForTest([]acp.ModeInfo{
		{ID: PrimaryAgentBuild, Name: PrimaryAgentBuild},
		{ID: PrimaryAgentPlan, Name: PrimaryAgentPlan},
	}, PrimaryAgentBuild, "unknown", fallbackOpenCodePrimaryAgents(), PrimaryAgentBuild)
	require.NoError(t, err)

	require.Equal(t, PrimaryAgentBuild, agent.CurrentPrimaryAgentForTest())
	require.Empty(t, requests())
}

func TestOpenCodeUpdateSettingsSendsSessionSetMode(t *testing.T) {
	t.Parallel()

	a, requests := newOpenCodeAgentForRPC(t)
	a.SetAvailablePrimaryAgentsForTest([]*leapmuxv1.AvailableOption{
		{Id: PrimaryAgentBuild, Name: PrimaryAgentBuild},
		{Id: PrimaryAgentPlan, Name: PrimaryAgentPlan},
	})
	a.SetCurrentPrimaryAgentForTest(PrimaryAgentBuild)

	updated := a.UpdateSettings(map[string]string{agent.OptionIDPrimaryAgent: PrimaryAgentPlan})
	require.True(t, updated.AppliedLive)
	require.Equal(t, PrimaryAgentPlan, a.CurrentPrimaryAgentForTest())
	recorded := requests()
	require.Len(t, recorded, 1)
	require.Equal(t, acp.MethodSessionSetMode, recorded[0].Method)
}

func TestOpenCodeClearContextReappliesModelAndPrimaryAgent(t *testing.T) {
	t.Parallel()

	a, requests := newOpenCodeAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == acp.MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("openai/gpt-5")
	a.SetCurrentPrimaryAgentForTest(PrimaryAgentPlan)
	a.SetAvailablePrimaryAgentsForTest([]*leapmuxv1.AvailableOption{
		{Id: PrimaryAgentBuild, Name: "Build"},
		{Id: PrimaryAgentPlan, Name: "Plan"},
	})
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.SetReapplySettingsForTest(a.ReapplyModelAndSecondaryForTest)

	sessionID, clearErr := a.ClearContext()
	require.NoError(t, clearErr)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "session-2", a.SessionIDForTest())

	recorded := requests()
	require.Len(t, recorded, 3)
	assert.Equal(t, acp.MethodSessionNew, recorded[0].Method)
	assert.Equal(t, acp.MethodSessionSetConfigOption, recorded[1].Method)
	assert.Equal(t, acp.ConfigOptionIDModel, recorded[1].Params["configId"])
	assert.Equal(t, "openai/gpt-5", recorded[1].Params["value"])
	assert.Equal(t, acp.MethodSessionSetMode, recorded[2].Method)
	assert.Equal(t, PrimaryAgentPlan, recorded[2].Params["modeId"])
}

func TestOpenCodeCurrentSettingsExposesPrimaryAgent(t *testing.T) {
	t.Parallel()

	a := &Agent{}
	a.HooksForTest().ModeChannel = acp.ModeChannelPrimaryAgent
	a.SetModelForTest("openai/gpt-5")
	a.SetAvailableModelsForTest([]*agent.ModelInfo{{Id: "openai/gpt-5", DisplayName: "GPT-5"}})
	a.SetCurrentPrimaryAgentForTest(PrimaryAgentPlan)
	groups := a.OptionGroups()
	require.Equal(t, "openai/gpt-5", optionids.CurrentValue(groups, agent.OptionIDModel))
	require.Equal(t, PrimaryAgentPlan, optionids.CurrentValue(groups, agent.OptionIDPrimaryAgent))
}

func TestOpenCodeAvailablePrimaryAgentGroupFallsBack(t *testing.T) {
	t.Parallel()

	// configure sets the channel and acp.Start seeds the static fallback list from the provider's
	// registration; OptionGroups serves that fallback before the session reports a primary-agent
	// catalog. This test wires the Base directly, so it sets secondaryFallback itself.
	ag := &Agent{}
	ag.HooksForTest().ModeChannel = acp.ModeChannelPrimaryAgent
	ag.SetSecondaryFallbackForTest(fallbackOpenCodePrimaryAgents())
	groups := ag.OptionGroups()
	require.Len(t, groups, 1)
	require.Equal(t, agent.OptionIDPrimaryAgent, groups[0].GetId())
	require.Len(t, groups[0].Options, 2)
	require.Equal(t, PrimaryAgentBuild, groups[0].Options[0].Id)
	require.Equal(t, PrimaryAgentBuild, groups[0].GetDefaultValue())
}

// effortAxisResponse builds a session/set_config_option response whose configOptions carry
// a reasoning-effort (thought_level) axis at the given current value, offering low/medium/high.
func effortAxisResponse(current string) json.RawMessage {
	return json.RawMessage(`{"configOptions":[{"id":"effort","category":"thought_level","name":"Effort","currentValue":"` + current +
		`","options":[{"value":"none","name":"None"},{"value":"low","name":"Low"},{"value":"medium","name":"Medium"},{"value":"high","name":"High"}]}]}`)
}

func openCodeEffortWrites(requests []agenttest.RecordedRequest) []agenttest.RecordedRequest {
	var out []agenttest.RecordedRequest
	for _, r := range requests {
		if r.Method == acp.MethodSessionSetConfigOption && r.Params["configId"] == agent.OptionIDEffort {
			out = append(out, r)
		}
	}
	return out
}

// TestOpenCodeModelSwitchRaisesNoneEffortToHigh verifies the ACP-base override: when a model
// switch surfaces an effort axis the daemon defaults to "none" (reasoning OFF), LeapMux raises
// it to a real level by PUSHING session/set_config_option(effort) -- not just rewriting the
// displayed value -- so the running session and the surfaced group agree. "high" is offered, so
// chooseDefaultEffort picks it.
func TestOpenCodeModelSwitchRaisesNoneEffortToHigh(t *testing.T) {
	t.Parallel()

	ag, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method != acp.MethodSessionSetConfigOption {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		switch req.Params["configId"] {
		case acp.ConfigOptionIDModel:
			// The new model surfaces an effort axis the daemon leaves at "none".
			return agenttest.RPCReply{Result: effortAxisResponse("none")}
		case agent.OptionIDEffort:
			// Echo whatever level LeapMux pushes back (expected: "high").
			value, _ := req.Params["value"].(string)
			return agenttest.RPCReply{Result: effortAxisResponse(value)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.SetModelForTest("anthropic/claude-sonnet-4")
	ag.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))

	// The prior model surfaced no effort axis.
	require.Nil(t, optionids.GroupByID(ag.OptionGroups(), agent.OptionIDEffort))

	require.True(t, ag.UpdateSettings(map[string]string{agent.OptionIDModel: "openai/gpt-5.5"}).AppliedLive)

	// The "none" default was raised by a real set_config_option(effort=high) write.
	writes := openCodeEffortWrites(requests())
	require.Len(t, writes, 1, "a none default must be raised via exactly one effort write")
	assert.Equal(t, "high", writes[0].Params["value"])

	// Both the running session and the surfaced group now agree on "high".
	assert.Equal(t, "high", agent.CurrentOptions(ag.OptionGroups())[agent.OptionIDEffort])
}

// TestOpenCodeModelSwitchRaisesNoneEffortByIDWithoutCategory guards the effort id-fallback in
// the model-switch override: a daemon that surfaces its effort axis by the well-known id
// "effort" but OMITS the ACP `category` ("thought_level") must still have a "none" default
// raised to a real level. Before isEffortConfigOption's id-fallback, acpEffortConfigOption
// matched on category alone, so a category-less effort axis slipped past the override and the
// model stayed reasoning-disabled the instant it was selected.
func TestOpenCodeModelSwitchRaisesNoneEffortByIDWithoutCategory(t *testing.T) {
	t.Parallel()

	// Identical to effortAxisResponse but with NO `category` field -- the axis is recognizable
	// only by its well-known id "effort".
	effortNoCategory := func(current string) json.RawMessage {
		return json.RawMessage(`{"configOptions":[{"id":"effort","name":"Effort","currentValue":"` + current +
			`","options":[{"value":"none","name":"None"},{"value":"low","name":"Low"},{"value":"medium","name":"Medium"},{"value":"high","name":"High"}]}]}`)
	}
	ag, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method != acp.MethodSessionSetConfigOption {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		switch req.Params["configId"] {
		case acp.ConfigOptionIDModel:
			// The new model surfaces a category-less effort axis the daemon leaves at "none".
			return agenttest.RPCReply{Result: effortNoCategory("none")}
		case agent.OptionIDEffort:
			value, _ := req.Params["value"].(string)
			return agenttest.RPCReply{Result: effortNoCategory(value)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.SetModelForTest("anthropic/claude-sonnet-4")
	ag.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))

	require.True(t, ag.UpdateSettings(map[string]string{agent.OptionIDModel: "openai/gpt-5.5"}).AppliedLive)

	// The "none" default was raised via exactly one effort write even though the axis carried
	// no thought_level category -- matched by id alone.
	writes := openCodeEffortWrites(requests())
	require.Len(t, writes, 1, "a category-less effort axis matched by id is still raised via one effort write")
	assert.Equal(t, "high", writes[0].Params["value"])
	assert.Equal(t, "high", agent.CurrentOptions(ag.OptionGroups())[agent.OptionIDEffort])
}

// TestOpenCodeModelSwitchKeepsReportedEffort verifies the override only rescues a "none"
// default: when the daemon reports a real level on a model switch, that is the daemon's choice
// and must be left untouched (no override write).
func TestOpenCodeModelSwitchKeepsReportedEffort(t *testing.T) {
	t.Parallel()

	ag, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == acp.MethodSessionSetConfigOption && req.Params["configId"] == acp.ConfigOptionIDModel {
			return agenttest.RPCReply{Result: effortAxisResponse("low")}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.SetModelForTest("anthropic/claude-sonnet-4")
	ag.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))

	require.True(t, ag.UpdateSettings(map[string]string{agent.OptionIDModel: "openai/gpt-5.5"}).AppliedLive)

	assert.Empty(t, openCodeEffortWrites(requests()),
		"a real reported level is the daemon's choice and must not trigger an override write")
	assert.Equal(t, "low", agent.CurrentOptions(ag.OptionGroups())[agent.OptionIDEffort])
}

// TestOpenCodeModelSwitchLeavesNoneWhenNoRealLevel verifies the override leaves the daemon's
// value untouched when the axis offers no ranked level above none/off: chooseDefaultEffort has
// nothing to install, so we must not invent one (and must not push an empty write).
func TestOpenCodeModelSwitchLeavesNoneWhenNoRealLevel(t *testing.T) {
	t.Parallel()

	a, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == acp.MethodSessionSetConfigOption && req.Params["configId"] == acp.ConfigOptionIDModel {
			// The surfaced effort axis offers only none/off -- no real level to raise to.
			return agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"effort","category":"thought_level","name":"Effort","currentValue":"none","options":[{"value":"none","name":"None"},{"value":"off","name":"Off"}]}]}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetModelForTest("anthropic/claude-sonnet-4")
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))

	require.True(t, a.UpdateSettings(map[string]string{agent.OptionIDModel: "openai/gpt-5.5"}).AppliedLive)

	assert.Empty(t, openCodeEffortWrites(requests()),
		"no ranked level above none means nothing to install -- no override write")
	assert.Equal(t, "none", agent.CurrentOptions(a.OptionGroups())[agent.OptionIDEffort])
}

// TestOpenCodeExplicitNoneEffortNotRaised locks the override's scope: it fires only on the
// model-write path. A user who DELIBERATELY selects "none" (an explicit effort edit, which
// routes through setConfigOption) must be honored, not bounced back up.
func TestOpenCodeExplicitNoneEffortNotRaised(t *testing.T) {
	t.Parallel()

	ag, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == acp.MethodSessionSetConfigOption && req.Params["configId"] == agent.OptionIDEffort {
			value, _ := req.Params["value"].(string)
			return agenttest.RPCReply{Result: effortAxisResponse(value)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	ag.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	// Seed a surfaced effort axis at "high", as a model switch would.
	ag.Mu.Lock()
	ag.ApplyOptionGroupsLockedForTest([]acp.ConfigOption{{
		ID: agent.OptionIDEffort, Category: "thought_level", Name: "Effort", CurrentValue: "high",
		Options: []acp.ConfigOptionValue{{Value: "none"}, {Value: "low"}, {Value: "medium"}, {Value: "high"}},
	}})
	ag.Mu.Unlock()

	require.True(t, ag.UpdateSettings(map[string]string{agent.OptionIDEffort: "none"}).AppliedLive)

	assert.Equal(t, "none", agent.CurrentOptions(ag.OptionGroups())[agent.OptionIDEffort],
		"a deliberate effort selection must be honored")
	require.Len(t, openCodeEffortWrites(requests()), 1,
		"exactly the explicit write -- no override write chasing it back up")
	assert.Equal(t, "none", openCodeEffortWrites(requests())[0].Params["value"])
}
