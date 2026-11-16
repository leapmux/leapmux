package kimi

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiEmbeddedResetPrecedesTheRemainingOperations(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"text","frameId":"f1","role":"assistant","text":"old"}}`)
	rig.transcriptOps(t, 2,
		`{"op":"reset","agentId":"main","snapshot":{"items":[{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","state":"running","frames":[{"kind":"text","frameId":"f1","role":"assistant","text":"new"}]}]}],"interactions":[],"hasMoreOlder":false}}`,
		`{"op":"append","target":{"type":"frame","turnId":"t0","stepId":"t0.1","frameId":"f1"},"offset":3,"text":"!"}`,
		`{"op":"step.upsert","turnId":"t0","step":{"kind":"step","stepId":"t0.1","state":"completed"}}`)
	messages := rig.sink.Messages()
	require.Len(t, messages, 1)
	_, text := assembledRow(t, messages[0])
	assert.Equal(t, "new!", text)
}

func TestKimiEmbeddedResetRetiresControlsBeforeChildAdoption(t *testing.T) {
	t.Parallel()
	for _, agentID := range []string{"main", "agent-0"} {
		t.Run(agentID, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.sink.UpdateSessionID("session_1")
			publishNativeKimiApproval(t, rig, agentID, "approval_1")
			require.Len(t, rig.sink.PublishedControls(), 1)
			rig.transcriptAgentOps(t, agentID, 1, fmt.Sprintf(`{"op":"reset","agentId":%q,"snapshot":{"items":[],"interactions":[],"hasMoreOlder":false}}`, agentID))
			assert.Equal(t, []string{"approval_1"}, rig.sink.CanceledControls())
		})
	}
}

func TestKimiRepeatedRunningChildResetPreservesItsBusyState(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"parent","stepId":"parent.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Review"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	rig.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"},"prompt":"Continue"}}`)
	require.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	before := len(child.Messages())
	rig.agent.HandleOutput([]byte(`{"type":"transcript.reset","session_id":"session_1","payload":{"agent_id":"agent-0","seq":2,"snapshot":{"items":[{"kind":"turn","turnId":"older","state":"completed","origin":{"kind":"user"},"steps":[]},{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"},"prompt":"Continue","steps":[]}],"interactions":[]}}}`))
	require.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	current, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	assert.False(t, current.ended, "an older user turn must not end the child's current task row")
	assert.ErrorIs(t, agenttest.SendChildInput(rig.agent, "session_1/agent-0", agent.ChildInput{Content: "another prompt"}), agent.ErrAgentBusy)
	assert.Len(t, child.Messages(), before, "restoring activity must not repeat the first-start prompt")
}

func TestKimiCompleteResetClosesOnlyTheToolsItRemoved(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
		kimiNativeBashOpening,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"keep","toolCallId":"keep","name":"Bash","state":"running","input":{"command":"printf keep"}}}`)
	before := rig.sink.Messages()
	require.Len(t, before, 2)
	rig.transcriptReset(t, 2, `{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","state":"running","frames":[{"kind":"tool","frameId":"keep","toolCallId":"keep","name":"Bash","state":"running","input":{"command":"printf keep"}}]}]}`)
	after := rig.sink.Messages()
	require.Len(t, after, 3)
	assert.True(t, after[2].Closing)
	assert.Equal(t, before[0].SpanID, after[2].SpanID)
	assert.Equal(t, agent.MessageCompletionInterrupted, after[2].Completion)
	rig.transcriptOps(t, 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"}}}`)
	assert.Len(t, rig.sink.Messages(), 5, "the later turn end closes only the remaining tool and its divider")
}

func TestKimiRecoveredOlderTurnDoesNotCloseTheRunningTurnsTool(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","state":"running","origin":{"kind":"user"}}}`,
		`{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":{"kind":"tool","frameId":"finished","toolCallId":"finished","name":"Read","state":"done","input":{"path":"read.go"},"output":"package read"}}`,
		`{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":{"kind":"tool","frameId":"keep","toolCallId":"keep","name":"Bash","state":"running","input":{"command":"printf keep"}}}`)
	opening := rig.sink.Messages()
	require.Len(t, opening, 2)
	span := opening[1].SpanID
	published := len(rig.sink.TurnActives())
	rig.transcriptReset(t, 2,
		`{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"user"},"steps":[]}`,
		`{"kind":"turn","turnId":"t1","state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t1.1","state":"running","frames":[{"kind":"tool","frameId":"finished","toolCallId":"finished","name":"Read","state":"done","input":{"path":"read.go"},"output":"package read"},{"kind":"tool","frameId":"keep","toolCallId":"keep","name":"Bash","state":"running","input":{"command":"printf keep"}}]}]}`)
	assert.NotContains(t, rig.sink.ClosedSpans(), span, "the recovered older completion owns no tool from the running turn")
	assert.NotContains(t, rig.sink.TurnActives()[published:], false, "restoring older completions must not admit input into a running turn")
	assert.Equal(t, "Bash", rig.agent.sink.GetSpanType(span), "the older turn must not reset the running span's metadata")
	for _, message := range rig.sink.Messages() {
		if message.TurnEnd {
			count, ok := metadataField(t, message, contracts.MessageMetadataFieldToolUses)
			require.True(t, ok)
			assert.JSONEq(t, `0`, string(count), "the older turn does not inherit the running turn's tool count")
		}
	}
	rig.transcriptOps(t, 3, `{"op":"frame.upsert","turnId":"t1","stepId":"t1.1","frame":{"kind":"tool","frameId":"keep","toolCallId":"keep","name":"Bash","state":"done","input":{"command":"printf keep"},"output":"keep"}}`)
	closed := 0
	for _, message := range rig.sink.Messages() {
		if message.SpanID == span && message.Closing {
			closed++
		}
	}
	assert.Equal(t, 1, closed, "only the native result closes this tool")
}

func TestKimiEmbeddedResetDoesNotAppendTheLastProgressChunkAgain(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"}}}`,
		kimiNativeBashOpening)
	rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.call","toolCallId":"call","name":"Bash","state":"running","input":{"command":"printf ok"},"progress":{"kind":"stdout","text":"one"}}}`)
	call := rig.agent.run("main").transcript.turns["t0"].steps["t0.1"].tools["t0.1.call"].call
	require.EqualValues(t, 3, call.outputBytes)
	rig.transcriptOps(t, 3, `{"op":"reset","agentId":"main","snapshot":{"items":[{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"user"},"steps":[{"kind":"step","stepId":"t0.1","state":"running","frames":[{"kind":"tool","frameId":"t0.1.call","toolCallId":"call","name":"Bash","state":"running","input":{"command":"printf ok"},"progress":{"kind":"stdout","text":"one"}}]}]}],"interactions":[],"hasMoreOlder":false}}`)
	assert.EqualValues(t, 3, call.outputBytes, "the snapshot repeats the last chunk rather than delivering another chunk")
	assert.Equal(t, "one", call.tail)
}
