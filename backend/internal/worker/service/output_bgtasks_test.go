package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// setupBgTaskTest provisions a worker service with one Claude-code agent and
// returns the sink, the agent_id, and a row-listing helper bound to that agent.
// Mirrors setupTodoTest.
func setupBgTaskTest(t *testing.T) (agent.OutputSink, string, func() []db.AgentBackgroundTask) {
	t.Helper()
	_, sink, ownerID, listRows := setupBgTaskTestWithService(t)
	return sink, ownerID, listRows
}

// setupBgTaskTestWithService is setupBgTaskTest for a test that also reads the
// DISPLAY list, which only the service can answer. The two lists are no longer
// the same: the cap limits what LoadBackgroundTasks returns, and a row that
// carries a child transcript stays in the table after it leaves that list, so
// listRows (the table) and displayedRowKeys (the list) disagree by design.
func setupBgTaskTestWithService(t *testing.T) (*Service, agent.OutputSink, string, func() []db.AgentBackgroundTask) {
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
	return svc, sink, "agent-1", listRows
}

// displayedRowKeys returns the row keys of the capped display list -- what the
// sidebar shows and what a client receives -- in cache order.
func displayedRowKeys(t *testing.T, svc *Service, ownerID string) []string {
	t.Helper()
	items, err := svc.Output.LoadBackgroundTasks(context.Background(), ownerID)
	require.NoError(t, err)
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = item.RowKey
	}
	return keys
}

// rowKeySet collapses persisted rows to a set of their keys for a Contains /
// NotContains assertion.
func rowKeySet(rows []db.AgentBackgroundTask) map[string]struct{} {
	keys := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		keys[r.RowKey] = struct{}{}
	}
	return keys
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

// TestBgTask_UpdateStatusFinalStampsEndedAt verifies that a final status
// transition via UpdateBackgroundTaskStatus (the path providers actually call
// before CloseBackgroundTask) stamps ended_at. The old code left ended_at NULL
// forever because CloseBackgroundTask early-returned on IsFinished().
func TestBgTask_UpdateStatusFinalStampsEndedAt(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "x", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusCompleted, ""))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "completed", rows[0].Status)
	assert.True(t, rows[0].EndedAt.Valid, "final status update stamps ended_at")
}

// TestBgTask_UpdateStatusMonotonicOnFinal verifies that a non-final
// status update on an already-finished row does NOT resurrect it. A late or
// replayed task_progress (carrying Running) arriving after a close must leave
// the row final, or it pins the parent's thinking indicator forever.
func TestBgTask_UpdateStatusMonotonicOnFinal(t *testing.T) {
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
	assert.Equal(t, "completed", rows[0].Status, "finished row stays finished")
	assert.True(t, rows[0].EndedAt.Valid, "ended_at stays stamped")
}

// TestBgTask_UpsertMonotonicOnFinal verifies that a non-final UPSERT on
// an already-finished row does NOT resurrect it (the replay-resurrection path
// after a worker restart, where replayed running rows hit the upsert).
func TestBgTask_UpsertMonotonicOnFinal(t *testing.T) {
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
	assert.Equal(t, "interrupted", rows[0].Status, "replayed running upsert does not resurrect a finished row")
	assert.True(t, rows[0].EndedAt.Valid, "ended_at stays stamped")
}

// TestBgTask_PartialUpsertPreservesExistingFields verifies that a partial upsert
// (one that omits fields a previous upsert set) does NOT blank them. The old
// full-row replace wiped titles/groups on a final-status output_file write. Only
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
	assert.True(t, rows[0].EndedAt.Valid, "final close stamps ended_at")
}

// TestBgTask_CloseIsIdempotentOnFinishedRow verifies a second close on an
// already-finished row is a no-op (CloseAgentBackgroundTask only closes active
// rows). The ended_at and status must not change.
func TestBgTask_CloseIsIdempotentOnFinishedRow(t *testing.T) {
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
	assert.Equal(t, "completed", rows[0].Status, "first final status wins")
	assert.Equal(t, endedAt, rows[0].EndedAt, "ended_at must not change on re-close")
}

func TestBgTask_CapEvictsOldestFinished(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Seed MaxTasks rows: the first is completed (oldest finished), the rest running.
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
	assert.NotContains(t, keys, "task-1", "oldest finished row evicted")
	assert.Contains(t, keys, "task-new")
}

func TestBgTask_CapNoFinishedRowEvictsOldestActive(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Seed MaxTasks running rows — no finished row for eviction to take.
	for i := 1; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("task-%d", i),
			Kind:   bgtask.KindSubagent,
			Title:  fmt.Sprintf("task %d", i),
			Status: bgtask.StatusRunning,
		}))
	}
	require.Len(t, listRows(), bgtask.MaxTasks)

	// Insert one more at the cap with no finished row: the oldest ACTIVE row
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
	assert.NotContains(t, keys, "task-1", "oldest active row evicted at cap with no finished row")
	assert.Contains(t, keys, "task-new", "new row linked")
}

