package kimi

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestKimiLateTaskOutputPreservesTheNewerTurnScope(t *testing.T) {
	t.Parallel()
	for _, report := range []bool{false, true} {
		label := "diagnostic"
		if report {
			label = "report"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			native := newKimiOutputRig(t)
			seedKimiChildInvocation(t, native)
			native.transcriptAgentOps(t, "agent-0", 1,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other"}}}`,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other"}}}`)
			native.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"done","input":{"description":"Old assignment","prompt":"Review it."},"output":"task_id: old-task\nstatus: running\nagent_id: agent-0\nactual_subagent_type: explore\nautomatic_notification: true\n\ndescription: Old assignment","agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
			native.transcriptOps(t, 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","ordinal":0,"state":"completed","origin":{"kind":"user"}}}`)
			native.transcriptOps(t, 4, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t1","ordinal":1,"state":"running","origin":{"kind":"user"}}}`)
			native.transcriptAgentOps(t, "agent-0", 2, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"running","origin":{"kind":"user"}}}`)
			_, child := childOf(t, native.sink, "session_1/agent-0")
			writer := native.agent.runSink(native.agent.run("agent-0"))
			writer.ReportProgress(agent.NativeTokenProgress("new-model", 17))
			writer.ReportProgress(agent.OutputDeltaProgress("new-output", 8))
			native.agent.sink.ReportProgress(agent.NativeTokenProgress("new-parent-model", 23))
			childBefore, parentBefore := child.ProgressSnapshot(), native.sink.ProgressSnapshot()
			if report {
				native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","result_summary":"Old task result."}`))
			} else {
				native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","error":"Old task result."}`))
			}
			assert.Equal(t, childBefore, child.ProgressSnapshot(), "old task output must not reset the newer child's model progress")
			assert.Equal(t, parentBefore, native.sink.ProgressSnapshot())
			assert.True(t, native.agent.ActiveChildTurnState("session_1/agent-0").Active)
			find := func(rows []agenttest.Message) agenttest.Message {
				t.Helper()
				for _, row := range rows {
					if strings.Contains(string(row.Content), "Old task result.") {
						return row
					}
				}
				t.Fatal("the old task result was not retained")
				return agenttest.Message{}
			}
			rows := child.Messages()
			if report {
				rows = child.PersistedNotifications()
			}
			row := find(rows)
			require.True(t, row.Stored())
			assert.True(t, row.TranscriptOnly, "old task output must keep its historical publication owner")
			if report {
				assert.True(t, find(native.sink.PersistedNotifications()).TranscriptOnly, "the parent report copy also keeps its old scope")
			}
		})
	}
}

func TestKimiRecoveredTaskPreservesTheUserScopeReplayedDuringAdoption(t *testing.T) {
	t.Parallel()
	native := newKimiOutputRig(t)
	native.agent.sink.UpdateSessionID("session_1")
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"user","state":"running","origin":{"kind":"user"}}}`,
		`{"op":"frame.upsert","turnId":"user","stepId":"user.1","frame":{"kind":"thinking","frameId":"thought","text":"This newer user turn must keep its reasoning progress while it runs."}}`)
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"unknown-old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","error":"The older unobserved task failed."}`))
	_, linked := native.agent.children.get("agent-0")
	require.True(t, linked)

	native.agent.Mu.Lock()
	userTurn := native.agent.runLocked("agent-0").userTurn
	native.agent.Mu.Unlock()
	assert.True(t, userTurn)
	row, output := childOf(t, native.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "the current user assignment stays running")
	assert.Greater(t, output.ProgressSnapshot().ThinkingTokens, int64(0))
	rows := output.Messages()
	require.NotEmpty(t, rows)
	diagnostic := rows[len(rows)-1]
	assert.Contains(t, string(diagnostic.Content), "The older unobserved task failed.")
	assert.True(t, diagnostic.TranscriptOnly, "unknown old work needs an explicit historical owner")
	assert.Empty(t, diagnostic.AgentSessionID, "an unknown child session must not inherit the parent's native session")
	assert.True(t, native.agent.ActiveChildTurnState("session_1/agent-0").Active)
}

