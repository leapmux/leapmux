package kimi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestKimiTaskResponseRetainsNativeState(t *testing.T) {
	t.Parallel()
	task := `{"task_id":"task_1","kind":"shell","status":"ended","outcome":"completed","detached":true}`
	var frame kimiFrame
	require.NoError(t, json.Unmarshal([]byte(`{"type":"response","request_id":"r1","code":0,"task":`+task+`}`), &frame))
	encoded, err := json.Marshal(frame)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Contains(t, fields, "task")
	assert.JSONEq(t, task, string(fields["task"]))
}

func TestKimiTaskTransportSupportsNativeWatch(t *testing.T) {
	t.Parallel()
	rig := newKimiTestRig(t, agent.Options{})
	ctx, cancel := rig.agent.requestContext()
	defer cancel()
	require.NoError(t, rig.agent.stream.sendTaskCommand(ctx, "task.watch", rig.sessionID(), "main", "task_1"))
	rig.fake.mu.Lock()
	commands := append([]fakeKimiTaskCommand(nil), rig.fake.taskCommands...)
	rig.fake.mu.Unlock()
	require.Len(t, commands, 1)
	assert.Equal(t, "task.watch", commands[0].Type)
	assert.Equal(t, "main", commands[0].AgentID)
	assert.Equal(t, "task_1", commands[0].TaskID)
}

func TestKimiTaskStateReadValidatesIdentityAndReleasesItsWatch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		state     any
		refuse    bool
		wantError string
	}{
		{name: "completed task", state: map[string]any{"task_id": "task_1", "kind": "shell", "status": "ended", "outcome": "completed", "detached": true}},
		{name: "missing state", wantError: "matching native state"},
		{name: "foreign task", state: map[string]any{"task_id": "other", "kind": "shell", "status": "running", "detached": true}, wantError: "matching native state"},
		{name: "invalid outcome", state: map[string]any{"task_id": "task_1", "kind": "shell", "status": "ended", "detached": true}, wantError: "invalid outcome"},
		{name: "refused watch", refuse: true, wantError: "refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiTestRig(t, agent.Options{})
			rig.fake.mu.Lock()
			rig.fake.taskStateFor = func(command fakeKimiTaskCommand) any { assert.Equal(t, "main", command.AgentID); return tc.state }
			rig.fake.taskCommandFor = func(command fakeKimiTaskCommand) (int, string) {
				if tc.refuse && command.Type == kimiFrameTaskWatch {
					return 40404, "refused"
				}
				return 0, "success"
			}
			rig.fake.mu.Unlock()
			state, err := rig.agent.stream.readTaskState(t.Context(), rig.sessionID(), kimiTaskAddress{ownerAgentID: "main", taskID: "task_1"})
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "completed", state.Outcome)
			}
			rig.fake.mu.Lock()
			commands := append([]fakeKimiTaskCommand(nil), rig.fake.taskCommands...)
			rig.fake.mu.Unlock()
			require.Len(t, commands, 2)
			assert.Equal(t, kimiFrameTaskWatch, commands[0].Type)
			assert.Equal(t, kimiFrameTaskUnwatch, commands[1].Type)
			assert.Equal(t, commands[0].TaskID, commands[1].TaskID)
		})
	}
}
