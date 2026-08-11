package service

import (
	"context"
	"fmt"
	"slices"
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
		rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(ctx, db.ListAgentBackgroundTasksNewestFirstParams{
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

func TestBgTask_CapNoTerminalEvictsOldestActive(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Seed MaxTasks running rows — nothing terminal for eviction to take.
	for i := 1; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("task-%d", i),
			Kind:   bgtask.KindSubagent,
			Title:  fmt.Sprintf("task %d", i),
			Status: bgtask.StatusRunning,
		}))
	}
	require.Len(t, listRows(), bgtask.MaxTasks)

	// Insert one more at the cap with no terminal row: the oldest ACTIVE row
	// (task-1) is evicted so the new spawn links instead of being dropped --
	// dropping would orphan an already-created child agent row.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-new", Kind: bgtask.KindSubagent, Title: "fresh", Status: bgtask.StatusRunning,
	}))
	rows := listRows()
	require.Len(t, rows, bgtask.MaxTasks, "cap maintained")
	keys := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		keys[r.RowKey] = struct{}{}
	}
	assert.NotContains(t, keys, "task-1", "oldest active row evicted at cap with no terminal row")
	assert.Contains(t, keys, "task-new", "new row linked")
}

// TestBgTask_CapAllActiveLinkedPreservesChildLinkage verifies the cap-eviction
// path never deletes a row that carries a child_agent_id: doing so would make
// that child permanently unsteerable (the registry row is the only index from
// child id -> owner+rowKey, and the agents row survives the delete). When every
// active row is linked, the cap is exceeded instead of orphaning a child.
func TestBgTask_CapAllActiveLinkedPreservesChildLinkage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-linked",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-linked", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	// Seed MaxTasks running rows, each linked to a distinct child transcript.
	for i := 1; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey:       fmt.Sprintf("task-%d", i),
			Kind:         bgtask.KindSubagent,
			Title:        fmt.Sprintf("task %d", i),
			ChildAgentID: fmt.Sprintf("child-%d", i),
			Status:       bgtask.StatusRunning,
		}))
	}

	// Insert one more at the cap with no terminal row: every active row is
	// linked, so the cap is exceeded rather than orphaning a steerable child.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:       "task-new",
		Kind:         bgtask.KindSubagent,
		Title:        "fresh",
		ChildAgentID: "child-new",
		Status:       bgtask.StatusRunning,
	}))

	rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(ctx, db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: "agent-linked", Limit: 1000,
	})
	require.NoError(t, err)
	assert.Len(t, rows, bgtask.MaxTasks+1, "cap exceeded rather than orphaning a linked child")

	// Every child linkage survives -- none were evicted.
	for i := 1; i <= bgtask.MaxTasks; i++ {
		_, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(ctx, fmt.Sprintf("child-%d", i))
		assert.NoError(t, err, "child-%d linkage preserved (not evicted)", i)
	}
}

// TestBgTask_CapAllActiveEvictsOldestUnlinked verifies that when the cap is hit
// with no terminal row and SOME active rows are unlinked, eviction takes the
// oldest UNLINKED row (not the oldest linked one), preserving child steerability.
func TestBgTask_CapAllActiveEvictsOldestUnlinked(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// task-1: unlinked (evict candidate). task-2: linked (must survive).
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "unlinked", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-2", Kind: bgtask.KindSubagent, Title: "linked", ChildAgentID: "child-2", Status: bgtask.StatusRunning,
	}))
	// Fill the rest of the cap with linked rows.
	for i := 3; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("task-%d", i), Kind: bgtask.KindSubagent, Title: fmt.Sprintf("t%d", i),
			ChildAgentID: fmt.Sprintf("child-%d", i), Status: bgtask.StatusRunning,
		}))
	}
	require.Len(t, listRows(), bgtask.MaxTasks)

	// One more: the oldest UNLINKED row (task-1) is evicted; task-2 (linked) survives.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-new", Kind: bgtask.KindSubagent, Title: "fresh", Status: bgtask.StatusRunning,
	}))
	rows := listRows()
	require.Len(t, rows, bgtask.MaxTasks, "cap maintained by evicting the unlinked row")
	keys := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		keys[r.RowKey] = struct{}{}
	}
	assert.NotContains(t, keys, "task-1", "oldest UNLINKED row evicted")
	assert.Contains(t, keys, "task-2", "linked row preserved")
}

func TestBgTask_RenameRekeysRowAndPreservesStatus(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Open a Running row under the spawn key.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "spawn-key", Kind: bgtask.KindSubagent, Title: "spawn", Status: bgtask.StatusRunning,
	}))
	require.Len(t, listRows(), 1)

	// Rename to the stable child session id, then close it.
	require.NoError(t, sink.RenameBackgroundTask("spawn-key", "sess-stable"))
	require.NoError(t, sink.CloseBackgroundTask("sess-stable", bgtask.StatusCompleted))

	rows := listRows()
	require.Len(t, rows, 1, "rename collapsed the lifecycle to one row")
	assert.Equal(t, "sess-stable", rows[0].RowKey)
	assert.Equal(t, "completed", rows[0].Status, "status preserved across the rename")
	// The old spawn key is gone.
	for _, r := range rows {
		assert.NotEqual(t, "spawn-key", r.RowKey, "spawn key renamed away")
	}
}

