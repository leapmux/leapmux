package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

func TestAvailableOptionGroups_FastModeWhenAvailable(t *testing.T) {
	a := &ClaudeCodeAgent{
		fastModeAvailable: true,
		fastMode:          "on",
		alwaysThinking:    "on",
	}
	groups := a.AvailableOptionGroups()

	var fastGroup *leapmuxv1.AvailableOptionGroup
	for _, g := range groups {
		if g.Key == ExtraKeyFastMode {
			fastGroup = g
			break
		}
	}
	require.NotNil(t, fastGroup, "fast mode group should be present when available")
	assert.Len(t, fastGroup.Options, 2)

	for _, opt := range fastGroup.Options {
		if opt.Id == "on" {
			assert.True(t, opt.IsDefault)
		} else {
			assert.False(t, opt.IsDefault)
		}
	}
}

func TestAvailableOptionGroups_NoFastModeWhenUnavailable(t *testing.T) {
	a := &ClaudeCodeAgent{
		alwaysThinking: "on",
	}
	groups := a.AvailableOptionGroups()
	for _, g := range groups {
		assert.NotEqual(t, ExtraKeyFastMode, g.Key, "should not have fast mode group when unavailable")
	}
}

func TestAvailableOptionGroups_AlwaysIncludesThinking(t *testing.T) {
	a := &ClaudeCodeAgent{
		alwaysThinking: "off",
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
		if opt.Id == "off" {
			assert.True(t, opt.IsDefault)
		} else {
			assert.False(t, opt.IsDefault)
		}
	}
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

	a.alwaysThinking = "off"

	result := a.UpdateSettings(&leapmuxv1.AgentSettings{
		Model:  a.model,
		Effort: a.effort,
		ExtraSettings: map[string]string{
			ExtraKeyAlwaysThinking: "on",
		},
	})
	assert.True(t, result)
	assert.Equal(t, "on", a.alwaysThinking)
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

// --- Unit tests for ClearContext ---

func TestClearContext_SendsClearCommand(t *testing.T) {
	a := newTestAgentWithControlProtocol(t)
	defer stopTestAgent(a)

	sessionID, ok := a.ClearContext()
	assert.True(t, ok, "ClearContext should return true for a running agent")
	assert.Empty(t, sessionID, "session ID should be empty (updated asynchronously)")
}

func TestClearContext_FailsWhenStopped(t *testing.T) {
	ctx := context.Background()
	agent, err := mockStart(ctx, Options{
		AgentID:    "clear-stop-test",
		Model:      "test",
		WorkingDir: t.TempDir(),
	}, noopSink{})
	require.NoError(t, err)

	agent.Stop()
	_ = agent.Wait()

	_, ok := agent.ClearContext()
	assert.False(t, ok, "ClearContext should return false when agent is stopped")
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
	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestHelperProcessWithControlProtocol", "--")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS_CONTROL=1")
	cmd.Dir = t.TempDir()

	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)

	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)

	cmd.Stderr = nil

	a := &ClaudeCodeAgent{
		processBase: processBase{
			agentID:     "test-ctrl",
			cmd:         cmd,
			stdin:       stdin,
			ctx:         ctx,
			cancel:      cancel,
			processDone: make(chan struct{}),
			stderrDone:  make(chan struct{}),
			apiTimeout:  5 * time.Second,
		},
		model:                 "opus[1m]",
		effort:                "high",
		outputStyle:           "default",
		availableOutputStyles: []string{"default", "Explanatory", "Learning"},
		fastMode:              "off",
		alwaysThinking:        "on",
		workingDir:            t.TempDir(),
		sink:                  noopSink{},
		pendingControl:        make(map[string]chan<- claudeCodeControlResult),
	}
	close(a.stderrDone)

	require.NoError(t, cmd.Start())

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	go a.readOutputLoop(scanner)

	return a
}

func stopTestAgent(a *ClaudeCodeAgent) {
	a.Stop()
	_ = a.Wait()
}
