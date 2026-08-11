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
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqltime"
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
		keyOf:  func(r bgtask.Item) string { return r.RowKey },
		setKey: func(r *bgtask.Item, key string) { r.RowKey = key },
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
		cap: bgtask.MaxTasks,
		// One cap pool per KIND. A run that opens hundreds of shells would
		// otherwise evict every finished subagent, and the subagent rows are the
		// ones carrying a transcript worth reopening.
		bucketOf:  func(r bgtask.Item) string { return bgtask.KindWire(r.Kind) },
		seedLimit: bgtask.MaxTasksTotal,
		label:     "background tasks",
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
	ctx := h.bgTaskCtx()
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
		return nil, false, err
	}
	// A terminal transition stamps ended_at. The status-update query does not
	// (sqlc cannot infer the type of a positional parameter inside a CASE WHEN,
	// so the atomic single-statement form is not generatable), so a dedicated
	// idempotent query does it. The stamp's WHERE filters `ended_at IS NULL AND
	// status IN (terminal)`, so it only writes on a genuine transition. On a
	// transient DB error the cache leaves EndedAt zero (mirroring the DB's NULL)
	// and the error propagates so the caller sees the incomplete transition;
	// the idempotent stamp can be retried by any later terminal update.
	if status.IsTerminal() {
		if err := h.queries.StampAgentBackgroundTaskEndedAt(ctx, db.StampAgentBackgroundTaskEndedAtParams{
			EndedAt:      sqltime.SQLiteNullTimeOf(now),
			OwnerAgentID: rootAgentID,
			RowKey:       rowKey,
		}); err != nil {
			return nil, false, err
		}
		if cache.Rows[idx].EndedAt.IsZero() {
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
	ctx := h.bgTaskCtx()
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
	now := nowMillis()
	if err := h.queries.CloseAgentBackgroundTask(ctx, db.CloseAgentBackgroundTaskParams{
		Status:       bgtask.StatusWire(status),
		EndedAt:      sqltime.SQLiteNullTimeOf(now),
		UpdatedAt:    sqltime.NewSQLiteTime(now),
		OwnerAgentID: rootAgentID,
		RowKey:       rowKey,
	}); err != nil {
		return nil, false, err
	}
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
	// gets its terminal divider exactly once, for the same reason
	// CloseBackgroundTask does. Without this a subagent whose owner process died
	// would keep a transcript that simply stops.
	var endedChildIDs []string
	for i := range cache.Rows {
		if !cache.Rows[i].Status.IsTerminal() {
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
	for _, childID := range endedChildIDs {
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
	providerChildKey = bgtask.SanitizeRowKey(providerChildKey)

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
	rows, changed, err := s.h.applyBackgroundTaskUpsertLocked(cache, s.rootAgentID, task)
	if err != nil {
		slog.Warn("ensure registry row for child failed", "row_key", rowKey, "error", err)
		return nil
	}
	if changed {
		return rows
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

// persistChildEnd writes the terminal divider that closes a child transcript.
//
// Unexported and service-internal on purpose: no provider calls it. The one
// caller is CloseBackgroundTask, because the registry close is the single
// provider-neutral moment at which a subagent is known to be over -- Claude's
// forwarded result, Codex's collab terminal state, and an ACP tool call going
// completed all funnel through it, and the providers whose child transcript
// merely stops have no other terminal signal at all.
//
// Called exactly once per row: applyBackgroundTaskClose reports changed=true
// only on the pending/running -> terminal transition, and the DB's own
// status-IN('pending','running') guard makes that hold across a worker restart
// too. So this needs no emptiness check of its own.
func (s *agentOutputSink) persistChildEnd(childAgentID string, status bgtask.Status) {
	if childAgentID == "" {
		return
	}
	s.h.persistSubagentEndDivider(childAgentID, status)
}

// childTranscriptAlreadyEnded reports whether the child's last message is its
// provider's turn-end envelope, i.e. the transcript already closes itself.
// Reads the DB rather than an in-memory flag so it holds across a worker
// restart, and answers false on any read error -- a missing divider is a worse
// outcome than a duplicated one.
func (h *OutputHandler) childTranscriptAlreadyEnded(ctx context.Context, childAgentID string, provider leapmuxv1.AgentProvider) bool {
	last, err := h.queries.GetLatestMessageByAgentID(ctx, childAgentID)
	if err != nil {
		return false
	}
	raw, err := msgcodec.Decompress(last.Content, last.ContentCompression)
	if err != nil {
		return false
	}
	return agent.ProviderFor(provider).IsTurnEndEnvelope(raw)
}

// persistSubagentEndDivider is the handler-level writer behind persistChildEnd.
// It exists separately because the OTHER caller -- the process-exit sweep
// (MarkAgentBackgroundTasksExited) -- runs on the handler and has no sink in
// hand.
//
// The provider comes from the child's own agent row rather than from a caller:
// createMessageRow refuses an UNSPECIFIED provider, and the exit sweep has no
// sink to borrow one from. A child row that no longer resolves is skipped --
// there is no transcript left to close.
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
	// its subagent's own terminal envelope (Claude's result) has already drawn
	// one, and that one is richer -- it carries the duration, and on failure the
	// error label and detail. Stacking the neutral divider under it would say the
	// same thing twice. A subagent stopped before it could forward a result does
	// not end here, so it still gets the neutral divider.
	if h.childTranscriptAlreadyEnded(ctx, childAgentID, child.AgentProvider) {
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
	// The child's own tracker, so the divider renders with whatever span rails
	// were still open in that transcript rather than against an empty snapshot.
	if err := h.persistAndBroadcast(childAgentID, child.AgentProvider,
		leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content, agent.SpanInfo{},
		h.childTracker(childAgentID)); err != nil {
		slog.Warn("persist subagent end divider", "child", childAgentID, "error", err)
	}
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

// applyAndBroadcast runs a registry mutation and broadcasts the post-mutation
// snapshot only when it changed. The sanitize+broadcast-on-change contract is
// shared by every sink registry primitive; centralizing it here means a future
// mutation can't forget to sanitize its key or skip a broadcast. The apply
// callback runs with the cache lock held internally (it acquires and releases
// cache.Mu), so the broadcast runs outside the lock.
func (s *agentOutputSink) applyAndBroadcast(rowKey string, apply func(rootAgentID, key string) (rows []bgtask.Item, changed bool, err error)) error {
	rows, changed, err := apply(s.rootAgentID, bgtask.SanitizeRowKey(rowKey))
	if err != nil {
		return err
	}
	if changed {
		s.h.broadcastBackgroundTasks(s.rootAgentID, rows)
	}
	return nil
}

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
	return s.applyAndBroadcast(rowKey, func(root, key string) ([]bgtask.Item, bool, error) {
		return s.h.applyBackgroundTaskStatus(root, key, status, activeForm)
	})
}

func (s *agentOutputSink) CloseBackgroundTask(rowKey string, status bgtask.Status) error {
	// The child whose transcript this close ends, captured on the ONE call that
	// actually terminalizes the row (see persistChildEnd for why that is the
	// right idempotency guard). Empty for a shell row or a re-close.
	var endedChildID string
	err := s.applyAndBroadcast(rowKey, func(root, key string) ([]bgtask.Item, bool, error) {
		rows, changed, applyErr := s.h.applyBackgroundTaskClose(root, key, status)
		if changed {
			endedChildID = childAgentIDForRow(rows, key)
		}
		return rows, changed, applyErr
	})
	if err != nil {
		return err
	}
	// After the broadcast, so a slow transport cannot delay the DB write, and
	// outside the cache lock applyBackgroundTaskClose holds.
	s.persistChildEnd(endedChildID, status)
	return nil
}

// childAgentIDForRow returns the child transcript linked to rowKey, or "" when
// the row is absent or carries no child (a shell task, or a subagent whose
// provider never linked a transcript).
func childAgentIDForRow(rows []bgtask.Item, rowKey string) string {
	for _, r := range rows {
		if r.RowKey == rowKey {
			return r.ChildAgentID
		}
	}
	return ""
}

// RenameBackgroundTask atomically re-keys a row from oldKey to newKey under the
// root owner, preserving status, child linkage, and terminal state. A no-op
// when the old row is absent or newKey is empty. Used by ACP providers that
// learn the stable child id only on the terminal update (OpenCode), so a single
// row tracks the whole lifecycle instead of a spawn row orphaned Running while
// a separately-keyed row closes.
//
// Order matters: seed the cache, then mutate it. On a cold cache (after a
// worker restart, or the first registry touch for a root), renameRowKeyLocked
// sees an empty Rows slice and returns false, so the DB rename must NOT be
// gated on the in-memory rename succeeding -- seed first, then re-key.
// oldKey is sanitized the same way every other registry primitive sanitizes its
// key: the spawn row was opened under the SANITIZED toolCallId
// (UpsertBackgroundTask sanitizes at the sink boundary), so the raw toolCallId
// a provider passes as RenameFrom never matches unless it is sanitized here too.
// The DB write runs BEFORE the cache mutation commits: on a DB error the cache
// stays keyed at oldKey (matching the DB), not the half-renamed newKey.
func (s *agentOutputSink) RenameBackgroundTask(oldKey, newKey string) error {
	if oldKey == "" || newKey == "" {
		return nil
	}
	newKey = bgtask.SanitizeRowKey(newKey)
	oldKey = bgtask.SanitizeRowKey(oldKey)
	ctx := s.h.bgTaskCtx()
	var pendingBroadcast []bgtask.Item
	cache := s.h.bgTaskCache(s.rootAgentID)
	cache.Mu.Lock()
	if err := cache.ensureSeededLocked(ctx, s.rootAgentID); err != nil {
		cache.Mu.Unlock()
		return err
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
func (h *OutputHandler) applyBackgroundTaskUpsertLocked(cache *bgTaskCache, rootAgentID string, task bgtask.Upsert) ([]bgtask.Item, bool, error) {
	ctx := h.bgTaskCtx()
	if err := cache.ensureSeededLocked(ctx, rootAgentID); err != nil {
		return nil, false, err
	}
	idx := cache.indexOf(task.RowKey)
	// One ms-floored instant for the DB write and the cache, so a warm-cache
	// read matches a cold-start read (no Go-time.Now-vs-SQLite drift).
	now := nowMillis()
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
		// Preserve descriptive fields the incoming upsert left blank so a partial
		// upsert cannot blank a previously-set row (a terminal output_file write
		// would otherwise wipe the title). Status is exempt: callers set it
		// deliberately, and StatusPending is a valid value, not a sentinel for
		// "preserve".
		merged = merged.PreservingBlanksFrom(existing)
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
	} else if bucket := bgtask.KindWire(merged.Kind); cache.atCapForBucket(bucket) {
		// Scoped to the incoming row's kind, so making room for a shell never
		// deletes a subagent (and the reverse).
		evicted, err := cache.evictOldestTerminalInBucketLocked(ctx, rootAgentID, bucket)
		if err != nil {
			return nil, false, err
		}
		if !evicted {
			// At the cap with no terminal row to evict. Dropping the new row
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
			slog.Warn("background task registry at cap with no terminal row; evicting oldest unlinked active row",
				"owner", rootAgentID, "row_key", task.RowKey, "kind", bucket, "cap", bgtask.MaxTasks)
			evictIdx := -1
			for i, r := range cache.Rows {
				if bgtask.KindWire(r.Kind) == bucket && r.ChildAgentID == "" {
					evictIdx = i
					break
				}
			}
			if evictIdx >= 0 {
				if _, err := cache.evictAtLocked(ctx, rootAgentID, evictIdx); err != nil {
					return nil, false, err
				}
			} else {
				slog.Warn("background task registry at cap with all active rows linked to children; exceeding cap to avoid orphaning a steerable child",
					"owner", rootAgentID, "row_key", task.RowKey, "kind", bucket, "cap", bgtask.MaxTasks)
			}
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
		// created_at binds on INSERT only (the ON CONFLICT UPDATE does not touch
		// it); updated_at binds on both. Both derive from the same `now` so the
		// cache and the persisted row agree to the millisecond.
		CreatedAt: sqltime.NewSQLiteTime(merged.CreatedAt),
		UpdatedAt: sqltime.NewSQLiteTime(now),
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
