package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Unit tests for AvailableOptionGroups ---

func TestAvailableOptionGroups_IncludesOutputStyles(t *testing.T) {
	a := &ClaudeCodeAgent{
		availableOutputStyles: []string{"default", "Explanatory", "Learning"},
		outputStyle:           "Explanatory",
		alwaysThinking:        "on",
	}
	groups := a.OptionGroups()

	styleGroup := optionids.GroupByID(groups, ClaudeOptionOutputStyle)
	require.NotNil(t, styleGroup, "output style group should be present")
	assert.Equal(t, "Output Style", styleGroup.Label)
	assert.Len(t, styleGroup.Options, 3)

	// The current selection is "Explanatory"; the factory default is "default".
	assert.Equal(t, "Explanatory", styleGroup.GetCurrentValue())
	assert.Equal(t, "default", styleGroup.GetDefaultValue())
}

func TestAvailableOptionGroups_NoOutputStylesWhenEmpty(t *testing.T) {
	a := &ClaudeCodeAgent{
		alwaysThinking: "on",
	}
	groups := a.OptionGroups()
	assert.Nil(t, optionids.GroupByID(groups, ClaudeOptionOutputStyle), "should not have output style group when empty")
}

func TestAvailableOptionGroups_FastModeOn(t *testing.T) {
	a := &ClaudeCodeAgent{
		fastMode:       "on",
		alwaysThinking: "on",
	}
	groups := a.OptionGroups()

	fastGroup := optionids.GroupByID(groups, ClaudeOptionFastMode)
	require.NotNil(t, fastGroup, "fast mode group should always be present")
	assert.Len(t, fastGroup.Options, 2)

	// The current selection is "on"; the factory default is always "off".
	assert.Equal(t, "on", fastGroup.GetCurrentValue())
	assert.Equal(t, "off", fastGroup.GetDefaultValue())
}

func TestAvailableOptionGroups_FastModeDefaultsToOff(t *testing.T) {
	a := &ClaudeCodeAgent{
		alwaysThinking: "on",
	}
	groups := a.OptionGroups()

	fastGroup := optionids.GroupByID(groups, ClaudeOptionFastMode)
	require.NotNil(t, fastGroup, "fast mode group should always be present")
	assert.Equal(t, "off", fastGroup.GetDefaultValue(), "off should be the default")
}

func TestAvailableOptionGroups_AlwaysIncludesThinking(t *testing.T) {
	a := &ClaudeCodeAgent{
		alwaysThinking: AlwaysThinkingOff,
	}
	groups := a.OptionGroups()

	thinkingGroup := optionids.GroupByID(groups, ClaudeOptionAlwaysThinking)
	require.NotNil(t, thinkingGroup, "thinking group should always be present")
	// The current selection is "off"; the factory default is always "on".
	assert.Equal(t, AlwaysThinkingOff, thinkingGroup.GetCurrentValue())
	assert.Equal(t, AlwaysThinkingOn, thinkingGroup.GetDefaultValue())
}

// The enabled option's display name tracks Claude Code's per-model
// thinking-type gate: "Adaptive" for first-party models, "On" for Haiku
// (legacy type:"enabled"). The option ID stays "on" for all models.
func TestAvailableOptionGroups_ThinkingLabelsByModel(t *testing.T) {
	cases := []struct {
		model    string
		wantName string
		current  string // initial a.alwaysThinking
	}{
		{"opus", "Adaptive", AlwaysThinkingOn},
		{"opus[1m]", "Adaptive", AlwaysThinkingOn},
		{"sonnet", "Adaptive", AlwaysThinkingOn},
		{"sonnet[1m]", "Adaptive", AlwaysThinkingOff},
		{"haiku", "On", AlwaysThinkingOn},
		{"haiku", "On", AlwaysThinkingOff},
		{"", "Adaptive", AlwaysThinkingOn},
		{"unknown-future-model", "Adaptive", AlwaysThinkingOn},
	}
	for _, tc := range cases {
		t.Run(tc.model+"/"+tc.current, func(t *testing.T) {
			a := &ClaudeCodeAgent{model: tc.model, alwaysThinking: tc.current}
			groups := a.OptionGroups()

			g := optionids.GroupByID(groups, ClaudeOptionAlwaysThinking)
			require.NotNil(t, g)
			require.Len(t, g.Options, 2, "thinking group has exactly two options (enabled + off)")

			enabled := g.Options[0]
			off := g.Options[1]
			assert.Equal(t, AlwaysThinkingOn, enabled.Id, "enabled option ID is always 'on'")
			assert.Equal(t, tc.wantName, enabled.Name, "enabled option name varies by model")
			assert.Equal(t, AlwaysThinkingOff, off.Id)
			assert.Equal(t, "Off", off.Name)
			// The factory default is always the enabled option; the current selection
			// reflects the agent's alwaysThinking state.
			assert.Equal(t, AlwaysThinkingOn, g.GetDefaultValue(), "'on' is always the default")
			assert.Equal(t, tc.current, g.GetCurrentValue(), "current value tracks alwaysThinking")
		})
	}
}

