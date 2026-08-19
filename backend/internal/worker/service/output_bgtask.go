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
		Kind:           bgtask.KindFromWire(r.Kind),
		GroupKey:       r.GroupKey,
		GroupLabel:     r.GroupLabel,
		Title:          r.Title,
		TitleIsCommand: ptrconv.Int64ToBool(r.TitleIsCommand),
		Description:    r.Description,
		ActiveForm:     r.ActiveForm,
		Status:         bgtask.StatusFromWire(r.Status),
		CreatedAt:      r.CreatedAt.Time,
		UpdatedAt:      r.UpdatedAt.Time,
		EndedAt:        endedAt,
	}
}

// bgTaskOps is the registryOps for background tasks, capturing the handler's
// queries so every cache instance shares the same type-specific behaviour.
func (h *OutputHandler) bgTaskOps() registryOps[bgtask.Item] {
	return registryOps[bgtask.Item]{
		listRows: func(ctx context.Context, ownerID, bucket string, limit int32) ([]seedEntry[bgtask.Item], error) {
			rows, err := h.queries.ListAgentBackgroundTasksByKindNewestFirst(ctx, db.ListAgentBackgroundTasksByKindNewestFirstParams{
				OwnerAgentID: ownerID,
				Kind:         bucket,
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
		reclaimFinishedBelowSeq: func(ctx context.Context, ownerID, bucket string, seq int64) error {
			_, err := h.queries.DeleteFinishedAgentBackgroundTasksBelowSeq(ctx, db.DeleteFinishedAgentBackgroundTasksBelowSeqParams{
				OwnerAgentID: ownerID,
				Kind:         bucket,
				Seq:          seq,
			})
			return err
		},
		keyOf:  func(r bgtask.Item) string { return r.RowKey },
		setKey: func(r *bgtask.Item, key string) { r.RowKey = key },
		isFinished: func(r bgtask.Item) bool {
			return r.Status.IsFinished()
		},
		deleteByKey: func(ctx context.Context, ownerID, key string) error {
			_, err := h.queries.DeleteAgentBackgroundTaskByRowKey(ctx, db.DeleteAgentBackgroundTaskByRowKeyParams{
				OwnerAgentID: ownerID,
				RowKey:       key,
			})
			return err
		},
		cap: bgtask.MaxTasks,
		// One cap pool per KIND. A run that opens hundreds of shells would
		// otherwise evict every finished subagent, and the subagent rows are the
		// ones carrying a transcript worth reopening.
		bucketOf: func(r bgtask.Item) string { return bgtask.KindWire(r.Kind) },
		buckets:  bgtask.KindWires(),
		label:    "background tasks",
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

// registryChange reports what one registry mutation did.
//
// endedChildID is the whole reason this type exists. Three appliers can move a
// row from active into a final status -- an upsert that carries a final status,
// a status update, and a close -- and the provider that owns the row decides
// which one it uses. Claude and Codex reach the final status through the first
// two and only then call CloseBackgroundTask; Pi never closes at all. So the
// close is NOT the moment a subagent is known to be over, and a divider written
// only from there never fires for those providers. Each applier therefore
// reports the child transcript that ITS OWN transition just ended, and
// applyAndBroadcast writes the divider once, for whichever applier it was.
type registryChange struct {
	rows []bgtask.Item
	// changed is false for a no-op (absent row, byte-identical replay, or a
	// non-final update against an already-final row).
	changed bool
	// endedChildID is the child transcript this mutation ended, or "" when the
	// row stayed active, was already final, carries no child (a shell task), or
	// nothing changed.
	endedChildID string
	// endedStatus is the final status the row landed on, carried with the id so
	// the divider reports what the applier actually wrote rather than what the
	// caller asked for (an upsert can keep an existing final status instead).
	endedStatus bgtask.Status
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

// applyBackgroundTaskStatus updates a row's status + active_form without
// closing it (used for running-progress updates). A no-op when the row is
// absent or the patch is a no-op.
func (h *OutputHandler) applyBackgroundTaskStatus(rootAgentID, rowKey string, status bgtask.Status, activeForm string) (registryChange, error) {
	ctx := h.bgTaskCtx()
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	if err := cache.ensureSeededLocked(ctx, rootAgentID); err != nil {
		return registryChange{}, err
	}
	idx := cache.indexOf(rowKey)
	if idx < 0 {
		return registryChange{rows: cache.snapshot()}, nil
	}
	existing := cache.Rows[idx]
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
	// One ms-floored instant for both the DB write and the in-memory cache, so a
	// warm-cache read after this transition returns the same stamp a cold-start
	// read does (no Go-time.Now-vs-SQLite-strftime drift).
	now := nowMillis()
	if err := h.queries.UpdateAgentBackgroundTaskStatus(ctx, db.UpdateAgentBackgroundTaskStatusParams{
		Status:       bgtask.StatusWire(status),
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
		if err := h.queries.StampAgentBackgroundTaskEndedAt(ctx, db.StampAgentBackgroundTaskEndedAtParams{
			EndedAt:      sqltime.SQLiteNullTimeOf(now),
			OwnerAgentID: rootAgentID,
			RowKey:       rowKey,
		}); err != nil {
			return registryChange{}, err
		}
		if cache.Rows[idx].EndedAt.IsZero() {
			cache.Rows[idx].EndedAt = now
		}
	}
	// Read the row's PRIOR status before the write overwrites it: the divider is
	// owed to the active -> final transition alone.
	wasFinished := cache.Rows[idx].Status.IsFinished()
	cache.Rows[idx].Status = status
	cache.Rows[idx].ActiveForm = activeForm
	cache.Rows[idx].UpdatedAt = now
	// The guards above exclude an already-final row only when the incoming status
	// is non-final, or when BOTH the status and the activeForm repeat. A second
	// final update carrying the same status and a different activeForm reaches
	// here -- Claude sends exactly that, a summary-bearing update followed by a
	// bare one -- so the transition is tested against the prior status rather
	// than assumed. Reporting the child twice writes the closing divider twice.
	var endedChildID string
	if !wasFinished && status.IsFinished() {
		endedChildID = cache.Rows[idx].ChildAgentID
	}
	return registryChange{
		rows:         cache.snapshot(),
		changed:      true,
		endedChildID: endedChildID,
		endedStatus:  status,
	}, nil
}

// applyBackgroundTaskClose moves a row into a final status (stamps ended_at).
// The query's status-IN('pending','running') guard means a final row can never
// be resurrected or re-closed. A no-op when the row is already final.
func (h *OutputHandler) applyBackgroundTaskClose(rootAgentID, rowKey string, status bgtask.Status) (registryChange, error) {
	ctx := h.bgTaskCtx()
	cache := h.bgTaskCache(rootAgentID)
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	if err := cache.ensureSeededLocked(ctx, rootAgentID); err != nil {
		return registryChange{}, err
	}
	idx := cache.indexOf(rowKey)
	if idx < 0 {
		return registryChange{rows: cache.snapshot()}, nil
	}
	if cache.Rows[idx].Status.IsFinished() {
		return registryChange{rows: cache.snapshot()}, nil
	}
	now := nowMillis()
	if err := h.queries.CloseAgentBackgroundTask(ctx, db.CloseAgentBackgroundTaskParams{
		Status:       bgtask.StatusWire(status),
		EndedAt:      sqltime.SQLiteNullTimeOf(now),
		UpdatedAt:    sqltime.NewSQLiteTime(now),
		OwnerAgentID: rootAgentID,
		RowKey:       rowKey,
	}); err != nil {
		return registryChange{}, err
	}
	cache.Rows[idx].Status = status
	cache.Rows[idx].EndedAt = now
	cache.Rows[idx].UpdatedAt = now
	return registryChange{
		rows:         cache.snapshot(),
		changed:      true,
		endedChildID: cache.Rows[idx].ChildAgentID,
		endedStatus:  status,
	}, nil
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
	if err := cache.ensureSeededLocked(ctx, rootAgentID); err != nil {
		cache.Mu.Unlock()
		slog.Warn("mark background tasks exited: seed failed", "agent_id", rootAgentID, "error", err)
		return
	}
	status := bgtask.StatusInterrupted
	if stopped {
		status = bgtask.StatusStopped
	}
	now := nowMillis()
	if _, err := h.queries.MarkAgentBackgroundTasksEnded(ctx, db.MarkAgentBackgroundTasksEndedParams{
		Status:       bgtask.StatusWire(status),
		EndedAt:      sqltime.SQLiteNullTimeOf(now),
		UpdatedAt:    sqltime.NewSQLiteTime(now),
		OwnerAgentID: rootAgentID,
	}); err != nil {
		cache.Mu.Unlock()
		slog.Warn("mark background tasks ended failed", "agent_id", rootAgentID, "error", err)
		return
	}
	changed := false
	// The children this sweep just ended. Collected here, under the lock, on the
	// same "was still active" test that decides the write -- so each transcript
	// gets its closing divider exactly once, for the same reason a registry
	// mutation does. Without this a subagent whose owner process died would keep
	// a transcript that simply stops.
	var endedChildIDs []string
	for i := range cache.Rows {
		if !cache.Rows[i].Status.IsFinished() {
			cache.Rows[i].Status = status
			cache.Rows[i].EndedAt = now
			cache.Rows[i].UpdatedAt = now
			changed = true
			if cache.Rows[i].ChildAgentID != "" {
				endedChildIDs = append(endedChildIDs, cache.Rows[i].ChildAgentID)
			}
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
	h.WriteSubagentEndDividers(endedChildIDs, status)
}

// WriteSubagentEndDividers closes each listed child transcript with the divider
// carrying status. Exported for the boot sweep in RestoreState, which ends rows
// straight in the DB across every owner (no caches exist yet at boot) and so
// cannot go through a registry mutation.
//
// The caller must pass only the children whose rows IT just moved into a final
// status, so each transcript is closed exactly once.
func (h *OutputHandler) WriteSubagentEndDividers(childAgentIDs []string, status bgtask.Status) {
	for _, childID := range childAgentIDs {
		h.persistSubagentEndDivider(childID, status)
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
	ctx := s.h.bgTaskCtx()
	// Refused, not rewritten -- see bgtask.ValidateRowKey. The key identifies
	// the registry row this child links to, so a rewrite here would link the
	// child to a DIFFERENT provider's row.
	if err := bgtask.ValidateRowKey(providerChildKey); err != nil {
		return "", fmt.Errorf("child registry key: %w", err)
	}
	// The MODEL supplies this title -- Claude's Task description, Codex's
	// collab prompt line, an ACP spawn label, Pi's subagent title -- so it
	// arrives with no length limit and no character rule. Clean it HERE
	// because the `agents` row that CreateChildAgent inserts takes it
	// directly, and because the blank test that picks that row's fallback has
	// to judge the CLEANED title: a title of nothing but stripped characters
	// is a blank tab label, not a name.
	//
	// The registry row that linkRegistryRow upserts, and the broadcast that
	// follows it, meet the same rule again at applyBackgroundTaskUpsertLocked,
	// which every registry title write passes through. CleanName is
	// idempotent, so the title arrives there byte-identical.
	//
	// CleanName, not SanitizeName: the title is DERIVED, so there is no caller
	// to report an error to. Refusing it would lose the child transcript over
	// a title the user never typed. This is the rule OpenAgent, RenameAgent,
	// UpdateTerminalTitle and the plan auto-rename apply.
	//
	// An empty result stays empty on the REGISTRY path: a blank title in a
	// bgtask.Upsert means "keep the title the row already holds"
	// (bgtask.Item.PreservingBlanksFrom), and substituting a fallback here
	// would overwrite a real title with a placeholder. The `agents` row takes
	// its own fallback instead -- see the CreateChildAgent call.
	title = validate.CleanName(title)

	// pendingBroadcast holds a post-mutation snapshot to fan out AFTER the
	// cache lock is released. Broadcasting under cache.Mu can block on a slow
	// gRPC stream consumer (BroadcastAgentEvent -> SendStream), serializing
	// every registry op for this root behind the transport.
	var pendingBroadcast []bgtask.Item
	childID, err := s.ensureChildAgentLocked(ctx, spawnSpanID, providerChildKey, title, &pendingBroadcast)
	if err != nil {
		return "", err
	}
	if pendingBroadcast != nil {
		s.h.broadcastBackgroundTasks(s.rootAgentID, pendingBroadcast)
	}
	return childID, nil
}

// ensureChildAgentLocked resolves (and creates on first sight) the virtual
// child agent for a spawn. It keeps DB I/O OUTSIDE the per-root cache mutex so
// a slow DB round-trip during one spawn does not serialize every other registry
// op for the root. The flow:
//
//  1. Cache lookup under the lock (fast path); return on hit.
//  2. DB work outside the lock: spawn-span fallback lookup, parent fetch, child
//     row create (with UNIQUE-race re-read).
//  3. Re-acquire the lock, re-check the cache (another goroutine may have
//     inserted the same child while the lock was released), then link the
//     registry row and snapshot the broadcast payload.
//
// *pendingBroadcast receives the post-mutation snapshot so the caller
// broadcasts after the lock is released.
func (s *agentOutputSink) ensureChildAgentLocked(ctx context.Context, spawnSpanID, providerChildKey, title string, pendingBroadcast *[]bgtask.Item) (string, error) {
	cache := s.h.bgTaskCache(s.rootAgentID)

	// 1. Fast path: cache hit under the lock. ensureSeededLocked runs once (the
	//    seeded flag makes later calls cheap), so it stays inside this brief
	//    locked section.
	cache.Mu.Lock()
	if err := cache.ensureSeededLocked(ctx, s.rootAgentID); err != nil {
		cache.Mu.Unlock()
		return "", err
	}
	if providerChildKey != "" {
		if idx := cache.indexOf(providerChildKey); idx >= 0 && cache.Rows[idx].ChildAgentID != "" {
			cid := cache.Rows[idx].ChildAgentID
			cache.Mu.Unlock()
			return cid, nil
		}
	}
	cache.Mu.Unlock()

	// 2. DB work OUTSIDE the lock.
	// Fallback: GetChildAgentBySpawnSpan covers a worker restart between the
	// agent-row insert and the registry upsert. parent_agent_id for the new row
	// is THIS sink's agentID (works for grandchild spawns too: the spawn span
	// lives in this sink's own transcript).
	if existing, err := s.h.queries.GetChildAgentBySpawnSpan(ctx, db.GetChildAgentBySpawnSpanParams{
		ParentAgentID: sqlString(s.agentID),
		SpawnSpanID:   spawnSpanID,
	}); err == nil {
		s.linkRegistryRow(cache, providerChildKey, existing.ID, title, pendingBroadcast)
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
	// OpenAgent takes, so one rule names every untitled agent row, and two
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
		ID:            childID,
		ParentAgentID: sqlString(s.agentID),
		SpawnSpanID:   spawnSpanID,
		WorkingDir:    parent.WorkingDir,
		HomeDir:       parent.HomeDir,
		Title:         rowTitle,
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
	// 3. Re-acquire the lock to link the registry row (and re-check the cache
	//    for a concurrent insert that won the race while the lock was released).
	s.linkRegistryRow(cache, providerChildKey, childID, title, pendingBroadcast)
	return childID, nil
}

// linkRegistryRow links the registry row for childID to providerChildKey under
// the cache lock, handling the race where another goroutine already linked the
// same child while EnsureChildAgent's DB work ran unlocked. If the cache already
// carries a row for childKey with a non-empty ChildAgentID, that earlier writer
// wins and this call is a no-op. Writes any post-mutation snapshot to
// *pendingBroadcast so the caller broadcasts after releasing the lock.
func (s *agentOutputSink) linkRegistryRow(cache *bgTaskCache, providerChildKey, childID, title string, pendingBroadcast *[]bgtask.Item) {
	if providerChildKey == "" {
		return
	}
	cache.Mu.Lock()
	defer cache.Mu.Unlock()
	// Re-check: a concurrent EnsureChildAgent for the same key may have committed
	// while this caller's DB work ran unlocked. If so, do not overwrite.
	if idx := cache.indexOf(providerChildKey); idx >= 0 && cache.Rows[idx].ChildAgentID != "" {
		return
	}
	*pendingBroadcast = s.ensureRegistryRowLocked(cache, providerChildKey, childID, title)
}

// ensureRegistryRowLocked upserts the registry row carrying child_agent_id,
// returning the post-mutation snapshot when it changed (nil otherwise) so the
// caller can broadcast AFTER releasing cache.Mu (broadcasting under the lock
// can block on a slow gRPC stream consumer). Caller must hold cache.Mu.
func (s *agentOutputSink) ensureRegistryRowLocked(cache *bgTaskCache, rowKey, childID, title string) []bgtask.Item {
	task := bgtask.Upsert{
		RowKey:        rowKey,
		Kind:          bgtask.KindSubagent,
		ChildAgentID:  childID,
		ParentAgentID: s.agentID,
		Title:         title,
		Status:        bgtask.StatusRunning,
	}
	// The upsert always carries StatusRunning, so it can never be the active ->
	// final transition and change.endedChildID is always empty here. Discarding
	// it is safe for that reason, and for that reason only.
	change, err := s.h.applyBackgroundTaskUpsertLocked(cache, s.rootAgentID, task)
	if err != nil {
		slog.Warn("ensure registry row for child failed", "row_key", rowKey, "error", err)
		return nil
	}
	if change.changed {
		return change.rows
	}
	return nil
}

// ChildSink returns an OutputSink bound to the child agent's transcript. The
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
		tracker:       s.h.childTracker(childAgentID),
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

// PersistChildPrompt writes the spawn prompt as the child transcript's first
// message. See the OutputSink doc for the contract; the emptiness check is what
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
	content, err := json.Marshal(map[string]string{"content": prompt})
	if err != nil {
		return fmt.Errorf("marshal child prompt: %w", err)
	}
	return s.ChildSink(childAgentID).PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content, agent.SpanInfo{})
}

// claimSubagentTranscriptClose reports whether THIS caller owns the closing
// divider for a subagent transcript.
//
// Exactly one divider closes a transcript, and TWO independent writers can end
// one: the registry (whichever applier moved the row into a final status) and
// the child's own turn end (a provider that forwards its subagent's closing
// envelope, like Claude's result). Either can arrive first.
//
// A durable INSERT decides it, rather than each side reading the transcript's
// last message and inferring what the other did. Two reasons the probes could
// not: both writers persist AFTER releasing the cache mutex, so two goroutines
// could each read "not ended" and each write; and each probe cost a DB read
// plus a zstd decompress, the turn-end one on EVERY child turn rather than only
// the last. The claim also survives a worker restart, which is when a content
// probe is least able to guess.
//
// Fails OPEN on a DB error: a missing divider leaves a transcript that never
// visibly ends, with a thinking indicator that never resolves, which is worse
// than a duplicated rule.
func (h *OutputHandler) claimSubagentTranscriptClose(ctx context.Context, childAgentID string) bool {
	rows, err := h.queries.ClaimSubagentTranscriptClose(ctx, childAgentID)
	if err != nil {
		slog.Warn("claim subagent transcript close", "child", childAgentID, "error", err)
		return true
	}
	return rows > 0
}

// persistSubagentEndDivider writes the divider that closes a child transcript.
//
// Unexported and service-internal on purpose: no provider calls it. Every
// caller reaches it through the ONE registry mutation that moved the row from
// active into a final status -- an upsert carrying the final status, a status
// update, or a close, whichever the provider uses -- plus the process-exit
// sweep and the boot sweep, which end rows no provider will ever close.
//
// Called exactly once per row: an applier reports endedChildID only on that
// transition, and the DB's own status guard makes it hold across a worker
// restart. A no-op for an empty id (a shell row, a re-close, or a subagent
// whose provider never linked a transcript).
//
// The provider comes from the child's own agent row rather than from a caller:
// createMessageRow refuses an UNSPECIFIED provider, and the sweeps have no sink
// to borrow one from. A child row that no longer resolves is skipped -- there
// is no transcript left to close.
func (h *OutputHandler) persistSubagentEndDivider(childAgentID string, status bgtask.Status) {
	if childAgentID == "" {
		return
	}
	ctx := h.bgTaskCtx()
	child, err := h.queries.GetAgentByID(ctx, childAgentID)
	if err != nil {
		slog.Warn("subagent end divider: child agent not found", "child", childAgentID, "error", err)
		return
	}
	// Exactly ONE divider closes a subagent transcript. A provider that forwards
	// its subagent's own closing envelope (Claude's result) draws a richer one --
	// it carries the duration, and on failure the error label and detail -- so it
	// claims first when it gets there first, and this neutral divider stands
	// down. A subagent stopped before it could forward a result never claims, so
	// it still gets this one.
	if !h.claimSubagentTranscriptClose(ctx, childAgentID) {
		return
	}
	content, err := json.Marshal(map[string]string{
		"type":   agent.NotificationTypeSubagentEnded,
		"status": bgtask.StatusWire(status),
	})
	if err != nil {
		slog.Warn("marshal subagent end divider", "child", childAgentID, "error", err)
		return
	}
	if err := h.persistAndBroadcast(childAgentID, child.AgentProvider,
		leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content, agent.SpanInfo{},
		h.closingChildTracker(childAgentID)); err != nil {
		slog.Warn("persist subagent end divider", "child", childAgentID, "error", err)
	}
}

// closingChildTracker resolves the child's span tracker for the closing
// divider WITHOUT registering one.
//
// childTracker (getOrCreate) is wrong here. The tab-close teardown runs
// CleanupChildAgents -- which deletes every descendant's tracker entry -- and
// only THEN MarkAgentBackgroundTasksExited, which writes a divider per still-
// active child. A getOrCreate on that path registers a fresh entry for an
// agent whose root is already gone, and nothing deletes it again, because this
// root's cleanup pass already ran: it survives until the hourly orphan sweep.
//
// So look the entry up read-only. When it is present the divider renders
// against whatever span rails were still open in that transcript; when the
// teardown already pruned it, a transient zero tracker gives the same empty
// rails the registered-but-blank entry would have given, and leaks nothing.
func (h *OutputHandler) closingChildTracker(childAgentID string) *SpanTracker {
	if t, _, ok := h.trackers.get(childAgentID); ok {
		return t
	}
	return &SpanTracker{}
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
	s.childMu.Lock()
	if s.childSinks != nil {
		delete(s.childSinks, childAgentID)
	}
	s.childMu.Unlock()
}

// --- Registry write primitives on the sink ---

// applyAndBroadcast runs a registry mutation, broadcasts the post-mutation
// snapshot when it changed, and writes the closing divider for a child
// transcript the mutation ended. The validate+broadcast+close-the-transcript
// contract is shared by every sink registry primitive; centralizing it here
// means a future mutation can't forget to check its key, skip a broadcast,
// or leave a subagent transcript that simply stops.
//
// The apply callback runs with the cache lock held internally (it acquires and
// releases cache.Mu), so both the broadcast and the divider write run outside
// the lock -- a slow gRPC consumer or a DB write cannot serialize every other
// registry op for this root.
func (s *agentOutputSink) applyAndBroadcast(rowKey string, apply func(rootAgentID, key string) (registryChange, error)) error {
	// The key is checked and passed through unchanged, never rewritten: a
	// rewrite is not injective, and two provider keys that map onto one string
	// become one registry row (see bgtask.ValidateRowKey). Refusing here fails
	// the one mutation and leaves every other row addressable.
	if err := bgtask.ValidateRowKey(rowKey); err != nil {
		return err
	}
	change, err := apply(s.rootAgentID, rowKey)
	if err != nil {
		return err
	}
	if change.changed {
		s.h.broadcastBackgroundTasks(s.rootAgentID, change.rows)
	}
	// After the broadcast, so a slow transport cannot delay the DB write. The
	// applier sets endedChildID only on the active -> final transition, and the
	// DB's own status guard makes that hold across a worker restart, so this
	// runs exactly once per row and needs no emptiness check of its own.
	s.h.persistSubagentEndDivider(change.endedChildID, change.endedStatus)
	return nil
}

func (s *agentOutputSink) UpsertBackgroundTask(task bgtask.Upsert) error {
	// No key normalization here: applyAndBroadcast below checks the key, and
	// the value it checks is the one the closure carries, so the two cannot
	// address different rows.
	//
	// The parent agent id is the agent that owns THIS sink. Providers that
	// spawn subagents don't need to thread it through every call site -- the
	// sink knows its own identity. For a root sink this is the root; for a
	// child sink it's the child (correct for grandchild spawns).
	if task.ParentAgentID == "" {
		task.ParentAgentID = s.agentID
	}
	return s.applyAndBroadcast(task.RowKey, func(root, _ string) (registryChange, error) {
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
// gated on the in-memory rename succeeding -- seed first, then re-key.
// Both keys are checked and neither is rewritten, the same way every other
// registry primitive treats a key. The spawn row was opened under the
// toolCallId the provider sent, so the raw toolCallId it passes as RenameFrom
// matches by construction -- a rewrite on one path and not the other is
// exactly how a rename stops finding its own row.
// The DB write runs BEFORE the cache mutation commits: on a DB error the cache
// stays keyed at oldKey (matching the DB), not the half-renamed newKey.
func (s *agentOutputSink) RenameBackgroundTask(oldKey, newKey string) error {
	if oldKey == "" || newKey == "" {
		return nil
	}
	if err := bgtask.ValidateRowKey(oldKey); err != nil {
		return fmt.Errorf("rename from: %w", err)
	}
	if err := bgtask.ValidateRowKey(newKey); err != nil {
		return fmt.Errorf("rename to: %w", err)
	}
	ctx := s.h.bgTaskCtx()
	var pendingBroadcast []bgtask.Item
	cache := s.h.bgTaskCache(s.rootAgentID)
	cache.Mu.Lock()
	if err := cache.ensureSeededLocked(ctx, s.rootAgentID); err != nil {
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
	// oldKey loses and is deleted.
	occupied, err := s.h.queries.CountAgentBackgroundTasksByRowKey(ctx, db.CountAgentBackgroundTasksByRowKeyParams{
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
		if _, err := s.h.queries.DeleteAgentBackgroundTaskByRowKey(ctx, db.DeleteAgentBackgroundTaskByRowKeyParams{
			OwnerAgentID: s.rootAgentID,
			RowKey:       oldKey,
		}); err != nil {
			cache.Mu.Unlock()
			return err
		}
		if cache.dropRowLocked(oldKey) {
			pendingBroadcast = cache.snapshot()
		}
		cache.Mu.Unlock()
		if pendingBroadcast != nil {
			s.h.broadcastBackgroundTasks(s.rootAgentID, pendingBroadcast)
		}
		return nil
	}
	if _, err := s.h.queries.RenameAgentBackgroundTask(ctx, db.RenameAgentBackgroundTaskParams{
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

// applyBackgroundTaskUpsertLocked is the cache.Mu-already-held variant of
// applyBackgroundTaskUpsert, used by EnsureChildAgent's registry re-link path
// (which already holds the cache lock). It bypasses re-locking to avoid a
// self-deadlock.
func (h *OutputHandler) applyBackgroundTaskUpsertLocked(cache *bgTaskCache, rootAgentID string, task bgtask.Upsert) (registryChange, error) {
	// Every write of agent_background_tasks.title passes through here: the
	// sink's UpsertBackgroundTask, which every provider calls, and
	// EnsureChildAgent's link upsert. The MODEL supplies these titles, so they
	// arrive with no length limit and no character rule. This column carries
	// the SAME name rule as every other title column in the worker -- one rule
	// for every title, and no per-column exception. bgtask.Upsert.CleanTitle
	// holds the rule and what it costs a row whose title IS a shell command.
	//
	// The clean runs BEFORE the no-op guard below, and it has to. A provider
	// that replays a raw title would otherwise differ from the stored cleaned
	// one on every replay, and each replay would rewrite the row and broadcast
	// again for the life of the agent.
	task = task.CleanTitle()
	ctx := h.bgTaskCtx()
	if err := cache.ensureSeededLocked(ctx, rootAgentID); err != nil {
		return registryChange{}, err
	}
	idx := cache.indexOf(task.RowKey)
	// Set on the active -> final transition below, so an upsert that carries the
	// final status writes the closing divider the same way a close does.
	var endedChildID string
	// One ms-floored instant for the DB write and the cache, so a warm-cache
	// read matches a cold-start read (no Go-time.Now-vs-SQLite drift).
	now := nowMillis()
	merged := task.ToItem()
	merged.UpdatedAt = now
	if idx >= 0 {
		existing := cache.Rows[idx]
		merged.CreatedAt = existing.CreatedAt
		merged.EndedAt = existing.EndedAt
		// Preserve descriptive fields the incoming upsert left blank so a partial
		// upsert cannot blank a previously-set row (a final-status output_file write
		// would otherwise wipe the title). Status is exempt: callers set it
		// deliberately, and StatusPending is a valid value, not a sentinel for
		// "preserve".
		merged = merged.PreservingBlanksFrom(existing)
		// A final status is monotonic and absorbing: a late or replayed
		// non-final upsert (a duplicate task_started, a replayed running
		// row after a worker restart) must not resurrect a row that reached a
		// final state. Drop the non-final status and keep the row as-is
		// so the active-form/title refresh still lands, but the row stays
		// final with its ended_at stamp intact.
		if existing.Status.IsFinished() && !merged.Status.IsFinished() {
			merged.Status = existing.Status
			merged.EndedAt = existing.EndedAt
		} else if merged.Status.IsFinished() && !existing.Status.IsFinished() {
			// A transition into a final status stamps ended_at.
			merged.EndedAt = now
			endedChildID = merged.ChildAgentID
		}
		// The no-op guard compares every field EXCEPT UpdatedAt (which is `now`
		// on this call and will always differ from the existing stamp). Without
		// that exclusion a byte-identical replay still rewrites + broadcasts on
		// every clock tick.
		if existing.WithUpdatedAt(merged.UpdatedAt) == merged {
			return registryChange{rows: cache.snapshot()}, nil
		}
	} else if bucket := bgtask.KindWire(merged.Kind); cache.atCapForBucket(bucket) {
		// Scoped to the incoming row's kind, so making room for a shell never
		// deletes a subagent (and the reverse).
		_, evicted, err := cache.evictOldestFinishedInBucketLocked(ctx, rootAgentID, bucket)
		if err != nil {
			return registryChange{}, err
		}
		if !evicted {
			// At the cap with no finished row to evict. Dropping the new row
			// would orphan an already-created child agent row (EnsureChildAgent
			// inserts the child before this upsert links it), leaving an
			// unopenable transcript. Evict an older row instead. Prefer the
			// oldest row that carries NO child linkage: evicting a linked row
			// would make that child permanently unsteerable (the registry row
			// is the only index from child id -> owner+rowKey, and the child
			// agents row survives the registry delete). When every active row
			// is linked to a child transcript, exceed the cap rather than
			// orphan a steerable child -- the cap is a soft bound to protect
			// the child-linkage invariant.
			slog.Warn("background task registry at cap with no finished row; evicting oldest unlinked active row",
				"owner", rootAgentID, "row_key", task.RowKey, "kind", bucket, "cap", bgtask.MaxTasks)
			evictIdx := -1
			for i, r := range cache.Rows {
				if bgtask.KindWire(r.Kind) == bucket && r.ChildAgentID == "" {
					evictIdx = i
					break
				}
			}
			if evictIdx >= 0 {
				if _, _, err := cache.evictAtLocked(ctx, rootAgentID, evictIdx); err != nil {
					return registryChange{}, err
				}
			} else {
				slog.Warn("background task registry at cap with all active rows linked to children; exceeding cap to avoid orphaning a steerable child",
					"owner", rootAgentID, "row_key", task.RowKey, "kind", bucket, "cap", bgtask.MaxTasks)
			}
		}
	}
	if idx < 0 {
		merged.CreatedAt = now
		// A row that is born final never had an active phase, so it ends its
		// child transcript here -- the only transition it will ever have.
		if merged.Status.IsFinished() {
			endedChildID = merged.ChildAgentID
		}
	}
	if merged.Status.IsFinished() && merged.EndedAt.IsZero() {
		merged.EndedAt = now
	}
	if err := h.queries.UpsertAgentBackgroundTask(ctx, db.UpsertAgentBackgroundTaskParams{
		OwnerAgentID:   rootAgentID,
		RowKey:         task.RowKey,
		Seq:            cache.nextSeq,
		Kind:           bgtask.KindWire(merged.Kind),
		ChildAgentID:   merged.ChildAgentID,
		ParentAgentID:  merged.ParentAgentID,
		GroupKey:       merged.GroupKey,
		GroupLabel:     merged.GroupLabel,
		Title:          merged.Title,
		TitleIsCommand: ptrconv.BoolToInt64(merged.TitleIsCommand),
		Description:    merged.Description,
		ActiveForm:     merged.ActiveForm,
		Status:         bgtask.StatusWire(merged.Status),
		// created_at binds on INSERT only (the ON CONFLICT UPDATE does not touch
		// it); updated_at binds on both. Both derive from the same `now` so the
		// cache and the persisted row agree to the millisecond.
		CreatedAt: sqltime.NewSQLiteTime(merged.CreatedAt),
		UpdatedAt: sqltime.NewSQLiteTime(now),
	}); err != nil {
		return registryChange{}, err
	}
	if idx < 0 {
		cache.Rows = append(cache.Rows, merged)
		cache.nextSeq++
	} else {
		cache.Rows[idx] = merged
	}
	return registryChange{
		rows:         cache.snapshot(),
		changed:      true,
		endedChildID: endedChildID,
		endedStatus:  merged.Status,
	}, nil
}

// sqlString converts a Go string to a sql.NullString where "" is NULL and a
// non-empty value is valid. agents.parent_agent_id is nullable: a child's own
// parent is always set, so callers pass a non-empty id.
func sqlString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