// TestBgTask_CapAllActiveLinkedHoldsTheDisplayCap verifies that a pool full of
// linked ACTIVE rows still respects the cap on the display list. The cap used to
// be exceeded here to keep a linked row's index alive; that trade is gone,
// because the row now survives eviction in the table on its own.
func TestBgTask_CapAllActiveLinkedHoldsTheDisplayCap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink, ownerID, listRows := setupBgTaskTestWithService(t)
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

	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:       "task-new",
		Kind:         bgtask.KindSubagent,
		Title:        "fresh",
		ChildAgentID: "child-new",
		Status:       bgtask.StatusRunning,
	}))

	displayed := displayedRowKeys(t, svc, ownerID)
	assert.Len(t, displayed, bgtask.MaxTasks, "the display list holds its cap")
	assert.NotContains(t, displayed, "task-1", "the oldest row left the display list")
	assert.Contains(t, displayed, "task-new")

	// The table keeps every row, so no child lost its index.
	assert.Len(t, listRows(), bgtask.MaxTasks+1, "the evicted row is retained in the table")
	for i := 1; i <= bgtask.MaxTasks; i++ {
		_, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(ctx, fmt.Sprintf("child-%d", i))
		assert.NoError(t, err, "child-%d linkage preserved", i)
	}
}

// TestBgTask_CapAllActiveEvictsTheOldestWhateverItCarries verifies the rule that
// replaced "prefer an unlinked row": at the cap with no finished row, the OLDEST
// active row leaves the display list, and its linkage alone decides whether the
// persisted row goes with it.
func TestBgTask_CapAllActiveEvictsTheOldestWhateverItCarries(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, listRows := setupBgTaskTestWithService(t)
	// task-1: linked and oldest. task-2: unlinked, and under the old rule it
	// the old rule evicted it in task-1's place.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "linked", ChildAgentID: "child-1", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-2", Kind: bgtask.KindSubagent, Title: "unlinked", Status: bgtask.StatusRunning,
	}))
	for i := 3; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("task-%d", i), Kind: bgtask.KindSubagent, Title: fmt.Sprintf("t%d", i),
			ChildAgentID: fmt.Sprintf("child-%d", i), Status: bgtask.StatusRunning,
		}))
	}
	require.Len(t, displayedRowKeys(t, svc, ownerID), bgtask.MaxTasks)

	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-new", Kind: bgtask.KindSubagent, Title: "fresh", Status: bgtask.StatusRunning,
	}))

	displayed := displayedRowKeys(t, svc, ownerID)
	require.Len(t, displayed, bgtask.MaxTasks, "the display list holds its cap")
	assert.NotContains(t, displayed, "task-1", "the OLDEST active row leaves the list, linked or not")
	assert.Contains(t, displayed, "task-2", "an unlinked younger row is not preferred as the victim")

	// task-1 left the list but keeps its row, because it carries a transcript.
	assert.Contains(t, rowKeySet(listRows()), "task-1", "a linked row is retained in the table")
	childID, status, ok, err := sink.LookupBackgroundTask("task-1")
	require.NoError(t, err)
	require.True(t, ok, "the retained row still resolves by key")
	assert.Equal(t, "child-1", childID)
	assert.Equal(t, bgtask.StatusRunning, status)
}

// A row that carries NO transcript indexes nothing, so eviction must still
// reclaim it: retention is for the linkage, not a licence to grow the table.
func TestBgTask_CapDeletesAnEvictedUnlinkedRow(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, listRows := setupBgTaskTestWithService(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "shell-1", Kind: bgtask.KindShell, Title: "npm test", Status: bgtask.StatusCompleted,
	}))
	for i := 2; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("shell-%d", i), Kind: bgtask.KindShell,
			Title: fmt.Sprintf("cmd %d", i), Status: bgtask.StatusRunning,
		}))
	}
	require.Len(t, listRows(), bgtask.MaxTasks)

	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "shell-new", Kind: bgtask.KindShell, Title: "fresh", Status: bgtask.StatusRunning,
	}))

	assert.NotContains(t, displayedRowKeys(t, svc, ownerID), "shell-1")
	assert.NotContains(t, rowKeySet(listRows()), "shell-1", "an unlinked row is deleted, not retained")
	assert.Len(t, listRows(), bgtask.MaxTasks, "the table holds the cap when nothing is retained")

	_, _, ok, err := sink.LookupBackgroundTask("shell-1")
	require.NoError(t, err)
	assert.False(t, ok, "a deleted row resolves nowhere")
}

// The linkage is what eviction must not destroy: past the cap a finished
// subagent still has to resolve its transcript, both by row key (a Claude
// SendMessage revive) and by child agent id (send-to-subagent and interrupt).
func TestBgTask_CapRetainsAnEvictedLinkedRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink, ownerID, listRows := setupBgTaskTestWithService(t)
	for i := 1; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("task-%d", i), Kind: bgtask.KindSubagent, Title: fmt.Sprintf("t%d", i),
			ChildAgentID: fmt.Sprintf("child-%d", i), Status: bgtask.StatusCompleted,
		}))
	}
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-new", Kind: bgtask.KindSubagent, Title: "fresh",
		ChildAgentID: "child-new", Status: bgtask.StatusRunning,
	}))

	require.NotContains(t, displayedRowKeys(t, svc, ownerID), "task-1",
		"the oldest finished row left the display list")

	// Route 1: row key -> child. This is what a revive reads.
	childID, status, ok, err := sink.LookupBackgroundTask("task-1")
	require.NoError(t, err)
	require.True(t, ok, "the evicted row still resolves by key")
	assert.Equal(t, "child-1", childID)
	assert.Equal(t, bgtask.StatusCompleted, status, "the retained row keeps its final status")

	// Route 2: child -> (owner, row key). This is what send and interrupt read.
	row, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(ctx, "child-1")
	require.NoError(t, err, "the reverse lookup behind send/interrupt still resolves")
	assert.Equal(t, ownerID, row.OwnerAgentID)
	assert.Equal(t, "task-1", row.RowKey)

	assert.Len(t, listRows(), bgtask.MaxTasks+1, "the table keeps the retained row")
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

