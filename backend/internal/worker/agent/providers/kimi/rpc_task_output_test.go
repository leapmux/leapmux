package kimi

import (
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKimiTaskOutputReaderUsesTheCompleteNativeTarget(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "native 🙂 output"} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.fake.mu.Lock()
			rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
				assert.Equal(t, "agent-owner", command.AgentID)
				require.NotNil(t, command.TailBytes)
				assert.Equal(t, 1<<20, *command.TailBytes)
				return map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": "completed", "detached": false}
			}
			rig.fake.taskOutputsFor = func(command fakeKimiTaskCommand) []any {
				return []any{
					map[string]any{"type": "task.output", "session_id": "foreign", "agent_id": command.AgentID, "task_id": command.TaskID, "offset": 0, "text": "wrong session"},
					map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": "foreign", "task_id": command.TaskID, "offset": 0, "text": "wrong owner"},
					map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": "foreign", "offset": 0, "text": "wrong task"},
					map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": command.TaskID, "offset": 0, "text": text},
				}
			}
			rig.fake.mu.Unlock()
			output, err := rig.agent.stream.readTaskOutput(testutil.DeadlineContext(t), "session_1", kimiTaskAddress{ownerAgentID: "agent-owner", taskID: "task-output"})
			require.NoError(t, err)
			assert.Zero(t, output.offset)
			assert.Equal(t, text, output.text)
		})
	}
}

func TestKimiTaskOutputReaderRejectsInvalidOutput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		offset any
		text   any
	}{
		{"missing offset", nil, "text"},
		{"negative offset", -1, "text"},
		{"missing text", 0, nil},
		{"oversized text", 0, strings.Repeat("x", (1<<20)+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.fake.mu.Lock()
			rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any {
				return map[string]any{"task_id": command.TaskID, "kind": "subagent", "status": "ended", "outcome": "completed", "detached": false}
			}
			rig.fake.taskOutputsFor = func(command fakeKimiTaskCommand) []any {
				return []any{map[string]any{"type": "task.output", "session_id": command.SessionID, "agent_id": command.AgentID, "task_id": command.TaskID, "offset": test.offset, "text": test.text}}
			}
			rig.fake.mu.Unlock()
			_, err := rig.agent.stream.readTaskOutput(testutil.DeadlineContext(t), "session_1", kimiTaskAddress{ownerAgentID: "main", taskID: "task-output"})
			require.Error(t, err)
		})
	}
}
