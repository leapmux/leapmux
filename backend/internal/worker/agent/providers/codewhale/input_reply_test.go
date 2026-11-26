package codewhale

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
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

	receipt, err := a.SendInput("Check the result.", nil)
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
	assert.Zero(t, a.replies.Len())
	assert.Empty(t, a.turnID, "the late start response must not reopen the finished turn")
}

func TestCodewhaleReplyRejectsAConflictingItemTurn(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	acceptTurns(runtime, testTurnID)
	a, sink := newTestAgent(t, runtime)
	receipt, err := a.SendInput("Check the result.", nil)
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
	receipt, err := a.SendInput("Check the result.", nil)
	require.NoError(t, err)
	a.HandleOutput(itemEvent(1, "item.completed", "partial", contracts.CodewhaleItemKindAgentMessage, "Only part of the turn.", nil))

	a.finishAfterExit(agent.MessageCompletionInterrupted)

	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
}

func TestCodewhaleSteerKeepsItsReplyWithoutALeadReceipt(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptSteers(rt)
	a, sink := newSteeringAgent(t, rt)
	a.beginCodewhaleReplyStart()
	require.NoError(t, agenttest.SteerInput(a, testCodewhaleSteer(1, "Also this.")))
	a.HandleOutput(steeredEvent(2, "Also this."))
	a.HandleOutput(itemEvent(3, contracts.CodewhaleEventItemCompleted, "answer", contracts.CodewhaleItemKindAgentMessage, "The steered reply.", nil))
	a.HandleOutput(turnCompletedEvent(4, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	assert.Equal(t, []agent.SteerResolution{testCodewhaleEcho(1, "item_steer_1", testTurnID)}, sink.SteerResolutions())
	require.Empty(t, sink.NativeReplyTurns(), "the lead's response can still identify an input in this turn")

	a.finishCodewhaleReplyStart(agent.NativeInputReceipt{})
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: testThreadID, TurnID: testTurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: "item_steer_1", AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: codewhaleReplyMessageKey(testTurnID, "answer")}},
	}}, sink.NativeReplyTurns())
}

func TestCodewhaleReplyNeedsTheTurnIDFromAcceptance(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	acceptTurns(runtime, "")
	a, sink := newTestAgent(t, runtime)

	receipt, err := a.SendInput("Check the result.", nil)

	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.Empty(t, receipt)
	assert.Zero(t, a.replies.Len())
	assert.Zero(t, a.replies.PendingStarts())
	assert.Empty(t, sink.NativeReplyTurns())
}

