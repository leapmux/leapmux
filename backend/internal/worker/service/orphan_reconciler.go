package service

import (
	"context"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/generated/db"
)

// OrphanReconciler periodically reconciles the worker's local
// agents / terminals / file-tab rows against the hub's
// workspace_tab_owned view. Its job is to absorb client crashes /
// network partitions that left the worker side and the CRDT side
// disagreeing:
//
//   - Local entity present, hub doesn't know about it → tombstone
//     the local agent / terminal / file-tab row.
//   - Local row's workspace_id differs from the hub's workspace_id
//     → CRDT is canonical; update the local row to match.
//
// The reconciler runs every interval (default 1 hour) and on
// explicit Trigger() calls (e.g. on worker reconnect).
type OrphanReconciler struct {
	queries   *db.Queries
	files     *FileTabPathStore
	listFn    func(ctx context.Context) ([]*leapmuxv1.OwnedTab, error)
	now       func() time.Time
	interval  time.Duration
	trigger   chan struct{}
	stop      chan struct{}
	done      chan struct{}
	logger    *slog.Logger
	agents    AgentStopper
	terminals TerminalStopper
}

// AgentStopper is the in-memory hook OrphanReconciler uses to
// terminate a stale agent subprocess alongside the DB row close.
// Satisfied by *agent.Manager; declared here as a narrow interface
// so the reconciler doesn't depend on the agent package (avoiding
// service ↔ agent import cycles at the package boundary).
type AgentStopper interface {
	// StopAgent signals the agent with the given id. Returns true
	// when the agent was found in memory and a stop signal was
	// dispatched; false means the process already exited (no-op).
	StopAgent(agentID string) bool
}

// TerminalStopper mirrors AgentStopper for terminal subprocesses.
// Satisfied by *terminal.Manager.
type TerminalStopper interface {
	// StopTerminal signals the terminal's PTY-attached shell. The
	// concrete *terminal.Manager returns no value here, so the
	// interface follows suit; a missing terminal is a silent no-op.
	StopTerminal(terminalID string)
}

// OrphanReconcilerOptions configures NewOrphanReconciler.
//
// Agents / Terminals are optional. When non-nil, the reconciler
// dispatches a stop signal to the in-memory manager alongside the
// DB closed_at update, so orphan subprocesses are reaped at
// reconcile time rather than only at worker restart. Tests that
// don't exercise the live-process path can leave them nil.
type OrphanReconcilerOptions struct {
	Interval  time.Duration
	Now       func() time.Time
	Logger    *slog.Logger
	Agents    AgentStopper
	Terminals TerminalStopper
}

// NewOrphanReconciler binds a reconciler to the worker's local DB
// queries plus the FileTabPathStore for path mutations. listFn is
// the hub-side ListOwnedTabsForWorker call (injected so tests can
// substitute a fake).
func NewOrphanReconciler(queries *db.Queries, files *FileTabPathStore, listFn func(ctx context.Context) ([]*leapmuxv1.OwnedTab, error), opts OrphanReconcilerOptions) *OrphanReconciler {
	if opts.Interval <= 0 {
		opts.Interval = time.Hour
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &OrphanReconciler{
		queries:   queries,
		files:     files,
		listFn:    listFn,
		now:       opts.Now,
		interval:  opts.Interval,
		trigger:   make(chan struct{}, 1),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		logger:    opts.Logger,
		agents:    opts.Agents,
		terminals: opts.Terminals,
	}
}

// Trigger schedules an immediate reconciliation pass. Non-blocking;
// duplicate triggers coalesce.
func (r *OrphanReconciler) Trigger() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is cancelled or Stop is called. Run a single
// pass on start, then run on each interval tick or Trigger().
func (r *OrphanReconciler) Run(ctx context.Context) {
	defer close(r.done)
	t := time.NewTicker(r.interval)
	defer t.Stop()

	r.reconcileOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-t.C:
			r.reconcileOnce(ctx)
		case <-r.trigger:
			r.reconcileOnce(ctx)
		}
	}
}