// Each model option carries its model-dependent groups (effort + extended
// thinking) in SubGroups, so the frontend can rebuild them the instant the
// model selection changes -- without waiting for the relaunch a model switch
// triggers. The carried groups are model-specific: Opus offers xhigh/ultracode
// and an "Adaptive" thinking label; Sonnet drops xhigh; Haiku has no effort
// group and an "On" thinking label.
func TestOptionGroups_ModelOptionsCarrySubGroups(t *testing.T) {
	a := &ClaudeCodeAgent{model: "sonnet", alwaysThinking: AlwaysThinkingOn}
	groups := a.OptionGroups()
	modelGroup := optionids.GroupByID(groups, OptionIDModel)
	require.NotNil(t, modelGroup)

	subGroup := func(modelID, groupID string) *leapmuxv1.AvailableOptionGroup {
		for _, o := range modelGroup.GetOptions() {
			if o.GetId() == modelID {
				return optionids.GroupByID(o.GetSubGroups(), groupID)
			}
		}
		return nil
	}
	effortIDs := func(g *leapmuxv1.AvailableOptionGroup) []string {
		ids := make([]string, 0, len(g.GetOptions()))
		for _, o := range g.GetOptions() {
			ids = append(ids, o.GetId())
		}
		return ids
	}

	// Opus[1m]: effort offers xhigh + ultracode and defaults to xhigh; thinking
	// enabled label is "Adaptive".
	opusEffort := subGroup("opus[1m]", OptionIDEffort)
	require.NotNil(t, opusEffort)
	assert.Contains(t, effortIDs(opusEffort), EffortXHigh)
	assert.Contains(t, effortIDs(opusEffort), EffortUltracode)
	assert.Equal(t, EffortXHigh, opusEffort.GetDefaultValue())
	opusThinking := subGroup("opus[1m]", ClaudeOptionAlwaysThinking)
	require.NotNil(t, opusThinking)
	assert.Equal(t, "Adaptive", opusThinking.GetOptions()[0].GetName())

	// Sonnet: effort drops xhigh (offers max) and defaults to high.
	sonnetEffort := subGroup("sonnet", OptionIDEffort)
	require.NotNil(t, sonnetEffort)
	assert.NotContains(t, effortIDs(sonnetEffort), EffortXHigh)
	assert.Contains(t, effortIDs(sonnetEffort), "max")
	assert.Equal(t, EffortHigh, sonnetEffort.GetDefaultValue())

	// Haiku: no effort group at all; thinking enabled label is "On".
	assert.Nil(t, subGroup("haiku", OptionIDEffort), "Haiku has no effort group")
	haikuThinking := subGroup("haiku", ClaudeOptionAlwaysThinking)
	require.NotNil(t, haikuThinking)
	assert.Equal(t, "On", haikuThinking.GetOptions()[0].GetName())
}

func TestModelSupportsAdaptiveThinking(t *testing.T) {
	// Expects the short alias. Fully-qualified IDs should be normalized
	// via normalizeClaudeCodeModel before calling.
	cases := map[string]bool{
		"opus":                 true,
		"opus[1m]":             true,
		"sonnet":               true,
		"sonnet[1m]":           true,
		"haiku":                false,
		"":                     true, // unknown → default-on
		"unknown-future-model": true,
	}
	for model, want := range cases {
		t.Run(model, func(t *testing.T) {
			assert.Equal(t, want, modelSupportsAdaptiveThinking(model))
		})
	}
}

func TestNormalizeClaudeCodeModel(t *testing.T) {
	cases := map[string]string{
		// Short aliases pass through.
		"opus":       "opus",
		"opus[1m]":   "opus[1m]",
		"sonnet":     "sonnet",
		"sonnet[1m]": "sonnet[1m]",
		"haiku":      "haiku",
		// Fully-qualified IDs Claude Code returns from get_settings.
		"claude-opus-4-7":            "opus",
		"claude-opus-4-7[1m]":        "opus[1m]",
		"claude-opus-4-6":            "opus",
		"claude-opus-4-6[1m]":        "opus[1m]",
		"claude-sonnet-4-6":          "sonnet",
		"claude-sonnet-4-6[1m]":      "sonnet[1m]",
		"claude-haiku-4-5-20251001":  "haiku",
		"claude-haiku-4-5":           "haiku",
		"claude-sonnet-4-5-20240101": "sonnet",
		// Fable is 1M-only: every spelling canonicalizes to "fable[1m]" (the static
		// catalog id and what the live CLI reports), so a bare "fable" or a
		// fully-qualified value never splits from the running model.
		"fable":              "fable[1m]",
		"fable[1m]":          "fable[1m]",
		"claude-fable-5":     "fable[1m]",
		"claude-fable-5[1m]": "fable[1m]",
		"FABLE":              "fable[1m]",
		// Version-first ids (numeric tokens leading the family): the family alias is
		// found by skipping the leading version tokens, where a from-position-0 alpha
		// scan would return "" and leak the raw id.
		"claude-3-5-sonnet":         "sonnet",
		"claude-3-5-haiku-20241022": "haiku",
		"3-7-sonnet":                "sonnet",
		// Mixed-case CLI values collapse to the lowercase alias space (S3), so a
		// running model still matches its own (lowercase) catalog entry.
		"OPUS[1M]":          "opus[1m]",
		"Claude-Sonnet-4-6": "sonnet",
		"Opus":              "opus",
		// Degenerate input.
		"":              "",
		"unknown-thing": "unknown",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			assert.Equal(t, want, normalizeClaudeCodeModel(in))
		})
	}
}

func TestFlagSettingThinking(t *testing.T) {
	assert.Equal(t, false, flagSettingThinking(AlwaysThinkingOff))
	assert.Nil(t, flagSettingThinking(AlwaysThinkingOn), "on → nil (lets Claude Code default-on kick in)")
	assert.Nil(t, flagSettingThinking(""), "empty → nil")
	assert.Nil(t, flagSettingThinking("anything-else"), "unknown → nil")
}

// --- Unit tests for CurrentSettings ---

