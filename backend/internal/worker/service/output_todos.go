package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// agentTodoCache mirrors an agent's agent_todos rows in memory so the
// worker can build the post-mutation broadcast without re-fetching after each
// event, and pick the next seq for an INSERT without a pre-SELECT round-trip.
// Initialized lazily from the DB on first touch per agent; the cache and DB
// stay in lock-step because every successful write goes through applyTodoEvent.
//
// It is an alias for the shared registryCache mechanics (seed/snapshot/index/
// evict), with the type-specific behaviour supplied by todoOps. The todo-
// specific dual-key lookups (indexByID by task id, itemsEqual for snapshot
// no-op detection, todoItems projection) live as free functions below; the
// cache's own mutex (registryCache.Mu) serializes the multi-step
// "check existence -> DB write -> in-place mutation" sequences and guards the
// lazy DB seed.
type agentTodoCache = registryCache[cachedTodo, int64]

// agentTodoView is one to-do operation bound to the query handle it runs on; see
// registryView for why a registry operation carries the handle rather than taking
// it at each step.
type agentTodoView = registryView[cachedTodo, int64]

// cachedTodo pairs an Item with the agent_todos `seq` it persists under. The
// seq is the row's key: eviction and per-row updates address rows by it. The
// Item's ID is the provider's task id when it has one (incremental Task*
// path) and empty otherwise (snapshot path for providers without stable
// per-task ids -- TodoWrite / Codex plan / ACP plan); todoIndexByID resolves a
// task id to its row.
type cachedTodo struct {
	item todoevents.Item
	seq  int64
}

type todoUpdateMutation struct {
	index  int
	seq    int64
	before todoevents.Item
	item   todoevents.Item
}

func reduceTodoUpdate(rows []cachedTodo, event todoevents.Event) (todoUpdateMutation, bool) {
	if event.Kind != todoevents.KindUpdate || event.ID == "" {
		return todoUpdateMutation{}, false
	}
	idx := todoIndexByID(rows, event.ID)
	if idx < 0 {
		return todoUpdateMutation{}, false
	}
	row := rows[idx]
	return todoUpdateMutation{
		index:  idx,
		seq:    row.seq,
		before: row.item,
		item:   normalizeTodoItem(todoevents.ApplyPatch(row.item, event.Patch)),
	}, true
}

// normalizeTodoItem resolves an unreported status only after reduction.
// The stored row, cache, and broadcast then carry the same canonical item.
func normalizeTodoItem(item todoevents.Item) todoevents.Item {
	item.Status = item.Status.OrPending()
	return item
}

