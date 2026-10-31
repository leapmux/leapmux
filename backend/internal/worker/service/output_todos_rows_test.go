package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

func TestOutputTodosUnchangedUpsertDoesNotStartATransaction(t *testing.T) {
	t.Parallel()
	svc := setupTodoRowTest(t)
	item := todoevents.Item{
		ID: "existing", Content: "Keep this task", ActiveForm: "Keeping this task",
		Description: "Existing detail", Status: todoevents.StatusPending,
	}
	_, _, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: todoevents.KindCreate, Item: item})
	require.NoError(t, err)
	cache := svc.Output.todoCache("agent-1")
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	before, nextSeq := cache.snapshot(), cache.nextSeq
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reg := cache.on(svc.Queries, "agent-1")
	for _, event := range []todoevents.Event{
		{Kind: todoevents.KindCreate},
		{Kind: todoevents.KindDetail},
		{Kind: todoevents.KindCreate, Item: item},
		{Kind: todoevents.KindDetail, Item: todoevents.Item{ID: item.ID}},
		{Kind: todoevents.KindMerge},
		{Kind: todoevents.KindMerge, Items: []todoevents.Item{item, item}},
	} {
		items, changed, err := applyTodoEventLocked(ctx, reg, svc.Output.db, event)
		assert.NoError(t, err, "an unchanged event needs no transaction, even when its context is cancelled")
		assert.False(t, changed)
		assert.Equal(t, todoItems(before), items)
		assert.Equal(t, before, cache.Rows)
		assert.Equal(t, nextSeq, cache.nextSeq)
	}
	_, changed, err := applyTodoEventLocked(ctx, reg, svc.Output.db, todoevents.Event{
		Kind: todoevents.KindDetail, Item: todoevents.Item{ID: item.ID, Content: "A real mutation"},
	})
	assert.ErrorIs(t, err, context.Canceled, "a real mutation must still use its persistence context")
	assert.False(t, changed)
	assert.Equal(t, before, cache.Rows)
	assert.Equal(t, nextSeq, cache.nextSeq)
}