func TestCurrentSettings_IncludesExtraSettings(t *testing.T) {
	a := &ClaudeCodeAgent{
		processBase:             processBase{agentID: "test"},
		model:                   "sonnet",
		effort:                  "low",
		confirmedPermissionMode: "default",
		availableOutputStyles:   []string{"default", "Explanatory"},
		outputStyle:             "Explanatory",
		fastMode:                "on",
		alwaysThinking:          "off",
	}
	opts := CurrentOptions(a.OptionGroups())
	assert.Equal(t, "sonnet", opts[OptionIDModel])
	assert.Equal(t, "low", opts[OptionIDEffort])
	assert.Equal(t, "Explanatory", opts[ClaudeOptionOutputStyle])
	assert.Equal(t, "on", opts[ClaudeOptionFastMode])
	assert.Equal(t, "off", opts[ClaudeOptionAlwaysThinking])
}

func TestRefreshSettingsFromAgent_DoesNotReportPermissionMode(t *testing.T) {
	sink := &testSink{}
	a, err := spawnMockClaudeAgent(context.Background(), "TestHelperProcessWithControlProtocol",
		[]string{"GO_WANT_HELPER_PROCESS_CONTROL=1"},
		Options{
			AgentID:    "test-refresh",
			Options:    map[string]string{OptionIDModel: "opus[1m]"},
			WorkingDir: t.TempDir(),
			APITimeout: 5 * time.Second,
		},
		sink)
	require.NoError(t, err)
	defer stopTestAgent(a)

	a.confirmedPermissionMode = PermissionModeDefault
	a.refreshSettingsFromAgent(5 * time.Second)

	require.Equal(t, 1, sink.SettingsRefreshCount())
	refresh := sink.LastSettingsRefresh()
	assert.Empty(t, refresh.PermissionMode, "Claude get_settings does not report permission mode")
}

// --- Unit tests for UpdateSettings logic (no-op / validation) ---

func TestUpdateSettings_NothingChanged(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:              a.model,
		OptionIDEffort:             a.effort,
		ClaudeOptionOutputStyle:    a.outputStyle,
		ClaudeOptionFastMode:       a.fastMode,
		ClaudeOptionAlwaysThinking: a.alwaysThinking,
	})
	assert.True(t, result, "should return true when nothing changed")
}

func TestUpdateSettings_OutputStyleInvalid(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.availableOutputStyles = []string{"default", "Explanatory"}
	a.outputStyle = "default"

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:           a.model,
		OptionIDEffort:          a.effort,
		ClaudeOptionOutputStyle: "NonExistent",
	})
	assert.False(t, result, "should return false for invalid output style")
	assert.Equal(t, "default", a.outputStyle, "output style should not change")
}

func TestUpdateSettings_ModelChange(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:  "sonnet",
		OptionIDEffort: a.effort,
	})
	assert.True(t, result, "should return true for model change via apply_flag_settings")
	assert.Equal(t, "sonnet", a.model, "model should be updated from get_settings response")
}

// TestUpdateSettings_RespelledModelSendsNoModelFlag is the regression guard for C4.1: a model
// re-spelled into the same normalized id (a CLI alias like "OPUS[1M]" vs the running "opus[1m]")
// is recognized as unchanged, so UpdateSettings sends no redundant "model" flag. A fast-mode
// toggle rides along so an apply_flag_settings IS sent -- it must carry fastMode but NOT model.
func TestUpdateSettings_RespelledModelSendsNoModelFlag(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "flags.jsonl")
	a := newTestAgentWithControlProtocolEnv(t, "GO_HELPER_RECORD_REQUESTS="+recordPath)
	defer stopTestAgent(a)

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:        "OPUS[1M]", // normalizes to the running "opus[1m]" -- not a real change
		ClaudeOptionFastMode: FastModeOn, // a real change, so an apply_flag_settings is sent
	})
	require.True(t, result)

	sent := readRecordedFlagSettings(t, recordPath)
	require.NotEmpty(t, sent, "the fast-mode change must trigger an apply_flag_settings")
	for _, settings := range sent {
		assert.NotContains(t, settings, "model",
			"a re-spelled-but-identical model must not send a redundant model flag")
		assert.Contains(t, settings, ClaudeOptionFastMode, "the real fast-mode change is sent")
	}
}

// readRecordedFlagSettings reads the apply_flag_settings settings maps the mock recorded
// (one JSON object per line), or nil when the file was never written (no apply was sent).
func readRecordedFlagSettings(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		out = append(out, m)
	}
	return out
}

func TestUpdateSettings_EffortChange(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:  a.model,
		OptionIDEffort: "low",
	})
	assert.True(t, result, "should return true for effort change")
	assert.Equal(t, "low", a.effort, "effort should be updated from get_settings response")
}

func TestUpdateSettings_PermissionModeChange(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.confirmedPermissionMode = PermissionModeDefault

	result := a.UpdateSettings(map[string]string{
		OptionIDPermissionMode: PermissionModePlan,
	})
	assert.True(t, result, "should return true for permission mode change")
	assert.Equal(t, PermissionModePlan, a.confirmedPermissionMode)
}

// TestUpdateSettings_AutoSwitchClearsStaleAutoModeAvailable guards the [C8] state-accuracy fix on
// the live path: a successful live switch to "auto" proves the session can enter it, so it clears a
// stale autoModeAvailable=false (left by a transient startup probe failure) -- otherwise OptionGroups
// would keep filtering "auto" out of the picker even though the session is running it.
func TestUpdateSettings_AutoSwitchClearsStaleAutoModeAvailable(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.autoModeAvailable = false // a transient startup probe failure left this stale
	a.confirmedPermissionMode = PermissionModeDefault

	result := a.UpdateSettings(map[string]string{OptionIDPermissionMode: PermissionModeAuto})
	require.True(t, result, "a live permission-mode switch should not request a restart")
	assert.Equal(t, PermissionModeAuto, a.confirmedPermissionMode)
	assert.True(t, a.autoModeAvailable,
		"a successful live switch to auto clears the stale autoModeAvailable=false so the picker offers auto again")
}

