package antigravity

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityTurnFrames(t *testing.T) {
	agenttest.AssertTurnFrames(t, []agenttest.TurnFrameCase{
		{Name: "running", Line: `{"status":"CASCADE_RUN_STATUS_RUNNING"}`, Moves: true},
		{Name: "idle", Line: `{"status":"CASCADE_RUN_STATUS_IDLE"}`},
		{Name: "busy", Line: `{"status":"CASCADE_RUN_STATUS_BUSY"}`, Moves: true},
	}, func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		a, sink := testAgent(t)
		a.HandleOutput([]byte(tc.Line))
		return sink.TurnActiveCalls
	})
}

func TestAntigravityRejectsMissingAndReplacedSessions(t *testing.T) {
	a, _ := testAgent(t)
	agenttest.AssertRejectsMissingAndReplacedSessions(t, a)
}

func TestAntigravityBusyRefusalRepublishesTheTurn(t *testing.T) {
	a, sink := testAgent(t)
	a.beginTurn(a.root)
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, a.SendInput("later", nil))
}

func TestAntigravityRisingTurnTokens(t *testing.T) {
	a, sink := testAgent(t)
	agenttest.AssertRisingTurnTokens(t, sink, a)
}

func TestAntigravityUnknownStepPersists(t *testing.T) {
	agenttest.AssertUnknownFramePersists(t, []agenttest.UnknownFrameCase{
		{Name: "future step", Frame: []byte(`{"type":"CORTEX_STEP_TYPE_FUTURE","status":"CORTEX_STEP_STATUS_DONE","future":{"large":9007199254740993}}`)},
	}, func(t *testing.T, tc agenttest.UnknownFrameCase) *agenttest.Sink {
		a, sink := testAgent(t)
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{tc.Frame}))
		return sink
	})
}

func TestAntigravityRetryAttempt(t *testing.T) {
	agenttest.RunRetryAttemptCase(t, agenttest.RetryAttemptCase{
		Name: "Antigravity stream retry keeps partial model history", FramesPath: "testdata/retry_partial.jsonl",
		NewAgent: func(t *testing.T, sink *agenttest.Sink) func([]byte) {
			a := newAgentState(agent.NewProviderServices(sink), antigravityRPC{}, t.TempDir(), nil)
			a.Mu.Lock()
			a.installRoot("cascade", false)
			a.Mu.Unlock()
			return a.HandleOutput
		},
		TextOf: func(row agenttest.Message) (agenttest.RetryAttemptText, bool) {
			var step antigravityStep
			if json.Unmarshal(row.Content, &step) != nil || step.Type != contracts.AntigravityStepTypePlannerResponse {
				return agenttest.RetryAttemptText{}, false
			}
			return agenttest.RetryAttemptText{Text: step.PlannerResponse.ModifiedResponse, Completion: row.Completion}, true
		},
		Want: []agenttest.RetryAttemptText{{Text: "HELLO WRONG", Completion: agent.MessageCompletionComplete}, {Text: "HI THERE", Completion: agent.MessageCompletionComplete}},
	})
}

func TestAntigravityCumulativePlannerText(t *testing.T) {
	a, sink := testAgent(t)
	for index, text := range []string{"HEL", "HELLO ", "HELLO WORLD"} {
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{plannerStep("CORTEX_STEP_STATUS_RUNNING", text)}))
		_ = index
	}
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{plannerStep(contracts.AntigravityStepStatusDone, "HELLO WORLD")}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	require.Len(t, transcriptMessages(sink), 1)
	var step antigravityStep
	require.NoError(t, json.Unmarshal(transcriptMessages(sink)[0].Content, &step))
	assert.Equal(t, "HELLO WORLD", step.PlannerResponse.ModifiedResponse)
}

func TestAntigravityRewrittenPlannerTextDoesNotEmitNegativeDelta(t *testing.T) {
	a, sink := testAgent(t)
	for _, text := range []string{"HELLO", "HI", "HI THERE"} {
		feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{plannerStep("CORTEX_STEP_STATUS_RUNNING", text)}))
	}
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{plannerStep(contracts.AntigravityStepStatusDone, "HI THERE")}))
	require.Len(t, transcriptMessages(sink), 1)
	assert.Contains(t, string(transcriptMessages(sink)[0].Content), "HI THERE")
}

