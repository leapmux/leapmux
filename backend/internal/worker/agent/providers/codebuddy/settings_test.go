package codebuddy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCodebuddyThinkingGroup(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	group := optionids.GroupByID(a.OptionGroups(), "thinking")
	require.NotNil(t, group)
	assert.Equal(t, "Extended Thinking", group.GetLabel())
	assert.Equal(t, "auto", group.GetCurrentValue())
	assert.Equal(t, "auto", group.GetDefaultValue())
	assert.True(t, group.GetMutable())
	var choices []string
	for _, option := range group.GetOptions() {
		choices = append(choices, option.GetId())
	}
	assert.Equal(t, []string{"auto", "false", "true"}, choices)
	assert.Equal(t, "auto", a.SettingsSnapshot().SurfacedOptions["thinking"])
}

func TestCodebuddyThinkingUsesNativeConfiguration(t *testing.T) {
	t.Parallel()
	a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(`{"updated":{"alwaysThinkingEnabled":false}}`))
	result := a.UpdateSettings(map[string]string{"thinking": "false"})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, "false", result.ConfirmedOptions()["thinking"])
	assert.Equal(t, "false", a.SettingsSnapshot().SurfacedOptions["thinking"])
	var sent struct {
		Type    string `json:"type"`
		Request struct {
			Subtype string                     `json:"subtype"`
			Config  map[string]json.RawMessage `json:"config"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal([]byte(peer.String()), &sent))
	assert.Equal(t, "control_request", sent.Type)
	assert.Equal(t, "set_config", sent.Request.Subtype)
	require.Len(t, sent.Request.Config, 1)
	var enabled bool
	require.NoError(t, json.Unmarshal(sent.Request.Config["alwaysThinkingEnabled"], &enabled))
	assert.False(t, enabled, "false must be a native boolean, not an absent setting")
}

func TestCodebuddyThinkingSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		current string
		options map[string]string
		want    string
		restart bool
		write   bool
	}{
		{name: "enable", current: "auto", options: map[string]string{"thinking": "true"}, want: "true", write: true},
		{name: "disable", current: "true", options: map[string]string{"thinking": "false"}, want: "false", write: true},
		{name: "reenable", current: "false", options: map[string]string{"thinking": "true"}, want: "true", write: true},
		{name: "unchanged on", current: "true", options: map[string]string{"thinking": "true"}, want: "true"},
		{name: "unchanged auto", current: "auto", options: map[string]string{"thinking": "auto"}, want: "auto"},
		{name: "restore profile from on", current: "true", options: map[string]string{"thinking": "auto"}, want: "true", restart: true},
		{name: "restore profile from off", current: "false", options: map[string]string{"thinking": "auto"}, want: "false", restart: true},
		{name: "empty edit", current: "true", options: map[string]string{"thinking": ""}, want: "true"},
		{name: "omitted", current: "true", options: map[string]string{}, want: "true"},
		{name: "numeric", current: "auto", options: map[string]string{"thinking": "1"}, want: "auto"},
		{name: "zero", current: "true", options: map[string]string{"thinking": "0"}, want: "true"},
		{name: "wrong case", current: "auto", options: map[string]string{"thinking": "TRUE"}, want: "auto"},
		{name: "whitespace", current: "auto", options: map[string]string{"thinking": " "}, want: "auto"},
		{name: "invalid combined edit", current: "auto", options: map[string]string{"thinking": "bad", OptionOutputStyle: "Concise", agent.OptionIDEffort: "low", agent.OptionIDModel: "alternate"}, want: "auto"},
		{name: "style restart", current: "true", options: map[string]string{"thinking": "false", OptionOutputStyle: "Concise"}, want: "true", restart: true},
		{name: "effort restart", current: "true", options: map[string]string{"thinking": "false", agent.OptionIDEffort: "low"}, want: "true", restart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enabled := tc.options["thinking"] == "true"
			body, err := json.Marshal(map[string]any{"updated": map[string]bool{"alwaysThinkingEnabled": enabled}})
			require.NoError(t, err)
			a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(string(body)))
			a.thinkingChoice = tc.current
			a.outputStyle = "Default"
			a.outputStyles = []string{"Default", "Concise"}
			a.model = "primary"
			a.effort = "high"
			a.opts.Options = map[string]string{agent.OptionIDEffort: "high"}
			result := a.UpdateSettings(tc.options)
			assert.Equal(t, !tc.restart, result.AppliedLive)
			assert.Equal(t, tc.want, a.SettingsSnapshot().SurfacedOptions[OptionThinking])
			assert.Equal(t, "Default", a.outputStyle)
			assert.Equal(t, "high", a.effort)
			assert.Equal(t, "primary", a.model)
			if tc.restart {
				assert.Equal(t, agent.RestartRequiredSettings(tc.options), result)
			} else if _, requested := tc.options["thinking"]; requested {
				assert.Equal(t, tc.want, result.ConfirmedOptions()[OptionThinking])
			}
			if tc.write {
				assert.Contains(t, peer.String(), `"subtype":"set_config"`)
			} else {
				assert.Empty(t, peer.String())
			}
		})
	}
}

func TestCodebuddyThinkingRequiresExactNativeConfirmation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		answer func(string) string
	}{
		{name: "native refusal", answer: codebuddyControlFailure("The configuration request failed.")},
		{name: "per-key refusal", answer: codebuddyControlAnswer(`{"updated":{"alwaysThinkingEnabled":false},"errors":{"alwaysThinkingEnabled":"The setting is locked."}}`)},
		{name: "missing update", answer: codebuddyControlAnswer(`{}`)},
		{name: "empty update", answer: codebuddyControlAnswer(`{"updated":{}}`)},
		{name: "null update", answer: codebuddyControlAnswer(`{"updated":{"alwaysThinkingEnabled":null}}`)},
		{name: "string instead of boolean", answer: codebuddyControlAnswer(`{"updated":{"alwaysThinkingEnabled":"false"}}`)},
		{name: "contradictory boolean", answer: codebuddyControlAnswer(`{"updated":{"alwaysThinkingEnabled":true}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _ := newCodebuddySteerAgent(t, nil, tc.answer)
			result := a.UpdateSettings(map[string]string{OptionThinking: codebuddyThinkingOff})
			assert.False(t, result.AppliedLive, "an unconfirmed valid choice requires the existing restart fallback")
			assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[OptionThinking].State)
			assert.Equal(t, codebuddyThinkingAuto, result.SurfacedOptions[OptionThinking])
			assert.Equal(t, codebuddyThinkingAuto, a.SettingsSnapshot().SurfacedOptions[OptionThinking])
		})
	}
}

