package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
	groups := a.AvailableOptionGroups()

	var styleGroup *leapmuxv1.AvailableOptionGroup
	for _, g := range groups {
		if g.Key == ExtraKeyOutputStyle {
			styleGroup = g
			break
		}
	}
	require.NotNil(t, styleGroup, "output style group should be present")
	assert.Equal(t, "Output Style", styleGroup.Label)
	assert.Len(t, styleGroup.Options, 3)

	for _, opt := range styleGroup.Options {
		if opt.Id == "Explanatory" {
			assert.True(t, opt.IsDefault, "Explanatory should be marked as default")
		} else {
			assert.False(t, opt.IsDefault, "%s should not be default", opt.Id)
		}
	}
}

func TestAvailableOptionGroups_NoOutputStylesWhenEmpty(t *testing.T) {
	a := &ClaudeCodeAgent{
		alwaysThinking: "on",
	}
	groups := a.AvailableOptionGroups()
	for _, g := range groups {
		assert.NotEqual(t, ExtraKeyOutputStyle, g.Key, "should not have output style group when empty")
	}
}

func TestAvailableOptionGroups_FastModeOn(t *testing.T) {
	a := &ClaudeCodeAgent{
		fastMode:       "on",
		alwaysThinking: "on",
	}
	groups := a.AvailableOptionGroups()

	var fastGroup *leapmuxv1.AvailableOptionGroup
	for _, g := range groups {
		if g.Key == ExtraKeyFastMode {
			fastGroup = g
			break
		}
	}
	require.NotNil(t, fastGroup, "fast mode group should always be present")
	assert.Len(t, fastGroup.Options, 2)

	for _, opt := range fastGroup.Options {
		if opt.Id == "on" {
			assert.True(t, opt.IsDefault)
		} else {
			assert.False(t, opt.IsDefault)
		}
	}
}

func TestAvailableOptionGroups_FastModeDefaultsToOff(t *testing.T) {
	a := &ClaudeCodeAgent{
		alwaysThinking: "on",
	}
	groups := a.AvailableOptionGroups()

	var fastGroup *leapmuxv1.AvailableOptionGroup
	for _, g := range groups {
		if g.Key == ExtraKeyFastMode {
			fastGroup = g
			break
		}
	}
	require.NotNil(t, fastGroup, "fast mode group should always be present")
	for _, opt := range fastGroup.Options {
		if opt.Id == "off" {
			assert.True(t, opt.IsDefault, "off should be default")
		} else {
			assert.False(t, opt.IsDefault, "on should not be default")
		}
	}
}

func TestAvailableOptionGroups_AlwaysIncludesThinking(t *testing.T) {
	a := &ClaudeCodeAgent{
		alwaysThinking: AlwaysThinkingOff,
	}
	groups := a.AvailableOptionGroups()

	var thinkingGroup *leapmuxv1.AvailableOptionGroup
	for _, g := range groups {
		if g.Key == ExtraKeyAlwaysThinking {
			thinkingGroup = g
			break
		}
	}
	require.NotNil(t, thinkingGroup, "thinking group should always be present")
	for _, opt := range thinkingGroup.Options {
		if opt.Id == AlwaysThinkingOff {
			assert.True(t, opt.IsDefault)
		} else {
			assert.False(t, opt.IsDefault)
		}
	}
}

// The enabled option's display name tracks Claude Code's per-model
// thinking-type gate: "Adaptive" for first-party models, "On" for Haiku
// (legacy type:"enabled"). The option ID stays "on" for all models.
func TestAvailableOptionGroups_ThinkingLabelsByModel(t *testing.T) {
	cases := []struct {
		model     string
		wantName  string
		current   string // initial a.alwaysThinking
		wantOnDef bool   // whether the "on" option is IsDefault
	}{
		{"opus", "Adaptive", AlwaysThinkingOn, true},
		{"opus[1m]", "Adaptive", AlwaysThinkingOn, true},
		{"sonnet", "Adaptive", AlwaysThinkingOn, true},
		{"sonnet[1m]", "Adaptive", AlwaysThinkingOff, false},
		{"haiku", "On", AlwaysThinkingOn, true},
		{"haiku", "On", AlwaysThinkingOff, false},
		{"", "Adaptive", AlwaysThinkingOn, true},
		{"unknown-future-model", "Adaptive", AlwaysThinkingOn, true},
	}
	for _, tc := range cases {
		t.Run(tc.model+"/"+tc.current, func(t *testing.T) {
			a := &ClaudeCodeAgent{model: tc.model, alwaysThinking: tc.current}
			groups := a.AvailableOptionGroups()

			var g *leapmuxv1.AvailableOptionGroup
			for _, gr := range groups {
				if gr.Key == ExtraKeyAlwaysThinking {
					g = gr
					break
				}
			}
			require.NotNil(t, g)
			require.Len(t, g.Options, 2, "thinking group has exactly two options (enabled + off)")

			enabled := g.Options[0]
			off := g.Options[1]
			assert.Equal(t, AlwaysThinkingOn, enabled.Id, "enabled option ID is always 'on'")
			assert.Equal(t, tc.wantName, enabled.Name, "enabled option name varies by model")
			assert.Equal(t, tc.wantOnDef, enabled.IsDefault, "'on' IsDefault")
			assert.Equal(t, AlwaysThinkingOff, off.Id)
			assert.Equal(t, "Off", off.Name)
			assert.Equal(t, !tc.wantOnDef, off.IsDefault, "'off' IsDefault")
		})
	}
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
		outputStyle:             "Explanatory",
		fastMode:                "on",
		alwaysThinking:          "off",
	}
	s := a.CurrentSettings()
	assert.Equal(t, "sonnet", s.Model)
	assert.Equal(t, "low", s.Effort)
	assert.Equal(t, "Explanatory", s.ExtraSettings[ExtraKeyOutputStyle])
	assert.Equal(t, "on", s.ExtraSettings[ExtraKeyFastMode])
	assert.Equal(t, "off", s.ExtraSettings[ExtraKeyAlwaysThinking])
}