// newTestAgentWithDeferredPermissionMode spawns a mock whose control protocol NEVER acks
// set_permission_mode (simulating the CLI holding the ack until an active turn ends), with
// a short APITimeout so min(permissionModeApplyTimeout, APITimeout) makes the live wait
// time out quickly.
func newTestAgentWithDeferredPermissionMode(t *testing.T) *ClaudeCodeAgent {
	t.Helper()
	a, err := spawnMockClaudeAgent(context.Background(), "TestHelperProcessWithControlProtocol",
		[]string{"GO_WANT_HELPER_PROCESS_CONTROL=1", "GO_HELPER_NO_ACK_PERMISSION_MODE=1"},
		Options{
			AgentID:    "test-ctrl-deferred",
			Options:    map[string]string{OptionIDModel: "opus[1m]"},
			WorkingDir: t.TempDir(),
			APITimeout: 200 * time.Millisecond,
		},
		noopSink{})
	require.NoError(t, err)
	a.confirmedPermissionMode = PermissionModeDefault
	return a
}

// TestUpdateSettings_PermissionModeDeferredAck verifies the S4 behavior: a
// set_permission_mode whose ack the CLI defers (a turn is in progress) is treated as
// accepted-pending, NOT a failure. UpdateSettings returns true (so the caller persists the
// change and does NOT restart -- which would abort the turn) and records the requested mode
// optimistically; the deferred control_response later reconciles the confirmed value
// (see TestClaudeHandleControlResponse_DeferredModeReconcilesInMemory).
func TestUpdateSettings_PermissionModeDeferredAck(t *testing.T) {
	a := newTestAgentWithDeferredPermissionMode(t)
	defer stopTestAgent(a)

	result := a.UpdateSettings(map[string]string{OptionIDPermissionMode: PermissionModePlan})
	assert.True(t, result, "a deferred set_permission_mode ack must not request a restart")
	assert.Equal(t, PermissionModePlan, a.confirmedPermissionMode,
		"the requested mode is recorded optimistically while the ack is pending")
}

// TestClaudeHandleControlResponse_DeferredModeReconcilesInMemory is the regression guard for
// [S5]: when the CLI's deferred set_permission_mode control_response finally arrives, it
// reconciles BOTH the in-memory confirmed mode (which OptionGroups reads) AND the persisted
// row/broadcast to the mode the CLI actually applied -- not the optimistic value. Without the
// in-memory update the catalog would keep reporting the optimistic mode after the CLI settled
// on a different one.
func TestClaudeHandleControlResponse_DeferredModeReconcilesInMemory(t *testing.T) {
	sink := &testSink{}
	a := &ClaudeCodeAgent{sink: sink}
	// The live path recorded "plan" optimistically and remembered the deferred toggle's id.
	a.confirmedPermissionMode = PermissionModePlan
	a.deferredPermissionModeReqID = "req-1"

	// The deferred control_response for THAT request: the CLI actually settled on acceptEdits.
	a.claudeCodeHandleControlResponse([]byte(
		`{"type":"control_response","response":{"subtype":"success","request_id":"req-1","response":{"mode":"acceptEdits"}}}`))

	assert.Equal(t, PermissionModeAcceptEdits, a.confirmedPermissionMode,
		"the in-memory confirmed mode is reconciled to the CLI's applied mode")
	require.Equal(t, []string{PermissionModeAcceptEdits}, sink.permissionModes,
		"the persisted row + broadcast are reconciled to the applied mode too")
	assert.Empty(t, a.deferredPermissionModeReqID, "the consumed deferred id is cleared")
}

// TestClaudeHandleControlResponse_IgnoresUncorrelatedMode is the regression guard for the
// request_id correlation [S8]: a mode-bearing control_response whose request_id does NOT match
// the awaited deferred toggle -- a stale/duplicate ack, an earlier (superseded) toggle's ack,
// or any other mode-carrying response -- must NOT clobber the confirmed mode.
func TestClaudeHandleControlResponse_IgnoresUncorrelatedMode(t *testing.T) {
	sink := &testSink{}
	a := &ClaudeCodeAgent{sink: sink}
	a.confirmedPermissionMode = PermissionModePlan
	a.deferredPermissionModeReqID = "req-current"

	// A stale ack for a different (already-superseded) request.
	a.claudeCodeHandleControlResponse([]byte(
		`{"type":"control_response","response":{"subtype":"success","request_id":"req-stale","response":{"mode":"default"}}}`))

	assert.Equal(t, PermissionModePlan, a.confirmedPermissionMode,
		"an uncorrelated ack must not clobber the confirmed mode")
	assert.Empty(t, sink.permissionModes, "no broadcast for an uncorrelated ack")
	assert.Equal(t, "req-current", a.deferredPermissionModeReqID,
		"the awaited deferred id is left pending")
}

