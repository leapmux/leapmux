package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

func newOpenCodeAgentForRPC(t *testing.T) (*OpenCodeAgent, func() []recordedRequest) {
	return newACPAgentForRPC(t,
		func() *OpenCodeAgent {
			a := &OpenCodeAgent{}
			a.modeChannel = modeChannelPrimaryAgent
			a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
			return a
		},
		func(a *OpenCodeAgent) *acpBase { return &a.acpBase },
	)
}

func newOpenCodeAgentForRPCWithResponder(t *testing.T, respond func(method string) json.RawMessage) (*OpenCodeAgent, func() []recordedRequest) {
	return newACPAgentForRPCWithResponder(t,
		func() *OpenCodeAgent {
			a := &OpenCodeAgent{}
			a.modeChannel = modeChannelPrimaryAgent
			a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
			return a
		},
		func(a *OpenCodeAgent) *acpBase { return &a.acpBase },
		respond,
	)
}

func newOpenCodeAgentForRPCWithRequestResponder(t *testing.T, respond func(req recordedRequest) json.RawMessage) (*OpenCodeAgent, func() []recordedRequest) {
	return newACPAgentForRPCWithRequestResponder(t,
		func() *OpenCodeAgent {
			a := &OpenCodeAgent{}
			a.modeChannel = modeChannelPrimaryAgent
			a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
			return a
		},
		func(a *OpenCodeAgent) *acpBase { return &a.acpBase },
		respond,
	)
}

func TestBuildSessionRequest_NewSession(t *testing.T) {
	method, params := buildACPSessionRequest("", "/workspace", acpMethodSessionNew, openCodeMethodSessionResume)
	assert.Equal(t, acpMethodSessionNew, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.NotContains(t, parsed, "sessionId")
}

func TestBuildSessionRequest_ResumeSession(t *testing.T) {
	method, params := buildACPSessionRequest("session-123", "/workspace", acpMethodSessionNew, openCodeMethodSessionResume)
	assert.Equal(t, openCodeMethodSessionResume, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.Equal(t, "session-123", parsed["sessionId"])
}

func TestOpenCodeConfigurePrimaryAgentsUsesSessionCurrentMode(t *testing.T) {
	agent := &OpenCodeAgent{}
	agent.primaryAgentHiddenFilter = isHiddenPrimaryAgent
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: OpenCodePrimaryAgentBuild, Name: OpenCodePrimaryAgentBuild},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
		{ID: openCodeHiddenCompaction, Name: openCodeHiddenCompaction},
	}, OpenCodePrimaryAgentPlan, "", fallbackOpenCodePrimaryAgents(), OpenCodePrimaryAgentBuild)
	require.NoError(t, err)

	require.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	require.Len(t, agent.availablePrimaryAgents, 2)
}

func TestOpenCodeConfigurePrimaryAgentsRestoresSavedPrimaryAgent(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPC(t)
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: OpenCodePrimaryAgentBuild, Name: OpenCodePrimaryAgentBuild},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}, OpenCodePrimaryAgentBuild, OpenCodePrimaryAgentPlan, fallbackOpenCodePrimaryAgents(), OpenCodePrimaryAgentBuild)
	require.NoError(t, err)

	require.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	recorded := requests()
	require.Len(t, recorded, 1)
	require.Equal(t, acpMethodSessionSetMode, recorded[0].Method)
	require.Equal(t, OpenCodePrimaryAgentPlan, recorded[0].Params["modeId"])
}

func TestOpenCodeConfigurePrimaryAgentsIgnoresUnknownSavedPrimaryAgent(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPC(t)
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: OpenCodePrimaryAgentBuild, Name: OpenCodePrimaryAgentBuild},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}, OpenCodePrimaryAgentBuild, "unknown", fallbackOpenCodePrimaryAgents(), OpenCodePrimaryAgentBuild)
	require.NoError(t, err)

	require.Equal(t, OpenCodePrimaryAgentBuild, agent.currentPrimaryAgent)
	require.Empty(t, requests())
}

func TestOpenCodeUpdateSettingsSendsSessionSetMode(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPC(t)
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: OpenCodePrimaryAgentBuild},
		{Id: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild

	updated := agent.UpdateSettings(map[string]string{OptionIDPrimaryAgent: OpenCodePrimaryAgentPlan})
	require.True(t, updated)
	require.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	recorded := requests()
	require.Len(t, recorded, 1)
	require.Equal(t, acpMethodSessionSetMode, recorded[0].Method)
}

