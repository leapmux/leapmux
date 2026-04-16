package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func newKiloAgentForRPC(t *testing.T) (*KiloAgent, func() []recordedRequest) {
	return newACPAgentForRPC(t,
		func() *KiloAgent { return &KiloAgent{} },
		func(a *KiloAgent) *acpBase { return &a.acpBase },
	)
}

func newKiloAgentForRPCWithResponder(t *testing.T, respond func(method string) json.RawMessage) (*KiloAgent, func() []recordedRequest) {
	return newACPAgentForRPCWithResponder(t,
		func() *KiloAgent { return &KiloAgent{} },
		func(a *KiloAgent) *acpBase { return &a.acpBase },
		respond,
	)
}

func TestKiloBuildSessionRequest_NewSession(t *testing.T) {
	method, params := buildACPSessionRequest("", "/workspace", acpMethodSessionNew, openCodeMethodSessionResume)
	assert.Equal(t, acpMethodSessionNew, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.NotContains(t, parsed, "sessionId")
}

func TestKiloBuildSessionRequest_ResumeSession(t *testing.T) {
	method, params := buildACPSessionRequest("session-123", "/workspace", acpMethodSessionNew, openCodeMethodSessionResume)
	assert.Equal(t, openCodeMethodSessionResume, method)

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(params, &parsed))
	assert.Equal(t, "/workspace", parsed["cwd"])
	assert.Equal(t, "session-123", parsed["sessionId"])
}

func TestKiloConfigurePrimaryAgentsUsesSessionCurrentMode(t *testing.T) {
	agent := &KiloAgent{}
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
		{ID: openCodeHiddenCompaction, Name: openCodeHiddenCompaction},
	}, OpenCodePrimaryAgentPlan, "")
	require.NoError(t, err)

	require.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	require.Len(t, agent.availablePrimaryAgents, 2)
}

func TestKiloConfigurePrimaryAgentsRestoresSavedPrimaryAgent(t *testing.T) {
	agent, requests := newKiloAgentForRPC(t)
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}, KiloPrimaryAgentCode, OpenCodePrimaryAgentPlan)
	require.NoError(t, err)

	require.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	recorded := requests()
	require.Len(t, recorded, 1)
	require.Equal(t, acpMethodSessionSetMode, recorded[0].Method)
	require.Equal(t, OpenCodePrimaryAgentPlan, recorded[0].Params["modeId"])
}

func TestKiloConfigurePrimaryAgentsIgnoresUnknownSavedPrimaryAgent(t *testing.T) {
	agent, requests := newKiloAgentForRPC(t)
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}, KiloPrimaryAgentCode, "unknown")
	require.NoError(t, err)

	require.Equal(t, KiloPrimaryAgentCode, agent.currentPrimaryAgent)
	require.Empty(t, requests())
}

func TestKiloUpdateSettingsSendsSessionSetMode(t *testing.T) {
	agent, requests := newKiloAgentForRPC(t)
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode, IsDefault: true},
		{Id: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}
	agent.currentPrimaryAgent = KiloPrimaryAgentCode

	updated := agent.UpdateSettings(&leapmuxv1.AgentSettings{
		ExtraSettings: map[string]string{OptionGroupKeyPrimaryAgent: OpenCodePrimaryAgentPlan},
	})
	require.True(t, updated)
	require.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	recorded := requests()
	require.Len(t, recorded, 1)
	require.Equal(t, acpMethodSessionSetMode, recorded[0].Method)
}

func TestKiloCurrentSettingsExposesPrimaryAgent(t *testing.T) {
	agent := &KiloAgent{acpBase: acpBase{model: "openai/gpt-5", currentPrimaryAgent: OpenCodePrimaryAgentPlan}}
	settings := agent.CurrentSettings()
	require.Equal(t, "openai/gpt-5", settings.GetModel())
	require.Equal(t, OpenCodePrimaryAgentPlan, settings.GetExtraSettings()[OptionGroupKeyPrimaryAgent])
}

func TestKiloAvailablePrimaryAgentGroupFallsBack(t *testing.T) {
	agent := &KiloAgent{}
	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 1)
	require.Equal(t, OptionGroupKeyPrimaryAgent, groups[0].Key)
	require.Len(t, groups[0].Options, 2)
	require.Equal(t, KiloPrimaryAgentCode, groups[0].Options[0].Id)
	require.True(t, groups[0].Options[0].IsDefault)
}
