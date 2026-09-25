package grok

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestGrokApprovalModeTableStatesEachWireForm(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		id             string
		yolo, auto     bool
		permissionMode string
	}{
		{id: contracts.GrokApprovalModeAsk, permissionMode: "default"},
		{id: contracts.GrokApprovalModeAuto, auto: true, permissionMode: "auto"},
		{id: contracts.GrokApprovalModeAlwaysApprove, yolo: true, permissionMode: "always-approve"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			spec, ok := approvalModeFor(tc.id)
			require.True(t, ok)
			assert.Equal(t, map[string]any{"yoloMode": tc.yolo, "autoMode": tc.auto}, spec.sessionMeta())

			params, err := approvalNotificationParams(spec)
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, json.Unmarshal(params, &decoded))
			assert.Equal(t, map[string]any{
				"yolo_mode": tc.yolo, "auto_mode": tc.auto, "permission_mode": tc.permissionMode,
				"clientIdentifier": grokClientIdentifier,
			}, decoded)
		})
	}
}

func TestGrokInitialApprovalModeFallsBackToAsk(t *testing.T) {
	t.Parallel()
	assert.Equal(t, contracts.GrokApprovalModeAuto, initialApprovalMode(contracts.GrokApprovalModeAuto))
	assert.Equal(t, contracts.GrokApprovalModeAsk, initialApprovalMode(""), "a launch that states nothing asks")
	assert.Equal(t, contracts.GrokApprovalModeAsk, initialApprovalMode("yolo"), "a mode that no longer exists asks")
}

func TestGrokSessionParamsStateTheApprovalMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode       string
		yolo, auto bool
	}{
		{mode: contracts.GrokApprovalModeAsk},
		{mode: contracts.GrokApprovalModeAuto, auto: true},
		{mode: contracts.GrokApprovalModeAlwaysApprove, yolo: true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			a := &Agent{}
			a.configure(agent.Options{Options: map[string]string{contracts.GrokOptionApprovalMode: tc.mode}})
			params := map[string]any{"cwd": "/w"}
			a.adjustSessionParams("session/new", params)
			assert.Equal(t, map[string]any{"yoloMode": tc.yolo, "autoMode": tc.auto}, params["_meta"])
			assert.Equal(t, "/w", params["cwd"], "the adjustment keeps the base's own params")
		})
	}
}

func TestGrokApprovalModeGroupCarriesTheLiveValue(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{Options: map[string]string{contracts.GrokOptionApprovalMode: contracts.GrokApprovalModeAuto}}, nil)

	group := optionids.GroupByID(a.OptionGroups(), contracts.GrokOptionApprovalMode)
	require.NotNil(t, group)
	assert.Equal(t, contracts.GrokApprovalModeAuto, group.GetCurrentValue())
	assert.Equal(t, contracts.GrokApprovalModeAsk, group.GetDefaultValue())
	assert.True(t, group.GetMutable())
	ids := make([]string, 0, len(group.GetOptions()))
	for _, option := range group.GetOptions() {
		ids = append(ids, option.GetId())
	}
	assert.Equal(t, []string{contracts.GrokApprovalModeAsk, contracts.GrokApprovalModeAuto, contracts.GrokApprovalModeAlwaysApprove}, ids)
}

func TestGrokUpdateSettingsSwitchesTheApprovalModeLive(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)

	result := a.UpdateSettings(map[string]string{contracts.GrokOptionApprovalMode: contracts.GrokApprovalModeAlwaysApprove})

	assert.True(t, result.AppliedLive)
	syncPeer(t, a)
	sent := requestsFor(requests(), grokYoloModeChangedMethod)
	require.Len(t, sent, 1)
	assert.Equal(t, map[string]any{
		"yolo_mode": true, "auto_mode": false, "permission_mode": "always-approve", "clientIdentifier": grokClientIdentifier,
	}, sent[0].Params)
	assert.Equal(t, contracts.GrokApprovalModeAlwaysApprove, agent.CurrentOptions(a.OptionGroups())[contracts.GrokOptionApprovalMode])

	// A new session states the mode that the reader chose.
	params := map[string]any{}
	a.adjustSessionParams("session/new", params)
	assert.Equal(t, map[string]any{"yoloMode": true, "autoMode": false}, params["_meta"])
}