func TestCodebuddyThinkingTimeoutKeepsItsConfirmedPreference(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	a, _ := newCodebuddySteerAgent(t, clock, nil)
	a.opts.APITimeout = time.Minute
	newTimer := clock.Trap().NewTimer()
	stopTimer := clock.Trap().TimerStop()
	t.Cleanup(func() { newTimer.Close(); stopTimer.Close() })
	ctx := testutil.DeadlineContext(t)
	done := make(chan agent.SettingsApplyResult, 1)
	go func() { done <- a.UpdateSettings(map[string]string{OptionThinking: codebuddyThinkingOff}) }()
	delay := testutil.WaitForTimer(t, ctx, newTimer)
	assert.Equal(t, time.Minute, delay)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, stopTimer)
	select {
	case result := <-done:
		assert.False(t, result.AppliedLive)
		assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[OptionThinking].State)
		assert.Equal(t, codebuddyThinkingAuto, result.SurfacedOptions[OptionThinking])
	case <-ctx.Done():
		t.Fatal("the thinking update did not end after its timeout")
	}
}

func TestCodebuddyThinkingIgnoresConfirmationAfterStop(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	processDone := make(chan struct{})
	a := newOfflineAgentWithRuntime(t, sink, nil, processDone)
	t.Cleanup(func() {
		select {
		case <-processDone:
		default:
			close(processDone)
		}
		a.Stop()
	})
	requests := make(chan string, 1)
	peer := &codebuddyControlPeer{answer: func(id string) string { requests <- id; return "" }}
	peer.agent.Store(a)
	a.SetStdinForTest(peer)
	done := make(chan agent.SettingsApplyResult, 1)
	go func() { done <- a.UpdateSettings(map[string]string{OptionThinking: codebuddyThinkingOff}) }()
	ctx := testutil.DeadlineContext(t)
	var id string
	select {
	case id = <-requests:
	case <-ctx.Done():
		t.Fatal("the thinking request did not arrive")
	}
	close(processDone)
	a.Stop()
	a.HandleOutput([]byte(codebuddyControlAnswer(`{"updated":{"alwaysThinkingEnabled":false}}`)(id)))
	select {
	case result := <-done:
		assert.False(t, result.AppliedLive)
		assert.Equal(t, codebuddyThinkingAuto, result.SurfacedOptions[OptionThinking])
	case <-ctx.Done():
		t.Fatal("the thinking update did not end after stop")
	}
	assert.Equal(t, codebuddyThinkingAuto, a.SettingsSnapshot().SurfacedOptions[OptionThinking])
}

