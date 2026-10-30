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
		OutputGeneration: r.OutputGeneration,
		RowKey:           r.RowKey,
		ChildAgentID:     r.ChildAgentID,
		ParentAgentID:    r.ParentAgentID,
		Kind:             bgtask.Kind(r.Kind),
		GroupKey:         r.GroupKey,
		GroupLabel:       r.GroupLabel,
		Title:            r.Title,
		TitleIsCommand:   ptrconv.Int64ToBool(r.TitleIsCommand),
		Description:      r.Description,
		ActiveForm:       r.ActiveForm,
		Status:           bgtask.Status(r.Status),
		CreatedAt:        r.CreatedAt.Time,
		UpdatedAt:        r.UpdatedAt.Time,
		EndedAt:          endedAt,
	}
}

// bgTaskOps supplies the shared operations for background task caches.
// Each database operation uses the query handle supplied by its caller.
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
	// changed reports visible state. An ownership-only claim can still write storage.
	changed  bool
	eviction *backgroundTaskEviction
}

// applyBackgroundTaskUpsert is the persist-mutate-broadcast for an upsert. It
// writes the DB row, mutates the cache in place, runs cap-eviction, and
// broadcasts. A same-generation, byte-identical replay skips both the write
// and the broadcast. A newer generation still persists its ownership claim
// without changing visible task data.
func (h *OutputHandler) applyBackgroundTaskUpsert(rootAgentID string, generation int64, task bgtask.Upsert) (registryChange, error) {
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	return h.applyBackgroundTaskUpsertLocked(cache, rootAgentID, generation, task)
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
	rootAgentID, rowKey string, generation int64,
	mutate func(context.Context, bgTaskView, bgtask.Item, bool, func() (int, error)) error,
) (registryChange, error) {
	return h.mutateBackgroundTaskRegistry(rootAgentID, generation, []string{rowKey}, func(ctx context.Context, reg bgTaskView) error {
		row, found, err := claimBackgroundTaskGenerationLocked(ctx, reg, rowKey, generation)
		if err != nil {
			return err
		}
		idx := reg.cache.indexOf(rowKey)
		if !found {
			reg.cache.dropRowLocked(rowKey)
			reg.cache.forgetEvictedActiveLocked(rowKey)
			return nil
		}
		if idx >= 0 {
			reg.cache.Rows[idx] = row
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
	})
}

// applyBackgroundTaskStatus updates a row's status + active_form without
// closing it (used for running-progress updates). A no-op when the row is
// absent or the patch is a no-op.
func (h *OutputHandler) applyBackgroundTaskStatus(rootAgentID, rowKey string, generation int64, status bgtask.Status, activeForm string) (registryChange, error) {
	activeForm = validate.StripUnreadable(activeForm, bgtask.LabelByteLimit)
	return h.withBgTaskRow(rootAgentID, rowKey, generation, func(ctx context.Context, reg bgTaskView, existing bgtask.Item, _ bool, admit func() (int, error)) error {
		// Finished outcomes absorb active replays. An unchanged operation still claims its generation.
		if existing.Status.IsFinished() && !status.IsFinished() || existing.Status == status && existing.ActiveForm == activeForm {
			return nil
		}
		idx, err := admit()
		if err != nil {
			return err
		}
		now := nowMillis()
		rows, err := reg.queries.UpdateAgentBackgroundTaskStatus(ctx, db.UpdateAgentBackgroundTaskStatusParams{
			OutputGeneration: generation, Status: leapmuxv1.BackgroundTaskStatus(status), ActiveForm: activeForm,
			UpdatedAt: sqltime.NewSQLiteTime(now), OwnerAgentID: rootAgentID, RowKey: rowKey,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return errBackgroundTaskSuperseded
		}
		endedAt := existing.EndedAt
		// The final status and its first end stamp commit in the same transaction.
		// An existing end stamp is an ordinary idempotent no-op, not a generation refusal.
		if status.IsFinished() && endedAt.IsZero() {
			rows, err = reg.queries.StampAgentBackgroundTaskEndedAt(ctx, db.StampAgentBackgroundTaskEndedAtParams{
				OutputGeneration: generation, MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
				EndedAt: sqltime.SQLiteNullTimeOf(now), OwnerAgentID: rootAgentID, RowKey: rowKey,
			})
			if err != nil {
				return err
			}
			if rows == 0 {
				stored, found, readErr := storedBackgroundTask(ctx, reg, rowKey)
				if readErr != nil {
					return readErr
				}
				if !found || stored.OutputGeneration > generation {
					return errBackgroundTaskSuperseded
				}
				if stored.Status != status || stored.EndedAt.IsZero() {
					return errors.New("the stored final task has no matching end stamp")
				}
				endedAt = stored.EndedAt
			} else {
				endedAt = now
			}
		}
		reg.cache.Rows[idx].Status = status
		reg.cache.Rows[idx].ActiveForm = activeForm
		reg.cache.Rows[idx].UpdatedAt = now
		reg.cache.Rows[idx].EndedAt = endedAt
		return nil
	})
}

// applyBackgroundTaskClose moves a row into a final status (stamps ended_at).
// The query's final-status threshold admits open rows, including Paused.
// A row that is already finished keeps its first outcome and end time.
func (h *OutputHandler) applyBackgroundTaskClose(rootAgentID, rowKey string, generation int64, status bgtask.Status) (registryChange, error) {
	return h.withBgTaskRow(rootAgentID, rowKey, generation, func(ctx context.Context, reg bgTaskView, existing bgtask.Item, _ bool, admit func() (int, error)) error {
		if existing.Status.IsFinished() {
			return nil
		}
		idx, err := admit()
		if err != nil {
			return err
		}
		now := nowMillis()
		rows, err := reg.queries.CloseAgentBackgroundTask(ctx, db.CloseAgentBackgroundTaskParams{
			OutputGeneration: generation, MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
			Status: leapmuxv1.BackgroundTaskStatus(status), EndedAt: sqltime.SQLiteNullTimeOf(now),
			UpdatedAt: sqltime.NewSQLiteTime(now), OwnerAgentID: rootAgentID, RowKey: rowKey,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return errBackgroundTaskSuperseded
		}
		reg.cache.Rows[idx].Status, reg.cache.Rows[idx].EndedAt, reg.cache.Rows[idx].UpdatedAt = status, now, now
		return nil
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
func (h *OutputHandler) applyBackgroundTaskRevive(rootAgentID, rowKey string, generation int64) (registryChange, error) {
	return h.withBgTaskRow(rootAgentID, rowKey, generation, func(ctx context.Context, reg bgTaskView, existing bgtask.Item, displayed bool, admit func() (int, error)) error {
		if !existing.Status.IsFinished() {
			return nil
		}
		now := nowMillis()
		rows, err := reg.queries.ReviveAgentBackgroundTask(ctx, db.ReviveAgentBackgroundTaskParams{
			OutputGeneration: generation, Status: leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
			MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus), UpdatedAt: sqltime.NewSQLiteTime(now),
			OwnerAgentID: rootAgentID, RowKey: rowKey,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			stored, found, readErr := storedBackgroundTask(ctx, reg, rowKey)
			if readErr != nil {
				return readErr
			}
			if found && stored.OutputGeneration > generation {
				return errBackgroundTaskSuperseded
			}
			_, err = h.adoptStoredBgTaskRowLocked(ctx, reg, rowKey, displayed, admit)
			return err
		}
		idx, err := admit()
		if err != nil {
			return err
		}
		reg.cache.Rows[idx].Status = bgtask.StatusRunning
		// Activity and description belong to the run that ended, not the revived run.
		reg.cache.Rows[idx].ActiveForm, reg.cache.Rows[idx].Description = "", ""
		reg.cache.Rows[idx].EndedAt, reg.cache.Rows[idx].UpdatedAt = time.Time{}, now
		return nil
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

// MarkAgentBackgroundTasksExited finalizes an offline or closing root using its stored cutoff.
// Live process exits use the exact physical output facet captured by the Manager instead.
func (h *OutputHandler) MarkAgentBackgroundTasksExited(rootAgentID string, stopped bool) {
	if h.shuttingDown.Load() {
		return
	}
	row, err := h.queries.GetAgentByID(h.bgTaskCtx(), rootAgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Warn("read the closing root output generation", "agent_id", rootAgentID, "error", err)
		return
	}
	if row.ParentAgentID.Valid {
		slog.Warn("refuse a virtual child's process exit", "agent_id", rootAgentID)
		return
	}
	operation := h.captureBackgroundTaskExit(rootAgentID, 0, row.OutputGeneration, backgroundTaskExitStatus(stopped))
	if operation == nil {
		return
	}
	queue := h.outputQueue(rootAgentID)
	queue.Append(agent.OutputWrite{Label: "finalize the closing root registry", Run: operation})
	if err := queue.Flush(); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		slog.Warn("retain closing background task output", "agent_id", rootAgentID, "error", err)
	}
}

func backgroundTaskExitStatus(stopped bool) bgtask.Status {
	if stopped {
		return bgtask.StatusStopped
	}
	return bgtask.StatusInterrupted
}

// CaptureBackgroundTaskExit keeps the original physical root after a replacement publishes.
func (s *agentOutputSink) CaptureBackgroundTaskExit(stopped bool) func() error {
	if s.root != nil {
		return nil
	}
	return s.h.captureBackgroundTaskExit(s.rootAgentID, 0, s.outputGeneration, backgroundTaskExitStatus(stopped))
}

// CaptureBackgroundTaskStartFailure cannot end another unregistered or newer physical attempt.
func (s *agentOutputSink) CaptureBackgroundTaskStartFailure() func() error {
	if s.root != nil {
		return nil
	}
	return s.h.captureBackgroundTaskExit(s.rootAgentID, s.outputGeneration, s.outputGeneration, bgtask.StatusInterrupted)
}

func (h *OutputHandler) enqueueBackgroundTaskExit(output agent.OutputEffectServices, stopped bool) error {
	if output == nil {
		return errors.New("the exited process supplied no output owner")
	}
	operation := output.CaptureBackgroundTaskExit(stopped)
	if operation == nil {
		return nil
	}
	queue := output.OutputQueue()
	if queue == nil {
		return errors.New("the exited process supplied no output queue")
	}
	queue.Append(agent.OutputWrite{Label: "finalize the exited process registry", Run: operation})
	// A storage failure leaves this exact callback at the head for the Worker's retry clock.
	if err := queue.Flush(); err != nil && !errors.Is(err, agent.ErrOutputInFlight) {
		slog.Warn("retain exited background task output", "error", err)
	}
	return nil
}

func (h *OutputHandler) captureBackgroundTaskExit(rootID string, minimumGeneration, generation int64, status bgtask.Status) func() error {
	if h.shuttingDown.Load() {
		return nil
	}
	return func() error {
		if h.shuttingDown.Load() {
			return nil
		}
		return h.applyBackgroundTaskExit(rootID, minimumGeneration, generation, status)
	}
}

func (h *OutputHandler) applyBackgroundTaskExit(rootID string, minimumGeneration, generation int64, status bgtask.Status) error {
	ctx := h.bgTaskCtx()
	cache := h.bgTaskCache(rootID)
	cache.Mu.Lock()
	reg := cache.on(h.queries, rootID)
	var before, snapshot []bgtask.Item
	err := reg.inTransactionLocked(ctx, h.db, func(tx bgTaskView) error {
		if err := tx.ensureSeededLocked(ctx); err != nil {
			return err
		}
		before = cache.snapshot()
		// An earlier retained Upsert can create its row after exit admission.
		// Use execution time and never stamp an end before an eligible stored creation.
		now := nowMillis()
		latest, err := tx.queries.GetLatestActiveAgentBackgroundTaskCreatedAtForGeneration(ctx, db.GetLatestActiveAgentBackgroundTaskCreatedAtForGenerationParams{
			OwnerAgentID: rootID, MinOutputGeneration: minimumGeneration, OutputGeneration: generation, MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
		})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if latest.After(now) {
				now = latest.Time
			}
			if _, err := tx.queries.MarkAgentBackgroundTasksEnded(ctx, db.MarkAgentBackgroundTasksEndedParams{
				OwnerAgentID: rootID, MinOutputGeneration: minimumGeneration, OutputGeneration: generation, Status: leapmuxv1.BackgroundTaskStatus(status),
				MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus), EndedAt: sqltime.SQLiteNullTimeOf(now), UpdatedAt: sqltime.NewSQLiteTime(now),
			}); err != nil {
				return err
			}
		}
		// Rebuild both projections from storage so the filtered sweep preserves newer hidden work.
		cache.seeded = false
		if err := tx.ensureSeededLocked(ctx); err != nil {
			return err
		}
		working, err := tx.queries.ListWorkingAgentBackgroundTaskKeys(ctx, db.ListWorkingAgentBackgroundTaskKeysParams{
			OwnerAgentID: rootID, PendingStatus: leapmuxv1.BackgroundTaskStatus(bgtask.StatusPending), RunningStatus: leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning),
		})
		if err != nil {
			return err
		}
		cache.evictedActive = nil
		for _, key := range working {
			if cache.indexOf(key) >= 0 {
				continue
			}
			if cache.evictedActive == nil {
				cache.evictedActive = make(map[string]struct{})
			}
			cache.evictedActive[key] = struct{}{}
		}
		snapshot = cache.snapshot()
		return nil
	})
	cache.Mu.Unlock()
	if err != nil {
		return err
	}
	if !bgTaskVisibleRowsEqual(before, snapshot) {
		h.broadcastBackgroundTasks(rootID, snapshot)
	}
	// Derive the current tree rather than announcing that a replacement process exited.
	h.refreshActivityTree(rootID, settleImmediate)
	return nil
}

// --- agentOutputSink: ProviderServices registry + child-transcript methods ---

// EnsureChildAgent resolves the stored child under this sink's agent.
// Replays and Worker restarts resolve the same native child or spawn span.
// NewRootSink in output.go specifies the registry root owner.
// SpawnSpanID uses agents.spawn_span_id and the unique parent/span index.
// ProviderChildKey can identify the child before its native spawn span arrives.
// A supplied native session must match the stored child before registry changes.
func (s *agentOutputSink) EnsureChildAgent(spec agent.ChildAgentSpec) (string, error) {
	if spec.SpawnSpanID == "" && spec.ProviderChildKey == "" {
		return "", errors.New("a child requires a native key or a spawn span")
	}
	if s.outputGeneration <= 0 {
		return "", errors.New("a native child writer requires a positive output generation")
	}
	ctx := s.h.bgTaskCtx()
	// Keep native identity intact. The registry derives its separate display key without truncation.
	rowKey := s.normalizeRowKey(spec.ProviderChildKey, "child registry key")
	// Provider titles need cleaning. Keep a blank registry title so an existing title survives.
	// The child insert supplies the same generated fallback that OpenAgent uses.
	spec.Title = validate.CleanName(spec.Title)
	cache := s.h.bgTaskCache(s.rootAgentID)
	cache.Mu.Lock()
	reg := cache.on(s.h.queries, s.rootAgentID)
	var childID string
	var change registryChange
	var eviction *backgroundTaskEviction
	err := reg.inTransactionLocked(ctx, s.h.db, func(tx bgTaskView) error {
		if rowKey != "" {
			// Reject a superseded source before seeding or changing child identity.
			row, found, err := storedBackgroundTask(ctx, tx, rowKey)
			if err != nil {
				return err
			}
			if found && row.OutputGeneration > s.outputGeneration {
				return errBackgroundTaskSuperseded
			}
			if err := tx.ensureSeededLocked(ctx); err != nil {
				return err
			}
		}
		before := cache.snapshot()
		var existing bgtask.Item
		var found bool
		var err error
		if rowKey != "" {
			existing, found, err = claimBackgroundTaskGenerationLocked(ctx, tx, rowKey, s.outputGeneration)
			if err != nil {
				return err
			}
		}
		childID, err = s.resolveStoredChildAgent(ctx, tx, spec)
		if err != nil {
			return err
		}
		child, err := tx.queries.GetAgentByID(ctx, childID)
		if err != nil {
			return err
		}
		if !child.ParentAgentID.Valid || child.ParentAgentID.String != s.agentID {
			return fmt.Errorf("the native child belongs to another parent: %w", agent.ErrChildIdentityRefused)
		}
		if spec.AgentSessionID != "" && child.AgentSessionID != spec.AgentSessionID {
			return fmt.Errorf("the child belongs to another native session: %w", agent.ErrChildIdentityRefused)
		}
		if spec.ProviderChildKey != "" {
			if child.ProviderChildKey != "" && child.ProviderChildKey != spec.ProviderChildKey {
				return fmt.Errorf("the child spawn span belongs to another native child: %w", agent.ErrChildIdentityRefused)
			}
			if child.ProviderChildKey == "" {
				if _, err := tx.queries.AdoptChildProviderKey(ctx, db.AdoptChildProviderKeyParams{
					ID: childID, ParentAgentID: sqlString(s.agentID), ProviderChildKey: spec.ProviderChildKey,
				}); err != nil {
					return fmt.Errorf("record native child key: %w", err)
				}
				// Verify the identity on the same transaction handle before linkage.
				child, err = tx.queries.GetAgentByID(ctx, childID)
				if err != nil {
					return err
				}
				if child.ProviderChildKey != spec.ProviderChildKey {
					return fmt.Errorf("the child spawn span belongs to another native child: %w", agent.ErrChildIdentityRefused)
				}
			}
		}
		if spec.SpawnSpanID != "" && spec.ProviderChildKey != "" {
			if _, err := tx.queries.AttachChildSpawnSpan(ctx, db.AttachChildSpawnSpanParams{
				ID: childID, ParentAgentID: sqlString(s.agentID), ProviderChildKey: spec.ProviderChildKey, SpawnSpanID: spec.SpawnSpanID,
			}); err != nil {
				return fmt.Errorf("attach native child spawn span: %w", err)
			}
		}
		if rowKey != "" {
			if found && existing.ChildAgentID != "" {
				if idx := cache.indexOf(rowKey); idx >= 0 {
					cache.Rows[idx] = existing
				}
			} else if err := mutateBackgroundTaskUpsertLocked(ctx, tx, s.outputGeneration, bgtask.Upsert{
				RowKey: rowKey, Kind: bgtask.KindSubagent, ChildAgentID: childID,
				ParentAgentID: s.agentID, Title: spec.Title, Status: bgtask.StatusRunning,
			}, &eviction); err != nil {
				return fmt.Errorf("store the child registry linkage: %w", err)
			}
		}
		change = registryChange{rows: cache.snapshot(), changed: !bgTaskVisibleRowsEqual(before, cache.Rows)}
		if change.changed {
			change.eviction = eviction
		}
		return nil
	})
	cache.Mu.Unlock()
	if errors.Is(err, errBackgroundTaskSuperseded) {
		return "", fmt.Errorf("the native child belongs to a newer physical output source: %w", agent.ErrChildIdentityRefused)
	}
	if err != nil {
		return "", err
	}
	if change.changed {
		s.h.broadcastBackgroundTasks(s.rootAgentID, change.rows)
		// Linking changes the child's activity. Refresh after the cache lock is released because refresh reads that cache.
		s.h.refreshActivityTree(s.rootAgentID, settleHeld)
	}
	s.logRegistryEviction(change)
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

// resolveStoredChildAgent resolves or creates the native identity on the caller's transaction.
// The caller holds the registry cache lock and validates identity before linking.
func (s *agentOutputSink) resolveStoredChildAgent(ctx context.Context, reg bgTaskView, spec agent.ChildAgentSpec) (string, error) {
	spawnSpanID, providerChildKey, title := spec.SpawnSpanID, spec.ProviderChildKey, spec.Title
	rowKey := bgtask.NormalizeRowKey(providerChildKey)
	// The stored linkage remains authoritative outside the display cap. Resolve
	// it without re-admission or a second transcript for a later native span.
	if providerChildKey != "" {
		row, found, err := storedBackgroundTask(ctx, reg, rowKey)
		if err != nil {
			return "", err
		}
		if found && row.ChildAgentID != "" {
			return row.ChildAgentID, nil
		}
	}
	if providerChildKey != "" {
		existing, err := reg.queries.GetChildAgentByProviderKey(ctx, db.GetChildAgentByProviderKeyParams{
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
	if existing, err := reg.queries.GetChildAgentBySpawnSpan(ctx, db.GetChildAgentBySpawnSpanParams{
		ParentAgentID: sqlString(s.agentID),
		SpawnSpanID:   spawnSpanID,
	}); err == nil {
		return existing.ID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	// Create the child agent row. Copy working_dir/home_dir/provider from THIS
	// sink's agent row.
	parent, err := reg.queries.GetAgentByID(ctx, s.agentID)
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
	if err := reg.queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
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
			existing, readErr := reg.queries.GetChildAgentByProviderKey(ctx, db.GetChildAgentByProviderKeyParams{
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
		if existing, rerr := reg.queries.GetChildAgentBySpawnSpan(ctx, db.GetChildAgentBySpawnSpanParams{
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
//   - ACP (every ACP family): each child conversation uses its child sink
//     for tool-call span lifecycle and tracking, independently of the root.
//   - Pi: no child sink; child linkage is registry-only.
func (s *agentOutputSink) ChildSink(childAgentID string) agent.ProviderServices {
	child := s.rawChildSink(childAgentID)
	return agent.NewOrderedProviderServices(child.OutputQueue(), agent.NewProviderServices(child))
}

func (s *agentOutputSink) rawChildSink(childAgentID string) *agentOutputSink {
	mutation := s.h.transcriptMutationMutex(s.rootAgentID)
	mutation.Lock()
	s.childMu.Lock()
	child, err := s.rawChildSinkLocked(childAgentID)
	s.childMu.Unlock()
	mutation.Unlock()
	if err != nil {
		slog.Warn("Read the provider session for the transcript", "agent_id", childAgentID, "error", err)
	}
	return child
}

// rawChildSinkLocked resolves a child under the root mutation lock and childMu.
func (s *agentOutputSink) rawChildSinkLocked(childAgentID string) (*agentOutputSink, error) {
	if s.childSinks != nil {
		if c, ok := s.childSinks[childAgentID]; ok {
			return c, nil
		}
	}
	current := s.registeredCurrent() && !s.progressClosed
	tracker := &SpanTracker{}
	if current {
		tracker = s.h.childTracker(childAgentID)
	}
	child := &agentOutputSink{
		h:                s.h,
		root:             s.turnPublisherSink(),
		agentID:          childAgentID,
		rootAgentID:      s.rootAgentID,
		outputGeneration: s.outputGeneration,
		agentProvider:    s.agentProvider,
		plugin:           s.plugin,
		tracker:          tracker,
		messageSession:   &nativeSessionFact{},
	}
	var restoreErr error
	if current {
		restoreErr = child.restoreMessageSession()
	}
	child.progress = newGenerationProgressPublisher(func(info map[string]interface{}) {
		s.h.broadcastAgentSessionInfo(childAgentID, info)
	}, child.currentMessageSessionID)
	if !current {
		child.progress.close()
		child.progressClosed = true
		return child, restoreErr
	}
	if s.childSinks == nil {
		s.childSinks = make(map[string]*agentOutputSink)
	}
	s.childSinks[childAgentID] = child
	s.h.sinksByAgent.Store(childAgentID, child)
	return child, restoreErr
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
	content, err := userMessageContent(prompt)
	if err != nil {
		return fmt.Errorf("marshal child prompt: %w", err)
	}
	child := s.rawChildSink(childAgentID)
	writer := agent.NewOrderedProviderServices(child.OutputQueue(), agent.NewProviderServices(child))
	queries := s.h.queries
	return child.initialUserMessage.Persist(writer, agent.MessageContent{Original: content}, func() (bool, error) {
		maxSeq, err := queries.GetMaxSeqByAgentID(s.h.bgTaskCtx(), childAgentID)
		if err != nil {
			return false, fmt.Errorf("read child transcript head: %w", err)
		}
		return maxSeq == 0, nil
	})
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
	mutation := s.h.transcriptMutationMutex(s.rootAgentID)
	mutation.RLock()
	s.childMu.Lock()
	child := s.childSinks[childAgentID]
	var lifetime *childSinkLifetime
	if child != nil {
		lifetime = child.childLifetime
	}
	s.childMu.Unlock()
	mutation.RUnlock()
	if child == nil {
		s.cleanupUncachedChild(childAgentID)
		return
	}
	s.cleanupChildLifetime(childAgentID, child, lifetime)
}

func (s *agentOutputSink) cleanupChildLifetime(childAgentID string, child *agentOutputSink, lifetime *childSinkLifetime) bool {
	mutation := s.h.transcriptMutationMutex(s.rootAgentID)
	for {
		removal := s.h.prepareActivityRemoval(childAgentID)
		mutation.Lock()
		s.childMu.Lock()
		current := s.childSinks[childAgentID] == child && s.registeredCurrent() && child.registeredCurrent() &&
			child.childLifetime == lifetime
		s.childMu.Unlock()
		if !current {
			mutation.Unlock()
			return false
		}
		s.freezeChildLifetimeLocked(child)
		if !s.h.cleanupChildMapsLocked(childAgentID, removal) {
			mutation.Unlock()
			continue
		}
		s.detachChildSink(childAgentID)
		agentIDs := child.retireRegisteredTree()
		s.h.enqueueProgressClears(agentIDs)
		mutation.Unlock()
		s.h.watcher.DrainAgentEvents(childAgentID)
		s.h.drainProgressClears(agentIDs)
		return true
	}
}

// cleanupUncachedChild retires existing activity before a child receives a cached sink.
func (s *agentOutputSink) cleanupUncachedChild(childAgentID string) {
	if s.h.queries == nil {
		return
	}
	row, err := s.h.queries.GetAgentByID(bgCtx(), childAgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		slog.Error("read the child before activity cleanup", "agent_id", childAgentID, "error", err)
		return
	}
	if !row.ParentAgentID.Valid || row.ParentAgentID.String != s.agentID {
		return
	}
	value, exists := s.h.activity.Load(childAgentID)
	if !exists {
		return
	}
	activity := value.(*agentActivity)
	activity.mu.Lock()
	scope := activity.scope
	activity.mu.Unlock()
	mutation := s.h.transcriptMutationMutex(s.rootAgentID)
	for {
		removal := s.h.prepareActivityRemoval(childAgentID)
		if removal == nil || removal.activity != activity {
			return
		}
		mutation.Lock()
		s.childMu.Lock()
		cached := s.childSinks[childAgentID] != nil
		s.childMu.Unlock()
		_, registered := s.h.sinksByAgent.Load(childAgentID)
		activity.mu.Lock()
		current, present := s.h.activity.Load(childAgentID)
		sameActivity := present && current == activity && activity.scope == scope
		activity.mu.Unlock()
		if !s.registeredCurrent() || cached || registered || !sameActivity {
			mutation.Unlock()
			return
		}
		if !s.h.cleanupChildMapsLocked(childAgentID, removal) {
			mutation.Unlock()
			continue
		}
		mutation.Unlock()
		s.h.watcher.DrainAgentEvents(childAgentID)
		return
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
	s.logRegistryEviction(change)
	return nil
}

func (s *agentOutputSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	// An upsert with no key has no identity to open a row under: the registry
	// refuses it rather than deriving a key for bytes no provider sent. A
	// caller with no registry linkage skips the call entirely; renaming and
	// child-agent ensurement still treat the empty key as their no-op signal.
	if task.RowKey == "" {
		return fmt.Errorf("background task upsert has no row key")
	}
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
		return s.h.applyBackgroundTaskUpsert(root, s.outputGeneration, task)
	})
}

func (s *agentOutputSink) UpdateBackgroundTaskStatus(rowKey string, status bgtask.Status, activeForm string) error {
	return s.applyAndBroadcast(rowKey, func(root, key string) (registryChange, error) {
		return s.h.applyBackgroundTaskStatus(root, key, s.outputGeneration, status, activeForm)
	})
}

func (s *agentOutputSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	return s.applyAndBroadcast(rowKey, func(root, key string) (registryChange, error) {
		return s.h.applyBackgroundTaskClose(root, key, s.outputGeneration, status)
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

// LookupChildIdentity resolves a stored registry key to its durable child
// without re-deriving the key: the identity's spelling is what the registry
// already holds, so the read goes to the cache and the primary key by its
// exact bytes. The native key comes from the child agent row, which preserves
// the provider's own bytes however the registry key was spelled.
func (s *agentOutputSink) LookupChildIdentity(identity bgtask.RowIdentity) (agent.ChildIdentity, bool, error) {
	key := identity.String()
	cache := s.h.bgTaskCache(s.rootAgentID)
	cache.Mu.Lock()
	if err := cache.on(s.h.queries, s.rootAgentID).ensureSeededLocked(s.h.bgTaskCtx()); err != nil {
		cache.Mu.Unlock()
		return agent.ChildIdentity{}, false, fmt.Errorf("seed background tasks for %s: %w", s.rootAgentID, err)
	}
	childID := ""
	if idx := cache.indexOf(key); idx >= 0 {
		childID = cache.Rows[idx].ChildAgentID
	}
	cache.Mu.Unlock()
	if childID == "" {
		// The cache holds only the display window; a row that carries a child
		// transcript outlives it. The primary key read is the same fallback
		// LookupBackgroundTask makes, with the same third-answer error rule.
		row, found, err := s.h.loadStoredBgTask(s.h.bgTaskCtx(), s.h.queries, s.rootAgentID, key)
		if err != nil || !found {
			return agent.ChildIdentity{}, false, err
		}
		childID = row.ChildAgentID
	}
	if childID == "" {
		return agent.ChildIdentity{}, false, nil
	}
	child, err := s.h.queries.GetAgentByID(s.h.bgTaskCtx(), childID)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.ChildIdentity{}, false, nil
	}
	if err != nil {
		return agent.ChildIdentity{}, false, fmt.Errorf("read the stored child identity %s: %w", childID, err)
	}
	return agent.ChildIdentity{AgentID: childID, ProviderChildKey: child.ProviderChildKey}, true, nil
}

// ReviveBackgroundTask returns a finished row to running and clears the prior run's descriptive state.
// A failed registry write returns an error so the provider can retain its pending restart.
func (s *agentOutputSink) ReviveBackgroundTask(rowKey string) error {
	return s.applyAndBroadcast(rowKey, func(root, key string) (registryChange, error) {
		return s.h.applyBackgroundTaskRevive(root, key, s.outputGeneration)
	})
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
	oldKey, newKey = s.normalizeRowKey(oldKey, "rename from"), s.normalizeRowKey(newKey, "rename to")
	change, err := s.h.mutateBackgroundTaskRegistry(s.rootAgentID, s.outputGeneration, []string{oldKey, newKey}, func(ctx context.Context, reg bgTaskView) error {
		// Check both identities before claims, deletion, hiding, or any display admission.
		source, sourceFound, err := storedBackgroundTask(ctx, reg, oldKey)
		if err != nil {
			return err
		}
		target, targetFound := source, sourceFound
		if oldKey != newKey {
			target, targetFound, err = storedBackgroundTask(ctx, reg, newKey)
			if err != nil {
				return err
			}
		}
		if sourceFound && source.OutputGeneration > s.outputGeneration || targetFound && target.OutputGeneration > s.outputGeneration {
			return errBackgroundTaskSuperseded
		}
		if sourceFound {
			source, _, err = claimBackgroundTaskGenerationLocked(ctx, reg, oldKey, s.outputGeneration)
			if err != nil {
				return err
			}
			if idx := reg.cache.indexOf(oldKey); idx >= 0 {
				reg.cache.Rows[idx] = source
			}
		}
		if oldKey == newKey {
			return nil
		}
		if targetFound {
			target, _, err = claimBackgroundTaskGenerationLocked(ctx, reg, newKey, s.outputGeneration)
			if err != nil {
				return err
			}
			if idx := reg.cache.indexOf(newKey); idx >= 0 {
				reg.cache.Rows[idx] = target
			}
		}
		if !sourceFound {
			return nil
		}
		if targetFound {
			// A replay can recreate the provisional key while its canonical row already exists.
			// The canonical winner keeps its lifecycle. Retain a linked loser unless the
			// winner already supplies the same child's index; deleting that duplicate loses no transcript route.
			supersededChild := source.ChildAgentID != "" && source.ChildAgentID == target.ChildAgentID
			reg.deleteByKeyOverride = func(ctx context.Context, q *db.Queries, owner, key string) error {
				rows, err := q.DeleteAgentBackgroundTaskForGeneration(ctx, db.DeleteAgentBackgroundTaskForGenerationParams{
					OwnerAgentID: owner, RowKey: key, OutputGeneration: s.outputGeneration,
				})
				if err != nil {
					return err
				}
				if rows == 0 {
					return errBackgroundTaskSuperseded
				}
				return nil
			}
			_, err := reg.deleteRowLocked(ctx, oldKey, supersededChild)
			return err
		}
		rows, err := reg.queries.RenameAgentBackgroundTask(ctx, db.RenameAgentBackgroundTaskParams{
			NewRowKey: newKey, OldRowKey: oldKey, OutputGeneration: s.outputGeneration, OwnerAgentID: s.rootAgentID,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return errBackgroundTaskSuperseded
		}
		reg.cache.renameRowKeyLocked(oldKey, newKey)
		if _, hidden := reg.cache.evictedActive[oldKey]; hidden {
			delete(reg.cache.evictedActive, oldKey)
			reg.cache.evictedActive[newKey] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if change.changed {
		s.h.broadcastBackgroundTasks(s.rootAgentID, change.rows)
		s.h.refreshActivityTree(s.rootAgentID, settleHeld)
	}
	return nil
}

// applyBackgroundTaskUpsertLocked commits an upsert and its eviction together.
// A failed write restores the database and the display cache when the handler owns a database handle.
// A test handler without that handle restores its cache only.
// The caller must hold cache.Mu.
func (h *OutputHandler) applyBackgroundTaskUpsertLocked(cache *bgTaskCache, rootAgentID string, generation int64, task bgtask.Upsert) (registryChange, error) {
	var eviction *backgroundTaskEviction
	change, err := h.mutateBackgroundTaskRegistryLocked(cache, rootAgentID, generation, []string{task.RowKey}, func(ctx context.Context, reg bgTaskView) error {
		return mutateBackgroundTaskUpsertLocked(ctx, reg, generation, task, &eviction)
	})
	if change.changed {
		change.eviction = eviction
	}
	return change, err
}

// mutateBackgroundTaskUpsertLocked preserves the existing replay and finished-state rules.
// The caller owns the transaction, cache mirror, and cache mutex.
func mutateBackgroundTaskUpsertLocked(ctx context.Context, reg bgTaskView, generation int64, task bgtask.Upsert, eviction **backgroundTaskEviction) error {
	task = task.Clean()
	cache := reg.cache
	existing, found, err := claimBackgroundTaskGenerationLocked(ctx, reg, task.RowKey, generation)
	if err != nil {
		return err
	}
	idx := cache.indexOf(task.RowKey)
	now := nowMillis()
	merged := task.ToItem()
	merged.OutputGeneration, merged.UpdatedAt = generation, now
	if found {
		if idx >= 0 {
			cache.Rows[idx] = existing
		}
		merged.CreatedAt, merged.EndedAt = existing.CreatedAt, existing.EndedAt
		merged = merged.PreservingBlanksFrom(existing)
		// Finished rows absorb active replays, while their descriptive fields may still change.
		if existing.Status.IsFinished() && !merged.Status.IsFinished() {
			merged.Status, merged.EndedAt = existing.Status, existing.EndedAt
		} else if merged.Status.IsFinished() && !existing.Status.IsFinished() {
			merged.EndedAt = now
		}
		if bgTaskVisibleEqual(existing.WithUpdatedAt(merged.UpdatedAt), merged) {
			return nil
		}
		// Re-admit only after a real visible update is established. Admission can evict an unrelated row.
		if idx < 0 {
			idx, err = reg.admitRowLocked(ctx, existing)
			if err != nil {
				return err
			}
		}
	} else {
		// Each kind retains its existing independent display cap and child-index policy.
		evicted, dropped, err := reg.makeRoomLocked(ctx, int64(merged.Kind))
		if err != nil {
			return err
		}
		if dropped && !evicted.Status.IsFinished() {
			*eviction = &backgroundTaskEviction{incomingKey: task.RowKey, row: evicted}
		}
		merged.CreatedAt = now
	}
	if merged.Status.IsFinished() && merged.EndedAt.IsZero() {
		merged.EndedAt = now
	}
	endedAt := sqltime.SQLiteNullTime{Time: merged.EndedAt, Valid: !merged.EndedAt.IsZero()}
	rows, err := reg.queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID: reg.ownerID, OutputGeneration: generation, RowKey: task.RowKey, Seq: cache.nextSeq,
		Kind: leapmuxv1.BackgroundTaskKind(merged.Kind), ChildAgentID: merged.ChildAgentID, ParentAgentID: merged.ParentAgentID,
		GroupKey: merged.GroupKey, GroupLabel: merged.GroupLabel, Title: merged.Title,
		TitleIsCommand: ptrconv.BoolToInt64(merged.TitleIsCommand), Description: merged.Description, ActiveForm: merged.ActiveForm,
		Status: leapmuxv1.BackgroundTaskStatus(merged.Status), CreatedAt: sqltime.NewSQLiteTime(merged.CreatedAt),
		UpdatedAt: sqltime.NewSQLiteTime(now), EndedAt: endedAt,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return errBackgroundTaskSuperseded
	}
	if !found {
		cache.Rows = append(cache.Rows, merged)
		cache.nextSeq++
	} else {
		cache.Rows[idx] = merged
	}
	return nil
}

type backgroundTaskEviction struct {
	incomingKey string
	row         bgtask.Item
}

func (s *agentOutputSink) logRegistryEviction(change registryChange) {
	if change.eviction == nil {
		return
	}
	eviction := change.eviction
	slog.Warn("The background task registry reached its display cap. It drops the oldest active display row because no row finished.",
		"owner", s.rootAgentID, "row_key", eviction.incomingKey, "kind", int64(eviction.row.Kind), "cap", bgtask.MaxTasks,
		"evicted_row_key", eviction.row.RowKey, "retained_in_store", eviction.row.ChildAgentID != "")
}

var errBackgroundTaskSuperseded = errors.New("a newer physical output writer owns this background task")

func bgTaskVisibleEqual(a, b bgtask.Item) bool {
	a.OutputGeneration, b.OutputGeneration = 0, 0
	return a == b
}

func bgTaskVisibleRowsEqual(a, b []bgtask.Item) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bgTaskVisibleEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func storedBackgroundTask(ctx context.Context, reg bgTaskView, key string) (bgtask.Item, bool, error) {
	row, err := reg.queries.GetAgentBackgroundTaskByRowKey(ctx, db.GetAgentBackgroundTaskByRowKeyParams{OwnerAgentID: reg.ownerID, RowKey: key})
	if errors.Is(err, sql.ErrNoRows) {
		return bgtask.Item{}, false, nil
	}
	if err != nil {
		return bgtask.Item{}, false, err
	}
	return bgItemFromRow(row), true, nil
}

// claimBackgroundTaskGenerationLocked reads authority from the transaction before any display mutation.
// A generation-only claim leaves all task metadata and timestamps intact.
func claimBackgroundTaskGenerationLocked(ctx context.Context, reg bgTaskView, key string, generation int64) (bgtask.Item, bool, error) {
	row, found, err := storedBackgroundTask(ctx, reg, key)
	if err != nil || !found {
		return row, found, err
	}
	if row.OutputGeneration > generation {
		return bgtask.Item{}, false, errBackgroundTaskSuperseded
	}
	if row.OutputGeneration < generation {
		rows, err := reg.queries.ClaimAgentBackgroundTaskOutputGeneration(ctx, db.ClaimAgentBackgroundTaskOutputGenerationParams{
			OutputGeneration: generation, OwnerAgentID: reg.ownerID, RowKey: key,
		})
		if err != nil {
			return bgtask.Item{}, false, err
		}
		if rows == 0 {
			return bgtask.Item{}, false, errBackgroundTaskSuperseded
		}
		row.OutputGeneration = generation
	}
	return row, true, nil
}

func (h *OutputHandler) mutateBackgroundTaskRegistry(root string, generation int64, keys []string, mutate func(context.Context, bgTaskView) error) (registryChange, error) {
	cache := h.bgTaskCache(root)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	return h.mutateBackgroundTaskRegistryLocked(cache, root, generation, keys, mutate)
}

func (h *OutputHandler) mutateBackgroundTaskRegistryLocked(cache *bgTaskCache, root string, generation int64, keys []string, mutate func(context.Context, bgTaskView) error) (registryChange, error) {
	if generation <= 0 {
		return registryChange{}, errors.New("a runtime background task writer requires a positive output generation")
	}
	ctx := h.bgTaskCtx()
	reg := cache.on(h.queries, root)
	var change registryChange
	err := reg.inTransactionLocked(ctx, h.db, func(tx bgTaskView) error {
		// A stale logical mutation must not even trigger cold-seed reclamation or display admission.
		for _, key := range keys {
			row, found, err := storedBackgroundTask(ctx, tx, key)
			if err != nil {
				return err
			}
			if found && row.OutputGeneration > generation {
				return errBackgroundTaskSuperseded
			}
		}
		if err := tx.ensureSeededLocked(ctx); err != nil {
			return err
		}
		before := cache.snapshot()
		if err := mutate(ctx, tx); err != nil {
			return err
		}
		change = registryChange{rows: cache.snapshot(), changed: !bgTaskVisibleRowsEqual(before, cache.Rows)}
		return nil
	})
	if errors.Is(err, errBackgroundTaskSuperseded) {
		return registryChange{rows: cache.snapshot()}, nil
	}
	return change, err
}

// sqlString converts a Go string to a sql.NullString where "" is NULL and a
// non-empty value is valid. agents.parent_agent_id is nullable: a child's own
// parent is always set, so callers pass a non-empty id.
func sqlString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
