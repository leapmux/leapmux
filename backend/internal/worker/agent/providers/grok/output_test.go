package grok

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// queueChanged is Grok's report of the prompt that runs in one session.
func queueChanged(t *testing.T, sessionID, running string) []byte {
	t.Helper()
	params := map[string]any{"sessionId": sessionID, "entries": []any{}}
	if running != "" {
		params["runningPromptId"] = running
	}
	return frame(t, map[string]any{"method": grokQueueChangedMethod, "params": params})
}

// turnCompleted is the end of one turn of the main session.
func turnCompleted(t *testing.T, promptID string) []byte {
	t.Helper()
	return notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "turn_completed", "prompt_id": promptID, "stop_reason": "end_turn",
	})
}

// turnEnds counts the turn-end rows of one transcript.
func turnEnds(messages []agenttest.Message) []agenttest.Message {
	var out []agenttest.Message
	for _, message := range messages {
		if message.TurnEnd {
			out = append(out, message)
		}
	}
	return out
}

func TestGrokAgentStartedTurnOpensAndEnds(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)

	// A finished background child wakes the parent with no prompt of LeapMux's.
	a.HandleOutput(queueChanged(t, grokTestSession, "subagent-completed-bg-1"))
	assert.True(t, a.PromptActive(), "the agent's own turn refuses a second prompt")
	assert.True(t, a.AgentTurnActive())

	a.HandleOutput(sessionUpdate(t, grokTestSession, map[string]any{
		"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "The helper finished."},
	}))
	end := turnCompleted(t, "subagent-completed-bg-1")
	a.HandleOutput(end)

	assert.False(t, a.PromptActive(), "turn_completed is the only end of such a turn")
	ends := turnEnds(sink.Messages())
	require.Len(t, ends, 1)
	assert.JSONEq(t, string(end), string(ends[0].Content), "Grok's own frame is the turn-end row")
}

// ownPromptID registers a prompt of LeapMux's, as sending one does, and returns
// the id that it states.
func ownPromptID(t *testing.T, a *Agent) string {
	t.Helper()
	params := map[string]any{"sessionId": grokTestSession}
	a.adjustPromptParams(params)
	meta, ok := params["_meta"].(map[string]any)
	require.True(t, ok)
	id, ok := meta[grokPromptIDKey].(string)
	require.True(t, ok)
	return id
}

func TestGrokQueueChangeOfALeapMuxPromptOpensNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	id := ownPromptID(t, a)
	a.SetPromptActiveForTest(true)

	a.HandleOutput(queueChanged(t, grokTestSession, id))
	assert.False(t, a.AgentTurnActive(), "the running prompt is LeapMux's own")

	// Its turn_completed precedes the prompt response, which ends the turn.
	a.HandleOutput(turnCompleted(t, id))
	assert.True(t, a.PromptActive(), "the prompt response, not turn_completed, ends a LeapMux turn")
	assert.Empty(t, turnEnds(sink.Messages()))
}

func TestGrokAgentTurnAfterALeapMuxPromptWaitsForItsEnd(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	id := ownPromptID(t, a)
	a.SetPromptActiveForTest(true)
	a.HandleOutput(queueChanged(t, grokTestSession, id))
	a.HandleOutput(turnCompleted(t, id))

	// The background child's completion wakes the parent before the base
	// processed the response of LeapMux's prompt.
	a.HandleOutput(queueChanged(t, grokTestSession, "subagent-completed-bg-1"))
	assert.False(t, a.AgentTurnActive(), "the agent turn waits for the prompt's end")

	a.FinishPromptRequestForTest(grokTestSession, json.RawMessage(`{"stopReason":"end_turn"}`), nil)
	assert.True(t, a.AgentTurnActive(), "the prompt's end hands the busy state over")
	assert.True(t, a.PromptActive())

	a.HandleOutput(turnCompleted(t, "subagent-completed-bg-1"))
	assert.False(t, a.PromptActive())
	assert.Len(t, turnEnds(sink.Messages()), 2)
}

