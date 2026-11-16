package kimi

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sendNativeKimiTaskState(t *testing.T, rig *kimiOutputRig, state kimiNativeTaskState) {
	t.Helper()
	raw, err := json.Marshal(struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		kimiNativeTaskState
	}{Type: "task", SessionID: rig.sessionID(), AgentID: "main", kimiNativeTaskState: state})
	require.NoError(t, err)
	rig.agent.HandleOutput(raw)
}

func TestKimiProcessTaskIsAShellRow(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	state := kimiNativeTaskState{TaskID: "bash-1", Kind: "shell", Status: "running", Detached: true, Command: "npm run watch\n--verbose", Description: "Watch the build"}
	sendNativeKimiTaskState(t, rig, state)
	row, ok := rig.sink.BackgroundTask("session_1/task/main/bash-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindShell, row.Kind)
	assert.Equal(t, "npm run watch", row.Title)
	assert.True(t, row.TitleIsCommand)
	assert.Equal(t, "Watch the build", row.Description)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	sendNativeKimiTaskState(t, rig, state)
	row, _ = rig.sink.BackgroundTask("session_1/task/main/bash-1")
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	state.Status, state.Outcome = "ended", "killed"
	sendNativeKimiTaskState(t, rig, state)
	row, _ = rig.sink.BackgroundTask("session_1/task/main/bash-1")
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

func TestKimiProcessTaskWithNoCommandIsTitledByItsDescription(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	sendNativeKimiTaskState(t, rig, kimiNativeTaskState{TaskID: "bash-2", Kind: "shell", Status: "running", Detached: true, Description: "Serve the docs"})
	row, ok := rig.sink.BackgroundTask("session_1/task/main/bash-2")
	require.True(t, ok)
	assert.Equal(t, "Serve the docs", row.Title)
	assert.False(t, row.TitleIsCommand)
	sendNativeKimiTaskState(t, rig, kimiNativeTaskState{TaskID: "bash-3", Kind: "shell", Status: "running", Detached: true, Command: strings.Repeat("x", 500)})
	row, ok = rig.sink.BackgroundTask("session_1/task/main/bash-3")
	require.True(t, ok)
	assert.Equal(t, strings.Repeat("x", 120), row.Title)
	assert.True(t, row.TitleIsCommand)
	sendNativeKimiTaskState(t, rig, kimiNativeTaskState{TaskID: "bash-4", Kind: "shell", Status: "running", Detached: true, Command: strings.Repeat("한", 500)})
	row, ok = rig.sink.BackgroundTask("session_1/task/main/bash-4")
	require.True(t, ok)
	assert.True(t, utf8.ValidString(row.Title))
	assert.Equal(t, strings.Repeat("한", 42), row.Title)

}

func TestKimiTaskTerminatedBeforeItsStart(t *testing.T) {
	t.Parallel()
	for _, hadRuntimeRecord := range []bool{false, true} {
		t.Run(map[bool]string{false: "without a start", true: "after losing the runtime record"}[hadRuntimeRecord], func(t *testing.T) {
			t.Parallel()
			rig := newKimiOutputRig(t)
			state := kimiNativeTaskState{TaskID: "bash-1", Kind: "shell", Status: "running", Detached: true, Command: "make"}
			if hadRuntimeRecord {
				sendNativeKimiTaskState(t, rig, state)
				rig.agent.Mu.Lock()
				rig.agent.tasks = nil
				rig.agent.Mu.Unlock()
			}
			state.Status, state.Outcome = "ended", "failed"
			sendNativeKimiTaskState(t, rig, state)
			row, ok := rig.sink.BackgroundTask("session_1/task/main/bash-1")
			require.True(t, ok)
			assert.Equal(t, bgtask.StatusFailed, row.Status)
		})
	}
}

func TestKimiNativeOtherAndInvalidTasksHaveNoRow(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	for _, state := range []kimiNativeTaskState{
		{TaskID: "other-1", Kind: "other", Status: "running", Detached: false},
		{TaskID: "invalid-1", Kind: "question", Status: "running", Detached: false},
		{TaskID: "../bad", Kind: "shell", Status: "running", Detached: true},
		{TaskID: "", Kind: "shell", Status: "running", Detached: true},
	} {
		sendNativeKimiTaskState(t, rig, state)
	}
	assert.Empty(t, rig.sink.BackgroundTasks())
}

func TestKimiNativeShellCompletionIsAbsorbing(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	state := kimiNativeTaskState{TaskID: "bash-1", Kind: "shell", Status: "running", Detached: true, Command: "make"}
	sendNativeKimiTaskState(t, rig, state)
	state.Status, state.Outcome = "ended", "completed"
	sendNativeKimiTaskState(t, rig, state)
	state.Outcome = "failed"
	sendNativeKimiTaskState(t, rig, state)
	state.Status, state.Outcome = "running", ""
	sendNativeKimiTaskState(t, rig, state)
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusSucceeded}, rig.sink.BackgroundTaskStatuses("session_1/task/main/bash-1"))
}

func TestKimiTaskStatus(t *testing.T) {
	t.Parallel()

	for status, want := range map[string]struct {
		status bgtask.Status
		final  bool
	}{
		"completed": {bgtask.StatusSucceeded, true},
		"failed":    {bgtask.StatusFailed, true},
		"timed_out": {bgtask.StatusFailed, true},
		"lost":      {bgtask.StatusFailed, true},
		"killed":    {bgtask.StatusStopped, true},
		"running":   {bgtask.StatusRunning, false},
		"":          {bgtask.StatusRunning, false},
		"paused":    {bgtask.StatusRunning, false},
	} {
		got, final := kimiTaskStatus(status)
		assert.Equal(t, want.status, got, status)
		assert.Equal(t, want.final, final, status)
	}
}
