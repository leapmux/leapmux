package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/util/validate"
)

// bgTaskCache is the in-memory mirror of one root agent's background-task
// registry, built on the shared registryCache mechanics. The type-specific
// ops (list query, key/finished/seq extractors, delete query) live in
// bgTaskOps, built once per OutputHandler.
type bgTaskCache = registryCache[bgtask.Item]

// bgTaskView is one background-task operation bound to the query handle it runs
// on; see registryView for why a registry operation carries the handle rather
// than taking it at each step.
type bgTaskView = registryView[bgtask.Item]

// bgItemFromRow projects a persisted agent_background_tasks row into the
// in-memory Item shape. Used by the seed closure.
func bgItemFromRow(r db.AgentBackgroundTask) bgtask.Item {
	var endedAt time.Time
	if r.EndedAt.Valid {
		endedAt = r.EndedAt.Time
	}
	return bgtask.Item{
		RowKey:         r.RowKey,
		ChildAgentID:   r.ChildAgentID,
		ParentAgentID:  r.ParentAgentID,
		Kind:           bgtask.Kind(r.Kind),
		GroupKey:       r.GroupKey,
		GroupLabel:     r.GroupLabel,
		Title:          r.Title,
		TitleIsCommand: ptrconv.Int64ToBool(r.TitleIsCommand),
		Description:    r.Description,
		ActiveForm:     r.ActiveForm,
		Status:         bgtask.Status(r.Status),
		CreatedAt:      r.CreatedAt.Time,
		UpdatedAt:      r.UpdatedAt.Time,
		EndedAt:        endedAt,
	}
}

// bgTaskOps is the registryOps for background tasks, capturing the handler's
// queries so every cache instance shares the same type-specific behaviour.
func (h *OutputHandler) bgTaskOps() registryOps[bgtask.Item] {
	return registryOps[bgtask.Item]{
		listRows: func(ctx context.Context, q *db.Queries, ownerID string, bucket int64, limit int32) ([]seedEntry[bgtask.Item], error) {
			rows, err := q.ListAgentBackgroundTasksByKindNewestFirst(ctx, db.ListAgentBackgroundTasksByKindNewestFirstParams{
				OwnerAgentID: ownerID,
				Kind:         leapmuxv1.BackgroundTaskKind(bucket),
				Limit:        int64(limit),
			})
			if err != nil {
				return nil, fmt.Errorf("list agent_background_tasks: %w", err)
			}
			entries := make([]seedEntry[bgtask.Item], len(rows))
			for i, r := range rows {
				entries[i] = seedEntry[bgtask.Item]{item: bgItemFromRow(r), seq: r.Seq}
			}
			return entries, nil
		},
		reclaimFinishedBelowSeq: func(ctx context.Context, q *db.Queries, ownerID string, bucket, seq int64) error {
			_, err := q.DeleteFinishedAgentBackgroundTasksBelowSeq(ctx, db.DeleteFinishedAgentBackgroundTasksBelowSeqParams{
				MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
				OwnerAgentID:   ownerID,
				Kind:           leapmuxv1.BackgroundTaskKind(bucket),
				Seq:            seq,
			})
			return err
		},
		keyOf:  func(r bgtask.Item) string { return r.RowKey },
		setKey: func(r *bgtask.Item, key string) { r.RowKey = key },
		isFinished: func(r bgtask.Item) bool {
			return r.Status.IsFinished()
		},
		isWorking: func(r bgtask.Item) bool {
			return r.Status.IsWorking()
		},
		deleteByKey: func(ctx context.Context, q *db.Queries, ownerID, key string) error {
			_, err := q.DeleteAgentBackgroundTaskByRowKey(ctx, db.DeleteAgentBackgroundTaskByRowKeyParams{
				OwnerAgentID: ownerID,
				RowKey:       key,
			})
			return err
		},
		// A row that carries a child transcript leaves the DISPLAY list at the
		// cap but stays in the table. It is the only index from that child agent
		// id back to (owner, row_key) -- the reverse lookup behind
		// send-to-subagent and interrupt -- and the child agents row outlives the
		// registry delete, so deleting the row leaves a subagent nobody can reach
		// while its transcript is still readable.
		retention: &registryRetention[bgtask.Item]{
			keep: func(r bgtask.Item) bool { return r.ChildAgentID != "" },
			load: func(ctx context.Context, q *db.Queries, ownerID, key string) (bgtask.Item, bool, error) {
				return h.loadStoredBgTask(ctx, q, ownerID, key)
			},
			reseq: func(ctx context.Context, q *db.Queries, ownerID, key string, seq int64) error {
				return q.ResequenceAgentBackgroundTask(ctx, db.ResequenceAgentBackgroundTaskParams{
					Seq:          seq,
					OwnerAgentID: ownerID,
					RowKey:       key,
				})
			},
		},
		cap: bgtask.MaxTasks,
		// One cap pool per KIND. A run that opens hundreds of shells would
		// otherwise evict every finished subagent, and the subagent rows are the
		// ones carrying a transcript worth reopening.
		bucketOf: func(r bgtask.Item) int64 { return int64(r.Kind) },
		buckets:  bgtask.KindBuckets(),
		label:    "background tasks",
	}
}