func TestCodewhaleReplyWithAnOpenItemCannotCompleteAfterANewerStart(t *testing.T) {
	t.Parallel()
	runtime := newFakeRuntime(t)
	acceptTurns(runtime, testTurnID)
	a, sink := newTestAgent(t, runtime)
	receipt, err := a.SendInput("Check the result.", nil)
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

type orderedRetirementSink struct {
	agent.ProviderServices
	retiredTurns []string
}

func (s *orderedRetirementSink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	s.retiredTurns = append(s.retiredTurns, receipt.TurnID)
	return s.ProviderServices.RetireNativeInputTurn(receipt, outcome)
}

func TestCodewhaleReplyRetirementAndSteerFlushOrderIsDeterministic(t *testing.T) {
	t.Parallel()
	wantTurns := []string{"turn_1", "turn_2", "turn_3", "turn_4", "turn_5", "turn_6", "turn_7", "turn_8"}

	for range 16 {
		var recorder *orderedRetirementSink
		a, _ := newTestAgentWith(t, nil, testAgentOptions{services: func(inner agent.ProviderServices) agent.ProviderServices {
			recorder = &orderedRetirementSink{ProviderServices: inner}
			return recorder
		}})
		for _, turnID := range wantTurns {
			a.beginCodewhaleReplyStart()
			a.finishCodewhaleReplyStart(codewhaleInputReceipt(testThreadID, turnID))
		}
		a.dispatchMu.Lock()
		a.retireCodewhaleRepliesAfterExit()
		a.dispatchMu.Unlock()
		require.Equal(t, wantTurns, recorder.retiredTurns)
	}

	for range 16 {
		a, sink := newTestAgent(t, nil)
		a.beginCodewhaleReplyStart()
		a.dispatchMu.Lock()
		for _, turnID := range wantTurns {
			require.True(t, a.replies.RecordSteerBoundary(turnID, agent.NativeInputReceipt{
				SessionID: testThreadID, TurnID: turnID, InputID: "steer_" + turnID,
			}))
			a.replies.Finish(turnID, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, a.sink)
		}
		a.dispatchMu.Unlock()
		a.finishCodewhaleReplyStart(agent.NativeInputReceipt{})
		turns := sink.NativeReplyTurns()
		require.Len(t, turns, len(wantTurns))
		gotTurns := make([]string, len(turns))
		for i, turn := range turns {
			gotTurns[i] = turn.TurnID
		}
		require.Equal(t, wantTurns, gotTurns)
	}
}

type failMessagePersistSink struct {
	agent.ProviderServices
	err error
}

func (s *failMessagePersistSink) OutputQueue() *agent.OutputQueue { return nil }

func (s *failMessagePersistSink) PersistMessage(leapmuxv1.MessageSource, agent.MessageContent, agent.SpanInfo) error {
	return s.err
}

func TestCodewhaleReplyUnprovedOnStreamedPersistFailureAndSteerBoundaryGuards(t *testing.T) {
	t.Parallel()

	t.Run("non-retained final message persist failure retires turn as unproved", func(t *testing.T) {
		t.Parallel()
		runtime := newFakeRuntime(t)
		acceptTurns(runtime, testTurnID)
		a, sink := newTestAgent(t, runtime)
		a.sink = &failMessagePersistSink{ProviderServices: a.sink, err: assert.AnError}

		receipt, err := a.SendInput("Check the result.", nil)
		require.NoError(t, err)
		a.HandleOutput(itemEvent(1, "item.completed", "msg-1", contracts.CodewhaleItemKindAgentMessage, "Final reply.", nil))
		a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusCompleted))

		assert.Empty(t, sink.NativeReplyTurns())
		assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
	})

	t.Run("retained pending streamed text error still publishes exact reply", func(t *testing.T) {
		t.Parallel()
		runtime := newFakeRuntime(t)
		acceptTurns(runtime, testTurnID)
		a, sink := newTestAgent(t, runtime)
		a.sink = &failMessagePersistSink{ProviderServices: a.sink, err: assert.AnError}

		_, err := a.SendInput("Check the result.", nil)
		require.NoError(t, err)
		a.HandleOutput(runtimeEvent(1, "item.delta", testTurnID, "streamed-1", map[string]any{
			"kind": contracts.CodewhaleItemKindAgentMessage, "delta": "Streamed reply.",
		}))
		a.HandleOutput(itemEvent(2, "item.completed", "streamed-1", contracts.CodewhaleItemKindAgentMessage, "", nil))
		a.HandleOutput(turnCompletedEvent(3, testTurnID, contracts.CodewhaleTurnStatusCompleted))

		assert.Empty(t, sink.NativeReplyRetirements())
		require.Len(t, sink.NativeReplyTurns(), 1)
		assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: codewhaleReplyMessageKey(testTurnID, "streamed-1")}}, sink.NativeReplyTurns()[0].Items)
	})

	t.Run("recordCodewhaleSteerBoundary rejects empty or mismatched thread ID", func(t *testing.T) {
		t.Parallel()
		a, _ := newTestAgent(t, nil)
		a.dispatchMu.Lock()
		defer a.dispatchMu.Unlock()
		assert.False(t, a.recordCodewhaleSteerBoundary(agent.NativeInputReceipt{
			SessionID: "", TurnID: testTurnID, InputID: "steer-1",
		}))
		assert.False(t, a.recordCodewhaleSteerBoundary(agent.NativeInputReceipt{
			SessionID: "other-thread", TurnID: testTurnID, InputID: "steer-1",
		}))
		assert.Zero(t, a.replies.Len())
	})
}
