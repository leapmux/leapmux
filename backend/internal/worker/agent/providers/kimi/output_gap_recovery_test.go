package kimi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func TestKimiDiscoveredTaskValidatesNativeAssociation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                         string
		shell                        bool
		background                   bool
		child, kind, parent, command string
		detached                     bool
		associated                   bool
	}{
		{name: "background agent", background: true, child: "agent-0", kind: "subagent", parent: "tool", detached: true, associated: true},
		{name: "agent without optional parent", background: true, child: "agent-0", kind: "subagent", detached: true, associated: true},
		{name: "contradictory foreground agent", background: true, child: "agent-0", kind: "subagent", parent: "tool", detached: false},
		{name: "different agent", background: true, child: "other-agent", kind: "subagent", parent: "tool", detached: true},
		{name: "different agent task kind", background: true, child: "agent-0", kind: "shell", parent: "tool", detached: true},
		{name: "different agent parent", background: true, child: "agent-0", kind: "subagent", parent: "other-tool", detached: true},
		{name: "background shell", shell: true, background: true, kind: "shell", parent: "tool", command: "printf ok > NUL", detached: true, associated: true},
		{name: "windows native command", shell: true, background: true, kind: "shell", parent: "tool", command: "printf ok > /dev/null", detached: true, associated: true},
		{name: "different windows command", shell: true, background: true, kind: "shell", parent: "tool", command: "printf other > /dev/null", detached: true},
		{name: "shell moved to background", shell: true, background: false, kind: "shell", parent: "tool", command: "printf ok > NUL", detached: true, associated: true},
		{name: "foreground shell", shell: true, background: true, kind: "shell", parent: "tool", command: "printf ok > NUL", detached: false},
		{name: "different shell parent", shell: true, background: true, kind: "shell", parent: "other-tool", command: "printf ok > NUL", detached: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
			rig.fake.mu.Lock()
			rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
				assert.Equal(t, "main", command.AgentID)
				assert.Equal(t, "native-task", command.TaskID)
				state := map[string]any{"task_id": command.TaskID, "kind": tc.kind, "status": "running", "detached": tc.detached, "child_agent_id": tc.child, "parent_tool_call_id": tc.parent, "command": tc.command}
				if tc.shell {
					state["status"], state["outcome"] = "ended", "killed"
				}
				return state
			}
			rig.fake.mu.Unlock()
			name := "Agent"
			input := map[string]any{"description": "Reviewer", "prompt": "Review it.", "run_in_background": tc.background}
			output := "task_id: native-task\nstatus: running\nagent_id: agent-0\nactual_subagent_type: explore\nautomatic_notification: true\n\ndescription: Reviewer"
			frame := map[string]any{"kind": "tool", "frameId": "tool", "toolCallId": "tool", "name": name, "state": "done", "input": input, "output": output,
				"agentRefs": []any{map[string]any{"agentId": "agent-0", "role": "child"}}}
			if tc.shell {
				frame["name"] = "Bash"
				frame["input"] = map[string]any{"command": "printf ok > NUL", "run_in_background": tc.background}
				frame["output"] = "task_id: native-task\npid: 123\ndescription: command\nstatus: running\nautomatic_notification: true\n"
				delete(frame, "agentRefs")
			}
			op := map[string]any{"op": "frame.upsert", "turnId": "t0", "stepId": "t0.1", "frame": frame}
			raw, err := json.Marshal(op)
			require.NoError(t, err)
			native.transcriptOps(t, 1, string(raw))
			waitFor(t, rig.agent.taskRecoveryWork.Idle, "the native task association check finishes")
			rig.agent.dispatchMu.Lock()
			rig.agent.Mu.Lock()
			task := rig.agent.tasks[kimiTaskAddress{ownerAgentID: "main", taskID: "native-task"}]
			rig.agent.Mu.Unlock()
			associated := false
			identity := kimiTranscriptToolIdentity{sessionID: "session_1", agentID: "main", turnID: "t0", stepID: "t0.1", frameID: "tool", toolCallID: "tool"}
			invocation := rig.agent.run("main").childInvocations[identity]
			if invocation != nil && invocation.taskTarget != nil {
				associated = invocation.taskTarget.complete
			}
			rig.agent.finishDispatch()
			assert.Equal(t, tc.associated, associated, "native metadata controls association with this complete invocation")
			if tc.shell && !tc.detached {
				assert.Nil(t, task, "a foreground shell is not a background registry task")
				return
			}
			require.NotNil(t, task, "valid native task state remains independently observable even when association is refused")
			if tc.shell {
				row, exists := rig.sink.BackgroundTask("session_1/task/main/native-task")
				require.True(t, exists)
				assert.Equal(t, bgtask.StatusStopped, row.Status)
			}
		})
	}
}
