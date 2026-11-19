package kimi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiSeparateTaskOutcomesDoNotEndLiveChildActivity(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"spawn","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"Reviewer","prompt":"Review it."},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-a","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	rig.startPublicTurn(t, "agent-0", "live", 0)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-a","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	activity, exists := rig.sink.BackgroundTask(kimiChildRowKey("session_1", "agent-0"))
	require.True(t, exists)
	assert.Equal(t, bgtask.RoleChildActivity, activity.Role)
	assert.Equal(t, bgtask.StatusRunning, activity.Status, "native task finality is independent from the current child turn")
	assert.True(t, rig.agent.ActiveChildTurnState(activity.RowKey).Active)
	outcome, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "task-a"))
	require.True(t, exists, "the native task keeps its independent identity")
	assert.Equal(t, bgtask.RoleTaskOutcome, outcome.Role)
	assert.Equal(t, bgtask.StatusSucceeded, outcome.Status)
	assert.Equal(t, activity.ChildAgentID, outcome.ChildAgentID)
}

func TestKimiSeparateChildActivityCanIdleWhileItsTaskRemainsRunning(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"spawn","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"Reviewer","prompt":"Review it."},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"coordinator","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`))
	rig.startPublicTurn(t, "agent-0", "attempt", 0)
	rig.endPublicTurn(t, "agent-0", "attempt", 0, "completed")
	activity, exists := rig.sink.BackgroundTask(kimiChildRowKey("session_1", "agent-0"))
	require.True(t, exists)
	assert.False(t, activity.Status.IsWorking(), "the completed native attempt settles canonical child activity")
	assert.False(t, rig.agent.ActiveChildTurnState(activity.RowKey).Active)
	task, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "coordinator"))
	require.True(t, exists)
	assert.Equal(t, bgtask.RoleTaskOutcome, task.Role)
	assert.Equal(t, bgtask.StatusRunning, task.Status, "a suspended coordinator remains outstanding between attempts")
}

func TestKimiSeparateNativeTasksRetainDifferentOutcomesForOneChild(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"spawn","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"Reviewer","prompt":"Review it."},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	for _, frame := range []string{
		`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-a","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"older"}`,
		`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-b","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"newer"}`,
		`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-a","kind":"subagent","status":"ended","outcome":"completed","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"older"}`,
	} {
		rig.agent.HandleOutput([]byte(frame))
	}
	first, ok := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "task-a"))
	require.True(t, ok)
	second, ok := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "task-b"))
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, first.Status)
	assert.Equal(t, bgtask.StatusRunning, second.Status)
	assert.Equal(t, first.ChildAgentID, second.ChildAgentID)
	assert.NotEmpty(t, first.ChildAgentID)
	assert.Len(t, rig.sink.ChildAgentIDs(), 1, "separate outcomes share the existing child transcript")
}

func TestKimiSeparateInvocationOutcomesSurviveReusedCallIDs(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "first", 0)
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"first","item":{"type":"tool_call","item_id":"reused","step_index":1,"name":"Agent","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"first","resume":"agent-0"},"output":"agent_id: agent-0\nactual_subagent_type: explore\nstatus: completed\nstop_reason: completed\n\n[summary]\nFinished.","agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	rig.endPublicTurn(t, kimiMainAgentID, "first", 0, "completed")
	rig.startPublicTurn(t, kimiMainAgentID, "second", 1)
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"second","item":{"type":"tool_call","item_id":"reused","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"second","resume":"agent-0"},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	var outcomes []bgtask.Item
	for _, item := range rig.sink.BackgroundTasks() {
		if item.Role == bgtask.RoleTaskOutcome {
			outcomes = append(outcomes, item)
		}
	}
	require.Len(t, outcomes, 2, "each complete invocation identity keeps its result")
	statuses := map[string]bgtask.Status{}
	for _, item := range outcomes {
		statuses[item.Title] = item.Status
		assert.NotEmpty(t, item.ChildAgentID)
	}
	assert.Equal(t, bgtask.StatusSucceeded, statuses["first"])
	assert.Equal(t, bgtask.StatusRunning, statuses["second"])
	assert.Equal(t, outcomes[0].ChildAgentID, outcomes[1].ChildAgentID)
}

func TestKimiSeparateEndedTaskAcceptsItsExactLateInvocationBinding(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	seedPublicKimiChildInvocation(t, rig)
	state := kimiNativeTaskState{TaskID: "old-task", Kind: "subagent", Status: "ended", Outcome: "completed", ChildAgentID: "agent-0", ParentToolCallID: "spawn"}
	rig.agent.dispatchMu.Lock()
	rig.agent.applyNativeTaskState("main", state)
	owner := rig.agent.run("main")
	var invocation *kimiChildInvocation
	for _, candidate := range owner.childInvocations {
		invocation = candidate
	}
	require.NotNil(t, invocation)
	rig.agent.applyNativeTaskStateBound("main", state, invocation)
	bound := rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "old-task"}].boundInvocation
	rig.agent.finishDispatch()
	require.Same(t, invocation, bound, "finished native evidence may acquire an exact relation without changing its outcome")
	outcome, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "old-task"))
	require.True(t, exists)
	assert.Equal(t, bgtask.StatusSucceeded, outcome.Status)
	var outcomes []bgtask.Item
	for _, item := range rig.sink.BackgroundTasks() {
		if item.Role == bgtask.RoleTaskOutcome {
			outcomes = append(outcomes, item)
		}
	}
	assert.Len(t, outcomes, 1, "exact aliases retain a single task outcome")
}

func TestKimiSeparateParentInvocationCannotReviveIdleChildActivity(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	seedPublicKimiChildInvocation(t, rig)
	rig.startPublicTurn(t, "agent-0", "old", 0)
	rig.endPublicTurn(t, "agent-0", "old", 0, "completed")
	before, _ := rig.sink.BackgroundTask(kimiChildRowKey("session_1", "agent-0"))
	require.False(t, before.Status.IsWorking())
	rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{"type":"tool_call","item_id":"new","step_index":1,"name":"Agent","status":"running","stage":"executing","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"description":"New assignment","resume":"agent-0"},"agent_refs":[{"agent_id":"agent-0","role":"child"}]}}`)
	after, _ := rig.sink.BackgroundTask(kimiChildRowKey("session_1", "agent-0"))
	assert.Equal(t, before.Status, after.Status)
	assert.False(t, rig.agent.ActiveChildTurnState(after.RowKey).Active)
}

