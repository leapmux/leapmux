package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestKimiNativeTaskStateRetainsItsOwnOutcome(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Review the diff.","description":"Reviewer"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Reviewer"}`))
	_, linked := rig.agent.children.get("agent-0")
	require.True(t, linked)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","result_summary":"Review completed."}`))
	row, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "task-child"))
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	assert.Empty(t, rig.agent.ActiveChildTurnState("session_1/agent-0"))
}

func TestKimiRecoveredTaskCompletionPreservesIndependentChildTurn(t *testing.T) {
	t.Parallel()
	for _, outcome := range []string{"completed", "failed", "killed"} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
			native.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"description":"Reviewer"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			native.transcriptAgentOps(t, "agent-0", 1,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"running","origin":{"kind":"other"}}}`,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"first","state":"completed","origin":{"kind":"other"}}}`)
			native.transcriptAgentOps(t, "agent-0", 2,
				`{"op":"turn.upsert","turn":{"kind":"turn","turnId":"follow-up","state":"running","origin":{"kind":"user"},"prompt":"More."}}`,
				`{"op":"frame.upsert","turnId":"follow-up","stepId":"follow-up.1","frame":{"kind":"text","frameId":"answer","role":"assistant","text":"Working on the follow-up."}}`)
			_, childSink := childOf(t, rig.sink, "session_1/agent-0")
			before := childSink.MessageCount()
			rig.fake.mu.Lock()
			rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
				state := map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": outcome, "detached": false, "child_agent_id": "agent-0", "parent_tool_call_id": "spawn"}
				if outcome == "failed" {
					state["error"] = "The earlier task failed."
				}
				return state
			}
			rig.fake.mu.Unlock()
			finished := beginNativeKimiReconnect(t, rig, json.RawMessage(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`))
			waitFor(t, finished, "the native reconnect dispatches the missing-task list")
			waitFor(t, rig.agent.taskRecoveryWork.Idle, "the old task outcome finishes recovery")
			row, exists := rig.sink.BackgroundTask("session_1/agent-0")
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
			afterRecovery := before
			if outcome == "failed" {
				afterRecovery++
				require.Equal(t, afterRecovery, childSink.MessageCount())
				_, failure := assembledRow(t, childSink.Messages()[before])
				assert.Equal(t, "The earlier task failed.", failure)
			}
			assert.Equal(t, afterRecovery, childSink.MessageCount(), "old task completion does not flush the new answer")
			child, linked := rig.agent.children.get("agent-0")
			require.True(t, linked)

			assert.False(t, child.ended)
			native.transcriptAgentOps(t, "agent-0", 3, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"follow-up","state":"completed","origin":{"kind":"user"}}}`)
			row, exists = rig.sink.BackgroundTask("session_1/agent-0")
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
			require.Greater(t, childSink.MessageCount(), afterRecovery)
			assert.Contains(t, string(childSink.Messages()[afterRecovery].Content), "Working on the follow-up.")
		})
	}
}

func TestKimiNativeTaskBeforeReferenceKeepsAddressAndCompletion(t *testing.T) {
	t.Parallel()
	for _, ended := range []bool{false, true} {
		t.Run(fmt.Sprint(ended), func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.transcriptOps(t, 1, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Review the diff.","description":"Reviewer"}}}`)
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Reviewer"}`))
			if ended {
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			}
			rig.transcriptOps(t, 2, `{"op":"frame.upsert","turnId":"t0","stepId":"t0.1","frame":{"kind":"tool","frameId":"t0.1.spawn","toolCallId":"spawn","name":"Agent","state":"running","input":{"prompt":"Review the diff.","description":"Reviewer"},"agentRefs":[{"agentId":"agent-0","role":"child"}]}}`)
			child, linked := rig.agent.children.get("agent-0")
			require.True(t, linked)
			row, exists := rig.sink.BackgroundTask("session_1/agent-0")
			require.True(t, exists)
			assert.True(t, child.ended, "a task or parent reference does not activate a child turn")
			assert.False(t, row.Status.IsWorking())
			task := nativeTaskOf(t, rig.sink, "task-child")
			assert.Equal(t, row.ChildAgentID, task.ChildAgentID)
			expected := bgtask.StatusRunning
			if ended {
				expected = bgtask.StatusSucceeded
			}
			assert.Equal(t, expected, task.Status)

		})
	}
}

func TestKimiNativeTaskCancellationDoesNotClaimAChildTurnInterrupt(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	sendNativeKimiChildTask(t, native, "main", "agent-0", "task-child", "running", "")
	require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
	sendNativeKimiChildTask(t, native, "main", "agent-0", "task-child", "ended", "killed")
	row := nativeTaskOf(t, rig.sink, "task-child")
	assert.Equal(t, bgtask.StatusStopped, row.Status, "a native cancellation is not a user child-tab interruption")
	canonical, _ := rig.sink.BackgroundTask("session_1/agent-0")
	assert.False(t, canonical.Status.IsWorking())
	rig.fake.mu.Lock()
	assert.Empty(t, rig.fake.taskCommands)
	rig.fake.mu.Unlock()
	assert.Empty(t, rig.fake.requestsTo("POST "+kimiItemPath("session_1", "tasks", "task-child", kimiActionCancel)))
}

