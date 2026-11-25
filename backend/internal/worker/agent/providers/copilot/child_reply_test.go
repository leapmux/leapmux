package copilot

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type copilotChildReplyTestEvent struct {
	agentID string
	kind    string
	id      string
	parent  string
	data    map[string]any
}

func deliverCopilotChildReplyEvents(t *testing.T, a *Agent, events []copilotChildReplyTestEvent) {
	t.Helper()
	for _, event := range events {
		payload := map[string]any{
			"type": event.kind, "id": event.id, "parentId": event.parent, "data": event.data,
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

type copilotRecordedControlSink struct {
	*agenttest.Sink
	mu               sync.Mutex
	published        []agent.ControlRequest
	canceled         []string
	controlBindings  []agent.NativeControlInputs
	turnBindings     map[agent.NativeInputReceipt]string
	childControlSink map[string]*copilotRecordedControlSink
}

func newCopilotRecordedControlSink(base *agenttest.Sink) *copilotRecordedControlSink {
	return &copilotRecordedControlSink{
		Sink:             base,
		turnBindings:     make(map[agent.NativeInputReceipt]string),
		childControlSink: make(map[string]*copilotRecordedControlSink),
	}
}

func (s *copilotRecordedControlSink) ChildSink(childAgentID string) agent.ProviderServices {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.childControlSink[childAgentID]; existing != nil {
		return agent.NewProviderServices(existing)
	}
	wrapped := newCopilotRecordedControlSink(s.Child(childAgentID))
	s.childControlSink[childAgentID] = wrapped
	return agent.NewProviderServices(wrapped)
}

func (s *copilotRecordedControlSink) recordedChild(childAgentID string) *copilotRecordedControlSink {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.childControlSink[childAgentID]
}

func (s *copilotRecordedControlSink) PublishControlRequest(req agent.ControlRequest) error {
	s.mu.Lock()
	s.published = append(s.published, req)
	s.mu.Unlock()
	return nil
}

func (s *copilotRecordedControlSink) CancelControlRequestInstance(requestID, instanceID string) {
	s.mu.Lock()
	s.canceled = append(s.canceled, requestID+":"+instanceID)
	s.mu.Unlock()
}

func (s *copilotRecordedControlSink) SetNativeControlInputs(control agent.NativeControlInputs) error {
	s.mu.Lock()
	s.controlBindings = append(s.controlBindings, control)
	s.mu.Unlock()
	return nil
}

func (s *copilotRecordedControlSink) BindNativeInputTurn(receipt agent.NativeInputReceipt, turnID string) error {
	s.mu.Lock()
	s.turnBindings[receipt] = turnID
	s.mu.Unlock()
	return nil
}

func TestCopilotChildIdleSendPublishesExactReply(t *testing.T) {
	t.Parallel()
	h := newCopilotTaskHarness(t, copilotTaskScript{})
	rootSink := newCopilotRecordedControlSink(h.sink)
	h.agent.sink = agent.NewProviderServices(rootSink)
	row, childSink := startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)
	childRecorded := rootSink.recordedChild(row.ChildAgentID)
	require.NotNil(t, childRecorded)

	// Verify the child agent record carries the root Copilot session ID so control routing matches.
	h.agent.outputMu.Lock()
	assert.Equal(t, "session-1", h.agent.currentNativeSessionID())
	h.agent.outputMu.Unlock()

	pauseCopilotSubagent(t, h.agent)
	result, err := h.agent.SendChildInput("agent-1", agent.ChildInput{Content: "Now say CHILD_AGAIN."})
	require.NoError(t, err)
	require.Equal(t, "session-1", result.NativeReceipt.SessionID)
	require.NotEmpty(t, result.NativeReceipt.InputID)

	deliverCopilotChildReplyEvents(t, h.agent, []copilotChildReplyTestEvent{
		{agentID: "agent-1", kind: contracts.CopilotEventUserMessage, id: "child-user-1", data: map[string]any{
			"messageId": "m-idle-1", "interactionId": "int-idle-1", "turnId": "0",
			"delivery": "idle", "content": "Now say CHILD_AGAIN.",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnStart, id: "child-start-1", parent: "child-user-1", data: map[string]any{
			"turnId": "0", "interactionId": "int-idle-1",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantMessage, id: "child-reply-1", parent: "child-start-1", data: map[string]any{
			"turnId": "0", "interactionId": "int-idle-1", "originatingMessageId": "m-idle-1", "content": "CHILD_AGAIN",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnEnd, id: "child-end-1", parent: "child-reply-1", data: map[string]any{
			"turnId": "0",
		}},
		{kind: contracts.CopilotEventSystemNotification, id: "child-idle-1", parent: "child-end-1", data: map[string]any{
			"kind": map[string]any{"type": copilotAgentIdle, "agentId": "agent-1"},
		}},
	})

	require.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "session-1", TurnID: "int-idle-1",
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: result.NativeReceipt.InputID, AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: copilotReplyMessageKey("child-reply-1")}},
	}}, childSink.NativeReplyTurns())
	assert.Empty(t, h.sink.NativeReplyTurns())
	assert.Empty(t, childSink.NativeReplyRetirements())
}