// TestBgTask_RenameOntoOccupiedKeyDropsTheDuplicate covers the session-replay
// collision. (owner_agent_id, row_key) is the PRIMARY KEY, so a rename onto an
// occupied key fails the UPDATE. That is reachable: a replay re-creates the
// spawn row under the toolCallId while the pre-restart row already sits, closed,
// under the session id. The failed rename used to leave the re-created row
// Running for the life of the process, which pinned the parent's thinking
// indicator. The row already at newKey wins; the duplicate at oldKey is dropped.
func TestBgTask_RenameOntoOccupiedKeyDropsTheDuplicate(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// The completed row from before the restart, already re-keyed to the session id.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "sess-stable", Kind: bgtask.KindSubagent, Title: "spawn", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.CloseBackgroundTask("sess-stable", bgtask.StatusCompleted))
	// The replay re-creates the spawn row under the toolCallId.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "spawn-key", Kind: bgtask.KindSubagent, Title: "spawn", Status: bgtask.StatusRunning,
	}))
	require.Len(t, listRows(), 2)

	// The replayed final update renames onto the occupied key.
	require.NoError(t, sink.RenameBackgroundTask("spawn-key", "sess-stable"))

	rows := listRows()
	require.Len(t, rows, 1, "the duplicate is dropped rather than left Running")
	assert.Equal(t, "sess-stable", rows[0].RowKey)
	assert.Equal(t, "completed", rows[0].Status, "the surviving row keeps its final status")
}

// The losing duplicate leaves the display list, but its PERSISTED row goes only
// when it carries no child. A row that identifies a transcript is that child's one
// index back to (owner, row_key), and the rename is no more entitled to destroy
// it than eviction is. No provider reaches this today -- OpenCode and Kilo are
// the only renamers, and both drop child sessions over ACP -- so the invariant
// is pinned here rather than left to the callers that happen to exist.
func TestBgTask_RenameOntoOccupiedKeyRetainsALinkedDuplicate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink, ownerID, listRows := setupBgTaskTestWithService(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "sess-stable", Kind: bgtask.KindSubagent, Title: "spawn", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.CloseBackgroundTask("sess-stable", bgtask.StatusCompleted))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "spawn-key", Kind: bgtask.KindSubagent, Title: "spawn",
		ChildAgentID: "child-1", Status: bgtask.StatusRunning,
	}))

	require.NoError(t, sink.RenameBackgroundTask("spawn-key", "sess-stable"))

	assert.NotContains(t, displayedRowKeys(t, svc, ownerID), "spawn-key",
		"the duplicate leaves the display list either way")
	assert.Contains(t, rowKeySet(listRows()), "spawn-key",
		"its row is retained, because it identifies a transcript")
	row, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(ctx, "child-1")
	require.NoError(t, err, "the child keeps its index")
	assert.Equal(t, "spawn-key", row.RowKey)
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

	// A final update that renames spawn-key -> sess-stable is the first
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

// The registry's final status on a process exit rides on the ExitHandler the
// worker installs (bootstrap's SetOnExit -> Service.HandleAgentProcessExit).
// That handler is where "the process behind this work is gone" becomes a status
// on the rows, and nothing else asserted it -- so a change that stopped calling
// it would leave every in-flight subagent and shell row 'running' forever: the
// sidebar shows work that is not happening, and the parent tab keeps a thinking
// indicator that an active row is enough to pin.
func TestBgTask_ProcessExitGivesEveryActiveRowAFinalStatus(t *testing.T) {
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
	statusOf := func(rowKey string) string {
		t.Helper()
		rows, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(ctx, db.ListAgentBackgroundTasksNewestFirstParams{
			OwnerAgentID: "agent-1", Limit: 1000,
		})
		require.NoError(t, err)
		for _, r := range rows {
			if r.RowKey == rowKey {
				return r.Status
			}
		}
		t.Fatalf("no row %q", rowKey)
		return ""
	}

	// Both kinds, plus a row that already ended: a crash must end the work in
	// flight and leave the finished row's own outcome alone.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "sub", Kind: bgtask.KindSubagent, Title: "review the diff", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "shell", Kind: bgtask.KindShell, Title: "npm test", Status: bgtask.StatusPending,
	}))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "done", Kind: bgtask.KindSubagent, Title: "already finished", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.CloseBackgroundTask("done", bgtask.StatusCompleted))

	// A crash: the work did not stop, it was cut off.
	svc.HandleAgentProcessExit("agent-1", 1, errors.New("boom"), false)
	assert.Equal(t, "interrupted", statusOf("sub"), "a running subagent row ends when its process dies")
	assert.Equal(t, "interrupted", statusOf("shell"), "a queued shell row ends too -- it will never run")
	assert.Equal(t, "completed", statusOf("done"), "a row that already ended keeps its own outcome")

	// An explicit stop is a deliberate user action, not a failure.
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "later", Kind: bgtask.KindShell, Title: "sleep 60", Status: bgtask.StatusRunning,
	}))
	svc.HandleAgentProcessExit("agent-1", 0, nil, true)
	assert.Equal(t, "stopped", statusOf("later"))
	assert.Equal(t, "interrupted", statusOf("sub"), "the earlier exit's verdict is not rewritten")
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