func TestKimiUnjoinedChildRetryCannotBorrowThePreviousTaskScope(t *testing.T) {
	t.Parallel()
	native := newKimiOutputRig(t)
	seedKimiChildInvocation(t, native)
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other","payload":{"kind":"system_trigger","name":"subagent"}}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other","payload":{"kind":"system_trigger","name":"subagent"}}}}`)
	// The next assignment's parent and task frames have not reached this stream.
	native.transcriptAgentOps(t, "agent-0", 2,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"unjoined","state":"running","origin":{"kind":"other","payload":{"kind":"system_trigger","name":"subagent"}}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"unjoined","state":"completed","origin":{"kind":"other","payload":{"kind":"system_trigger","name":"subagent"}}}}`)
	native.transcriptAgentOps(t, "agent-0", 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"retry","state":"running","origin":{"kind":"retry"}}}`)
	_, child := childOf(t, native.sink, "session_1/agent-0")
	writer := native.agent.runSink(native.agent.run("agent-0"))
	writer.ReportProgress(agent.NativeTokenProgress("new-retry", 17))
	before := child.ProgressSnapshot()
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","error":"The old assignment failed."}`))
	assert.Equal(t, before, child.ProgressSnapshot())
	assert.True(t, native.agent.ActiveChildTurnState("session_1/agent-0").Active)
	row, _ := childOf(t, native.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	rows := child.Messages()
	require.NotEmpty(t, rows)
	assert.Contains(t, string(rows[len(rows)-1].Content), "The old assignment failed.")
	assert.True(t, rows[len(rows)-1].TranscriptOnly)
}

func TestKimiVerifiedTaskIdentityRejectsAReusedParentCall(t *testing.T) {
	t.Parallel()
	native := newKimiOutputRig(t)
	native.agent.sink.UpdateSessionID("session_1")
	// Task A was observed without its original parent's transcript frame.
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-a","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"reused","description":"Old task"}`))
	native.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other"}}}`)
	native.transcriptOps(t, 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"new-parent","ordinal":1,"state":"running","origin":{"kind":"user"}}}`,
		`{"op":"step.upsert","turnId":"new-parent","step":{"kind":"step","stepId":"new-step","ordinal":1,"state":"running"}}`,
		`{"op":"frame.upsert","turnId":"new-parent","stepId":"new-step","frame":{"kind":"tool","frameId":"new-frame","toolCallId":"reused","name":"Agent","state":"done","input":{"resume":"agent-0","prompt":"New task."},"output":"task_id: task-b\nstatus: running\nagent_id: agent-0\nactual_subagent_type: explore\nautomatic_notification: true\n\ndescription: New task","agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	native.agent.Mu.Lock()
	task := native.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "task-a"}]
	native.agent.Mu.Unlock()
	require.NotNil(t, task)
	native.agent.dispatchMu.Lock()
	binding := native.agent.findTaskInvocation(task, native.agent.run("main"))
	native.agent.finishDispatch()
	assert.Nil(t, binding, "the verified task-b result excludes task-a despite the reused call ID")
	native.agent.sink.ReportProgress(agent.NativeTokenProgress("new-parent", 23))
	before := native.sink.ProgressSnapshot()
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-a","kind":"subagent","status":"ended","outcome":"completed","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"reused","result_summary":"The old assignment result."}`))
	assert.Equal(t, before, native.sink.ProgressSnapshot())
	rows := native.sink.PersistedNotifications()
	require.NotEmpty(t, rows)
	assert.Contains(t, string(rows[len(rows)-1].Content), "The old assignment result.")
	assert.True(t, rows[len(rows)-1].TranscriptOnly)
}