// loadStoredBgTask reads one persisted registry row by its PRIMARY KEY and
// projects it into the in-memory shape. found=false for a row that does not
// exist, so a caller tells "absent" from "unreadable" without inspecting the
// error.
//
// This is the single reader for a row the display cache does not hold. Both
// callers need it for the same reason -- the cap limits the list, not the table
// -- and one of them (registryOps.retention.load) is how every mutation reaches
// a retained row.
func (h *OutputHandler) loadStoredBgTask(ctx context.Context, q *db.Queries, ownerID, rowKey string) (bgtask.Item, bool, error) {
	row, err := q.GetAgentBackgroundTaskByRowKey(ctx, db.GetAgentBackgroundTaskByRowKeyParams{
		OwnerAgentID: ownerID,
		RowKey:       rowKey,
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return bgtask.Item{}, false, nil
	case err != nil:
		return bgtask.Item{}, false, fmt.Errorf("read background task %s/%s: %w", ownerID, rowKey, err)
	}
	return bgItemFromRow(row), true, nil
}

// bgTaskCache returns the per-root-agent cache, creating an empty (unseeded)
// one if none exists. Mirrors todoCache.
func (h *OutputHandler) bgTaskCache(rootAgentID string) *bgTaskCache {
	if v, ok := h.bgtasks.Load(rootAgentID); ok {
		return v.(*bgTaskCache)
	}
	fresh := &bgTaskCache{ops: h.bgTaskOps()}
	actual, _ := h.bgtasks.LoadOrStore(rootAgentID, fresh)
	return actual.(*bgTaskCache)
}

// SetShuttingDown marks the handler as shutting down so a shutdown-driven
// StopAll leaves background-task rows active (the next boot labels them
// 'interrupted'). Called at the top of Service.Shutdown.
func (h *OutputHandler) SetShuttingDown() {
	// Under refreshLifecycleMu, so that every beginActivityRefresh which saw the
	// latch false has already taken its count by the time this returns. Shutdown
	// then calls WaitActivityRefreshes with no Add left that could race the Wait.
	h.refreshLifecycleMu.Lock()
	defer h.refreshLifecycleMu.Unlock()
	h.shuttingDown.Store(true)
}

// broadcastBackgroundTasks fans the post-mutation snapshot out to live watchers
// under the root owner id. The event is notification-class (Part 3d) so an
// off-screen root tab still updates the sidebar/badge.
func (h *OutputHandler) broadcastBackgroundTasks(rootAgentID string, rows []bgtask.Item) {
	h.watcher.BroadcastAgentEvent(rootAgentID, &leapmuxv1.AgentEvent{
		AgentId: rootAgentID,
		Event: &leapmuxv1.AgentEvent_BackgroundTasksChanged{
			BackgroundTasksChanged: &leapmuxv1.AgentBackgroundTasksChanged{
				AgentId: rootAgentID,
				Tasks:   bgtask.ItemsToProto(rows),
			},
		},
	})
}

// LoadBackgroundTasks returns the root's DISPLAY list, seeding the in-memory
// cache from agent_background_tasks on first access. Cold-start RPCs route
// through here so a warm cache returns without a DB read. For a CHILD agent this
// returns empty (children own no registry).
//
// The list is capped at bgtask.MaxTasks per kind and is NOT exhaustive: a row
// that carries a child transcript stays in the table after it leaves this list
// (see registryOps.retention), so it is invisible in the sidebar and still
// resolvable by key. A caller that needs the linkage must query the table --
// LookupBackgroundTask and GetAgentBackgroundTaskByChildAgentID both do.
func (h *OutputHandler) LoadBackgroundTasks(ctx context.Context, rootAgentID string) ([]bgtask.Item, error) {
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	if err := cache.on(h.queries, rootAgentID).ensureSeededLocked(ctx); err != nil {
		return nil, err
	}
	return cache.snapshot(), nil
}

// registryChange carries the snapshot and publication decision for one mutation.
type registryChange struct {
	rows []bgtask.Item
	// A replay or an absent row changes no stored or displayed state.
	changed bool
}

// applyBackgroundTaskUpsert is the persist-mutate-broadcast for an upsert. It
// writes the DB row, mutates the cache in place, runs cap-eviction, and
// broadcasts. A byte-identical replay (same row, same status, same fields)
// skips BOTH the write and the broadcast.
func (h *OutputHandler) applyBackgroundTaskUpsert(rootAgentID string, task bgtask.Upsert) (registryChange, error) {
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	return h.applyBackgroundTaskUpsertLocked(cache, rootAgentID, task)
}

// withBgTaskRow runs `mutate` against the registry row at rowKey, under the
// root's cache mutex and with the cache seeded. A no-op for a row that exists
// nowhere.
//
// One prologue for every applier that patches an EXISTING row, so the seeding
// rule, the lookup rule, and the "no such row" answer have a single home. The
// upsert applier is deliberately not a caller: it runs under a lock the caller
// already holds, and its miss branch inserts instead of answering no-op.
//
// `mutate` receives the resolved row, the view bound to this operation's query
// handle, and an `admit` function. Its own writes go through the view, so every
// statement of one mutation -- the lookup, the re-admission, the eviction that
// re-admission can force, and the applier's own UPDATE -- runs on one handle.
//
// It must call `admit` at the point where it commits to a write. That order is
// the contract:
// a retained row that left the display list is not put back for a mutation that
// turns out to be a no-op, because re-admitting evicts a displayed row -- and
// deletes an unlinked one from the table -- and the applier then reports
// changed=false, so no broadcast tells the client its list moved. `admit` is
// also the only route to a cache index, so an applier cannot write without it.
func (h *OutputHandler) withBgTaskRow(
	rootAgentID, rowKey string,
	mutate func(ctx context.Context, reg bgTaskView, row bgtask.Item, displayed bool, admit func() (int, error)) (registryChange, error),
) (registryChange, error) {
	ctx := h.bgTaskCtx()
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	reg := cache.on(h.queries, rootAgentID)
	if err := reg.ensureSeededLocked(ctx); err != nil {
		return registryChange{}, err
	}
	row, idx, found, err := reg.findRowLocked(ctx, rowKey)
	if err != nil {
		return registryChange{}, err
	}
	if !found {
		return registryChange{rows: cache.snapshot()}, nil
	}
	admit := func() (int, error) {
		if idx >= 0 {
			return idx, nil
		}
		var err error
		idx, err = reg.admitRowLocked(ctx, row)
		return idx, err
	}
	return mutate(ctx, reg, row, idx >= 0, admit)
}

// applyBackgroundTaskStatus updates a row's status + active_form without
// closing it (used for running-progress updates). A no-op when the row is
// absent or the patch is a no-op.
func (h *OutputHandler) applyBackgroundTaskStatus(rootAgentID, rowKey string, status bgtask.Status, activeForm string) (registryChange, error) {
	// The SECOND entry point for a provider-chosen label, and it never builds an
	// Upsert -- so `Upsert.Clean` cannot reach it and the cap has to be applied
	// here as well. Same rule, same limit.
	activeForm = validate.StripUnreadable(activeForm, bgtask.LabelByteLimit)
	return h.withBgTaskRow(rootAgentID, rowKey, func(ctx context.Context, reg bgTaskView, existing bgtask.Item, displayed bool, admit func() (int, error)) (registryChange, error) {
		cache := reg.cache
		// A final status is monotonic and absorbing: a late or replayed
		// non-final status update (a duplicate task_progress, a replayed
		// running upsert) must not resurrect a row that already reached a
		// final state. Without this guard the row flips back to running and
		// pins the parent's thinking indicator forever (no later close arrives).
		if existing.Status.IsFinished() && !status.IsFinished() {
			return registryChange{rows: cache.snapshot()}, nil
		}
		if existing.Status == status && existing.ActiveForm == activeForm {
			return registryChange{rows: cache.snapshot()}, nil
		}
		idx, err := admit()
		if err != nil {
			return registryChange{}, err
		}
		// One ms-floored instant for both the DB write and the in-memory cache, so a
		// warm-cache read after this transition returns the same stamp a cold-start
		// read does (no Go-time.Now-vs-SQLite-strftime drift).
		now := nowMillis()
		if err := reg.queries.UpdateAgentBackgroundTaskStatus(ctx, db.UpdateAgentBackgroundTaskStatusParams{
			Status:       leapmuxv1.BackgroundTaskStatus(status),
			ActiveForm:   activeForm,
			UpdatedAt:    sqltime.NewSQLiteTime(now),
			OwnerAgentID: rootAgentID,
			RowKey:       rowKey,
		}); err != nil {
			return registryChange{}, err
		}
		// A transition into a final status stamps ended_at. The status-update query does not
		// (sqlc cannot infer the type of a positional parameter inside a CASE WHEN,
		// so the atomic single-statement form is not generatable), so a dedicated
		// idempotent query does it. The stamp's WHERE filters `ended_at IS NULL AND
		// status IN (the final statuses)`, so it only writes on a genuine transition. On a
		// transient DB error the cache leaves EndedAt zero (mirroring the DB's NULL)
		// and the error propagates so the caller sees the incomplete transition;
		// the idempotent stamp can be retried by any later final update.
		if status.IsFinished() {
			if err := reg.queries.StampAgentBackgroundTaskEndedAt(ctx, db.StampAgentBackgroundTaskEndedAtParams{
				MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
				EndedAt:        sqltime.SQLiteNullTimeOf(now),
				OwnerAgentID:   rootAgentID,
				RowKey:         rowKey,
			}); err != nil {
				return registryChange{}, err
			}
			if cache.Rows[idx].EndedAt.IsZero() {
				cache.Rows[idx].EndedAt = now
			}
		}
		cache.Rows[idx].Status = status
		cache.Rows[idx].ActiveForm = activeForm
		cache.Rows[idx].UpdatedAt = now
		return registryChange{rows: cache.snapshot(), changed: true}, nil
	})
}

// applyBackgroundTaskClose moves a row into a final status (stamps ended_at).
// The query's status-IN('pending','running') guard means a final row can never
// be resurrected or re-closed. A no-op when the row is already final.
func (h *OutputHandler) applyBackgroundTaskClose(rootAgentID, rowKey string, status bgtask.Status) (registryChange, error) {
	return h.withBgTaskRow(rootAgentID, rowKey, func(ctx context.Context, reg bgTaskView, existing bgtask.Item, displayed bool, admit func() (int, error)) (registryChange, error) {
		cache := reg.cache
		if existing.Status.IsFinished() {
			return registryChange{rows: cache.snapshot()}, nil
		}
		idx, err := admit()
		if err != nil {
			return registryChange{}, err
		}
		now := nowMillis()
		if err := reg.queries.CloseAgentBackgroundTask(ctx, db.CloseAgentBackgroundTaskParams{
			MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
			Status:         leapmuxv1.BackgroundTaskStatus(status),
			EndedAt:        sqltime.SQLiteNullTimeOf(now),
			UpdatedAt:      sqltime.NewSQLiteTime(now),
			OwnerAgentID:   rootAgentID,
			RowKey:         rowKey,
		}); err != nil {
			return registryChange{}, err
		}
		cache.Rows[idx].Status = status
		cache.Rows[idx].EndedAt = now
		cache.Rows[idx].UpdatedAt = now
		return registryChange{rows: cache.snapshot(), changed: true}, nil
	})
}

// applyBackgroundTaskRevive returns a FINISHED row to Running and clears its
// ended_at and its descriptive state, for a subagent that its provider
// restarted. The registry status changes without adding transcript messages.
//
// This is the ONLY applier that undoes a final status, and it stays separate
// from the upsert and the status update on purpose. Those two absorb a non-final
// status against a final row, and that guard must hold: a replayed running
// upsert has no way to prove the task restarted, so honoring it would leave a
// row Running that nothing closes and pin the parent's thinking indicator. A
// caller reaches THIS applier only with positive evidence of a restart, so the
// two cases never have to be told apart after the fact.
//
// Idempotent: an absent row and an already-active row both return an unchanged
// no-op, which is what makes a duplicate revive harmless.
func (h *OutputHandler) applyBackgroundTaskRevive(rootAgentID, rowKey string) (registryChange, error) {
	return h.withBgTaskRow(rootAgentID, rowKey, func(ctx context.Context, reg bgTaskView, existing bgtask.Item, displayed bool, admit func() (int, error)) (registryChange, error) {
		cache := reg.cache
		if !existing.Status.IsFinished() {
			return registryChange{rows: cache.snapshot()}, nil
		}
		// One ms-floored instant for the DB write and the cache, so a warm-cache read
		// matches a cold-start read (no Go-time.Now-vs-SQLite drift).
		now := nowMillis()
		rows, err := reg.queries.ReviveAgentBackgroundTask(ctx, db.ReviveAgentBackgroundTaskParams{
			Status:         leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
			MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
			UpdatedAt:      sqltime.NewSQLiteTime(now),
			OwnerAgentID:   rootAgentID,
			RowKey:         rowKey,
		})
		if err != nil {
			return registryChange{}, err
		}
		if rows == 0 {
			return h.adoptStoredBgTaskRowLocked(ctx, reg, rowKey, displayed, admit)
		}
		idx, err := admit()
		if err != nil {
			return registryChange{}, err
		}
		cache.Rows[idx].Status = bgtask.StatusRunning
		// ActiveForm and Description both describe the run that ENDED -- the last
		// activity text, and the output file its task_notification specified. The
		// restarted run reported neither yet, and the row's activity slot shows
		// whichever is present, so leaving them pins the previous run's output path
		// under a subagent that runs again.
		cache.Rows[idx].ActiveForm = ""
		cache.Rows[idx].Description = ""
		cache.Rows[idx].EndedAt = time.Time{}
		cache.Rows[idx].UpdatedAt = now
		return registryChange{rows: cache.snapshot(), changed: true}, nil
	})
}

// adoptStoredBgTaskRowLocked replaces a cached row with what the store holds,
// for a revive whose UPDATE matched nothing. Caller must hold cache.Mu.
//
// The revive's WHERE filters on a final status, so a zero row count means the
// cache and the row disagree, and the row decides. It does NOT mean "the row is
// already active": the same count answers "no such row", and the two need
// different repairs. Re-reading settles it without a second guess, and it also
// adopts every field rather than the two a hand-written repair remembered --
// active_form and description describe the run that ENDED, and the SQL clears
// them for exactly that reason.
//
// The repair is for the DISPLAY list only. A retained row that left the list has
// no cached copy to disagree with the store, so there is nothing to adopt and
// nothing to broadcast -- and admitting it would evict a displayed row, and
// delete an unlinked one, for a write that never happens.
func (h *OutputHandler) adoptStoredBgTaskRowLocked(
	ctx context.Context, reg bgTaskView, rowKey string, displayed bool, admit func() (int, error),
) (registryChange, error) {
	cache := reg.cache
	if !displayed {
		return registryChange{rows: cache.snapshot()}, nil
	}
	stored, found, err := h.loadStoredBgTask(ctx, reg.queries, reg.ownerID, rowKey)
	if err != nil {
		return registryChange{}, err
	}
	if !found {
		// The row is gone (a cascade from its root agent, or a delete that raced
		// this call). Drop the stale cache entry rather than leave the display list
		// showing a row no cold read returns. dropRowLocked touches no DB row,
		// which is correct: there is none left to delete.
		//
		// The drop runs BEFORE the snapshot. Go evaluates a composite literal's
		// fields in order, so building both in one expression would capture the
		// list with the dead row still in it and broadcast that.
		dropped := cache.dropRowLocked(rowKey)
		return registryChange{rows: cache.snapshot(), changed: dropped}, nil
	}
	idx, err := admit()
	if err != nil {
		return registryChange{}, err
	}
	changed := cache.Rows[idx] != stored
	cache.Rows[idx] = stored
	return registryChange{rows: cache.snapshot(), changed: changed}, nil
}

// MarkAgentBackgroundTasksExited gives every still-active row owned by
// rootAgentID a final status on process exit: stopped (explicit Stop) -> Stopped, else
// interrupted (crash) -> Interrupted. Skipped entirely when the shutdown latch
// is set (a shutdown-driven StopAll leaves rows active for the next boot's
// 'interrupted' sweep). Broadcasts once if anything changed.
func (h *OutputHandler) MarkAgentBackgroundTasksExited(rootAgentID string, stopped bool) {
	if h.shuttingDown.Load() {
		return
	}
	ctx := h.bgTaskCtx()
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	if err := cache.on(h.queries, rootAgentID).ensureSeededLocked(ctx); err != nil {
		cache.Mu.Unlock()
		slog.Warn("mark background tasks exited: seed failed", "agent_id", rootAgentID, "error", err)
		return
	}
	status := bgtask.StatusInterrupted
	if stopped {
		status = bgtask.StatusStopped
	}
	now := nowMillis()
	// End every stored active task, including rows outside the display cap.
	_, err := h.queries.MarkAgentBackgroundTasksEnded(ctx, db.MarkAgentBackgroundTasksEndedParams{
		MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
		Status:         leapmuxv1.BackgroundTaskStatus(status),
		EndedAt:        sqltime.SQLiteNullTimeOf(now),
		UpdatedAt:      sqltime.NewSQLiteTime(now),
		OwnerAgentID:   rootAgentID,
	})
	if err != nil {
		cache.Mu.Unlock()
		slog.Warn("mark background tasks ended failed", "agent_id", rootAgentID, "error", err)
		return
	}
	// The SQL write also ends retained rows that the display cap hid. Their
	// keys no longer count as working after this point.
	cache.evictedActive = nil
	// The cache catches up to the write it just made, so the broadcast below
	// reports the display list the DB now holds. `changed` is the CACHE's answer
	// on purpose: a row the display list never held moved nothing the client can
	// see.
	changed := false
	for i := range cache.Rows {
		if !cache.Rows[i].Status.IsFinished() {
			cache.Rows[i].Status = status
			cache.Rows[i].EndedAt = now
			cache.Rows[i].UpdatedAt = now
			changed = true
		}
	}
	// Snapshot under the lock; broadcast AFTER release so a slow/stalled gRPC
	// stream consumer cannot block every other registry read/write for this root
	// (BroadcastAgentEvent -> SendStream can block on the transport).
	snapshot := cache.snapshot()
	cache.Mu.Unlock()
	if changed {
		h.broadcastBackgroundTasks(rootAgentID, snapshot)
	}
	// Unconditional, unlike the broadcast above: the process died, so every
	// descendant is idle now whether or not the DISPLAY list moved.
	//
	// settleImmediate, and NOT because a dead process resumes nothing:
	// settleCanResumeLocked already refuses to hold that one, so that reason
	// would make this argument removable. It is the TAB-CLOSE path that needs
	// it (see rootTeardown), where the agent can still be alive when this runs.
	// The derivation then calls the stop resumable and holds it, and nothing
	// later delivers it: no process-exit reset follows, and the cleanup that
	// does follow retires the entry. A watcher of a descendant transcript kept a
	// spinner and an armed Interrupt button on work whose process was gone.
	h.refreshActivityTree(rootAgentID, settleImmediate)
}

// --- agentOutputSink: ProviderServices registry + child-transcript methods ---

// EnsureChildAgent resolves the stored child under this sink's agent.
// Replays and Worker restarts resolve the same native child or spawn span.
// NewSink in output.go specifies the registry root owner.
// SpawnSpanID uses agents.spawn_span_id and the unique parent/span index.
// ProviderChildKey can identify the child before its native spawn span arrives.
// A supplied native session must match the stored child before registry changes.
func (s *agentOutputSink) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	if spec.SpawnSpanID == "" && spec.ProviderChildKey == "" {
		return "", errors.New("a child requires a native key or a spawn span")
	}
	ctx := s.h.bgTaskCtx()
	// Keep native identity intact. The registry derives its separate display key without truncation.
	rowKey := s.normalizeRowKey(spec.ProviderChildKey, "child registry key")
	// Provider titles need cleaning. Keep a blank registry title so an existing title survives.
	// The child insert supplies the same generated fallback that OpenAgent uses.
	spec.Title = validate.CleanName(spec.Title)
	childID, err := s.resolveStoredChildAgent(ctx, spec)
	if err != nil {
		return "", err
	}
	child, err := s.h.queries.GetAgentByID(ctx, childID)
	if err != nil {
		return "", err
	}
	if !child.ParentAgentID.Valid || child.ParentAgentID.String != s.agentID {
		return "", fmt.Errorf("the native child belongs to another parent: %w", agent.ErrChildIdentityRefused)
	}
	if spec.AgentSessionID != "" && child.AgentSessionID != spec.AgentSessionID {
		return "", fmt.Errorf("the child belongs to another native session: %w", agent.ErrChildIdentityRefused)
	}
	if spec.ProviderChildKey != "" {
		if child.ProviderChildKey != "" && child.ProviderChildKey != spec.ProviderChildKey {
			return "", fmt.Errorf("the child spawn span belongs to another native child: %w", agent.ErrChildIdentityRefused)
		}
		if child.ProviderChildKey == "" {
			if _, err := s.h.queries.AdoptChildProviderKey(ctx, db.AdoptChildProviderKeyParams{
				ID: childID, ParentAgentID: sqlString(s.agentID), ProviderChildKey: spec.ProviderChildKey,
			}); err != nil {
				return "", fmt.Errorf("record native child key: %w", err)
			}
			// A concurrent native key can claim the same old span. Re-read the exact committed identity before any registry write.
			child, err = s.h.queries.GetAgentByID(ctx, childID)
			if err != nil {
				return "", err
			}
			if child.ProviderChildKey != spec.ProviderChildKey {
				return "", fmt.Errorf("the child spawn span belongs to another native child: %w", agent.ErrChildIdentityRefused)
			}
		}
	}
	if spec.SpawnSpanID != "" && spec.ProviderChildKey != "" {
		if _, err := s.h.queries.AttachChildSpawnSpan(ctx, db.AttachChildSpawnSpanParams{
			ID: childID, ParentAgentID: sqlString(s.agentID), ProviderChildKey: spec.ProviderChildKey, SpawnSpanID: spec.SpawnSpanID,
		}); err != nil {
			return "", fmt.Errorf("attach native child spawn span: %w", err)
		}
	}
	// Validate ownership before registry mutation. Broadcast only after the registry lock is released.
	pendingBroadcast, err := s.linkRegistryRow(s.h.bgTaskCache(s.rootAgentID), rowKey, childID, spec.Title)
	if err != nil {
		return "", err
	}
	if pendingBroadcast != nil {
		s.h.broadcastBackgroundTasks(s.rootAgentID, pendingBroadcast)
		// Linking changes the child's activity. Refresh after the cache lock is released because refresh reads that cache.
		s.h.refreshActivityTree(s.rootAgentID, settleHeld)
	}
	return childID, nil
}

