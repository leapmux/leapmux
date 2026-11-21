package ohmypi

import (
	"encoding/json"
	"fmt"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOMPReplyStopsCollectingAtItsNativeYield(t *testing.T) {
	r := newRig(t)
	receipt, err := r.agent.SendInputWithReceipt("A reader prompt.", nil)
	require.NoError(t, err)
	commands := r.commandsOfType(CommandPrompt)
	require.Len(t, commands, 1)
	assert.Equal(t, receipt.InputID, commands[0].ID)
	assert.NotContains(t, commands[0].Payload, "streamingBehavior")
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Retry context."}]}}`,
		`{"type":"agent_end","isTerminal":false,"yielded":false}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Owned final reply."}]}}`,
		`{"type":"agent_end","isTerminal":false,"yielded":true}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Background job result."}]}}`,
		ompResultFrame("another-command", "completed"),
	)
	assert.Empty(t, r.sink.NativeReplyTurns())
	r.emit(ompResultFrame(receipt.InputID, "completed"))
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: receipt.SessionID, TurnID: receipt.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
		Items: []agent.NativeReplyItem{
			{Ordinal: 2, MessageKey: "omp-reply:" + receipt.TurnID + ":2"},
			{Ordinal: 3, MessageKey: "omp-reply:" + receipt.TurnID + ":3"},
		},
	}}, r.sink.NativeReplyTurns())
	assert.True(t, r.agent.PublishTurnActive().Active, "the native background run remains active")
}

func TestOMPExactInputStaysInWorkerQueueAfterNativeBusyRefusal(t *testing.T) {
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{
				Error:  "Agent is already processing. Use steer() or followUp() to queue messages, or wait for completion.",
				Before: []string{`{"type":"agent_start"}`},
			}
		}
		return nil
	})
	receipt, err := r.agent.SendInputWithReceipt("A reader prompt.", nil)
	require.ErrorIs(t, err, agent.ErrAgentBusy)
	assert.Empty(t, receipt)
	assert.True(t, r.agent.PublishTurnActive().Active)
	commands := r.commandsOfType(CommandPrompt)
	require.Len(t, commands, 1)
	assert.NotContains(t, commands[0].Payload, "streamingBehavior")
	r.emit(`{"type":"agent_end","isTerminal":true}`, ompResultFrame(commands[0].ID, "completed"))
	assert.Empty(t, r.sink.NativeReplyTurns(), "a refused prompt owns no reply")
}

func TestOMPReplyRequiresEvidenceOfSuccess(t *testing.T) {
	for _, tc := range []struct {
		status  string
		outcome leapmuxv1.AgentInputTurnOutcome
	}{
		{"completed", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED},
		{"aborted", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED},
		{"error", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED},
		{"future", leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED},
	} {
		t.Run(tc.status, func(t *testing.T) {
			r := newRig(t)
			receipt, err := r.agent.SendInputWithReceipt("A reader prompt.", nil)
			require.NoError(t, err)
			r.emit(ompResultFrame(receipt.InputID, tc.status))
			assert.Empty(t, r.sink.NativeReplyTurns())
			assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{receipt: tc.outcome}, r.sink.NativeReplyRetirements())
		})
	}
}

func TestOMPReplyFinishesAtNativePauseWhileSessionIsActive(t *testing.T) {
	r := newRig(t)
	receipt, err := r.agent.SendInputWithReceipt("A reader prompt.", nil)
	require.NoError(t, err)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Ready for background work."}]}}`,
		`{"type":"agent_pause","yielded":true}`,
		ompResultFrame(receipt.InputID, "completed"),
	)
	turns := r.sink.NativeReplyTurns()
	require.Len(t, turns, 1)
	assert.Equal(t, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED, turns[0].Outcome)
	assert.Equal(t, []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "omp-reply:" + receipt.TurnID + ":2"}}, turns[0].Items)
	assert.True(t, r.agent.PublishTurnActive().Active)
}

func TestOMPLocalReplyCanCompleteAtItsAcknowledgement(t *testing.T) {
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Data: json.RawMessage(`{"agentInvoked":false}`)}
		}
		return nil
	})
	receipt, err := r.agent.SendInputWithReceipt("Text handled by an input hook.", nil)
	require.NoError(t, err)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: receipt.SessionID, TurnID: receipt.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: receipt.InputID, AfterOrdinal: 1}},
	}}, r.sink.NativeReplyTurns())
	assert.False(t, r.agent.PublishTurnActive().Active)
}

func TestOMPLateAcknowledgementCannotClaimAnotherRun(t *testing.T) {
	r := newRig(t)
	r.respond(func(command recordedCommand) *rigReply {
		if command.Type == CommandPrompt {
			return &rigReply{Before: []string{`{"type":"agent_start"}`}}
		}
		return nil
	})
	receipt, err := r.agent.SendInputWithReceipt("A reader prompt.", nil)
	require.NoError(t, err)
	r.emit(
		`{"type":"agent_end","isTerminal":true}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Unrelated reply."}]}}`,
		`{"type":"agent_end","isTerminal":true}`,
		ompResultFrame(receipt.InputID, "completed"),
	)
	assert.Empty(t, r.sink.NativeReplyTurns())
	assert.Equal(t, map[agent.NativeInputReceipt]leapmuxv1.AgentInputTurnOutcome{
		receipt: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED,
	}, r.sink.NativeReplyRetirements())
}

func ompResultFrame(id, status string) string {
	return fmt.Sprintf(`{"type":"prompt_result","id":%q,"status":%q,"agentInvoked":true,"sessionSettled":false}`, id, status)
}