func TestCopilotChildIdenticalSteersShareExactReplyAndIgnoreParentWriteAgent(t *testing.T) {
	t.Parallel()
	h := newCopilotTaskHarness(t, copilotTaskScript{})
	_, childSink := startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)

	// The child's initial iteration runs and receives a parent write_agent message with identical text.
	deliverCopilotChildReplyEvents(t, h.agent, []copilotChildReplyTestEvent{
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnStart, id: "init-start", data: map[string]any{
			"turnId": "0", "interactionId": "int-run",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantMessage, id: "init-msg", parent: "init-start", data: map[string]any{
			"turnId": "0", "interactionId": "int-run", "originatingMessageId": "m-spawn", "content": "Initial reply.",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnEnd, id: "init-end", parent: "init-msg", data: map[string]any{
			"turnId": "0",
		}},
	})

	_, err := h.agent.SteerChildInput("agent-1", agent.SteerRequest{
		InputID: "queue-1", AttemptID: "attempt-1", Content: "Also say CHILD_STEERED.",
	})
	require.NoError(t, err)
	_, err = h.agent.SteerChildInput("agent-1", agent.SteerRequest{
		InputID: "queue-2", AttemptID: "attempt-2", Content: "Also say CHILD_STEERED.",
	})
	require.NoError(t, err)

	// An earlier iteration receives a parent write_agent echo with identical content; it must not consume client admissions.
	deliverCopilotChildReplyEvents(t, h.agent, []copilotChildReplyTestEvent{
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnStart, id: "parent-iter-start", data: map[string]any{
			"turnId": "1", "interactionId": "int-run",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventUserMessage, id: "parent-echo", parent: "parent-iter-start", data: map[string]any{
			"messageId": "m-parent", "interactionId": "int-run", "turnId": "1",
			"delivery": "steering", "source": "agent-session-1", "content": "Also say CHILD_STEERED.",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantMessage, id: "parent-reply", parent: "parent-echo", data: map[string]any{
			"turnId": "1", "interactionId": "int-run", "originatingMessageId": "m-spawn", "content": "Parent reply.",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnEnd, id: "parent-iter-end", parent: "parent-reply", data: map[string]any{
			"turnId": "1",
		}},
	})
	assert.Empty(t, childSink.SteerResolutions(), "parent write_agent must not resolve client steers")
	assert.Empty(t, childSink.NativeReplyTurns())

	// Next iteration receives both client steers (source omitted) plus another parent echo, and replies once.
	deliverCopilotChildReplyEvents(t, h.agent, []copilotChildReplyTestEvent{
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnStart, id: "steer-start", data: map[string]any{
			"turnId": "2", "interactionId": "int-run",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventUserMessage, id: "client-echo-1", parent: "steer-start", data: map[string]any{
			"messageId": "m-c1", "interactionId": "int-run", "turnId": "2",
			"delivery": "steering", "content": "Also say CHILD_STEERED.",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventUserMessage, id: "client-echo-2", parent: "client-echo-1", data: map[string]any{
			"messageId": "m-c2", "interactionId": "int-run", "turnId": "2",
			"delivery": "steering", "content": "Also say CHILD_STEERED.",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantMessage, id: "steer-reply", parent: "client-echo-2", data: map[string]any{
			"turnId": "2", "interactionId": "int-run", "originatingMessageId": "m-spawn", "content": "CHILD_STEERED",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnEnd, id: "steer-end", parent: "steer-reply", data: map[string]any{
			"turnId": "2",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventSubagentCompleted, id: "sub-done", parent: "steer-end", data: map[string]any{
			"toolCallId": "task-1",
		}},
	})

	resolutions := childSink.SteerResolutions()
	require.Len(t, resolutions, 2)
	assert.Equal(t, "queue-1", resolutions[0].InputID)
	assert.Equal(t, agent.SteerOutcomeEchoed, resolutions[0].Outcome)
	assert.Equal(t, "int-run", resolutions[0].Receipt.TurnID)
	assert.Equal(t, "queue-2", resolutions[1].InputID)
	assert.Equal(t, agent.SteerOutcomeEchoed, resolutions[1].Outcome)
	assert.Equal(t, "int-run", resolutions[1].Receipt.TurnID)
	assert.NotEqual(t, resolutions[0].Receipt.InputID, resolutions[1].Receipt.InputID)

	require.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "session-1", TurnID: "int-run",
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{
			{InputID: resolutions[0].Receipt.InputID, AfterOrdinal: 1},
			{InputID: resolutions[1].Receipt.InputID, AfterOrdinal: 1},
		},
		Items: []agent.NativeReplyItem{
			{Ordinal: 2, MessageKey: copilotReplyMessageKey("steer-reply")},
		},
	}}, childSink.NativeReplyTurns())
}

func TestCopilotChildEarlyEchoDeferredUntilRPCReceipt(t *testing.T) {
	t.Parallel()
	earlyDelivered := false
	h := newCopilotRuntimeHarness(t, map[string]string{
		"session.tasks.list":        copilotTaskListAnswer,
		"session.tasks.sendMessage": `{"sent":true}`,
	}, func(a *Agent, method string) {
		if method == "session.tasks.sendMessage" && !earlyDelivered {
			earlyDelivered = true
			deliverCopilotChildReplyEvents(t, a, []copilotChildReplyTestEvent{
				{agentID: "agent-1", kind: contracts.CopilotEventUserMessage, id: "early-user", data: map[string]any{
					"messageId": "m-early", "interactionId": "int-early", "turnId": "0",
					"delivery": "idle", "content": "Early echo.",
				}},
				{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnStart, id: "early-start", parent: "early-user", data: map[string]any{
					"turnId": "0", "interactionId": "int-early",
				}},
				{agentID: "agent-1", kind: contracts.CopilotEventAssistantMessage, id: "early-reply", parent: "early-start", data: map[string]any{
					"turnId": "0", "interactionId": "int-early", "originatingMessageId": "m-early", "content": "EARLY_DONE",
				}},
				{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnEnd, id: "early-end", parent: "early-reply", data: map[string]any{
					"turnId": "0",
				}},
				{kind: contracts.CopilotEventSystemNotification, id: "early-idle", parent: "early-end", data: map[string]any{
					"kind": map[string]any{"type": copilotAgentIdle, "agentId": "agent-1"},
				}},
			})
		}
	})
	_, childSink := startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)
	pauseCopilotSubagent(t, h.agent)

	result, err := h.agent.SendChildInput("agent-1", agent.ChildInput{Content: "Early echo."})
	require.NoError(t, err)
	require.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "session-1", TurnID: "int-early",
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: result.NativeReceipt.InputID, AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: copilotReplyMessageKey("early-reply")}},
	}}, childSink.NativeReplyTurns())
}