// ChildSpawnSpan reads back the spawn span recorded on the child's agent row.
//
// It reads the ROW and not the registry cache. The cache holds the display list,
// and a child transcript outlives that cap, so the hundredth subagent of a
// session has to answer exactly like the first. The read is one indexed column,
// and only a restart event asks for it.
//
// Scoped to THIS sink's children, so the answer can only be a span from the
// transcript the caller owns. A child of another root misses.
//
// An unknown id is a MISS ("" and no error), the same answer the caller gets for
// a child that carries no span. A read that FAILS returns the error, because a
// caller must not read a database it could not reach as "no such child".
func (s *agentOutputSink) ChildSpawnSpan(childAgentID string) (string, error) {
	if childAgentID == "" {
		return "", nil
	}
	span, err := s.h.queries.GetChildAgentSpawnSpan(s.h.bgTaskCtx(), db.GetChildAgentSpawnSpanParams{
		ID:            childAgentID,
		ParentAgentID: sqlString(s.agentID),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read spawn span of child %s: %w", childAgentID, err)
	}
	return span, nil
}

// resolveStoredChildAgent resolves the stored child without changing its registry linkage.
// Native keys and spawn spans each survive a restart before registry publication.
// Database reads stay outside the display cache lock. The caller validates ownership before linking.
func (s *agentOutputSink) resolveStoredChildAgent(ctx context.Context, spec agent.ChildAgentSpec) (string, error) {
	spawnSpanID, providerChildKey, title := spec.SpawnSpanID, spec.ProviderChildKey, spec.Title
	rowKey := bgtask.NormalizeRowKey(providerChildKey)
	cache := s.h.bgTaskCache(s.rootAgentID)

	// 1. Fast path: cache hit under the lock. ensureSeededLocked runs once (the
	//    seeded flag makes later calls cheap), so it stays inside this brief
	//    locked section.
	cache.Mu.Lock()
	if err := cache.on(s.h.queries, s.rootAgentID).ensureSeededLocked(ctx); err != nil {
		cache.Mu.Unlock()
		return "", err
	}
	if providerChildKey != "" {
		if idx := cache.indexOf(rowKey); idx >= 0 && cache.Rows[idx].ChildAgentID != "" {
			cid := cache.Rows[idx].ChildAgentID
			cache.Mu.Unlock()
			return cid, nil
		}
	}
	cache.Mu.Unlock()

	// 2. DB work OUTSIDE the lock.
	// The retained row is the SECOND answer to the fast path's question, and it
	// belongs here rather than above it: the store read is DB work, and this
	// function exists to keep DB work off the per-root mutex.
	//
	// The display list alone answered "no transcript" for every linked row past
	// the cap, and the spawn-span lookup below then missed too, so the call
	// CREATED a second transcript and re-pointed the durable row at it. Codex
	// reaches that state routinely: a collab call that re-registers a closed
	// thread hands EnsureChildAgent a NEW spawn span, so the spawn span cannot
	// find the child the first run made. This reads the row without re-admitting
	// it, which is right -- a question is not the activity that earns a place in
	// the sidebar.
	if providerChildKey != "" {
		row, found, err := s.h.loadStoredBgTask(ctx, s.h.queries, s.rootAgentID, rowKey)
		if err != nil {
			return "", err
		}
		if found && row.ChildAgentID != "" {
			return row.ChildAgentID, nil
		}
	}
	if providerChildKey != "" {
		existing, err := s.h.queries.GetChildAgentByProviderKey(ctx, db.GetChildAgentByProviderKeyParams{
			ParentAgentID: sqlString(s.agentID), ProviderChildKey: providerChildKey,
		})
		if err == nil {
			return existing.ID, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	// Fallback: GetChildAgentBySpawnSpan covers a worker restart between the
	// agent-row insert and the registry upsert. parent_agent_id for the new row
	// is THIS sink's agentID (works for grandchild spawns too: the spawn span
	// lives in this sink's own transcript).
	if existing, err := s.h.queries.GetChildAgentBySpawnSpan(ctx, db.GetChildAgentBySpawnSpanParams{
		ParentAgentID: sqlString(s.agentID),
		SpawnSpanID:   spawnSpanID,
	}); err == nil {
		return existing.ID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	// Create the child agent row. Copy working_dir/home_dir/provider from THIS
	// sink's agent row.
	parent, err := s.h.queries.GetAgentByID(ctx, s.agentID)
	if err != nil {
		return "", fmt.Errorf("get parent agent for child spawn: %w", err)
	}
	childID := id.Generate()
	// The `agents` row must never hold a blank title: it is the tab label the
	// user reads when the subagent transcript opens, and nothing rewrites it
	// later (a second EnsureChildAgent for the same spawn finds this row and
	// only re-links the registry). A blank title arrives two ways, and both
	// are routine: the provider had none to give (Claude's
	// routeSubagentMessage passes "" by design, and Codex's collabChildTitle
	// answers "" for a thread it has not titled yet), or cleaning removed
	// every character of the one it gave. Both take the SAME fallback
	// OpenAgent takes, so one rule titles every untitled agent row, and two
	// untitled subagents stay apart in the tab strip -- a fixed literal would
	// label them identically.
	//
	// The insert is the ONLY write of this column from a provider title, and
	// that is deliberate. A later provider title cannot update the row,
	// because `agents.title` is also where a user's rename of the child tab
	// lands (RenameAgent writes the same column) and the row carries no mark
	// that separates a name the user chose from one the model sent. Codex
	// calls EnsureChildAgent once per collab-state event for the whole run, so
	// an updating write would restore the provider's title over the user's
	// rename on the next event, every time.
	//
	// The cost is a real one and it is paid here: a child that an out-of-order
	// spawn created with no title (Claude's routeSubagentMessage) keeps the
	// pooled name on its TAB even after a later task_started supplies the real
	// description. That description is not lost -- the later
	// EnsureChildAgent links it into the registry row, which is what the
	// background-tasks sidebar reads.
	rowTitle := title
	if rowTitle == "" {
		rowTitle = pickAgentTitle()
	}
	if err := s.h.queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:               childID,
		ParentAgentID:    sqlString(s.agentID),
		SpawnSpanID:      spawnSpanID,
		ProviderChildKey: providerChildKey,
		Options:          spec.Options.Marshal(),
		WorkingDir:       parent.WorkingDir,
		HomeDir:          parent.HomeDir,
		Title:            rowTitle,
		AgentProvider:    parent.AgentProvider,
		AgentSessionID:   spec.AgentSessionID,
	}); err != nil {
		if providerChildKey != "" {
			existing, readErr := s.h.queries.GetChildAgentByProviderKey(ctx, db.GetChildAgentByProviderKeyParams{
				ParentAgentID: sqlString(s.agentID), ProviderChildKey: providerChildKey,
			})
			if readErr == nil {
				return existing.ID, nil
			}
			if !errors.Is(readErr, sql.ErrNoRows) {
				return "", fmt.Errorf("read native child after creation failed: %w", readErr)
			}
		}
		// UNIQUE violation on idx_agents_spawn_span (race / replay): re-read.
		if existing, rerr := s.h.queries.GetChildAgentBySpawnSpan(ctx, db.GetChildAgentBySpawnSpanParams{
			ParentAgentID: sqlString(s.agentID),
			SpawnSpanID:   spawnSpanID,
		}); rerr == nil {
			childID = existing.ID
		} else {
			return "", fmt.Errorf("create child agent: %w", err)
		}
	}
	// The caller validates the resolved child before linking its registry row.
	return childID, nil
}

// linkRegistryRow links a validated child under the cache lock.
// A prior linkage remains intact. The caller publishes the returned snapshot after the lock is released.
func (s *agentOutputSink) linkRegistryRow(cache *bgTaskCache, rowKey, childID, title string) ([]bgtask.Item, error) {
	if rowKey == "" {
		return nil, nil
	}
	// Read retained rows before the display lock. A row can outlive the display cap.
	// A failed read cannot establish that the row is unlinked.
	stored, storedFound, err := s.h.loadStoredBgTask(s.h.bgTaskCtx(), s.h.queries, s.rootAgentID, rowKey)
	if err != nil {
		return nil, fmt.Errorf("read the child registry linkage: %w", err)
	}
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	// Another caller can link the same row while this caller reads the database.
	if idx := cache.indexOf(rowKey); idx >= 0 {
		if cache.Rows[idx].ChildAgentID != "" {
			return nil, nil
		}
	} else if storedFound && stored.ChildAgentID != "" {
		return nil, nil
	}
	return s.ensureRegistryRowLocked(cache, rowKey, childID, title)
}

// ensureRegistryRowLocked stores the child's registry row and returns a changed snapshot.
// The caller must hold cache.Mu and publish the snapshot after it releases that lock.
func (s *agentOutputSink) ensureRegistryRowLocked(cache *bgTaskCache, rowKey, childID, title string) ([]bgtask.Item, error) {
	task := bgtask.Upsert{
		RowKey:        rowKey,
		Kind:          bgtask.KindSubagent,
		ChildAgentID:  childID,
		ParentAgentID: s.agentID,
		Title:         title,
		Status:        bgtask.StatusRunning,
	}
	// This linkage preserves an existing final status.
	change, err := s.h.applyBackgroundTaskUpsertLocked(cache, s.rootAgentID, task)
	if err != nil {
		return nil, fmt.Errorf("store the child registry linkage: %w", err)
	}
	if change.changed {
		return change.rows, nil
	}
	return nil, nil
}

// ChildSink returns a ProviderServices value for the child transcript. The
// child sink has its OWN span tracker (registered with kind=child so cleanup
// and the orphan sweep distinguish it from a root tracker); transcript
// primitives act on the child. Registry primitives on a child sink write under
// the same ROOT owner.
//
// Per-provider child-span contract (provider-capability-driven, not enforced
// by the interface):
//   - Claude, Codex: full per-child span lifecycle — Open/Close/Reserve/
//     SetType/GetType driven through the child sink.
//   - ACP (every ACP family): child transcripts are persisted flat
//     (SpanInfo{}); the child tracker is created but quiescent. ACP's own
//     tool-call spans run on the ROOT sink.
//   - Pi: no child sink; child linkage is registry-only.
func (s *agentOutputSink) ChildSink(childAgentID string) agent.ProviderServices {
	s.childMu.Lock()
	defer s.childMu.Unlock()
	if s.childSinks != nil {
		if c, ok := s.childSinks[childAgentID]; ok {
			return agent.NewProviderServices(c)
		}
	}
	child := &agentOutputSink{
		h:             s.h,
		root:          s.turnPublisherSink(),
		agentID:       childAgentID,
		rootAgentID:   s.rootAgentID,
		agentProvider: s.agentProvider,
		plugin:        s.plugin,
		tracker:       s.h.childTracker(childAgentID),
	}
	child.restoreMessageSession()
	child.progress = newGenerationProgressPublisher(func(info map[string]interface{}) {
		s.h.broadcastAgentSessionInfo(childAgentID, info)
	}, child.currentMessageSessionID)
	if s.progressClosed {
		child.progress.close()
		return agent.NewProviderServices(child)
	}
	if s.childSinks == nil {
		s.childSinks = make(map[string]*agentOutputSink)
	}
	s.childSinks[childAgentID] = child
	s.h.sinksByAgent.Store(childAgentID, child)
	return agent.NewProviderServices(child)
}

func (s *agentOutputSink) PersistChildMessage(childAgentID string, source leapmuxv1.MessageSource, content []byte, span agent.SpanInfo) error {
	return s.ChildSink(childAgentID).PersistMessage(source, agent.MessageContent{Original: content}, span)
}

func (s *agentOutputSink) PersistChildTurnEnd(childAgentID string, content agent.MessageContent, span agent.SpanInfo) error {
	return s.ChildSink(childAgentID).PersistTurnEnd(content, span)
}

// PersistChildPrompt writes the spawn prompt as the child transcript's first
// message. See the ProviderServices doc for the contract; the emptiness check is what
// makes it idempotent, and it is deliberately a READ of the child's max seq
// rather than a flag: a worker restart loses a flag but not the transcript.
func (s *agentOutputSink) PersistChildPrompt(childAgentID, prompt string) error {
	if childAgentID == "" || strings.TrimSpace(prompt) == "" {
		return nil
	}
	maxSeq, err := s.h.queries.GetMaxSeqByAgentID(s.h.bgTaskCtx(), childAgentID)
	if err != nil {
		return fmt.Errorf("read child transcript head: %w", err)
	}
	if maxSeq > 0 {
		// The subagent already spoke. Prepending is not possible (seq is
		// append-only) and appending would put the instruction BELOW the work it
		// asked for, so say nothing.
		return nil
	}
	// The same envelope a typed user message uses, so the renderer needs no new
	// shape: markdown body, USER source, no span.
	content, err := userMessageContent(prompt)
	if err != nil {
		return fmt.Errorf("marshal child prompt: %w", err)
	}
	return s.ChildSink(childAgentID).PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: content}, agent.SpanInfo{})
}