func TestOpenCodeClearContextReappliesModelAndPrimaryAgent(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPCWithResponder(t, func(method string) json.RawMessage {
		if method == acpMethodSessionNew {
			return json.RawMessage(`{"sessionId":"session-2"}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "openai/gpt-5"
	agent.currentPrimaryAgent = OpenCodePrimaryAgentPlan
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: "Build"},
		{Id: OpenCodePrimaryAgentPlan, Name: "Plan"},
	}
	agent.sink = &testSink{}
	agent.reapplySettings = agent.reapplyModelAndSecondary

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "session-2", agent.sessionID)

	recorded := requests()
	require.Len(t, recorded, 3)
	assert.Equal(t, acpMethodSessionNew, recorded[0].Method)
	assert.Equal(t, acpMethodSessionSetConfigOption, recorded[1].Method)
	assert.Equal(t, acpConfigOptionIDModel, recorded[1].Params["configId"])
	assert.Equal(t, "openai/gpt-5", recorded[1].Params["value"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[2].Method)
	assert.Equal(t, OpenCodePrimaryAgentPlan, recorded[2].Params["modeId"])
}

func TestOpenCodeCurrentSettingsExposesPrimaryAgent(t *testing.T) {
	agent := &OpenCodeAgent{acpBase: acpBase{modeChannel: modeChannelPrimaryAgent, model: "openai/gpt-5", availableModels: []*ModelInfo{{Id: "openai/gpt-5", DisplayName: "GPT-5"}}, currentPrimaryAgent: OpenCodePrimaryAgentPlan}}
	groups := agent.OptionGroups()
	require.Equal(t, "openai/gpt-5", optionids.CurrentValue(groups, OptionIDModel))
	require.Equal(t, OpenCodePrimaryAgentPlan, optionids.CurrentValue(groups, OptionIDPrimaryAgent))
}

func TestOpenCodeAvailablePrimaryAgentGroupFallsBack(t *testing.T) {
	// configure sets the channel and acpStart seeds the static fallback list from the provider's
	// registration; OptionGroups serves that fallback before the session reports a primary-agent
	// catalog. This test wires the acpBase directly, so it sets secondaryFallback itself.
	agent := &OpenCodeAgent{acpBase: acpBase{
		modeChannel:       modeChannelPrimaryAgent,
		secondaryFallback: fallbackOpenCodePrimaryAgents(),
	}}
	groups := agent.OptionGroups()
	require.Len(t, groups, 1)
	require.Equal(t, OptionIDPrimaryAgent, groups[0].GetId())
	require.Len(t, groups[0].Options, 2)
	require.Equal(t, OpenCodePrimaryAgentBuild, groups[0].Options[0].Id)
	require.Equal(t, OpenCodePrimaryAgentBuild, groups[0].GetDefaultValue())
}

// effortAxisResponse builds a session/set_config_option response whose configOptions carry
// a reasoning-effort (thought_level) axis at the given current value, offering low/medium/high.
func effortAxisResponse(current string) json.RawMessage {
	return json.RawMessage(`{"configOptions":[{"id":"effort","category":"thought_level","name":"Effort","currentValue":"` + current +
		`","options":[{"value":"none","name":"None"},{"value":"low","name":"Low"},{"value":"medium","name":"Medium"},{"value":"high","name":"High"}]}]}`)
}

func openCodeEffortWrites(requests []recordedRequest) []recordedRequest {
	var out []recordedRequest
	for _, r := range requests {
		if r.Method == acpMethodSessionSetConfigOption && r.Params["configId"] == OptionIDEffort {
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
	agent, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req recordedRequest) json.RawMessage {
		if req.Method != acpMethodSessionSetConfigOption {
			return json.RawMessage(`{}`)
		}
		switch req.Params["configId"] {
		case acpConfigOptionIDModel:
			// The new model surfaces an effort axis the daemon leaves at "none".
			return effortAxisResponse("none")
		case OptionIDEffort:
			// Echo whatever level LeapMux pushes back (expected: "high").
			value, _ := req.Params["value"].(string)
			return effortAxisResponse(value)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "anthropic/claude-sonnet-4"
	agent.sink = &testSink{}

	// The prior model surfaced no effort axis.
	require.Nil(t, optionids.GroupByID(agent.OptionGroups(), OptionIDEffort))

	require.True(t, agent.UpdateSettings(map[string]string{OptionIDModel: "openai/gpt-5.5"}))

	// The "none" default was raised by a real set_config_option(effort=high) write.
	writes := openCodeEffortWrites(requests())
	require.Len(t, writes, 1, "a none default must be raised via exactly one effort write")
	assert.Equal(t, "high", writes[0].Params["value"])

	// Both the running session and the surfaced group now agree on "high".
	assert.Equal(t, "high", CurrentOptions(agent.OptionGroups())[OptionIDEffort])
}

// TestOpenCodeModelSwitchRaisesNoneEffortByIDWithoutCategory guards the effort id-fallback in
// the model-switch override: a daemon that surfaces its effort axis by the well-known id
// "effort" but OMITS the ACP `category` ("thought_level") must still have a "none" default
// raised to a real level. Before isEffortConfigOption's id-fallback, acpEffortConfigOption
// matched on category alone, so a category-less effort axis slipped past the override and the
// model stayed reasoning-disabled the instant it was selected.
func TestOpenCodeModelSwitchRaisesNoneEffortByIDWithoutCategory(t *testing.T) {
	// Identical to effortAxisResponse but with NO `category` field -- the axis is recognizable
	// only by its well-known id "effort".
	effortNoCategory := func(current string) json.RawMessage {
		return json.RawMessage(`{"configOptions":[{"id":"effort","name":"Effort","currentValue":"` + current +
			`","options":[{"value":"none","name":"None"},{"value":"low","name":"Low"},{"value":"medium","name":"Medium"},{"value":"high","name":"High"}]}]}`)
	}
	agent, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req recordedRequest) json.RawMessage {
		if req.Method != acpMethodSessionSetConfigOption {
			return json.RawMessage(`{}`)
		}
		switch req.Params["configId"] {
		case acpConfigOptionIDModel:
			// The new model surfaces a category-less effort axis the daemon leaves at "none".
			return effortNoCategory("none")
		case OptionIDEffort:
			value, _ := req.Params["value"].(string)
			return effortNoCategory(value)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "anthropic/claude-sonnet-4"
	agent.sink = &testSink{}

	require.True(t, agent.UpdateSettings(map[string]string{OptionIDModel: "openai/gpt-5.5"}))

	// The "none" default was raised via exactly one effort write even though the axis carried
	// no thought_level category -- matched by id alone.
	writes := openCodeEffortWrites(requests())
	require.Len(t, writes, 1, "a category-less effort axis matched by id is still raised via one effort write")
	assert.Equal(t, "high", writes[0].Params["value"])
	assert.Equal(t, "high", CurrentOptions(agent.OptionGroups())[OptionIDEffort])
}

// TestOpenCodeModelSwitchKeepsReportedEffort verifies the override only rescues a "none"
// default: when the daemon reports a real level on a model switch, that is the daemon's choice
// and must be left untouched (no override write).
func TestOpenCodeModelSwitchKeepsReportedEffort(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req recordedRequest) json.RawMessage {
		if req.Method == acpMethodSessionSetConfigOption && req.Params["configId"] == acpConfigOptionIDModel {
			return effortAxisResponse("low")
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "anthropic/claude-sonnet-4"
	agent.sink = &testSink{}

	require.True(t, agent.UpdateSettings(map[string]string{OptionIDModel: "openai/gpt-5.5"}))

	assert.Empty(t, openCodeEffortWrites(requests()),
		"a real reported level is the daemon's choice and must not trigger an override write")
	assert.Equal(t, "low", CurrentOptions(agent.OptionGroups())[OptionIDEffort])
}

// TestOpenCodeModelSwitchLeavesNoneWhenNoRealLevel verifies the override leaves the daemon's
// value untouched when the axis offers no ranked level above none/off: chooseDefaultEffort has
// nothing to install, so we must not invent one (and must not push an empty write).
func TestOpenCodeModelSwitchLeavesNoneWhenNoRealLevel(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req recordedRequest) json.RawMessage {
		if req.Method == acpMethodSessionSetConfigOption && req.Params["configId"] == acpConfigOptionIDModel {
			// The surfaced effort axis offers only none/off -- no real level to raise to.
			return json.RawMessage(`{"configOptions":[{"id":"effort","category":"thought_level","name":"Effort","currentValue":"none","options":[{"value":"none","name":"None"},{"value":"off","name":"Off"}]}]}`)
		}
		return json.RawMessage(`{}`)
	})
	agent.model = "anthropic/claude-sonnet-4"
	agent.sink = &testSink{}

	require.True(t, agent.UpdateSettings(map[string]string{OptionIDModel: "openai/gpt-5.5"}))

	assert.Empty(t, openCodeEffortWrites(requests()),
		"no ranked level above none means nothing to install -- no override write")
	assert.Equal(t, "none", CurrentOptions(agent.OptionGroups())[OptionIDEffort])
}

// TestOpenCodeExplicitNoneEffortNotRaised locks the override's scope: it fires only on the
// model-write path. A user who DELIBERATELY selects "none" (an explicit effort edit, which
// routes through setConfigOption) must be honored, not bounced back up.
func TestOpenCodeExplicitNoneEffortNotRaised(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPCWithRequestResponder(t, func(req recordedRequest) json.RawMessage {
		if req.Method == acpMethodSessionSetConfigOption && req.Params["configId"] == OptionIDEffort {
			value, _ := req.Params["value"].(string)
			return effortAxisResponse(value)
		}
		return json.RawMessage(`{}`)
	})
	agent.sink = &testSink{}
	// Seed a surfaced effort axis at "high", as a model switch would.
	agent.mu.Lock()
	agent.applyOptionGroupsLocked([]acpConfigOption{{
		ID: OptionIDEffort, Category: "thought_level", Name: "Effort", CurrentValue: "high",
		Options: []acpConfigOptionValue{{Value: "none"}, {Value: "low"}, {Value: "medium"}, {Value: "high"}},
	}})
	agent.mu.Unlock()

	require.True(t, agent.UpdateSettings(map[string]string{OptionIDEffort: "none"}))

	assert.Equal(t, "none", CurrentOptions(agent.OptionGroups())[OptionIDEffort],
		"a deliberate effort selection must be honored")
	require.Len(t, openCodeEffortWrites(requests()), 1,
		"exactly the explicit write -- no override write chasing it back up")
	assert.Equal(t, "none", openCodeEffortWrites(requests())[0].Params["value"])
}
