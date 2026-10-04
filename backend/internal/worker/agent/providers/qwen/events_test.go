package qwen

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// startTurn is Qwen's opening of a turn it starts by itself. A background
// answer asks for admission with an id; a goal round states it with none.
func startTurn(t *testing.T, id any, sessionID, source string) []byte {
	t.Helper()
	message := map[string]any{"method": qwenStartTurnMethod, "params": map[string]any{"sessionId": sessionID, "source": source}}
	if id != nil {
		message["id"] = id
	}
	return frame(t, message)
}

// endTurn is Qwen's end of a turn it started by itself.
func endTurn(t *testing.T, source string) []byte {
	t.Helper()
	return frame(t, map[string]any{"method": contracts.QwenMethodEndTurn, "params": map[string]any{
		"sessionId": qwenTestSession, "reason": "end_turn", "source": source,
	}})
}

// turnEndRows returns the turn-end rows of one transcript.
func turnEndRows(messages []agenttest.Message) []agenttest.Message {
	var out []agenttest.Message
	for _, message := range messages {
		if message.TurnEnd {
			out = append(out, message)
		}
	}
	return out
}

// admission reads the agent's answer to one admission request.
func admission(t *testing.T, a *Agent, requests func() []agenttest.RecordedRequest, id int) bool {
	t.Helper()
	syncPeer(t, a)
	raw, ok := agenttest.JSONRPCResultsByID(t, rawLines(requests()))[jsonNumber(id)]
	require.True(t, ok, "the agent answers every admission request")
	var reply struct {
		Accepted bool `json:"accepted"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &reply))
	return reply.Accepted
}

func TestQwenBackgroundTurnIsAdmittedAndEnded(t *testing.T) {
	t.Parallel()
	a, sink, requests := newQwenAgent(t, nil, nil)

	a.HandleOutput(startTurn(t, 4, qwenTestSession, "background_notification"))
	assert.True(t, admission(t, a, requests, 4))
	assert.True(t, a.AgentTurnActive(), "the answering turn holds the busy state")

	a.HandleOutput(sessionUpdate(t, map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "The helper finished."},
	}))
	end := endTurn(t, "background_notification")
	a.HandleOutput(end)

	assert.False(t, a.PromptActive())
	ends := turnEndRows(sink.Messages())
	require.Len(t, ends, 1)
	assert.JSONEq(t, string(end), string(ends[0].Content), "Qwen's own frame is the turn-end row")
}

func TestQwenGoalRoundOpensWithoutAnAdmission(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	a.HandleOutput(startTurn(t, nil, qwenTestSession, "goal"))
	assert.True(t, a.AgentTurnActive())
	syncPeer(t, a)
	for _, request := range requests() {
		assert.NotEqual(t, "", request.Method, "a notification takes no answer")
	}
}

func TestQwenGoalRoundBeforeThePromptResponseWaitsForIt(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	// `/goal <objective>` runs as a prompt of LeapMux's. Qwen starts the first
	// round of the goal before it answers that prompt.
	a.SetPromptActiveForTest(true)
	a.HandleOutput(startTurn(t, nil, qwenTestSession, "goal"))
	assert.False(t, a.AgentTurnActive(), "the round waits for the prompt's end")

	a.FinishPromptRequestForTest(qwenTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.True(t, a.AgentTurnActive(), "the prompt's end hands the busy state to the round")

	a.HandleOutput(endTurn(t, "goal"))
	assert.False(t, a.PromptActive())
	assert.Len(t, turnEndRows(sink.Messages()), 2)
}

func TestQwenAdmissionOfAnotherSessionIsRefused(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	a.HandleOutput(startTurn(t, 5, "other-session", "background_notification"))
	assert.False(t, admission(t, a, requests, 5))
	assert.False(t, a.PromptActive())
}

func TestQwenSecondAdmissionIsDeferredWhileATurnRuns(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	a.HandleOutput(startTurn(t, 6, qwenTestSession, "background_notification"))
	a.HandleOutput(startTurn(t, 7, qwenTestSession, "background_notification"))
	assert.True(t, admission(t, a, requests, 6))
	assert.False(t, admission(t, a, requests, 7), "Qwen defers a turn while another runs")
}

// The agent defers a background turn that asks for admission while LeapMux's
// own prompt runs, and does not queue the turn behind the prompt. Qwen aborts a
// running turn when a prompt arrives. So an admitted turn could start the
// instant before the worker's next prompt, and that prompt would cut it. The
// base would then count that prompt's output as the agent turn's. Qwen asks
// again once its prompt ends.
func TestQwenAdmissionWhileLeapMuxPromptRunsIsDeferred(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(startTurn(t, 8, qwenTestSession, "background_notification"))

	assert.False(t, admission(t, a, requests, 8))
	assert.False(t, a.AgentTurnActive())
	a.FinishPromptRequestForTest(qwenTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.False(t, a.PromptActive(), "no agent turn waits behind the prompt")

	a.HandleOutput(startTurn(t, 9, qwenTestSession, "background_notification"))
	assert.True(t, admission(t, a, requests, 9), "the next request, once the prompt ended, is admitted")
}

// An end of a turn of another session ends nothing. After a context clear, the
// old session's end would otherwise end the agent turn of the new session.
func TestQwenEndTurnOfAnotherSessionEndsNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(startTurn(t, nil, qwenTestSession, "goal"))
	require.True(t, a.AgentTurnActive())

	a.HandleOutput(frame(t, map[string]any{"method": contracts.QwenMethodEndTurn, "params": map[string]any{
		"sessionId": "an-old-session", "reason": "end_turn", "source": "goal",
	}}))

	assert.True(t, a.AgentTurnActive(), "the turn of this session goes on")
	assert.Empty(t, turnEndRows(sink.Messages()))
	a.HandleOutput(endTurn(t, "goal"))
	assert.False(t, a.AgentTurnActive())
}

func TestQwenModeUpdateMovesThePermissionMode(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetAvailableModesForTest(qwenModes())
	a.SetPermissionModeForTest(contracts.QwenModeDefault)

	a.HandleOutput(frame(t, map[string]any{"method": qwenModeUpdateMethod, "params": map[string]any{
		"v": 1, "sessionId": qwenTestSession, "currentModeId": contracts.QwenModeAutoEdit,
	}}))
	assert.Equal(t, contracts.QwenModeAutoEdit, a.PermissionModeForTest())
	assert.Equal(t, contracts.QwenModeAutoEdit, sink.LastSettingsRefresh().PermissionMode, "the reader sees the new mode")

	for _, params := range []map[string]any{
		{"sessionId": "other-session", "currentModeId": contracts.QwenModeYolo},
		{"sessionId": qwenTestSession},
	} {
		a.HandleOutput(frame(t, map[string]any{"method": qwenModeUpdateMethod, "params": params}))
	}
	assert.Equal(t, contracts.QwenModeAutoEdit, a.PermissionModeForTest(), "another session's mode and an empty one change nothing")
	assert.Equal(t, 1, sink.SettingsRefreshCount())
}

func TestQwenConsumesItsMetadataNotifications(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	for _, method := range []string{"qwen/notify/session/title-update", "qwen/notify/session/model-update", qwenAuthenticateUpdateMethod} {
		line := providerkit.ParseLine(frame(t, map[string]any{"method": method, "params": map[string]any{"sessionId": qwenTestSession}}))
		assert.True(t, a.handleExtraMethod(line), method)
	}
	// A REQUEST that LeapMux does not answer reaches the base, which refuses it.
	for _, method := range []string{"craft/claimTodoStopGuardContinuation", "qwen/notify/session/unknown", qwenAuthenticateUpdateMethod} {
		line := providerkit.ParseLine(frame(t, map[string]any{"id": 9, "method": method, "params": map[string]any{}}))
		assert.False(t, a.handleExtraMethod(line), method)
	}
	assert.False(t, a.handleExtraMethod(providerkit.ParseLine(frame(t, map[string]any{"method": qwenDrainMethod, "params": map[string]any{}}))),
		"a drain with no id cannot take its answer")
}

// The base refuses a drain it never answers with an error, and an error reply
// switches steering off for the rest of Qwen's session.
func TestQwenAnswersEveryDrain(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	reply := drain(t, a, requests, 3, qwenTestSession)
	assert.Equal(t, []any{}, reply["items"])
	for _, request := range requests() {
		assert.NotContains(t, request.Raw, `"error"`)
	}
}

// A drain that states no session is the current session's, as every Qwen frame
// that states no session is.
func TestQwenDrainWithNoSessionIsTheCurrentSessions(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	require.NoError(t, a.SteerInput("kept", nil))

	items, ok := drainWithParams(t, a, requests, 1, map[string]any{})["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	assert.Equal(t, "kept", items[0].(map[string]any)["displayText"])
}

// A steered message with no text and no attachment carries nothing, so the
// drain takes it and sends no item for it. The queue does not keep it for a
// later drain or for the follow-up prompt either.
func TestQwenDrainDropsAMessageThatCarriesNothing(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	require.NoError(t, a.SteerInput("", nil))
	require.NoError(t, a.SteerInput("real", nil))

	items, ok := drain(t, a, requests, 1, qwenTestSession)["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	assert.Equal(t, "real", items[0].(map[string]any)["displayText"])

	assert.Empty(t, drain(t, a, requests, 2, qwenTestSession)["items"])
	_, _, ok = a.followUpPrompt(false)
	assert.False(t, ok)
}

func TestQwenGoalRoundOfAnotherSessionOpensNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)

	a.HandleOutput(startTurn(t, nil, "an-old-session", "goal"))

	assert.False(t, a.AgentTurnActive())
	assert.False(t, a.PromptActive())
	a.HandleOutput(endTurn(t, "goal"))
	assert.Empty(t, turnEndRows(sink.Messages()), "no turn of this session was open")
}

// An admission request that states no session is the current session's.
func TestQwenAdmissionWithNoSessionIsAdmitted(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)

	a.HandleOutput(frame(t, map[string]any{"id": 11, "method": qwenStartTurnMethod, "params": map[string]any{"source": "background_notification"}}))

	assert.True(t, admission(t, a, requests, 11))
	assert.True(t, a.AgentTurnActive())
}

// Qwen's end of a turn that no agent turn holds writes no turn-end row: the
// prompt response of a turn that LeapMux started closes that turn.
func TestQwenEndTurnWithNoOpenTurnWritesNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(endTurn(t, "goal"))
	assert.Empty(t, turnEndRows(sink.Messages()))

	a.SetPromptActiveForTest(true)
	a.HandleOutput(endTurn(t, "goal"))
	assert.True(t, a.PromptActive(), "the end of an agent turn does not end LeapMux's own prompt")
	assert.Empty(t, turnEndRows(sink.Messages()))
}

// An end that states no session ends the turn of the current session.
func TestQwenEndTurnWithNoSessionEndsTheCurrentTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(startTurn(t, nil, qwenTestSession, "goal"))
	require.True(t, a.AgentTurnActive())

	a.HandleOutput(frame(t, map[string]any{"method": contracts.QwenMethodEndTurn, "params": map[string]any{"reason": "end_turn"}}))

	assert.False(t, a.AgentTurnActive())
	assert.Len(t, turnEndRows(sink.Messages()), 1)
}

func TestQwenModeUpdateWithNoSessionMovesTheCurrentMode(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	a.SetAvailableModesForTest(qwenModes())
	a.SetPermissionModeForTest(contracts.QwenModeDefault)

	a.HandleOutput(frame(t, map[string]any{"method": qwenModeUpdateMethod, "params": "not an object"}))
	assert.Equal(t, contracts.QwenModeDefault, a.PermissionModeForTest(), "an unreadable update changes nothing")

	a.HandleOutput(frame(t, map[string]any{"method": qwenModeUpdateMethod, "params": map[string]any{"currentModeId": contracts.QwenModePlan}}))
	assert.Equal(t, contracts.QwenModePlan, a.PermissionModeForTest())
}
