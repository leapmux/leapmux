package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// bgTaskCache is the in-memory mirror of one root agent's background-task
// registry, built on the shared registryCache mechanics. The type-specific
// ops (list query, key/terminal/seq extractors, delete query) live in
// bgTaskOps, built once per OutputHandler.
type bgTaskCache = registryCache[bgtask.Item]

// bgItemFromRow projects a persisted agent_background_tasks row into the
// in-memory Item shape. Used by the seed closure.
func bgItemFromRow(r db.AgentBackgroundTask) bgtask.Item {
	var endedAt time.Time
	if r.EndedAt.Valid {
		endedAt = r.EndedAt.Time
	}
	return bgtask.Item{
		RowKey:        r.RowKey,
		ChildAgentID:  r.ChildAgentID,
		ParentAgentID: r.ParentAgentID,
		Kind:          bgtask.KindFromWire(r.Kind),
		GroupKey:      r.GroupKey,
		GroupLabel:    r.GroupLabel,
		Title:         r.Title,
		Description:   r.Description,
		ActiveForm:    r.ActiveForm,
		Status:        bgtask.StatusFromWire(r.Status),
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
		EndedAt:       endedAt,
	}
}

// bgTaskOps is the registryOps for background tasks, capturing the handler's
// queries so every cache instance shares the same type-specific behaviour.
func (h *OutputHandler) bgTaskOps() registryOps[bgtask.Item] {
	return registryOps[bgtask.Item]{
		listRows: func(ctx context.Context, ownerID string, limit int32) ([]seedEntry[bgtask.Item], error) {
			rows, err := h.queries.ListAgentBackgroundTasks(ctx, db.ListAgentBackgroundTasksParams{
				OwnerAgentID: ownerID,
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
		keyOf: func(r bgtask.Item) string { return r.RowKey },
		isTerminal: func(r bgtask.Item) bool {
			return r.Status.IsTerminal()
		},
		deleteByKey: func(ctx context.Context, ownerID, key string) error {
			_, err := h.queries.DeleteAgentBackgroundTaskByRowKey(ctx, db.DeleteAgentBackgroundTaskByRowKeyParams{
				OwnerAgentID: ownerID,
				RowKey:       key,
			})
			return err
		},
		cap:   bgtask.MaxTasks,
		label: "background tasks",
	}
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

// LoadBackgroundTasks returns the root's registry, seeding the in-memory cache
// from agent_background_tasks on first access. Cold-start RPCs route through
// here so a warm cache returns without a DB read. For a CHILD agent this
// returns empty (children own no registry).
func (h *OutputHandler) LoadBackgroundTasks(ctx context.Context, rootAgentID string) ([]bgtask.Item, error) {
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	if err := cache.ensureSeededLocked(ctx, rootAgentID); err != nil {
		return nil, err
	}
	return cache.snapshot(), nil
}

// applyBackgroundTaskUpsert is the persist-mutate-broadcast for an upsert. It
// writes the DB row, mutates the cache in place, runs cap-eviction, and
// broadcasts. A byte-identical replay (same row, same status, same fields)
// skips BOTH the write and the broadcast.
func (h *OutputHandler) applyBackgroundTaskUpsert(rootAgentID string, task bgtask.Upsert) ([]bgtask.Item, bool, error) {
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	return h.applyBackgroundTaskUpsertLocked(cache, rootAgentID, task)
}

// applyBackgroundTaskStatus updates a row's status + active_form without
// closing it (used for running-progress updates). A no-op when the row is
// absent or the patch is a no-op.
func (h *OutputHandler) applyBackgroundTaskStatus(rootAgentID, rowKey string, status bgtask.Status, activeForm string) ([]bgtask.Item, bool, error) {
	ctx := bgCtx()
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	if err := cache.ensureSeededLocked(ctx, rootAgentID); err != nil {
		return nil, false, err
	}
	idx := cache.indexOf(rowKey)
	if idx < 0 {
		return cache.snapshot(), false, nil
	}
	existing := cache.Rows[idx]
	// A terminal status is monotonic and absorbing: a late or replayed
	// non-terminal status update (a duplicate task_progress, a replayed
	// running upsert) must not resurrect a row that already reached a
	// terminal state. Without this guard the row flips back to running and
	// pins the parent's thinking indicator forever (no later close arrives).
	if existing.Status.IsTerminal() && !status.IsTerminal() {
		return cache.snapshot(), false, nil
	}
	if existing.Status == status && existing.ActiveForm == activeForm {
		return cache.snapshot(), false, nil
	}
	if err := h.queries.UpdateAgentBackgroundTaskStatus(ctx, db.UpdateAgentBackgroundTaskStatusParams{
		Status:       bgtask.StatusWire(status),
		ActiveForm:   activeForm,
		OwnerAgentID: rootAgentID,
		RowKey:       rowKey,
	}); err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	// A terminal transition stamps ended_at. The status-update query does not
	// (sqlc can't infer param types in a CASE WHEN), so a dedicated query does
	// it. This fixes a data-integrity defect where the close that followed this
	// update early-returned on IsTerminal() and ended_at stayed NULL forever.
	if status.IsTerminal() {
		if err := h.queries.StampAgentBackgroundTaskEndedAt(ctx, db.StampAgentBackgroundTaskEndedAtParams{
			OwnerAgentID: rootAgentID,
			RowKey:       rowKey,
		}); err != nil {
			// The stamp is the durable record of ended_at; a failure here means
			// the cache must not report a state the DB does not have. Roll back
			// the cache to the DB's view by leaving EndedAt unset.
			slog.Warn("stamp agent background task ended_at failed", "owner", rootAgentID, "row_key", rowKey, "error", err)
		} else if cache.Rows[idx].EndedAt.IsZero() {
			cache.Rows[idx].EndedAt = now
		}
	}
	cache.Rows[idx].Status = status
	cache.Rows[idx].ActiveForm = activeForm
	cache.Rows[idx].UpdatedAt = now
	return cache.snapshot(), true, nil
}

// applyBackgroundTaskClose terminalizes a row (stamps ended_at). The query's
// status-IN('pending','running') guard means a terminal row can never be
// resurrected or re-closed. A no-op when the row is already terminal.
func (h *OutputHandler) applyBackgroundTaskClose(rootAgentID, rowKey string, status bgtask.Status) ([]bgtask.Item, bool, error) {
	ctx := bgCtx()
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	if err := cache.ensureSeededLocked(ctx, rootAgentID); err != nil {
		return nil, false, err
	}
	idx := cache.indexOf(rowKey)
	if idx < 0 {
		return cache.snapshot(), false, nil
	}
	if cache.Rows[idx].Status.IsTerminal() {
		return cache.snapshot(), false, nil
	}
	if err := h.queries.CloseAgentBackgroundTask(ctx, db.CloseAgentBackgroundTaskParams{
		Status:       bgtask.StatusWire(status),
		OwnerAgentID: rootAgentID,
		RowKey:       rowKey,
	}); err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	cache.Rows[idx].Status = status
	cache.Rows[idx].EndedAt = now
	cache.Rows[idx].UpdatedAt = now
	return cache.snapshot(), true, nil
}

// MarkAgentBackgroundTasksExited terminalizes every still-active row owned by
// rootAgentID on process exit: stopped (explicit Stop) -> Stopped, else
// interrupted (crash) -> Interrupted. Skipped entirely when the shutdown latch
// is set (a shutdown-driven StopAll leaves rows active for the next boot's
// 'interrupted' sweep). Broadcasts once if anything changed.
func (h *OutputHandler) MarkAgentBackgroundTasksExited(rootAgentID string, stopped bool) {
	if h.shuttingDown.Load() {
		return
	}
	ctx := bgCtx()
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	if err := cache.ensureSeededLocked(ctx, rootAgentID); err != nil {
		slog.Warn("mark background tasks exited: seed failed", "agent_id", rootAgentID, "error", err)
		return
	}
	status := bgtask.StatusInterrupted
	if stopped {
		status = bgtask.StatusStopped
	}
	if _, err := h.queries.MarkAgentBackgroundTasksEnded(ctx, db.MarkAgentBackgroundTasksEndedParams{
		Status:       bgtask.StatusWire(status),
		OwnerAgentID: rootAgentID,
	}); err != nil {
		slog.Warn("mark background tasks ended failed", "agent_id", rootAgentID, "error", err)
		return
	}
	now := time.Now().UTC()
	changed := false
	for i := range cache.Rows {
		if !cache.Rows[i].Status.IsTerminal() {
			cache.Rows[i].Status = status
			cache.Rows[i].EndedAt = now
			cache.Rows[i].UpdatedAt = now
			changed = true
		}
	}
	if changed {
		h.broadcastBackgroundTasks(rootAgentID, cache.snapshot())
	}
}

// --- agentOutputSink: OutputSink registry + child-transcript methods ---

// EnsureChildAgent resolves (and creates on first sight) the virtual child
// agent spawned by the tool_use span spawnSpanID in THIS sink's transcript.
// Idempotent across replays and worker restarts. See output.go's NewSink for
// the rootAgentID semantics.
//
// spawnSpanID is persisted as agents.spawn_span_id and backed by the unique
// index idx_agents_spawn_span (parent_agent_id, spawn_span_id). For Claude and
// Codex this is the tool_use span. For ACP providers (Goose, Cursor, OpenCode)
// it is the spawn toolCallId — in ACP the toolCallId serves as both the span
// and the registry row key, so the two roles collapse to one string.
func (s *agentOutputSink) EnsureChildAgent(spawnSpanID, providerChildKey, title string) (string, error) {
	ctx := bgCtx()
	providerChildKey = bgtask.SanitizeRowKey(providerChildKey)

	// 1. Registry cache lookup by row_key == providerChildKey.
	cache := s.h.bgTaskCache(s.rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	if err := cache.ensureSeededLocked(ctx, s.rootAgentID); err != nil {
		return "", err
	}
	if providerChildKey != "" {
		if idx := cache.indexOf(providerChildKey); idx >= 0 && cache.Rows[idx].ChildAgentID != "" {
			return cache.Rows[idx].ChildAgentID, nil
		}
	}

	// 2. Fallback: GetChildAgentBySpawnSpan covers a worker restart between
	//    the agent-row insert and the registry upsert. parent_agent_id for the
	//    new row is THIS sink's agentID (works for grandchild spawns too: the
	//    spawn span lives in this sink's own transcript).
	if existing, err := s.h.queries.GetChildAgentBySpawnSpan(ctx, db.GetChildAgentBySpawnSpanParams{
		ParentAgentID: sqlString(s.agentID),
		SpawnSpanID:   spawnSpanID,
	}); err == nil {
		childID := existing.ID
		// Re-link the registry row (if any) to the reattached child.
		if providerChildKey != "" {
			s.ensureRegistryRowLocked(cache, providerChildKey, childID, title)
		}
		return childID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	// 3. Create the child agent row. Copy working_dir/home_dir/provider from
	//    THIS sink's agent row.
	parent, err := s.h.queries.GetAgentByID(ctx, s.agentID)
	if err != nil {
		return "", fmt.Errorf("get parent agent for child spawn: %w", err)
	}
	childID := id.Generate()
	if err := s.h.queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            childID,
		ParentAgentID: sqlString(s.agentID),
		SpawnSpanID:   spawnSpanID,
		WorkingDir:    parent.WorkingDir,
		HomeDir:       parent.HomeDir,
		Title:         title,
		AgentProvider: parent.AgentProvider,
	}); err != nil {
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
	// Record the child span tracker so ChildSink(childID) is ready.
	s.h.spanTracker(childID)
	// 4. Upsert the registry row with child_agent_id set (create it if the
	//    provider never called UpsertBackgroundTask first).
	if providerChildKey != "" {
		s.ensureRegistryRowLocked(cache, providerChildKey, childID, title)
	}
	return childID, nil
}

// ensureRegistryRowLocked upserts the registry row carrying child_agent_id,
// broadcasting only on a change (matching the public write primitives). Caller
// must hold cache.Mu.
func (s *agentOutputSink) ensureRegistryRowLocked(cache *bgTaskCache, rowKey, childID, title string) {
	task := bgtask.Upsert{
		RowKey:        rowKey,
		Kind:          bgtask.KindSubagent,
		ChildAgentID:  childID,
		ParentAgentID: s.agentID,
		Title:         title,
		Status:        bgtask.StatusRunning,
	}
	rows, changed, err := s.h.applyBackgroundTaskUpsertLocked(cache, s.rootAgentID, task)
	if err != nil {
		slog.Warn("ensure registry row for child failed", "row_key", rowKey, "error", err)
		return
	}
	if changed {
		s.h.broadcastBackgroundTasks(s.rootAgentID, rows)
	}
}

// ChildSink returns an OutputSink bound to the child agent's transcript. The
// child sink has its OWN span tracker; transcript primitives act on the child.
// Registry primitives on a child sink write under the same ROOT owner.
func (s *agentOutputSink) ChildSink(childAgentID string) agent.OutputSink {
	s.childMu.Lock()
	defer s.childMu.Unlock()
	if s.childSinks != nil {
		if c, ok := s.childSinks[childAgentID]; ok {
			return c
		}
	}
	child := &agentOutputSink{
		h:             s.h,
		agentID:       childAgentID,
		rootAgentID:   s.rootAgentID,
		agentProvider: s.agentProvider,
		plugin:        s.plugin,
		tracker:       s.h.spanTracker(childAgentID),
	}
	if s.childSinks == nil {
		s.childSinks = make(map[string]*agentOutputSink)
	}
	s.childSinks[childAgentID] = child
	return child
}

func (s *agentOutputSink) PersistChildMessage(childAgentID string, source leapmuxv1.MessageSource, content []byte, span agent.SpanInfo) error {
	return s.ChildSink(childAgentID).PersistMessage(source, content, span)
}

func (s *agentOutputSink) PersistChildTurnEnd(childAgentID string, content []byte, span agent.SpanInfo) error {
	return s.ChildSink(childAgentID).PersistTurnEnd(content, span)
}

// --- Registry write primitives on the sink ---

func (s *agentOutputSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	task.RowKey = bgtask.SanitizeRowKey(task.RowKey)
	// The parent agent id is the agent that owns THIS sink. Providers that
	// spawn subagents don't need to thread it through every call site -- the
	// sink knows its own identity. For a root sink this is the root; for a
	// child sink it's the child (correct for grandchild spawns).
	if task.ParentAgentID == "" {
		task.ParentAgentID = s.agentID
	}
	rows, changed, err := s.h.applyBackgroundTaskUpsert(s.rootAgentID, task)
	if err != nil {
		return err
	}
	if changed {
		s.h.broadcastBackgroundTasks(s.rootAgentID, rows)
	}
	return nil
}

func (s *agentOutputSink) UpdateBackgroundTaskStatus(rowKey string, status bgtask.Status, activeForm string) error {
	rowKey = bgtask.SanitizeRowKey(rowKey)
	rows, changed, err := s.h.applyBackgroundTaskStatus(s.rootAgentID, rowKey, status, activeForm)
	if err != nil {
		return err
	}
	if changed {
		s.h.broadcastBackgroundTasks(s.rootAgentID, rows)
	}
	return nil
}

func (s *agentOutputSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	rowKey = bgtask.SanitizeRowKey(rowKey)
	rows, changed, err := s.h.applyBackgroundTaskClose(s.rootAgentID, rowKey, status)
	if err != nil {
		return err
	}
	if changed {
		s.h.broadcastBackgroundTasks(s.rootAgentID, rows)
	}
	return nil
}

// applyBackgroundTaskUpsertLocked is the cache.Mu-already-held variant of
// applyBackgroundTaskUpsert, used by EnsureChildAgent's registry re-link path
// (which already holds the cache lock). It bypasses re-locking to avoid a
// self-deadlock.
func (h *OutputHandler) applyBackgroundTaskUpsertLocked(cache *bgTaskCache, rootAgentID string, task bgtask.Upsert) ([]bgtask.Item, bool, error) {
	ctx := bgCtx()
	if err := cache.ensureSeededLocked(ctx, rootAgentID); err != nil {
		return nil, false, err
	}
	idx := cache.indexOf(task.RowKey)
	now := time.Now().UTC()
	merged := bgtask.Item{
		RowKey:        task.RowKey,
		ChildAgentID:  task.ChildAgentID,
		ParentAgentID: task.ParentAgentID,
		Kind:          task.Kind,
		GroupKey:      task.GroupKey,
		GroupLabel:    task.GroupLabel,
		Title:         task.Title,
		Description:   task.Description,
		ActiveForm:    task.ActiveForm,
		Status:        task.Status,
		UpdatedAt:     now,
	}
	if idx >= 0 {
		existing := cache.Rows[idx]
		merged.CreatedAt = existing.CreatedAt
		merged.EndedAt = existing.EndedAt
		// Preserve fields the incoming upsert left blank so a partial upsert
		// cannot blank a previously-set row (a terminal output_file write would
		// otherwise wipe the title and flip kind back to subagent). Only
		// ChildAgentID was guarded before; the same blank-means-keep rule applies
		// to every descriptive field. Status is exempt: callers set it
		// deliberately (it is the very thing a status update changes), and
		// StatusPending is a valid value, not a sentinel for "preserve".
		if merged.ChildAgentID == "" {
			merged.ChildAgentID = existing.ChildAgentID
		}
		if merged.ParentAgentID == "" {
			merged.ParentAgentID = existing.ParentAgentID
		}
		if merged.Kind == bgtask.KindUnspecified {
			merged.Kind = existing.Kind
		}
		if merged.GroupKey == "" {
			merged.GroupKey = existing.GroupKey
			merged.GroupLabel = existing.GroupLabel
		}
		if merged.Title == "" {
			merged.Title = existing.Title
		}
		if merged.Description == "" {
			merged.Description = existing.Description
		}
		if merged.ActiveForm == "" {
			merged.ActiveForm = existing.ActiveForm
		}
		// A terminal status is monotonic and absorbing: a late or replayed
		// non-terminal upsert (a duplicate task_started, a replayed running
		// row after a worker restart) must not resurrect a row that reached a
		// terminal state. Drop the non-terminal status and keep the row as-is
		// so the active-form/title refresh still lands, but the row stays
		// terminal with its ended_at stamp intact.
		if existing.Status.IsTerminal() && !merged.Status.IsTerminal() {
			merged.Status = existing.Status
			merged.EndedAt = existing.EndedAt
		} else if merged.Status.IsTerminal() && !existing.Status.IsTerminal() {
			// A transition into a terminal status stamps ended_at.
			merged.EndedAt = now
		}
		// The no-op guard compares every field EXCEPT UpdatedAt (which is `now`
		// on this call and will always differ from the existing stamp). Without
		// that exclusion a byte-identical replay still rewrites + broadcasts on
		// every clock tick.
		if existing.WithUpdatedAt(merged.UpdatedAt) == merged {
			return cache.snapshot(), false, nil
		}
	} else if cache.atCap() {
		evicted, err := cache.evictOldestTerminalLocked(ctx, rootAgentID)
		if err != nil {
			return nil, false, err
		}
		if !evicted {
			// The registry is at the cap and every row is active, so the new
			// row is dropped. Log so the silent drop is observable; a row that
			// never enters the registry never shows in the sidebar and never
			// links its child tab.
			slog.Warn("background task registry at cap; dropping new row",
				"owner", rootAgentID, "row_key", task.RowKey, "cap", bgtask.MaxTasks)
			return cache.snapshot(), false, nil
		}
	}
	if idx < 0 {
		merged.CreatedAt = now
	}
	if merged.Status.IsTerminal() && merged.EndedAt.IsZero() {
		merged.EndedAt = now
	}
	if err := h.queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID:  rootAgentID,
		RowKey:        task.RowKey,
		Seq:           cache.nextSeq,
		Kind:          bgtask.KindWire(merged.Kind),
		ChildAgentID:  merged.ChildAgentID,
		ParentAgentID: merged.ParentAgentID,
		GroupKey:      merged.GroupKey,
		GroupLabel:    merged.GroupLabel,
		Title:         merged.Title,
		Description:   merged.Description,
		ActiveForm:    merged.ActiveForm,
		Status:        bgtask.StatusWire(merged.Status),
	}); err != nil {
		return nil, false, err
	}
	if idx < 0 {
		cache.Rows = append(cache.Rows, merged)
		cache.nextSeq++
	} else {
		cache.Rows[idx] = merged
	}
	return cache.snapshot(), true, nil
}

// sqlString converts a Go string to a sql.NullString where "" is NULL and a
// non-empty value is valid. agents.parent_agent_id is nullable: a child's own
// parent is always set, so callers pass a non-empty id.
func sqlString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
