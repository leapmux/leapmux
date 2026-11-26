package droid

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func deliverDroidReplyNotification(t *testing.T, a *Agent, payload map[string]any) {
	t.Helper()
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "type": droidTypeNotification, "method": droidMethodSessionNotif,
		"params": map[string]any{"sessionId": a.mainSessionID(), "notification": payload},
	})
	require.NoError(t, err)
	a.HandleOutput(frame)
}

func deliverDroidReplyMessage(t *testing.T, a *Agent, requestID, id, parentID, role string, blocks ...map[string]any) {
	t.Helper()
	deliverDroidReplyNotification(t, a, map[string]any{
		"type": contracts.DroidNotificationCreateMessage, "requestId": requestID,
		"message": map[string]any{"id": id, "parentId": parentID, "role": role, "content": blocks},
	})
}

func deliverDroidReplyEnd(t *testing.T, a *Agent, turnID string) {
	t.Helper()
	deliverDroidReplyNotification(t, a, map[string]any{
		"type": contracts.DroidNotificationAgentTurnCompleted, "turnId": turnID,
		"reason": contracts.DroidTurnEndReasonCompleted,
	})
}

func TestDroidReplyFollowsToolResultsBeforeTheInputResponse(t *testing.T) {
	t.Parallel()
	for _, refusedResponse := range []bool{false, true} {
		name := "successful response"
		if refusedResponse {
			name = "error after committed echo"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := newSteerAgent(t)
			stdin.respond = func(request droidEnvelope) {
				if request.Method != droidMethodAddUserMessage {
					return
				}
				var input addUserMessageParams
				require.NoError(t, json.Unmarshal(request.Params, &input))
				deliverDroidReplyMessage(t, a, request.ID, input.MessageID, "root", "user",
					map[string]any{"type": "text", "text": input.Text})
				deliverDroidReplyMessage(t, a, "", "first", input.MessageID, "assistant",
					map[string]any{"type": "text", "text": "The first part."},
					map[string]any{"type": "tool_use", "id": "call"})
				deliverDroidReplyNotification(t, a, map[string]any{
					"type": contracts.DroidToolNotificationToolResult, "messageId": "result", "toolUseId": "call",
				})
				deliverDroidReplyMessage(t, a, "", "second", "result", "assistant",
					map[string]any{"type": "text", "text": "The second part."})
				deliverDroidReplyEnd(t, a, input.MessageID)
				answerDroidInput(t, a, request.ID, refusedResponse)
			}

			receipt, err := a.SendInput("Check the reply.", nil)
			require.NoError(t, err)
			require.NotEmpty(t, receipt.InputID)
			assert.Equal(t, receipt.InputID, receipt.TurnID)
			assert.Equal(t, "main-session", receipt.SessionID)
			firstKey := droidMessageWriteKey("first", 0)
			secondKey := droidMessageWriteKey("second", 0)
			assert.Equal(t, []agent.NativeReplyTurn{{
				SessionID: "main-session", TurnID: receipt.TurnID,
				Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
				Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
				Items: []agent.NativeReplyItem{
					{Ordinal: 2, MessageKey: firstKey},
					{Ordinal: 3, MessageKey: secondKey},
				},
			}}, sink.NativeReplyTurns())
			textByKey := make(map[string]string)
			for _, row := range sink.Messages() {
				if row.IdempotencyKey == firstKey || row.IdempotencyKey == secondKey {
					text, ok := agent.ReplyText(droidProvider{}, agent.MessageContent{Original: row.Content})
					require.True(t, ok)
					textByKey[row.IdempotencyKey] = text
				}
			}
			assert.Equal(t, map[string]string{firstKey: "The first part.", secondKey: "The second part."}, textByKey)
			assert.Empty(t, a.nativeReplies)
		})
	}
}