// TestUpsertBackgroundTaskKeepsTheRowKeyVerbatim verifies that the neutral
// layer stores a provider's row key exactly as the provider wrote it, and that
// every later primitive addresses the row by that same string.
//
// The key that carries an embedded newline is Cursor's observed toolCallId
// shape. The layer used to STRIP it, which is what made the rule
// non-injective: see TestUpsertBackgroundTaskRefusesToMergeTwoKeys below.
// Readability is answered at the reader instead (rowTitle in
// BackgroundTaskList.tsx), so the identity can stay verbatim.
func TestUpsertBackgroundTaskKeepsTheRowKeyVerbatim(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	const key = "call-abc\nfc-def"
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: key,
		Kind:   bgtask.KindSubagent,
		Title:  "x",
		Status: bgtask.StatusRunning,
	}))
	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, key, rows[0].RowKey, "the key reaches the DB as the provider wrote it")

	// The update and the close address the row by the SAME string the provider
	// sent. A rewrite on one path and not the other is how a status update
	// stops finding its own row.
	require.NoError(t, sink.UpdateBackgroundTaskStatus(key, bgtask.StatusRunning, "working"))
	require.NoError(t, sink.CloseBackgroundTask(key, bgtask.StatusCompleted))
	rows = listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, "completed", rows[0].Status)
	assert.Equal(t, "working", rows[0].ActiveForm)
}

// The regression this rule exists for, end to end. Two providers' keys that a
// strip or a cap maps onto one string must stay TWO registry rows. When they
// did not, the second task overwrote the first's title and status, and one of
// the two disappeared from the sidebar with no report of why.
func TestUpsertBackgroundTaskRefusesToMergeTwoKeys(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	pairs := [][2]string{
		{"call-abc", "call-a\u200bbc"},                                         // an invisible character
		{"call-def", "call-def\n"},                                             // a trailing newline
		{"call-ghi", "call-\u202eghi"},                                         // a bidirectional override
		{strings.Repeat("k", 250) + "-one", strings.Repeat("k", 250) + "-two"}, // differ past a 256-byte cap
	}
	for i, pair := range pairs {
		for j, key := range pair {
			require.NoErrorf(t, sink.UpsertBackgroundTask(bgtask.Upsert{
				RowKey: key,
				Kind:   bgtask.KindShell,
				Title:  fmt.Sprintf("task %d-%d", i, j),
				Status: bgtask.StatusRunning,
			}), "pair %d member %d must be storable", i, j)
		}
	}

	rows := listRows()
	assert.Len(t, rows, 2*len(pairs), "each key must own a row: a merge loses one task per pair")

	// Every title survives, which is the user-visible half: a merge would have
	// left the second title on one row and dropped the first.
	titles := make([]string, 0, len(rows))
	for _, r := range rows {
		titles = append(titles, r.Title)
	}
	for i := range pairs {
		for j := range 2 {
			assert.Containsf(t, titles, fmt.Sprintf("task %d-%d", i, j),
				"the title of pair %d member %d must survive", i, j)
		}
	}
}

// An unusable key fails its own mutation and leaves every other row
// addressable. Refusing is what keeps the bound AND the injectivity: a cap
// would keep the bound by merging two keys, and no total function onto a
// bounded set is injective.
func TestUpsertBackgroundTaskRefusesAnUnusableRowKey(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "good-key",
		Kind:   bgtask.KindShell,
		Title:  "kept",
		Status: bgtask.StatusRunning,
	}))

	for _, tc := range []struct{ name, key, marker string }{
		{"past the byte limit", strings.Repeat("a", bgtask.RowKeyByteLimit+1), "must be at most"},
		{"invalid UTF-8", "call-\xffabc", "must be valid UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := sink.UpsertBackgroundTask(bgtask.Upsert{
				RowKey: tc.key,
				Kind:   bgtask.KindShell,
				Title:  "refused",
				Status: bgtask.StatusRunning,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.marker)

			assert.Error(t, sink.UpdateBackgroundTaskStatus(tc.key, bgtask.StatusRunning, "working"))
			assert.Error(t, sink.CloseBackgroundTask(tc.key, bgtask.StatusCompleted))
		})
	}

	rows := listRows()
	require.Len(t, rows, 1, "a refused key must not add a row, and must not disturb the one already there")
	assert.Equal(t, "good-key", rows[0].RowKey)
	assert.Equal(t, "kept", rows[0].Title)
}

// Each KIND gets its own cap pool. A run that opens shell after shell must not
// evict the finished subagents -- those are the rows that carry a transcript
// worth reopening.
func TestBgTask_CapIsPerKind(t *testing.T) {
	t.Parallel()

	sink, _, listRows := setupBgTaskTest(t)
	// Fill the SUBAGENT pool with finished rows (the eviction-eligible kind).
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

// The shell pool evicts its own oldest finished row once IT is full, and still
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
	assert.NotContains(t, keys, "shell-1", "the shell pool evicted its own oldest finished row")
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
	assert.Len(t, loaded, bgtask.MaxTasks*len(bgtask.Kinds), "the seed covers both pools, each to its own cap")
}