func TestOutputTodosUseTheSameStatusBeforeAndAfterColdSeeding(t *testing.T) {
	t.Parallel()
	for _, kind := range []todoevents.EventKind{
		todoevents.KindCreate, todoevents.KindDetail, todoevents.KindMerge, todoevents.KindSnapshot,
	} {
		t.Run(fmt.Sprintf("event %d", kind), func(t *testing.T) {
			svc := setupTodoRowTest(t)
			item := todoevents.Item{ID: "unreported", Content: "No status was reported"}
			event := todoevents.Event{Kind: kind, Item: item, Items: []todoevents.Item{item}, Snapshot: []todoevents.Item{item}}
			items, changed, err := svc.Output.applyTodoEvent("agent-1", event)
			require.NoError(t, err)
			require.True(t, changed)
			require.Len(t, items, 1)
			assert.Equal(t, todoevents.StatusPending, items[0].Status)
			assert.Equal(t, leapmuxv1.TodoStatus_TODO_STATUS_PENDING, items[0].ToProto().GetStatus())
			warm, err := svc.Output.LoadTodos(context.Background(), "agent-1")
			require.NoError(t, err)
			svc.Output.todos.Delete("agent-1")
			cold, err := svc.Output.LoadTodos(context.Background(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, cold, warm)
			assert.Equal(t, todoevents.StatusUnspecified, event.Item.Status, "normalization must not mutate the input event")
			assert.Equal(t, todoevents.StatusUnspecified, event.Snapshot[0].Status)
			cache := svc.Output.todoCache("agent-1")
			cache.Mu.Lock()
			defer cache.Mu.Unlock()
			beforeSeq := cache.nextSeq
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, changed, err = applyTodoEventLocked(ctx, cache.on(svc.Queries, "agent-1"), svc.DB, event)
			assert.NoError(t, err, "an identical event remains unchanged after a cold seed")
			assert.False(t, changed)
			assert.Equal(t, beforeSeq, cache.nextSeq)
		})
	}
}

func TestReduceTodoUpdateResolvesAnExplicitUnspecifiedStatus(t *testing.T) {
	status := todoevents.StatusUnspecified
	rows := []cachedTodo{{seq: 1, item: todoevents.Item{ID: "task", Status: todoevents.StatusInProgress}}}
	mutation, changed := reduceTodoUpdate(rows, todoevents.Event{
		Kind: todoevents.KindUpdate, ID: "task", Patch: todoevents.Patch{Status: &status},
	})
	require.True(t, changed)
	assert.Equal(t, todoevents.StatusPending, mutation.item.Status)
	assert.Equal(t, todoevents.StatusInProgress, rows[0].item.Status)
}

func TestTodoDetailWithoutStatusPreservesEveryExistingStatus(t *testing.T) {
	t.Parallel()
	for _, status := range []todoevents.Status{
		todoevents.StatusPending, todoevents.StatusInProgress, todoevents.StatusCompleted,
		todoevents.StatusDeleted, todoevents.StatusBlocked,
	} {
		t.Run(status.String(), func(t *testing.T) {
			svc := setupTodoRowTest(t)
			_, _, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{
				Kind: todoevents.KindCreate, Item: todoevents.Item{ID: "task", Content: "Before", Status: status},
			})
			require.NoError(t, err)
			event := todoevents.Event{Kind: todoevents.KindDetail, Item: todoevents.Item{ID: "task", Content: "After"}}
			items, changed, err := svc.Output.applyTodoEvent("agent-1", event)
			require.NoError(t, err)
			require.True(t, changed)
			require.Len(t, items, 1)
			assert.Equal(t, status, items[0].Status)
			assert.Equal(t, "After", items[0].Content)
			stored := todoStatusesBySeq(t, svc)
			assert.Equal(t, leapmuxv1.TodoStatus(status), stored[1])
			items, changed, err = svc.Output.applyTodoEvent("agent-1", event)
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Equal(t, status, items[0].Status)
		})
	}
}

func TestTodoUpdateStoresAndBroadcastsTheResolvedStatus(t *testing.T) {
	t.Parallel()
	svc := setupTodoRowTest(t)
	_, _, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{
		Kind: todoevents.KindCreate, Item: todoevents.Item{ID: "task", Status: todoevents.StatusInProgress},
	})
	require.NoError(t, err)
	status := todoevents.StatusUnspecified
	items, changed, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{
		Kind: todoevents.KindUpdate, ID: "task", Patch: todoevents.Patch{Status: &status},
	})
	require.NoError(t, err)
	require.True(t, changed)
	require.Len(t, items, 1)
	assert.Equal(t, todoevents.StatusPending, items[0].Status)
	assert.Equal(t, leapmuxv1.TodoStatus_TODO_STATUS_PENDING, items[0].ToProto().GetStatus())
	assert.Equal(t, leapmuxv1.TodoStatus_TODO_STATUS_PENDING, todoStatusesBySeq(t, svc)[1])
}

// setupTodoRowTest provisions a worker service with one agent, "agent-1", for
// the tests that drive the to-do registry through applyTodoEvent and inspect
// the agent_todos rows behind it.
func setupTodoRowTest(t *testing.T) *Service {
	t.Helper()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	return svc
}

// todoTableItems lists agent-1's stored rows in ascending seq order as
// (seq, task id, content) triples.
func todoTableItems(t *testing.T, svc *Service) []string {
	t.Helper()
	rows, err := svc.Queries.ListAgentTodosNewestFirst(context.Background(), db.ListAgentTodosNewestFirstParams{AgentID: "agent-1", Limit: 1000})
	require.NoError(t, err)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[len(rows)-1-i] = fmt.Sprintf("%d/%s/%s", r.Seq, r.TaskID, r.Content)
	}
	return out
}

