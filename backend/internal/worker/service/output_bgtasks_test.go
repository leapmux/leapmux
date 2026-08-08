package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// setupBgTaskTest provisions a worker service with one Claude-code agent and
// returns the sink, the agent_id, and a row-listing helper bound to that agent.
// Mirrors setupTodoTest.
func setupBgTaskTest(t *testing.T) (agent.OutputSink, string, func() []db.AgentBackgroundTask) {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	listRows := func() []db.AgentBackgroundTask {
		t.Helper()
		rows, err := svc.Queries.ListAgentBackgroundTasks(ctx, db.ListAgentBackgroundTasksParams{
			OwnerAgentID: "agent-1", Limit: 1000,
		})
		require.NoError(t, err)
		return rows
	}
	return sink, "agent-1", listRows
}

func TestBgTask_UpsertCreatesRowAndBroadcasts(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1",
		Kind:   bgtask.KindSubagent,
		Title:  "build feature",
		Status: bgtask.StatusRunning,
	}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "task-1", rows[0].RowKey)
	assert.Equal(t, "subagent", rows[0].Kind)
	assert.Equal(t, "build feature", rows[0].Title)
	assert.Equal(t, "running", rows[0].Status)
}

func TestBgTask_UpsertIdenticalReplaySkipsBroadcast(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// First upsert.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1",
		Kind:   bgtask.KindSubagent,
		Title:  "build feature",
		Status: bgtask.StatusRunning,
	}))
	rowsAfterFirst := listRows()
	require.Len(t, rowsAfterFirst, 1)
	updatedAt := rowsAfterFirst[0].UpdatedAt

	// Byte-identical replay must skip the DB write (updated_at unchanged).
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1",
		Kind:   bgtask.KindSubagent,
		Title:  "build feature",
		Status: bgtask.StatusRunning,
	}))
	rowsAfterReplay := listRows()
	require.Len(t, rowsAfterReplay, 1)
	assert.Equal(t, updatedAt, rowsAfterReplay[0].UpdatedAt,
		"identical replay must not rewrite the row")
}

func TestBgTask_UpdateStatusTransitions(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "x", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusRunning, "running Bash"))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "running Bash", rows[0].ActiveForm)
}

// TestBgTask_UpdateStatusTerminalStampsEndedAt verifies that a terminal status
// transition via UpdateBackgroundTaskStatus (the path providers actually call
// before CloseBackgroundTask) stamps ended_at. The old code left ended_at NULL
// forever because CloseBackgroundTask early-returned on IsTerminal().
func TestBgTask_UpdateStatusTerminalStampsEndedAt(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "x", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusCompleted, ""))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "completed", rows[0].Status)
	assert.True(t, rows[0].EndedAt.Valid, "terminal status update stamps ended_at")
}

// TestBgTask_UpdateStatusMonotonicOnTerminal verifies that a non-terminal
// status update on an already-terminal row does NOT resurrect it. A late or
// replayed task_progress (carrying Running) arriving after a close must leave
// the row terminal, or it pins the parent's thinking indicator forever.
func TestBgTask_UpdateStatusMonotonicOnTerminal(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "x", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	// A late Running update must not flip the row back.
	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusRunning, "late progress"))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "completed", rows[0].Status, "terminal row stays terminal")
	assert.True(t, rows[0].EndedAt.Valid, "ended_at stays stamped")
}

// TestBgTask_UpsertMonotonicOnTerminal verifies that a non-terminal UPSERT on
// an already-terminal row does NOT resurrect it (the replay-resurrection path
// after a worker restart, where replayed running rows hit the upsert).
func TestBgTask_UpsertMonotonicOnTerminal(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "x", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusInterrupted))
	// A replayed running upsert must not resurrect the interrupted row.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "x", Status: bgtask.StatusRunning,
	}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "interrupted", rows[0].Status, "replayed running upsert does not resurrect a terminal row")
	assert.True(t, rows[0].EndedAt.Valid, "ended_at stays stamped")
}