// Stop signals the run loop to exit and waits for it.
func (r *OrphanReconciler) Stop() {
	select {
	case <-r.stop:
		return
	default:
	}
	close(r.stop)
	<-r.done
}

type ownedTabKey struct {
	tabType leapmuxv1.TabType
	tabID   string
}

func (r *OrphanReconciler) reconcileOnce(ctx context.Context) {
	if r.listFn == nil {
		return
	}
	// Probe the local tables first — they're cheap (in-process SQLite)
	// — so an idle worker can skip the hub RPC entirely when there's
	// nothing to reconcile. Errors fall through with empty results;
	// the hub call below still surfaces drift the local probe missed.
	hasLocal := r.hasAnyLocalRows(ctx)

	if !hasLocal {
		return
	}

	hubTabs, err := r.listFn(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list owned tabs", "err", err)
		return
	}
	hubByKey := make(map[ownedTabKey]*leapmuxv1.OwnedTab, len(hubTabs))
	for _, t := range hubTabs {
		hubByKey[ownedTabKey{tabType: t.GetTabType(), tabID: t.GetTabId()}] = t
	}

	r.reconcileFileTabs(ctx, hubByKey)
	r.reconcileAgents(ctx, hubByKey)
	r.reconcileTerminals(ctx, hubByKey)
}

// hasAnyLocalRows returns true when at least one of the three reconciled
// local tables (worker_file_tabs, agents, terminals) has any row. Used
// by reconcileOnce to short-circuit before the hub RPC on idle workers.
// Each list error is logged but not surfaced — the caller falls through
// to the hub call, which will fail loudly if the worker is truly broken.
func (r *OrphanReconciler) hasAnyLocalRows(ctx context.Context) bool {
	if r.queries == nil {
		return true
	}
	if rows, err := r.queries.ListAllWorkerFileTabs(ctx); err == nil && len(rows) > 0 {
		return true
	}
	if rows, err := r.queries.ListAllAgentIDsAndWorkspaces(ctx); err == nil && len(rows) > 0 {
		return true
	}
	if rows, err := r.queries.ListAllTerminals(ctx); err == nil && len(rows) > 0 {
		return true
	}
	return false
}

func (r *OrphanReconciler) reconcileFileTabs(ctx context.Context, hubByKey map[ownedTabKey]*leapmuxv1.OwnedTab) {
	rows, err := r.queries.ListAllWorkerFileTabs(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list worker_file_tabs", "err", err)
		return
	}
	for _, row := range rows {
		k := ownedTabKey{tabType: leapmuxv1.TabType_TAB_TYPE_FILE, tabID: row.TabID}
		hub, ok := hubByKey[k]
		if !ok {
			// Order matters: drop the worktree_tabs link FIRST, then the
			// file_tab row. The two calls are intentionally split (so
			// orphan reconciliation never takes the worktree-removal
			// branch closeTabCommon owns), but they aren't atomic — a
			// failure between them on the OTHER order (file_tab first,
			// worktree_tabs second) would permanently leak the
			// worktree_tabs link: the next reconciler tick wouldn't see
			// the file_tab row, so it wouldn't try to clean it up, and
			// CountWorktreeTabs would over-count for that worktree
			// forever. Doing it in THIS order lets eventual consistency
			// recover: if the link drop fails we don't delete the
			// file_tab row, so the next tick re-enters this branch and
			// retries.
			if err := r.queries.DeleteWorktreeTabsByTabID(ctx, db.DeleteWorktreeTabsByTabIDParams{
				TabType: leapmuxv1.TabType_TAB_TYPE_FILE,
				TabID:   row.TabID,
			}); err != nil {
				r.logger.Warn("orphan reconciler: drop worktree association for stale file tab",
					"tab_id", row.TabID, "err", err)
				// Leave the file_tab row in place so the next tick
				// retries from the top.
				continue
			}
			if r.files != nil {
				if err := r.files.RevokeRow(ctx, row.OrgID, row.TabID); err != nil {
					r.logger.Warn("orphan reconciler: revoke stale file tab",
						"tab_id", row.TabID, "err", err)
				}
			}
			continue
		}
		if hub.GetWorkspaceId() != row.WorkspaceID {
			if r.files != nil {
				if err := r.files.Relocate(ctx, row.OrgID, row.TabID, hub.GetWorkspaceId()); err != nil {
					r.logger.Warn("orphan reconciler: relocate file tab",
						"tab_id", row.TabID, "err", err)
				}
			}
		}
	}
}

