package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiConversationSwarmBindsEachNativeTaskReference(t *testing.T) {
	t.Parallel()
	for _, snapshot := range []bool{false, true} {
		for _, tasksFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("snapshot=%t/tasks_first=%t", snapshot, tasksFirst), func(t *testing.T) {
				t.Parallel()
				rig := newKimiConversationRig(t)
				tasks := []struct {
					childID, taskID, outcome string
					status                   bgtask.Status
				}{
					{"agent-0", "task-a", "failed", bgtask.StatusFailed},
					{"agent-1", "task-b", "completed", bgtask.StatusSucceeded},
				}
				publishTasks := func() {
					for _, task := range tasks {
						raw, err := json.Marshal(map[string]any{"type": "task", "task_id": task.taskID, "kind": "subagent", "status": "ended", "outcome": task.outcome, "detached": false, "child_agent_id": task.childID, "parent_tool_call_id": "swarm-call", "description": "Native assignment"})
						require.NoError(t, err)
						rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "main", string(raw)))
					}
				}
				if tasksFirst {
					publishTasks()
				}
				tool := json.RawMessage(`{"type":"tool_call","item_id":"swarm-call","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"AgentSwarm","stage":"executing","input":{"description":"Review both files","subagent_type":"coder","prompt_template":"Review {{item}}","items":["a.go","b.go"]},"agent_refs":[{"agent_id":"agent-0","role":"member","swarm_index":0,"task_id":"task-a"},{"agent_id":"agent-1","role":"member","swarm_index":1,"task_id":"task-b"}]}`)
				header := map[string]any{"turn_id": "parent", "ordinal": 0, "kind": "agent", "status": "running", "started_at": "2026-11-18T00:00:00Z", "steps": []any{}}
				if snapshot {
					header["items"] = []json.RawMessage{tool}
					raw, err := json.Marshal(map[string]any{"type": "turns", "turns": []any{header}, "has_more": false})
					require.NoError(t, err)
					rig.conversationFrame(t, string(raw))
				} else {
					raw, err := json.Marshal(map[string]any{"type": "turn.started", "turn": header})
					require.NoError(t, err)
					rig.conversationFrame(t, string(raw))
					rig.conversationFrame(t, `{"type":"item.started","turn_id":"parent","item":`+string(tool)+`}`)
				}
				for _, task := range tasks {
					rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), task.childID, `{"type":"turn.started","turn":{"turn_id":"independent-child","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:01Z","steps":[]}}`))
				}
				if !tasksFirst {
					publishTasks()
				}
				awaitKimiTestOutput(t, rig.agent.sink)
				identity := kimiInvocationIdentity{sessionID: rig.sessionID(), agentID: "main", turnID: "parent", toolCallID: "swarm-call"}
				invocation := rig.agent.run("main").childInvocations[identity]
				require.NotNil(t, invocation)
				outcomes := 0
				for _, row := range rig.sink.BackgroundTasks() {
					if row.Role == bgtask.RoleTaskOutcome {
						outcomes++
					}
				}
				assert.Equal(t, 2, outcomes, "native aliases leave one outcome per exact task")
				groupKey := ""
				for _, task := range tasks {
					native := rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: task.taskID}]
					require.NotNil(t, native)
					require.Same(t, invocation, native.boundInvocation, "the public reference must bind this exact native task")
					row, exists := rig.sink.BackgroundTask(native.rowKey)
					require.True(t, exists)
					assert.Equal(t, task.status, row.Status)
					assert.Equal(t, bgtask.RoleTaskOutcome, row.Role)
					assert.NotEmpty(t, row.GroupKey)
					if groupKey == "" {
						groupKey = row.GroupKey
					} else {
						assert.Equal(t, groupKey, row.GroupKey)
					}
					assert.True(t, rig.agent.ActiveChildTurnState(rig.sessionID()+"/"+task.childID).Active, "a coordinator task outcome cannot end its child's independent turn")
					child, linked := rig.agent.children.get(task.childID)
					require.True(t, linked)
					assert.Equal(t, child.childAgentID, row.ChildAgentID)
					activity, exists := rig.sink.BackgroundTask(child.rowKey)
					require.True(t, exists)
					assert.Equal(t, bgtask.RoleChildActivity, activity.Role)
					assert.Equal(t, bgtask.StatusRunning, activity.Status)
				}
			})
		}
	}
}