// TestBgTask_PartialUpsertPreservesExistingFields verifies that a partial upsert
// (one that omits fields a previous upsert set) does NOT blank them. The old
// full-row replace wiped titles/groups on a terminal output_file write. Only
// ChildAgentID was guarded; the fix extends the blank-means-keep rule to every
// descriptive field.
func TestBgTask_PartialUpsertPreservesExistingFields(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Full row: title, group, description, kind shell.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:      "task-1",
		Kind:        bgtask.KindShell,
		Title:       "run tests",
		GroupKey:    "ci",
		GroupLabel:  "CI",
		Description: "npm test",
		Status:      bgtask.StatusRunning,
	}))
	// Partial upsert: only status + description (simulating an output_file write).
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:      "task-1",
		Description: "/tmp/out.log",
		Status:      bgtask.StatusCompleted,
	}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "run tests", rows[0].Title, "title preserved from first upsert")
	assert.Equal(t, "shell", rows[0].Kind, "kind preserved")
	assert.Equal(t, "ci", rows[0].GroupKey, "group_key preserved")
	assert.Equal(t, "CI", rows[0].GroupLabel, "group_label preserved")
	assert.Equal(t, "/tmp/out.log", rows[0].Description, "description updated")
	assert.Equal(t, "completed", rows[0].Status)
}

// TestBgTask_ParentAgentIDAutoPopulated verifies the neutral sink layer fills in
// parent_agent_id from the sink's own agent identity when the provider omits it.
// Without this, the registry's parent_agent_id was always NULL.
func TestBgTask_ParentAgentIDAutoPopulated(t *testing.T) {
	t.Parallel()

	sink, agentID, listRows := setupBgTaskTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1",
		Kind:   bgtask.KindSubagent,
		Title:  "x",
		Status: bgtask.StatusRunning,
	}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, agentID, rows[0].ParentAgentID, "parent_agent_id auto-populated from the sink's own agent id")
}

func TestBgTask_CloseStampsEndedAt(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "x", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "completed", rows[0].Status)
	assert.True(t, rows[0].EndedAt.Valid, "terminal close stamps ended_at")
}

// TestBgTask_CloseIsIdempotentOnTerminalRow verifies a second close on an
// already-terminal row is a no-op (CloseAgentBackgroundTask only closes active
// rows). The ended_at and status must not change.
func TestBgTask_CloseIsIdempotentOnTerminalRow(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "x", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	rows := listRows()
	require.Len(t, rows, 1)
	endedAt := rows[0].EndedAt

	// Re-close with a different status — must not resurrect/re-close.
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusFailed))
	rows = listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "completed", rows[0].Status, "first terminal status wins")
	assert.Equal(t, endedAt, rows[0].EndedAt, "ended_at must not change on re-close")
}

func TestBgTask_CapEvictsOldestTerminal(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Seed MaxTasks rows: the first is completed (oldest terminal), the rest running.
	for i := 1; i <= bgtask.MaxTasks; i++ {
		status := bgtask.StatusRunning
		if i == 1 {
			status = bgtask.StatusCompleted
		}
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("task-%d", i),
			Kind:   bgtask.KindSubagent,
			Title:  fmt.Sprintf("task %d", i),
			Status: status,
		}))
	}
	require.Len(t, listRows(), bgtask.MaxTasks)

	// Insert one more at the cap: task-1 (completed) should be evicted.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-new", Kind: bgtask.KindSubagent, Title: "fresh", Status: bgtask.StatusRunning,
	}))
	rows := listRows()
	require.Len(t, rows, bgtask.MaxTasks, "cap maintained")
	keys := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		keys[r.RowKey] = struct{}{}
	}
	assert.NotContains(t, keys, "task-1", "oldest terminal row evicted")
	assert.Contains(t, keys, "task-new")
}