func TestGrokAdjustPromptParamsStatesAFreshID(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, nil)
	params := map[string]any{"_meta": map[string]any{"mode": "plan"}}
	a.adjustPromptParams(params)
	meta := params["_meta"].(map[string]any)
	assert.Equal(t, "plan", meta["mode"], "the prompt keeps the metadata that it carried")
	first := meta[grokPromptIDKey].(string)
	second := ownPromptID(t, a)
	assert.NotEqual(t, first, second)
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.True(t, a.turns.own[first])
	assert.True(t, a.turns.own[second])
}

func TestGrokOwnPromptIDsAreForgottenWhenTheyEnd(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, nil)
	completed := ownPromptID(t, a)
	superseded := ownPromptID(t, a)
	a.HandleOutput(queueChanged(t, grokTestSession, superseded))
	a.HandleOutput(turnCompleted(t, completed))
	// The next running prompt proves the one before it ended.
	a.HandleOutput(queueChanged(t, grokTestSession, "goal-round-1"))

	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Empty(t, a.turns.own)
}

func TestGrokQueueChangeRepeatsNoTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(queueChanged(t, grokTestSession, "goal-1"))
	a.HandleOutput(queueChanged(t, grokTestSession, "goal-1"))
	a.HandleOutput(turnCompleted(t, "goal-1"))

	assert.Len(t, turnEnds(sink.Messages()), 1, "one running prompt is one turn")
	assert.False(t, a.PromptActive())
}

func TestGrokQueueChangeOfAnotherSessionOrNoPromptOpensNothing(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(queueChanged(t, "child-session", "child-prompt"))
	a.HandleOutput(queueChanged(t, grokTestSession, ""))
	a.HandleOutput(frame(t, map[string]any{"method": grokQueueChangedMethod, "params": "not an object"}))

	assert.False(t, a.PromptActive())
}

func TestGrokTurnCompletedOfAnotherPromptLeavesTheAgentTurnOpen(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(queueChanged(t, grokTestSession, "goal-1"))

	a.HandleOutput(turnCompleted(t, "some-other-prompt"))
	assert.True(t, a.PromptActive())
	assert.Empty(t, turnEnds(sink.Messages()))

	a.HandleOutput(turnCompleted(t, "goal-1"))
	assert.False(t, a.PromptActive())
}

func TestGrokResponseCompletedBroadcastsTheUsage(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(notification(t, grokTestSession, map[string]any{
		"sessionUpdate": "response_completed",
		"usage": map[string]any{
			"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 30, "cache_creation_input_tokens": 5,
		},
	}))

	usage, ok := sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		contracts.ContextUsageFieldInputTokens:              int64(100),
		contracts.ContextUsageFieldOutputTokens:             int64(20),
		contracts.ContextUsageFieldCacheReadInputTokens:     int64(30),
		contracts.ContextUsageFieldCacheCreationInputTokens: int64(5),
	}, usage)
}

func TestGrokResponseCompletedOfAChildSessionIsNotTheParentsUsage(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	// A row routes the child session, so the agent serves it, and only the
	// main-session rule keeps its usage off the parent.
	a.AttachChildSession("child-session", "call_spawn")
	a.HandleOutput(notification(t, "child-session", map[string]any{
		"sessionUpdate": "response_completed", "usage": map[string]any{"input_tokens": 999},
	}))
	a.HandleOutput(notification(t, grokTestSession, map[string]any{"sessionUpdate": "response_completed"}))

	assert.Zero(t, sink.SessionInfoCount())
}

func TestGrokReportsCompactionInTheTranscript(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	// A child session that a row routes compacts its own context, which is not
	// the parent's.
	a.AttachChildSession("child-session", "call_spawn")
	a.HandleOutput(notification(t, "child-session", map[string]any{"sessionUpdate": "auto_compact_started", "percentage": 90}))
	for _, update := range []map[string]any{
		{"sessionUpdate": "auto_compact_started", "percentage": 86},
		{"sessionUpdate": "auto_compact_completed", "tokens_before": 120, "tokens_after": 40},
		{"sessionUpdate": "auto_compact_completed", "tokens_after": 40},
		{"sessionUpdate": "auto_compact_failed", "error": "degenerate summary"},
		{"sessionUpdate": "auto_compact_failed"},
	} {
		a.HandleOutput(notification(t, grokTestSession, update))
	}

	var texts []string
	for _, notification := range sink.Notifications() {
		assert.Equal(t, contracts.NotificationTypeAgentStatus, notification[contracts.NotificationFieldType])
		texts = append(texts, notification[contracts.NotificationFieldText].(string))
	}
	assert.Equal(t, []string{
		"Compacting the context (86% of the window used)",
		"Context compacted from 120 to 40 tokens",
		"Context compacted to 40 tokens",
		"Context compaction failed: degenerate summary",
		"Context compaction failed",
	}, texts)
}