// --- Unit tests for UpdateSettings logic (no-op / validation) ---

func TestUpdateSettings_NothingChanged(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  a.model,
		Effort: a.effort,
		ExtraSettings: map[string]string{
			ExtraKeyOutputStyle:    a.outputStyle,
			ExtraKeyFastMode:       a.fastMode,
			ExtraKeyAlwaysThinking: a.alwaysThinking,
		},
	})
	assert.True(t, result, "should return true when nothing changed")
}

func TestUpdateSettings_OutputStyleInvalid(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.availableOutputStyles = []string{"default", "Explanatory"}
	a.outputStyle = "default"

	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  a.model,
		Effort: a.effort,
		ExtraSettings: map[string]string{
			ExtraKeyOutputStyle: "NonExistent",
		},
	})
	assert.False(t, result, "should return false for invalid output style")
	assert.Equal(t, "default", a.outputStyle, "output style should not change")
}

func TestUpdateSettings_ModelChange(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  "sonnet",
		Effort: a.effort,
	})
	assert.True(t, result, "should return true for model change via apply_flag_settings")
	assert.Equal(t, "sonnet", a.model, "model should be updated from get_settings response")
}

func TestUpdateSettings_EffortChange(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  a.model,
		Effort: "low",
	})
	assert.True(t, result, "should return true for effort change")
	assert.Equal(t, "low", a.effort, "effort should be updated from get_settings response")
}

func TestUpdateSettings_OutputStyleChange(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.availableOutputStyles = []string{"default", "Explanatory", "Learning"}

	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  a.model,
		Effort: a.effort,
		ExtraSettings: map[string]string{
			ExtraKeyOutputStyle: "Explanatory",
		},
	})
	assert.True(t, result, "should return true for output style change")
	assert.Equal(t, "Explanatory", a.outputStyle, "output style should be updated")
}

func TestUpdateSettings_FastModeOn(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.fastMode = "off"

	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  a.model,
		Effort: a.effort,
		ExtraSettings: map[string]string{
			ExtraKeyFastMode: "on",
		},
	})
	assert.True(t, result)
	assert.Equal(t, "on", a.fastMode)
}

func TestUpdateSettings_AlwaysThinkingOff(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  a.model,
		Effort: a.effort,
		ExtraSettings: map[string]string{
			ExtraKeyAlwaysThinking: "off",
		},
	})
	assert.True(t, result)
	assert.Equal(t, "off", a.alwaysThinking)
}

func TestUpdateSettings_FastModeOff(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.fastMode = "on"

	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  a.model,
		Effort: a.effort,
		ExtraSettings: map[string]string{
			ExtraKeyFastMode: "off",
		},
	})
	assert.True(t, result)
	assert.Equal(t, "off", a.fastMode)
}

func TestUpdateSettings_AlwaysThinkingOn(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.alwaysThinking = AlwaysThinkingOff

	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  a.model,
		Effort: a.effort,
		ExtraSettings: map[string]string{
			ExtraKeyAlwaysThinking: AlwaysThinkingOn,
		},
	})
	assert.True(t, result)
	assert.Equal(t, AlwaysThinkingOn, a.alwaysThinking)
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
	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  "sonnet",
		Effort: a.effort,
	})
	assert.False(t, result, "should return false when apply_flag_settings fails")
	assert.Equal(t, originalModel, a.model, "model should not change on failure")
}

func TestUpdateSettings_MultipleChanges(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	a.availableOutputStyles = []string{"default", "Learning"}
	a.fastMode = "off"

	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  "sonnet",
		Effort: "low",
		ExtraSettings: map[string]string{
			ExtraKeyOutputStyle: "Learning",
			ExtraKeyFastMode:    "on",
		},
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
				Settings map[string]interface{} `json:"settings"`
			} `json:"request"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.Type != "control_request" {
			_, _ = fmt.Fprintln(os.Stdout, line)
			continue
		}

		var responseBody interface{}
		switch msg.Request.Subtype {
		case "apply_flag_settings":
			for k, v := range msg.Request.Settings {
				if v == nil {
					delete(state, k)
				} else {
					state[k] = v
				}
			}
			responseBody = map[string]interface{}{}
		case "get_settings":
			// Build effective settings from state, applying defaults for
			// absent (null-deleted) boolean settings.
			effective := map[string]interface{}{}
			if v, ok := state["outputStyle"]; ok && v != nil {
				effective["outputStyle"] = v
			}
			if v, ok := state["fastMode"]; ok && v != nil {
				effective["fastMode"] = v
			} else {
				effective["fastMode"] = false // default: off
			}
			if v, ok := state["alwaysThinkingEnabled"]; ok && v != nil {
				effective["alwaysThinkingEnabled"] = v
			} else {
				effective["alwaysThinkingEnabled"] = true // default: on
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
	t.Helper()
	a, err := spawnMockClaudeAgent(context.Background(), "TestHelperProcessWithControlProtocol",
		[]string{"GO_WANT_HELPER_PROCESS_CONTROL=1"},
		Options{
			AgentID:    "test-ctrl",
			Model:      "opus[1m]",
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