// TestClaudeHandleControlResponse_DeferredAutoClearsStaleAutoModeAvailable guards the [C8] state-
// accuracy fix on the deferred-ack path: when the CLI's deferred set_permission_mode ack confirms
// "auto", it proves the session can enter it, so a stale autoModeAvailable=false (left by a transient
// startup probe failure) is cleared -- mirroring the live path -- so OptionGroups offers auto again.
func TestClaudeHandleControlResponse_DeferredAutoClearsStaleAutoModeAvailable(t *testing.T) {
	sink := &testSink{}
	a := &ClaudeCodeAgent{sink: sink}
	a.autoModeAvailable = false // a transient startup probe failure left this stale
	a.confirmedPermissionMode = PermissionModeAuto
	a.deferredPermissionModeReqID = "req-auto"

	a.claudeCodeHandleControlResponse([]byte(
		`{"type":"control_response","response":{"subtype":"success","request_id":"req-auto","response":{"mode":"auto"}}}`))

	assert.Equal(t, PermissionModeAuto, a.confirmedPermissionMode)
	assert.True(t, a.autoModeAvailable,
		"a deferred ack confirming auto clears the stale autoModeAvailable=false")
	assert.Empty(t, a.deferredPermissionModeReqID, "the consumed deferred id is cleared")
}

// TestUpdateSettings_PermissionModeGenuineError verifies that a genuine (non-timeout)
// set_permission_mode rejection is still treated as a failure: UpdateSettings returns false
// (requesting a restart) and leaves the confirmed mode unchanged -- the deferred-ack path is
// scoped to timeouts only.
func TestUpdateSettings_PermissionModeGenuineError(t *testing.T) {
	a, err := spawnMockClaudeAgent(context.Background(), "TestHelperProcessWithControlProtocol",
		[]string{"GO_WANT_HELPER_PROCESS_CONTROL=1", "GO_HELPER_ERROR_PERMISSION_MODE=1"},
		Options{
			AgentID:    "test-ctrl-err",
			Options:    map[string]string{OptionIDModel: "opus[1m]"},
			WorkingDir: t.TempDir(),
			APITimeout: 2 * time.Second,
		},
		noopSink{})
	require.NoError(t, err)
	defer stopTestAgent(a)
	a.confirmedPermissionMode = PermissionModeDefault

	result := a.UpdateSettings(map[string]string{OptionIDPermissionMode: PermissionModePlan})
	assert.False(t, result, "a genuine set_permission_mode error must request a restart")
	assert.Equal(t, PermissionModeDefault, a.confirmedPermissionMode,
		"the confirmed mode is unchanged when the change was rejected")
}

// TestUpdateSettings_CombinedChangePermissionFailureDefersBroadcast guards S5: when a combined
// model+permission-mode change applies the model live (apply_flag_settings lands) but the
// permission-mode apply is then rejected, UpdateSettings must signal a restart WITHOUT first
// reading back / broadcasting the half-applied model. refreshSettingsFromAgent is deferred until
// every live apply succeeds, so no settings_changed for the model fires (and a.model is not folded
// to the new value) before the restart applies the full requested settings atomically.
func TestUpdateSettings_CombinedChangePermissionFailureDefersBroadcast(t *testing.T) {
	sink := &testSink{}
	a, err := spawnMockClaudeAgent(context.Background(), "TestHelperProcessWithControlProtocol",
		[]string{"GO_WANT_HELPER_PROCESS_CONTROL=1", "GO_HELPER_ERROR_PERMISSION_MODE=1"},
		Options{
			AgentID:    "test-combined-fail",
			Options:    map[string]string{OptionIDModel: "opus[1m]"},
			WorkingDir: t.TempDir(),
			APITimeout: 2 * time.Second,
		},
		sink)
	require.NoError(t, err)
	defer stopTestAgent(a)
	a.model = "opus[1m]"
	a.confirmedPermissionMode = PermissionModeDefault

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:          "sonnet",
		OptionIDPermissionMode: PermissionModePlan,
	})
	assert.False(t, result, "a combined change whose permission-mode apply fails must request a restart")
	assert.Equal(t, 0, sink.SettingsRefreshCount(),
		"the half-applied model must NOT be read back/broadcast when a later axis fails")
	assert.Equal(t, "opus[1m]", a.model,
		"a.model stays at its pre-change value (the refresh is deferred) until the restart applies it")
	assert.Equal(t, PermissionModeDefault, a.confirmedPermissionMode,
		"the rejected permission mode is unchanged")
}

// TestUpdateSettings_AutoRequiresRestart verifies that switching effort to
// "auto" mid-session signals the caller to restart the agent. The CLI
// doesn't accept "auto" as an effortLevel via apply_flag_settings; the
// only way to hand control back to the CLI's own default is to re-spawn
// without --effort.
func TestUpdateSettings_AutoRequiresRestart(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	require.Equal(t, "high", a.effort, "precondition")

	result := a.UpdateSettings(map[string]string{OptionIDEffort: "auto"})
	assert.False(t, result, "switching to \"auto\" should request a restart")
	assert.Equal(t, "high", a.effort, "live effort must stay untouched until restart")
}

// TestUpdateSettings_AutoNoOpWhenAlreadyAuto verifies that a redundant
// "auto"→"auto" update doesn't request a restart.
func TestUpdateSettings_AutoNoOpWhenAlreadyAuto(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.effort = "auto"

	result := a.UpdateSettings(map[string]string{OptionIDEffort: "auto"})
	assert.True(t, result, "a no-op \"auto\"→\"auto\" should not request a restart")
	assert.Equal(t, "auto", a.effort)
}

func TestUpdateSettings_OutputStyleChange(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.availableOutputStyles = []string{"default", "Explanatory", "Learning"}

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:           a.model,
		OptionIDEffort:          a.effort,
		ClaudeOptionOutputStyle: "Explanatory",
	})
	assert.True(t, result, "should return true for output style change")
	assert.Equal(t, "Explanatory", a.outputStyle, "output style should be updated")
}