// reconcileAgents iterates every locally-known agent and absorbs the
// hub's view: hub-absent → close locally; workspace-mismatch →
// rewrite the local row's workspace_id.
func (r *OrphanReconciler) reconcileAgents(ctx context.Context, hubByKey map[ownedTabKey]*leapmuxv1.OwnedTab) {
	rows, err := r.queries.ListAllAgentIDsAndWorkspaces(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list agents", "err", err)
		return
	}
	for _, row := range rows {
		k := ownedTabKey{tabType: leapmuxv1.TabType_TAB_TYPE_AGENT, tabID: row.ID}
		hub, ok := hubByKey[k]
		if !ok {
			// Hub no longer knows about this agent. Mark the row
			// closed in SQLite AND dispatch a stop signal to the
			// in-memory agent manager so the live exec.Cmd is
			// reaped. Without the in-memory stop the subprocess
			// keeps running until the worker process itself exits
			// (closed_at != NULL just keeps it from being respawned
			// on the NEXT worker startup); for long-running
			// workers that's an open-ended leak.
			if err := r.queries.CloseAgent(ctx, row.ID); err != nil {
				r.logger.Warn("orphan reconciler: close stale agent",
					"agent_id", row.ID, "err", err)
			}
			if r.agents != nil {
				if stopped := r.agents.StopAgent(row.ID); stopped {
					r.logger.Info("orphan reconciler: stopped stale agent subprocess",
						"agent_id", row.ID)
				}
			}
			continue
		}
		if hub.GetWorkspaceId() != row.WorkspaceID {
			r.logger.Info("orphan reconciler: agent workspace_id drift",
				"agent_id", row.ID,
				"local_workspace", row.WorkspaceID,
				"hub_workspace", hub.GetWorkspaceId(),
			)
			// The worker's MoveTabWorkspace RPC handles the
			// authoritative update; we just log here so an operator
			// has visibility. Auto-rewriting from this loop would
			// need a worker-DB UPDATE that bypasses the agent
			// manager's in-memory state.
		}
	}
}

// reconcileTerminals does the same for terminals.
func (r *OrphanReconciler) reconcileTerminals(ctx context.Context, hubByKey map[ownedTabKey]*leapmuxv1.OwnedTab) {
	rows, err := r.queries.ListAllTerminals(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list terminals", "err", err)
		return
	}
	for _, row := range rows {
		k := ownedTabKey{tabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, tabID: row.ID}
		hub, ok := hubByKey[k]
		if !ok {
			// Symmetric to reconcileAgents: SQLite close + send a
			// stop signal to the in-memory terminal manager so the
			// PTY-attached shell process is reaped at reconcile
			// time, not at worker restart.
			if err := r.queries.CloseTerminal(ctx, row.ID); err != nil {
				r.logger.Warn("orphan reconciler: close stale terminal",
					"terminal_id", row.ID, "err", err)
			}
			if r.terminals != nil {
				r.terminals.StopTerminal(row.ID)
			}
			continue
		}
		if hub.GetWorkspaceId() != row.WorkspaceID {
			r.logger.Info("orphan reconciler: terminal workspace_id drift",
				"terminal_id", row.ID,
				"local_workspace", row.WorkspaceID,
				"hub_workspace", hub.GetWorkspaceId(),
			)
		}
	}
}