// A pool is seeded to ITS OWN cap, so a burst in one pool cannot starve another.
// Under one global window, an owner whose newest rows are all shells seeded an
// EMPTY subagent pool -- and the subagent rows are the ones carrying a
// transcript worth reopening.
func TestBgTask_SeedFillsEachPoolDespiteSkew(t *testing.T) {
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

	// A few subagents FIRST, then a full pool of newer shells on top of them.
	const subagents = 3
	for i := 1; i <= subagents; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("agent-%03d", i), Kind: bgtask.KindSubagent,
			Title: "a", Status: bgtask.StatusCompleted,
		}))
	}
	for i := 1; i <= bgtask.MaxTasks; i++ {
		require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("shell-%03d", i), Kind: bgtask.KindShell,
			Title: "s", Status: bgtask.StatusCompleted,
		}))
	}

	// Drop the warm cache so the next read seeds from the DB.
	svc.Output.CleanupAgent("agent-1")
	loaded, err := svc.Output.LoadBackgroundTasks(ctx, "agent-1")
	require.NoError(t, err)

	var gotSubagents int
	for _, r := range loaded {
		if r.Kind == bgtask.KindSubagent {
			gotSubagents++
		}
	}
	assert.Equal(t, subagents, gotSubagents,
		"every subagent row survives, although newer shells fill their own pool")
}

// The seed RECLAIMS the finished rows its window leaves behind. Eviction only
// deletes a row the cache holds, so surplus left outside the window was neither
// shown nor deleted: invisible in the sidebar, and growing without limit.
func TestBgTask_SeedReclaimsFinishedSurplus(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	// Push the shell pool past its cap with FINISHED rows, writing straight to
	// the DB so in-memory eviction does not trim them first -- which is exactly
	// the state a soft-cap overflow leaves behind across a restart.
	const overflow = 5
	now := nowMillis()
	for i := 1; i <= bgtask.MaxTasks+overflow; i++ {
		require.NoError(t, svc.Queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
			OwnerAgentID: "agent-1",
			RowKey:       fmt.Sprintf("shell-%03d", i),
			Seq:          int64(i),
			Kind:         "shell",
			Title:        "s",
			Status:       "completed",
			CreatedAt:    sqltime.NewSQLiteTime(now),
			UpdatedAt:    sqltime.NewSQLiteTime(now),
		}))
	}
	before, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(ctx, db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: "agent-1", Limit: 1000,
	})
	require.NoError(t, err)
	require.Len(t, before, bgtask.MaxTasks+overflow, "the DB holds the surplus before the seed")

	_, err = svc.Output.LoadBackgroundTasks(ctx, "agent-1")
	require.NoError(t, err)

	after, err := svc.Queries.ListAgentBackgroundTasksNewestFirst(ctx, db.ListAgentBackgroundTasksNewestFirstParams{
		OwnerAgentID: "agent-1", Limit: 1000,
	})
	require.NoError(t, err)
	assert.Len(t, after, bgtask.MaxTasks, "the seed reclaimed the finished surplus")
}

// The reclaim pass and the cap agree on what a linked row is worth. A finished
// subagent row below the seed window is the only index from its child agent id
// back to (owner, row_key), so the pass must step over it -- otherwise a restart
// deletes at boot exactly what eviction was changed to keep.
func TestBgTask_SeedReclaimSparesLinkedRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, ownerID, listRows := setupBgTaskTestWithService(t)
	// Straight to the DB, so nothing is evicted before the seed runs. Below the
	// window: one finished row that carries a child, one that does not.
	const overflow = 4
	now := nowMillis()
	for i := 1; i <= bgtask.MaxTasks+overflow; i++ {
		childID := ""
		if i%2 == 0 {
			childID = fmt.Sprintf("child-%03d", i)
		}
		require.NoError(t, svc.Queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
			OwnerAgentID: ownerID,
			RowKey:       fmt.Sprintf("task-%03d", i),
			Seq:          int64(i),
			Kind:         "subagent",
			ChildAgentID: childID,
			Title:        "t",
			Status:       "completed",
			CreatedAt:    sqltime.NewSQLiteTime(now),
			UpdatedAt:    sqltime.NewSQLiteTime(now),
		}))
	}
	require.Len(t, listRows(), bgtask.MaxTasks+overflow)

	_, err := svc.Output.LoadBackgroundTasks(ctx, ownerID)
	require.NoError(t, err)

	keys := rowKeySet(listRows())
	// task-001 .. task-004 sit below the window: the even ones are linked.
	assert.Contains(t, keys, "task-002", "a linked row below the window is spared")
	assert.Contains(t, keys, "task-004", "a linked row below the window is spared")
	assert.NotContains(t, keys, "task-001", "an unlinked row below the window is reclaimed")
	assert.NotContains(t, keys, "task-003", "an unlinked row below the window is reclaimed")
	assert.Len(t, keys, bgtask.MaxTasks+overflow/2)

	// The spared rows still answer the reverse lookup that send and interrupt use.
	row, err := svc.Queries.GetAgentBackgroundTaskByChildAgentID(ctx, "child-002")
	require.NoError(t, err)
	assert.Equal(t, "task-002", row.RowKey)
}