func TestCodebuddyThinkingAndCatalogRepliesStaySeparate(t *testing.T) {
	t.Parallel()
	requests := make(chan string, 2)
	a, peer := newCodebuddySteerAgent(t, nil, func(id string) string { requests <- id; return "" })
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.outputStyle = "Default"
	a.outputStyles = []string{"Default"}
	thinkingDone := make(chan agent.SettingsApplyResult, 1)
	catalogDone := make(chan struct{})
	go func() { thinkingDone <- a.UpdateSettings(map[string]string{OptionThinking: codebuddyThinkingOff}) }()
	go func() { a.refreshOutputStyleCatalog(); close(catalogDone) }()
	ctx := testutil.DeadlineContext(t)
	for range 2 {
		select {
		case <-requests:
		case <-ctx.Done():
			t.Fatal("both native requests did not arrive")
		}
	}
	frames := strings.Split(strings.TrimSpace(peer.String()), "\n")
	require.Len(t, frames, 2)
	for index := len(frames) - 1; index >= 0; index-- {
		var frame struct {
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		require.NoError(t, json.Unmarshal([]byte(frames[index]), &frame))
		switch frame.Request.Subtype {
		case "initialize":
			a.HandleOutput([]byte(codebuddyControlAnswer(`{"output_style":"Explanatory","available_output_styles":["Default","Concise"]}`)(frame.RequestID)))
		case "set_config":
			a.HandleOutput([]byte(codebuddyControlAnswer(`{"updated":{"alwaysThinkingEnabled":false}}`)(frame.RequestID)))
		default:
			t.Fatalf("unexpected native request: %s", frame.Request.Subtype)
		}
	}
	select {
	case result := <-thinkingDone:
		require.True(t, result.AppliedLive)
		assert.Equal(t, codebuddyThinkingOff, result.ConfirmedOptions()[OptionThinking])
	case <-ctx.Done():
		t.Fatal("the thinking reply was not delivered")
	}
	select {
	case <-catalogDone:
	case <-ctx.Done():
		t.Fatal("the catalog reply was not delivered")
	}
	assert.Equal(t, codebuddyThinkingOff, a.SettingsSnapshot().SurfacedOptions[OptionThinking])
	assert.Equal(t, "Default", a.SettingsSnapshot().SurfacedOptions[OptionOutputStyle])
	assert.Equal(t, []string{"Default", "Concise"}, a.outputStyles)
	assert.Equal(t, 1, sink.SettingsRefreshCount())
	assert.Zero(t, sink.StatusActiveCount())
}

func TestCodebuddyPermissionModeTargetsThePinnedSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		current   string
		requested string
	}{
		{name: "same default", current: "default", requested: "default"},
		{name: "same bypass", current: "bypassPermissions", requested: "bypassPermissions"},
		{name: "same plan", current: "plan", requested: "plan"},
		{name: "changed mode", current: "default", requested: "plan"},
		{name: "unknown empty mode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var peer *codebuddyControlPeer
			a, peer := newCodebuddySteerAgent(t, nil, func(id string) string {
				var sent struct {
					Request struct {
						SessionID string `json:"session_id"`
						Mode      string `json:"mode"`
					} `json:"request"`
				}
				if json.Unmarshal([]byte(peer.String()), &sent) != nil {
					return codebuddyControlFailure("Invalid permission request")(id)
				}
				if sent.Request.Mode == "" {
					return codebuddyControlFailure("Invalid permission mode")(id)
				}
				if sent.Request.SessionID != "session-1" {
					return codebuddyControlFailure("Session not found: current")(id)
				}
				body, err := json.Marshal(map[string]string{"session_id": sent.Request.SessionID, "mode": sent.Request.Mode})
				if err != nil {
					return codebuddyControlFailure("Could not encode the mode confirmation")(id)
				}
				return codebuddyControlAnswer(string(body))(id)
			})
			a.permissionMode = tc.current
			err := a.applyPermissionMode(tc.requested)
			if tc.requested != "" {
				require.NoError(t, err)
				assert.Equal(t, tc.requested, a.permissionMode)
			} else {
				require.ErrorContains(t, err, "Invalid permission mode")
				assert.Equal(t, tc.current, a.permissionMode)
			}
			assert.Contains(t, peer.String(), `"session_id":"session-1"`)
		})
	}
}