func TestCopilotChildUncertainDeliveryTaintsIdenticalPrompt(t *testing.T) {
	t.Parallel()
	h := newCopilotTaskHarness(t, copilotTaskScript{send: `{}`})
	// First send returns an omitted receipt (ErrDeliveryUncertain); second admission simulates a retry.
	_, childSink := startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)
	pauseCopilotSubagent(t, h.agent)

	_, err := h.agent.SendChildInput("agent-1", agent.ChildInput{Content: "Repeat."})
	require.ErrorIs(t, err, agent.ErrDeliveryUncertain)

	// Register a second acknowledged admission with the same prompt text.
	child, second, err := h.agent.beginChildAdmission("agent-1", "Repeat.", false, nil)
	require.NoError(t, err)
	h.agent.acceptChildAdmission(child, second)

	// When the tainted echo arrives, the second admission must retire UNPROVED rather than claiming it.
	deliverCopilotChildReplyEvents(t, h.agent, []copilotChildReplyTestEvent{
		{agentID: "agent-1", kind: contracts.CopilotEventUserMessage, id: "tainted-user", data: map[string]any{
			"messageId": "m-tainted", "interactionId": "int-tainted", "turnId": "0",
			"delivery": "idle", "content": "Repeat.",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnStart, id: "tainted-start", data: map[string]any{
			"turnId": "0", "interactionId": "int-tainted",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantMessage, id: "tainted-reply", data: map[string]any{
			"turnId": "0", "interactionId": "int-tainted", "originatingMessageId": "m-tainted", "content": "Ambiguous.",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnEnd, id: "tainted-end", data: map[string]any{
			"turnId": "0",
		}},
		{kind: contracts.CopilotEventSystemNotification, id: "tainted-idle", data: map[string]any{
			"kind": map[string]any{"type": copilotAgentIdle, "agentId": "agent-1"},
		}},
	})
	assert.Empty(t, childSink.NativeReplyTurns())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, childSink.NativeReplyRetirements()[second.receipt])
}

func TestCopilotChildPermissionRoutesToChildSinkAndBindsActiveInputs(t *testing.T) {
	t.Parallel()
	h := newCopilotRuntimeHarness(t, map[string]string{
		"session.tasks.list":                                 copilotTaskListAnswer,
		"session.tasks.sendMessage":                          `{"sent":true}`,
		"session.permissions.handlePendingPermissionRequest": `{"success":true}`,
	}, nil)
	rootSink := newCopilotRecordedControlSink(h.sink)
	h.agent.sink = agent.NewProviderServices(rootSink)

	row, childSink := startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)
	childRecorded := rootSink.recordedChild(row.ChildAgentID)
	require.NotNil(t, childRecorded)

	// Start child iteration and deliver a steered client message.
	deliverCopilotChildReplyEvents(t, h.agent, []copilotChildReplyTestEvent{
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnStart, id: "iter-0-start", data: map[string]any{
			"turnId": "0", "interactionId": "int-perm",
		}},
	})
	_, err := h.agent.SteerChildInput("agent-1", agent.SteerRequest{
		InputID: "queue-perm", AttemptID: "attempt-perm", Content: "Run tool with permission.",
	})
	require.NoError(t, err)
	deliverCopilotChildReplyEvents(t, h.agent, []copilotChildReplyTestEvent{
		{agentID: "agent-1", kind: contracts.CopilotEventUserMessage, id: "perm-user", data: map[string]any{
			"messageId": "m-perm", "interactionId": "int-perm", "turnId": "0",
			"delivery": "steering", "content": "Run tool with permission.",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventToolStarted, id: "perm-tool-start", data: map[string]any{
			"turnId": "0", "toolCallId": "tc-child-1", "toolName": "bash", "arguments": map[string]any{"command": "echo ok"},
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventPermissionRequested, id: "perm-req-1", data: map[string]any{
			"requestId":         "native-perm-1",
			"permissionRequest": map[string]any{"kind": "shell", "toolCallId": "tc-child-1"},
		}},
		// An unrelated toolCallId must not bind to the active child steer inputs.
		{agentID: "agent-1", kind: contracts.CopilotEventPermissionRequested, id: "perm-req-unrelated", data: map[string]any{
			"requestId":         "native-perm-unrelated",
			"permissionRequest": map[string]any{"kind": "shell", "toolCallId": "tc-other-turn"},
		}},
	})

	// Root sink receives zero controls; child sink receives both controls and binds only the matching tool call.
	assert.Empty(t, rootSink.published)
	require.Len(t, childRecorded.published, 2)
	expectedKey := copilotControlID("session-1", "permission", "native-perm-1")
	assert.Equal(t, expectedKey, childRecorded.published[0].RequestID)
	assert.Equal(t, "session-1", childRecorded.published[0].AgentSessionID)

	resolutions := childSink.SteerResolutions()
	require.Len(t, resolutions, 1)
	require.Len(t, childRecorded.controlBindings, 1)
	assert.Equal(t, expectedKey, childRecorded.controlBindings[0].RequestID)
	assert.Equal(t, []string{resolutions[0].Receipt.InputID}, childRecorded.controlBindings[0].InputIDs)

	// Approving the child control sends the RPC on the root session with the native requestId.
	answer, err := json.Marshal(map[string]any{
		"response": map[string]any{
			"request_id": expectedKey,
			"response":   map[string]any{"kind": contracts.CopilotDecisionApproveOnce},
		},
	})
	require.NoError(t, err)
	require.NoError(t, h.agent.SendRawInput(agent.RawInput{
		Data: answer, ControlInstanceID: childRecorded.published[0].InstanceID,
	}))
	assert.Equal(t, map[string]any{
		"sessionId": "session-1",
		"requestId": "native-perm-1",
		"result":    map[string]any{"kind": contracts.CopilotDecisionApproveOnce},
	}, h.lastParams(t))

	// Completing the permission cancels the card on childRecorded, not rootSink.
	deliverCopilotChildReplyEvents(t, h.agent, []copilotChildReplyTestEvent{
		{agentID: "agent-1", kind: contracts.CopilotEventPermissionCompleted, id: "perm-done-1", data: map[string]any{
			"requestId": "native-perm-1", "toolCallId": "tc-child-1",
		}},
	})
	assert.Empty(t, rootSink.canceled)
	assert.Equal(t, []string{expectedKey + ":" + childRecorded.published[0].InstanceID}, childRecorded.canceled)

	// Cancelling the subagent withdraws any remaining child controls and retires the active turn as INTERRUPTED.
	deliverCopilotChildReplyEvents(t, h.agent, []copilotChildReplyTestEvent{
		{agentID: "agent-1", kind: contracts.CopilotEventSubagentCompleted, id: "sub-cancel", data: map[string]any{
			"toolCallId": "task-1", "cancelled": true,
		}},
	})
	unrelatedKey := copilotControlID("session-1", "permission", "native-perm-unrelated")
	assert.Equal(t, []string{
		expectedKey + ":" + childRecorded.published[0].InstanceID,
		unrelatedKey + ":" + childRecorded.published[1].InstanceID,
	}, childRecorded.canceled)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED, childSink.NativeReplyRetirements()[resolutions[0].Receipt])
}

