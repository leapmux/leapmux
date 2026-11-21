package codewhale

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestCodewhaleReplyKeepsItemsPublishedBeforeTheStartResponse(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	a, sink := newTestAgent(t, runtime)
	runtime.handle(http.MethodPost, turnsRoute, func(response http.ResponseWriter, _ *http.Request) {
		const foreign = "turn_foreign"
		a.HandleOutput(turnStartedEvent(1, foreign))
		a.HandleOutput(runtimeEvent(2, "item.completed", foreign, "foreign", map[string]any{
			"item": turnItem{ID: "foreign", TurnID: foreign, Kind: contracts.CodewhaleItemKindAgentMessage, Detail: "An autonomous turn."},
		}))
		a.HandleOutput(turnCompletedEvent(3, foreign, contracts.CodewhaleTurnStatusCompleted))

		a.HandleOutput(turnStartedEvent(4, testTurnID))
		a.HandleOutput(itemEvent(5, "item.completed", "response-1", contracts.CodewhaleItemKindAgentMessage, "First result.", nil))
		a.HandleOutput(runtimeEvent(6, "item.delta", testTurnID, "response-2", map[string]any{
			"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "Second result.",
		}))
		a.HandleOutput(itemEvent(7, "item.completed", "response-2", contracts.CodewhaleItemKindAgentMessage, "", nil))
		a.HandleOutput(turnCompletedEvent(8, testTurnID, contracts.CodewhaleTurnStatusCompleted))
		writeFakeJSON(response, http.StatusCreated, turnStartResponse{Turn: turnRecord{ID: testTurnID, Status: "queued"}})
	})

	receipt, err := a.SendInputWithReceipt("Check the result.", nil)
	require.NoError(t, err)
	assert.Equal(t, agent.NativeInputReceipt{SessionID: testThreadID, InputID: testTurnID, TurnID: testTurnID}, receipt)
	firstKey := codewhaleReplyMessageKey(testTurnID, "response-1")
	secondKey := codewhaleReplyMessageKey(testTurnID, "response-2")
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: testThreadID,
		TurnID:    testTurnID,
		Outcome:   leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:    []agent.NativeReplyBoundary{{InputID: testTurnID, AfterOrdinal: 1}},
		Items: []agent.NativeReplyItem{
			{Ordinal: 2, MessageKey: firstKey},
			{Ordinal: 3, MessageKey: secondKey},
		},
	}}, sink.NativeReplyTurns())
	textByKey := make(map[string]string)
	for _, message := range sink.Messages() {
		if message.NativeTurnID == testTurnID {
			text, ok := agent.ReplyText(codewhaleProvider{}, agent.MessageContent{Original: message.Content})
			require.True(t, ok)
			assert.Equal(t, testThreadID, message.AgentSessionID)
			textByKey[message.IdempotencyKey] = text
		}
	}
	assert.Equal(t, map[string]string{firstKey: "First result.", secondKey: "Second result."}, textByKey)
	assert.Empty(t, sink.NativeReplyRetirements())
	assert.Empty(t, a.replyTurns)
	assert.Empty(t, a.turnID, "the late start response must not reopen the finished turn")
}

func TestCodewhaleReplyRejectsAConflictingItemTurn(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	acceptTurns(runtime, testTurnID)
	a, sink := newTestAgent(t, runtime)
	receipt, err := a.SendInputWithReceipt("Check the result.", nil)
	require.NoError(t, err)

	a.HandleOutput(runtimeEvent(1, "item.completed", testTurnID, "conflict", map[string]any{
		"item": turnItem{ID: "conflict", TurnID: "turn_other", Kind: contracts.CodewhaleItemKindAgentMessage, Detail: "Another turn's text."},
	}))
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusCompleted))

	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
}

func TestCodewhaleReplyWithoutANativeEndRetiresOnProcessExit(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	acceptTurns(runtime, testTurnID)
	a, sink := newTestAgent(t, runtime)
	receipt, err := a.SendInputWithReceipt("Check the result.", nil)
	require.NoError(t, err)
	a.HandleOutput(itemEvent(1, "item.completed", "partial", contracts.CodewhaleItemKindAgentMessage, "Only part of the turn.", nil))

	a.finishAfterExit(agent.MessageCompletionInterrupted)

	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
}

func TestCodewhaleReplyNeedsTheTurnIDFromAcceptance(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	acceptTurns(runtime, "")
	a, sink := newTestAgent(t, runtime)

	receipt, err := a.SendInputWithReceipt("Check the result.", nil)

	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Empty(t, receipt)
	assert.Empty(t, a.replyTurns)
	assert.Zero(t, a.replyStarts)
	assert.Empty(t, sink.NativeReplyTurns())
}

func TestCodewhaleReplyWithAnOpenItemCannotCompleteAfterANewerStart(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	acceptTurns(runtime, testTurnID)
	a, sink := newTestAgent(t, runtime)
	receipt, err := a.SendInputWithReceipt("Check the result.", nil)
	require.NoError(t, err)
	a.HandleOutput(itemEvent(1, "item.started", "unfinished", contracts.CodewhaleItemKindAgentMessage, "", nil))
	a.HandleOutput(runtimeEvent(2, "item.delta", testTurnID, "unfinished", map[string]any{
		"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "A partial result.",
	}))

	// A separate start response can overtake the old turn's SSE ending.
	require.True(t, a.markTurnStarted("turn_newer"))
	a.HandleOutput(turnCompletedEvent(3, testTurnID, contracts.CodewhaleTurnStatusCompleted))

	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
	assert.Equal(t, "turn_newer", a.turnID)
}