// The TABLE outgrows the cap, because a row that carries a child transcript
// survives eviction there. The seed must then keep the NEWEST rows.
//
// An oldest-first LIMIT keeps the finished rows and drops the live subagents,
// so a restart shows a registry of stale completed rows with the running work
// missing -- and derives nextSeq from a truncated maximum, which then collides
// with a surviving seq under UNIQUE (owner_agent_id, seq) and wedges every
// later insert for that owner.
func TestBgTask_SeedKeepsTheNewestRowsPastTheCap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink, ownerID, listRows := setupBgTaskTestWithService(t)

	// Fill the subagent pool with rows that each carry a child, which is the
	// state that outgrows the cap: eviction hides them, it does not delete them.
	// Go past the pool's cap so the seed's LIMIT genuinely has to choose.
	const overflow = 8
	newest := fmt.Sprintf("agent-%03d", bgtask.MaxTasks+overflow)
	for i := 1; i <= bgtask.MaxTasks+overflow; i++ {
		rowKey := fmt.Sprintf("agent-%03d", i)
		_, err := sink.EnsureChildAgent(fmt.Sprintf("span-%03d", i), rowKey, "SCAN")
		require.NoError(t, err)
	}
	require.Len(t, listRows(), bgtask.MaxTasks+overflow,
		"the table must actually outgrow the cap, or this test proves nothing")
	require.Len(t, displayedRowKeys(t, svc, ownerID), bgtask.MaxTasks,
		"the display list holds the cap all the same")

	// Drop the warm cache so the next read seeds from the DB.
	svc.Output.CleanupAgent("agent-1")
	loaded, err := svc.Output.LoadBackgroundTasks(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, loaded, bgtask.MaxTasks)

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

// --- Writes against a row that outlived the display cap ---
//
// The cap made the cache a PARTIAL view of the table, so every applier that
// reads `indexOf` and treats a miss as "no such row" now has a second answer to
// give: the row is retained, it is just not displayed. These three lock that
// down, one per applier.

// storedRow reads one persisted row by key, failing the test when it is absent.
// The DISPLAY list cannot answer for a retained row, which is the point of every
// test that uses this.
func storedRow(t *testing.T, svc *Service, ownerID, rowKey string) db.AgentBackgroundTask {
	t.Helper()
	row, err := svc.Queries.GetAgentBackgroundTaskByRowKey(context.Background(),
		db.GetAgentBackgroundTaskByRowKeyParams{OwnerAgentID: ownerID, RowKey: rowKey})
	require.NoError(t, err, "no persisted row %q", rowKey)
	return row
}

// A final status absorbs a non-final one wherever the row lives. A resumed session
// re-announces every task it once ran with a Running upsert, and the guard that
// drops it sits on the cached branch -- so a retained row took the replay as an
// INSERT, and the subagent's finished row came back Running with nothing left to
// close it.
func TestBgTask_ReplayedRunningUpsertCannotResurrectARetainedRow(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "SCAN",
		ChildAgentID: "child-1", Status: bgtask.StatusRunning,
	}))
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	require.NotContains(t, displayedRowKeys(t, svc, ownerID), "task-1",
		"the row leaves the display list")
	endedAt := storedRow(t, svc, ownerID, "task-1").EndedAt

	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "SCAN",
		ChildAgentID: "child-1", Status: bgtask.StatusRunning,
	}))

	row := storedRow(t, svc, ownerID, "task-1")
	assert.Equal(t, "completed", row.Status, "the replay must not resurrect the row")
	assert.Equal(t, endedAt, row.EndedAt, "ended_at survives the replay")
}

// A progress update for a retained row has to land on it. Dropping it left the
// row Running in the table for the life of the process, and a reseed then put a
// stale Running subagent back in the sidebar.
func TestBgTask_StatusUpdateReachesARetainedRow(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	_, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	require.NotContains(t, displayedRowKeys(t, svc, ownerID), "task-1",
		"the row leaves the display list")

	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusRunning, "running Bash"))

	assert.Equal(t, "running Bash", storedRow(t, svc, ownerID, "task-1").ActiveForm)
}

// The close is the half that ends the subagent, and it owes the child
// transcript its divider. A dropped close left the row Running AND the
// transcript with no ending -- until the next boot swept it, which is not the
// same run and not the same status.
func TestBgTask_CloseReachesARetainedRowAndEndsItsTranscript(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	childID, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	require.NotContains(t, displayedRowKeys(t, svc, ownerID), "task-1",
		"the row leaves the display list")

	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))

	row := storedRow(t, svc, ownerID, "task-1")
	assert.Equal(t, "completed", row.Status)
	assert.True(t, row.EndedAt.Valid, "the close stamps ended_at")

	msgs := transcriptMessages(t, svc, childID)
	require.Len(t, msgs, 1, "the child transcript gets its closing divider")
	assert.Equal(t, agent.NotificationTypeSubagentEnded, msgs[0]["type"])
	assert.Equal(t, "completed", msgs[0]["status"])
}