func TestKimiConversationRefusalCreatesNoUnlinkedChildRun(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name string
		refs string
	}{
		{"unsafe task route", `[{"agent_id":"agent-0","role":"member","task_id":"../task"}]`},
		{"duplicate task", `[{"agent_id":"agent-0","role":"member","task_id":"task-a"},{"agent_id":"agent-1","role":"member","task_id":"task-a"}]`},
		{"duplicate child", `[{"agent_id":"agent-0","role":"member","task_id":"task-a"},{"agent_id":"agent-0","role":"member","task_id":"task-b"}]`},
		{"wrong role after a valid reference", `[{"agent_id":"agent-0","role":"member","task_id":"task-a"},{"agent_id":"agent-1","role":"child","task_id":"task-b"}]`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiConversationRig(t)
			rig.conversationFrame(t, `{"type":"turn.started","turn":{"turn_id":"parent","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)
			rig.conversationFrame(t, `{"type":"item.started","turn_id":"parent","item":{"type":"tool_call","item_id":"swarm-call","status":"running","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","name":"AgentSwarm","stage":"executing","input":{"description":"Invalid native assignment","prompt_template":"Review {{item}}","items":["a.go","b.go"]},"agent_refs":`+scenario.refs+`}}`)
			assert.Empty(t, rig.sink.ChildAgentIDs(), "a refused declaration cannot create a child transcript")
			rig.agent.dispatchMu.Lock()
			defer rig.agent.finishDispatch()
			rig.agent.Mu.Lock()
			assert.Len(t, rig.agent.runs, 1, "a refused declaration cannot leave unlinked child runs")
			rig.agent.Mu.Unlock()
			assert.Empty(t, rig.agent.run("main").childInvocations)
		})
	}
}

func TestKimiConversationNativeTaskReferencesDisambiguateReusedCallIDs(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	var turns []any
	for index := range 2 {
		turnID := fmt.Sprintf("parent-%d", index)
		childID := fmt.Sprintf("agent-%d", index)
		taskID := fmt.Sprintf("task-%d", index)
		turns = append(turns, map[string]any{
			"turn_id": turnID, "ordinal": index, "kind": "agent", "status": "ended", "outcome": "completed",
			"started_at": "2026-11-18T00:00:00Z", "ended_at": "2026-11-18T00:00:01Z", "steps": []any{},
			"items": []any{map[string]any{
				"type": "tool_call", "item_id": "reused-call", "status": "ended", "outcome": "completed",
				"created_at": "2026-11-18T00:00:00Z", "started_at": "2026-11-18T00:00:00Z", "name": "Agent",
				"input":  map[string]any{"description": "Native assignment", "prompt": "Review it."},
				"output": "", "agent_refs": []any{map[string]any{"agent_id": childID, "role": "child", "task_id": taskID}},
			}},
		})
	}
	raw, err := json.Marshal(map[string]any{"type": "turns", "has_more": false, "turns": turns})
	require.NoError(t, err)
	rig.conversationFrame(t, string(raw))
	require.Len(t, rig.sink.ChildAgentIDs(), 2, "native task references distinguish restored operations that reuse a model call ID")
	for index := range 2 {
		taskID := fmt.Sprintf("task-%d", index)
		childID := fmt.Sprintf("agent-%d", index)
		raw, err := json.Marshal(map[string]any{"type": "task", "task_id": taskID, "kind": "subagent", "status": "ended", "outcome": "completed", "detached": false, "child_agent_id": childID, "parent_tool_call_id": "reused-call"})
		require.NoError(t, err)
		rig.agent.HandleOutput(kimiConversationTestFrame(t, rig.sessionID(), "main", string(raw)))
		task := rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: taskID}]
		require.NotNil(t, task)
		identity := kimiInvocationIdentity{sessionID: rig.sessionID(), agentID: "main", turnID: fmt.Sprintf("parent-%d", index), toolCallID: "reused-call"}
		require.Same(t, rig.agent.run("main").childInvocations[identity], task.boundInvocation)
	}
}