// userMessageContent encodes text as a transcript's user-message envelope: the
// shape the frontend classifies as user_content and renders as markdown.
//
// The one encoder for every user row the service writes -- a child's opening
// prompt, a message the parent delivered to a subagent, a typed message, a
// resend, and each backend-synthesized row. The shape is a wire contract with
// the renderer, so five hand-written copies of it could be changed in four
// places and drift in the fifth.
//
// Not for a message that carries attachments: that row is a different envelope
// ({content, attachments}) and its one caller builds it inline.
func userMessageContent(text string) ([]byte, error) {
	return json.Marshal(map[string]string{"content": text})
}

// PersistChildUserMessage appends a delivered message to a child transcript.
// See the ProviderServices doc for the contract. Unlike PersistChildPrompt there is
// NO emptiness guard: this message belongs wherever the transcript currently
// ends, which is the point of it.
func (s *agentOutputSink) PersistChildUserMessage(childAgentID, text string) error {
	if childAgentID == "" || strings.TrimSpace(text) == "" {
		return nil
	}
	content, err := userMessageContent(text)
	if err != nil {
		return fmt.Errorf("marshal child user message: %w", err)
	}
	return s.ChildSink(childAgentID).PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, agent.MessageContent{Original: content},
		agent.SpanInfo{MarkType: leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE})
}