func TestBgTask_RenameIsNoOpForMissingRow(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Renaming a key that was never inserted is a no-op (no row created).
	require.NoError(t, sink.RenameBackgroundTask("absent", "whatever"))
	assert.Empty(t, listRows())
}

// TestBgTask_RenameOnColdCacheRekeysDBRow verifies the rename seeds the cache
// BEFORE re-keying it: a rename arriving as the first registry touch for a root
// (after a worker restart emptied the in-memory cache) must still re-key the
// persisted DB row. Before the fix, renameRowKeyLocked ran on an empty cache,
// returned false, and the DB rename was skipped entirely -- leaking the spawn
// row Running under the old key.
func TestBgTask_RenameOnColdCacheRekeysDBRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-cold",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-cold", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "spawn-key", Kind: bgtask.KindSubagent, Title: "spawn", Status: bgtask.StatusRunning,
	}))

	// Simulate a worker restart: the DB row survives, the in-memory cache is gone.
	svc.Output.bgtasks.Delete("agent-cold")

	// A terminal update that renames spawn-key -> sess-stable is the first
	// registry touch on the fresh cache.
	require.NoError(t, sink.RenameBackgroundTask("spawn-key", "sess-stable"))

	rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(ctx, db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: "agent-cold", Limit: 1000,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1, "DB row re-keyed on a cold cache")
	assert.Equal(t, "sess-stable", rows[0].RowKey)
	assert.NotEqual(t, "spawn-key", rows[0].RowKey, "old key replaced")
}

// TestBgTask_RenameSanitizesOldKey verifies the rename sanitizes oldKey the same
// way UpsertBackgroundTask sanitizes the row key it opens. A spawn row opened
// under a control-char-carrying toolCallId (Cursor toolCallIds embed newlines)
// is stored SANITIZED; the raw toolCallId a provider passes as RenameFrom must
// be sanitized too, or the WHERE row_key = oldKey clause never matches and the
// rename silently no-ops, leaking the spawn row Running.
func TestBgTask_RenameSanitizesOldKey(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Open the spawn row under a key containing a control char. Upsert sanitizes
	// it, so the DB row is keyed by the sanitized form.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "call-abc\nfc-def", Kind: bgtask.KindSubagent, Title: "x", Status: bgtask.StatusRunning,
	}))

	// A provider passes the RAW (unsanitized) toolCallId as RenameFrom.
	require.NoError(t, sink.RenameBackgroundTask("call-abc\nfc-def", "sess-stable"))

	rows := listRows()
	require.Len(t, rows, 1, "the spawn row was re-keyed, not duplicated or leaked")
	assert.Equal(t, "sess-stable", rows[0].RowKey)
}

// TestBgTask_ShutdownCtxCancelledAfterCancelBackgroundCtx verifies the bgtask
// write context is cancelled once Service.Shutdown has called
// CancelBackgroundCtx, so an undrained in-flight write fails fast instead of
// racing sqlDB.Close().
func TestBgTask_ShutdownCtxCancelledAfterCancelBackgroundCtx(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-shutdown",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-shutdown", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	// Before cancel: a write succeeds.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "pre-shutdown", Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning,
	}))

	// Shutdown drains then cancels the bgtask context.
	svc.Output.CancelBackgroundCtx()

	// After cancel: a bgtask write fails with context.Canceled. The sink's write
	// path routes through bgTaskCtx(), which now returns the cancelled context.
	err := sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "post-shutdown", Kind: bgtask.KindSubagent, Status: bgtask.StatusRunning,
	})
	assert.ErrorIs(t, err, context.Canceled, "write after shutdown context cancel fails fast")
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
		rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(ctx, db.ListAgentBackgroundTasksNewestFirstParams{
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

// Each KIND gets its own cap pool. A run that opens shell after shell must not
// evict the finished subagents -- those are the rows that carry a transcript
// worth reopening.
func TestBgTask_CapIsPerKind(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Fill the SUBAGENT pool with terminal rows (the eviction-eligible kind).
	for i := 1; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("agent-%d", i),
			Kind:   bgtask.KindSubagent,
			Title:  fmt.Sprintf("agent %d", i),
			Status: bgtask.StatusCompleted,
		}))
	}
	require.Len(t, listRows(), bgtask.MaxTasks)

	// A shell row now has its own empty pool, so it is added WITHOUT evicting a
	// subagent -- the registry grows past what a single shared cap allowed.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "shell-1", Kind: bgtask.KindShell, Title: "npm test", Status: bgtask.StatusRunning,
	}))
	rows := listRows()
	assert.Len(t, rows, bgtask.MaxTasks+1, "the shell lands in its own pool")
	keys := map[string]struct{}{}
	for _, r := range rows {
		keys[r.RowKey] = struct{}{}
	}
	assert.Contains(t, keys, "agent-1", "no subagent evicted to make room for a shell")
	assert.Contains(t, keys, "shell-1")
}

