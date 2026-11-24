package providerkit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func intPtr(v int) *int { return &v }

func TestBackgroundProcessStatus(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		status   string
		exitCode *int
		want     bgtask.Status
	}{
		{name: "empty defaults to running", status: "", want: bgtask.StatusRunning},
		{name: "whitespace defaults to running", status: "  \t ", want: bgtask.StatusRunning},
		{name: "running", status: "running", want: bgtask.StatusRunning},
		{name: "started mixed case", status: "  Started ", want: bgtask.StatusRunning},
		{name: "starting", status: "starting", want: bgtask.StatusRunning},
		{name: "active", status: "ACTIVE", want: bgtask.StatusRunning},
		{name: "unknown defaults to running", status: "warming_up", want: bgtask.StatusRunning},
		{name: "pending", status: "pending", want: bgtask.StatusPending},
		{name: "queued", status: "QUEUED", want: bgtask.StatusPending},
		{name: "completed nil exit code", status: "completed", want: bgtask.StatusSucceeded},
		{name: "completed zero exit code", status: "completed", exitCode: intPtr(0), want: bgtask.StatusSucceeded},
		{name: "completed positive exit code", status: "completed", exitCode: intPtr(2), want: bgtask.StatusFailed},
		{name: "completed negative exit code", status: "completed", exitCode: intPtr(-1), want: bgtask.StatusFailed},
		{name: "succeeded zero exit code", status: "succeeded", exitCode: intPtr(0), want: bgtask.StatusSucceeded},
		{name: "exited nonzero exit code", status: "exited", exitCode: intPtr(1), want: bgtask.StatusFailed},
		{name: "done nil exit code", status: "done", want: bgtask.StatusSucceeded},
		{name: "failed even with zero exit code", status: "failed", exitCode: intPtr(0), want: bgtask.StatusFailed},
		{name: "error", status: "ERROR", want: bgtask.StatusFailed},
		{name: "killed", status: "killed", exitCode: intPtr(0), want: bgtask.StatusStopped},
		{name: "stopped", status: "stopped", want: bgtask.StatusStopped},
		{name: "cancelled", status: "cancelled", want: bgtask.StatusStopped},
		{name: "canceled", status: "canceled", want: bgtask.StatusStopped},
		{name: "terminated", status: "TERMINATED", want: bgtask.StatusStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, BackgroundProcessStatus(tc.status, tc.exitCode))
		})
	}
}

func TestBackgroundProcessTitle(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		description   string
		label         string
		command       string
		wantTitle     string
		wantIsCommand bool
	}{
		{name: "description takes precedence", description: " Dev server ", label: "srv", command: "npm run dev", wantTitle: "Dev server", wantIsCommand: false},
		{name: "label takes precedence over command", description: "  ", label: " Build watcher ", command: "make watch", wantTitle: "Build watcher", wantIsCommand: false},
		{name: "command marks TitleIsCommand", command: " go test ./... ", wantTitle: "go test ./...", wantIsCommand: true},
		{name: "fallback when all empty", description: " ", label: "\t", command: "\n", wantTitle: "Background process", wantIsCommand: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			title, isCommand := BackgroundProcessTitle(tc.description, tc.label, tc.command)
			assert.Equal(t, tc.wantTitle, title)
			assert.Equal(t, tc.wantIsCommand, isCommand)
		})
	}
}