func TestDroidReplyLeavesANewerTurnActiveWhenAnOlderTurnEnds(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	first, err := a.SendInput("Begin the first input.", nil)
	require.NoError(t, err)
	deliverDroidReplyMessage(t, a, "", first.InputID, "root", "user")
	deliverDroidReplyMessage(t, a, "", "first-reply", first.InputID, "assistant",
		map[string]any{"type": "text", "text": "The first result."},
		map[string]any{"type": "tool_use", "id": "first-tool"})
	deliverDroidReplyNotification(t, a, map[string]any{
		"type": contracts.DroidNotificationWorkingStateChanged, "newState": contracts.DroidWorkingStateIdle,
	})

	second, err := a.SendInput("Begin the second input.", nil)
	require.NoError(t, err)
	deliverDroidReplyMessage(t, a, "", second.InputID, "first-reply", "user")
	deliverDroidReplyMessage(t, a, "", "second-reply", second.InputID, "assistant",
		map[string]any{"type": "tool_use", "id": "second-tool"})
	assert.Equal(t, second.TurnID, a.droidReplyTurnForTools([]string{"second-tool"}))
	assert.Empty(t, a.droidReplyTurnForTools([]string{"first-tool", "second-tool"}))

	deliverDroidReplyEnd(t, a, first.TurnID)
	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, first.TurnID, turns[0].TurnID)
	assert.True(t, a.PublishTurnActive().Active)
	assert.Contains(t, a.nativeReplies, second.TurnID)
}

func beginDroidTestReply(t *testing.T, a *Agent, requests <-chan droidEnvelope) agent.NativeInputReceipt {
	t.Helper()
	leadDone := make(chan agent.NativeInputReceipt, 1)
	go func() {
		receipt, err := a.SendInput("Begin the native turn.", nil)
		assert.NoError(t, err)
		leadDone <- receipt
	}()
	leadRequest := receiveDroidInput(t, requests)
	answerDroidInput(t, a, leadRequest.ID, false)
	lead := <-leadDone
	require.NotEmpty(t, lead.InputID)
	deliverDroidReplyMessage(t, a, leadRequest.ID, lead.InputID, "root", "user")
	return lead
}

func TestDroidReplyStartsTheSteerAfterCommittedText(t *testing.T) {
	t.Parallel()
	a, sink, requests, _ := newDroidInputHarness(t)
	lead := beginDroidTestReply(t, a, requests)
	deliverDroidReplyMessage(t, a, "", "first", lead.InputID, "assistant",
		map[string]any{"type": "text", "text": "The lead reply."},
		map[string]any{"type": "tool_use", "id": "call"})
	deliverDroidReplyNotification(t, a, map[string]any{
		"type": contracts.DroidToolNotificationToolResult, "messageId": "tool-result", "toolUseId": "call",
	})

	steerDone := make(chan error, 1)
	go func() { steerDone <- agenttest.SteerInput(a, testDroidSteer(1, "Check a different result.")) }()
	steerRequest := receiveDroidInput(t, requests)
	steer := testDroidSteerEcho(t, steerRequest, 1)
	steer.Receipt.TurnID = lead.TurnID
	deliverDroidReplyMessage(t, a, steerRequest.ID, steer.Receipt.InputID, "tool-result", "user")
	answerDroidInput(t, a, steerRequest.ID, true)
	require.NoError(t, <-steerDone, "the committed echo proves delivery before the RPC answer")
	assert.Equal(t, []agent.SteerResolution{steer}, sink.SteerResolutions())

	deliverDroidReplyMessage(t, a, "", "second", steer.Receipt.InputID, "assistant",
		map[string]any{"type": "text", "text": "The steered reply."},
		map[string]any{"type": "tool_use", "id": "steer-call"})
	assert.Equal(t, lead.TurnID, a.droidReplyTurnForTools([]string{"steer-call"}))
	deliverDroidReplyEnd(t, a, lead.TurnID)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: lead.SessionID, TurnID: lead.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{
			{InputID: lead.InputID, AfterOrdinal: 1},
			{InputID: steer.Receipt.InputID, AfterOrdinal: 2},
		},
		Items: []agent.NativeReplyItem{
			{Ordinal: 2, MessageKey: droidMessageWriteKey("first", 0)},
			{Ordinal: 3, MessageKey: droidMessageWriteKey("second", 0)},
		},
	}}, sink.NativeReplyTurns())
}

