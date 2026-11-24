package ohmypi

import (
	"fmt"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOMPIdleChildReplyBelongsToTheNativeSession(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
	r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`,
		frameTaskStart, frameStarted, strings.ReplaceAll(frameStarted, "Probe", "Sibling"))
	child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
	sibling := r.sink.Child(subagentRow(t, r, "Sibling").ChildAgentID)
	key := subagentRowKey(r.agent.sessionID, "Probe")

	result, err := r.agent.SendChildInput(key, agent.ChildInput{InputID: "first", Content: "Next check."})
	require.NoError(t, err)
	require.Equal(t, "/s/Probe.jsonl", result.NativeReceipt.SessionID)
	require.NotEmpty(t, result.NativeReceipt.InputID)
	commands := r.commandsOfType(CommandSteerSubagent)
	require.Len(t, commands, 1)
	assert.Equal(t, "child-prefix:"+result.NativeReceipt.InputID+"\nNext check.", commands[0].Payload["message"])

	echo := fmt.Sprintf(`{"type":"message_end","message":{"role":"user","leapmuxInputId":%q}}`, result.NativeReceipt.InputID)
	reply := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"The check passed."}]}}`
	turnEnd := `{"type":"turn_end","message":{"role":"assistant","stopReason":"stop"}}`
	yield := `{"type":"agent_pause","yielded":true,"messages":[{"role":"assistant","stopReason":"stop"}]}`
	r.emit(subagentEvent("Sibling", echo), subagentEvent("Sibling", reply), subagentEvent("Sibling", turnEnd), subagentEvent("Sibling", yield))
	assert.Empty(t, sibling.NativeReplyTurns(), "an echo on a sibling route cannot claim the receipt")
	r.emit(subagentEvent("Probe", `{"type":"agent_start"}`), subagentEvent("Probe", echo),
		subagentEvent("Probe", reply), subagentEvent("Probe", turnEnd), subagentEvent("Probe", yield),
		subagentEvent("Probe", `{"type":"agent_start"}`),
		subagentEvent("Probe", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Later autonomous output."}]}}`),
		frameCompleted)

	inputID := result.NativeReceipt.InputID
	assert.Equal(t, []agent.NativeReplyTurn{{
		SessionID: "/s/Probe.jsonl", TurnID: inputID,
		Outcome: leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED,
		Inputs:  []agent.NativeReplyBoundary{{InputID: inputID, AfterOrdinal: 1}},
		Items:   []agent.NativeReplyItem{{Ordinal: 2, MessageKey: "omp-reply:" + inputID + ":2"}},
	}}, child.NativeReplyTurns())
	assert.Empty(t, r.sink.NativeReplyTurns())
}

func TestOMPChildCloseWithoutEchoLeavesSteerUncertain(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.agent.steerBridge = ompSteerBridge{Capability: "child-proof", ChildPrefix: "child-prefix:"}
	r.emit(`{"type":"available_commands_update","commands":[{"name":"child-proof","source":"extension"}]}`,
		frameTaskStart, frameStarted, subagentEvent("Probe", `{"type":"agent_start"}`))
	child := r.sink.Child(subagentRow(t, r, "Probe").ChildAgentID)
	require.NoError(t, r.agent.SteerChildInputAtEcho(subagentRowKey(r.agent.sessionID, "Probe"), agent.SteerRequest{
		InputID: "steer", AttemptID: "steer-attempt", Content: "Next check.",
	}))
	assert.Empty(t, child.SteerResolutions(), "the command acknowledgement proves no delivery")
	r.emit(frameCompleted)
	assert.Equal(t, []agent.SteerResolution{{
		InputID: "steer", AttemptID: "steer-attempt", Outcome: agent.SteerOutcomeUncertain,
	}}, child.SteerResolutions())
	assert.Empty(t, child.NativeReplyTurns())
	assert.Empty(t, r.sink.SteerResolutions())
}
