package goose

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The texts Goose 1.55.0 prints in its goal loop, pinned to the sources that
// build them. A wording change in a Goose release breaks the inference with no
// error; this test is what catches it.
func TestGooseGoalTextsMatchGoose155(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Goal: ", contracts.GooseGoalTextNoticePrefix,
		"ops_retry.rs formats the notice as format!(\"Goal: {goal}\")")
	assert.Equal(t, "Ran into this error: ", contracts.GooseGoalTextErrorPrefix,
		"conversation/message.rs builds the error message on this prefix")
	assert.Equal(t, "I've reached the maximum number of actions I can do without user input. Would you like me to continue?",
		contracts.GooseGoalTextMaxTurnsMessage, "ops_maxturns.rs holds this as its MAX_TURNS_MESSAGE constant")
}

// gooseGoalPrompt drives one goal round: the arming notice, the final agent
// message, and the prompt response that ends the turn.
func gooseGoalPrompt(t *testing.T, a *Agent, notice bool, finalMessageID, finalText string, stopped bool) {
	t.Helper()
	if notice {
		a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"_goose/unstable/session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"status_message","status":{"type":"notice","message":"Goal: Reply with the single word OK."}}}}`))
	}
	if finalText != "" {
		a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":` + jsonString(t, finalText) + `},"messageId":"` + finalMessageID + `"}}}`))
	}
	if stopped {
		a.NoteInterruptRequestedForTest()
	}
	a.FinishPromptRequestForTest("s1", []byte(`{"stopReason":"end_turn","usage":{"totalTokens":2}}`), nil)
}

func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

// r2-goal-ok-export-1.log: the notice arms the rule and the round ends with an
// ordinary answer, so the goal is done and states the objective the notice
// carried.
func TestGooseGoalCompletesOnAnOrdinaryFinalAnswer(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	gooseGoalPrompt(t, a, true, "probe-3", "OK", false)
	goal, ok := sink.LastGoal()
	require.True(t, ok, "an ordinary final answer completes the goal")
	assert.Equal(t, agent.GoalUpdate{Objective: "Reply with the single word OK.", Status: agent.GoalStatusDone}, goal)
}

// r2-goal-error-1.log line 37: the round ends with Goose's own error message,
// so the goal stays as it was.
func TestGooseGoalKeepsAfterAnErrorFinalMessage(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	gooseGoalPrompt(t, a, true, "msg_ae790061",
		"Ran into this error: Server error: Server error (503 Service Unavailable) at http://127.0.0.1:52593/v1/chat/completions: probe stub: service unavailable.\n\nPlease retry if you think this is a transient or recoverable error.",
		false)
	assert.Empty(t, sink.Goals(), "an error final message keeps the goal")
	assert.Zero(t, sink.GoalClears())
}

// r2-goal-maxturns-1.log line 36: Goose asks the user whether to continue, so
// the goal stays.
func TestGooseGoalKeepsAfterAMaxTurnsFinalMessage(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	gooseGoalPrompt(t, a, true, "msg_19fd5cd8",
		"I've reached the maximum number of actions I can do without user input. Would you like me to continue?", false)
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

// A stop keeps the goal: the reader ended the round themselves, and Goose
// states nothing about the goal at a stop.
func TestGooseGoalKeepsAtAStop(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	gooseGoalPrompt(t, a, true, "probe-3", "partial answer", true)
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

// The rule needs its arming notice: a round with no goal notice states nothing
// about the goal, whatever its final message says.
func TestGooseGoalDecidesNothingWithoutTheNotice(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	gooseGoalPrompt(t, a, false, "probe-3", "OK", false)
	assert.Empty(t, sink.Goals())
}

// The LAST agent message decides. A later ordinary answer after an error one
// completes the goal, because the error round is over.
func TestGooseGoalReadsTheLastAgentMessage(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"_goose/unstable/session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"status_message","status":{"type":"notice","message":"Goal: Reply with the single word OK."}}}}`))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Ran into this error: transient"},"messageId":"msg_err"}}}`))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"OK, "},"messageId":"probe-3"}}}`))
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"all done"},"messageId":"probe-3"}}}`))
	a.FinishPromptRequestForTest("s1", []byte(`{"stopReason":"end_turn"}`), nil)
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalUpdate{Objective: "Reply with the single word OK.", Status: agent.GoalStatusDone}, goal)
}

// A round whose final message is empty states nothing: no answer proved the
// goal met, and clearing it would lose a goal Goose still holds.
func TestGooseGoalDecidesNothingWithoutAFinalMessage(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	gooseGoalPrompt(t, a, true, "", "", false)
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

// One prompt's notice arms that prompt alone: the next prompt without a notice
// states nothing, even when its final message is an ordinary answer.
func TestGooseGoalArmsOnePromptAtATime(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newGooseOutputAgent(agent.NewProviderServices(sink))
	a.SetPromptActiveForTest(true)
	gooseGoalPrompt(t, a, true, "probe-3", "OK", false)
	require.Len(t, sink.Goals(), 1)
	a.SetPromptActiveForTest(true)
	gooseGoalPrompt(t, a, false, "probe-4", "Another answer", false)
	assert.Len(t, sink.Goals(), 1, "the second prompt decides nothing without its own notice")
}
