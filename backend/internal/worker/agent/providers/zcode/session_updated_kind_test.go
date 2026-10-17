package zcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `session.updated` is the app-server's catch-all: every internal event with no
// explicit wire mapping becomes one, with a free-form payload. The shapes are told
// apart by which fields they carry, in the one order both readers share. This table
// feeds each documented shape -- and the pairs that overlap -- through `kind()`.
func TestZCodeSessionUpdatedKind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		payload string
		want    zcodeSessionUpdateKind
	}{
		{
			name:    "a network status carries a model_ type and a request id",
			payload: `{"type":"model_retry_scheduled","requestId":"q1","attempt":2}`,
			want:    zcodeSessionUpdateNetworkStatus,
		},
		{
			name:    "a background task carries a task id",
			payload: `{"taskId":"task-1","status":"running"}`,
			want:    zcodeSessionUpdateBackgroundTask,
		},
		{
			name:    "a subagent update carries an agent id and its parent call",
			payload: `{"agentId":"child-1","parentToolCallId":"spawn-1","status":"running"}`,
			want:    zcodeSessionUpdateSubagent,
		},
		{
			name:    "a model response carries content and a stop reason",
			payload: `{"content":"Hello","querySource":"main_turn","stopReason":"stop"}`,
			want:    zcodeSessionUpdateModelResponse,
		},
		{
			name:    "a side query is still a model response shape",
			payload: `{"content":"{\"passed\": true, \"reason\": \"...\", \"nextAction\": \"\"}","querySource":"target_completion_verification","stopReason":"stop","toolCallCount":0,"usage":{}}`,
			want:    zcodeSessionUpdateModelResponse,
		},
		{
			name:    "a tool-only step is still a model response shape",
			payload: `{"content":"","stopReason":"tool_internal","usage":{},"toolCallCount":0}`,
			want:    zcodeSessionUpdateModelResponse,
		},
		{
			name:    "a goal verification carries a verification id and a target id",
			payload: `{"verificationId":"v1","targetId":"g1","goalIteration":2,"status":"completed","verification":{"passed":false,"reason":"...","nextAction":"Run the tests"}}`,
			want:    zcodeSessionUpdateGoalVerification,
		},
		{
			// The captured frames of a real 3.14.5 goal check. Their extra fields
			// (the anchors and the foreground execution id) must not move the
			// classification to another kind.
			name:    "the captured goal check start frame is a goal verification",
			payload: `{"anchorAssistantMessageId":"msg_a","anchorTurnId":"turn_a","foregroundExecutionId":"runtime_command_1","goalIteration":1,"status":"started","targetId":"target_x","verificationId":"f34b0107-762a-4b"}`,
			want:    zcodeSessionUpdateGoalVerification,
		},
		{
			name:    "the captured goal check verdict frame is a goal verification",
			payload: `{"anchorAssistantMessageId":"msg_a","anchorTurnId":"turn_a","foregroundExecutionId":"runtime_command_1","goalIteration":1,"status":"completed","targetId":"target_x","verification":{"nextAction":"Fix the failing test.","passed":false,"reason":"The tests still fail."},"verificationId":"f34b0107-762a-4b"}`,
			want:    zcodeSessionUpdateGoalVerification,
		},
		{
			name:    "a goal change carries an action, a source and a target",
			payload: `{"action":"status_updated","source":"runtime","target":{"targetID":"g1","status":"complete"}}`,
			want:    zcodeSessionUpdateGoalChange,
		},
		{
			name:    "a cleared goal change carries a null target",
			payload: `{"action":"cleared","source":"command","target":null}`,
			want:    zcodeSessionUpdateGoalChange,
		},
		{
			name:    "a mode change carries a mode and the plan flag",
			payload: `{"mode":"edit","planEnabled":false,"source":"command"}`,
			want:    zcodeSessionUpdateModeChange,
		},
		{
			name:    "a compaction lifecycle carries an operation id, a trigger and a status",
			payload: `{"operationId":"c1","status":"completed","trigger":"auto","phase":"standalone_turn","display":"separator","preCompactTokenCount":180000,"postCompactTokenCount":12000}`,
			want:    zcodeSessionUpdateCompaction,
		},
		{
			name:    "a microcompact carries the local-tool-result-clear strategy",
			payload: `{"strategy":"local_tool_result_clear","tokensSaved":4000,"clearedToolCallIds":["t1"],"trigger":"token_pressure"}`,
			want:    zcodeSessionUpdateMicrocompact,
		},
		{
			name:    "a model selection carries a model selection object",
			payload: `{"modelSelection":{"providerId":"p","modelId":"glm-5.3","options":{}},"previousModelSelection":{"providerId":"p","modelId":"glm-6","options":{}},"origin":"registryFallback","contextWindow":200000}`,
			want:    zcodeSessionUpdateModelSelected,
		},
		{
			name:    "a hook run carries a run id and an invocation id",
			payload: `{"hookRunId":"h1","hookInvocationId":"i1","hookEventName":"PreToolUse","outcome":"failed","descriptor":{"clientVisible":true,"sourceKind":"user"},"errorMessage":"boom"}`,
			want:    zcodeSessionUpdateHookRun,
		},
		{
			name:    "a workspace hook admission carries a pending count and a bundle digest",
			payload: `{"pendingCount":2,"bundleDigest":"abc"}`,
			want:    zcodeSessionUpdateHookAdmission,
		},
		{
			name:    "a steer discard carries pending input ids, a reason and a target turn",
			payload: `{"pendingInputIds":["p1"],"reason":"user_removed","targetTurnId":"turn_1"}`,
			want:    zcodeSessionUpdateSteerDiscarded,
		},
		{
			name:    "a verification beside a status is still a verification",
			payload: `{"verificationId":"v1","targetId":"g1","status":"completed","verification":{"passed":true,"reason":"ok"}}`,
			want:    zcodeSessionUpdateGoalVerification,
		},
		{
			name:    "a request id alone states nothing",
			payload: `{"baseURL":"https://api.example","requestId":"q1","maxAttempts":3}`,
			want:    zcodeSessionUpdateDrop,
		},
		{
			name:    "a stop reason alone states nothing",
			payload: `{"stopReason":"end_turn"}`,
			want:    zcodeSessionUpdateDrop,
		},
		{
			name:    "a compact boundary carries no operation id and no status",
			payload: `{"boundaryId":"b1","trigger":"auto","phase":"pre_request","preCompactTokenCount":180000,"postCompactTokenCount":12000}`,
			want:    zcodeSessionUpdateDrop,
		},
		{
			name:    "a model anomaly warning carries no request id",
			payload: `{"type":"model_anomaly_warning","reason":"strange"}`,
			want:    zcodeSessionUpdateDrop,
		},
		{
			name:    "an empty payload states nothing",
			payload: `{}`,
			want:    zcodeSessionUpdateDrop,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var payload zcodeSessionUpdatedPayload
			assert.NoError(t, json.Unmarshal([]byte(tc.payload), &payload))
			assert.Equal(t, tc.want, payload.kind())
		})
	}
}

