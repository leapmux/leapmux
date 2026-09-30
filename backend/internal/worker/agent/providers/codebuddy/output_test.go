package codebuddy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestCodebuddyResultBackfillsAnAnswerThatReasoningDidNotStream(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","session_id":"session-1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"I inspect the numbers first."}]}}`))
	a.HandleOutput([]byte(`{"type":"result","session_id":"session-1","result":"The answer is 6,912.","is_error":false}`))

	messages := sink.Messages()
	require.Len(t, messages, 3, "the missing answer appears between the native thinking and result frames")
	var answer struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal(messages[1].Content, &answer))
	assert.Equal(t, contracts.CodebuddyFrameKindAssistant, answer.Type)
	assert.Equal(t, "assistant", answer.Message.Role)
	require.Len(t, answer.Message.Content, 1)
	assert.Equal(t, "text", answer.Message.Content[0].Type)
	assert.Equal(t, "The answer is 6,912.", answer.Message.Content[0].Text)
	assert.Contains(t, string(messages[2].Content), `"type":"result"`, "the native result still ends the turn")
}

func TestCodebuddyEnterPlanModeUpdatesOnlyAfterSuccessfulNativeResult(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		result string
		want   string
	}{
		{
			name:   "successful result",
			result: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"enter-plan","is_error":false,"content":[{"type":"text","text":"Entered plan mode."}],"_meta":{"rawResponse":{"renderer":{"type":"enterplan"}}}}]}}`,
			want:   contracts.CodebuddyModePlan,
		},
		{
			name:   "failed result",
			result: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"enter-plan","is_error":true,"content":[{"type":"text","text":"Plan mode failed."}]}]}}`,
		},
		{
			name:   "unrelated result",
			result: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"other-tool","is_error":false,"content":[{"type":"text","text":"Done."}]}]}}`,
		},
		{
			name:   "result without status",
			result: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"enter-plan","content":[{"type":"text","text":"Unknown outcome."}]}]}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"enter-plan","name":"EnterPlanMode","input":{}}]}}`))
			assert.Empty(t, sink.PermissionModes(), "a tool request does not confirm a mode change")
			a.HandleOutput([]byte(tc.result))
			assert.Equal(t, tc.want, sink.PermissionMode())
			if tc.want != "" {
				assert.Equal(t, tc.want, a.permissionMode)
			} else {
				assert.Equal(t, "default", a.permissionMode)
			}
		})
	}
}

func TestCodebuddyNativePlanToolRequestsASessionPreservingRestart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		frames []string
		want   bool
	}{
		{
			name: "successful plan entry persists through turn end",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"EnterPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":false,"content":[{"type":"text","text":"Entered plan mode."}]}]}}`,
				`{"type":"result","result":"Ready to plan.","is_error":false}`,
			},
			want: true,
		},
		{
			name: "rejected plan exit persists through the next init",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"ExitPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this plan yet. Keep planning."}]}]}}`,
				`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`,
			},
			want: true,
		},
		{
			name: "approved plan exit requires its new launch mode",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"ExitPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":false,"content":[{"type":"text","text":"The plan is approved."}]}]}}`,
				`{"type":"result","result":"Implementing the plan.","is_error":false}`,
			},
			want: true,
		},
		{
			name: "failed plan entry does not request restart",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"EnterPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"Could not enter plan mode."}]}]}}`,
				`{"type":"result","result":"Plan failed.","is_error":true}`,
			},
		},
		{
			name:   "native init without a plan tool does not request restart",
			frames: []string{`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.setTurnActive(true)
			for _, frame := range tc.frames {
				a.HandleOutput([]byte(frame))
			}
			restarter, ok := any(a).(interface{ NativeTurnRestartRequired() bool })
			require.True(t, ok, "CodeBuddy must report tool-driven mode changes before the next queued input")
			assert.Equal(t, tc.want, restarter.NativeTurnRestartRequired())
		})
	}
}

func TestCodebuddyNativePlanToolDoesNotRestartWhenLaunchModeAlreadyMatches(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		frames []string
	}{
		{
			name: "plan entry",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"EnterPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":false,"content":[{"type":"text","text":"Entered plan mode."}]}]}}`,
				`{"type":"result","result":"Ready to plan.","is_error":false}`,
			},
		},
		{
			name: "rejected exit",
			frames: []string{
				`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"ExitPlanMode","input":{}}]}}`,
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this plan yet. Keep planning."}]}]}}`,
				`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := newOfflineAgent(t, &agenttest.Sink{})
			a.opts = agent.Options{Options: map[string]string{agent.OptionIDPermissionMode: contracts.CodebuddyModePlan}}
			a.permissionMode = contracts.CodebuddyModePlan
			a.setTurnActive(true)
			for _, frame := range tc.frames {
				a.HandleOutput([]byte(frame))
			}
			assert.False(t, a.NativeTurnRestartRequired(), "the launch mode already carries native Plan into the next prompt")
		})
	}
}

func TestCodebuddyInitPublishesNativePermissionMode(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`))
	assert.Equal(t, contracts.CodebuddyModePlan, a.permissionMode)
	assert.Equal(t, contracts.CodebuddyModePlan, sink.PermissionMode())

	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1"}`))
	assert.Equal(t, contracts.CodebuddyModePlan, a.permissionMode, "an absent mode leaves the last native state alone")
	assert.Equal(t, []string{contracts.CodebuddyModePlan}, sink.PermissionModes())

	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"acceptEdits"}`))
	assert.Equal(t, contracts.CodebuddyModeAcceptEdits, a.permissionMode)
	assert.Equal(t, []string{contracts.CodebuddyModePlan, contracts.CodebuddyModeAcceptEdits}, sink.PermissionModes())
}

func TestCodebuddyRejectedPlanExitSettlesAtNextInit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		tool       string
		result     string
		wantActive bool
	}{
		{
			name:   "denied plan exit",
			tool:   "ExitPlanMode",
			result: `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this plan yet. They want to keep planning."}]}]}}`,
		},
		{
			name:       "failed plan exit",
			tool:       "ExitPlanMode",
			result:     `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"Could not read the plan file."}]}]}}`,
			wantActive: true,
		},
		{
			name:       "approved plan exit",
			tool:       "ExitPlanMode",
			result:     `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":false,"content":[{"type":"text","text":"The plan is approved."}]}]}}`,
			wantActive: true,
		},
		{
			name:       "unrelated tool failure",
			tool:       "Bash",
			result:     `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"Command failed."}]}]}}`,
			wantActive: true,
		},
		{
			name:       "plan result with no outcome",
			tool:       "ExitPlanMode",
			result:     `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","content":[{"type":"text","text":"Unknown outcome."}]}]}}`,
			wantActive: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			use := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"` + tc.tool + `","input":{}}]}}`
			a.HandleOutput([]byte(use))
			a.HandleOutput([]byte(tc.result))
			active, published := sink.LastTurnActive()
			assert.True(t, published)
			assert.True(t, active, "the native client has not stated a new input boundary yet")

			a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`))
			active, published = sink.LastTurnActive()
			assert.True(t, published)
			assert.Equal(t, tc.wantActive, active)
			if !tc.wantActive {
				require.NoError(t, a.SendInput("Present the revised plan.", nil))
			}
		})
	}
}

