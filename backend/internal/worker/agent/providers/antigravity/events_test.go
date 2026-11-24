package antigravity

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAntigravityReducerTurnTimeline(t *testing.T) {
	a, sink := testAgent(t)
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle, FullyIdle: true})
	assert.Empty(t, sink.TurnActiveCalls)
	for range 2 {
		feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusRunning})
		feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})
	}
	assert.Equal(t, []bool{true, false, true, false}, sink.TurnActiveCalls)
}

func TestAntigravityReducerReplaysDoneStepOnce(t *testing.T) {
	a, sink := testAgent(t)
	update := updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{plannerStep(contracts.AntigravityStepStatusDone, "once")})
	feedUpdate(t, a, update)
	feedUpdate(t, a, update)
	require.Len(t, transcriptMessages(sink), 1)
}

func TestAntigravityReducerTurnInStreamGap(t *testing.T) {
	a, sink := testAgent(t)
	feedUpdate(t, a, updateWithSteps(cascadeStatusIdle, []int{0}, []json.RawMessage{plannerStep(contracts.AntigravityStepStatusDone, "complete in gap")}))
	assert.Equal(t, []bool{true, false}, sink.TurnActiveCalls)
	require.Len(t, transcriptMessages(sink), 1)
}

func TestAntigravityResumeIgnoresSeededSteps(t *testing.T) {
	a, sink := testAgent(t)
	a.root.seededCount = 4
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0, 4}, []json.RawMessage{
		plannerStep(contracts.AntigravityStepStatusDone, "old"), plannerStep(contracts.AntigravityStepStatusDone, "new"),
	}))
	require.Len(t, transcriptMessages(sink), 1)
	assert.Contains(t, string(transcriptMessages(sink)[0].Content), "new")
}

func TestAntigravityFirstOpenIndexSkipsBackgroundSteps(t *testing.T) {
	a, _ := testAgent(t)
	a.root.seededCount = 2
	for index := 2; index <= 5; index++ {
		a.root.steps[index] = &stepState{status: "CORTEX_STEP_STATUS_DONE", consumed: true}
	}
	a.root.steps[2].status = "CORTEX_STEP_STATUS_RUNNING"
	a.root.steps[2].consumed = false
	a.root.steps[2].liveOutputAccepted = true
	a.root.background["task"] = backgroundTask{rowKey: "task", stepIndex: 2}
	assert.Equal(t, 6, a.firstOpenIndexLocked(a.root))
	a.root.steps[3].status = "CORTEX_STEP_STATUS_RUNNING"
	a.root.steps[3].consumed = false
	assert.Equal(t, 3, a.firstOpenIndexLocked(a.root))
}

func TestAntigravityModelUsageUnmarshal(t *testing.T) {
	t.Run("parses string and numeric counters and skips non-counter values", func(t *testing.T) {
		var usage antigravityModelUsage
		raw := []byte(`{
			"inputTokens": "2118",
			"outputTokens": 194,
			"zeroTokens": "0",
			"zeroNumber": 0,
			"maxTokens": "9223372036854775807",
			"overflow": "99999999999999999999999999",
			"fractional": 12.5,
			"flag": true,
			"list": [1, 2],
			"empty": null,
			"model": "MODEL_PLACEHOLDER_M532",
			"responseHeader": {"sessionID": "-4651990807555912056"}
		}`)
		require.NoError(t, json.Unmarshal(raw, &usage))
		assert.Equal(t, antigravityModelUsage{
			"inputTokens":  2118,
			"outputTokens": 194,
			"zeroTokens":   0,
			"zeroNumber":   0,
			"maxTokens":    9223372036854775807,
		}, usage)
	})

	t.Run("resets to nil on null, empty object, or non-counter-only object", func(t *testing.T) {
		for _, payload := range []string{
			`null`,
			`{}`,
			`{"model":"M532","responseHeader":{"sessionID":"1"}}`,
		} {
			usage := antigravityModelUsage{"stale": 1}
			require.NoError(t, json.Unmarshal([]byte(payload), &usage), payload)
			assert.Nil(t, usage, payload)
		}
	})

	t.Run("rejects non-object json", func(t *testing.T) {
		for _, payload := range []string{`[]`, `"2118"`, `42`} {
			var usage antigravityModelUsage
			assert.Error(t, json.Unmarshal([]byte(payload), &usage), payload)
		}
	})
}

func TestAntigravityConsumedStepRefreshesStructuredModelUsage(t *testing.T) {
	a, sink := testAgent(t)
	initial := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_PLANNER_RESPONSE","status":"CORTEX_STEP_STATUS_DONE","plannerResponse":{"toolCalls":[{"name":"read_file"}]}}`)
	updated := json.RawMessage(`{"type":"CORTEX_STEP_TYPE_PLANNER_RESPONSE","status":"CORTEX_STEP_STATUS_DONE","plannerResponse":{"toolCalls":[{"name":"read_file"}]},"metadata":{"modelUsage":{"inputTokens":"100","outputTokens":"25","responseHeader":{"sessionID":"-1"}}}}`)
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{initial}))
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{updated}))
	feedUpdate(t, a, agentStateUpdate{Status: cascadeStatusIdle})

	assert.Empty(t, transcriptMessages(sink))
	rows := sink.Messages()
	require.Len(t, rows, 1)
	require.True(t, rows[0].TurnEnd)
	assert.JSONEq(t, `{"name":"antigravity_turn_end","num_tool_uses":0,"metadata":{"modelUsage":{"inputTokens":100,"outputTokens":25}}}`, string(rows[0].Content))
}