// CleanupChildAgent releases the per-child service state for a child that has
// closed permanently. This sink is the child's DIRECT PARENT: the cached child
// sink lives in s.childSinks, so prune it here in O(1) instead of scanning
// h.rootSinks to find the owning root. The per-agent maps (span tracker,
// todos, ...) live on the handler and are keyed by child id, so they go
// through cleanupChildMaps.
//
// Precondition: the receiver is the child's direct parent (the sink that
// cached the child via ChildSink). A caller that resolves a child only by id,
// without the parent sink, has no single correct parent to delete from and
// must not use this method.
//
// cleanupChildMaps runs BEFORE the childSinks delete. If a concurrent
// ChildSink re-caches the child after the registry entry is gone but before
// the cache delete, it creates a FRESH registry entry that the already-run
// cleanupChildMaps leaves intact — the re-cached sink binds to a live tracker,
// not an orphan. Reversing the order would let cleanupChildMaps orphan a
// sink that a racing ChildSink just cached.
//
// Idempotent; a no-op when the child was never cached.
func (s *agentOutputSink) CleanupChildAgent(childAgentID string) {
	s.h.cleanupChildMaps(childAgentID)
	if child := s.detachChildSink(childAgentID); child != nil {
		agentIDs := child.closeProgressTree()
		for _, id := range agentIDs {
			s.h.sinksByAgent.Delete(id)
		}
		s.h.clearProgressFor(agentIDs)
	} else {
		s.h.sinksByAgent.Delete(childAgentID)
	}
}

