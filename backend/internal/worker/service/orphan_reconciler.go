package service

import (
	"context"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
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
	listFn    func(ctx context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error)
	now       func() time.Time
	interval  time.Duration
	trigger   chan struct{}
	stop      chan struct{}
	done      chan struct{}
	logger    *slog.Logger
	agents    AgentStopper
	terminals TerminalStopper

	// reapWorktree removes a worktree confirmed orphaned (all its tab
	// links are strands). Nil disables worktree GC (tests that don't
	// exercise it leave it unset).
	reapWorktree func(ctx context.Context, wt db.Worktree)
	// prevOrphanWorktrees holds worktree ids seen orphaned on the previous
	// pass. A worktree must be orphaned across two consecutive passes
	// before reconcileWorktrees removes it, so a transient zero-live window
	// during startup or worktree reuse is never mistaken for a strand.
	prevOrphanWorktrees map[string]struct{}
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
	// ReapWorktree, when set, enables the orphan-worktree GC pass: it is
	// invoked for each worktree confirmed orphaned across two consecutive
	// reconcile passes. Wire it to (*Service).ReapOrphanWorktree.
	ReapWorktree func(ctx context.Context, wt db.Worktree)
}

// NewOrphanReconciler binds a reconciler to the worker's local DB
// queries plus the FileTabPathStore for path mutations. listFn is
// the hub-side ListOwnedTabsForWorker call (injected so tests can
// substitute a fake).
//
// listFn hands back the WHOLE response, not just its tabs: the reap decision
// needs the owner the response declares (see reconcileFileTabs), and a
// signature that returned the tab list alone would let a caller drop that owner
// on the floor and turn a narrow list into a universal absence.
func NewOrphanReconciler(queries *db.Queries, files *FileTabPathStore, listFn func(ctx context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error), opts OrphanReconcilerOptions) *OrphanReconciler {
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
		queries:             queries,
		files:               files,
		listFn:              listFn,
		now:                 opts.Now,
		interval:            opts.Interval,
		trigger:             make(chan struct{}, 1),
		stop:                make(chan struct{}),
		done:                make(chan struct{}),
		logger:              opts.Logger,
		agents:              opts.Agents,
		terminals:           opts.Terminals,
		reapWorktree:        opts.ReapWorktree,
		prevOrphanWorktrees: make(map[string]struct{}),
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

// ownedTabKey identifies one reconcilable tab on both sides of the
// comparison: the hub's workspace_tab_owned projection and the worker's local
// rows. userID is part of the key because workspace_tab_owned and
// worker_file_tabs are both keyed by (user_id, tab_id) -- a FILE tab id is
// unique only within a user, so an owner-blind key looks up user B's local row
// in a map built from user A's hub rows, misses, and reaps a live tab.
//
// It is normalized through worktreeTabUserID, so AGENT/TERMINAL keys collapse
// to userID "" on both sides: their ids are globally unique, and the local
// agents/terminals tables carry no owner column to match against.
type ownedTabKey struct {
	tabType leapmuxv1.TabType
	tabID   string
	userID  string
}

// newOwnedTabKey builds a key with the owner axis normalized. Both the hub-side
// map build and every local-row lookup go through it so the two can't drift.
func newOwnedTabKey(tabType leapmuxv1.TabType, tabID, userID string) ownedTabKey {
	return ownedTabKey{tabType: tabType, tabID: tabID, userID: worktreeTabUserID(tabType, userID)}
}

func (r *OrphanReconciler) reconcileOnce(ctx context.Context) {
	// Worktree GC is local-only (no hub dependency) and must run even when
	// the hub list is unavailable or there are no live tab rows: a strand
	// can outlive its tab row once the cleanup loop hard-deletes the closed
	// agent/terminal, so it would be invisible to the hasAnyLocalRows
	// short-circuit below.
	r.reconcileWorktrees(ctx)

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

	resp, err := r.listFn(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list owned tabs", "err", err)
		return
	}
	owner, ownerOK := userid.New(resp.GetOwnerUserId())
	if !ownerOK {
		// No declared scope, so the response is authoritative for nobody and
		// every absence below would be an unfounded reap. Nothing to do --
		// including the relocations, since a response with no owner is not one
		// we can attribute rows to either.
		r.logger.Warn("orphan reconciler: hub response declares no owner scope; skipping this pass",
			"hub_tabs", len(resp.GetTabs()))
		return
	}
	hubTabs := resp.GetTabs()
	hubByKey := make(map[ownedTabKey]*leapmuxv1.OwnedTab, len(hubTabs))
	for _, t := range hubTabs {
		hubByKey[newOwnedTabKey(t.GetTabType(), t.GetTabId(), t.GetUserId())] = t
	}

	// Only the file-tab pass takes the owner. Local agents / terminals carry no
	// owner column at all (their ids are globally unique, so nothing on this
	// side ever needed one), so their rows can be attributed to no owner and
	// excluded from no scope; the only scope check available to them is the
	// mint gate above.
	r.reconcileFileTabs(ctx, hubByKey, owner)
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

// reconcileWorktrees reclaims worktrees whose tab links are all
// startup-race strands — no live agent/terminal/file tab references them.
// A worktree must be seen orphaned in TWO consecutive passes before it is
// removed: the transient zero-live windows during agent/terminal startup
// (the row exists before its worktree_tabs link is written) and during
// worktree reuse are far shorter than the reconcile interval, so they
// never survive into a second pass — only a genuine strand does.
//
// This is the backstop the startup link guards in runAgentStartup /
// runTerminalStartup rely on: without it, a close that raced startup — or
// a link written when getAgentByID/GetTerminalForReady returned a
// transient error and the guard fell through to link — would strand a
// worktree_tabs row whose tab is gone and leak the worktree dir forever
// (reconcileAgents/reconcileTerminals close the tab row but never drop
// worktree_tabs links).
func (r *OrphanReconciler) reconcileWorktrees(ctx context.Context) {
	if r.reapWorktree == nil {
		return
	}
	candidates, err := r.queries.ListOrphanCandidateWorktrees(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list orphan-candidate worktrees", "err", err)
		return
	}
	nextOrphans := make(map[string]struct{}, len(candidates))
	for _, wt := range candidates {
		if _, seenLastPass := r.prevOrphanWorktrees[wt.ID]; seenLastPass {
			// Orphaned across two consecutive passes — not a transient
			// startup/reuse window. reapWorktree re-checks live refs under
			// the per-worktree lock before actually removing.
			r.reapWorktree(ctx, wt)
			continue
		}
		// First pass it looked orphaned: remember it; reap next pass if it
		// is still orphaned then.
		nextOrphans[wt.ID] = struct{}{}
	}
	r.prevOrphanWorktrees = nextOrphans
}

// reconcileFileTabs reaps local file-tab rows the hub no longer lists.
//
// owner is the single owner the hub response is authoritative about -- exactly
// one, the calling worker's registrant, because the hub's query binds user_id
// (workspace_tab_owned is keyed by (user_id, tab_id), so worker_id alone
// selects across tenants).
//
// The read below is deliberately NOT scoped to that owner, and must not be.
// Only the REAP is an inference from absence; a relocation acts on a row the
// hub actually LISTED, which names its own owner and is authoritative for
// itself. Narrowing the read to `owner` would silently stop applying
// cross-owner relocations -- pinned by
// TestOrphanReconciler_FileTab_SharedTabIDStaysWithItsOwner, which fails if you
// try it.
func (r *OrphanReconciler) reconcileFileTabs(ctx context.Context, hubByKey map[ownedTabKey]*leapmuxv1.OwnedTab, owner userid.UserID) {
	rows, err := r.queries.ListAllWorkerFileTabs(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list worker_file_tabs", "err", err)
		return
	}
	for _, row := range rows {
		k := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_FILE, row.TabID, row.UserID)
		hub, ok := hubByKey[k]
		if !ok {
			// INVARIANT: absence is only evidence of an orphan for the owner
			// the response covers. ListAllWorkerFileTabs walks EVERY owner's
			// rows (it must -- see the note on this function about
			// relocations), while the hub list is scoped to one. So for any
			// other owner "not in hubByKey" means "not asked about", not
			// "deleted", and reaping on that would destroy a live tab (its
			// worktree link, then its row) belonging to a user this response
			// said nothing about.
			//
			// Compared through Matches rather than a raw ==: a raw compare on
			// two strings fails OPEN for blank-vs-blank, and it would be
			// invisible to audit.identityComparisonSites, whose detector
			// recognises only Matches/MatchesUser/auth.IsOwner. This is the
			// worker's most destructive code path; it should be the easiest one
			// for that net to see.
			//
			// Do not weaken this to `hubByKey is non-empty`, and do not drop it
			// because "a worker only ever serves one registrant": that property
			// is not enforced by any schema on either side, and this is the
			// exact trap that made the hub-side owner predicate a data-loss
			// risk in the first place.
			if !owner.Matches(row.UserID) {
				continue
			}
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
				UserID:  worktreeTabUserID(leapmuxv1.TabType_TAB_TYPE_FILE, row.UserID),
			}); err != nil {
				r.logger.Warn("orphan reconciler: drop worktree association for stale file tab",
					"tab_id", row.TabID, "err", err)
				// Leave the file_tab row in place so the next tick
				// retries from the top.
				continue
			}
			if r.files != nil {
				if err := r.files.RevokeRow(ctx, row.UserID, row.TabID); err != nil {
					r.logger.Warn("orphan reconciler: revoke stale file tab",
						"tab_id", row.TabID, "err", err)
				}
			}
			continue
		}
		if hub.GetWorkspaceId() != row.WorkspaceID {
			if r.files != nil {
				if err := r.files.Relocate(ctx, row.UserID, row.TabID, hub.GetWorkspaceId()); err != nil {
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
		k := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, row.ID, "")
		hub, ok := hubByKey[k]
		if !ok {
			// Unlike the file-tab loop this absence is NOT owner-checked --
			// see the note in reconcileOnce: there is no owner to check it
			// against on either side of this comparison (the key normalizes to
			// "" for AGENT).
			//
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
		k := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, row.ID, "")
		hub, ok := hubByKey[k]
		if !ok {
			// Owner-blind for the same reason as reconcileAgents.
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