func TestCopilotChildOverlappedIdenticalSteersSplittingIterationsRetireUnproved(t *testing.T) {
	t.Parallel()
	h := newCopilotTaskHarness(t, copilotTaskScript{})
	_, childSink := startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)

	// Two overlapping RPCs with identical prompt text: second starts before first is accepted.
	child, first, err := h.agent.beginChildAdmission("agent-1", "Same steer.", true, &copilotQueuedSteer{
		prompt:     "Same steer.",
		resolution: agent.SteerResolution{InputID: "queue-1", AttemptID: "attempt-1"},
	})
	require.NoError(t, err)
	_, second, err := h.agent.beginChildAdmission("agent-1", "Same steer.", true, &copilotQueuedSteer{
		prompt:     "Same steer.",
		resolution: agent.SteerResolution{InputID: "queue-2", AttemptID: "attempt-2"},
	})
	require.NoError(t, err)
	h.agent.acceptChildAdmission(child, first)
	h.agent.acceptChildAdmission(child, second)

	// Only one echo lands in iteration 0 before the model replies, and the second lands in iteration 1.
	deliverCopilotChildReplyEvents(t, h.agent, []copilotChildReplyTestEvent{
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnStart, id: "s0", data: map[string]any{
			"turnId": "0", "interactionId": "int-split",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventUserMessage, id: "u0", parent: "s0", data: map[string]any{
			"messageId": "m-0", "interactionId": "int-split", "turnId": "0", "delivery": "steering", "content": "Same steer.",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantMessage, id: "a0", parent: "u0", data: map[string]any{
			"turnId": "0", "interactionId": "int-split", "originatingMessageId": "m-orig", "content": "Reply 0",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnEnd, id: "e0", parent: "a0", data: map[string]any{"turnId": "0"}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnStart, id: "s1", parent: "e0", data: map[string]any{
			"turnId": "1", "interactionId": "int-split",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventUserMessage, id: "u1", parent: "s1", data: map[string]any{
			"messageId": "m-1", "interactionId": "int-split", "turnId": "1", "delivery": "steering", "content": "Same steer.",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantMessage, id: "a1", parent: "u1", data: map[string]any{
			"turnId": "1", "interactionId": "int-split", "originatingMessageId": "m-orig", "content": "Reply 1",
		}},
		{agentID: "agent-1", kind: contracts.CopilotEventAssistantTurnEnd, id: "e1", parent: "a1", data: map[string]any{"turnId": "1"}},
		{agentID: "agent-1", kind: contracts.CopilotEventSubagentCompleted, id: "done", parent: "e1", data: map[string]any{"toolCallId": "task-1"}},
	})

	assert.Empty(t, childSink.NativeReplyTurns(), "split overlapped identical steers must not claim either reply")
	resolutions := childSink.SteerResolutions()
	require.Len(t, resolutions, 2)
	assert.Equal(t, agent.SteerOutcomeEchoed, resolutions[0].Outcome)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, childSink.NativeReplyRetirements()[resolutions[0].Receipt])
	assert.Equal(t, agent.SteerOutcomeUncertain, resolutions[1].Outcome)
}