func TestGrokUpdateSettingsLeavesAnUnchangedApprovalModeAlone(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)

	a.UpdateSettings(map[string]string{contracts.GrokOptionApprovalMode: contracts.GrokApprovalModeAsk})
	syncPeer(t, a)

	assert.Empty(t, requestsFor(requests(), grokYoloModeChangedMethod), "the mode already runs")
}

func TestGrokApplyLocalOptionRefusesAnUnknownMode(t *testing.T) {
	t.Parallel()
	a, _, requests := newGrokAgent(t, agent.Options{}, nil)

	handled, err := a.applyLocalOption(contracts.GrokOptionApprovalMode, "yolo")
	assert.True(t, handled)
	require.Error(t, err)
	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), grokYoloModeChangedMethod))
	assert.Equal(t, contracts.GrokApprovalModeAsk, agent.CurrentOptions(a.OptionGroups())[contracts.GrokOptionApprovalMode], "a refused mode changes nothing")

	handled, err = a.applyLocalOption("somethingElse", "x")
	assert.False(t, handled, "an option that Grok's group does not own is not handled")
	assert.NoError(t, err)
}

func TestGrokDecorateModelReadsTheContextWindow(t *testing.T) {
	t.Parallel()
	model := &agent.ModelInfo{Id: "grok-4.6"}
	decorateModel(model, json.RawMessage(`{"totalContextTokens":500000,"agentType":"grok-build-plan"}`))
	assert.Equal(t, int64(500000), model.ContextWindow)

	for _, meta := range []string{``, `null`, `{}`, `{"totalContextTokens":0}`, `{"totalContextTokens":-1}`, `{"totalContextTokens":"big"}`} {
		model := &agent.ModelInfo{Id: "m", ContextWindow: 7}
		decorateModel(model, json.RawMessage(meta))
		assert.Equal(t, int64(7), model.ContextWindow, "meta %q states no window", meta)
	}
}

func TestGrokRegistrationStatesTheStaticGroups(t *testing.T) {
	t.Parallel()
	registration := Registration()
	modes := optionids.GroupByID(registration.OptionGroups, agent.OptionIDPermissionMode)
	require.NotNil(t, modes)
	ids := make([]string, 0, len(modes.GetOptions()))
	for _, option := range modes.GetOptions() {
		ids = append(ids, option.GetId())
	}
	assert.Equal(t, []string{contracts.GrokModeDefault, contracts.GrokModePlan, contracts.GrokModeAsk}, ids)
	require.NotNil(t, optionids.GroupByID(registration.OptionGroups, contracts.GrokOptionApprovalMode))
	assert.Equal(t, contracts.GrokApprovalModeAsk, registration.ProviderOptionDefaults[contracts.GrokOptionApprovalMode])
	assert.Equal(t, contracts.GrokModeDefault, registration.PermissionDefaults.Fallback)
	assert.Equal(t, "LEAPMUX_GROK_DEFAULT_MODEL", registration.EnvModelKey)
	assert.Equal(t, "LEAPMUX_GROK_DEFAULT_EFFORT", registration.EnvEffortKey)
}

// A session request states the safe default when the kept mode is one that the
// table does not know, so no session inherits the user's config.toml.
func TestGrokSessionParamsFallBackToAskForAnUnknownMode(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.approval.current = "retired-mode"
	params := map[string]any{"sessionId": "s"}

	a.adjustSessionParams("session/resume", params)

	assert.Equal(t, map[string]any{"yoloMode": false, "autoMode": false}, params["_meta"])
	assert.Equal(t, "s", params["sessionId"])
}