// applyTodoEvent persists a single to-do event into agent_todos AND
// applies it to the in-memory mirror. Returns the post-mutation list
// (so the caller can broadcast without re-fetching) and a `mutated`
// flag that is false when the event was a no-op (unknown-id update, empty
// id, or a patch that changes nothing).
func (h *OutputHandler) applyTodoEvent(agentID string, ev todoevents.Event) ([]todoevents.Item, bool, error) {
	ctx := bgCtx()
	cache := h.todoCache(agentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	reg := cache.on(h.queries, agentID)
	if err := reg.ensureSeededLocked(ctx); err != nil {
		return nil, false, err
	}
	return applyTodoEventLocked(ctx, reg, h.db, ev)
}

func applyTodoEventLocked(ctx context.Context, reg agentTodoView, database *sql.DB, ev todoevents.Event) ([]todoevents.Item, bool, error) {
	cache := reg.cache
	switch ev.Kind {
	case todoevents.KindSnapshot:
		return applyTodoSnapshotLocked(ctx, reg, database, ev.Snapshot)

	case todoevents.KindCreate, todoevents.KindDetail:
		mutation, changed := reduceTodoUpsert(cache.Rows, ev.Kind, ev.Item)
		if !changed {
			return todoItems(cache.Rows), false, nil
		}
		// One transaction, as applyTodoMergeLocked's: at the cap the eviction's
		// DELETE and the insert must commit together, or a failed insert leaves
		// an unrelated to-do deleted from the table and the cache.
		mutated := false
		err := reg.inTransactionLocked(ctx, database, func(tx agentTodoView) error {
			var err error
			mutated, err = applyPreparedTodoUpsertLocked(ctx, tx, mutation)
			return err
		})
		if err != nil {
			return nil, false, err
		}
		return todoItems(cache.Rows), mutated, nil

	case todoevents.KindMerge:
		return applyTodoMergeLocked(ctx, reg, database, ev.Items)

	case todoevents.KindUpdate:
		mutation, ok := reduceTodoUpdate(cache.Rows, ev)
		if !ok {
			return nil, false, nil
		}
		return applyPreparedTodoUpdateLocked(ctx, reg, mutation)
	}
	return nil, false, nil
}

func applyPreparedTodoUpdateLocked(ctx context.Context, reg agentTodoView, mutation todoUpdateMutation) ([]todoevents.Item, bool, error) {
	cache := reg.cache
	if mutation.index < 0 || mutation.index >= len(cache.Rows) ||
		cache.Rows[mutation.index].seq != mutation.seq ||
		cache.Rows[mutation.index].item != mutation.before {
		return nil, false, fmt.Errorf("prepared todo update no longer matches canonical task %q", mutation.item.ID)
	}
	// Skip the database write and broadcast for an empty or repeated patch.
	if mutation.item == mutation.before {
		return todoItems(cache.Rows), false, nil
	}
	if err := updateTodoRowLocked(ctx, reg, mutation.seq, mutation.item); err != nil {
		return nil, false, err
	}
	cache.Rows[mutation.index].item = mutation.item
	return todoItems(cache.Rows), true, nil
}

// updateTodoRowLocked writes item over the row at seq. The cache mirrors the
// table, so an update that matches no row means the two diverged; it fails
// rather than leave the cache holding a row the table does not.
// Caller must hold the cache's mutex.
func updateTodoRowLocked(ctx context.Context, reg agentTodoView, seq int64, item todoevents.Item) error {
	rows, err := reg.queries.UpdateAgentTodo(ctx, db.UpdateAgentTodoParams{
		Content:     item.Content,
		ActiveForm:  item.ActiveForm,
		Description: item.Description,
		Status:      leapmuxv1.TodoStatus(item.Status),
		AgentID:     reg.ownerID,
		Seq:         seq,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("agent_todos row %s/%d is gone; the registry cache no longer mirrors the table", reg.ownerID, seq)
	}
	return nil
}

// evictOldestFinishedTodoLocked removes the oldest finished row (completed or
// deleted) from the cache and the DB via the shared registryCache mechanics,
// through the CALLER's view -- so an eviction a transaction forced rolls back
// with it.
// Returns false (with no error) when no finished row exists; the caller drops
// the incoming event in that case. Logs the eviction for operator visibility.
// Caller must hold the cache's mutex.
//
// agent_todos is a SINGLE-pool registry (todoOps sets no bucketOf), so 0 identifies
// its one pool.
func evictOldestFinishedTodoLocked(ctx context.Context, reg agentTodoView) (bool, error) {
	// The generic returns the row it deleted, so the log reports that row rather
	// than one a second scan here picked out.
	evicted, evictedHappened, err := reg.evictOldestFinishedInBucketLocked(ctx, 0)
	if err != nil {
		return false, err
	}
	if evictedHappened {
		slog.Info("agent_todos cap reached; evicted oldest completed/deleted task",
			"agent_id", reg.ownerID, "evicted_task_id", evicted.item.ID, "evicted_status", evicted.item.Status, "cap", todoevents.MaxTodos)
	}
	return evictedHappened, nil
}

// upsertTodoLocked writes one row of a KindCreate, KindDetail or KindMerge event and
// reports whether it changed anything. The caller holds cache.Mu.
//
// `reg` carries the handle the write goes through, and the eviction this can force
// goes through the same one. Every caller passes a transaction view, so the
// eviction and the write commit together: a KindCreate or KindDetail event gets
// its own transaction, and a merge frame's whole set of writes lands in one.
// upsertTodoLocked never opens a transaction itself, so nothing nests.
func upsertTodoLocked(ctx context.Context, reg agentTodoView, kind todoevents.EventKind, item todoevents.Item) (bool, error) {
	mutation, changed := reduceTodoUpsert(reg.cache.Rows, kind, item)
	if !changed {
		return false, nil
	}
	return applyPreparedTodoUpsertLocked(ctx, reg, mutation)
}

type todoUpsertMutation struct {
	index  int
	seq    int64
	before todoevents.Item
	item   todoevents.Item
}

// reduceTodoUpsert resolves replacement and detail semantics without I/O.
// An unchanged result needs neither a transaction nor a rollback mirror.
func reduceTodoUpsert(rows []cachedTodo, kind todoevents.EventKind, item todoevents.Item) (todoUpsertMutation, bool) {
	if item.ID == "" {
		return todoUpsertMutation{}, false
	}
	mutation := todoUpsertMutation{index: todoIndexByID(rows, item.ID), item: item}
	if mutation.index >= 0 {
		row := rows[mutation.index]
		mutation.seq, mutation.before = row.seq, row.item
		if kind == todoevents.KindDetail {
			mutation.item = todoevents.MergeDetail(row.item, item)
		}
	}
	mutation.item = normalizeTodoItem(mutation.item)
	if mutation.index >= 0 && mutation.item == mutation.before {
		return todoUpsertMutation{}, false
	}
	return mutation, true
}

// applyPreparedTodoUpsertLocked writes through the caller's transaction view.
// The caller keeps cache.Mu locked from reduction through application.
func applyPreparedTodoUpsertLocked(ctx context.Context, reg agentTodoView, mutation todoUpsertMutation) (bool, error) {
	cache := reg.cache
	if mutation.index >= 0 {
		if mutation.index >= len(cache.Rows) || cache.Rows[mutation.index].seq != mutation.seq || cache.Rows[mutation.index].item != mutation.before {
			return false, fmt.Errorf("prepared todo upsert no longer matches canonical task %q", mutation.item.ID)
		}
		if err := updateTodoRowLocked(ctx, reg, mutation.seq, mutation.item); err != nil {
			return false, err
		}
		cache.Rows[mutation.index].item = mutation.item
		return true, nil
	}
	if todoIndexByID(cache.Rows, mutation.item.ID) >= 0 {
		return false, fmt.Errorf("prepared todo insertion already has a canonical task %q", mutation.item.ID)
	}
	if len(cache.Rows) >= todoevents.MaxTodos {
		// At cap: evict the oldest finished row (completed or
		// deleted) to make room for the new task. When no finished
		// row exists, drop the new task and log so operators see
		// the cap pressure.
		evicted, err := evictOldestFinishedTodoLocked(ctx, reg)
		if err != nil {
			return false, err
		}
		if !evicted {
			slog.Warn("agent_todos cap reached, no completed or deleted task to evict; dropping new task",
				"agent_id", reg.ownerID, "task_id", mutation.item.ID, "cap", todoevents.MaxTodos)
			return false, nil
		}
	}
	// Pick the next seq from the in-memory mirror so a fresh insert gets the
	// right ordering without a `MAX(seq)` round-trip.
	seq := cache.nextSeq
	if err := reg.queries.InsertAgentTodo(ctx, db.InsertAgentTodoParams{
		AgentID:     reg.ownerID,
		Seq:         seq,
		TaskID:      mutation.item.ID,
		Content:     mutation.item.Content,
		ActiveForm:  mutation.item.ActiveForm,
		Description: mutation.item.Description,
		Status:      leapmuxv1.TodoStatus(mutation.item.Status),
	}); err != nil {
		return false, err
	}
	cache.Rows = append(cache.Rows, cachedTodo{item: mutation.item, seq: seq})
	cache.nextSeq++
	return true, nil
}

// applyTodoSnapshotLocked replaces every agent_todos row with the supplied
// list, capped at todoevents.MaxTodos. The delete-all + N inserts are
// ONE transaction when database is set (production); tests that construct the
// handler with a nil *sql.DB run the same loop without one.
//
// The cache moves only AFTER the write returns, so nothing here needs the
// mirror that inTransactionLocked captures. It runs through the same helper
// anyway, and the alternative -- a second, mirror-free helper beside it -- is
// what this refuses. Two helpers make the caller decide, the decision is
// invisible, and the day this body starts mutating the cache it would still
// carry the one that cannot roll it back.
//
// Caller must hold cache.Mu.
func applyTodoSnapshotLocked(ctx context.Context, reg agentTodoView, database *sql.DB, snapshot []todoevents.Item) ([]todoevents.Item, bool, error) {
	cache := reg.cache
	capped := snapshot
	if len(capped) > todoevents.MaxTodos {
		capped = capped[:todoevents.MaxTodos]
	}
	// Re-emitted plans (Codex re-broadcasts on every tick; TaskList
	// polled twice with no change) carry a structurally identical
	// snapshot. Skip the delete-and-insert tx + broadcast on a no-op.
	if todoItemsEqual(cache.Rows, capped) {
		return todoItems(cache.Rows), false, nil
	}
	normalized := make([]todoevents.Item, len(capped))
	for i, item := range capped {
		normalized[i] = normalizeTodoItem(item)
	}
	capped = normalized
	err := reg.inTransactionLocked(ctx, database, func(tx agentTodoView) error {
		return writeTodoSnapshotLocked(ctx, tx, capped)
	})
	if err != nil {
		// The helper reports the registry and the stage ("commit agent_todos");
		// only this caller knows WHICH grouped write failed.
		return nil, false, fmt.Errorf("apply todo snapshot: %w", err)
	}
	cache.Rows = make([]cachedTodo, len(capped))
	for i, it := range capped {
		cache.Rows[i] = cachedTodo{item: it, seq: int64(i + 1)}
	}
	cache.nextSeq = int64(len(capped)) + 1
	return todoItems(cache.Rows), true, nil
}

// applyTodoMergeLocked writes every row a merge frame lists, in ONE transaction.
//
// A merge frame states the new truth for the rows it lists, and a long checklist lists
// many. One statement for each was one transaction for each -- every one a separate
// commit, all of them under the cache lock -- where applyTodoSnapshotLocked's
// delete-and-insert has always been a single unit. It is now the same unit, including
// an eviction the cap forces on the way: the body writes through the view
// inTransactionLocked binds to the transaction, so the eviction's DELETE lands in
// this transaction with the upserts.
//
// The cache mirrors the table, and each upsert reads what the one before it left, so
// the mirror moves as the transaction does. inTransactionLocked puts it back on a
// failure: half a merge in memory over none of it in the table is a list nothing can
// rebuild until the next cold seed, which for a live agent is never.
//
// Caller must hold cache.Mu.
func applyTodoMergeLocked(ctx context.Context, reg agentTodoView, database *sql.DB, items []todoevents.Item) ([]todoevents.Item, bool, error) {
	changes := false
	for _, item := range items {
		if _, changed := reduceTodoUpsert(reg.cache.Rows, todoevents.KindCreate, item); changed {
			changes = true
			break
		}
	}
	if !changes {
		return todoItems(reg.cache.Rows), false, nil
	}
	mutated := false
	err := reg.inTransactionLocked(ctx, database, func(tx agentTodoView) error {
		for _, item := range items {
			// KindCreate, so a listed row REPLACES the row that carries its ID. A merge
			// frame states the new truth for the rows it lists, which MergeDetail's
			// preserve-the-blank rule would refuse to write.
			changed, err := upsertTodoLocked(ctx, tx, todoevents.KindCreate, item)
			if err != nil {
				return err
			}
			mutated = mutated || changed
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("apply todo merge: %w", err)
	}
	return todoItems(reg.cache.Rows), mutated, nil
}

// writeTodoSnapshotLocked deletes the owner's rows and inserts the capped list, on the
// view's own handle. Caller must hold cache.Mu.
func writeTodoSnapshotLocked(ctx context.Context, reg agentTodoView, capped []todoevents.Item) error {
	q, agentID := reg.queries, reg.ownerID
	if err := q.DeleteAllAgentTodos(ctx, agentID); err != nil {
		return fmt.Errorf("delete agent_todos: %w", err)
	}
	for i, it := range capped {
		if err := q.InsertAgentTodo(ctx, db.InsertAgentTodoParams{
			AgentID:     agentID,
			Seq:         int64(i + 1),
			TaskID:      it.ID,
			Content:     it.Content,
			ActiveForm:  it.ActiveForm,
			Description: it.Description,
			Status:      leapmuxv1.TodoStatus(it.Status),
		}); err != nil {
			return fmt.Errorf("insert agent_todo: %w", err)
		}
	}
	return nil
}

// todoItemFromRow projects a persisted agent_todos row into the in-memory
// Item shape. Used by ensureSeededLocked when populating the cache
// from DB.
func todoItemFromRow(r db.AgentTodo) todoevents.Item {
	return todoevents.Item{
		ID:          r.TaskID,
		Content:     r.Content,
		Status:      todoevents.Status(r.Status),
		ActiveForm:  r.ActiveForm,
		Description: r.Description,
	}
}

// todoCache returns the per-agent cache, creating an empty (unseeded)
// one if none exists. Concurrent first-touch callers receive the same
// instance via sync.Map.LoadOrStore; the actual DB seed runs once via
// ensureSeededLocked under the cache's own mutex.
func (h *OutputHandler) todoCache(agentID string) *agentTodoCache {
	if v, ok := h.todos.Load(agentID); ok {
		return v.(*agentTodoCache)
	}
	fresh := &agentTodoCache{ops: todoOps()}
	actual, _ := h.todos.LoadOrStore(agentID, fresh)
	return actual.(*agentTodoCache)
}

// todoOps supplies the shared operations for to-do caches.
// Each database operation uses the query handle supplied by its caller.
func todoOps() registryOps[cachedTodo, int64] {
	return registryOps[cachedTodo, int64]{
		// agent_todos is a SINGLE-pool registry, so `bucket` is always 0.
		listRows: func(ctx context.Context, q *db.Queries, ownerID string, _ int64, limit int32) ([]seedEntry[cachedTodo], error) {
			rows, err := q.ListAgentTodosNewestFirst(ctx, db.ListAgentTodosNewestFirstParams{
				AgentID: ownerID,
				Limit:   int64(limit),
			})
			if err != nil {
				return nil, fmt.Errorf("list agent_todos: %w", err)
			}
			entries := make([]seedEntry[cachedTodo], len(rows))
			for i, r := range rows {
				entries[i] = seedEntry[cachedTodo]{item: cachedTodo{item: todoItemFromRow(r), seq: r.Seq}, seq: r.Seq}
			}
			return entries, nil
		},
		keyOf: func(r cachedTodo) int64 { return r.seq },
		isFinished: func(r cachedTodo) bool {
			return r.item.Status.IsFinished()
		},
		isWorking: func(r cachedTodo) bool {
			return !r.item.Status.IsFinished()
		},
		deleteByKey: func(ctx context.Context, q *db.Queries, ownerID string, seq int64) error {
			return q.DeleteAgentTodoBySeq(ctx, db.DeleteAgentTodoBySeqParams{AgentID: ownerID, Seq: seq})
		},
		// One cap pool (bucketOf nil), so the seed limit is the cap itself.
		cap:   todoevents.MaxTodos,
		label: "agent_todos",
	}
}

// LoadTodos returns the agent's to-do list, seeding the in-memory
// cache from agent_todos on first access. Cold-start RPCs route
// through here so a warm cache returns without a DB read and every
// subsequent caller observes the same authoritative snapshot.
func (h *OutputHandler) LoadTodos(ctx context.Context, agentID string) ([]todoevents.Item, error) {
	cache := h.todoCache(agentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	if err := cache.on(h.queries, agentID).ensureSeededLocked(ctx); err != nil {
		return nil, err
	}
	return todoItems(cache.Rows), nil
}

// todoItems projects the cache's rows into a freshly-allocated slice of the
// items (no seqs). Used to build the post-mutation broadcast payload.
// Caller must hold the cache's mutex.
func todoItems(rows []cachedTodo) []todoevents.Item {
	out := make([]todoevents.Item, len(rows))
	for i, r := range rows {
		out[i] = r.item
	}
	return out
}

// todoIndexByID returns the row index for the given task id, or -1 when not
// present. Providers address to-dos by task id for create/update/delete, and
// the registry keys a row by its seq, so this lookup is by item.ID rather than
// by the registry key. Caller must hold the cache's mutex.
func todoIndexByID(rows []cachedTodo, id string) int {
	return slices.IndexFunc(rows, func(r cachedTodo) bool { return r.item.ID == id })
}

// todoItemsEqual compares canonical rows with a snapshot after normalization.
// The snapshot path uses it to skip unchanged writes and broadcasts.
// Caller must hold the cache's mutex.
func todoItemsEqual(rows []cachedTodo, other []todoevents.Item) bool {
	if len(rows) != len(other) {
		return false
	}
	for i := range rows {
		if rows[i].item != normalizeTodoItem(other[i]) {
			return false
		}
	}
	return true
}
