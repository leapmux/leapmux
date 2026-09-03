package service

import (
	"context"
	"log/slog"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/backoffutil"
	"github.com/leapmux/leapmux/internal/util/nilcheck"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/generated/db"
)

// OrphanReconciler periodically reconciles the worker's local
// agents / terminals / file-tab rows against the hub's
// workspace_tab_owned view. Its job is to absorb client crashes /
// network partitions that left the worker side and the CRDT side
// disagreeing:
//
//   - Local entity present, hub doesn't know about it -> stop its process
//     and tombstone the local agent / terminal / file-tab row.
//
// There is no second, drift-repair half any more. The worker stores no
// workspace id, so "which workspace is this tab in?" is a question only the
// CRDT answers and only the CRDT can get wrong -- absence is the entire
// comparison, and it is what makes an offline close and an offline
// cross-workspace move converge with nothing to reconcile on this side.
//
// The reconciler runs every interval (default 1 hour) and on
// explicit Trigger() calls (e.g. on worker reconnect).
type OrphanReconciler struct {
	queries  *db.Queries
	listFn   func(ctx context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error)
	now      func() time.Time
	interval time.Duration
	trigger  chan struct{}
	stop     chan struct{}
	done     chan struct{}
	logger   *slog.Logger

	// reapWorktree removes a worktree confirmed orphaned (all its tab
	// links are strands). Nil disables worktree GC (tests that don't
	// exercise it leave it unset).
	reapWorktree func(ctx context.Context, wt db.Worktree)

	// closeTab performs the full per-type teardown for a tab the hub no longer
	// knows about, so an offline convergence matches an online close. Required at
	// construction; there is no narrower fallback (see OrphanReconcilerOptions).
	//
	// One hook rather than three: the per-type dispatch belongs to the Service side
	// (closeTabForConvergence), and splitting it here meant every caller restated
	// which policy each type gets.
	closeTab func(tabType leapmuxv1.TabType, userID, tabID string)
	// onConverged reports a CONVERGED pass to the caller. Nil disables the report.
	onConverged func()
	// prevOrphanWorktrees maps a worktree id to when it FIRST looked orphaned. A
	// worktree must stay orphaned for orphanWorktreeGrace before
	// reconcileWorktrees removes it, so a transient zero-live window during
	// startup or worktree reuse is never mistaken for a strand.
	prevOrphanWorktrees map[string]time.Time
	// prevOrphanTabs maps a reconcilable tab to when it FIRST looked absent from
	// the hub's owned-tab list. A tab must stay absent for tabGrace before any
	// case tears it down. See orphanTabGrace for the size of that window.
	prevOrphanTabs map[ownedTabKey]time.Time
	// tabGrace is orphanTabGrace unless a caller overrode it.
	tabGrace time.Duration
}

// OrphanReconcilerOptions configures NewOrphanReconciler.
//
// The three Close*Tab hooks are required; everything else has a default. There
// are no longer separate Agents / Terminals process-stopper hooks: stopping the
// subprocess is part of the shared per-type teardown the Close*Tab hooks run, so
// a caller cannot wire one without the other.
type OrphanReconcilerOptions struct {
	Interval time.Duration
	Now      func() time.Time
	Logger   *slog.Logger
	// TabGrace overrides orphanTabGrace, the delay between a tab first looking
	// absent from the hub and a case that tears it down. Zero selects the default.
	// A negative value disables the grace, which only a test that drives the
	// real Run loop in real time should choose -- production must keep the
	// window, because it is what stops a nudge-driven pass from preempting the
	// close RPC that is still in flight.
	TabGrace time.Duration
	// ReapWorktree, when set, enables the orphan-worktree GC pass: it is
	// invoked for each worktree confirmed orphaned across two consecutive
	// reconcile passes. Wire it to (*Service).ReapOrphanWorktree.
	ReapWorktree func(ctx context.Context, wt db.Worktree)
	// CloseTab performs the FULL per-type teardown for a tab the hub no longer
	// knows about. Wire it to (*Service).CloseTabForReconcile so an offline
	// convergence tears a tab down exactly as an online close does, and so the
	// worktree-link policy is chosen once on that side rather than per call here.
	//
	// Injected rather than called directly for the same reason ReapWorktree is:
	// the reconciler is deliberately independent of the agent/terminal packages,
	// which is what lets its tests drive it with narrow fakes.
	//
	// All three are REQUIRED -- NewOrphanReconciler panics without them. They used
	// to be optional, falling back to a DB close plus a process stop, which the
	// surrounding comments themselves called "NOT enough in production": it
	// skipped the startup cancel, the runtime-state clear and the registered
	// cleanups (the remote-IPC teardown among them), and leaked one terminal
	// manager entry per reap. A tier that exists only for test convenience is
	// exactly the divergent close path this change set out to remove.
	CloseTab func(tabType leapmuxv1.TabType, userID, tabID string)
	// OnConverged, when set, is called after every pass that CONVERGED (see
	// reconcileOnce). It is the ONE place a caller learns that this worker's
	// local rows now agree with the hub, which is the precondition the boot-time
	// agent resume waits on: resuming before convergence spawns CLIs for tabs the
	// CRDT deleted while the worker was down, and a converged pass is the only
	// statement that no reap is pending.
	//
	// It reports the converged pass alone, rather than every pass with a flag,
	// so a caller cannot start work after a pass that failed. Every pass reports
	// through it, including one reached by the retry backoff: the startup pass of
	// a worker whose channel is not settled yet always fails, so a hook that
	// fired only on the first pass would never see the eventual convergence.
	//
	// Called on the reconciler's own goroutine, so it must not block: a slow hook
	// delays the next pass. Wire it to something that hands the work off.
	OnConverged func()
}