func TestCodebuddyResultClearsRejectedPlanExitBeforeNextInit(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"plan-call","name":"ExitPlanMode","input":{}}]}}`))
	a.HandleOutput([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"plan-call","is_error":true,"content":[{"type":"text","text":"The user doesn't want to proceed with this plan yet."}]}]}}`))
	a.HandleOutput([]byte(`{"type":"result","result":"","is_error":false}`))
	states := sink.TurnActives()
	require.NotEmpty(t, states)
	assert.False(t, states[len(states)-1])

	a.HandleOutput([]byte(`{"type":"system","subtype":"init","session_id":"session-1","permissionMode":"plan"}`))
	assert.Equal(t, states, sink.TurnActives(), "a later init does not publish a second end for the old turn")
}

func TestCodebuddyResultDoesNotDuplicateStreamedText(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"The answer is 6,912."}]}}`))
	a.HandleOutput([]byte(`{"type":"result","result":"The answer is 6,912.","is_error":false}`))

	messages := sink.Messages()
	require.Len(t, messages, 2, "a streamed answer needs no second assistant row")
	assert.Contains(t, string(messages[0].Content), `"type":"text"`)
	assert.Contains(t, string(messages[1].Content), `"type":"result"`)
}

func TestCodebuddyResultBackfillsAfterAPriorTurnStreamedText(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"First answer."}]}}`))
	a.HandleOutput([]byte(`{"type":"result","result":"First answer."}`))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Second thought."}]}}`))
	a.HandleOutput([]byte(`{"type":"result","result":"Second answer."}`))

	messages := sink.Messages()
	require.Len(t, messages, 5)
	assert.Contains(t, string(messages[3].Content), `"text":"Second answer."`)
	assert.Contains(t, string(messages[4].Content), `"type":"result"`)
}