func TestKimiNativeTaskListRestoresAndEndsBackgroundShell(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[{"task_id":"shell-1","kind":"shell","status":"running","detached":true,"command":"printf background"}]}`))
	row, exists := rig.sink.BackgroundTask("session_1/task/main/shell-1")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"shell-1","kind":"shell","status":"ended","outcome":"killed","detached":true,"command":"printf background"}`))
	row, exists = rig.sink.BackgroundTask("session_1/task/main/shell-1")
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

func sendNativeKimiChildTask(t *testing.T, rig *kimiOutputRig, owner, child, task, status, outcome string) {
	t.Helper()
	raw := fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":%q,"task_id":%q,"kind":"subagent","status":%q,"outcome":%q,"detached":false,"child_agent_id":%q,"parent_tool_call_id":%q}`, owner, task, status, outcome, child, "spawn-"+child)
	rig.agent.HandleOutput([]byte(raw))
}

func TestKimiNativeTasksKeepTheOwningAgentInTheirIdentity(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sendNativeKimiChildTask(t, rig, "main", "agent-0", "shared-task", "running", "")
	sendNativeKimiChildTask(t, rig, "agent-0", "agent-1", "shared-task", "running", "")
	sendNativeKimiChildTask(t, rig, "main", "agent-0", "shared-task", "ended", "completed")
	sendNativeKimiChildTask(t, rig, "agent-0", "agent-1", "shared-task", "ended", "completed")
	for owner, child := range map[string]string{"main": "agent-0", "agent-0": "agent-1"} {
		row, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", owner, "shared-task"))
		require.True(t, exists)
		assert.Equal(t, bgtask.StatusSucceeded, row.Status, child)
	}
}

func TestKimiNativeHeaderRevivesChildIndependentlyFromNewTasks(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sendNativeKimiChildTask(t, rig, "main", "agent-0", "task-first", "running", "")
	sendNativeKimiChildTask(t, rig, "main", "agent-0", "task-first", "ended", "completed")
	sendNativeKimiChildTask(t, rig, "main", "agent-0", "task-second", "running", "")
	child, exists := rig.agent.children.get("agent-0")
	require.True(t, exists)
	assert.True(t, child.ended, "new coordinator work alone does not revive child activity")
	assert.Equal(t, bgtask.StatusSucceeded, nativeTaskOf(t, rig.sink, "task-first").Status)
	assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, rig.sink, "task-second").Status)
	rig.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"new-attempt","state":"running","origin":{"kind":"other"}}}`)
	child, _ = rig.agent.children.get("agent-0")
	assert.False(t, child.ended)

	sendNativeKimiChildTask(t, rig, "main", "agent-0", "task-first", "ended", "completed")
	row, _ := rig.sink.BackgroundTask("session_1/agent-0")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.True(t, rig.agent.ActiveChildTurnState(row.RowKey).Active)
}

func TestKimiNativeTaskListRecoversTheOutcomeOfAKnownMissingTask(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
	sendNativeKimiChildTask(t, native, "main", "agent-0", "task-child", "running", "")
	rig.fake.mu.Lock()
	rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
		assert.Equal(t, "main", command.AgentID)
		assert.Equal(t, "task-child", command.TaskID)
		return map[string]any{"task_id": "task-child", "kind": "subagent", "status": "ended", "outcome": "completed", "detached": false, "child_agent_id": "agent-0", "parent_tool_call_id": "spawn-agent-0", "result_summary": "Recovered completion."}
	}
	rig.fake.mu.Unlock()
	rig.agent.HandleOutput([]byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`))
	waitFor(t, func() bool {
		row, ok := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "task-child"))
		return ok && row.Status == bgtask.StatusSucceeded
	}, "the native task response supplies the missed outcome")
	rig.fake.mu.Lock()
	commands := append([]fakeKimiTaskCommand(nil), rig.fake.taskCommands...)
	rig.fake.mu.Unlock()
	require.Len(t, commands, 2)
	assert.Equal(t, kimiFrameTaskWatch, commands[0].Type)
	assert.Equal(t, kimiFrameTaskUnwatch, commands[1].Type)
}

func TestKimiNativeTaskCompletionPreservesIndependentChildActivity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, completedTask, outcome, wantTask string
		second                                 bool
	}{
		{"older task ends", "task-first", "completed", "task-second", true},
		{"invalid completion", "broken-task", "", "task-first", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			sendNativeKimiChildTask(t, rig, "main", "agent-0", "task-first", "running", "")
			rig.transcriptAgentOps(t, "agent-0", 1, `{"op":"turn.upsert","turn":{"kind":"turn","turnId":"current","state":"running","origin":{"kind":"other"}}}`)
			if tc.second {
				sendNativeKimiChildTask(t, rig, "main", "agent-0", "task-second", "running", "")
			}
			sendNativeKimiChildTask(t, rig, "main", "agent-0", tc.completedTask, "ended", tc.outcome)
			child, exists := rig.agent.children.get("agent-0")
			require.True(t, exists)

			assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, rig.sink, tc.wantTask).Status)
			assert.False(t, child.ended)
			row, exists := rig.sink.BackgroundTask("session_1/agent-0")
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
		})
	}
}
