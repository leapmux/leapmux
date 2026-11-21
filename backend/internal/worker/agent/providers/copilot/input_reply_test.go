package copilot

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type copilotReplyTestEvent struct {
	kind, id, parent string
	ephemeral        bool
	data             map[string]any
}

func deliverCopilotReplyEvents(t *testing.T, a *Agent, events []copilotReplyTestEvent) {
	t.Helper()
	for _, event := range events {
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "session.event",
			"params": map[string]any{"sessionId": "session-1", "event": map[string]any{
				"type": event.kind, "id": event.id, "parentId": event.parent, "data": event.data,
				"ephemeral": event.ephemeral,
			}},
		})
		require.NoError(t, err)
		a.HandleOutput(raw)
	}
}

func TestCopilotReplyUsesTheAcknowledgedInputAndItsFinalIdle(t *testing.T) {
	t.Parallel()
	events := []copilotReplyTestEvent{
		{kind: contracts.CopilotEventUserMessage, id: "input-echo", data: map[string]any{"messageId": "input", "interactionId": "interaction"}},
		{kind: contracts.CopilotEventAssistantTurnStart, id: "start-0", parent: "unpublished-telemetry", data: map[string]any{"turnId": "0", "interactionId": "interaction"}},
		{kind: contracts.CopilotEventAssistantMessage, id: "reply-0", parent: "unpublished-call-trace", data: map[string]any{"turnId": "0", "originatingMessageId": "input", "content": "First check."}},
		{kind: contracts.CopilotEventAssistantTurnEnd, id: "end-0", parent: "unpublished-tool-output", data: map[string]any{"turnId": "0"}},
		{kind: contracts.CopilotEventSessionIdle, id: "earlier-idle", parent: "earlier-turn-end", ephemeral: true, data: map[string]any{"mode": "interactive"}},
		{kind: contracts.CopilotEventAssistantTurnStart, id: "start-1", parent: "end-0", data: map[string]any{"turnId": "1", "interactionId": "interaction"}},
		{kind: contracts.CopilotEventAssistantMessage, id: "reply-1", parent: "start-1", data: map[string]any{"turnId": "1", "originatingMessageId": "input", "content": "Second check."}},
		{kind: contracts.CopilotEventAssistantTurnEnd, id: "end-1", parent: "reply-1", data: map[string]any{"turnId": "1"}},
		{kind: contracts.CopilotEventSessionUsageCheckpoint, id: "checkpoint", parent: "end-1"},
		{kind: contracts.CopilotEventSessionIdle, id: "idle", parent: "checkpoint", ephemeral: true, data: map[string]any{"mode": "interactive"}},
	}
	for _, test := range []struct {
		name   string
		before bool
	}{
		{name: "events before response", before: true},
		{name: "events after response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newCopilotRuntimeHarness(t, map[string]string{"session.send": `{"messageId":"input"}`}, func(a *Agent, method string) {
				if test.before && method == "session.send" {
					deliverCopilotReplyEvents(t, a, events)
				}
			})
			receipt, err := h.agent.SendInputWithReceipt("Verify.", nil)
			require.NoError(t, err)
			assert.Equal(t, agent.NativeInputReceipt{SessionID: "session-1", InputID: "input", TurnID: "input"}, receipt)
			if !test.before {
				deliverCopilotReplyEvents(t, h.agent, events[:5])
				assert.Empty(t, h.sink.NativeReplyTurns(), "an iteration ending and another run's idle carry no completion")
				deliverCopilotReplyEvents(t, h.agent, events[5:])
			}
			require.Equal(t, []agent.NativeReplyTurn{{
				SessionID: "session-1", TurnID: "input", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
				Inputs: []agent.NativeReplyBoundary{{InputID: "input", AfterOrdinal: 1}},
				Items: []agent.NativeReplyItem{
					{Ordinal: 2, MessageKey: copilotReplyMessageKey("reply-0")},
					{Ordinal: 3, MessageKey: copilotReplyMessageKey("reply-1")},
				},
			}}, h.sink.NativeReplyTurns())
		})
	}
}

func TestCopilotReplyRetiresWhenAnotherInputOverlaps(t *testing.T) {
	t.Parallel()
	h := newCopilotRuntimeHarness(t, map[string]string{"session.send": `{"messageId":"input"}`}, nil)
	receipt, err := h.agent.SendInputWithReceipt("Verify.", nil)
	require.NoError(t, err)
	deliverCopilotReplyEvents(t, h.agent, []copilotReplyTestEvent{
		{kind: contracts.CopilotEventUserMessage, id: "input-echo", data: map[string]any{"messageId": "input", "interactionId": "interaction"}},
		{kind: contracts.CopilotEventAssistantTurnStart, id: "start", parent: "input-echo", data: map[string]any{"turnId": "0", "interactionId": "interaction"}},
		{kind: contracts.CopilotEventUserMessage, id: "other-input-echo", parent: "start", data: map[string]any{"messageId": "foreign", "interactionId": "interaction"}},
		{kind: contracts.CopilotEventAssistantMessage, id: "foreign-reply", parent: "other-input-echo", data: map[string]any{"turnId": "0", "originatingMessageId": "foreign", "content": "Another input's reply."}},
		{kind: contracts.CopilotEventAssistantTurnEnd, id: "end", parent: "foreign-reply", data: map[string]any{"turnId": "0"}},
		{kind: contracts.CopilotEventSessionIdle, id: "idle", parent: "end", ephemeral: true},
	})
	assert.Empty(t, h.sink.NativeReplyTurns())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, h.sink.NativeReplyRetirements()[receipt])
}

func TestCopilotReplyAcknowledgedAfterProcessExit(t *testing.T) {
	t.Parallel()
	frames := []copilotReplyTestEvent{
		{kind: contracts.CopilotEventUserMessage, id: "echo", data: map[string]any{"messageId": "input", "interactionId": "interaction"}},
		{kind: contracts.CopilotEventAssistantTurnStart, id: "start", parent: "echo", data: map[string]any{"turnId": "0", "interactionId": "interaction"}},
		{kind: contracts.CopilotEventAssistantMessage, id: "reply", parent: "start", data: map[string]any{"turnId": "0", "originatingMessageId": "input", "content": "The check completed."}},
		{kind: contracts.CopilotEventAssistantTurnEnd, id: "end", parent: "reply", data: map[string]any{"turnId": "0"}},
		{kind: contracts.CopilotEventSessionIdle, id: "idle", parent: "end", ephemeral: true, data: map[string]any{"mode": "interactive"}},
	}
	for _, completed := range []bool{false, true} {
		testName := "no final idle"
		if completed {
			testName = "final idle buffered"
		}
		t.Run(testName, func(t *testing.T) {
			t.Parallel()
			a, sink := newNativeCopilotForEvents(t)
			capture := a.beginNativeReplyCapture("session-1")
			delivered := frames
			if !completed {
				delivered = frames[:len(frames)-1]
			}
			deliverCopilotReplyEvents(t, a, delivered)
			a.outputMu.Lock()
			a.closing = true
			a.discardNativeReplyCaptures()
			a.outputMu.Unlock()

			receipt := a.acceptNativeReplyCapture(capture, "input")
			assert.Empty(t, a.replyCaptures)
			if completed {
				require.Len(t, sink.NativeReplyTurns(), 1)
				assert.Equal(t, receipt.TurnID, sink.NativeReplyTurns()[0].TurnID)
				assert.Empty(t, sink.NativeReplyRetirements())
				return
			}
			assert.Empty(t, sink.NativeReplyTurns())
			assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
		})
	}
}