func TestDroidSteerWithUnknownParentLeavesTheReplyUnproved(t *testing.T) {
	t.Parallel()
	a, sink, requests, _ := newDroidInputHarness(t)
	lead := beginDroidTestReply(t, a, requests)
	steerDone := make(chan error, 1)
	go func() { steerDone <- agenttest.SteerInput(a, testDroidSteer(1, "Check the revised result.")) }()
	request := receiveDroidInput(t, requests)
	steer := testDroidSteerEcho(t, request, 1)
	steer.Receipt.TurnID = ""
	deliverDroidReplyMessage(t, a, request.ID, steer.Receipt.InputID, "unknown-parent", "user")
	answerDroidInput(t, a, request.ID, false)
	require.NoError(t, <-steerDone)
	assert.Equal(t, []agent.SteerResolution{steer}, sink.SteerResolutions())

	deliverDroidReplyMessage(t, a, "", "reply", steer.Receipt.InputID, "assistant",
		map[string]any{"type": "text", "text": "The revised result."})
	deliverDroidReplyEnd(t, a, lead.TurnID)
	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		lead:          leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		steer.Receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
}

func TestDroidReplyRejectsAnAssistantMessageWithUnknownAncestry(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	receipt, err := a.SendInput("Begin the input.", nil)
	require.NoError(t, err)
	deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")
	deliverDroidReplyMessage(t, a, "", "first-reply", receipt.InputID, "assistant",
		map[string]any{"type": "text", "text": "A partial result."})
	deliverDroidReplyMessage(t, a, "", "unknown-reply", "unknown-parent", "assistant",
		map[string]any{"type": "text", "text": "Another result."})
	deliverDroidReplyEnd(t, a, receipt.TurnID)

	assert.Empty(t, sink.NativeReplyTurns())
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED, sink.NativeReplyRetirements()[receipt])
}

func TestDroidReplyCompletesWhenTheNativeTurnHasNoVisibleReply(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	receipt, err := a.SendInput("Begin the input.", nil)
	require.NoError(t, err)
	deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")
	deliverDroidReplyEnd(t, a, receipt.TurnID)

	turns := sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, receipt.TurnID, turns[0].TurnID)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turns[0].Outcome)
	assert.Empty(t, turns[0].Items)
}

func TestDroidReplyKeepsAProvisionalTurnThroughAnOlderIdle(t *testing.T) {
	t.Parallel()
	a, _, _ := newSteerAgent(t)
	receipt, err := a.SendInput("Begin the input.", nil)
	require.NoError(t, err)

	deliverDroidReplyNotification(t, a, map[string]any{
		"type": contracts.DroidNotificationWorkingStateChanged, "newState": contracts.DroidWorkingStateIdle,
	})
	require.True(t, a.PublishTurnActive().Active)
	_, err = a.SendInput("Another input.", nil)
	require.ErrorIs(t, err, agent.ErrAgentBusy)

	deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")
	deliverDroidReplyEnd(t, a, receipt.TurnID)
	assert.False(t, a.PublishTurnActive().Active)
}

func TestDroidSendInputForSessionReturnsReceiptAndTracksReply(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	receipt, err := a.SendInputForSession("main-session", "Check the session reply.", nil)
	require.NoError(t, err)
	require.NotEmpty(t, receipt.InputID)
	assert.Equal(t, "main-session", receipt.SessionID)
	assert.Equal(t, receipt.InputID, receipt.TurnID)

	deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")
	deliverDroidReplyMessage(t, a, "", "session-reply", receipt.InputID, "assistant",
		map[string]any{"type": "text", "text": "Session reply."})
	deliverDroidReplyEnd(t, a, receipt.TurnID)

	require.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "main-session", TurnID: receipt.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: droidMessageWriteKey("session-reply", 0)}},
	}}, sink.NativeReplyTurns())
}

func TestDroidFinishOutputResolvesUnreadPendingSteersAsUncertain(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	receipt, err := a.SendInput("Start turn.", nil)
	require.NoError(t, err)
	require.NoError(t, agenttest.SteerInput(a, testDroidSteer(1, "Steer before exit.")))
	a.finishOutput(agent.MessageCompletionError)
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "input-1", AttemptID: "attempt-1", Outcome: agent.SteerOutcomeUncertain,
	}}, sink.SteerResolutions())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, sink.NativeReplyRetirements())
	a.dispatchMu.Lock()
	assert.Empty(t, a.pendingSteers)
	assert.Empty(t, a.nativeReplies)
	a.dispatchMu.Unlock()

	// A second finishOutput must not emit duplicate steer resolutions or retirements.
	a.finishOutput(agent.MessageCompletionError)
	assert.Len(t, sink.SteerResolutions(), 1)
	assert.Len(t, sink.NativeReplyRetirements(), 1)
}

