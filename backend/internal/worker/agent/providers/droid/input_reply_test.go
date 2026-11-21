package droid

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
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

			receipt, err := a.SendInputWithReceipt("Check the reply.", nil)
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
	first, err := a.SendInputWithReceipt("Begin the first input.", nil)
	require.NoError(t, err)
	deliverDroidReplyMessage(t, a, "", first.InputID, "root", "user")
	deliverDroidReplyMessage(t, a, "", "first-reply", first.InputID, "assistant",
		map[string]any{"type": "text", "text": "The first result."},
		map[string]any{"type": "tool_use", "id": "first-tool"})
	deliverDroidReplyNotification(t, a, map[string]any{
		"type": contracts.DroidNotificationWorkingStateChanged, "newState": contracts.DroidWorkingStateIdle,
	})

	second, err := a.SendInputWithReceipt("Begin the second input.", nil)
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

func TestDroidReplyRejectsAnAssistantMessageWithUnknownAncestry(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	receipt, err := a.SendInputWithReceipt("Begin the input.", nil)
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
	receipt, err := a.SendInputWithReceipt("Begin the input.", nil)
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
	receipt, err := a.SendInputWithReceipt("Begin the input.", nil)
	require.NoError(t, err)

	deliverDroidReplyNotification(t, a, map[string]any{
		"type": contracts.DroidNotificationWorkingStateChanged, "newState": contracts.DroidWorkingStateIdle,
	})
	require.True(t, a.PublishTurnActive().Active)
	_, err = a.SendInputWithReceipt("Another input.", nil)
	require.ErrorIs(t, err, agent.ErrAgentBusy)

	deliverDroidReplyMessage(t, a, "", receipt.InputID, "root", "user")
	deliverDroidReplyEnd(t, a, receipt.TurnID)
	assert.False(t, a.PublishTurnActive().Active)
}