func TestUpdateSettings_FastModeOn(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.fastMode = "off"

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:        a.model,
		OptionIDEffort:       a.effort,
		ClaudeOptionFastMode: "on",
	})
	assert.True(t, result)
	assert.Equal(t, "on", a.fastMode)
}

func TestUpdateSettings_AlwaysThinkingOff(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:              a.model,
		OptionIDEffort:             a.effort,
		ClaudeOptionAlwaysThinking: "off",
	})
	assert.True(t, result)
	assert.Equal(t, "off", a.alwaysThinking)
}

func TestUpdateSettings_FastModeOff(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.fastMode = "on"

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:        a.model,
		OptionIDEffort:       a.effort,
		ClaudeOptionFastMode: "off",
	})
	assert.True(t, result)
	assert.Equal(t, "off", a.fastMode)
}

func TestUpdateSettings_AlwaysThinkingOn(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.alwaysThinking = AlwaysThinkingOff

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:              a.model,
		OptionIDEffort:             a.effort,
		ClaudeOptionAlwaysThinking: AlwaysThinkingOn,
	})
	assert.True(t, result)
	assert.Equal(t, AlwaysThinkingOn, a.alwaysThinking)
}

// TestUpdateSettings_ToggleRoundTripTracksConfirmedValue is the regression for the
// settings-changed notification baseline desync: each toggle's CONFIRMED value
// (read back from get_settings and persisted) must track the request, even when the
// CLI reports the cleared flag as absent. Before the fix, turning a flag back to its
// default direction (Fast Mode off / Extended Thinking on, both sent as null) left
// the confirmed value stale -- so the persisted baseline diverged from the UI and a
// later toggle silently produced no notification (old==new) or a reversed one.
func TestUpdateSettings_ToggleRoundTripTracksConfirmedValue(t *testing.T) {
	t.Run("extended thinking on->off->on->off", func(t *testing.T) {
		a := newTestAgentWithControlProtocol(t)
		defer stopTestAgent(a)
		a.alwaysThinking = AlwaysThinkingOn // adaptive default

		for _, step := range []struct{ set, want string }{
			{AlwaysThinkingOff, AlwaysThinkingOff}, // explicit false
			{AlwaysThinkingOn, AlwaysThinkingOn},   // null-clear -> default on (was stale "off")
			{AlwaysThinkingOff, AlwaysThinkingOff}, // must register as a real change again
			{AlwaysThinkingOn, AlwaysThinkingOn},
		} {
			require.True(t, a.UpdateSettings(map[string]string{ClaudeOptionAlwaysThinking: step.set}))
			assert.Equal(t, step.want, a.alwaysThinking, "after setting thinking=%s", step.set)
		}
	})

	t.Run("fast mode off->on->off->on", func(t *testing.T) {
		a := newTestAgentWithControlProtocol(t)
		defer stopTestAgent(a)
		a.fastMode = FastModeOff // default

		for _, step := range []struct{ set, want string }{
			{FastModeOn, FastModeOn},   // explicit true
			{FastModeOff, FastModeOff}, // null-clear -> default off (was stale "on")
			{FastModeOn, FastModeOn},   // must register as a real change again
			{FastModeOff, FastModeOff},
		} {
			require.True(t, a.UpdateSettings(map[string]string{ClaudeOptionFastMode: step.set}))
			assert.Equal(t, step.want, a.fastMode, "after setting fastMode=%s", step.set)
		}
	})
}

func TestUpdateSettings_ApplyFlagSettingsFails(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	// Stop the mock process so apply_flag_settings will fail.
	a.Stop()
	_ = a.Wait()

	// Re-mark as not stopped so UpdateSettings proceeds to sendControlAndWait.
	a.mu.Lock()
	a.stopped = false
	a.mu.Unlock()

	originalModel := a.model
	result := a.UpdateSettings(map[string]string{
		OptionIDModel:  "sonnet",
		OptionIDEffort: a.effort,
	})
	assert.False(t, result, "should return false when apply_flag_settings fails")
	assert.Equal(t, originalModel, a.model, "model should not change on failure")
}

func TestUpdateSettings_MultipleChanges(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.availableOutputStyles = []string{"default", "Learning"}
	a.fastMode = "off"

	result := a.UpdateSettings(map[string]string{
		OptionIDModel:           "sonnet",
		OptionIDEffort:          "low",
		ClaudeOptionOutputStyle: "Learning",
		ClaudeOptionFastMode:    "on",
	})
	assert.True(t, result, "should return true for multiple changes")
	assert.Equal(t, "sonnet", a.model)
	assert.Equal(t, "low", a.effort)
	assert.Equal(t, "Learning", a.outputStyle)
	assert.Equal(t, "on", a.fastMode)
}

// --- Unit tests for handlePendingControlResponse parsing ---

func TestHandlePendingControlResponse_ParsesInitializeFields(t *testing.T) {
	a := &ClaudeCodeAgent{
		processBase:    processBase{agentID: "test"},
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}

	ch := make(chan claudeCodeControlResult, 1)
	a.registerPendingControl("req-1", ch)

	raw := []byte(`{"type":"control_response","response":{` +
		`"subtype":"success","request_id":"req-1","response":{` +
		`"output_style":"Explanatory",` +
		`"available_output_styles":["default","Explanatory","Learning"],` +
		`"fast_mode_state":"on",` +
		`"mode":"default"` +
		`}}}`)

	line := &parsedLine{Type: "control_response", Raw: raw}
	consumed := a.handlePendingControlResponse(line)
	assert.True(t, consumed)

	result := <-ch
	assert.True(t, result.Success)
	assert.Equal(t, "Explanatory", result.OutputStyle)
	assert.Equal(t, []string{"default", "Explanatory", "Learning"}, result.AvailableOutputStyles)
	assert.Equal(t, "on", result.FastModeState)
	assert.Equal(t, "default", result.Mode)
	assert.NotEmpty(t, result.RawResponse)
}