// --- Registry write primitives on the sink ---

// normalizeRowKey returns the row key to address the registry by, and says so
// once when the provider's own key could not be used.
//
// The log line is the only trace a derived key leaves. The key it stores is a
// stable digest, so the sidebar row appears and stays addressable, but its
// LABEL falls back to that digest when the provider sent no title -- and
// nothing else on the path would tell an operator why a row carries a hash as
// its key. `what` identifies the call site, because a rename normalizes two keys
// and the reason can differ between them.
func (s *agentOutputSink) normalizeRowKey(rowKey, what string) string {
	normalized := bgtask.NormalizeRowKey(rowKey)
	if normalized != rowKey {
		slog.Warn("derived a registry row key from an unusable provider key",
			"what", what,
			"agent_id", s.rootAgentID,
			"provider", s.agentProvider.String(),
			"row_key", normalized,
			"reason", bgtask.ValidateRowKey(rowKey))
	}
	return normalized
}

// applyAndBroadcast publishes committed registry changes after the cache lock is released.
func (s *agentOutputSink) applyAndBroadcast(rowKey string, apply func(rootAgentID, key string) (registryChange, error)) error {
	// THE CHOKEPOINT for every registry mutation, which is why the key is
	// normalized here rather than at each caller: a key that normalizes on one
	// path and not another opens the row under one string and closes it under
	// another, leaving the first Running for the life of the process.
	rowKey = s.normalizeRowKey(rowKey, "registry row key")
	change, err := apply(s.rootAgentID, rowKey)
	if err != nil {
		return err
	}
	if change.changed {
		s.h.broadcastBackgroundTasks(s.rootAgentID, change.rows)
		// A registry row moving in or out of an active status changes the root's
		// answer AND the answer of the child that row stands for, so the whole
		// tree is republished. Both refreshes are edge-triggered, so the common
		// case -- a row updating while the set of ACTIVE rows is unchanged --
		// broadcasts nothing. Safe here and not one line earlier: the appliers
		// released the cache lock before returning, and refreshing reads that
		// same cache.
		s.h.refreshActivityTree(s.rootAgentID, settleHeld)
	}
	return nil
}

func (s *agentOutputSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	// The closure takes its key from applyAndBroadcast rather than from the
	// Upsert it closes over. applyAndBroadcast NORMALIZES, so the two are not
	// always the same string, and `task` carries the row key a second time --
	// the upsert would have written the raw provider key into the row while
	// every status change, close and rename addressed the normalized one, and
	// the task would sit Running for the life of the process.
	//
	// The parent agent id is the agent that owns THIS sink. Providers that
	// spawn subagents don't need to thread it through every call site -- the
	// sink knows its own identity. For a root sink this is the root; for a
	// child sink it's the child (correct for grandchild spawns).
	if task.ParentAgentID == "" {
		task.ParentAgentID = s.agentID
	}
	return s.applyAndBroadcast(task.RowKey, func(root, key string) (registryChange, error) {
		task.RowKey = key
		return s.h.applyBackgroundTaskUpsert(root, task)
	})
}

func (s *agentOutputSink) UpdateBackgroundTaskStatus(rowKey string, status bgtask.Status, activeForm string) error {
	return s.applyAndBroadcast(rowKey, func(root, key string) (registryChange, error) {
		return s.h.applyBackgroundTaskStatus(root, key, status, activeForm)
	})
}