func TestGrokReportsARetryInProgressOnly(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	for _, update := range []map[string]any{
		{"sessionUpdate": "retry_state", "type": "retrying", "attempt": 2, "max_retries": 5, "reason": "rate limited"},
		{"sessionUpdate": "retry_state", "type": "retrying", "attempt": 3, "max_retries": 5},
		{"sessionUpdate": "retry_state", "type": "exhausted"},
		{"sessionUpdate": "retry_state", "type": "failed"},
	} {
		a.HandleOutput(notification(t, grokTestSession, update))
	}
	// A child session's retry is the child's own. A row routes the session, so
	// the agent serves it, and only the main-session rule keeps it out.
	a.AttachChildSession("child-session", "call_spawn")
	a.HandleOutput(notification(t, "child-session", map[string]any{"sessionUpdate": "retry_state", "type": "retrying", "attempt": 1, "max_retries": 5}))

	var texts []string
	for _, notification := range sink.Notifications() {
		texts = append(texts, notification[contracts.NotificationFieldText].(string))
	}
	assert.Equal(t, []string{
		"Retrying the model request (attempt 2 of 5): rate limited",
		"Retrying the model request (attempt 3 of 5)",
	}, texts)
}

// A turn_completed that states no prompt id ends the agent's own turn.
func TestGrokTurnCompletedWithNoPromptIDEndsTheAgentTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(queueChanged(t, grokTestSession, "goal-1"))
	require.True(t, a.AgentTurnActive())

	a.HandleOutput(notification(t, grokTestSession, map[string]any{"sessionUpdate": "turn_completed", "stop_reason": "end_turn"}))

	assert.False(t, a.PromptActive())
	assert.Len(t, turnEnds(sink.Messages()), 1)
}

// A turn_completed with no agent turn open writes no turn-end row.
func TestGrokTurnCompletedWithNoAgentTurnWritesNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGrokAgent(t, agent.Options{}, nil)
	a.HandleOutput(turnCompleted(t, ""))
	a.HandleOutput(notification(t, grokTestSession, map[string]any{"sessionUpdate": "turn_completed", "prompt_id": 7}))
	assert.Empty(t, turnEnds(sink.Messages()))
	assert.False(t, a.PromptActive())
}

// The worker sends prompts on its own goroutines while the reader reads Grok's
// queue. Each prompt that LeapMux sent stays its own, so none of them opens an
// agent turn, and the agent forgets each prompt that a newer one replaced.
func TestGrokConcurrentPromptsStayLeapMuxsOwn(t *testing.T) {
	t.Parallel()
	a, _, _ := newGrokAgent(t, agent.Options{}, nil)
	const senders, perSender = 4, 25
	ids := make(chan string, senders*perSender)
	var wg sync.WaitGroup
	for range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perSender {
				params := map[string]any{"sessionId": grokTestSession}
				a.adjustPromptParams(params)
				meta, _ := params["_meta"].(map[string]any)
				id, _ := meta[grokPromptIDKey].(string)
				ids <- id
			}
		}()
	}
	go func() {
		wg.Wait()
		close(ids)
	}()
	seen := map[string]bool{}
	var last string
	for id := range ids {
		require.NotEmpty(t, id)
		require.False(t, seen[id], "each prompt states an id of its own")
		seen[id] = true
		a.HandleOutput(queueChanged(t, grokTestSession, id))
		last = id
	}

	assert.Len(t, seen, senders*perSender)
	assert.False(t, a.AgentTurnActive(), "no prompt of LeapMux's opens an agent turn")
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Equal(t, map[string]bool{last: true}, a.turns.own, "each replaced prompt is forgotten")
	assert.Equal(t, last, a.turns.running)
}