func TestDroidOnErrorRetiresActiveReplyAsFailed(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	receipt, err := a.SendInput("Start turn.", nil)
	require.NoError(t, err)
	deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")
	deliverDroidReplyNotification(t, a, map[string]any{
		"type": contracts.DroidNotificationError, "message": "boom",
	})
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED, sink.NativeReplyRetirements()[receipt])
	assert.Empty(t, a.nativeReplies)
}

func TestDroidReplySkipsWhitespaceOnlyTextBlocks(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	receipt, err := a.SendInput("Check whitespace.", nil)
	require.NoError(t, err)
	deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")
	deliverDroidReplyMessage(t, a, "", "msg-ws", receipt.InputID, "assistant",
		map[string]any{"type": "text", "text": " \t\n\uFEFF"},
		map[string]any{"type": "text", "text": "Visible answer."})
	deliverDroidReplyEnd(t, a, receipt.TurnID)
	require.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "main-session", TurnID: receipt.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: droidMessageWriteKey("msg-ws", 1)}},
	}}, sink.NativeReplyTurns())
}

func TestDroidUnidentifiedSteerDoesNotRegisterEmptyPendingSteer(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	_, err := a.SendInput("Start turn.", nil)
	require.NoError(t, err)
	require.NoError(t, agenttest.SteerInput(a, agent.SteerRequest{Content: "Unidentified steer."}))
	a.dispatchMu.Lock()
	pendingCount := len(a.pendingSteers)
	a.dispatchMu.Unlock()
	assert.Zero(t, pendingCount)
	a.finishOutput(agent.MessageCompletionError)
	assert.Empty(t, sink.SteerResolutions())
}

func TestDroidReplyTurnForTools(t *testing.T) {
	t.Parallel()
	a, _, _ := newSteerAgent(t)
	receipt, err := a.SendInput("Run tools.", nil)
	require.NoError(t, err)
	deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")
	deliverDroidReplyMessage(t, a, "", "assistant-tools", receipt.InputID, "assistant",
		map[string]any{"type": "tool_use", "id": "tool-a"},
		map[string]any{"type": "tool_use", "id": "tool-b"})

	assert.Empty(t, a.droidReplyTurnForTools(nil))
	assert.Empty(t, a.droidReplyTurnForTools([]string{}))
	assert.Empty(t, a.droidReplyTurnForTools([]string{"unknown-tool"}))
	assert.Equal(t, receipt.TurnID, a.droidReplyTurnForTools([]string{"tool-a", "tool-b"}))
	assert.Empty(t, a.droidReplyTurnForTools([]string{"tool-a", "unknown-tool"}))
}

type droidRefuseReserveSink struct {
	agent.ProviderServices
	mu          sync.Mutex
	refuseCount int
}

func (s *droidRefuseReserveSink) ReserveTranscriptOutput(queue *agent.OutputQueue, writes ...agent.OutputWrite) ([]*agent.OutputTicket, error) {
	s.mu.Lock()
	if s.refuseCount > 0 {
		s.refuseCount--
		s.mu.Unlock()
		return nil, errors.New("reservation refused")
	}
	s.mu.Unlock()
	return s.ProviderServices.ReserveTranscriptOutput(queue, writes...)
}

