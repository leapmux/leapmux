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
	agentID          string
	kind, id, parent string
	ephemeral        bool
	data             map[string]any
}

func deliverCopilotReplyEvents(t *testing.T, a *Agent, events []copilotReplyTestEvent) {
	t.Helper()
	for _, event := range events {
		payload := map[string]any{
			"type": event.kind, "id": event.id, "parentId": event.parent, "data": event.data,
			"ephemeral": event.ephemeral,
		}
		if event.agentID != "" {
			payload["agentId"] = event.agentID
		}
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "session.event",
			"params": map[string]any{"sessionId": "session-1", "event": payload},
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
			receipt, err := h.agent.SendInput("Verify.", nil)
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
	receipt, err := h.agent.SendInput("Verify.", nil)
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

func TestCopilotSteerSharesItsReceivingRunWithoutBorrowingEarlierText(t *testing.T) {
	t.Parallel()
	for _, ackBeforeEcho := range []bool{true, false} {
		name := "echo before acknowledgment"
		if ackBeforeEcho {
			name = "acknowledgment before echo"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink := newNativeCopilotForEvents(t)
			leadReceipt := a.acceptNativeReplyCapture(a.beginNativeReplyCapture("session-1"), "lead")
			deliverCopilotReplyEvents(t, a, []copilotReplyTestEvent{
				{kind: contracts.CopilotEventUserMessage, id: "lead-echo", data: map[string]any{"messageId": "lead", "interactionId": "initial"}},
				{kind: contracts.CopilotEventAssistantTurnStart, id: "lead-start", parent: "lead-echo", data: map[string]any{"turnId": "0", "interactionId": "initial"}},
				{kind: contracts.CopilotEventAssistantMessage, id: "lead-reply", parent: "lead-start", data: map[string]any{"turnId": "0", "interactionId": "initial", "originatingMessageId": "lead", "content": "Lead reply."}},
				{kind: contracts.CopilotEventAssistantTurnEnd, id: "lead-end", parent: "lead-reply", data: map[string]any{"turnId": "0"}},
			})
			steer := a.beginNativeSteerCapture("session-1", agent.SteerRequest{InputID: "queued-steer", AttemptID: "steer-attempt", Content: "Review the second item."})
			var steerReceipt agent.NativeInputReceipt
			if ackBeforeEcho {
				steerReceipt = a.acceptNativeReplyCapture(steer, "steer")
			}
			deliverCopilotReplyEvents(t, a, []copilotReplyTestEvent{
				{kind: contracts.CopilotEventUserMessage, id: "steer-echo", parent: "lead-end", data: map[string]any{"messageId": "steer", "interactionId": "steering"}},
				{kind: contracts.CopilotEventAssistantTurnStart, id: "steer-start", parent: "steer-echo", data: map[string]any{"turnId": "0", "interactionId": "steering"}},
				{kind: contracts.CopilotEventAssistantMessage, id: "steer-reply", parent: "steer-start", data: map[string]any{"turnId": "0", "interactionId": "steering", "originatingMessageId": "lead", "content": "Steered reply."}},
				{kind: contracts.CopilotEventAssistantTurnEnd, id: "steer-end", parent: "steer-reply", data: map[string]any{"turnId": "0"}},
				{kind: contracts.CopilotEventSessionIdle, id: "idle", parent: "steer-end", ephemeral: true},
			})
			if !ackBeforeEcho {
				require.Empty(t, sink.NativeReplyTurns())
				require.Empty(t, sink.NativeReplyRetirements(), "the echo can still be matched by the command response")
				steerReceipt = a.acceptNativeReplyCapture(steer, "steer")
			}
			assert.Equal(t, []agent.SteerResolution{{
				InputID: "queued-steer", AttemptID: "steer-attempt", Receipt: steerReceipt, Outcome: agent.SteerOutcomeEchoed,
			}}, sink.SteerResolutions())
			assert.ElementsMatch(t, []agent.NativeReplyTurn{
				{SessionID: leadReceipt.SessionID, TurnID: leadReceipt.TurnID, Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
					Inputs: []agent.NativeReplyBoundary{{InputID: leadReceipt.InputID, AfterOrdinal: 1}},
					Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: copilotReplyMessageKey("lead-reply")}, {Ordinal: 3, MessageKey: copilotReplyMessageKey("steer-reply")}}},
				{SessionID: steerReceipt.SessionID, TurnID: steerReceipt.TurnID, Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
					Inputs: []agent.NativeReplyBoundary{{InputID: steerReceipt.InputID, AfterOrdinal: 1}},
					Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: copilotReplyMessageKey("steer-reply")}}},
			}, sink.NativeReplyTurns())
		})
	}
}

func TestCopilotSendInputForSessionAndSteerInputReturnReceipts(t *testing.T) {
	t.Parallel()
	h := newCopilotRuntimeHarness(t, map[string]string{"session.send": `{"messageId":"lead-input"}`}, nil)
	leadReceipt, err := h.agent.SendInputForSession("session-1", "Verify.", nil)
	require.NoError(t, err)
	assert.Equal(t, agent.NativeInputReceipt{SessionID: "session-1", InputID: "lead-input", TurnID: "lead-input"}, leadReceipt)
	deliverCopilotReplyEvents(t, h.agent, []copilotReplyTestEvent{
		{kind: contracts.CopilotEventUserMessage, id: "lead-echo", data: map[string]any{"messageId": "lead-input", "interactionId": "initial"}},
		{kind: contracts.CopilotEventAssistantTurnStart, id: "lead-start", parent: "lead-echo", data: map[string]any{"turnId": "0", "interactionId": "initial"}},
		{kind: contracts.CopilotEventAssistantMessage, id: "lead-reply", parent: "lead-start", data: map[string]any{"turnId": "0", "interactionId": "initial", "originatingMessageId": "lead-input", "content": "Lead reply."}},
		{kind: contracts.CopilotEventAssistantTurnEnd, id: "lead-end", parent: "lead-reply", data: map[string]any{"turnId": "0"}},
		{kind: contracts.CopilotEventSessionIdle, id: "idle", parent: "lead-end", ephemeral: true, data: map[string]any{"mode": "interactive"}},
	})
	require.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "session-1", TurnID: "lead-input", Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{{InputID: "lead-input", AfterOrdinal: 1}},
		Items:  []agent.NativeReplyItem{{Ordinal: 2, MessageKey: copilotReplyMessageKey("lead-reply")}},
	}}, h.sink.NativeReplyTurns())

	a, _, _ := newCopilotSteeringAgent(t, copilotSteerScript{})
	steerReceipt, err := a.SteerInput(testCopilotSteerRequest("Focus on the diff."))
	require.NoError(t, err)
	assert.Zero(t, steerReceipt, "RowAtEcho steer must return an empty receipt so inputqueue holds it for echo")

	// Control-reply steers carry an empty AttemptID and must succeed without steer tracking.
	controlReceipt, err := a.SteerInput(agent.SteerRequest{InputID: "control-feedback", Content: "denied reason"})
	require.NoError(t, err)
	assert.Zero(t, controlReceipt)
}