func TestBgTask_CapNoTerminalDoesNotEvict(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Seed MaxTasks running rows — nothing for eviction to take.
	for i := 1; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("task-%d", i),
			Kind:   bgtask.KindSubagent,
			Title:  fmt.Sprintf("task %d", i),
			Status: bgtask.StatusRunning,
		}))
	}
	require.Len(t, listRows(), bgtask.MaxTasks)

	// Insert one more: no terminal row to evict -> the new row is dropped.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-dropped", Kind: bgtask.KindSubagent, Title: "dropped", Status: bgtask.StatusRunning,
	}))
	rows := listRows()
	require.Len(t, rows, bgtask.MaxTasks, "no eviction when no terminal row exists")
	keys := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		keys[r.RowKey] = struct{}{}
	}
	assert.NotContains(t, keys, "task-dropped")
}

func TestBgTask_LoadSeedsCacheFromDB(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	// Seed a row directly in the DB (cold-start path).
	require.NoError(t, svc.Queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: "agent-1", RowKey: "seeded", Seq: 1,
		Kind: "subagent", Title: "seeded row", Status: "running",
	}))
	items, err := svc.Output.LoadBackgroundTasks(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "seeded", items[0].RowKey)
	assert.Equal(t, "seeded row", items[0].Title)
}

func TestBgTask_MarkExitedStoppedVsInterrupted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	listRows := func() []db.AgentBackgroundTask {
		t.Helper()
		rows, err := svc.Queries.ListAgentBackgroundTasks(ctx, db.ListAgentBackgroundTasksParams{
			OwnerAgentID: "agent-1", Limit: 1000,
		})
		require.NoError(t, err)
		return rows
	}
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "a", Kind: bgtask.KindSubagent, Title: "a", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "b", Kind: bgtask.KindSubagent, Title: "b", Status: bgtask.StatusRunning,
	}))

	// Mark all active rows exited as stopped (a user-initiated close path).
	svc.Output.MarkAgentBackgroundTasksExited("agent-1", true)
	rows := listRows()
	require.Len(t, rows, 2)
	for _, r := range rows {
		assert.Equal(t, "stopped", r.Status, "stopped=true maps to StatusStopped")
		assert.True(t, r.EndedAt.Valid)
	}

	// Reset with fresh rows and mark interrupted (a worker restart).
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "c", Kind: bgtask.KindSubagent, Title: "c", Status: bgtask.StatusRunning,
	}))
	svc.Output.MarkAgentBackgroundTasksExited("agent-1", false)
	rows = listRows()
	var cRow *db.AgentBackgroundTask
	for i := range rows {
		if rows[i].RowKey == "c" {
			cRow = &rows[i]
			break
		}
	}
	require.NotNil(t, cRow)
	assert.Equal(t, "interrupted", cRow.Status, "stopped=false maps to StatusInterrupted")
}

func TestBgTask_CleanupDropsCache(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "x", Status: bgtask.StatusRunning,
	}))
	// The cache is seeded now.
	_, loaded := svc.Output.bgtasks.Load("agent-1")
	assert.True(t, loaded, "cache populated after upsert")

	svc.Output.CleanupAgent("agent-1")
	_, loaded = svc.Output.bgtasks.Load("agent-1")
	assert.False(t, loaded, "cache dropped after CleanupAgent")
}

// TestUpsertBackgroundTaskSanitizesRowKey verifies the neutral layer strips
// control characters from provider-supplied row keys before they reach the DB
// (Cursor toolCallIds can contain an embedded newline).
func TestUpsertBackgroundTaskSanitizesRowKey(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Embedded newline (Cursor's observed toolCallId quirk).
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "call-abc\nfc-def",
		Kind:   bgtask.KindSubagent,
		Title:  "x",
		Status: bgtask.StatusRunning,
	}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "call-abcfc-def", rows[0].RowKey, "newline stripped in the neutral layer")
	assert.NotContains(t, rows[0].RowKey, "\n")

	// Update + close must also key on the sanitized form.
	require.NoError(t, sink.UpdateBackgroundTaskStatus("call-abc\nfc-def", bgtask.StatusRunning, "working"))
	require.NoError(t, sink.CloseBackgroundTask("call-abc\nfc-def", bgtask.StatusCompleted))
	rows = listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "completed", rows[0].Status)
	assert.Equal(t, "working", rows[0].ActiveForm)
}