// fillTodosToTheCap creates MaxTodos to-dos through events. The first one is
// completed, so it is the one row a cap eviction may take.
func fillTodosToTheCap(t *testing.T, svc *Service) {
	t.Helper()
	for i := range todoevents.MaxTodos {
		status := todoevents.StatusInProgress
		if i == 0 {
			status = todoevents.StatusCompleted
		}
		_, _, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: todoevents.KindCreate, Item: todoevents.Item{
			ID: fmt.Sprintf("t%d", i), Content: fmt.Sprintf("task %d", i), Status: status,
		}})
		require.NoError(t, err)
	}
}

// TestOutputTodos_SnapshotIDShapedLikeASyntheticKeyStores is the regression
// test for a key collision. A snapshot row without a provider id used to be
// stored under the synthetic key "snap-<position>", so a provider id spelled
// "snap-1" in the second position collided with the first row's key, and the
// whole snapshot failed to store.
func TestOutputTodos_SnapshotIDShapedLikeASyntheticKeyStores(t *testing.T) {
	t.Parallel()
	svc := setupTodoRowTest(t)

	_, mutated, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: []todoevents.Item{
		{ID: "", Content: "no provider id", Status: todoevents.StatusPending},
		{ID: "snap-1", Content: "provider id snap-1", Status: todoevents.StatusPending},
	}})
	require.NoError(t, err)
	assert.True(t, mutated)
	assert.Equal(t, []string{"1//no provider id", "2/snap-1/provider id snap-1"}, todoTableItems(t, svc))
}

