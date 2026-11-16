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
			native.transcriptOps(t, 1, fmt.Sprintf(`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"description":"Reviewer","run_in_background":%t},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`, background))
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
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"parent","stepId":"parent.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-report","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0"}`))
	rig.transcriptAgentOps(t, "agent-0", 1,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"running","origin":{"kind":"other"}}}`,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"read","toolCallId":"read","name":"Read","state":"running","input":{"path":"read.go"}}}`)
	_, child := childOf(t, rig.sink, "session_1/agent-0")
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-report","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","result_summary":"native completed report"}`))
	for _, message := range child.Messages() {
		require.False(t, message.Closing, "task state does not prove that queued transcript operations were lost")
	}
	rig.transcriptAgentOps(t, "agent-0", 2,
		`{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"read","toolCallId":"read","name":"Read","state":"done","input":{"path":"read.go"},"output":"final native tool output"}}`,
		`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"t0","state":"completed","origin":{"kind":"other"}}}`)
	closed := 0
	for _, message := range child.Messages() {
		if message.Closing {
			closed++
			assert.NotEqual(t, agent.MessageCompletionInterrupted, message.Completion)
			assert.Contains(t, string(message.Content), `"state":"done"`)
			assert.Contains(t, string(message.Content), "final native tool output")
		}
	}
	assert.Equal(t, 1, closed)
	assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
}
