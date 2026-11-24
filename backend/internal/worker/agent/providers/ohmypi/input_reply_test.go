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

func TestOMPQueuedSteersShareReplyAcrossToolUse(t *testing.T) {
	r := newRig(t)
	r.agent.steerBridge = ompSteerBridge{Capability: "steer-ready", Prefix: "steer-prefix:"}
	r.emit(`{"type":"available_commands_update","commands":[{"name":"steer-ready","source":"extension"}]}`)
	lead, err := r.agent.SendInputWithReceipt("Check the main shipment.", nil)
	require.NoError(t, err)
	r.emit(
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Looking up the shipment."}]}}`,
		`{"type":"turn_end","message":{"role":"assistant","stopReason":"toolUse"}}`,
	)
	for _, id := range []string{"first", "second"} {
		require.NoError(t, r.agent.SteerInput(agent.SteerRequest{
			Content: "Check the next shipment.", InputID: id, AttemptID: id + "-attempt",
		}))
	}
	commands := r.commandsOfType(CommandPrompt)
	require.Len(t, commands, 3)
	for _, command := range commands[1:] {
		assert.Equal(t, "steer-prefix:"+command.ID+"\nCheck the next shipment.", command.Payload["message"])
		r.emit(fmt.Sprintf(`{"type":"message_end","message":{"role":"user","steering":true,"leapmuxInputId":%q}}`, command.ID))
	}
	assert.Empty(t, r.sink.NativeReplyTurns())
	assert.Equal(t, []agent.SteerResolution{
		{InputID: "first", AttemptID: "first-attempt", Outcome: agent.SteerOutcomeEchoed,
			Receipt: agent.NativeInputReceipt{SessionID: lead.SessionID, InputID: commands[1].ID, TurnID: lead.TurnID}},
		{InputID: "second", AttemptID: "second-attempt", Outcome: agent.SteerOutcomeEchoed,
			Receipt: agent.NativeInputReceipt{SessionID: lead.SessionID, InputID: commands[2].ID, TurnID: lead.TurnID}},
	}, r.sink.SteerResolutions())
	r.emit(
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"The shipment is ready."}]}}`,
		`{"type":"turn_end","message":{"role":"assistant","stopReason":"stop"}}`,
		`{"type":"agent_end","yielded":true}`,
		ompResultFrame(commands[1].ID, "completed"),
		ompResultFrame(commands[2].ID, "completed"),
		ompResultFrame(lead.InputID, "completed"),
	)
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: lead.SessionID, TurnID: lead.TurnID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs: []agent.NativeReplyBoundary{
			{InputID: lead.InputID, AfterOrdinal: 1},
			{InputID: commands[1].ID, AfterOrdinal: 3},
			{InputID: commands[2].ID, AfterOrdinal: 4},
		},
		Items: []agent.NativeReplyItem{
			{Ordinal: 2, MessageKey: "omp-reply:" + lead.TurnID + ":2"},
			{Ordinal: 5, MessageKey: "omp-reply:" + lead.TurnID + ":5"},
		},
	}}, r.sink.NativeReplyTurns())
}

func TestOMPSettledPromptWithoutUserEchoLeavesSteerUncertain(t *testing.T) {
	r := newRig(t)
	r.agent.steerBridge = ompSteerBridge{Capability: "steer-ready", Prefix: "steer-prefix:"}
	r.emit(
		`{"type":"available_commands_update","commands":[{"name":"steer-ready","source":"extension"}]}`,
		`{"type":"agent_start"}`,
	)
	require.NoError(t, r.agent.SteerInput(agent.SteerRequest{
		Content: "Check the next shipment.", InputID: "steer", AttemptID: "steer-attempt",
	}))
	commands := r.commandsOfType(CommandPrompt)
	require.Len(t, commands, 1)
	r.emit(
		fmt.Sprintf(`{"type":"prompt_result","id":%q,"status":"completed"}`, commands[0].ID),
		ompResultFrame(commands[0].ID, "completed"),
	)
	assert.Empty(t, r.sink.SteerResolutions(), "a result without settlement proves no delivery")
	r.emit(
		`{"type":"agent_end","yielded":true}`,
		fmt.Sprintf(`{"type":"prompt_result","id":%q,"status":"completed","agentInvoked":true,"sessionSettled":true}`, commands[0].ID),
	)
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "steer", AttemptID: "steer-attempt", Outcome: agent.SteerOutcomeUncertain,
	}}, r.sink.SteerResolutions())
	assert.Empty(t, r.sink.NativeReplyTurns())
}

func ompResultFrame(id, status string) string {
	return fmt.Sprintf(`{"type":"prompt_result","id":%q,"status":%q,"agentInvoked":true,"sessionSettled":false}`, id, status)
}
