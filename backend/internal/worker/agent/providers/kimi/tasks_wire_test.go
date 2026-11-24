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
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"spawn","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"prompt":"Review the diff.","description":"Reviewer"},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
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
			rig, native := newKimiChildPromptRig(t)
			const invocation = `{"type":"tool_call","item_id":"spawn","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"Agent","stage":"executing","input":{"description":"Old assignment","prompt":"Review it."},"agent_refs":[{"agent_id":"agent-0","role":"child","task_id":"old-task"}]}`
			native.conversationFrame(t, `{"type":"item.updated","turn_id":"t0","item_id":"spawn","set":{"agent_refs":[{"agent_id":"agent-0","role":"child","task_id":"old-task"}]}}`)
			childFrame := func(raw string) {
				rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", raw))
			}
			const first = `{"turn_id":"first","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}`
			childFrame(`{"type":"turn.started","turn":{"turn_id":"first","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			childFrame(`{"type":"turn.ended","turn":` + first + `}`)
			const followUp = `{"turn_id":"follow-up","ordinal":1,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[]}`
			const prompt = `{"type":"user_message","item_id":"follow-up-prompt","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:01Z","text":"More.","prompt_ids":["external-prompt"],"placement":"opener","origin":{"kind":"user"},"attachments":[],"skills":[]}`
			const answer = `{"type":"assistant_message","item_id":"answer","status":"running","created_at":"2026-11-18T00:00:01Z","text":"Working on the follow-up."}`
			childFrame(`{"type":"turn.started","turn":` + followUp + `}`)
			childFrame(`{"type":"item.ended","turn_id":"follow-up","item":` + prompt + `}`)
			childFrame(`{"type":"item.started","turn_id":"follow-up","item":` + answer + `}`)
			_, childSink := childOf(t, rig.sink, "session_1/agent-0")
			before := childSink.MessageCount()
			rig.fake.mu.Lock()
			rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
				assert.Equal(t, "old-task", command.TaskID)
				state := map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": outcome, "detached": false, "child_agent_id": "agent-0", "parent_tool_call_id": "spawn"}
				if outcome == "failed" {
					state["error"] = "The earlier task failed."
				}
				return state
			}
			rig.fake.mu.Unlock()
			parentPage := json.RawMessage(kimiConversationTestFrame(t, rig.sessionID(), "main", `{"type":"turns","has_more":false,"turns":[{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[],"items":[`+invocation+`]}]}`))
			var older, current map[string]any
			require.NoError(t, json.Unmarshal([]byte(first), &older))
			require.NoError(t, json.Unmarshal([]byte(followUp), &current))
			older["items"] = []any{}
			current["items"] = []json.RawMessage{json.RawMessage(prompt), json.RawMessage(answer)}
			page, err := json.Marshal(map[string]any{"type": "turns", "has_more": false, "turns": []any{older, current}})
			require.NoError(t, err)
			childPage := json.RawMessage(kimiConversationTestFrame(t, rig.sessionID(), "agent-0", string(page)))
			finished := beginNativeKimiReconnect(t, rig,
				json.RawMessage(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`), parentPage, childPage)
			waitFor(t, finished, "the native reconnect dispatches the task list and conversation pages")
			waitFor(t, rig.agent.taskRecoveryWork.Idle, "the old task outcome finishes recovery")
			awaitKimiTestOutput(t, rig.agent.sink)
			row, exists := rig.sink.BackgroundTask("session_1/agent-0")
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
			task := nativeTaskOf(t, rig.sink, "old-task")
			assert.Equal(t, map[string]bgtask.Status{"completed": bgtask.StatusSucceeded, "failed": bgtask.StatusFailed, "killed": bgtask.StatusStopped}[outcome], task.Status)
			afterRecovery := before
			if outcome == "failed" {
				afterRecovery++
				require.Equal(t, afterRecovery, childSink.MessageCount())
				diagnostic := childSink.Messages()[before]
				_, failure := assembledRow(t, diagnostic)
				assert.Equal(t, "The earlier task failed.", failure)
				assert.True(t, diagnostic.TranscriptOnly, "the old task diagnostic keeps its historical scope")
			}
			assert.Equal(t, afterRecovery, childSink.MessageCount(), "old task completion does not flush the new answer")
			child, linked := rig.agent.children.get("agent-0")
			require.True(t, linked)
			assert.False(t, child.ended)
			childFrame(`{"type":"turn.ended","turn":{"turn_id":"follow-up","ordinal":1,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:01Z","steps":[]}}`)
			awaitKimiTestOutput(t, rig.agent.sink)
			row, exists = rig.sink.BackgroundTask("session_1/agent-0")
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusSucceeded, row.Status)
			assert.False(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
			require.Greater(t, childSink.MessageCount(), afterRecovery)
			view, err := decodeKimiConversationRecord(childSink.Messages()[afterRecovery].Content)
			require.NoError(t, err)
			require.NotNil(t, view.item)
			require.NotNil(t, view.item.Text)
			assert.Equal(t, "Working on the follow-up.", *view.item.Text)
		})
	}
}

