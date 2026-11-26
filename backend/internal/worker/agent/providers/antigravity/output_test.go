package antigravity

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, sink, a, agenttest.SendInput(a, "later", nil))
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

func TestAntigravityRepeatedToolUpdatesKeepOneRailPerCall(t *testing.T) {
	a, sink := testAgent(t)
	firstPending := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_GENERIC","status":"CORTEX_STEP_STATUS_PENDING","metadata":{"toolCall":{"id":"first","name":"run_command"}}}`)
	firstRunning := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"first","name":"run_command"}}}`)
	firstDone := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_DONE","metadata":{"toolCall":{"id":"first","name":"run_command"}}}`)
	secondPending := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_GENERIC","status":"CORTEX_STEP_STATUS_PENDING","metadata":{"toolCall":{"id":"second","name":"read_file"}}}`)
	secondDone := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_VIEW_FILE","status":"CORTEX_STEP_STATUS_DONE","metadata":{"toolCall":{"id":"second","name":"read_file"}}}`)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{firstPending}))
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0, 1}, []json.RawMessage{firstRunning, secondPending}))
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0, 1}, []json.RawMessage{firstRunning, secondPending}))
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0, 1}, []json.RawMessage{firstDone, secondDone}))

	rows := transcriptMessages(sink)
	require.Len(t, rows, 4)
	assert.Empty(t, rows[0].SpansOpenAtPersist)
	assert.Positive(t, rows[0].SpanColor)
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "first"}}, rows[1].SpansOpenAtPersist)
	assert.Positive(t, rows[1].SpanColor)
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "first"}, {SpanID: "second"}}, rows[2].SpansOpenAtPersist)
	assert.True(t, rows[2].Closing)
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "second"}}, rows[3].SpansOpenAtPersist)
	assert.True(t, rows[3].Closing)
	assert.Equal(t, []agenttest.SpanOpen{{SpanID: "first"}, {SpanID: "second"}}, sink.OpenSpans())
}

type refusingToolRequestSink struct {
	agent.ProviderServices
	refusal error
}

func (s *refusingToolRequestSink) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if s.refusal != nil {
		return s.refusal
	}
	return s.ProviderServices.PersistMessage(source, content, span)
}

func TestAntigravityRefusedToolRequestCanRetryWithoutAnOpenRail(t *testing.T) {
	a, sink := testAgent(t)
	writer := &refusingToolRequestSink{ProviderServices: a.root.sink, refusal: errors.New("write refused")}
	a.root.sink = writer
	a.root.steps[0] = &stepState{}
	raw := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_GENERIC","status":"CORTEX_STEP_STATUS_PENDING","metadata":{"toolCall":{"id":"call","name":"read_file"}}}`)

	require.ErrorIs(t, a.persistLiveStep(a.root, 0, raw, "call"), writer.refusal)
	assert.Empty(t, transcriptMessages(sink))
	assert.False(t, a.root.steps[0].liveOutputAccepted)

	writer.refusal = nil
	require.NoError(t, a.persistLiveStep(a.root, 0, raw, "call"))
	rows := transcriptMessages(sink)
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].SpansOpenAtPersist)
	assert.Positive(t, rows[0].SpanColor)
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

func TestAntigravityConsumesTextlessPlannerStepsWithoutStoringRows(t *testing.T) {
	a, sink := testAgent(t)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{
		plannerStep(contracts.AntigravityStepStatusRunning, "a provisional reply"),
	}))
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0, 1, 2}, []json.RawMessage{
		json.RawMessage(`{"type":"CORTEX_STEP_TYPE_PLANNER_RESPONSE","status":"CORTEX_STEP_STATUS_DONE","plannerResponse":{"toolCalls":[{"name":"read_file","thinkingSignature":"opaque"}],"stopReason":"STOP"},"metadata":{"modelUsage":{"inputTokens":"23","outputTokens":"5","responseHeader":{"sessionID":"-4651990807555912056"},"apiProvider":"API_PROVIDER_GOOGLE_GEMINI"},"internalMetadata":{"statusTransitions":[]}}}`),
		json.RawMessage(`{"type":"CORTEX_STEP_TYPE_PLANNER_RESPONSE","status":"CORTEX_STEP_STATUS_DONE","plannerResponse":{"modifiedResponse":"  ","response":"\n","thinking":"\t"}}`),
		json.RawMessage(`{"type":"CORTEX_STEP_TYPE_PLANNER_RESPONSE","status":"CORTEX_STEP_STATUS_DONE"}`),
	}))
	assert.Empty(t, transcriptMessages(sink))
	assert.Contains(t, sink.ProgressUpdates(), agent.CompleteModelProgress("antigravity:cascade:0"))
	a.Mu.Lock()
	assert.Equal(t, 3, a.firstOpenIndexLocked(a.root))
	assert.Empty(t, a.root.steps[0].raw)
	a.Mu.Unlock()

	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{3}, []json.RawMessage{
		plannerStep(contracts.AntigravityStepStatusRunning, " \t"),
	}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	rows := sink.Messages()
	require.Len(t, rows, 1)
	divider := rows[0]
	require.True(t, divider.TurnEnd)
	assert.JSONEq(t, `{"name":"antigravity_turn_end","num_tool_uses":0,"metadata":{"modelUsage":{"inputTokens":23,"outputTokens":5}}}`, string(divider.Content))
}