func TestCodebuddyResultDoesNotBackfillAnErrorOrEmptyAnswer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		result string
	}{
		{name: "error", result: `{"type":"result","result":"The request failed.","is_error":true}`},
		{name: "blank", result: `{"type":"result","result":" ","is_error":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newOfflineAgent(t, sink)
			a.HandleOutput([]byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Thinking."}]}}`))
			a.HandleOutput([]byte(tc.result))
			assert.Len(t, sink.Messages(), 2)
		})
	}
}

func TestCodebuddyAvailableModelsResponseKeepsNativeIds(t *testing.T) {
	t.Parallel()
	a := newOfflineAgent(t, &agenttest.Sink{})
	response := make(chan codebuddyControlResult, 1)
	a.pendingControl["models-1"] = response
	line := providerkit.ParseLine([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"models-1","response":{"availableModels":[{"modelId":"custom-local:primary","name":"Primary"},{"modelId":"custom-local:alternate","name":"Alternate"}]}}}`))

	require.True(t, a.handlePendingControlResponse(line))
	result := <-response
	require.True(t, result.Success)
	require.True(t, result.HasModelCatalog)
	assert.Equal(t, []codebuddyModelInfo{
		{ID: "custom-local:primary", Name: "Primary"},
		{ID: "custom-local:alternate", Name: "Alternate"},
	}, result.Models)
}

func TestCodebuddyResultReportsPrimaryModelContext(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.model = "custom-local:primary"
	a.handleResult([]byte(`{
      "type":"result",
      "usage":{"input_tokens":13000,"output_tokens":50},
      "modelUsage":{
        "custom-local:primary":{"inputTokens":12000,"outputTokens":40,"contextWindow":128000},
        "custom-local:child":{"inputTokens":1000,"outputTokens":10,"contextWindow":64000}
      },
      "_meta":{"codebuddy.ai/contextUsed":12000}
    }`))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens:              int64(12000),
		contracts.ContextUsageFieldOutputTokens:             int64(40),
		contracts.ContextUsageFieldCacheCreationInputTokens: int64(0),
		contracts.ContextUsageFieldCacheReadInputTokens:     int64(0),
		contracts.ContextUsageFieldContextTokens:            int64(12000),
		contracts.ContextUsageFieldContextWindow:            int64(128000),
	}, value)
}

func TestCodebuddyResultUsesAggregateCountsWithoutAModelMatch(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.model = "custom-local:primary"
	a.handleResult([]byte(`{
      "type":"result",
      "usage":{"input_tokens":25,"output_tokens":4,"cache_read_input_tokens":7},
      "modelUsage":{
        "custom-local:secondary":{"inputTokens":10,"contextWindow":32000},
        "custom-local:child":{"inputTokens":15,"contextWindow":16000}
      }
    }`))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens:              int64(25),
		contracts.ContextUsageFieldOutputTokens:             int64(4),
		contracts.ContextUsageFieldCacheCreationInputTokens: int64(0),
		contracts.ContextUsageFieldCacheReadInputTokens:     int64(7),
	}, value)
}

func TestCodebuddyResultKeepsAnExplicitZeroContext(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.handleResult([]byte(`{"type":"result","usage":{"input_tokens":25},"_meta":{"codebuddy.ai/contextUsed":0}}`))

	value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	usage, ok := value.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, int64(0), usage[contracts.ContextUsageFieldContextTokens])
}

func TestCodebuddyResultWithoutValidUsageDoesNotBroadcast(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)
	a.handleResult([]byte(`{"type":"result"}`))
	a.handleResult([]byte(`{"type":"result","usage":"malformed"}`))

	_, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	assert.False(t, ok)
}