// TestOutputTodos_FailedInsertAfterEvictionRestoresTheEvictedRow is the
// regression test for a create that was not one unit. At the cap, a new
// to-do evicts the oldest finished row first. The create ran no transaction,
// so when the insert then failed, the eviction's DELETE stayed committed: an
// unrelated to-do was gone from the table and the cache, and no client heard.
func TestOutputTodos_FailedInsertAfterEvictionRestoresTheEvictedRow(t *testing.T) {
	t.Parallel()
	for _, kind := range []todoevents.EventKind{todoevents.KindCreate, todoevents.KindDetail} {
		t.Run(fmt.Sprintf("event %d", kind), func(t *testing.T) {
			svc := setupTodoRowTest(t)
			fillTodosToTheCap(t, svc)
			before := todoTableItems(t, svc)
			cachedBefore, err := svc.Output.LoadTodos(context.Background(), "agent-1")
			require.NoError(t, err)
			_, beforeSeq := todoCacheItems(t, svc)
			_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_new_todo BEFORE INSERT ON agent_todos
WHEN NEW.task_id = 'new' BEGIN SELECT RAISE(ABORT, 'to-do insert refused'); END`)
			require.NoError(t, err)

			_, _, err = svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: kind, Item: todoevents.Item{
				ID: "new", Content: "new task", Status: todoevents.StatusPending,
			}})
			require.ErrorContains(t, err, "to-do insert refused")

			assert.Equal(t, before, todoTableItems(t, svc), "the evicted row must be back in the table")
			cachedAfter, err := svc.Output.LoadTodos(context.Background(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, cachedBefore, cachedAfter, "the evicted row must be back in the cache")
			_, afterSeq := todoCacheItems(t, svc)
			assert.Equal(t, beforeSeq, afterSeq)
		})
	}
}

// todoCacheItems lists agent-1's cached rows in cache order, in the same
// (seq, task id, content) form as todoTableItems, and the cache's next seq.
func todoCacheItems(t *testing.T, svc *Service) ([]string, int64) {
	t.Helper()
	_, err := svc.Output.LoadTodos(context.Background(), "agent-1")
	require.NoError(t, err)
	cache := svc.Output.todoCache("agent-1")
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	out := make([]string, len(cache.Rows))
	for i, r := range cache.Rows {
		out[i] = fmt.Sprintf("%d/%s/%s", r.seq, r.item.ID, r.item.Content)
	}
	return out, cache.nextSeq
}

// todoStatusesBySeq reads agent-1's stored status of each row, by seq.
func todoStatusesBySeq(t *testing.T, svc *Service) map[int64]leapmuxv1.TodoStatus {
	t.Helper()
	rows, err := svc.Queries.ListAgentTodosNewestFirst(context.Background(), db.ListAgentTodosNewestFirstParams{AgentID: "agent-1", Limit: 1000})
	require.NoError(t, err)
	out := make(map[int64]leapmuxv1.TodoStatus, len(rows))
	for _, r := range rows {
		out[r.Seq] = r.Status
	}
	return out
}

// TestOutputTodos_SnapshotRowEvictionDeletesBySeq pins that a cap eviction
// deletes the row it chose, by seq. Two snapshot rows without a task id and
// with identical content are indistinguishable by anything but their seq.
func TestOutputTodos_SnapshotRowEvictionDeletesBySeq(t *testing.T) {
	t.Parallel()
	svc := setupTodoRowTest(t)
	snapshot := make([]todoevents.Item, todoevents.MaxTodos)
	for i := range snapshot {
		snapshot[i] = todoevents.Item{Content: fmt.Sprintf("work %d", i), Status: todoevents.StatusInProgress}
	}
	snapshot[0] = todoevents.Item{Content: "twin", Status: todoevents.StatusCompleted}
	snapshot[1] = todoevents.Item{Content: "twin", Status: todoevents.StatusCompleted}
	_, _, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: snapshot})
	require.NoError(t, err)

	_, mutated, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: todoevents.KindCreate, Item: todoevents.Item{
		ID: "fresh", Content: "fresh task", Status: todoevents.StatusPending,
	}})
	require.NoError(t, err)
	assert.True(t, mutated)

	table := todoTableItems(t, svc)
	require.Len(t, table, todoevents.MaxTodos)
	assert.Equal(t, "2//twin", table[0], "seq 1 is gone, and its identical twin at seq 2 survives")
	assert.Equal(t, fmt.Sprintf("%d/fresh/fresh task", todoevents.MaxTodos+1), table[len(table)-1],
		"the new row takes the next seq and its task id")
	cached, _ := todoCacheItems(t, svc)
	assert.Equal(t, table, cached, "the cache mirrors the table")
}

// TestOutputTodos_ProviderIDUpdateAddressesTheRowByTaskID pins that an update
// resolves a task id to its row's seq, both while a row's cache position and
// seq agree and after an eviction makes them differ.
func TestOutputTodos_ProviderIDUpdateAddressesTheRowByTaskID(t *testing.T) {
	t.Parallel()
	completed := todoevents.StatusCompleted

	t.Run("a snapshot with one provider id", func(t *testing.T) {
		t.Parallel()
		svc := setupTodoRowTest(t)
		_, _, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: []todoevents.Item{
			{Content: "a", Status: todoevents.StatusPending},
			{ID: "t-b", Content: "b", Status: todoevents.StatusPending},
			{Content: "c", Status: todoevents.StatusPending},
		}})
		require.NoError(t, err)

		_, mutated, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{
			Kind: todoevents.KindUpdate, ID: "t-b", Patch: todoevents.Patch{Status: &completed},
		})
		require.NoError(t, err)
		assert.True(t, mutated)
		pending := leapmuxv1.TodoStatus(todoevents.StatusPending)
		assert.Equal(t, map[int64]leapmuxv1.TodoStatus{1: pending, 2: leapmuxv1.TodoStatus(completed), 3: pending}, todoStatusesBySeq(t, svc))
	})

	t.Run("after an eviction moves the row", func(t *testing.T) {
		t.Parallel()
		svc := setupTodoRowTest(t)
		snapshot := make([]todoevents.Item, todoevents.MaxTodos)
		for i := range snapshot {
			snapshot[i] = todoevents.Item{Content: fmt.Sprintf("work %d", i), Status: todoevents.StatusInProgress}
		}
		snapshot[0] = todoevents.Item{Content: "done", Status: todoevents.StatusCompleted}
		snapshot[1] = todoevents.Item{ID: "t-b", Content: "b", Status: todoevents.StatusInProgress}
		_, _, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: todoevents.KindSnapshot, Snapshot: snapshot})
		require.NoError(t, err)
		_, _, err = svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: todoevents.KindCreate, Item: todoevents.Item{
			ID: "fresh", Content: "fresh", Status: todoevents.StatusPending,
		}})
		require.NoError(t, err)
		cached, _ := todoCacheItems(t, svc)
		require.Equal(t, "2/t-b/b", cached[0], "the eviction moved t-b to cache position 0, while its seq stays 2")

		_, mutated, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{
			Kind: todoevents.KindUpdate, ID: "t-b", Patch: todoevents.Patch{Status: &completed},
		})
		require.NoError(t, err)
		assert.True(t, mutated)
		statuses := todoStatusesBySeq(t, svc)
		assert.Equal(t, leapmuxv1.TodoStatus(completed), statuses[2], "the row at seq 2 is t-b's")
		for seq, status := range statuses {
			if seq != 2 {
				assert.NotEqual(t, leapmuxv1.TodoStatus(completed), status, "seq %d must not change", seq)
			}
		}
	})
}

// TestOutputTodos_DuplicateTaskIDInsertFailsLoudly pins the plain INSERT. A row
// that holds a task id the cache does not know is a divergence between the two,
// and the task-id index makes the insert fail instead of absorbing it. The
// failure comes after the cap eviction, and the create rolls both back.
func TestOutputTodos_DuplicateTaskIDInsertFailsLoudly(t *testing.T) {
	t.Parallel()
	svc := setupTodoRowTest(t)
	fillTodosToTheCap(t, svc)
	_, nextSeq := todoCacheItems(t, svc)
	_, err := svc.DB.ExecContext(t.Context(),
		`INSERT INTO agent_todos (agent_id, seq, task_id, content, status) VALUES ('agent-1', ?, 'behind-the-cache', 'hidden', 1)`,
		nextSeq+100)
	require.NoError(t, err)
	tableBefore := todoTableItems(t, svc)
	cacheBefore, nextSeqBefore := todoCacheItems(t, svc)

	_, _, err = svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: todoevents.KindCreate, Item: todoevents.Item{
		ID: "behind-the-cache", Content: "visible", Status: todoevents.StatusPending,
	}})
	require.ErrorContains(t, err, "UNIQUE constraint failed: agent_todos.agent_id, agent_todos.task_id")

	assert.Equal(t, tableBefore, todoTableItems(t, svc), "the table is as it was, the evictable row included")
	cacheAfter, nextSeqAfter := todoCacheItems(t, svc)
	assert.Equal(t, cacheBefore, cacheAfter, "the cache is as it was, the evictable row included")
	assert.Equal(t, nextSeqBefore, nextSeqAfter)
}

// TestOutputTodos_UpdateOfAVanishedRowFails pins the 0-row check. The cache
// mirrors the table, so an update that matches no row means the two diverged,
// and the update fails rather than change only the cache.
func TestOutputTodos_UpdateOfAVanishedRowFails(t *testing.T) {
	t.Parallel()
	svc := setupTodoRowTest(t)
	_, _, err := svc.Output.applyTodoEvent("agent-1", todoevents.Event{Kind: todoevents.KindCreate, Item: todoevents.Item{
		ID: "t0", Content: "task", Status: todoevents.StatusPending,
	}})
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), `DELETE FROM agent_todos WHERE agent_id = 'agent-1'`)
	require.NoError(t, err)

	completed := todoevents.StatusCompleted
	_, _, err = svc.Output.applyTodoEvent("agent-1", todoevents.Event{
		Kind: todoevents.KindUpdate, ID: "t0", Patch: todoevents.Patch{Status: &completed},
	})
	require.ErrorContains(t, err, "agent_todos row agent-1/1 is gone")
	items, err := svc.Output.LoadTodos(context.Background(), "agent-1")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, todoevents.StatusPending, items[0].Status, "the cached item does not change")
}