func TestKimiSeparateSuspendedTaskDoesNotKeepRestoredIdleChildWorking(t *testing.T) {
	t.Parallel()
	for _, empty := range []bool{false, true} {
		for _, resetFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("empty=%t/reset_first=%t", empty, resetFirst), func(t *testing.T) {
				t.Parallel()
				rig := newKimiConversationRig(t)
				attached := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
				rig.agent.Mu.Lock()
				rig.agent.attachedAt = attached
				rig.agent.Mu.Unlock()
				var turns []json.RawMessage
				if !empty {
					turns = append(turns, json.RawMessage(fmt.Sprintf(`{"turn_id":"old","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":%q,"ended_at":%q,"steps":[],"items":[]}`, attached.Add(-2*time.Hour).Format(time.RFC3339Nano), attached.Add(-time.Hour).Format(time.RFC3339Nano))))
				}
				reset := kimiConversationTestPage(t, rig.sessionID(), "agent-0", turns...)
				if resetFirst {
					rig.agent.HandleOutput(reset)
				}
				rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"suspended","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"swarm"}`))
				if !resetFirst {
					rig.agent.HandleOutput(reset)
				}
				child, exists := rig.sink.BackgroundTask(kimiChildRowKey("session_1", "agent-0"))
				require.True(t, exists)
				assert.False(t, child.Status.IsWorking(), "an idle native child snapshot settles activity independently from its coordinator")
				task, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "suspended"))
				require.True(t, exists)
				assert.Equal(t, bgtask.StatusRunning, task.Status)
				assert.False(t, rig.agent.ActiveChildTurnState(child.RowKey).Active)
				rig.startPublicTurn(t, "agent-0", "retry", 1)
				child, _ = rig.sink.BackgroundTask(child.RowKey)
				assert.True(t, child.Status.IsWorking(), "a genuine native retry revives activity")
				assert.True(t, rig.agent.ActiveChildTurnState(child.RowKey).Active)
				rig.endPublicTurn(t, "agent-0", "retry", 1, "completed")
				child, _ = rig.sink.BackgroundTask(child.RowKey)
				assert.False(t, child.Status.IsWorking())
				task, _ = rig.sink.BackgroundTask(task.RowKey)
				assert.Equal(t, bgtask.StatusRunning, task.Status)
			})
		}
	}
}