// NewOrphanReconciler binds a reconciler to the worker's local DB queries.
// listFn is the hub-side ListOwnedTabsForWorker call (injected so tests can
// substitute a fake); every teardown goes through the injected CloseTab.
//
// listFn hands back the WHOLE response, not just its tabs: the reap decision
// needs the owner the response declares (see reconcileTabPayloads), and a
// signature that returned the tab list alone would let a caller drop that owner
// on the floor and turn a narrow list into a universal absence.
func NewOrphanReconciler(queries *db.Queries, listFn func(ctx context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error), opts OrphanReconcilerOptions) *OrphanReconciler {
	if opts.Interval <= 0 {
		opts.Interval = time.Hour
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.TabGrace == 0 {
		opts.TabGrace = orphanTabGrace
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	// nilcheck, not `== nil`: a nil func converted to the field type is still a
	// non-nil interface in some shapes, and an unwired hook must fail at
	// construction rather than on the first reap -- which is hourly, offline, and
	// unattended, i.e. the worst place to discover it. Mirrors workermgr.New.
	if nilcheck.IsNilDependency(opts.CloseTab) {
		panic("service: NewOrphanReconciler requires CloseTab" +
			" (the offline teardown must be the same one an online close runs)")
	}
	return &OrphanReconciler{
		queries:             queries,
		listFn:              listFn,
		now:                 opts.Now,
		interval:            opts.Interval,
		trigger:             make(chan struct{}, 1),
		stop:                make(chan struct{}),
		done:                make(chan struct{}),
		logger:              opts.Logger,
		reapWorktree:        opts.ReapWorktree,
		closeTab:            opts.CloseTab,
		onConverged:         opts.OnConverged,
		prevOrphanWorktrees: make(map[string]time.Time),
		prevOrphanTabs:      make(map[ownedTabKey]time.Time),
		tabGrace:            opts.TabGrace,
	}
}

// orphanWorktreeGrace is how long a worktree must look orphaned before the GC
// will reclaim it. It limits the startup window between "the agent/terminal row
// exists" and "its worktree_tabs link is written", which is one `git worktree add`
// plus a couple of local SQLite writes -- so this is generous by two orders of
// magnitude, deliberately: over-waiting costs a directory a later pass reclaims,
// while under-waiting destroys a live tab's worktree.
const orphanWorktreeGrace = 2 * time.Minute

// orphanTabGrace is how long a locally open tab must stay absent from the hub's
// owned-tab list before a case tears it down.
//
// It limits the window that a client's OWN close opens. The client emits the
// CRDT TombstoneTab first and sends the close RPC second, on a different
// transport, and the hub nudges this worker the moment it applies that
// tombstone. So a nudge-driven pass can see the absence while the close that
// carries the user's WorktreeAction is still in flight. A reap in that window
// runs the KEEP-shaped convergence teardown, which drops the tab's
// worktree_tabs link; the real REMOVE close then finds no link, degrades to
// KEEP, and leaves the worktree on disk with nothing left to reclaim it -- a
// zero-link worktree is excluded from ListOrphanCandidateWorktrees forever.
//
// The measured window is 2-6 ms in a single-process `leapmux dev`, so this is
// generous by three orders of magnitude, deliberately: over-waiting delays a
// teardown that a later pass performs anyway, while under-waiting silently
// downgrades every online worktree-removing close.
const orphanTabGrace = 10 * time.Second

// Trigger schedules an immediate reconciliation pass. Non-blocking;
// duplicate triggers coalesce.
func (r *OrphanReconciler) Trigger() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

// reconcileRetryBase is the first delay after a pass that did not converge.
const reconcileRetryBase = time.Second

// Run blocks until ctx is cancelled or Stop is called. Run a single
// pass on start, then run on each interval tick or Trigger().
//
// A pass whose hub leg failed is retried on a short backoff rather than left
// until the next tick. Trigger() is fired on reconnect, which is exactly when
// the hub RPC is most likely to be answered by a channel that has not settled
// yet -- and dropping that one shot meant the reconnect converged nothing and
// the worker kept a closed tab's process alive for up to the full interval.
func (r *OrphanReconciler) Run(ctx context.Context) {
	defer close(r.done)
	t := time.NewTicker(r.interval)
	defer t.Stop()

	// Deterministic (no jitter): a single worker retrying its own hub is not a
	// thundering herd, and the retry test asserts convergence inside a fixed
	// budget. Capped at the ordinary interval, so a worker that is genuinely
	// offline decays into the hourly cadence instead of hammering a hub that
	// cannot answer.
	retryBackoff := backoffutil.NewBackoff(reconcileRetryBase, r.interval, 0)
	var (
		retryTimer *time.Timer
		retryC     <-chan time.Time
	)
	// finishPass is the one helper for "the pass finished": it reports a
	// convergence AND re-arms (or disarms) the retry from the outcome, so a
	// wake-up source can never re-arm without reporting. Splitting the two is
	// how the report would come to be missing from the retry case -- the case
	// that fires after a hub failure, and so the one that carries the eventual
	// convergence. It always stops the previous timer first, so a tick or a
	// Trigger that lands mid-backoff cannot leave two pending retries behind.
	finishPass := func(converged bool) {
		if converged && r.onConverged != nil {
			r.onConverged()
		}
		if retryTimer != nil {
			retryTimer.Stop()
			retryTimer, retryC = nil, nil
		}
		if converged {
			retryBackoff.Reset()
			return
		}
		retryTimer = time.NewTimer(retryBackoff.Next())
		retryC = retryTimer.C
	}
	defer func() {
		if retryTimer != nil {
			retryTimer.Stop()
		}
	}()

	finishPass(r.reconcileOnce(ctx))
	for {
		// Every wake-up source runs the same pass, so the cases only select;
		// the call sits below them and a fourth source costs one line.
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		case <-t.C:
		case <-r.trigger:
		case <-retryC:
		}
		finishPass(r.reconcileOnce(ctx))
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
// worker_tab_payloads are both keyed by (user_id, tab_id) -- a FILE tab id is
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

// reconcileOnce runs one pass and reports whether EVERY leg it attempted
// actually completed. False means the pass learned less than it set out to --
// the hub RPC failed, its response declared no owner scope, or a local SQLite
// read errored -- so the caller retries on a backoff instead of leaving the
// drift until the next interval tick. Having nothing to reconcile is not a
// failure: an absent listFn and an idle worker both report true.
//
// The local legs count, not just the hub one. A read that errors is
// indistinguishable at the row level from a table that is empty, so reporting
// convergence on it would tell the caller "idle, all good" for a worker whose
// DB was merely busy -- and busy is exactly the state the concurrency that
// motivated this retry produces. The drift then sits for a full hour.
func (r *OrphanReconciler) reconcileOnce(ctx context.Context) bool {
	// Worktree GC is local-only (no hub dependency) and must run even when
	// the hub list is unavailable or there are no live tab rows: a strand
	// can outlive its tab row once the cleanup loop hard-deletes the closed
	// agent/terminal, so it would be invisible to the hasAnyLocalRows
	// short-circuit below.
	converged := r.reconcileWorktrees(ctx)

	if r.listFn == nil {
		return converged
	}
	// Probe the local tables first — they're cheap (in-process SQLite)
	// — so an idle worker can skip the hub RPC entirely when there's
	// nothing to reconcile.
	hasLocal, localOK := r.hasAnyLocalRows(ctx)
	if !localOK {
		converged = false
	}
	if !hasLocal {
		return converged
	}

	resp, err := r.listFn(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list owned tabs", "err", err)
		return false
	}
	owner, ownerOK := userid.New(resp.GetOwnerUserId())
	if !ownerOK {
		// No declared scope, so the response is authoritative for nobody and
		// every absence below would be an unfounded reap. Nothing to do: a
		// response with no owner is not one we can attribute any row to.
		r.logger.Warn("orphan reconciler: hub response declares no owner scope; skipping this pass",
			"hub_tabs", len(resp.GetTabs()))
		return false
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
	now := r.now()
	next := make(map[ownedTabKey]time.Time)
	r.reconcileTabPayloads(ctx, hubByKey, owner, now, next)
	r.reconcileAgents(ctx, hubByKey, now, next)
	r.reconcileTerminals(ctx, hubByKey, now, next)
	// Assigned ONLY here, on the path where all three cases ran. Every early
	// return above learned nothing about absence, so overwriting the map there
	// would restart each tab's clock and defeat the grace.
	r.prevOrphanTabs = next
	if len(next) > 0 {
		// A deferred reap is unfinished business, not convergence. Report it so
		// Run re-arms its retry backoff and a later pass does the teardown,
		// instead of leaving it to the hourly tick.
		converged = false
	}
	return converged
}

// reapDue records that k looked absent on this pass, and reports whether it
// looked absent for longer than orphanTabGrace.
//
// A key the hub lists again simply never reaches here, so it drops out of
// `next` and its clock restarts on the following absence. That is what makes a
// transient absence -- the close-RPC race that orphanTabGrace exists for -- cost
// one deferred pass rather than a destructive teardown.
func (r *OrphanReconciler) reapDue(k ownedTabKey, now time.Time, next map[ownedTabKey]time.Time) bool {
	if r.tabGrace < 0 {
		return true
	}
	firstSeen, seen := r.prevOrphanTabs[k]
	if !seen {
		next[k] = now
		return false
	}
	if now.Sub(firstSeen) < r.tabGrace {
		next[k] = firstSeen
		return false
	}
	return true
}

// hasAnyLocalRows reports whether at least one of the three reconciled local
// tables (worker_tab_payloads, agents, terminals) has any row, and whether every
// probe actually answered. Used by reconcileOnce to short-circuit before the
// hub RPC on idle workers.
//
// ok=false means at least one read errored, so `has` is a floor rather than an
// answer: the caller must not treat a false `has` as "this worker is idle".
// All three run even after one fails -- they are in-process SQLite reads on an
// hourly loop, and a partial probe that stopped early would report a narrower
// floor for no saving worth having.
func (r *OrphanReconciler) hasAnyLocalRows(ctx context.Context) (has, ok bool) {
	if r.queries == nil {
		return true, true
	}
	ok = true
	payloadRows, err := r.queries.ListAllWorkerTabPayloads(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: probe worker_tab_payloads", "err", err)
		ok = false
	}
	agentIDs, err := r.queries.ListAllAgentIDs(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: probe agents", "err", err)
		ok = false
	}
	terminalIDs, err := r.queries.ListAllTerminalIDs(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: probe terminals", "err", err)
		ok = false
	}
	return len(payloadRows) > 0 || len(agentIDs) > 0 || len(terminalIDs) > 0, ok
}

// reconcileWorktrees reclaims worktrees whose tab links are all
// startup-race strands — no live agent/terminal/file tab references them.
//
// A worktree must have looked orphaned for at least orphanWorktreeGrace before it
// is removed. The transient zero-live windows -- agent/terminal startup, where the
// row exists before its worktree_tabs link is written, and worktree reuse -- are
// far shorter than that, so only a genuine strand survives it.
//
// The guard used to count PASSES ("seen in two consecutive passes"), justified by
// those windows being far shorter than the reconcile INTERVAL. ReconcileNudge
// broke that premise: the hub can now fire passes arbitrarily close together, and
// Trigger's size-1 buffer only coalesces nudges that arrive DURING a pass, so two
// passes can run milliseconds apart. Two back-to-back tombstone batches were
// therefore enough to reap the worktree of an agent still inside its startup
// window -- and reapWorktree's under-lock re-check sees the same zero, because the
// link still is not written. Elapsed time is independent of who triggers a pass,
// so a new trigger cannot silently defeat it again.
//
// This is the backstop the startup link guards in runAgentStartup /
// runTerminalStartup rely on: without it, a close that raced startup — or
// a link written when getAgentByID/GetTerminalForReady returned a
// transient error and the guard fell through to link — would strand a
// worktree_tabs row whose tab is gone and leak the worktree dir forever
// (reconcileAgents/reconcileTerminals close the tab row but never drop
// worktree_tabs links).
// Returns false when the candidate list could not be read, so the caller can
// retry: leaving it until the next hourly tick is how a worktree survives a
// transient DB error for an hour after its last tab is gone.
func (r *OrphanReconciler) reconcileWorktrees(ctx context.Context) bool {
	if r.reapWorktree == nil {
		return true
	}
	candidates, err := r.queries.ListOrphanCandidateWorktrees(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list orphan-candidate worktrees", "err", err)
		return false
	}
	now := r.now()
	nextOrphans := make(map[string]time.Time, len(candidates))
	for _, wt := range candidates {
		firstSeen, seenBefore := r.prevOrphanWorktrees[wt.ID]
		if !seenBefore {
			// First sighting: start its clock and let a later pass decide.
			nextOrphans[wt.ID] = now
			continue
		}
		if now.Sub(firstSeen) < orphanWorktreeGrace {
			// Still inside the grace window -- carry the ORIGINAL timestamp so a
			// burst of nudge-driven passes cannot keep resetting it.
			nextOrphans[wt.ID] = firstSeen
			continue
		}
		// Orphaned for longer than any startup or reuse window lasts.
		// reapWorktree re-checks live refs under the per-worktree lock, and
		// refuses outright if the tree holds uncommitted or unpushed work.
		r.reapWorktree(ctx, wt)
	}
	r.prevOrphanWorktrees = nextOrphans
	return true
}

// reconcileTabPayloads reaps local tab-payload rows the hub no longer lists.
// Both payload-backed kinds -- FILE and IMAGE -- are in scope: the row states
// its own tab_type, so this walk never enumerates the kinds.
//
// owner is the single owner the hub response is authoritative about -- exactly
// one, the calling worker's registrant, because the hub's query binds user_id
// (workspace_tab_owned is keyed by (user_id, tab_id), so worker_id alone
// selects across tenants).
//
// The read below walks EVERY owner's rows, and the owner check lives at the
// reap instead. Scoping the read would look equivalent and would be a worse
// place to put it: the reap is an inference from ABSENCE, so it is the one
// step whose correctness depends on knowing which owner the hub answered for,
// and stating that at the destructive line is what keeps a future reader from
// widening the read and silently widening the reap with it. Pinned by
// TestOrphanReconciler_FileTab_SharedTabIDStaysWithItsOwner.
func (r *OrphanReconciler) reconcileTabPayloads(ctx context.Context, hubByKey map[ownedTabKey]*leapmuxv1.OwnedTab, owner userid.UserID, now time.Time, next map[ownedTabKey]time.Time) {
	rows, err := r.queries.ListAllWorkerTabPayloads(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list worker_tab_payloads", "err", err)
		return
	}
	for _, row := range rows {
		// The row's OWN kind, not a constant: one table now holds both FILE and
		// IMAGE rows, and keying an IMAGE row as FILE would miss the hub's entry
		// for it and reap a live tab.
		tabType := leapmuxv1.TabType(row.TabType)
		k := newOwnedTabKey(tabType, row.TabID, row.UserID)
		if _, ok := hubByKey[k]; !ok {
			// INVARIANT: absence is only evidence of an orphan for the owner
			// the response covers. ListAllWorkerTabPayloads walks EVERY owner's
			// rows, while the hub list is scoped to one. So for any
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
			// Grace comes AFTER the owner check, so a row belonging to another
			// owner never enters the map and never starts a clock.
			if !r.reapDue(k, now, next) {
				continue
			}
			// Route through the SAME teardown the AGENT and TERMINAL cases use.
			// r.closeTab is (*Service).CloseTabForReconcile, which DROPS the
			// worktree_tabs link -- and dropping it is how KEEP is expressed. A
			// zero-link worktree is excluded from ListOrphanCandidateWorktrees
			// (that query requires at least one link), so the directory survives
			// until the user removes it. A reconciler reap is the OFFLINE half of
			// the user's own tab close, and an offline close pins KEEP, so
			// honouring it here is what stops this path from destroying a clean
			// worktree that the identical online close keeps.
			//
			// The opposite policy, keepWorktreeLinkForReconciler, belongs to
			// closeTabForDeletedWorkspace alone: there the workspace is gone, so
			// no user intent for the directory survives, and the strand is what
			// leaves it a GC candidate for reapWorktree.
			//
			// Pinned by
			// TestReconcileFileTabs_RoutesThroughSharedTeardownHonouringKeep.
			r.closeTab(tabType, row.UserID, row.TabID)
		}
	}
}

// reconcileAgents iterates every locally-known agent and absorbs the
// hub's view: hub-absent -> stop the subprocess and close the row locally.
func (r *OrphanReconciler) reconcileAgents(ctx context.Context, hubByKey map[ownedTabKey]*leapmuxv1.OwnedTab, now time.Time, next map[ownedTabKey]time.Time) {
	// OPEN rows only. A closed row has already been torn down, so comparing it
	// against the hub's live list only re-ran the full teardown -- cancelAndClear,
	// StopAgent, ClearAgentRuntimeState, the registered cleanups -- and re-logged
	// "closed stale agent" on every pass, for the whole 7-day retention window,
	// now once per ReconcileNudge rather than hourly.
	//
	// Reading all rows used to be load-bearing for an undocumented reason: a reap
	// landing in the startup window (row written, cleanup not yet registered) made
	// the cleanup run a no-op, and only a LATER pass over the closed row could
	// retry it. cleanupRegistry.claim now closes that window at the source -- the
	// cleanup fires the moment it registers -- so the backstop is no longer needed
	// and the redundant work is gone with it.
	rows, err := r.queries.ListAllOpenRootAgentIDs(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list agents", "err", err)
		return
	}
	for _, agentID := range rows {
		k := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_AGENT, agentID, "")
		if _, ok := hubByKey[k]; !ok {
			// Absent, but not yet for long enough. The client's own close
			// tombstones the tab before its RPC lands, so an immediate reap
			// here would drop the worktree link that close is about to use.
			if !r.reapDue(k, now, next) {
				continue
			}
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
			// Route through the SAME teardown an online CloseAgent runs.
			// Doing only the DB close plus StopAgent -- which is what this used
			// to do -- skipped the startup cancel, the runtime-state clear, and
			// the registered cleanups; the last of those is the remote-IPC
			// teardown, so the tab's unix-socket listener stayed open and its
			// delegation token stayed unrevoked for the worker's lifetime.
			//
			// There is no narrower fallback any more: the hook is required at
			// construction, so the offline path cannot be a weaker subset of the
			// online one even by misconfiguration.
			r.closeTab(leapmuxv1.TabType_TAB_TYPE_AGENT, "", agentID)
			r.logger.Info("orphan reconciler: closed stale agent", "agent_id", agentID)
		}
	}
}

// reconcileTerminals does the same for terminals.
func (r *OrphanReconciler) reconcileTerminals(ctx context.Context, hubByKey map[ownedTabKey]*leapmuxv1.OwnedTab, now time.Time, next map[ownedTabKey]time.Time) {
	// OPEN rows only, for the same reasons as reconcileAgents.
	rows, err := r.queries.ListAllOpenTerminalIDs(ctx)
	if err != nil {
		r.logger.Warn("orphan reconciler: list terminals", "err", err)
		return
	}
	for _, row := range rows {
		k := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_TERMINAL, row, "")
		if _, ok := hubByKey[k]; !ok {
			// Same grace as reconcileAgents: a close RPC still in flight must
			// not lose its worktree link to this pass.
			if !r.reapDue(k, now, next) {
				continue
			}
			// Owner-blind for the same reason as reconcileAgents.
			// Symmetric to reconcileAgents: SQLite close + send a
			// stop signal to the in-memory terminal manager so the
			// PTY-attached shell process is reaped at reconcile
			// time, not at worker restart.
			// Same reasoning as reconcileAgents: one shared teardown, so the
			// offline path cannot be a weaker subset of the online one. The shared
			// helper uses RemoveTerminal rather than StopTerminal, which also drops
			// the manager's terminals/meta/exitDone entries -- the fallback this
			// replaced leaked one set per reaped terminal.
			r.closeTab(leapmuxv1.TabType_TAB_TYPE_TERMINAL, "", row)
			r.logger.Info("orphan reconciler: closed stale terminal", "terminal_id", row)
		}
	}
}