func TestReconcileBackgroundProcessesLifecycleAndSorting(t *testing.T) {
	t.Parallel()

	var tracked map[string]struct{}

	// Empty snapshot with nil tracked map emits nothing and leaves map nil.
	assert.Nil(t, ReconcileBackgroundProcesses(&tracked, "agent-1", nil))
	assert.Nil(t, tracked)

	// Open three processes plus an empty rowKey and an untracked finished entry.
	ops := ReconcileBackgroundProcesses(&tracked, "agent-1", []BackgroundProcessSnapshot{
		{RowKey: "", Title: "ignored", Status: bgtask.StatusRunning},
		{RowKey: "bg:already-done", Title: "gone", Status: bgtask.StatusSucceeded},
		{RowKey: "bg:c", Title: "cmd c", TitleIsCommand: true, Status: bgtask.StatusRunning},
		{RowKey: "bg:a", Title: "", TitleIsCommand: true, Status: bgtask.StatusUnspecified},
		{RowKey: "bg:b", Title: "Watch build", Status: bgtask.StatusPending},
	})
	require.Len(t, ops, 3)

	require.NotNil(t, ops[0].Upsert)
	assert.Equal(t, bgtask.Upsert{
		RowKey:         "bg:c",
		Kind:           bgtask.KindShell,
		ParentAgentID:  "agent-1",
		Title:          "cmd c",
		TitleIsCommand: true,
		Status:         bgtask.StatusRunning,
	}, *ops[0].Upsert)

	require.NotNil(t, ops[1].Upsert)
	assert.Equal(t, bgtask.Upsert{
		RowKey:         "bg:a",
		Kind:           bgtask.KindShell,
		ParentAgentID:  "agent-1",
		Title:          "Background process",
		TitleIsCommand: false,
		Status:         bgtask.StatusRunning,
	}, *ops[1].Upsert)

	require.NotNil(t, ops[2].Upsert)
	assert.Equal(t, bgtask.StatusPending, ops[2].Upsert.Status)
	assert.Equal(t, map[string]struct{}{"bg:a": {}, "bg:b": {}, "bg:c": {}}, tracked)

	// Finish bg:b explicitly and omit bg:c and bg:a so they evict in sorted order.
	ops = ReconcileBackgroundProcesses(&tracked, "agent-1", []BackgroundProcessSnapshot{
		{RowKey: "bg:b", Status: bgtask.StatusFailed},
	})
	require.Len(t, ops, 3)
	assert.Equal(t, BackgroundProcessOp{RowKey: "bg:b", CloseStatus: bgtask.StatusFailed}, ops[0])
	assert.Equal(t, BackgroundProcessOp{RowKey: "bg:a", CloseStatus: bgtask.StatusEndedWithUnknownOutcome}, ops[1])
	assert.Equal(t, BackgroundProcessOp{RowKey: "bg:c", CloseStatus: bgtask.StatusEndedWithUnknownOutcome}, ops[2])
	assert.Empty(t, tracked)
}

func TestReconcileBackgroundProcessesDuplicateRowKeysAndNilTrackedPtr(t *testing.T) {
	t.Parallel()

	t.Run("active then finished in same snapshot closes once without eviction", func(t *testing.T) {
		t.Parallel()
		var tracked map[string]struct{}
		ops := ReconcileBackgroundProcesses(&tracked, "agent-1", []BackgroundProcessSnapshot{
			{RowKey: "bg:dup", Title: "npm run dev", TitleIsCommand: true, Status: bgtask.StatusRunning},
			{RowKey: "bg:dup", Title: "npm run dev", TitleIsCommand: true, Status: bgtask.StatusSucceeded},
		})
		require.Len(t, ops, 2)
		require.NotNil(t, ops[0].Upsert)
		assert.Equal(t, "bg:dup", ops[0].Upsert.RowKey)
		assert.Equal(t, BackgroundProcessOp{RowKey: "bg:dup", CloseStatus: bgtask.StatusSucceeded}, ops[1])
		assert.Empty(t, tracked)
	})

	t.Run("finished then active in same snapshot closes then re-tracks", func(t *testing.T) {
		t.Parallel()
		tracked := map[string]struct{}{"bg:dup": {}}
		ops := ReconcileBackgroundProcesses(&tracked, "agent-1", []BackgroundProcessSnapshot{
			{RowKey: "bg:dup", Status: bgtask.StatusStopped},
			{RowKey: "bg:dup", Title: "restarted", Status: bgtask.StatusRunning},
		})
		require.Len(t, ops, 2)
		assert.Equal(t, BackgroundProcessOp{RowKey: "bg:dup", CloseStatus: bgtask.StatusStopped}, ops[0])
		require.NotNil(t, ops[1].Upsert)
		assert.Equal(t, "restarted", ops[1].Upsert.Title)
		assert.Contains(t, tracked, "bg:dup")
	})

	t.Run("nil tracked pointer does not panic", func(t *testing.T) {
		t.Parallel()
		ops := ReconcileBackgroundProcesses(nil, "agent-1", []BackgroundProcessSnapshot{
			{RowKey: "bg:1", Title: "cmd", TitleIsCommand: true, Status: bgtask.StatusRunning},
		})
		require.Len(t, ops, 1)
		require.NotNil(t, ops[0].Upsert)
		assert.Equal(t, "bg:1", ops[0].Upsert.RowKey)
	})
}