func TestAntigravityPlannerWithStructuredModelUsageSplitsScrubsAndCountsTokens(t *testing.T) {
	a, sink := testAgent(t)
	raw := json.RawMessage(`{
		"type":"CORTEX_STEP_TYPE_PLANNER_RESPONSE",
		"status":"CORTEX_STEP_STATUS_DONE",
		"metadata":{
			"modelUsage":{
				"model":"MODEL_PLACEHOLDER_M532",
				"inputTokens":"2118",
				"outputTokens":194,
				"cacheReadTokens":"235445",
				"nullMetric":null,
				"responseHeader":{"sessionID":"-4651990807555912056"}
			},
			"internalMetadata":{"statusTransitions":[{"updatedStatus":"CORTEX_STEP_STATUS_DONE"}]}
		},
		"plannerResponse":{
			"modifiedResponse":"visible answer",
			"thinking":"private reasoning",
			"toolCalls":[{"id":"call_1","name":"run_command","thinkingSignature":"opaque-signature"}]
		}
	}`)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{raw}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})

	messages := transcriptMessages(sink)
	require.Len(t, messages, 2)
	for _, msg := range messages {
		assert.NotContains(t, string(msg.Content), "internalMetadata")
		assert.NotContains(t, string(msg.Content), "thinkingSignature")
	}

	rows := sink.Messages()
	divider := rows[len(rows)-1]
	require.True(t, divider.TurnEnd)
	assert.JSONEq(t, `{"name":"antigravity_turn_end","num_tool_uses":0,"metadata":{"modelUsage":{"inputTokens":2118,"outputTokens":194,"cacheReadTokens":235445}}}`, string(divider.Content))
}

func TestAntigravityUnknownStepScrubsOpaqueFieldsWhenObject(t *testing.T) {
	a, sink := testAgent(t)
	objectRaw := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_FUTURE","metadata":{"internalMetadata":{"secret":true}},"plannerResponse":{"toolCalls":[{"thinkingSignature":"sig"}]}}`)
	require.NoError(t, a.persistUnknownStep(a.root, 0, objectRaw))
	nonObjectRaw := json.RawMessage(`["not-an-object"]`)
	require.NoError(t, a.persistUnknownStep(a.root, 1, nonObjectRaw))

	messages := transcriptMessages(sink)
	require.Len(t, messages, 2)
	assert.NotContains(t, string(messages[0].Content), "internalMetadata")
	assert.NotContains(t, string(messages[0].Content), "thinkingSignature")
	assert.JSONEq(t, string(nonObjectRaw), string(messages[1].Content))
}

func TestAntigravityStoresOnlyNonblankPlannerContent(t *testing.T) {
	for _, planner := range []string{
		`{"modifiedResponse":" ","response":"  answer","thinking":"\t"}`,
		`{"modifiedResponse":" ","thinking":"  reasoning"}`,
	} {
		t.Run(planner, func(t *testing.T) {
			a, sink := testAgent(t)
			raw := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_PLANNER_RESPONSE","status":"CORTEX_STEP_STATUS_DONE","plannerResponse":` + planner + `}`)
			feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{raw}))
			rows := transcriptMessages(sink)
			require.Len(t, rows, 1)
			assert.JSONEq(t, string(raw), string(rows[0].Content))
		})
	}
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
	assert.Error(t, a.persistLiveStep(a.root, 0, raw, "call"))
	assert.False(t, a.root.steps[0].liveOutputAccepted)
	assert.Error(t, a.persistStep(a.root, 0, antigravityStep{}, raw))
	assert.False(t, a.root.steps[0].consumed)
}

func TestAntigravityBlankPlannerDoesNotConsumeRetry(t *testing.T) {
	for _, result := range []struct{ name, payload string }{
		{"text", `{"response":"The model recovered."}`},
		{"tool calls", `{"toolCalls":[{"name":"read_file"}],"stopReason":"STOP"}`},
	} {
		t.Run(result.name, func(t *testing.T) {
			a, sink := testAgent(t)
			a.beginTurn(a.root)
			a.noteModelRetry(a.root, &modelRequestStatus{Attempt: 1, MaxAttempts: 4})
			before := len(sink.Messages())
			empty := plannerStep(contracts.AntigravityStepStatusGenerating, "\u0085\uFEFF")
			feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{empty}))
			require.Len(t, sink.Messages(), before)
			require.NotContains(t, sink.LeapMuxNotifications(), map[string]any{contracts.NotificationFieldType: contracts.NotificationTypeApiRetryResolved})
			raw := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_PLANNER_RESPONSE","status":"CORTEX_STEP_STATUS_DONE","plannerResponse":` + result.payload + `}`)
			feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{raw}))
			require.Contains(t, sink.LeapMuxNotifications(), map[string]any{contracts.NotificationFieldType: contracts.NotificationTypeApiRetryResolved})
		})
	}
}