func TestCodebuddyPermissionModeRequiresSessionIdentity(t *testing.T) {
	t.Parallel()
	a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(`{"mode":"bypassPermissions"}`))
	a.sessionID = ""
	require.ErrorContains(t, a.applyPermissionMode("bypassPermissions"), "session")
	assert.Empty(t, peer.String())
}

func TestCodebuddyModeConfirmationKeepsRejectedInputRestart(t *testing.T) {
	t.Parallel()
	a, _ := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(`{"mode":"plan"}`))
	a.permissionMode = "plan"
	a.opts.Options = map[string]string{agent.OptionIDPermissionMode: "plan"}
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"content":[{"type":"tool_use","id":"rejected-plan","name":"ExitPlanMode"}]}}`))
	a.HandleOutput([]byte(`{"type":"user","session_id":"session-1","message":{"content":[{"type":"tool_result","tool_use_id":"rejected-plan","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this plan yet. Please revise."}]}]}}`))
	require.True(t, a.NativeTurnRestartRequired())
	require.NoError(t, a.applyPermissionMode("plan"))
	assert.True(t, a.NativeTurnRestartRequired(), "a mode confirmation does not restore the rejected native input loop")
}

func TestCodebuddySettingsOfferTheNativeModelCatalog(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.model = "custom-local:primary"
	a.models = []codebuddyModelInfo{
		{ID: "custom-local:primary", Name: "Primary"},
		{ID: "custom-local:alternate", Name: "Alternate"},
	}
	group := optionids.GroupByID(a.OptionGroups(), agent.OptionIDModel)
	require.NotNil(t, group)
	assert.Equal(t, "custom-local:primary", group.GetCurrentValue())
	require.Len(t, group.GetOptions(), 2)
	assert.Equal(t, "custom-local:primary", group.GetOptions()[0].GetId())
	assert.Equal(t, "custom-local:alternate", group.GetOptions()[1].GetId())
	assert.Equal(t, "custom-local:primary", a.SettingsSnapshot().SurfacedOptions[agent.OptionIDModel])
}

func TestCodebuddyModelGroupKeepsTheCurrentModelOutsideTheCatalog(t *testing.T) {
	t.Parallel()
	group := codebuddyModelGroup([]codebuddyModelInfo{{ID: "custom-local:alternate", Name: "Alternate"}}, "custom-local:primary")
	require.NotNil(t, group)
	require.Len(t, group.GetOptions(), 2)
	assert.Equal(t, "custom-local:primary", group.GetOptions()[0].GetId())
	assert.Equal(t, "custom-local:alternate", group.GetOptions()[1].GetId())
	assert.Equal(t, "custom-local:primary", group.GetDefaultValue())
	assert.Nil(t, codebuddyModelGroup(nil, ""))
}

func TestCodebuddyModelCatalogDropsEmptyAndDuplicateIds(t *testing.T) {
	t.Parallel()
	models := normalizeCodebuddyModels([]codebuddyModelInfo{
		{ID: " ", Name: "Blank"},
		{ID: " custom-local:primary ", Name: "Primary"},
		{ID: "custom-local:primary", Name: "Duplicate"},
		{ID: "custom-local:alternate", Name: "Alternate"},
	})
	assert.Equal(t, []codebuddyModelInfo{
		{ID: "custom-local:primary", Name: "Primary"},
		{ID: "custom-local:alternate", Name: "Alternate"},
	}, models)
}

func TestCodebuddyModelChangeRejectsAnUnavailableModel(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.model = "custom-local:primary"
	a.models = []codebuddyModelInfo{{ID: "custom-local:primary"}, {ID: "custom-local:alternate"}}

	result := a.UpdateSettings(map[string]string{agent.OptionIDModel: "custom-local:missing"})
	assert.False(t, result.AppliedLive)
	assert.Equal(t, agent.OptionSettlementUnresolved, result.Settlements[agent.OptionIDModel].State)
	assert.Equal(t, "custom-local:primary", result.SurfacedOptions[agent.OptionIDModel])
}

func TestCodebuddyEffortRequiresResumeRestart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		options map[string]string
	}{
		{name: "reasoning effort", options: map[string]string{agent.OptionIDEffort: "low"}},
		{name: "effort and model", options: map[string]string{agent.OptionIDEffort: "low", agent.OptionIDModel: "custom-local:alternate"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.model = "custom-local:primary"
			a.models = []codebuddyModelInfo{{ID: "custom-local:primary"}, {ID: "custom-local:alternate"}}
			a.opts = agent.Options{Options: map[string]string{
				agent.OptionIDModel:          "custom-local:primary",
				agent.OptionIDPermissionMode: contracts.CodebuddyModeBypassPermissions,
				agent.OptionIDEffort:         "high",
			}}
			a.permissionMode = contracts.CodebuddyModeBypassPermissions
			a.effort = "high"

			result := a.UpdateSettings(tc.options)
			assert.Equal(t, agent.RestartRequiredSettings(tc.options), result)
		})
	}
}

func TestCodebuddyUnchangedEffortIsConfirmedLive(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	a.opts = agent.Options{Options: map[string]string{agent.OptionIDEffort: "high"}}
	a.effort = "high"

	result := a.UpdateSettings(map[string]string{agent.OptionIDEffort: "high"})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[agent.OptionIDEffort].State)
	require.NotNil(t, result.Settlements[agent.OptionIDEffort].Value)
	assert.Equal(t, "high", *result.Settlements[agent.OptionIDEffort].Value)
}

// get_available_modes omits Auto, but CodeBuddy accepts it, so the Permissions
// group offers it beside the four advertised modes.
func TestCodebuddyPermissionModesOfferAuto(t *testing.T) {
	t.Parallel()
	group := codebuddyPermissionModeGroup(contracts.CodebuddyModeAuto)
	var ids []string
	for _, option := range group.GetOptions() {
		ids = append(ids, option.GetId())
		if option.GetId() == contracts.CodebuddyModeAuto {
			assert.Equal(t, "Auto", option.GetName())
			assert.NotEmpty(t, option.GetDescription())
		}
	}
	assert.Equal(t, []string{
		contracts.CodebuddyModeDefault, contracts.CodebuddyModeAcceptEdits, contracts.CodebuddyModeAuto,
		contracts.CodebuddyModePlan, contracts.CodebuddyModeBypassPermissions,
	}, ids)
	assert.Equal(t, contracts.CodebuddyModeAuto, group.GetCurrentValue())
	assert.Equal(t, contracts.CodebuddyModeAuto, permissionModeFlag(contracts.CodebuddyModeAuto), "the launch flag takes the mode verbatim")
}

// A running session switches to Auto with set_permission_mode, and the mode
// that CodeBuddy confirms is the one that LeapMux reports.
func TestCodebuddySettingsApplyAutoLive(t *testing.T) {
	a, peer := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(`{"mode":"auto"}`))
	result := a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.CodebuddyModeAuto})
	assert.True(t, result.AppliedLive)
	assert.Equal(t, contracts.CodebuddyModeAuto, result.ConfirmedOptions()[agent.OptionIDPermissionMode])
	assert.Contains(t, peer.String(), `"subtype":"set_permission_mode"`)
	assert.Contains(t, peer.String(), `"mode":"auto"`)
	a.mu.Lock()
	defer a.mu.Unlock()
	assert.Equal(t, contracts.CodebuddyModeAuto, a.permissionMode)
}

func TestCodebuddySettingsReportTheNativePermissionMode(t *testing.T) {
	a, _ := newCodebuddySteerAgent(t, nil, codebuddyControlAnswer(`{"mode":"default"}`))
	result := a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: contracts.CodebuddyModeBypassPermissions})
	assert.Equal(t, contracts.CodebuddyModeDefault, result.ConfirmedOptions()[agent.OptionIDPermissionMode])
}

func TestCodebuddyOutputStyleSettings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		options  map[string]string
		override string
		restart  bool
	}{
		{name: "changed style", options: map[string]string{OptionOutputStyle: "Explanatory"}, override: "Concise", restart: true},
		{name: "unchanged style", options: map[string]string{OptionOutputStyle: "Concise"}, override: "Concise"},
		{name: "omitted style", options: map[string]string{}, override: "Concise"},
		{name: "empty edit with an override", options: map[string]string{OptionOutputStyle: ""}, override: "Concise"},
		{name: "empty edit without an override", options: map[string]string{OptionOutputStyle: ""}},
		{name: "unknown style", options: map[string]string{OptionOutputStyle: "Unknown"}, override: "Concise"},
		{name: "blank style", options: map[string]string{OptionOutputStyle: " \t"}, override: "Concise"},
		{name: "unknown case-sensitive choice id", options: map[string]string{OptionOutputStyle: "concise"}, override: "Concise"},
		{name: "combined restart", options: map[string]string{OptionOutputStyle: "Explanatory", agent.OptionIDEffort: "low", agent.OptionIDModel: "alternate"}, override: "Concise", restart: true},
		{name: "invalid batch", options: map[string]string{OptionOutputStyle: "Unknown", agent.OptionIDEffort: "low", agent.OptionIDModel: "alternate"}, override: "Concise"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, peer := newCodebuddySteerAgent(t, nil, nil)
			a.outputStyles = []string{"Default", "Concise", "Explanatory"}
			a.outputStyle = "Concise"
			a.model = "primary"
			a.effort = "high"
			a.permissionMode = "default"
			a.opts.Options = map[string]string{agent.OptionIDEffort: "high", OptionOutputStyle: tc.override}
			before := a.SettingsSnapshot().SurfacedOptions
			result := a.UpdateSettings(tc.options)
			assert.Equal(t, !tc.restart, result.AppliedLive)
			assert.Equal(t, before, a.SettingsSnapshot().SurfacedOptions)
			assert.Empty(t, peer.String(), "style handling must precede every live model or permission write")
			if tc.restart {
				assert.Equal(t, agent.RestartRequiredSettings(tc.options), result)
			} else {
				assert.Equal(t, before, result.SurfacedOptions)
				if _, requested := tc.options[OptionOutputStyle]; requested {
					assert.Equal(t, "Concise", result.ConfirmedOptions()[OptionOutputStyle])
				}
				if tc.name == "invalid batch" {
					assert.Equal(t, "primary", result.ConfirmedOptions()[agent.OptionIDModel])
					assert.Equal(t, "high", result.ConfirmedOptions()[agent.OptionIDEffort])
				}
			}
		})
	}
}

func TestCodebuddyRejectsAnUnknownStyleWithoutACatalog(t *testing.T) {
	t.Parallel()
	a, peer := newCodebuddySteerAgent(t, nil, nil)
	result := a.UpdateSettings(map[string]string{OptionOutputStyle: "Unknown"})
	assert.True(t, result.AppliedLive, "a refused style must not reach a restart")
	assert.Equal(t, agent.OptionSettlementConfirmed, result.Settlements[OptionOutputStyle].State)
	assert.Nil(t, result.Settlements[OptionOutputStyle].Value, "remove the optimistic value when no style was configured")
	assert.NotContains(t, result.SurfacedOptions, OptionOutputStyle)
	assert.Nil(t, optionids.GroupByID(a.OptionGroups(), OptionOutputStyle))
	assert.Empty(t, peer.String())
}
