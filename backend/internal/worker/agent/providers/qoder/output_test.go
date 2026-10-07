package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// The frames below are the shapes that qodercli 1.1.65 writes to stdout. The
// usage blocks are left out.
const (
	// qoderAbortedResult ends a turn that an interrupt control request stopped.
	// Qoder uses the same subtype and is_error for a genuine failure.
	qoderAbortedResult = `{"type":"result","subtype":"error_during_execution","duration_ms":2318,"is_error":true,"num_turns":1,"stop_reason":"stop_sequence","total_cost_usd":0,"permission_denials":[],"errors":["Operation aborted"],"uuid":"fbc3efa2-930a-4f9a-a55e-dea1aa56ca28","session_id":"session-1"}`
	// qoderSuccessResult ends a turn that finished. It carries no num_tool_uses.
	qoderSuccessResult = `{"type":"result","subtype":"success","duration_ms":377,"is_error":false,"num_turns":2,"result":"done","stop_reason":"end_turn","total_cost_usd":0,"permission_denials":[],"uuid":"440c942c-6262-444f-841b-5aa4237a7015","session_id":"session-1"}`
)

// qoderToolUseFrame is one main-thread assistant frame that calls one tool.
func qoderToolUseFrame(callID string) []byte {
	return []byte(`{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"` + callID + `","name":"Bash","input":{"command":"echo SOUND42"}}]},"parent_tool_use_id":null,"session_id":"session-1"}`)
}

// qoderToolResultFrame is the user frame that answers one tool call.
func qoderToolResultFrame(callID string) []byte {
	return []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + callID + `","content":"SOUND42"}]},"parent_tool_use_id":null,"session_id":"session-1"}`)
}

// turnEndEntries returns the persisted turn-end rows in order.
func turnEndEntries(sink *agenttest.Sink) []agenttest.Message {
	var ends []agenttest.Message
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			ends = append(ends, message)
		}
	}
	return ends
}

// onlyTurnEnd returns the one persisted turn-end row.
func onlyTurnEnd(t *testing.T, sink *agenttest.Sink) agenttest.Message {
	t.Helper()
	ends := turnEndEntries(sink)
	require.Len(t, ends, 1, "one result frame persists one turn-end row")
	return ends[0]
}

// resolvedTurnEndToolUses reads the count the way the Worker's PersistTurnEnd
// reads it: the provider data first, then the worker metadata over it.
func resolvedTurnEndToolUses(message agenttest.Message) (int32, bool) {
	resolved := agent.ResolveMessageContent(qoderProvider{}, agent.MessageContent{
		Original: message.Content,
		Metadata: message.Metadata,
	})
	return qoderProvider{}.TurnEndToolUses(resolved)
}

// The user stopped the turn, so the turn end must not read as a failure.
func TestQoderInterruptMarksTheAbortedTurnEndInterrupted(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(qoderAbortedResult))

	assert.Equal(t, agent.MessageCompletionInterrupted, onlyTurnEnd(t, sink).Completion,
		"the turn that the user stopped must not read as a failure")
}

// A native turn without tools states an explicit zero, so the browser can
// tell "no tools" from "count unknown".
func TestQoderTurnEndReportsExplicitZeroToolUses(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	a.HandleOutput([]byte(qoderSuccessResult))

	count, ok := resolvedTurnEndToolUses(onlyTurnEnd(t, sink))
	assert.True(t, ok, "a native turn without tools must report explicit zero")
	assert.Equal(t, int32(0), count)
}

func TestQoderTurnEndCountsItsToolUses(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	for _, callID := range []string{"call-1", "call-2"} {
		a.HandleOutput(qoderToolUseFrame(callID))
		a.HandleOutput(qoderToolResultFrame(callID))
	}
	a.HandleOutput([]byte(qoderSuccessResult))

	count, ok := resolvedTurnEndToolUses(onlyTurnEnd(t, sink))
	assert.True(t, ok)
	assert.Equal(t, int32(2), count)
}