func TestHandlePendingControlResponse_ParsesModels(t *testing.T) {
	a := &ClaudeCodeAgent{
		processBase:    processBase{agentID: "test"},
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}

	ch := make(chan claudeCodeControlResult, 1)
	a.registerPendingControl("req-models", ch)

	// An initialize response carrying the model catalog: a "default" alias
	// sentinel, a disabled entry, the [1m] variants, an effort-bearing Fable, a
	// no-effort Haiku, plus a separate unavailable_models list.
	raw := []byte(`{"type":"control_response","response":{` +
		`"subtype":"success","request_id":"req-models","response":{` +
		`"models":[` +
		`{"value":"default","displayName":"Default","description":"d"},` +
		`{"value":"fable","displayName":"Fable 5","description":"Most powerful for the hardest problems","supportsEffort":true,"supportedEffortLevels":["low","medium","high","xhigh","max"]},` +
		`{"value":"fable[1m]","displayName":"Fable 5 (1M context)","description":"Most powerful for the hardest problems","supportsEffort":true,"supportedEffortLevels":["low","medium","high","xhigh","max"]},` +
		`{"value":"haiku","displayName":"Haiku","description":"Fastest for quick answers","supportsEffort":false},` +
		`{"value":"internal-preview","displayName":"Internal","disabled":true}` +
		`],` +
		`"unavailable_models":[{"value":"zdr-blocked","displayName":"Blocked"}]` +
		`}}}`)

	line := &parsedLine{Type: "control_response", Raw: raw}
	assert.True(t, a.handlePendingControlResponse(line))

	result := <-ch
	assert.True(t, result.Success)
	require.Len(t, result.Models, 5, "all raw entries decode, including default/disabled")
	require.Len(t, result.UnavailableModels, 1)
	assert.Equal(t, "fable", result.Models[1].Value)
	assert.True(t, result.Models[1].SupportsEffort)
	assert.Equal(t, []string{"low", "medium", "high", "xhigh", "max"}, result.Models[1].SupportedEffortLevels)
	assert.True(t, result.Models[4].Disabled)
	assert.Equal(t, "zdr-blocked", result.UnavailableModels[0].Value)

	// Conversion drops the disabled entry, surfaces the "default" sentinel as a
	// selectable option, and surfaces Fable with its full effort menu and
	// inferred context windows.
	converted := claudeModelsByID(convertClaudeModels(result.Models, result.UnavailableModels))
	// The raw "fable" and "fable[1m]" entries both canonicalize to "fable[1m]".
	require.Contains(t, converted, "fable[1m]")
	require.NotContains(t, converted, "fable", "bare fable canonicalizes to fable[1m]")
	require.Contains(t, converted, "default")
	require.NotContains(t, converted, "internal-preview")
	assert.Equal(t, "xhigh", converted["fable[1m]"].DefaultEffort)
	assert.Equal(t, int64(1_000_000), converted["fable[1m]"].ContextWindow)
	assert.Empty(t, converted["haiku"].SupportedEfforts)
}

func TestHandlePendingControlResponse_ParsesGetSettingsResponse(t *testing.T) {
	a := &ClaudeCodeAgent{
		processBase:    processBase{agentID: "test"},
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}

	ch := make(chan claudeCodeControlResult, 1)
	a.registerPendingControl("req-2", ch)

	raw := []byte(`{"type":"control_response","response":{` +
		`"subtype":"success","request_id":"req-2","response":{` +
		`"effective":{"outputStyle":"Learning","fastMode":true,"alwaysThinkingEnabled":false},` +
		`"applied":{"model":"sonnet","effort":"low"}` +
		`}}}`)

	line := &parsedLine{Type: "control_response", Raw: raw}
	consumed := a.handlePendingControlResponse(line)
	assert.True(t, consumed)

	result := <-ch
	assert.True(t, result.Success)
	assert.NotEmpty(t, result.RawResponse)

	// Verify the raw response can be parsed for get_settings fields.
	var settings struct {
		Effective struct {
			OutputStyle           string `json:"outputStyle"`
			FastMode              *bool  `json:"fastMode"`
			AlwaysThinkingEnabled *bool  `json:"alwaysThinkingEnabled"`
		} `json:"effective"`
		Applied struct {
			Model  string  `json:"model"`
			Effort *string `json:"effort"`
		} `json:"applied"`
	}
	require.NoError(t, json.Unmarshal(result.RawResponse, &settings))
	assert.Equal(t, "Learning", settings.Effective.OutputStyle)
	assert.NotNil(t, settings.Effective.FastMode)
	assert.True(t, *settings.Effective.FastMode)
	assert.NotNil(t, settings.Effective.AlwaysThinkingEnabled)
	assert.False(t, *settings.Effective.AlwaysThinkingEnabled)
	assert.Equal(t, "sonnet", settings.Applied.Model)
	assert.NotNil(t, settings.Applied.Effort)
	assert.Equal(t, "low", *settings.Applied.Effort)
}

// --- Test helper: mock process with control protocol ---

