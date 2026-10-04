package kimi

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func taskEvent(eventName string, info map[string]any) map[string]any {
	return map[string]any{"type": eventName, "info": info}
}

func TestKimiProcessTaskIsAShellRow(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.feed(t, taskEvent(contracts.KimiEventTaskStarted, map[string]any{
		"taskId": "bash-1", "kind": "process", "command": "npm run watch\n--verbose", "description": "Watch the build", "status": "running",
	}))
	row, ok := rig.sink.BackgroundTask("session_1/task/bash-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.KindShell, row.Kind)
	assert.Equal(t, "npm run watch", row.Title, "the title is the command's first line")
	assert.True(t, row.TitleIsCommand)
	assert.Equal(t, "Watch the build", row.Description)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	rig.feed(t, taskEvent(contracts.KimiEventTaskTerminated, map[string]any{"taskId": "bash-1", "kind": "process", "status": "running"}))
	row, _ = rig.sink.BackgroundTask("session_1/task/bash-1")
	assert.Equal(t, bgtask.StatusRunning, row.Status, "a status that is not an end leaves the row open")

	rig.feed(t, taskEvent(contracts.KimiEventTaskTerminated, map[string]any{"taskId": "bash-1", "kind": "process", "status": "killed"}))
	row, _ = rig.sink.BackgroundTask("session_1/task/bash-1")
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

func TestKimiProcessTaskWithNoCommandIsTitledByItsDescription(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.feed(t, taskEvent(contracts.KimiEventTaskStarted, map[string]any{"taskId": "bash-2", "kind": "process", "description": "Serve the docs"}))
	row, ok := rig.sink.BackgroundTask("session_1/task/bash-2")
	require.True(t, ok)
	assert.Equal(t, "Serve the docs", row.Title)
	assert.False(t, row.TitleIsCommand)

	long := strings.Repeat("x", 500)
	rig.feed(t, taskEvent(contracts.KimiEventTaskStarted, map[string]any{"taskId": "bash-3", "kind": "process", "command": long}))
	row, _ = rig.sink.BackgroundTask("session_1/task/bash-3")
	assert.LessOrEqual(t, len([]rune(row.Title)), 120)
}

func TestKimiTaskTerminatedBeforeItsStart(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.feed(t, taskEvent(contracts.KimiEventTaskStarted, map[string]any{"taskId": "bash-1", "kind": "process", "command": "make"}))
	// A process restart forgot the task; the end still reaches its row by key.
	rig.agent.Mu.Lock()
	rig.agent.tasks = nil
	rig.agent.Mu.Unlock()
	rig.feed(t, taskEvent(contracts.KimiEventTaskTerminated, map[string]any{"taskId": "bash-1", "kind": "process", "status": "failed"}))
	row, _ := rig.sink.BackgroundTask("session_1/task/bash-1")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
}

func TestKimiAgentTaskStopsItsSubagent(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.startTurn(t, 0, contracts.KimiOriginUser)
	spawnAgent(t, rig, "agent-0", "call_child", map[string]any{"taskId": "", "runInBackground": true})
	rig.feed(t, taskEvent(contracts.KimiEventTaskStarted, map[string]any{"taskId": "agent-task-9", "kind": "agent", "agentId": "agent-0"}))
	child, ok := rig.agent.children.get("agent-0")
	require.True(t, ok)
	assert.Equal(t, "agent-task-9", child.taskID)
	_, found := rig.sink.BackgroundTask("session_1/task/agent-task-9")
	assert.False(t, found, "an agent task's row is its subagent's")

	rig.feed(t, taskEvent(contracts.KimiEventTaskTerminated, map[string]any{"taskId": "agent-task-9", "kind": "agent", "agentId": "agent-0", "status": "timed_out"}))
	row, _ := rig.sink.BackgroundTask("session_1/agent-0")
	assert.Equal(t, bgtask.StatusFailed, row.Status)
	child, _ = rig.agent.children.get("agent-0")
	assert.Empty(t, child.taskID)
}

func TestKimiQuestionTaskHasNoRow(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.feed(t, taskEvent(contracts.KimiEventTaskStarted, map[string]any{"taskId": "q-1", "kind": "question"}))
	rig.feed(t, taskEvent(contracts.KimiEventTaskStarted, map[string]any{"taskId": "x-1", "kind": "unknown"}))
	rig.feed(t, taskEvent(contracts.KimiEventTaskStarted, map[string]any{"kind": "process"}))
	rig.feed(t, taskEvent(contracts.KimiEventTaskStarted, map[string]any{"taskId": "a-1", "kind": "agent"}))
	assert.Empty(t, rig.sink.BackgroundTasks())
}

func TestKimiTaskStatus(t *testing.T) {
	t.Parallel()

	for status, want := range map[string]struct {
		status bgtask.Status
		final  bool
	}{
		"completed": {bgtask.StatusCompleted, true},
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

func TestKimiWireTaskStatus(t *testing.T) {
	t.Parallel()

	for status, want := range map[string]struct {
		status bgtask.Status
		final  bool
	}{
		"completed": {bgtask.StatusCompleted, true},
		"failed":    {bgtask.StatusFailed, true},
		"cancelled": {bgtask.StatusStopped, true},
		"running":   {bgtask.StatusRunning, false},
		"":          {bgtask.StatusRunning, false},
		"killed":    {bgtask.StatusRunning, false},
		"paused":    {bgtask.StatusRunning, false},
	} {
		got, final := kimiWireTaskStatus(status)
		assert.Equal(t, want.status, got, "%q", status)
		assert.Equal(t, want.final, final, "%q: a status outside the listing's words must not close a row", status)
	}
}

func TestKimiStartedBefore(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 25, 10, 0, 0, int(500*time.Microsecond), time.UTC)
	assert.True(t, kimiStartedBefore("2026-09-25T09:59:59.999Z", at))
	assert.False(t, kimiStartedBefore("2026-09-25T10:00:00.000Z", at),
		"the server states milliseconds, so a start in the same millisecond is not before")
	assert.False(t, kimiStartedBefore("2026-09-25T10:00:00.001Z", at))
	assert.False(t, kimiStartedBefore("2026-09-25T19:00:00.000+09:00", at), "the zone of the start is read")
	for _, bad := range []string{"", "yesterday", "2026-09-25"} {
		assert.True(t, kimiStartedBefore(bad, at), "%q: a start that does not parse cannot be told from history", bad)
	}
}

// A task that the stream saw end is settled, and a listing that still states
// it running changes nothing.
func TestKimiReconcileTasksSkipsATaskThatEnded(t *testing.T) {
	t.Parallel()
	rig := newKimiOutputRig(t)
	rig.feed(t, taskEvent(contracts.KimiEventTaskStarted, map[string]any{"taskId": "bash-1", "kind": "process", "command": "make"}))
	rig.feed(t, taskEvent(contracts.KimiEventTaskTerminated, map[string]any{"taskId": "bash-1", "kind": "process", "status": "completed"}))

	rig.agent.reconcileTasks([]kimiTaskItem{
		{ID: "bash-1", Kind: kimiWireTaskKindBash, Status: kimiWireStatusFailed, Command: "make", StartedAt: time.Now().UTC().Format(time.RFC3339Nano)},
		{ID: "../x", Kind: kimiWireTaskKindBash, Status: kimiWireStatusRunning, StartedAt: time.Now().UTC().Format(time.RFC3339Nano)},
	})
	assert.Equal(t, []bgtask.Status{bgtask.StatusRunning, bgtask.StatusCompleted}, rig.sink.BackgroundTaskStatuses("session_1/task/bash-1"))
	assert.Len(t, rig.sink.BackgroundTasks(), 1, "an id that Kimi Code does not issue gets no row")
}