func TestAntigravityBlankPlannerKeepsUnrecoveredError(t *testing.T) {
	a, sink := testAgent(t)
	failure := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_ERROR_MESSAGE","status":"CORTEX_STEP_STATUS_DONE","errorMessage":{"error":{"shortError":"Native model failed."}}}`)
	empty := plannerStep(contracts.AntigravityStepStatusGenerating, "")
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0, 1}, []json.RawMessage{failure, empty}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	notices := sink.PersistedNotifications()
	require.Len(t, notices, 1)
	assert.Contains(t, string(notices[0].Content), "Native model failed.")
	rows := sink.Messages()
	require.Len(t, rows, 1)
	assert.True(t, rows[0].TurnEnd)
	assert.Equal(t, agent.MessageCompletionError, rows[0].Completion)
}

func TestAntigravityNormalTurnFlushesNonToolStepsAndPreservesOpenToolStepAndTail(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	planner := plannerStep(contracts.AntigravityStepStatusGenerating, "partial answer")
	cmd := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"cmd-1"}},"runCommand":{"command":"sleep 30","combinedOutput":{"full":"live line\n"}}}`)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0, 1}, []json.RawMessage{planner, cmd}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})

	// Non-tool planner step flushed, tool step kept open, live tail preserved, no global ResetProgress.
	assert.Empty(t, sink.ClosedSpans())
	assert.NotContains(t, sink.TurnLifecycle(), "reset_spans")
	assert.NotContains(t, sink.ProgressUpdates(), agent.ResetProgress())
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetModelProgress())
	assert.Contains(t, sink.ProgressUpdates(), agent.OutputTailProgress("cmd-1", "live line\n", false))
	assert.True(t, a.root.steps[0].consumed, "non-tool planner step must flush at normal turn end")
	assert.False(t, a.root.steps[1].consumed, "open tool step must stay alive across normal turn end")
}

func TestAntigravityTurnTwoInterruptClosesTurnOneToolStepInSortedOrder(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	cmd1 := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"cmd-1"}},"runCommand":{"command":"sleep 30"}}`)
	cmd2 := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_RUN_COMMAND","status":"CORTEX_STEP_STATUS_RUNNING","metadata":{"toolCall":{"id":"cmd-2"}},"runCommand":{"command":"sleep 40"}}`)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{cmd1}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	require.Empty(t, sink.ClosedSpans())

	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{1}, []json.RawMessage{cmd2}))
	a.Mu.Lock()
	a.root.interrupted = true
	a.Mu.Unlock()
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})

	assert.Equal(t, []string{"cmd-1", "cmd-2"}, sink.ClosedSpans(), "interrupted Turn 2 closes Turn 1 and Turn 2 tool steps in ascending step index order")
	assert.Contains(t, sink.TurnLifecycle(), "reset_spans")
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
}

func TestAntigravityManualCheckpointErrorAndCanceledEndCompactionWithoutRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		status      string
		wantOutcome string
	}{
		{name: "error", status: contracts.AntigravityStepStatusError, wantOutcome: contracts.CompactionEndOutcomeFailed},
		{name: "canceled", status: contracts.AntigravityStepStatusCanceled, wantOutcome: contracts.CompactionEndOutcomeAborted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, sink := testAgent(t)
			a.sink.AdoptNativeSession(a.root.id)
			execID := "exec-compact"
			a.beginManualCompaction(a.root, execID)
			raw, err := json.Marshal(map[string]any{
				"type":     contracts.AntigravityStepTypeCheckpoint,
				"status":   tc.status,
				"metadata": map[string]any{"executionId": execID},
			})
			require.NoError(t, err)
			step, err := decodeStep(raw)
			require.NoError(t, err)

			require.NoError(t, a.persistStep(a.root, 0, step, raw))
			assert.Empty(t, transcriptMessages(sink), "failed or canceled checkpoint must not persist a transcript row")
			a.Mu.Lock()
			assert.True(t, a.root.steps[0].consumed)
			assert.Nil(t, a.root.manualCompactionExecutionID)
			a.Mu.Unlock()

			notices := sink.LeapMuxNotifications()
			require.Contains(t, notices, map[string]any{
				contracts.NotificationFieldType:    contracts.NotificationTypeCompactionEnded,
				contracts.NotificationFieldOutcome: tc.wantOutcome,
			})
			value, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
			require.True(t, ok)
			assert.Nil(t, value)
		})
	}
}