func TestDroidCommitAssistantMessageReplyTracking(t *testing.T) {
	t.Parallel()

	t.Run("whitespace-only message completes with empty reply items", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSteerAgent(t)
		receipt, err := a.SendInput("Whitespace only.", nil)
		require.NoError(t, err)
		deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")
		deliverDroidReplyMessage(t, a, "", "msg-ws-only", receipt.InputID, "assistant",
			map[string]any{"type": "text", "text": " \t\n"})
		deliverDroidReplyEnd(t, a, receipt.TurnID)

		require.Equal(t, []agent.NativeReplyTurn{{
			SessionID: "main-session", TurnID: receipt.TurnID,
			Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
		}}, sink.NativeReplyTurns())
	})

	t.Run("thinking and text excludes reasoning from reply items", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSteerAgent(t)
		receipt, err := a.SendInput("Think then answer.", nil)
		require.NoError(t, err)
		deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")
		deliverDroidReplyMessage(t, a, "", "msg-think-text", receipt.InputID, "assistant",
			map[string]any{"type": "thinking", "thinking": "private reasoning"},
			map[string]any{"type": "text", "text": "Visible answer."})
		deliverDroidReplyEnd(t, a, receipt.TurnID)

		require.Equal(t, []agent.NativeReplyTurn{{
			SessionID: "main-session", TurnID: receipt.TurnID,
			Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
			Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
			Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: droidMessageWriteKey("msg-think-text", 1)}},
		}}, sink.NativeReplyTurns())
		var sawReasoning bool
		for _, row := range sink.Messages() {
			if kind, text, _, ok := agenttest.DecodeAssembledMessage(row.Content); ok && kind == agent.AssembledMessageKindReasoning {
				assert.Equal(t, "private reasoning", text)
				sawReasoning = true
			}
		}
		assert.True(t, sawReasoning, "reasoning block must still persist to the transcript")
	})

	t.Run("unretained PersistMessage error marks reply unproved", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSteerAgent(t)
		refuse := &droidRefuseReserveSink{ProviderServices: a.sink}
		a.sink = refuse

		receipt, err := a.SendInput("Refuse text persist.", nil)
		require.NoError(t, err)
		deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")

		// Fail the next reservation (the text block write inside commitAssistantMessage).
		refuse.mu.Lock()
		refuse.refuseCount = 1
		refuse.mu.Unlock()
		deliverDroidReplyMessage(t, a, "", "msg-unretained", receipt.InputID, "assistant",
			map[string]any{"type": "text", "text": "Lost text."})
		deliverDroidReplyEnd(t, a, receipt.TurnID)

		assert.Empty(t, sink.NativeReplyTurns())
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}, sink.NativeReplyRetirements())
	})

	t.Run("malformed content on tracked reply retires unproved", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSteerAgent(t)
		receipt, err := a.SendInput("Malformed reply.", nil)
		require.NoError(t, err)
		deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")
		deliverDroidReplyNotification(t, a, map[string]any{
			"type": contracts.DroidNotificationCreateMessage,
			"message": map[string]any{
				"id": "msg-bad", "parentId": receipt.InputID, "role": "assistant",
				"content": []map[string]any{{"type": "text", "text": 123}},
			},
		})
		deliverDroidReplyEnd(t, a, receipt.TurnID)

		assert.Empty(t, sink.NativeReplyTurns())
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
		}, sink.NativeReplyRetirements())
	})
}

func TestDroidOnErrorReplyAndCallbackBehavior(t *testing.T) {
	t.Parallel()

	t.Run("invokes bound onTurnEnd with StatusFailed when active", func(t *testing.T) {
		t.Parallel()
		a, _, _ := newSteerAgent(t)
		var got []bgtask.Status
		a.onTurnEnd = func(status bgtask.Status) { got = append(got, status) }
		_, err := a.SendInput("Run failing turn.", nil)
		require.NoError(t, err)

		deliverDroidReplyNotification(t, a, map[string]any{
			"type": contracts.DroidNotificationError, "message": "provider failed",
		})
		assert.Equal(t, []bgtask.Status{bgtask.StatusFailed}, got)
		assert.False(t, a.PublishTurnActive().Active)
	})

	t.Run("retires lead and admitted steer as failed", func(t *testing.T) {
		t.Parallel()
		a, sink, requests, _ := newDroidInputHarness(t)
		lead := beginDroidTestReply(t, a, requests)

		steerDone := make(chan error, 1)
		go func() { steerDone <- agenttest.SteerInput(a, testDroidSteer(1, "Steer before error.")) }()
		steerReq := receiveDroidInput(t, requests)
		steer := testDroidSteerEcho(t, steerReq, 1)
		steer.Receipt.TurnID = lead.TurnID
		deliverDroidReplyMessage(t, a, steerReq.ID, steer.Receipt.InputID, lead.InputID, "user")
		answerDroidInput(t, a, steerReq.ID, false)
		require.NoError(t, <-steerDone)

		deliverDroidReplyNotification(t, a, map[string]any{
			"type": contracts.DroidNotificationError, "message": "mid-turn error",
		})
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			lead:          leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
			steer.Receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
		}, sink.NativeReplyRetirements())
		assert.Empty(t, a.nativeReplies)
	})

	t.Run("idle agent does not invoke onTurnEnd", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSteerAgent(t)
		called := false
		a.onTurnEnd = func(bgtask.Status) { called = true }

		deliverDroidReplyNotification(t, a, map[string]any{
			"type": contracts.DroidNotificationError, "message": "idle error",
		})
		assert.False(t, called)
		require.Len(t, sink.PersistedNotifications(), 1)
	})

	t.Run("pre-echo provisional turn retires as failed", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSteerAgent(t)
		receipt, err := a.SendInput("Provisional turn.", nil)
		require.NoError(t, err)
		// Intentionally skip lead user echo so currentReply.echoed is false.
		deliverDroidReplyNotification(t, a, map[string]any{
			"type": contracts.DroidNotificationError, "message": "pre-echo error",
		})
		assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
			receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED,
		}, sink.NativeReplyRetirements())
		assert.Empty(t, a.nativeReplies)
		assert.Nil(t, a.currentReply)
		assert.False(t, a.PublishTurnActive().Active)
	})
}