func TestAntigravityPlannerThinkingAndTextPersistAsSeparateRows(t *testing.T) {
	a, sink := testAgent(t)
	raw, _ := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypePlannerResponse, "status": contracts.AntigravityStepStatusDone,
		"plannerResponse": map[string]any{"modifiedResponse": "visible answer", "thinking": "private reasoning"},
	})
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{raw}))
	messages := transcriptMessages(sink)
	require.Len(t, messages, 2)
	var thinking, text antigravityStep
	require.NoError(t, json.Unmarshal(messages[0].Content, &thinking))
	require.NoError(t, json.Unmarshal(messages[1].Content, &text))
	assert.Equal(t, "private reasoning", thinking.PlannerResponse.Thinking)
	assert.Empty(t, thinking.PlannerResponse.ModifiedResponse)
	assert.Empty(t, thinking.PlannerResponse.Response)
	assert.Equal(t, "visible answer", text.PlannerResponse.ModifiedResponse)
	assert.Equal(t, "private reasoning", text.PlannerResponse.Thinking)
}

func TestAntigravityChildUserInputPersistsWithoutRootDuplication(t *testing.T) {
	a, sink := testAgent(t)
	child := newCascadeState("child", a.sink, 0, false)
	child.parent = a.root
	raw, _ := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypeUserInput, "status": contracts.AntigravityStepStatusDone,
		"userInput": map[string]any{"userResponse": "original child prompt"},
	})
	var step antigravityStep
	require.NoError(t, json.Unmarshal(raw, &step))
	assert.NoError(t, a.persistStep(child, 0, step, raw))
	require.Len(t, transcriptMessages(sink), 1)
	assert.NoError(t, a.persistStep(a.root, 0, step, raw))
	require.Len(t, transcriptMessages(sink), 1)
}

func TestAntigravityTurnEndCountsCurrentTurnToolsAndUsage(t *testing.T) {
	a, sink := testAgent(t)
	tool, _ := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypeRunCommand, "status": contracts.AntigravityStepStatusDone,
		"metadata": map[string]any{
			"toolCall":   map[string]any{"id": "call-1", "name": "run_command"},
			"modelUsage": map[string]string{"inputTokens": "123", "cacheReadTokens": "23", "cacheWriteTokens": "4", "outputTokens": "8"},
		},
		"runCommand": map[string]any{"exitCode": 0},
	})
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{tool}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusRunning, ContextWindowUsage: &contextWindowUsage{EstimatedTokensUsed: 154, MaxContextTokens: 1000}})
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	messages := sink.Messages()
	require.NotEmpty(t, messages)
	turnEnd := messages[len(messages)-1]
	require.True(t, turnEnd.TurnEnd)
	var frame struct {
		NumToolUses int `json:"num_tool_uses"`
		Metadata    struct {
			ModelUsage map[string]int `json:"modelUsage"`
		} `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(turnEnd.Content, &frame))
	assert.Equal(t, 1, frame.NumToolUses)
	assert.Equal(t, 123, frame.Metadata.ModelUsage["inputTokens"])
	usage, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens: int64(123), contracts.ContextUsageFieldCacheReadInputTokens: int64(23),
		contracts.ContextUsageFieldCacheCreationInputTokens: int64(4), contracts.ContextUsageFieldOutputTokens: int64(8),
		contracts.ContextUsageFieldContextTokens: int64(154), contracts.ContextUsageFieldContextWindow: int64(1000),
	}, usage)
}

func TestAntigravityErrorThenRetryPublishesNoAgentError(t *testing.T) {
	a, sink := testAgent(t)
	errorStep, _ := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypeErrorMessage, "status": contracts.AntigravityStepStatusDone,
		"errorMessage": map[string]any{"error": map[string]any{"userErrorMessage": "Agent execution terminated due to error.", "shortError": "boom", "modelErrorMessage": "model reason", "fullError": "full reason"}},
	})
	var original map[string]any
	require.NoError(t, json.Unmarshal(errorStep, &original))
	original["diagnostics"] = map[string]any{"futureField": "retained"}
	errorStep, _ = json.Marshal(original)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{errorStep}))
	update := agentStateUpdate{Status: cascadeStatusRunning}
	update.ExecutionStatus.ModelRequest = &modelRequestStatus{Attempt: 1, MaxAttempts: 9}
	feedUpdate(t, a, update)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{1}, []json.RawMessage{plannerStep(contracts.AntigravityStepStatusDone, "recovered")}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), "modelRequest")
	assert.NotContains(t, string(sink.PersistedNotifications()[0].Content), "ERROR_MESSAGE")
}

func TestAntigravityFatalModelErrorPublishesShortReason(t *testing.T) {
	a, sink := testAgent(t)
	errorStep, _ := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypeErrorMessage, "status": contracts.AntigravityStepStatusDone,
		"errorMessage": map[string]any{"error": map[string]any{"userErrorMessage": "Agent execution terminated due to error.", "shortError": "boom", "modelErrorMessage": "model reason", "fullError": "full reason"}},
	})
	var original map[string]any
	require.NoError(t, json.Unmarshal(errorStep, &original))
	original["diagnostics"] = map[string]any{"futureField": "retained"}
	errorStep, _ = json.Marshal(original)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{errorStep}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Contains(t, string(sink.PersistedNotifications()[0].Content), "boom")
	var saved map[string]any
	require.NoError(t, json.Unmarshal(sink.PersistedNotifications()[0].Content, &saved))
	assert.Equal(t, original["errorMessage"], saved["errorMessage"])
	assert.Equal(t, original["diagnostics"], saved["diagnostics"])
}

func TestAntigravityUnknownStepRetriesFailedPersistence(t *testing.T) {
	a, sink := testAgent(t)
	sink.PersistErr = errors.New("write refused")
	raw := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_FUTURE","status":"CORTEX_STEP_STATUS_DONE"}`)
	assert.NoError(t, a.persistUnknownStep(a.root, 0, raw))
	assert.True(t, a.root.steps[0].consumed, "the output queue owns the failed write")
	sink.PersistErr = nil
	require.NoError(t, agent.FlushPendingOutput(a.sink))
	assert.True(t, a.root.steps[0].consumed)
	require.Len(t, transcriptMessages(sink), 2)
}

