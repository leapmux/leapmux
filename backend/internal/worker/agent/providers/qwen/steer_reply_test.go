package qwen

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

func TestQwenSteerBindsExactReplyAtDrainAndDropsUndrainedOnPromptEnd(t *testing.T) {
	t.Parallel()
	promptReply := make(chan agenttest.RPCReply, 1)
	a, sink, requests := newQwenAgent(t, nil, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == acp.MethodSessionPrompt {
			return <-promptReply
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	reporter, ok := any(a).(agent.SteerEchoReporter)
	require.True(t, ok, "Qwen must report steer resolution at drain")
	assert.True(t, reporter.SteerRowAtEcho())

	lead, err := a.SendInputWithReceipt("Check the release.", nil)
	require.NoError(t, err)
	require.NotEmpty(t, lead.TurnID)

	a.HandleOutput(sessionUpdate(t, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "Before drain."},
	}))

	require.NoError(t, a.SteerInput(agent.SteerRequest{
		InputID:   "queue-steer-1",
		AttemptID: "attempt-1",
		Content:   "Include changelog.",
	}))
	assert.Empty(t, sink.SteerResolutions(), "the steer stays pending until Qwen drains it")

	a.HandleOutput(frame(t, map[string]any{
		"id":     11,
		"method": qwenDrainMethod,
		"params": map[string]any{"sessionId": qwenTestSession},
	}))

	require.Len(t, sink.SteerResolutions(), 1)
	got := sink.SteerResolutions()[0]
	assert.Equal(t, "queue-steer-1", got.InputID)
	assert.Equal(t, "attempt-1", got.AttemptID)
	assert.Equal(t, agent.SteerOutcomeEchoed, got.Outcome)
	assert.Equal(t, qwenTestSession, got.Receipt.SessionID)
	assert.NotEmpty(t, got.Receipt.InputID)
	assert.Equal(t, lead.TurnID, got.Receipt.TurnID)

	// Queue a second tracked steer that Qwen never drains before the turn finishes.
	require.NoError(t, a.SteerInput(agent.SteerRequest{
		InputID:   "queue-steer-2",
		AttemptID: "attempt-2",
		Content:   "Undrained follow-up.",
	}))
	a.HandleOutput(sessionUpdate(t, map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": "After drain."},
	}))

	promptReply <- agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
	var drainReply map[string]any
	testutil.RequireEventually(t, func() bool {
		if len(sink.SteerResolutions()) != 2 {
			return false
		}
		results := agenttest.JSONRPCResultsByID(t, rawLines(requests()))
		raw, ok := results[jsonNumber(11)]
		if !ok {
			return false
		}
		return json.Unmarshal([]byte(raw), &drainReply) == nil
	})
	items, ok := drainReply["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-2",
		AttemptID: "attempt-2",
		Outcome:   agent.SteerOutcomeDropped,
	}, sink.SteerResolutions()[1])
}

func TestQwenStoppedPromptDropsUndrainedTrackedSteerToQueue(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput(agent.SteerRequest{
		InputID:   "queue-steer-stop",
		AttemptID: "attempt-stop",
		Content:   "Keep in paused queue.",
	}))
	a.handlePromptEnded(nil, true)

	require.Len(t, sink.SteerResolutions(), 1)
	assert.Equal(t, agent.SteerResolution{
		InputID:   "queue-steer-stop",
		AttemptID: "attempt-stop",
		Outcome:   agent.SteerOutcomeDropped,
	}, sink.SteerResolutions()[0])
	assert.Empty(t, sink.Notifications(), "a tracked queue steer returns to the queue instead of being discarded")
}
