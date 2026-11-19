package kimi

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiNativeAgentTaskRecoversItsReportWithoutResultSummary(t *testing.T) {
	t.Parallel()
	for _, background := range []bool{false, true} {
		t.Run(fmt.Sprintf("background=%t", background), func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
			native.startPublicTurn(t, kimiMainAgentID, "t0", 0)
			native.conversationFrame(t, fmt.Sprintf(`{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"spawn","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"Reviewer","run_in_background":%t},"agent_refs":[{"agent_id":"agent-0","role":"child","task_id":"task-report"}]}}`, background))
			rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-report","kind":"subagent","status":"running","detached":%t,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Reviewer"}`, background)))
			_, child := childOf(t, rig.sink, "session_1/agent-0")
			rig.fake.mu.Lock()
			rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
				return map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": "completed", "detached": background, "child_agent_id": "agent-0", "parent_tool_call_id": "spawn"}
			}
			rig.fake.taskOutputsFor = func(command fakeKimiTaskCommand) []any {
				return []any{map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": command.TaskID, "offset": 0, "text": "native task report: checked the actual diff"}}
			}
			rig.fake.mu.Unlock()
			end := fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-report","kind":"subagent","status":"ended","outcome":"completed","detached":%t,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`, background)
			rig.agent.HandleOutput([]byte(end))
			row, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "task-report"))
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusSucceeded, row.Status, "report recovery must not hold the task lifecycle open")
			waitFor(t, func() bool { return len(child.LeapMuxNotifications()) == 1 }, "the native task output reaches the child report")
			assert.Contains(t, fmt.Sprint(child.LeapMuxNotifications()[0]), "checked the actual diff")
			if background {
				waitFor(t, func() bool { return len(rig.sink.LeapMuxNotifications()) == 1 }, "a background report reaches its parent")
				assert.Contains(t, fmt.Sprint(rig.sink.LeapMuxNotifications()[0]), "checked the actual diff")
			} else {
				assert.Empty(t, rig.sink.LeapMuxNotifications())
			}
			rig.agent.HandleOutput([]byte(end))
			assert.Len(t, child.LeapMuxNotifications(), 1)
			for _, notification := range child.LeapMuxNotifications() {
				assert.NotEqual(t, "task.output", notification["type"], "the report must not display a raw protocol frame")
			}
		})
	}
}

func TestKimiNativeTaskReportRetriesAfterTheTaskHasClosed(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	clock := testutil.NewQuartzMock(t)
	rig.agent.clock = clock
	ctx := testutil.DeadlineContext(t)
	armed := clock.Trap().NewTimer(kimiTaskReportTimerTag)
	defer armed.Close()
	rig.fake.mu.Lock()
	rig.fake.taskCommandFor = func(command fakeKimiTaskCommand) (int, string) { return 50001, "output temporarily unavailable" }
	rig.fake.mu.Unlock()
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-report","kind":"subagent","status":"ended","outcome":"completed","detached":true,"child_agent_id":"agent-0","description":"Reviewer"}`))
	delay := testutil.WaitForTimer(t, ctx, armed)
	row, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "task-report"))
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Empty(t, child.LeapMuxNotifications())
	rig.fake.mu.Lock()
	rig.fake.taskCommandFor = nil
	rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
		return map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": "completed", "detached": true}
	}
	rig.fake.taskOutputsFor = func(command fakeKimiTaskCommand) []any {
		return []any{map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": command.TaskID, "offset": 123, "text": "the retained tail"}}
	}
	rig.fake.mu.Unlock()
	clock.Advance(delay).MustWait(ctx)
	waitFor(t, rig.agent.taskReportWork.Idle, "the closed task report is recovered")
	require.Len(t, child.LeapMuxNotifications(), 1)
	assert.Contains(t, fmt.Sprint(child.LeapMuxNotifications()[0]), "The task report is truncated")
	assert.Contains(t, fmt.Sprint(child.LeapMuxNotifications()[0]), "the retained tail")
	assert.Len(t, rig.sink.LeapMuxNotifications(), 1)
}

func TestKimiNativeTaskReportsDistinguishRepeatedTextFromDifferentTasks(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	for _, taskID := range []string{"first", "second"} {
		rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":%q,"kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-0","description":"Reviewer"}`, taskID)))
		rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":%q,"kind":"subagent","status":"ended","outcome":"completed","detached":true,"child_agent_id":"agent-0","result_summary":"the same valid result"}`, taskID)))
	}
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	assert.Len(t, child.LeapMuxNotifications(), 2)
	assert.Len(t, rig.sink.LeapMuxNotifications(), 2)
}

func TestKimiNativeTaskCompletionDoesNotInterruptQueuedTranscriptResults(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "parent", 0)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"parent","item":{"type":"tool_call","item_id":"spawn","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-report","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	rig.startPublicTurn(t, "agent-0", "t0", 0)
	rig.conversationAgentFrame(t, "agent-0", `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"read","step_index":1,"name":"Read","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"path":"read.go"}}}`)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-report","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","result_summary":"native completed report"}`))
	for _, message := range child.Messages() {
		require.False(t, message.Closing, "task state does not prove that queued transcript operations were lost")
	}
	rig.conversationAgentFrame(t, "agent-0", `{"type":"item.ended","turn_id":"t0","item":{"type":"tool_call","item_id":"read","step_index":1,"name":"Read","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"path":"read.go"},"output":"final native tool output"}}`)
	rig.endPublicTurn(t, "agent-0", "t0", 0, "completed")
	awaitKimiTestOutput(t, rig.agent.sink)
	closed := 0
	for _, message := range child.Messages() {
		if message.Closing {
			closed++
			assert.NotEqual(t, agent.MessageCompletionInterrupted, message.Completion)
			view, err := decodeKimiConversationRecord(message.Content)
			require.NoError(t, err)
			require.NotNil(t, view.item)
			assert.Equal(t, "completed", view.item.Outcome)
			assert.JSONEq(t, `"final native tool output"`, string(view.item.Output))
		}
	}
	assert.Equal(t, 1, closed)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}