func TestAntigravityDeniedToolDoesNotRetainErrorCompletion(t *testing.T) {
	a, sink := testAgent(t)
	denied, _ := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypeGeneric, "status": contracts.AntigravityStepStatusError,
		"metadata": map[string]any{"toolCall": map[string]any{"id": "denied", "name": "write_file"}},
		"error":    map[string]any{"userErrorMessage": "permission check failed: user denied permission for write_file(/p/a.txt)"},
	})
	var deniedStep antigravityStep
	require.NoError(t, json.Unmarshal(denied, &deniedStep))
	assert.NoError(t, a.persistStep(a.root, 0, deniedStep, denied))
	failed, _ := json.Marshal(map[string]any{
		"type": contracts.AntigravityStepTypeGeneric, "status": contracts.AntigravityStepStatusError,
		"metadata": map[string]any{"toolCall": map[string]any{"id": "failed", "name": "write_file"}},
		"error":    map[string]any{"userErrorMessage": "disk full"},
	})
	var failedStep antigravityStep
	require.NoError(t, json.Unmarshal(failed, &failedStep))
	assert.NoError(t, a.persistStep(a.root, 1, failedStep, failed))
	messages := transcriptMessages(sink)
	require.Len(t, messages, 2)
	assert.Equal(t, agent.MessageCompletionComplete, messages[0].Completion)
	assert.Equal(t, agent.MessageCompletionError, messages[1].Completion)
}

func TestAntigravityRunningToolPersistsItsRequestBeforeCompletion(t *testing.T) {
	a, sink := testAgent(t)
	running := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"held","name":"run_command","argumentsJson":"{\"CommandLine\":\"held-command\"}"}},"runCommand":{"commandLine":"held-command"}}`)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{running}))
	messages := transcriptMessages(sink)
	require.Len(t, messages, 1)
	assert.Contains(t, string(messages[0].Content), "held-command")
	assert.Empty(t, messages[0].Completion)
	done := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_DONE","metadata":{"toolCall":{"id":"held","name":"run_command","argumentsJson":"{\"CommandLine\":\"held-command\"}"}},"runCommand":{"commandLine":"held-command","exitCode":0}}`)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{done}))
	messages = transcriptMessages(sink)
	require.Len(t, messages, 2)
	assert.Equal(t, agent.MessageCompletionComplete, messages[1].Completion)
}

func TestAntigravityDonePlannerStepKeepsInterruptedCompletion(t *testing.T) {
	a, sink := testAgent(t)
	a.beginTurn(a.root)
	a.Mu.Lock()
	a.root.interrupted = true
	a.Mu.Unlock()
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{plannerStep(contracts.AntigravityStepStatusDone, "partial")}))
	messages := transcriptMessages(sink)
	require.Len(t, messages, 1)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[0].Completion)
}

func TestAntigravityScrubFailureAllowsPersistenceRetry(t *testing.T) {
	a, _ := testAgent(t)
	a.root.steps[0] = &stepState{}
	raw := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":`)
	assert.Error(t, a.persistLiveStep(a.root, 0, antigravityStep{}, raw, "call"))
	assert.False(t, a.root.steps[0].liveOutputAccepted)
	assert.Error(t, a.persistStep(a.root, 0, antigravityStep{}, raw))
	assert.False(t, a.root.steps[0].consumed)
}
