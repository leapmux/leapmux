package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
)

func newKiloAgentForRPC(t *testing.T) (*KiloAgent, func() []recordedRequest) {
	return newACPAgentForRPC(t,
		func() *KiloAgent {
			a := &KiloAgent{}
			a.modeChannel = modeChannelPrimaryAgent
			a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
			return a
		},
		func(a *KiloAgent) *acpBase { return &a.acpBase },
	)
}

func newKiloAgentForRPCWithResponder(t *testing.T, respond func(method string) json.RawMessage) (*KiloAgent, func() []recordedRequest) {
	return newACPAgentForRPCWithResponder(t,
		func() *KiloAgent {
			a := &KiloAgent{}
			a.modeChannel = modeChannelPrimaryAgent
			a.primaryAgentHiddenFilter = isHiddenPrimaryAgent
			return a
		},
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
	agent.primaryAgentHiddenFilter = isHiddenPrimaryAgent
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
		{ID: openCodeHiddenCompaction, Name: openCodeHiddenCompaction},
	}, OpenCodePrimaryAgentPlan, "", fallbackKiloPrimaryAgents(), KiloPrimaryAgentCode)
	require.NoError(t, err)

	require.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	require.Len(t, agent.availablePrimaryAgents, 2)
}

// A server reporting a hidden pseudo-agent (e.g. "compaction") as the current mode
// must not seed the picker with a selection that has no matching visible option;
// configurePrimaryAgents drops the hidden current and falls back to the first visible
// agent, mirroring the runtime syncConfigOptionSelectLocked guard.
func TestKiloConfigurePrimaryAgentsDropsHiddenCurrentMode(t *testing.T) {
	agent := &KiloAgent{}
	agent.primaryAgentHiddenFilter = isHiddenPrimaryAgent
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
		{ID: openCodeHiddenCompaction, Name: openCodeHiddenCompaction},
	}, openCodeHiddenCompaction, "", fallbackKiloPrimaryAgents(), KiloPrimaryAgentCode)
	require.NoError(t, err)

	require.Equal(t, KiloPrimaryAgentCode, agent.currentPrimaryAgent,
		"the hidden current is dropped; the first visible agent is selected")
	require.Len(t, agent.availablePrimaryAgents, 2)
}

func TestKiloConfigurePrimaryAgentsRestoresSavedPrimaryAgent(t *testing.T) {
	agent, requests := newKiloAgentForRPC(t)
	err := agent.configurePrimaryAgents([]acpModeInfo{
		{ID: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode},
		{ID: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}, KiloPrimaryAgentCode, OpenCodePrimaryAgentPlan, fallbackKiloPrimaryAgents(), KiloPrimaryAgentCode)
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
	}, KiloPrimaryAgentCode, "unknown", fallbackKiloPrimaryAgents(), KiloPrimaryAgentCode)
	require.NoError(t, err)

	require.Equal(t, KiloPrimaryAgentCode, agent.currentPrimaryAgent)
	require.Empty(t, requests())
}

// When the server reports no modes, configurePrimaryAgents must fall back to the
// provider-specific fallback list and default -- proving the shared base method
// honors the fallback/defaultAgent arguments (Kilo's, not OpenCode's).
func TestKiloConfigurePrimaryAgentsFallsBackWhenServerReportsNoModes(t *testing.T) {
	agent := &KiloAgent{}
	err := agent.configurePrimaryAgents(nil, "", "", fallbackKiloPrimaryAgents(), KiloPrimaryAgentCode)
	require.NoError(t, err)

	require.Equal(t, KiloPrimaryAgentCode, agent.currentPrimaryAgent)
	require.Len(t, agent.availablePrimaryAgents, 2)
	assert.Equal(t, KiloPrimaryAgentCode, agent.availablePrimaryAgents[0].GetId())
}

func TestKiloUpdateSettingsSendsSessionSetMode(t *testing.T) {
	agent, requests := newKiloAgentForRPC(t)
	agent.availablePrimaryAgents = []*leapmuxv1.AvailableOption{
		{Id: KiloPrimaryAgentCode, Name: KiloPrimaryAgentCode},
		{Id: OpenCodePrimaryAgentPlan, Name: OpenCodePrimaryAgentPlan},
	}
	agent.currentPrimaryAgent = KiloPrimaryAgentCode

	updated := agent.UpdateSettings(map[string]string{OptionIDPrimaryAgent: OpenCodePrimaryAgentPlan})
	require.True(t, updated)
	require.Equal(t, OpenCodePrimaryAgentPlan, agent.currentPrimaryAgent)
	recorded := requests()
	require.Len(t, recorded, 1)
	require.Equal(t, acpMethodSessionSetMode, recorded[0].Method)
}

func TestKiloCurrentSettingsExposesPrimaryAgent(t *testing.T) {
	agent := &KiloAgent{acpBase: acpBase{modeChannel: modeChannelPrimaryAgent, model: "openai/gpt-5", availableModels: []*ModelInfo{{Id: "openai/gpt-5", DisplayName: "GPT-5"}}, currentPrimaryAgent: OpenCodePrimaryAgentPlan}}
	groups := agent.OptionGroups()
	require.Equal(t, "openai/gpt-5", optionids.CurrentValue(groups, OptionIDModel))
	require.Equal(t, OpenCodePrimaryAgentPlan, optionids.CurrentValue(groups, OptionIDPrimaryAgent))
}

func TestKiloAvailablePrimaryAgentGroupFallsBack(t *testing.T) {
	// configure sets both the channel and the static fallback list; OptionGroups serves
	// that fallback before the session reports a primary-agent catalog.
	agent := &KiloAgent{acpBase: acpBase{
		modeChannel:       modeChannelPrimaryAgent,
		secondaryFallback: fallbackKiloPrimaryAgents(),
	}}
	groups := agent.OptionGroups()
	require.Len(t, groups, 1)
	require.Equal(t, OptionIDPrimaryAgent, groups[0].GetId())
	require.Len(t, groups[0].Options, 2)
	require.Equal(t, KiloPrimaryAgentCode, groups[0].Options[0].Id)
	require.Equal(t, KiloPrimaryAgentCode, groups[0].GetDefaultValue())
}