func (s *agentOutputSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	return s.applyAndBroadcast(rowKey, func(root, key string) (registryChange, error) {
		return s.h.applyBackgroundTaskClose(root, key, status)
	})
}

func (s *agentOutputSink) LookupBackgroundTask(rowKey string) (string, bgtask.Status, bool, error) {
	// The zero Status is StatusUnspecified, so a miss returns that alongside
	// ok=false. Callers must still read ok: the two are different answers, and
	// only ok tells "no such row" from "a row with no status".
	var noStatus bgtask.Status
	if rowKey == "" {
		return "", noStatus, false, nil
	}
	// Apply the same complete-key derivation as every registry mutation.
	// A long native key must find the row that its earlier upsert created.
	rowKey = s.normalizeRowKey(rowKey, "child registry lookup key")
	cache := s.h.bgTaskCache(s.rootAgentID)
	cache.Mu.Lock()
	// The seed failure is RETURNED, not folded into ok=false. It runs on the
	// first registry touch of a process, which is exactly the state a worker
	// restart leaves behind -- so the one call most likely to hit it is a revive
	// for a subagent that finished in the previous process, and answering "no
	// such row" there closes a live span and leaves the row finished.
	if err := cache.on(s.h.queries, s.rootAgentID).ensureSeededLocked(s.h.bgTaskCtx()); err != nil {
		cache.Mu.Unlock()
		return "", noStatus, false, fmt.Errorf("seed background tasks for %s: %w", s.rootAgentID, err)
	}
	if idx := cache.indexOf(rowKey); idx >= 0 {
		childID, status := cache.Rows[idx].ChildAgentID, cache.Rows[idx].Status
		cache.Mu.Unlock()
		return childID, status, true, nil
	}
	// The lock is released BEFORE the DB read, the way resolveStoredChildAgent
	// releases it around its own: this miss path runs on every FIRST task_started
	// too (no row exists yet), so holding the mutex across the round trip would
	// serialize a burst of spawns behind one another. Nothing is mutated here,
	// and the write order elsewhere is DB-then-cache, so an insert that lands in
	// the gap is visible to this read rather than lost by it.
	cache.Mu.Unlock()
	// The cache holds only what the sidebar shows -- the newest MaxTasks rows of
	// each kind -- and a row that carries a child transcript outlives that cap in
	// the table. So a cache miss is not an answer yet: past MaxTasks finished
	// subagents, EVERY revive of an older one would read "no such row", open a
	// second transcript, and leave the real one unreachable. Fall through to the
	// PRIMARY KEY, which is one indexed point lookup.
	//
	// This READS the retained row without re-admitting it to the display list,
	// unlike the appliers: a lookup answers a question, and a question is not the
	// activity that earns a place in the sidebar.
	row, found, err := s.h.loadStoredBgTask(s.h.bgTaskCtx(), s.h.queries, s.rootAgentID, rowKey)
	if err != nil {
		// A DB failure stays the THIRD answer, distinct from a miss, for the same
		// reason the seed failure above does: a caller that reads "no such row"
		// from a database it could not read treats a live subagent as brand new.
		return "", noStatus, false, err
	}
	if !found {
		return "", noStatus, false, nil
	}
	return row.ChildAgentID, row.Status, true, nil
}

// ReviveBackgroundTask returns a finished row to running and clears the prior run's descriptive state.
// A failed registry write returns an error so the provider can retain its pending restart.
func (s *agentOutputSink) ReviveBackgroundTask(rowKey string) error {
	return s.applyAndBroadcast(rowKey, s.h.applyBackgroundTaskRevive)
}

// RenameBackgroundTask atomically re-keys a row from oldKey to newKey under the
// root owner, preserving status, child linkage, and final state. A no-op
// when the old row is absent or newKey is empty. Used by ACP providers that
// learn the stable child id only on the final update (OpenCode), so a single
// row tracks the whole lifecycle instead of a spawn row orphaned Running while
// a separately-keyed row closes.
//
// Order matters: seed the cache, then mutate it. On a cold cache (after a
// worker restart, or the first registry touch for a root), renameRowKeyLocked
// sees an empty Rows slice and returns false, so the DB rename must NOT be
// conditional on the in-memory rename succeeding -- seed first, then re-key.
// Both keys go through the SAME normalization every other registry primitive
// uses, and that is what makes the rename find its own row. The spawn row was
// opened under the key `applyAndBroadcast` stored, so an unusable toolCallId
// that opened the row under a derived key must derive the same one here --
// normalizing on one path and not the other is exactly how a rename stops
// finding its own row.
// The DB write runs BEFORE the cache mutation commits: on a DB error the cache
// stays keyed at oldKey (matching the DB), not the half-renamed newKey.
func (s *agentOutputSink) RenameBackgroundTask(oldKey, newKey string) error {
	if oldKey == "" || newKey == "" {
		return nil
	}
	oldKey = s.normalizeRowKey(oldKey, "rename from")
	newKey = s.normalizeRowKey(newKey, "rename to")
	ctx := s.h.bgTaskCtx()
	var pendingBroadcast []bgtask.Item
	cache := s.h.bgTaskCache(s.rootAgentID)
	cache.Mu.Lock()
	reg := cache.on(s.h.queries, s.rootAgentID)
	if err := reg.ensureSeededLocked(ctx); err != nil {
		cache.Mu.Unlock()
		return err
	}
	// (owner_agent_id, row_key) is the PRIMARY KEY, so a rename onto an OCCUPIED
	// key fails the UPDATE. That collision is reachable: a session history replay
	// re-creates the spawn row under the toolCallId while the pre-restart row
	// already sits, closed, under the session id. Letting the UPDATE fail left the
	// re-created row Running for the life of the process, which pinned the
	// parent's thinking indicator. The row already at newKey is the complete one --
	// it carries the lifecycle that reached the rename -- so the DUPLICATE at
	// oldKey loses and leaves the display list.
	occupied, err := reg.queries.CountAgentBackgroundTasksByRowKey(ctx, db.CountAgentBackgroundTasksByRowKeyParams{
		OwnerAgentID: s.rootAgentID,
		RowKey:       newKey,
	})
	if err != nil {
		cache.Mu.Unlock()
		return err
	}
	if occupied > 0 {
		if oldKey == newKey {
			cache.Mu.Unlock()
			return nil
		}
		// Does the WINNER already carry the loser's child transcript? Read both
		// rows through the retention loader, so a row the display cap evicted still
		// answers. A read failure leaves supersededChild false, which retains the
		// loser -- the conservative half, because an orphaned child costs a
		// transcript nobody can reopen while a retained row costs one stale entry.
		supersededChild := false
		if loser, found, err := s.h.loadStoredBgTask(ctx, reg.queries, reg.ownerID, oldKey); err != nil {
			slog.Warn("bgtask rename: read the losing row failed",
				"owner", s.rootAgentID, "row_key", oldKey, "error", err)
		} else if found && loser.ChildAgentID != "" {
			winner, wfound, err := s.h.loadStoredBgTask(ctx, reg.queries, reg.ownerID, newKey)
			switch {
			case err != nil:
				slog.Warn("bgtask rename: read the winning row failed",
					"owner", s.rootAgentID, "row_key", newKey, "error", err)
			case wfound:
				supersededChild = winner.ChildAgentID == loser.ChildAgentID
			}
		}
		// deleteRowLocked, not a delete of this caller's own: whether the loser's
		// PERSISTED row goes with it is the question eviction asks, and the answer
		// is the same, so both ask registryRetention.keep through one function. A
		// row that carries a child transcript is the only index from that child
		// agent id back to (owner, row_key), so it is retained and only hidden.
		//
		// Claude's restart rename reaches this with a LINKED loser: a restarted
		// run whose forwarded envelope outran its task_started opened a pre-start
		// row under the original spawn span, and that row carries the child. The
		// ACP providers that rename (OpenCode, Kilo) drop child sessions over ACP
		// and never report a ChildAgentKey, so their loser is unlinked.
		//
		// A loser that carries the SAME child as the winner is deleted outright.
		// Retention exists to keep the one index from a child agent id back to
		// (owner, row_key); the winner already IS that index, so keeping the loser
		// preserves nothing and costs a permanent second row. Nothing closes it --
		// the run's result closes the winner -- so it survives every reclaim pass
		// (`child_agent_id = ''` excludes it, and it never reaches a finished
		// status in this process), and the next cold-start seed reads it back
		// beside the winner. The sidebar then lists one subagent twice, which is
		// the failure the rename exists to prevent.
		dropped, err := reg.deleteRowLocked(ctx, oldKey, supersededChild)
		if err != nil {
			cache.Mu.Unlock()
			return err
		}
		if dropped {
			pendingBroadcast = cache.snapshot()
		}
		cache.Mu.Unlock()
		if pendingBroadcast != nil {
			s.h.broadcastBackgroundTasks(s.rootAgentID, pendingBroadcast)
		}
		return nil
	}
	if _, err := reg.queries.RenameAgentBackgroundTask(ctx, db.RenameAgentBackgroundTaskParams{
		RowKey:       newKey,
		OwnerAgentID: s.rootAgentID,
		RowKey_2:     oldKey,
	}); err != nil {
		cache.Mu.Unlock()
		return err
	}
	// The DB row is now keyed at newKey; re-key the cache to match. If the row
	// was absent (a no-op rename), renameRowKeyLocked returns false and there is
	// nothing to broadcast.
	if cache.renameRowKeyLocked(oldKey, newKey) {
		pendingBroadcast = cache.snapshot()
	}
	cache.Mu.Unlock()
	if pendingBroadcast != nil {
		s.h.broadcastBackgroundTasks(s.rootAgentID, pendingBroadcast)
	}
	return nil
}