// The count of one turn must not leak into the next turn. The second turn
// opens the way the real reader opens it: an output frame that arrives while
// the agent is idle.
func TestQoderTurnEndToolUsesStartFromZeroEachTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	a.HandleOutput(qoderToolUseFrame("call-1"))
	a.HandleOutput(qoderToolResultFrame("call-1"))
	a.HandleOutput([]byte(qoderSuccessResult))
	a.HandleOutput([]byte(`{"type":"assistant","message":{"type":"message","role":"assistant","content":[{"type":"text","text":"done"}]},"parent_tool_use_id":null,"session_id":"session-1"}`))
	a.HandleOutput([]byte(qoderSuccessResult))

	ends := turnEndEntries(sink)
	require.Len(t, ends, 2)
	first, ok := resolvedTurnEndToolUses(ends[0])
	assert.True(t, ok)
	assert.Equal(t, int32(1), first)
	second, ok := resolvedTurnEndToolUses(ends[1])
	assert.True(t, ok)
	assert.Equal(t, int32(0), second)
}

// A failed turn that no interrupt stopped keeps the outcome that Qoder states.
func TestQoderFailedTurnWithoutInterruptKeepsItsOutcome(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	a.HandleOutput([]byte(qoderAbortedResult))

	assert.Empty(t, onlyTurnEnd(t, sink).Completion)
}

// A turn that finished before Qoder read the interrupt keeps its own outcome:
// the stop came too late to change it.
func TestQoderInterruptAfterTheTurnFinishedKeepsItsOutcome(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(qoderSuccessResult))

	assert.Empty(t, onlyTurnEnd(t, sink).Completion)
}

// Qoder acknowledges an interrupt that arrives while no turn runs, and sends
// no `result` for it. The interrupt must not mislabel the NEXT turn.
func TestQoderIdleInterruptDoesNotMislabelTheNextTurn(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newGoalAgent(t)

	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Contains(t, stdin.String(), `"subtype":"interrupt"`, "the interrupt still reaches Qoder")
	a.setTurnActive(true)
	a.HandleOutput([]byte(qoderAbortedResult))

	assert.Empty(t, onlyTurnEnd(t, sink).Completion)
}

// A turn can end without a `result`, for example at a stop. The note of that
// turn must not mislabel the next turn.
func TestQoderInterruptNoteEndsWithItsTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)

	a.setTurnActive(true)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	a.setTurnActive(false)
	a.setTurnActive(true)
	a.HandleOutput([]byte(qoderAbortedResult))

	assert.Empty(t, onlyTurnEnd(t, sink).Completion)
}

// An interrupt that never reached Qoder stops nothing, so it leaves no note.
func TestQoderUnsentInterruptLeavesNoNote(t *testing.T) {
	t.Parallel()
	a, sink, _ := newGoalAgent(t)
	a.SetStdinForTest(agenttest.FailingStdin{})

	a.setTurnActive(true)
	require.Error(t, a.Interrupt(agent.StopContext{}))
	a.HandleOutput([]byte(qoderAbortedResult))

	assert.Empty(t, onlyTurnEnd(t, sink).Completion)
}

// A child agent's tool calls belong to the child's transcript. Only the
// root's own calls count toward the root turn.
func TestQoderChildToolUsesStayOutOfTheRootCount(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newOfflineAgent(t, sink)

	a.setTurnActive(true)
	a.HandleOutput([]byte(qoderChildStarted))
	a.HandleOutput([]byte(`{"type":"assistant","parent_tool_use_id":"spawn-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"child-read-1","name":"Read","input":{"file_path":"note.txt"}}]}}`))
	a.HandleOutput(qoderToolUseFrame("root-call-1"))
	a.HandleOutput([]byte(qoderSuccessResult))

	count, ok := resolvedTurnEndToolUses(onlyTurnEnd(t, sink))
	assert.True(t, ok)
	assert.Equal(t, int32(1), count, "the child's Read must not count toward the root turn")
}