// The revive is the applier the whole change exists for: a Claude SendMessage
// restarts a subagent whose row left the display list long ago. Dropping it left
// the row finished while the subagent ran again, so the sidebar and the tab chip
// read "completed" for the whole second run.
func TestBgTask_ReviveReachesARetainedRow(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	_, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	require.NotContains(t, displayedRowKeys(t, svc, ownerID), "task-1",
		"the row leaves the display list")

	require.NoError(t, sink.ReviveBackgroundTask("task-1"))

	row := storedRow(t, svc, ownerID, "task-1")
	assert.Equal(t, "running", row.Status, "the subagent runs again")
	assert.False(t, row.EndedAt.Valid, "the revive clears ended_at")
}

// Re-admission is what makes a mutation reach a retained row, and it must not
// buy that by growing the list. The row comes back at the END, because a row a
// mutation just touched is the newest activity the registry has.
func TestBgTask_ReAdmittingARetainedRowHoldsTheDisplayCap(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	_, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	require.NotContains(t, displayedRowKeys(t, svc, ownerID), "task-1")

	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusRunning, "running Bash"))

	displayed := displayedRowKeys(t, svc, ownerID)
	assert.Len(t, displayed, bgtask.MaxTasks, "the cap holds across a re-admission")
	require.NotEmpty(t, displayed)
	assert.Equal(t, "task-1", displayed[len(displayed)-1],
		"the touched row is the newest entry in the list")
	assert.NotContains(t, displayed, "filler-0", "the oldest entry left to make room")
}

// A registry with no retention (agent_todos) must not gain a store fallback:
// its cap IS a storage bound, and a key it does not hold identifies nothing.
// The bgtask registry proves the other half, so this pins the nil branch of
// findRowLocked that every todo mutation takes.
func TestTodos_AnAbsentRowStaysAMissWithNoRetention(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	cache := svc.Output.todoCache("agent-1")
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	require.Nil(t, cache.ops.retention, "agent_todos retains nothing past its cap")

	_, idx, found, err := cache.findRowLocked(ctx, "agent-1", "todo-nope")
	require.NoError(t, err)
	assert.False(t, found, "no store fallback runs for a registry without retention")
	assert.Equal(t, -1, idx, "an absent row has no display index")
}

// A mutation that turns out to be a NO-OP must leave the display list exactly
// as it found it. Re-admitting a retained row first, and only then discovering
// the write changes nothing, evicted a displayed row to make space for a write
// that never happened -- and reported changed=false, so no broadcast told the
// client its list had moved. A resumed session replays one of these per past
// subagent, so the whole sidebar rotated behind the client's back.
func TestBgTask_ANoOpMutationOnARetainedRowLeavesTheDisplayListAlone(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	_, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	before := displayedRowKeys(t, svc, ownerID)
	require.NotContains(t, before, "task-1", "the row leaves the display list")

	// Absorbed by the final-status guard: the row is completed, so a running
	// update writes nothing.
	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusRunning, "running Bash"))
	// Absorbed by the already-final guard.
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))

	assert.Equal(t, before, displayedRowKeys(t, svc, ownerID),
		"a write that never happens evicts nothing and re-admits nothing")
}

// The eviction a no-op mutation used to trigger did not merely hide a row: an
// UNLINKED row that retention does not keep was DELETED from the table, so a
// duplicate progress event on one retained row destroyed an unrelated shell row.
func TestBgTask_ANoOpMutationOnARetainedRowDeletesNothing(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, listRows := setupBgTaskTestWithService(t)
	// task-1 is the OLDEST finished row, so the fill evicts it first and it ends
	// up retained-but-not-displayed. unlinked-1 is the NEXT finished row and
	// carries no child, so retention does not keep it -- it is what an eviction
	// on the re-admit path would delete from the table.
	_, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "unlinked-1", Kind: bgtask.KindSubagent, Title: "no transcript",
		Status: bgtask.StatusCompleted,
	}))
	// One row over the cap, so exactly one eviction runs and it takes task-1.
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks-1)
	require.NotContains(t, displayedRowKeys(t, svc, ownerID), "task-1")
	require.Contains(t, displayedRowKeys(t, svc, ownerID), "unlinked-1",
		"the victim is still displayed, so eviction can reach it")
	before := len(listRows())

	require.NoError(t, sink.UpdateBackgroundTaskStatus("task-1", bgtask.StatusRunning, "running Bash"))

	assert.Len(t, listRows(), before, "no persisted row is deleted for a no-op")
	assert.Equal(t, "completed", storedRow(t, svc, ownerID, "unlinked-1").Status,
		"the unlinked row an eviction would have destroyed is still there")
	assert.Equal(t, "completed", storedRow(t, svc, ownerID, "task-1").Status,
		"the absorbing guard still holds")
}

// A revive whose UPDATE matches nothing re-reads the row rather than guessing.
// The zero count answers "already active" AND "no such row", and the branch that
// assumed the first left a cache row with no table row behind it -- a subagent
// chip spinning for good. It also adopts every field, not the two a hand-written
// repair remembered: active_form and description describe the run that ENDED.
func TestBgTask_AReviveThatMatchesNothingAdoptsTheStoredRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "SCAN",
		Status: bgtask.StatusCompleted, Description: "/tmp/out.md", ActiveForm: "writing the report",
	}))
	// The cache says finished; the ROW is active. Only the DB moves, so the
	// revive's UPDATE (which filters on a final status) matches nothing.
	_, err := svc.Queries.ReviveAgentBackgroundTask(ctx, db.ReviveAgentBackgroundTaskParams{
		UpdatedAt:    sqltime.NewSQLiteTime(nowMillis()),
		OwnerAgentID: ownerID,
		RowKey:       "task-1",
	})
	require.NoError(t, err)

	require.NoError(t, sink.ReviveBackgroundTask("task-1"))

	items, err := svc.Output.LoadBackgroundTasks(ctx, ownerID)
	require.NoError(t, err)
	idx := slices.IndexFunc(items, func(i bgtask.Item) bool { return i.RowKey == "task-1" })
	require.GreaterOrEqual(t, idx, 0)
	assert.Equal(t, bgtask.StatusRunning, items[idx].Status, "the cache adopts the row's status")
	assert.Empty(t, items[idx].Description, "and the row's cleared description, not the finished run's")
	assert.Empty(t, items[idx].ActiveForm, "and the row's cleared activity text")
}

