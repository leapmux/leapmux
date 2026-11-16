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

func TestKimiInvalidNativeTaskUpdateDoesNotChangeKnownState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, body string }{
		{"missing detached", `"agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"failed","child_agent_id":"agent-0"`},
		{"null detached", `"agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","outcome":"failed","detached":null,"child_agent_id":"agent-0"`},
		{"invalid owner", `"agent_id":"bad/owner","task_id":"new-task","kind":"subagent","status":"running","detached":true`},
		{"invalid child", `"agent_id":"main","task_id":"new-task","kind":"subagent","status":"running","detached":true,"child_agent_id":"bad/child"`},
		{"root as child", `"agent_id":"main","task_id":"new-task","kind":"subagent","status":"running","detached":true,"child_agent_id":"main"`},
		{"negative swarm index", `"agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","swarm_index":-1`},
		{"invalid kind", `"agent_id":"main","task_id":"task-child","kind":"unknown","status":"running","detached":false,"child_agent_id":"agent-0"`},
		{"invalid state", `"agent_id":"main","task_id":"task-child","kind":"subagent","status":"unknown","detached":false,"child_agent_id":"agent-0"`},
		{"missing outcome", `"agent_id":"main","task_id":"task-child","kind":"subagent","status":"ended","detached":false,"child_agent_id":"agent-0"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-child","kind":"subagent","status":"running","detached":true,"child_agent_id":"agent-0","description":"Reviewer"}`))
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1",` + test.body + `}`))
			child, linked := rig.agent.children.get("agent-0")
			require.True(t, linked)
			assert.True(t, child.ended, "task state does not invent child turn activity")

			row, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "task-child"))
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusRunning, row.Status)
			rig.agent.Mu.Lock()
			assert.Len(t, rig.agent.tasks, 1)
			assert.True(t, rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "task-child"}].native.Detached)
			rig.agent.Mu.Unlock()
			assert.Len(t, rig.sink.BackgroundTasks(), 2, "one task outcome and its independent child activity marker")
		})
	}
}

func TestKimiNativeTaskListValidatesEveryEntryBeforeMutation(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		`{"task_id":"second","kind":"shell","status":"running"}`,
		`{"task_id":"second","kind":"unknown","status":"running","detached":true}`,
		`{"task_id":"first","kind":"shell","status":"running","detached":true}`,
	} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[{"task_id":"first","kind":"shell","status":"running","detached":true,"command":"printf first"},%s]}`, bad)))
			assert.Empty(t, rig.sink.BackgroundTasks())
			rig.agent.Mu.Lock()
			assert.Empty(t, rig.agent.tasks)
			rig.agent.Mu.Unlock()
		})
	}
}

func TestKimiTaskWatchRejectsMissingRequiredStateFields(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	rig.fake.mu.Lock()
	rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
		return map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": "completed"}
	}
	rig.fake.mu.Unlock()
	_, err := rig.agent.stream.readTaskState(testutil.DeadlineContext(t), "session_1", kimiTaskAddress{ownerAgentID: "main", taskID: "task-invalid"})
	require.Error(t, err)
	rig.fake.mu.Lock()
	defer rig.fake.mu.Unlock()
	require.Len(t, rig.fake.taskCommands, 2)
	assert.Equal(t, "task.unwatch", rig.fake.taskCommands[1].Type)
}