type droidOrderedRetireSink struct {
	agent.ProviderServices
	mu       sync.Mutex
	retireID []string
}

func (s *droidOrderedRetireSink) RetireNativeInputTurn(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) error {
	s.mu.Lock()
	s.retireID = append(s.retireID, receipt.TurnID)
	s.mu.Unlock()
	return s.ProviderServices.RetireNativeInputTurn(receipt, outcome)
}

func TestDroidFinishOutputDeterministicOrderAndDiscarding(t *testing.T) {
	t.Parallel()

	t.Run("retires multiple nativeReplies and pendingSteers in sorted order", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSteerAgent(t)
		recorder := &droidOrderedRetireSink{ProviderServices: a.sink}
		a.sink = recorder

		a.dispatchMu.Lock()
		for _, turnID := range []string{"turn-z", "turn-a", "turn-m"} {
			a.registerDroidReply(agent.NativeInputReceipt{SessionID: "main-session", InputID: turnID, TurnID: turnID}, "req-"+turnID)
		}
		a.pendingSteers = map[string]droidSteerAttempt{
			"msg-z": {queueInputID: "in-z", attemptID: "att-z", requestID: "r-z", sessionID: "main-session"},
			"msg-a": {queueInputID: "in-a", attemptID: "att-a", requestID: "r-a", sessionID: "main-session"},
			"msg-m": {queueInputID: "in-m", attemptID: "att-m", requestID: "r-m", sessionID: "main-session"},
		}
		a.dispatchMu.Unlock()

		a.finishOutput(agent.MessageCompletionError)

		recorder.mu.Lock()
		gotRetireOrder := append([]string(nil), recorder.retireID...)
		recorder.mu.Unlock()
		assert.Equal(t, []string{"turn-a", "turn-m", "turn-z"}, gotRetireOrder)
		assert.Equal(t, []agent.SteerResolution{
			{InputID: "in-a", AttemptID: "att-a", Outcome: agent.SteerOutcomeUncertain},
			{InputID: "in-m", AttemptID: "att-m", Outcome: agent.SteerOutcomeUncertain},
			{InputID: "in-z", AttemptID: "att-z", Outcome: agent.SteerOutcomeUncertain},
		}, sink.SteerResolutions())
	})

	t.Run("discarding output skips generation and closing tool persistence but resets spans", func(t *testing.T) {
		t.Parallel()
		a, sink, _ := newSteerAgent(t)
		a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"assistant_text_delta","messageId":"disc","blockIndex":0,"textDelta":"discard me"}`)))
		a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"tool_call","toolUse":{"id":"disc-tool","name":"Execute","input":{"command":"sleep 1"}}}`)))
		a.HandleOutput(droidAuditNotification(t, "main-session", []byte(`{"type":"tool_progress_update","toolUseId":"disc-tool","update":{"text":"running"}}`)))

		a.DiscardOutput()
		require.True(t, a.IsDiscardingOutput())
		a.finishOutput(agent.MessageCompletionInterrupted)

		assert.Empty(t, sink.Messages(), "discarding output must neither persist remaining generation nor write tool rows")
		assert.Contains(t, sink.ClosedSpans(), "droid-tool-disc-tool")
		assert.Equal(t, 1, sink.ResetSpanCount())
		assert.Equal(t, agent.ProgressSnapshot{}, sink.ProgressSnapshot())
	})
}
