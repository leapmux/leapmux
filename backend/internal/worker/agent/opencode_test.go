package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func newOpenCodeAgentForRPC(t *testing.T) (*OpenCodeAgent, func() []recordedRequest) {
	return newACPAgentForRPC(t,
		func() *OpenCodeAgent { return &OpenCodeAgent{} },
		func(a *OpenCodeAgent) *acpBase { return &a.acpBase },
	)
}

func newOpenCodeAgentForRPCWithResponder(t *testing.T, respond func(method string) json.RawMessage) (*OpenCodeAgent, func() []recordedRequest) {
	return newACPAgentForRPCWithResponder(t,
		func() *OpenCodeAgent { return &OpenCodeAgent{} },
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
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: OpenCodePrimaryAgentBuild, Name: OpenCodePrimaryAgentBuild},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
		{ID: openCodeHiddenCompaction, Name: openCodeHiddenCompaction},
	}, OpenCodePrimaryAgentPlan, "")
	require.NoError(t, err)

	require.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	require.Len(t, agent.availablePrimaryAgents, 2)
}

func TestOpenCodeConfigurePrimaryAgentsRestoresSavedPrimaryAgent(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPC(t)
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: OpenCodePrimaryAgentBuild, Name: OpenCodePrimaryAgentBuild},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}, OpenCodePrimaryAgentBuild, OpenCodePrimaryAgentPlan)
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
	}, OpenCodePrimaryAgentBuild, "unknown")
	require.NoError(t, err)

	require.Equal(t, OpenCodePrimaryAgentBuild, agent.currentPrimaryAgent)
	require.Empty(t, requests())
}

func TestOpenCodeUpdateSettingsSendsSessionSetMode(t *testing.T) {
	agent, requests := newOpenCodeAgentForRPC(t)
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: OpenCodePrimaryAgentBuild, Name: OpenCodePrimaryAgentBuild, IsDefault: true},
		{Id: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}
	agent.currentPrimaryAgent = OpenCodePrimaryAgentBuild

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		ExtraSettings: map[string]string{OptionGroupKeyPrimaryAgent: OpenCodePrimaryAgentPlan},
	})
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
		{Id: OpenCodePrimaryAgentBuild, Name: "Build", IsDefault: true},
		{Id: OpenCodePrimaryAgentPlan, Name: "Plan"},
	}
	agent.sink = &testSink{}
	agent.reapplySettings = agent.reapplyModelAndPrimaryAgent

	sessionID, ok := agent.ClearContext()
	require.True(t, ok)
	assert.Equal(t, "session-2", sessionID)
	assert.Equal(t, "session-2", agent.sessionID)

	recorded := requests()
	require.Len(t, recorded, 3)
	assert.Equal(t, acpMethodSessionNew, recorded[0].Method)
	assert.Equal(t, acpMethodSessionSetModel, recorded[1].Method)
	assert.Equal(t, "openai/gpt-5", recorded[1].Params["modelId"])
	assert.Equal(t, acpMethodSessionSetMode, recorded[2].Method)
	assert.Equal(t, OpenCodePrimaryAgentPlan, recorded[2].Params["modeId"])
}

func TestOpenCodeCurrentSettingsExposesPrimaryAgent(t *testing.T) {
	agent := &OpenCodeAgent{acpBase: acpBase{model: "openai/gpt-5", currentPrimaryAgent: OpenCodePrimaryAgentPlan}}
	settings := agent.CurrentSettings()
	require.Equal(t, "openai/gpt-5", settings.GetModel())
	require.Equal(t, OpenCodePrimaryAgentPlan, settings.GetExtraSettings()[OptionGroupKeyPrimaryAgent])
}

func TestOpenCodeAvailablePrimaryAgentGroupFallsBack(t *testing.T) {
	agent := &OpenCodeAgent{}
	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 1)
	require.Equal(t, OptionGroupKeyPrimaryAgent, groups[0].Key)
	require.Len(t, groups[0].Options, 2)
	require.Equal(t, OpenCodePrimaryAgentBuild, groups[0].Options[0].Id)
	require.True(t, groups[0].Options[0].IsDefault)
}