func TestKimiNativeTaskBeforeReferenceKeepsAddressAndCompletion(t *testing.T) {
	t.Parallel()
	for _, ended := range []bool{false, true} {
		t.Run(fmt.Sprint(ended), func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
			rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"spawn","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"prompt":"Review the diff.","description":"Reviewer"}}}`)
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn","description":"Reviewer"}`))
			if ended {
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
			}
			rig.conversationFrame(t, `{"type":"item.updated","turn_id":"t0","item_id":"spawn","set":{"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
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
	rig := newKimiConversationRig(t)
	sendNativeKimiChildTask(t, rig, "main", "agent-0", "task-first", "running", "")
	sendNativeKimiChildTask(t, rig, "main", "agent-0", "task-first", "ended", "completed")
	sendNativeKimiChildTask(t, rig, "main", "agent-0", "task-second", "running", "")
	child, exists := rig.agent.children.get("agent-0")
	require.True(t, exists)
	assert.True(t, child.ended, "new coordinator work alone does not revive child activity")
	assert.Equal(t, bgtask.StatusSucceeded, nativeTaskOf(t, rig.sink, "task-first").Status)
	assert.Equal(t, bgtask.StatusRunning, nativeTaskOf(t, rig.sink, "task-second").Status)
	rig.startPublicTurn(t, "agent-0", "new-attempt", 0)
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
			rig := newKimiConversationRig(t)
			sendNativeKimiChildTask(t, rig, "main", "agent-0", "task-first", "running", "")
			rig.startPublicTurn(t, "agent-0", "current", 0)
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

func TestKimiUnrecoverableNativeTaskRecoverySettlesToUnknownOutcome(t *testing.T) {
	t.Parallel()
	t.Run("missing native tasks on task.list", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		sendNativeKimiChildTask(t, native, "main", "agent-0", "task-missing", "running", "")
		rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"shell-missing","kind":"shell","status":"running","detached":true,"command":"sleep 10"}`))
		rig.fake.mu.Lock()
		rig.fake.taskCommandFor = func(command fakeKimiTaskCommand) (int, string) {
			if command.Type == kimiFrameTaskWatch {
				return 40404, "task not found"
			}
			return 0, "success"
		}
		rig.fake.mu.Unlock()

		rig.agent.HandleOutput([]byte(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`))
		waitFor(t, rig.agent.taskRecoveryWork.Idle, "missing task recovery finishes")
		awaitKimiTestOutput(t, rig.agent.sink)

		subagentRow, ok := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "task-missing"))
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, subagentRow.Status)
		shellRow, ok := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "shell-missing"))
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, shellRow.Status)
	})

	t.Run("provisional discovered task", func(t *testing.T) {
		t.Parallel()
		rig := newKimiTestRig(t, agent.Options{})
		native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
		rig.fake.mu.Lock()
		rig.fake.taskCommandFor = func(command fakeKimiTaskCommand) (int, string) {
			if command.Type == kimiFrameTaskWatch {
				return 40404, "task not found"
			}
			return 0, "success"
		}
		rig.fake.mu.Unlock()

		native.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"t0","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
		frame := map[string]any{
			"type": "tool_call", "item_id": "spawn", "name": "Agent", "status": "ended", "outcome": "completed",
			"created_at": "2026-11-18T00:00:00Z", "started_at": "2026-11-18T00:00:00Z",
			"input":      map[string]any{"description": "Reviewer", "prompt": "Review it.", "run_in_background": true},
			"output":     "task_id: provisional-task\nstatus: running\nagent_id: agent-0\nautomatic_notification: true\n",
			"agent_refs": []any{map[string]any{"agent_id": "agent-0", "role": "child", "task_id": "provisional-task"}},
		}
		raw, err := json.Marshal(map[string]any{"type": "item.ended", "turn_id": "t0", "item": frame})
		require.NoError(t, err)
		native.conversationFrame(t, string(raw))
		waitFor(t, rig.agent.taskRecoveryWork.Idle, "provisional task recovery finishes")
		awaitKimiTestOutput(t, rig.agent.sink)

		identity := kimiInvocationIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", toolCallID: "spawn"}
		outcomeKey := kimiInvocationGroupKey(identity) + "/child/agent-0"
		row, ok := rig.sink.BackgroundTask(outcomeKey)
		require.True(t, ok)
		assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)
	})
}