// applyBackgroundTaskUpsertLocked commits an upsert and its eviction together.
// A failed write restores the database and the display cache when the handler owns a database handle.
// A test handler without that handle restores its cache only.
// The caller must hold cache.Mu.
func (h *OutputHandler) applyBackgroundTaskUpsertLocked(cache *bgTaskCache, rootAgentID string, task bgtask.Upsert) (registryChange, error) {
	ctx := h.bgTaskCtx()
	reg := cache.on(h.queries, rootAgentID)
	var change registryChange
	err := reg.inTransactionLocked(ctx, h.db, func(tx bgTaskView) error {
		var err error
		change, err = mutateBackgroundTaskUpsertLocked(ctx, tx, task)
		return err
	})
	if err != nil {
		return registryChange{}, err
	}
	return change, nil
}

// mutateBackgroundTaskUpsertLocked uses one transaction view for every registry read and write.
// The caller owns the transaction, cache restoration, and cache.Mu.
func mutateBackgroundTaskUpsertLocked(ctx context.Context, reg bgTaskView, task bgtask.Upsert) (registryChange, error) {
	cache := reg.cache
	rootAgentID := reg.ownerID
	// Providers supply titles without length or character restrictions.
	// Apply the Worker's common title rule to every upsert and child linkage.
	// Upsert.Clean also preserves the distinct handling of command titles.
	// Clean before replay comparison so an unchanged raw title does not cause repeated writes and broadcasts.
	task = task.Clean()
	if err := reg.ensureSeededLocked(ctx); err != nil {
		return registryChange{}, err
	}
	// Retained rows remain existing rows after they leave the display list.
	// Read them through the registry view so replay preserves their status, title, and creation time.
	// Only an absent row requires space for a new row.
	existing, idx, found, err := reg.findRowLocked(ctx, task.RowKey)
	if err != nil {
		return registryChange{}, err
	}
	// Use one millisecond timestamp for the database and cache so warm and cold reads agree.
	now := nowMillis()
	merged := task.ToItem()
	merged.UpdatedAt = now
	if found {
		merged.CreatedAt = existing.CreatedAt
		merged.EndedAt = existing.EndedAt
		// Preserve descriptive fields that a partial upsert leaves blank.
		// Status remains explicit. The database rejects StatusUnspecified instead of inheriting an old status.
		merged = merged.PreservingBlanksFrom(existing)
		// A final status remains final after a late or replayed active update.
		// Preserve its end time while allowing descriptive fields to change.
		if existing.Status.IsFinished() && !merged.Status.IsFinished() {
			merged.Status = existing.Status
			merged.EndedAt = existing.EndedAt
		} else if merged.Status.IsFinished() && !existing.Status.IsFinished() {
			// A transition into a final status stamps ended_at.
			merged.EndedAt = now
		}
		// Exclude the current update time from replay comparison.
		// Otherwise an unchanged row would cause another write and broadcast.
		if existing.WithUpdatedAt(merged.UpdatedAt) == merged {
			return registryChange{rows: cache.snapshot()}, nil
		}
		// Admit a retained row only after comparison confirms a change.
		// Admission can evict a displayed row. An unchanged replay must not change the sidebar without a broadcast.
		// The enclosing transaction restores admission if the write fails.
		if idx < 0 {
			if idx, err = reg.admitRowLocked(ctx, existing); err != nil {
				return registryChange{}, err
			}
		}
	} else {
		// Each task kind owns its display pool, so shell tasks cannot evict subagent rows.
		// A full pool evicts its oldest finished row, or its oldest active row when none finished.
		// Keep linked rows in storage so the user can still open their child transcripts.
		// Delete unlinked rows from storage with the same transaction as the new insert.
		// Always admit a new child linkage. Refusing it at the cap leaves its already stored transcript unreachable from the sidebar.
		bucket := int64(merged.Kind)
		evictedRow, dropped, err := reg.makeRoomLocked(ctx, bucket)
		if err != nil {
			return registryChange{}, err
		}
		if dropped && !evictedRow.Status.IsFinished() {
			slog.Warn("The background task registry reached its display cap. It drops the oldest active display row because no row finished.",
				"owner", rootAgentID, "row_key", task.RowKey, "kind", bucket, "cap", bgtask.MaxTasks,
				"evicted_row_key", evictedRow.RowKey, "retained_in_store", evictedRow.ChildAgentID != "")
		}
	}
	if !found {
		merged.CreatedAt = now
	}
	if merged.Status.IsFinished() && merged.EndedAt.IsZero() {
		merged.EndedAt = now
	}
	if err := reg.queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID:   rootAgentID,
		RowKey:         task.RowKey,
		Seq:            cache.nextSeq,
		Kind:           leapmuxv1.BackgroundTaskKind(merged.Kind),
		ChildAgentID:   merged.ChildAgentID,
		ParentAgentID:  merged.ParentAgentID,
		GroupKey:       merged.GroupKey,
		GroupLabel:     merged.GroupLabel,
		Title:          merged.Title,
		TitleIsCommand: ptrconv.BoolToInt64(merged.TitleIsCommand),
		Description:    merged.Description,
		ActiveForm:     merged.ActiveForm,
		Status:         leapmuxv1.BackgroundTaskStatus(merged.Status),
		// INSERT sets creation time. Conflict updates preserve it.
		// Both paths set update time from the same timestamp as the cache.
		CreatedAt: sqltime.NewSQLiteTime(merged.CreatedAt),
		UpdatedAt: sqltime.NewSQLiteTime(now),
		EndedAt:   sqltime.SQLiteNullTimeOf(merged.EndedAt),
	}); err != nil {
		return registryChange{}, err
	}
	if !found {
		cache.Rows = append(cache.Rows, merged)
		cache.nextSeq++
	} else {
		cache.Rows[idx] = merged
	}
	return registryChange{rows: cache.snapshot(), changed: true}, nil
}

// sqlString converts a Go string to a sql.NullString where "" is NULL and a
// non-empty value is valid. agents.parent_agent_id is nullable: a child's own
// parent is always set, so callers pass a non-empty id.
func sqlString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
