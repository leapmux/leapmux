package junie

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

// newJunieAgentForRPC wires a Junie agent to a fake peer that answers `{}` to
// every request, with the hooks Start configures.
func newJunieAgentForRPC(t *testing.T) (*Agent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPC(t,
		func() *Agent {
			a := &Agent{}
			a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
			a.HooksForTest().ModeSetter = a.SetModeViaConfigOption
			a.HooksForTest().ModelIDNormalizer = normalizeJunieModelID
			a.HooksForTest().ModelSetter = a.setJunieModel
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
	)
}

// Junie reports a custom model profile by a decorated wire id and rejects the
// plain profile id on a model write. The two forms must normalize to one id, or
// a startup model apply fails and the worker relaunches the agent to retry it.
func TestJunieModelIDRoundTripsThroughTheDecoratedWireForm(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "custom:mock-model", normalizeJunieModelID("v1:6:custom:custom:mock-model"))
	assert.Equal(t, "v1:6:custom:custom:mock-model", junieModelIDForWire("custom:mock-model"))
	assert.Equal(t, "custom:mock-model", normalizeJunieModelID(junieModelIDForWire("custom:mock-model")),
		"the worker's own form is a fixed point of the round trip")

	// A non-profile id carries no decoration and travels unchanged.
	assert.Equal(t, "claude-sonnet", normalizeJunieModelID("claude-sonnet"))
	assert.Equal(t, "claude-sonnet", junieModelIDForWire("claude-sonnet"))
}

// A live model change writes the decorated id and stores the plain one.
func TestJunieUpdateSettingsWritesTheDecoratedModel(t *testing.T) {
	t.Parallel()

	a, requests := newJunieAgentForRPC(t)
	a.SetModelForTest("claude-sonnet")

	updated := a.UpdateSettings(map[string]string{agent.OptionIDModel: "custom:mock-model"})
	require.True(t, updated.AppliedLive)
	require.Equal(t, "custom:mock-model", a.ModelForTest())

	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, acp.MethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, "v1:6:custom:custom:mock-model", recorded[0].Params["value"])
}

// Junie rejects session/set_mode ("use session/set_config_option with
// configId=\"mode\""), so a live mode change must take the config-option route.
func TestJunieUpdateSettingsSendsModeViaConfigOption(t *testing.T) {
	t.Parallel()

	a, requests := newJunieAgentForRPC(t)
	a.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{
		{Id: contracts.JunieModeDefault, Name: "Default"},
		{Id: contracts.JunieModePlan, Name: "Plan"},
	})
	a.SetPermissionModeForTest(contracts.JunieModeDefault)

	updated := a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.JunieModePlan})
	require.True(t, updated.AppliedLive)
	require.Equal(t, contracts.JunieModePlan, a.PermissionModeForTest())

	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, acp.MethodSessionSetConfigOption, recorded[0].Method,
		"the mode write rides session/set_config_option, never session/set_mode")
	assert.Equal(t, acp.ConfigOptionIDMode, recorded[0].Params["configId"])
	assert.Equal(t, contracts.JunieModePlan, recorded[0].Params["value"])
}
