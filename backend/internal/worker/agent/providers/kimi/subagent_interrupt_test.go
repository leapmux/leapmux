package kimi

import (
	"fmt"
	"slices"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const kimiNativeInterruptTask = `{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-a","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`

// The Worker dispatches a child interrupt only through this optional interface.
func interruptKimiChild(provider agent.Agent, childKey string, stop agent.StopContext) error {
	interrupter, supported := provider.(agent.ChildInterrupter)
	if !supported {
		return agent.ErrChildOperationUnsupported
	}
	return interrupter.InterruptChild(childKey, stop)
}

func TestKimiChildInterruptDoesNotGuessACoordinatorOrChangeNativeOutcomes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		outcome string
		status  bgtask.Status
	}{
		{"completed", bgtask.StatusSucceeded}, {"failed", bgtask.StatusFailed}, {"killed", bgtask.StatusStopped}, {"timed_out", bgtask.StatusFailed}, {"lost", bgtask.StatusFailed},
	} {
		t.Run(test.outcome, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			native := &kimiOutputRig{agent: rig.agent, sink: rig.sink}
			rig.agent.HandleOutput([]byte(kimiNativeInterruptTask))
			rig.agent.HandleOutput([]byte(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-b","kind":"subagent","status":"running","detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"other"}`))
			child := rig.agent.run("agent-0")
			waitFor(t, func() bool { return child.conversationRecovery.Load() == nil }, "the child initial page precedes its running turn")
			native.startPublicTurn(t, "agent-0", "retry", 0)
			require.ErrorIs(t, interruptKimiChild(rig.agent, "session_1/agent-0", agent.StopContext{}), agent.ErrChildOperationUnsupported)
			rig.agent.HandleOutput([]byte(fmt.Sprintf(`{"type":"task","session_id":"session_1","agent_id":"main","task_id":"task-a","kind":"subagent","status":"ended","outcome":%q,"detached":false,"child_agent_id":"agent-0","parent_tool_call_id":"spawn"}`, test.outcome)))
			awaitKimiTestOutput(t, rig.agent.sink)
			if test.outcome == "completed" {
				waitFor(t, func() bool {
					rig.fake.mu.Lock()
					defer rig.fake.mu.Unlock()
					return slices.ContainsFunc(rig.fake.taskCommands, func(command fakeKimiTaskCommand) bool {
						return command.Type == kimiFrameTaskWatch && command.TaskID == "task-a"
					})
				}, "the completed task starts a native report lookup")
			}
			first, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "task-a"))
			require.True(t, exists)
			assert.Equal(t, test.status, first.Status, "unsupported interruption cannot relabel a native outcome")
			second, exists := rig.sink.BackgroundTask(kimiTaskRowKey("session_1", "main", "task-b"))
			require.True(t, exists)
			assert.Equal(t, bgtask.StatusRunning, second.Status)
			assert.True(t, rig.agent.ActiveChildTurnState("session_1/agent-0").Active)
			rig.fake.mu.Lock()
			assert.False(t, slices.ContainsFunc(rig.fake.taskCommands, func(command fakeKimiTaskCommand) bool {
				return command.Type == kimiFrameTaskStop
			}), "an unsupported interrupt must not stop a coordinator task")
			rig.fake.mu.Unlock()
			assert.Empty(t, rig.fake.requestsTo(kimiSessionPath(rig.sessionID(), kimiActionAbort)))
		})
	}
}