// The shell pool evicts its own oldest terminal row once IT is full, and still
// leaves the subagent pool untouched.
func TestBgTask_ShellPoolEvictsShellsOnly(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "agent-keep", Kind: bgtask.KindSubagent, Title: "keep me", Status: bgtask.StatusCompleted,
	}))
	for i := 1; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("shell-%d", i),
			Kind:   bgtask.KindShell,
			Title:  fmt.Sprintf("cmd %d", i),
			Status: bgtask.StatusCompleted,
		}))
	}
	require.Len(t, listRows(), bgtask.MaxTasks+1)

	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "shell-new", Kind: bgtask.KindShell, Title: "fresh", Status: bgtask.StatusRunning,
	}))
	rows := listRows()
	require.Len(t, rows, bgtask.MaxTasks+1, "the shell pool stayed at its cap")
	keys := map[string]struct{}{}
	for _, r := range rows {
		keys[r.RowKey] = struct{}{}
	}
	assert.NotContains(t, keys, "shell-1", "the shell pool evicted its own oldest terminal row")
	assert.Contains(t, keys, "agent-keep", "the subagent pool is untouched")
	assert.Contains(t, keys, "shell-new")
}

// The cold-start seed must load EVERY pool. A LIMIT of just one cap would
// return one kind's rows and leave the other pool looking empty, so a reboot
// would silently re-admit rows past the cap.
func TestBgTask_SeedLoadsEveryKindPool(t *testing.T) {
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
	for i := 1; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("agent-%d", i), Kind: bgtask.KindSubagent,
			Title: "a", Status: bgtask.StatusCompleted,
		}))
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("shell-%d", i), Kind: bgtask.KindShell,
			Title: "s", Status: bgtask.StatusCompleted,
		}))
	}

	// Drop the warm cache so the next read seeds from the DB.
	svc.Output.CleanupAgent("agent-1")
	loaded, err := svc.Output.LoadBackgroundTasks(ctx, "agent-1")
	require.NoError(t, err)
	assert.Len(t, loaded, bgtask.MaxTasksTotal, "the seed covers both pools")
}

// The cap is a SOFT bound: applyBackgroundTaskUpsertLocked exceeds it rather
// than orphan a steerable child, so an owner can hold more rows than
// MaxTasksTotal. The seed must then keep the NEWEST rows.
//
// An oldest-first LIMIT keeps the finished rows and drops the live subagents,
// so a restart shows a registry of stale completed rows with the running work
// missing -- and derives nextSeq from a truncated maximum, which then collides
// with a surviving seq under UNIQUE (owner_agent_id, seq) and wedges every
// later insert for that owner.
func TestBgTask_SeedKeepsTheNewestRowsPastTheCap(t *testing.T) {
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

	// Fill the subagent pool with ACTIVE rows that each carry a child, which is
	// the state that makes the cap soft: eviction refuses to orphan them. Go
	// past MaxTasksTotal so the seed's LIMIT genuinely has to choose.
	const overflow = 8
	newest := fmt.Sprintf("agent-%03d", bgtask.MaxTasksTotal+overflow)
	for i := 1; i <= bgtask.MaxTasksTotal+overflow; i++ {
		rowKey := fmt.Sprintf("agent-%03d", i)
		_, err := sink.EnsureChildAgent(fmt.Sprintf("span-%03d", i), rowKey, "SCAN")
		require.NoError(t, err)
	}
	total, err := svc.Output.LoadBackgroundTasks(ctx, "agent-1")
	require.NoError(t, err)
	require.Greater(t, len(total), bgtask.MaxTasksTotal,
		"the soft cap must actually be exceeded, or this test proves nothing")

	// Drop the warm cache so the next read seeds from the DB.
	svc.Output.CleanupAgent("agent-1")
	loaded, err := svc.Output.LoadBackgroundTasks(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, loaded, bgtask.MaxTasksTotal)

	keys := make([]string, len(loaded))
	for i, r := range loaded {
		keys[i] = r.RowKey
	}
	assert.Equal(t, newest, keys[len(keys)-1], "the newest row survives the seed")
	assert.NotContains(t, keys, "agent-001", "the oldest row is the one dropped")
	assert.True(t, slices.IsSorted(keys), "the seed restores ascending seq order")

	// nextSeq must clear every surviving seq, including the rows the seed did
	// not load. A collision here fails the UNIQUE (owner_agent_id, seq) index
	// and wedges the registry for this owner permanently.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "agent-after-reseed", Kind: bgtask.KindSubagent,
		Title: "post-restart", Status: bgtask.StatusRunning,
	}))
}
