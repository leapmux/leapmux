package kimi

import (
	"encoding/json"
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
			native := newKimiConversationRig(t)
			seedPublicKimiChildInvocation(t, native)
			native.startPublicTurn(t, "agent-0", "first", 0)
			native.endPublicTurn(t, "agent-0", "first", 0, "completed")
			native.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{"type":"tool_call","item_id":"spawn","step_index":1,"name":"Agent","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"Old assignment","prompt":"Review it."},"output":"task_id: old-task\nstatus: running\nagent_id: agent-0\nactual_subagent_type: explore\nautomatic_notification: true\n\ndescription: Old assignment","agent_refs":[{"agent_id":"agent-0","role":"child","task_id":"old-task"}]}}`)
			native.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
			native.startPublicTurn(t, kimiMainAgentID, "t1", 1)
			native.publicOpener(t, kimiMainAgentID, "t1", `{"kind":"user"}`)
			native.startPublicTurn(t, "agent-0", "user", 1)
			native.publicOpener(t, "agent-0", "user", `{"kind":"user"}`)
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

func TestKimiNativeTaskReportUsesItsExactCurrentParentTurn(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name, change string
		historical   bool
	}{
		{name: "current parent turn"},
		{name: "removed parent item", change: `{"type":"item.removed","turn_id":"t0","item_id":"spawn"}`, historical: true},
		{name: "new parent turn", change: `{"type":"turn.started","turn":{"turn_id":"new","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[]}}`, historical: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiFinalityRig(t, true)
			(kimiFinalityTool{background: true, taskID: "old-task"}).publish(t, rig)
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			if scenario.change != "" {
				rig.conversationFrame(t, scenario.change)
			}
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"old-task","kind":"subagent","status":"ended","outcome":"completed","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","result_summary":"Task finished independently."}`))
			awaitKimiTestOutput(t, rig.agent.sink)
			reports := rig.sink.PersistedNotifications()
			require.Len(t, reports, 1)
			assert.Contains(t, string(reports[0].Content), "Task finished independently.")
			assert.Equal(t, scenario.historical, reports[0].TranscriptOnly)
			_, child := childOf(t, rig.sink, "session_1/agent-0")
			childReports := child.PersistedNotifications()
			require.Len(t, childReports, 1)
			assert.True(t, childReports[0].TranscriptOnly, "the coordinator does not own the child's running turn")
			assertKimiFinalityChildStillRuns(t, rig)
		})
	}
}

func TestKimiRecoveredTaskPreservesTheUserScopeReplayedDuringAdoption(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	turn := json.RawMessage(`{"turn_id":"user","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[
		{"type":"user_message","item_id":"user-prompt","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Keep working.","prompt_ids":["native-user-prompt"],"placement":"opener","origin":{"kind":"user"},"attachments":[],"skills":[]},
		{"type":"reasoning","item_id":"thought","step_index":1,"status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","text":"Continue reasoning."}
	]}`)
	page := json.RawMessage(kimiConversationTestPage(t, rig.sessionID(), "agent-0", turn))
	rig.fake.update(rig.sessionID(), func(session *fakeKapSession) { session.ReplayFrames = []any{page} })
	const task = `{"type":"task","session_id":"session_1","agent_id":"main","task_id":"unknown-old-task","kind":"subagent","status":"ended","outcome":"failed","detached":false,"child_agent_id":"agent-0","error":"The older unobserved task failed."}`
	rig.agent.HandleOutput([]byte(task))
	run := rig.agent.run("agent-0")
	waitFor(t, func() bool { return run.conversationRecovery.Load() == nil }, "the adopted child recovers its public conversation page")
	rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", `{"type":"item.delta","turn_id":"user","item_id":"thought","field":"text","delta":" This newer user turn remains active."}`))
	awaitKimiTestOutput(t, rig.agent.sink)

	rig.agent.Mu.Lock()
	userTurn := run.userTurn
	rig.agent.Mu.Unlock()
	assert.True(t, userTurn)
	row, output := childOf(t, rig.sink, "session_1/agent-0")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "the current user assignment stays running")
	progress := output.ProgressSnapshot()
	assert.Greater(t, progress.ThinkingTokens, int64(0))
	rows := output.Messages()
	require.Len(t, rows, 1)
	diagnostic := rows[0]
	assert.Contains(t, string(diagnostic.Content), "The older unobserved task failed.")
	assert.True(t, diagnostic.TranscriptOnly, "unknown old work needs an explicit historical owner")
	assert.Empty(t, rig.sink.Messages(), "the child's task error stays in its child transcript")
	assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
	rig.agent.HandleOutput([]byte(task))
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Equal(t, progress, output.ProgressSnapshot(), "a repeated old diagnostic cannot reset the newer progress")
	assert.Equal(t, rows, output.Messages())
	assert.Equal(t, bgtask.StatusFailed, nativeTaskOf(t, rig.sink, "unknown-old-task").Status)
}

func TestKimiUnjoinedChildRetryCannotBorrowThePreviousTaskScope(t *testing.T) {
	t.Parallel()
	native := newKimiConversationRig(t)
	seedPublicKimiChildInvocation(t, native)
	native.startPublicTurn(t, "agent-0", "first", 0)
	native.publicOpener(t, "agent-0", "first", `{"kind":"system"}`)
	native.endPublicTurn(t, "agent-0", "first", 0, "completed")
	// The next assignment's parent and task frames have not reached this stream.
	native.startPublicTurn(t, "agent-0", "unjoined", 1)
	native.publicOpener(t, "agent-0", "unjoined", `{"kind":"system"}`)
	native.endPublicTurn(t, "agent-0", "unjoined", 1, "completed")
	native.startPublicTurn(t, "agent-0", "retry", 2)
	native.publicOpener(t, "agent-0", "retry", `{"kind":"retry"}`)
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
	native := newKimiConversationRig(t)
	native.agent.sink.UpdateSessionID("session_1")
	// Task A was observed without its original parent's transcript frame.
	native.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-a","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-0","parent_tool_call_id":"reused","description":"Old task"}`))
	native.startPublicTurn(t, "agent-0", "first", 0)
	native.endPublicTurn(t, "agent-0", "first", 0, "completed")
	native.startPublicTurn(t, kimiMainAgentID, "new-parent", 1)
	native.conversationFrame(t, `{"type":"item.ended","turn_id":"new-parent","item":{"type":"tool_call","item_id":"reused","step_index":1,"name":"Agent","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"resume":"agent-0","prompt":"New task."},"output":"task_id: task-b\nstatus: running\nagent_id: agent-0\nactual_subagent_type: explore\nautomatic_notification: true\n\ndescription: New task","agent_refs":[{"agent_id":"agent-0","role":"child","task_id":"task-b"}]}}`)
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