// The same branch for a row that is GONE. Reporting the drop and the snapshot in
// one composite literal built the payload before the drop ran, so the broadcast
// still carried the dead row and no later mutation corrected it.
func TestBgTask_AReviveOfADeletedRowBroadcastsWithoutIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "task-1", Kind: bgtask.KindSubagent, Title: "SCAN", Status: bgtask.StatusCompleted,
	}))
	require.Contains(t, displayedRowKeys(t, svc, ownerID), "task-1")
	// Delete the row under the cache, the way a cascade or a racing delete does.
	_, err := svc.Queries.DeleteAgentBackgroundTaskByRowKey(ctx, db.DeleteAgentBackgroundTaskByRowKeyParams{
		OwnerAgentID: ownerID, RowKey: "task-1",
	})
	require.NoError(t, err)

	require.NoError(t, sink.ReviveBackgroundTask("task-1"))

	assert.NotContains(t, displayedRowKeys(t, svc, ownerID), "task-1",
		"a row the table no longer holds leaves the display list too")
}

// A retained row that is not DISPLAYED has no cached copy to disagree with the
// store, so the zero-count repair has nothing to adopt -- and admitting it would
// evict a displayed row, and delete an unlinked one, for a write that never
// happens.
func TestBgTask_AReviveThatMatchesNothingSparesTheDisplayListForARetainedRow(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, listRows := setupBgTaskTestWithService(t)
	_, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	// The row is ACTIVE in the table, so the revive's UPDATE matches nothing.
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	before := displayedRowKeys(t, svc, ownerID)
	require.NotContains(t, before, "task-1", "the row leaves the display list")
	beforeRows := len(listRows())

	require.NoError(t, sink.ReviveBackgroundTask("task-1"))

	assert.Equal(t, before, displayedRowKeys(t, svc, ownerID), "the display list is untouched")
	assert.Len(t, listRows(), beforeRows, "and no persisted row is deleted")
}

// EnsureChildAgent asks "does a row for this key already carry a transcript",
// and a row that carries one outlives the display cap. Reading the display list
// alone answered "no" for every linked row past it, so the call fell through to
// the spawn-span lookup, missed there too, and CREATED a second transcript --
// then re-pointed the durable row at the orphan, leaving the first run's whole
// transcript unreachable. Codex reaches this on every re-registration, because a
// collab call that re-opens a closed thread supplies a NEW spawn span.
func TestBgTask_EnsureChildAgentFindsTheTranscriptOfARetainedRow(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	firstChild, err := sink.EnsureChildAgent("span-1", "thread-1", "collab child")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("thread-1", bgtask.StatusCompleted))
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	require.NotContains(t, displayedRowKeys(t, svc, ownerID), "thread-1",
		"the row leaves the display list")

	// The re-registration: a DIFFERENT spawn span for the same provider key.
	againChild, err := sink.EnsureChildAgent("span-2", "thread-1", "collab child")
	require.NoError(t, err)

	assert.Equal(t, firstChild, againChild, "the row's transcript is the one that answers")
	assert.Equal(t, firstChild, storedRow(t, svc, ownerID, "thread-1").ChildAgentID,
		"and the durable row still points at it")
}

// A re-admitted row takes a fresh seq, because the display list's slice order
// and the stored seq are ONE ordering key. Moving only the slice left the two
// disagreeing: a subagent a revive had just reopened sat at the end of the
// sidebar for the life of the process, then fell outside the next cold seed's
// window (the newest rows by seq) and vanished on the worker restart -- while
// 64 older finished rows stayed on screen.
func TestBgTask_AReAdmittedRowSurvivesTheNextColdSeed(t *testing.T) {
	t.Parallel()

	svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
	_, err := sink.EnsureChildAgent("span-1", "task-1", "SCAN")
	require.NoError(t, err)
	require.NoError(t, sink.CloseBackgroundTask("task-1", bgtask.StatusCompleted))
	fillSubagentDisplayCap(t, sink, bgtask.MaxTasks)
	require.NotContains(t, displayedRowKeys(t, svc, ownerID), "task-1",
		"the row leaves the display list")

	require.NoError(t, sink.ReviveBackgroundTask("task-1"))
	require.Contains(t, displayedRowKeys(t, svc, ownerID), "task-1",
		"the revive re-admits the row to this process's list")

	// Drop the cache, the way a worker restart does, and seed again from the DB.
	svc.Output.bgtasks.Delete(ownerID)

	assert.Contains(t, displayedRowKeys(t, svc, ownerID), "task-1",
		"the re-admitted row is inside the seed window the next process reads")
}