// The goal check's lifecycle re-reports the goal with its own detail. The goal
// it re-reports is the LATEST one: repeated goal updates fold into the kept
// report, and a check must not resurrect an older objective.
func TestHandleZCodeSessionUpdated_RepeatedGoalUpdatesFoldOntoTheLatest(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"action":"set","source":"command","target":{"targetID":"g1","sessionID":"sess-1",`+
			`"objective":"First objective","summaryTitle":"First","status":"active",`+
			`"tokenBudget":null,"tokensUsed":0,"timeUsedSeconds":0}}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventSessionUpdated,
		`{"action":"status_updated","source":"runtime","target":{"targetID":"g1","sessionID":"sess-1",`+
			`"objective":"Second objective","summaryTitle":"Second","status":"paused",`+
			`"tokenBudget":null,"tokensUsed":10,"timeUsedSeconds":5}}`))
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventSessionUpdated,
		`{"verificationId":"v1","targetId":"g1","goalIteration":1,"status":"started"}`))

	got, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "Second objective", got.Objective, "the check folds onto the latest update")
	assert.Equal(t, agent.GoalStatusPaused, got.Status)
	assert.Equal(t, "verifying", got.StatusDetail)

	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventSessionUpdated,
		`{"verificationId":"v1","targetId":"g1","goalIteration":2,"status":"completed"}`))

	got, ok = sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "Second objective", got.Objective, "the final keeps the folded update")
	assert.Empty(t, got.StatusDetail, "the check is over and the report drops the phrase")
}

// A kind with no handler in the folding switch is classified per row. Each of
// these kinds writes a row of its own, and one blanket folding key would
// collapse them into a single unreadable notice. A new kind lands in the same
// place until someone gives it a key.
func TestZCodeSessionUpdatedKindWithoutAHandlerHasNoFoldingKey(t *testing.T) {
	t.Parallel()

	sessionUpdated := func(payload string) json.RawMessage {
		return json.RawMessage(`{"eventId":"e","sessionId":"sess-1","seq":1,"type":"` +
			contracts.ZCodeEventSessionUpdated + `","payload":` + payload + `}`)
	}
	for name, payload := range map[string]string{
		"a microcompact boundary":       `{"strategy":"local_tool_result_clear","tokensSaved":4000,"clearedToolCallIds":["t1"],"trigger":"token_pressure"}`,
		"a registry fallback selection": `{"modelSelection":{"providerId":"p","modelId":"glm-5.3","options":{}},"previousModelSelection":{"providerId":"p","modelId":"glm-6","options":{}},"origin":"registryFallback","contextWindow":200000}`,
		"a client-visible hook failure": `{"hookRunId":"h1","hookInvocationId":"i1","hookEventName":"PreToolUse","outcome":"failed","descriptor":{"clientVisible":true,"sourceKind":"user"},"errorMessage":"boom"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, agent.NotificationClassification{},
				Registration().Plugin.Classify(sessionUpdated(payload)))
		})
	}
}