// TestHelperProcessWithControlProtocol is a test helper that responds to
// control_request messages. It tracks settings state from apply_flag_settings
// and returns it in get_settings responses.
func TestHelperProcessWithControlProtocol(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_CONTROL") != "1" {
		return
	}

	// Track current settings state.
	state := map[string]interface{}{
		"model":                 "opus[1m]",
		"effort":                "high",
		"mode":                  PermissionModeDefault,
		"outputStyle":           "default",
		"fastMode":              nil,
		"alwaysThinkingEnabled": nil,
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var msg struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype  string                 `json:"subtype"`
				Mode     string                 `json:"mode"`
				Settings map[string]interface{} `json:"settings"`
			} `json:"request"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.Type != "control_request" {
			_, _ = fmt.Fprintln(os.Stdout, line)
			continue
		}

		var responseBody interface{}
		switch msg.Request.Subtype {
		case "set_permission_mode":
			if os.Getenv("GO_HELPER_NO_ACK_PERMISSION_MODE") == "1" {
				// Simulate the CLI deferring the set_permission_mode ack until the active
				// turn ends: receive the request but write no control_response, so the
				// caller's sendControlAndWait times out.
				continue
			}
			if os.Getenv("GO_HELPER_ERROR_PERMISSION_MODE") == "1" {
				// Simulate the CLI REJECTING the mode (a genuine, non-timeout error).
				errResp, _ := json.Marshal(map[string]interface{}{
					"type": "control_response",
					"response": map[string]interface{}{
						"subtype":    "error",
						"request_id": msg.RequestID,
						"error":      "permission mode rejected",
					},
				})
				_, _ = fmt.Fprintln(os.Stdout, string(errResp))
				continue
			}
			mode := msg.Request.Mode
			if mode == "" {
				mode = PermissionModeDefault
			}
			state["mode"] = mode
			responseBody = map[string]interface{}{"mode": mode}
		case "apply_flag_settings":
			// When GO_HELPER_RECORD_REQUESTS names a file, append each apply_flag_settings'
			// settings map (one JSON object per line) so a test can assert WHICH flags were
			// sent -- e.g. that a re-spelled-but-identical model sends no "model" flag.
			if recordPath := os.Getenv("GO_HELPER_RECORD_REQUESTS"); recordPath != "" {
				if f, ferr := os.OpenFile(recordPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
					line, _ := json.Marshal(msg.Request.Settings)
					_, _ = fmt.Fprintln(f, string(line))
					_ = f.Close()
				}
			}
			for k, v := range msg.Request.Settings {
				if v == nil {
					delete(state, k)
				} else {
					state[k] = v
				}
			}
			responseBody = map[string]interface{}{}
		case "get_settings":
			// Mirror the real CLI (verified by disassembling the 2.1.170 binary):
			// `effective` is the merged settings map (getSettings spreads
			// PU().settings), so a flag CLEARED via apply_flag_settings -- sent null,
			// which deletes the key from state above -- is simply ABSENT here. The CLI
			// does NOT re-inject its compiled-in default. Omitting the key reproduces
			// that, so tests that clear a flag exercise the absent-field decode path in
			// refreshSettingsFromAgent (a nil *bool) rather than a fabricated default.
			effective := map[string]interface{}{}
			if v, ok := state["outputStyle"]; ok && v != nil {
				effective["outputStyle"] = v
			}
			if v, ok := state["fastMode"]; ok && v != nil {
				effective["fastMode"] = v
			}
			if v, ok := state["alwaysThinkingEnabled"]; ok && v != nil {
				effective["alwaysThinkingEnabled"] = v
			}

			model := "opus[1m]"
			if v, ok := state["model"].(string); ok {
				model = v
			}
			effort := "high"
			if v, ok := state["effortLevel"].(string); ok {
				effort = v
			}

			responseBody = map[string]interface{}{
				"effective": effective,
				"applied": map[string]interface{}{
					"model":  model,
					"effort": effort,
				},
				"sources": []interface{}{},
			}
		default:
			responseBody = map[string]interface{}{}
		}

		resp := map[string]interface{}{
			"type": "control_response",
			"response": map[string]interface{}{
				"subtype":    "success",
				"request_id": msg.RequestID,
				"response":   responseBody,
			},
		}
		data, _ := json.Marshal(resp)
		_, _ = fmt.Fprintln(os.Stdout, string(data))
		_ = os.Stdout.Sync()
	}

	os.Exit(0)
}

// newTestAgentWithControlProtocol creates a ClaudeCodeAgent backed by a mock
// process that handles control_request messages. The agent has sensible
// defaults set for testing UpdateSettings.
func newTestAgentWithControlProtocol(t *testing.T) *ClaudeCodeAgent {
	return newTestAgentWithControlProtocolEnv(t)
}

// newTestAgentWithControlProtocolEnv is newTestAgentWithControlProtocol with extra env vars
// forwarded to the mock process (e.g. GO_HELPER_RECORD_REQUESTS to capture apply_flag_settings).
func newTestAgentWithControlProtocolEnv(t *testing.T, extraEnv ...string) *ClaudeCodeAgent {
	t.Helper()
	a, err := spawnMockClaudeAgent(context.Background(), "TestHelperProcessWithControlProtocol",
		append([]string{"GO_WANT_HELPER_PROCESS_CONTROL=1"}, extraEnv...),
		Options{
			AgentID:    "test-ctrl",
			Options:    map[string]string{OptionIDModel: "opus[1m]"},
			WorkingDir: t.TempDir(),
			APITimeout: 5 * time.Second,
		},
		noopSink{})
	require.NoError(t, err)
	a.effort = "high"
	a.outputStyle = "default"
	a.availableOutputStyles = []string{"default", "Explanatory", "Learning"}
	a.fastMode = FastModeOff
	a.alwaysThinking = AlwaysThinkingOn
	return a
}

func stopTestAgent(a *ClaudeCodeAgent) {
	a.Stop()
	_ = a.Wait()
}
