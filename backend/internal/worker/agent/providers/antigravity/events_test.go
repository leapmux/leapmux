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
	for index := 2; index <= 5; index++ {
		a.root.steps[index] = &stepState{status: "CORTEX_STEP_STATUS_DONE"}
	}
	a.root.steps[2].status = "CORTEX_STEP_STATUS_RUNNING"
	a.root.background[2] = backgroundTask{rowKey: "task", stepIndex: 2}
	assert.Equal(t, 6, a.firstOpenIndexLocked(a.root))
	a.root.steps[3].status = "CORTEX_STEP_STATUS_RUNNING"
	assert.Equal(t, 3, a.firstOpenIndexLocked(a.root))
}
