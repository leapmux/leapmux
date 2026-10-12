package junie

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	// The source length and provider identify a proxy even when another proxy
	// offers the same model. Keep both wire IDs intact.
	for _, wire := range []string{
		"v1:24:proxy:leapmux-e2e-openai:gpt-5.3-codex",
		"v1:11:proxy:other:gpt-5.3-codex",
	} {
		assert.Equal(t, wire, normalizeJunieModelID(wire))
		assert.Equal(t, wire, junieModelIDForWire(wire))
	}
}

func TestJunieSetModelKeepsProxyProvidersDistinct(t *testing.T) {
	t.Parallel()
	a, requests := newJunieAgentForRPC(t)
	a.SetModelForTest("v1:24:proxy:leapmux-e2e-openai:gpt-5.3-codex")
	const other = "v1:11:proxy:other:gpt-5.3-codex"

	require.NoError(t, a.setJunieModel(other))

	assert.Equal(t, other, a.ModelForTest())
	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, other, recorded[0].Params["value"])
}

func TestJunieSetModelCanLeaveAProxyForABareModel(t *testing.T) {
	t.Parallel()
	a, requests := newJunieAgentForRPC(t)
	const current = "v1:24:proxy:leapmux-e2e-openai:gpt-5.3-codex"
	a.SetModelForTest(current)
	a.SetAvailableModelsForTest([]*agent.ModelInfo{{Id: current, DisplayName: "Proxy model"}})

	require.NoError(t, a.setJunieModel("gpt-5.3-codex"))

	assert.Equal(t, "gpt-5.3-codex", a.ModelForTest())
	recorded := requests()
	require.Len(t, recorded, 1)
	assert.Equal(t, "gpt-5.3-codex", recorded[0].Params["value"])
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
