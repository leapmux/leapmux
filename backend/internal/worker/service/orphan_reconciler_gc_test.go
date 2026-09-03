package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// These exercise the worktree-GC two-pass logic directly via the
// unexported reconcileOnce (so each call is exactly one pass and the
// prevOrphanWorktrees state carries between them) with a fake ReapWorktree
// that records ids and mirrors ReapOrphanWorktree's DB effect without
// touching disk/git. ReapOrphanWorktree itself (the real removal) is
// covered by TestReapOrphanWorktree_* in close_tab_test.go.

// The guard is an elapsed-time grace, not a pass counter, so these drive a fake
// clock. A pass counter was defeated by ReconcileNudge: the hub can fire passes
// milliseconds apart, so "seen in two consecutive passes" stopped implying "the
// startup window has elapsed".
func TestReconcileWorktrees_ReapsStrandOnlyAfterTheGraceWindow(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	q := svc.Queries
	ctx := context.Background()

	clock := time.Now()
	var reaped []string
	reap := func(rctx context.Context, wt db.Worktree) {
		reaped = append(reaped, wt.ID)
		_ = q.DeleteWorktree(rctx, wt.ID)
		_ = q.DeleteWorktreeTabsByWorktreeID(rctx, wt.ID)
	}
	rec := NewOrphanReconciler(q, svc.TabPayloads, nil, OrphanReconcilerOptions{
		Now:          func() time.Time { return clock },
		ReapWorktree: reap,
		CloseTab:     svc.CloseTabForReconcile,
	})

	// Strand: the worktree's only link points at a CLOSED agent (no live
	// reference) — the exact residue the startup guards can leave behind.
	_, cwErr := q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-strand", WorktreePath: "/r/strand", RepoRoot: "/r", BranchName: "b"})
	require.NoError(t, cwErr)
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a-closed", WorkingDir: "/r/strand", HomeDir: "/r/strand"}))
	require.NoError(t, closeErr(q.CloseAgent(ctx, "a-closed")))
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-strand", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a-closed"}))

	// Live: linked to an OPEN agent — never a candidate.
	_, cwErr = q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-live", WorktreePath: "/r/live", RepoRoot: "/r", BranchName: "b"})
	require.NoError(t, cwErr)
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a-open", WorkingDir: "/r/live", HomeDir: "/r/live"}))
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-live", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a-open"}))

	// Zero-link: freshly created, its tab hasn't linked yet (mid-creation)
	// — must never be reaped.
	_, cwErr = q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-fresh", WorktreePath: "/r/fresh", RepoRoot: "/r", BranchName: "b"})
	require.NoError(t, cwErr)

	rec.reconcileOnce(ctx)
	assert.Empty(t, reaped, "first sighting starts the clock but must not reap")

	// A burst of nudge-driven passes inside the window must NOT reap: this is
	// exactly what a pass counter got wrong.
	clock = clock.Add(time.Second)
	rec.reconcileOnce(ctx)
	rec.reconcileOnce(ctx)
	assert.Empty(t, reaped, "passes fired inside the grace window must not reap, however many there are")

	clock = clock.Add(orphanWorktreeGrace + time.Second)
	rec.reconcileOnce(ctx)
	assert.Equal(t, []string{"wt-strand"}, reaped, "only the strand that stayed orphaned past the grace window is reaped")

	strand, err := q.GetWorktreeByID(ctx, "wt-strand")
	require.NoError(t, err)
	assert.True(t, strand.DeletedAt.Valid, "strand row soft-deleted")
	for _, id := range []string{"wt-live", "wt-fresh"} {
		row, err := q.GetWorktreeByID(ctx, id)
		require.NoError(t, err)
		assert.False(t, row.DeletedAt.Valid, "%s must remain", id)
	}
}

func TestReconcileWorktrees_SparesWorktreeReLinkedDuringTheGraceWindow(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	q := svc.Queries
	ctx := context.Background()

	clock := time.Now()
	var reaped []string
	reap := func(_ context.Context, wt db.Worktree) { reaped = append(reaped, wt.ID) }
	rec := NewOrphanReconciler(q, svc.TabPayloads, nil, OrphanReconcilerOptions{
		Now:          func() time.Time { return clock },
		ReapWorktree: reap,
		CloseTab:     svc.CloseTabForReconcile,
	})

	_, cwErr := q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-reuse", WorktreePath: "/r/reuse", RepoRoot: "/r", BranchName: "b"})
	require.NoError(t, cwErr)
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a1-closed", WorkingDir: "/r/reuse", HomeDir: "/r/reuse"}))
	require.NoError(t, closeErr(q.CloseAgent(ctx, "a1-closed")))
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-reuse", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a1-closed"}))

	rec.reconcileOnce(ctx)
	assert.Empty(t, reaped, "first sighting only starts the clock")

	// Reuse race: between passes a NEW agent opens in the worktree and
	// links it before the predecessor's strand is cleaned, so the worktree
	// now has a live reference.
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a2-open", WorkingDir: "/r/reuse", HomeDir: "/r/reuse"}))
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-reuse", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a2-open"}))

	clock = clock.Add(orphanWorktreeGrace + time.Second)
	rec.reconcileOnce(ctx)
	assert.Empty(t, reaped,
		"a worktree re-linked by a live tab during the window must not be reaped even once the window elapses")
	row, err := q.GetWorktreeByID(ctx, "wt-reuse")
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "reused worktree must remain")
}

func TestWorktreeLiveness_CountAndCandidates_AcrossTabTypes(t *testing.T) {
	t.Parallel()

	// The worktree_tab_liveness view is the single definition of "is this
	// link live?" backing both CountLiveWorktreeRefs and
	// ListOrphanCandidateWorktrees. Pin its predicate across all three tab
	// tables: an agent/terminal counts live while closed_at IS NULL; a FILE
	// tab counts live while its worker_tab_payloads row is present (file tabs
	// are hard-deleted on close, so a missing row is a dead link).
	svc, _, _ := setupTestService(t)
	q := svc.Queries
	ctx := context.Background()

	link := func(wtID, tabID string, tabType leapmuxv1.TabType) {
		require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: wtID, TabType: tabType, TabID: tabID}))
	}
	// FILE links carry the user so worktree_tab_liveness scopes its
	// worker_tab_payloads join by (user_id, tab_id); these all use user-1.
	linkFile := func(wtID, tabID string) {
		require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: wtID, TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: tabID, UserID: "user-1"}))
	}
	mkWorktree := func(id string) {
		_, cwErr := q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: id, WorktreePath: "/r/" + id, RepoRoot: "/r", BranchName: "b"})
		require.NoError(t, cwErr)
	}

	// --- live references: each counts 1, never an orphan candidate ---
	mkWorktree("wt-live-agent")
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a-open", WorkingDir: "/r/wt-live-agent", HomeDir: "/r/wt-live-agent"}))
	link("wt-live-agent", "a-open", leapmuxv1.TabType_TAB_TYPE_AGENT)

	mkWorktree("wt-live-term")
	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{ID: "t-open", Screen: []byte{}}))
	link("wt-live-term", "t-open", leapmuxv1.TabType_TAB_TYPE_TERMINAL)

	mkWorktree("wt-live-file")
	require.NoError(t, q.UpsertWorkerTabPayload(ctx, db.UpsertWorkerTabPayloadParams{UserID: "user-1", TabID: "f-open", TabType: int64(leapmuxv1.TabType_TAB_TYPE_FILE), Payload: mustMarshalFilePayload("/r/wt-live-file/x", "")}))
	linkFile("wt-live-file", "f-open")

	// --- strands: each counts 0, all-strand worktrees are orphan candidates ---
	mkWorktree("wt-dead-agent")
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a-closed", WorkingDir: "/r/wt-dead-agent", HomeDir: "/r/wt-dead-agent"}))
	require.NoError(t, closeErr(q.CloseAgent(ctx, "a-closed")))
	link("wt-dead-agent", "a-closed", leapmuxv1.TabType_TAB_TYPE_AGENT)

	mkWorktree("wt-dead-term")
	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{ID: "t-closed", Screen: []byte{}}))
	require.NoError(t, closeErr(q.CloseTerminal(ctx, "t-closed")))
	link("wt-dead-term", "t-closed", leapmuxv1.TabType_TAB_TYPE_TERMINAL)

	// A FILE link whose worker_tab_payloads row never existed (or was
	// hard-deleted on close) -- a dead link, since file-tab liveness is
	// row-presence, not a closed_at flag.
	mkWorktree("wt-dead-file")
	linkFile("wt-dead-file", "f-gone")

	// --- mixed: a live agent + a closed-terminal strand on one worktree ->
	// counts only the live ref, so it is NOT a candidate ---
	mkWorktree("wt-mixed")
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "m-agent", WorkingDir: "/r/wt-mixed", HomeDir: "/r/wt-mixed"}))
	link("wt-mixed", "m-agent", leapmuxv1.TabType_TAB_TYPE_AGENT)
	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{ID: "m-term", Screen: []byte{}}))
	require.NoError(t, closeErr(q.CloseTerminal(ctx, "m-term")))
	link("wt-mixed", "m-term", leapmuxv1.TabType_TAB_TYPE_TERMINAL)

	for _, tc := range []struct {
		wtID string
		want int64
	}{
		{"wt-live-agent", 1},
		{"wt-live-term", 1},
		{"wt-live-file", 1},
		{"wt-dead-agent", 0},
		{"wt-dead-term", 0},
		{"wt-dead-file", 0},
		{"wt-mixed", 1}, // only the live agent counts; the closed-terminal strand does not
	} {
		got, err := q.CountLiveWorktreeRefs(ctx, tc.wtID)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got, "CountLiveWorktreeRefs(%s)", tc.wtID)
	}

	candidates, err := q.ListOrphanCandidateWorktrees(ctx)
	require.NoError(t, err)
	gotIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		gotIDs = append(gotIDs, c.ID)
	}
	assert.ElementsMatch(t, []string{"wt-dead-agent", "wt-dead-term", "wt-dead-file"}, gotIDs,
		"only worktrees whose every link is a dead strand are orphan candidates")
}

// TestReconcileFileTabs_RoutesThroughSharedTeardownHonouringKeep pins the FILE
// arm of the reconciler against the shared teardown, and pins which link policy
// that teardown applies.
//
// The arm used to hand-roll its own teardown, diverging from the AGENT and
// TERMINAL arms. It now routes through closePayloadTabCommon like they do, with
// dropWorktreeLink -- the same policy an ONLINE KEEP close uses. That is what
// KEEP means: a zero-link worktree is excluded from
// ListOrphanCandidateWorktrees, so the DIRECTORY SURVIVES rather than being
// reaped. A reconciler reap is the offline half of a user's tab close, and an
// offline close pins KEEP, so honouring it here is what stops the offline path
// from destroying a clean worktree the identical online close would have kept.
//
// So: after the reconciler reaps a stale FILE tab, the worker_tab_payloads row must
// be gone, the link must be gone, and the directory must NOT be an orphan
// candidate.
func TestReconcileFileTabs_RoutesThroughSharedTeardownHonouringKeep(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	q := svc.Queries
	ctx := context.Background()

	_, cwErr := q.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID: "wt-fileonly", WorktreePath: "/r/fileonly", RepoRoot: "/r", BranchName: "b",
	})
	require.NoError(t, cwErr)
	// The worktree's ONLY link is this FILE tab.
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{
		WorktreeID: "wt-fileonly", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "file-1", UserID: "user-1",
	}))
	require.NoError(t, q.UpsertWorkerTabPayload(ctx, db.UpsertWorkerTabPayloadParams{
		UserID: "user-1", TabID: "file-1",
		TabType: int64(leapmuxv1.TabType_TAB_TYPE_FILE), Payload: mustMarshalFilePayload("/r/fileonly/a.go", ""),
	}))

	// The hub owns nothing for this worker, so the local file tab is stale.
	listFn := func(context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
		return &leapmuxv1.ListOwnedTabsForWorkerResponse{OwnerUserId: "user-1"}, nil
	}
	// TabGrace -1: this test asserts WHAT the teardown does, not when it is due.
	// TestReconcileTabs_* below covers the grace itself.
	rec := NewOrphanReconciler(q, svc.TabPayloads, listFn, OrphanReconcilerOptions{
		CloseTab: svc.CloseTabForReconcile,
		TabGrace: -1,
	})
	rec.reconcileOnce(ctx)

	// The row is gone: the tab really was reaped, so this is not a no-op pass.
	rows, err := q.ListAllWorkerTabPayloads(ctx)
	require.NoError(t, err)
	for _, r := range rows {
		assert.NotEqual(t, "file-1", r.TabID, "the stale file tab row must be deleted")
	}

	// ...the link is gone, which is how KEEP is expressed.
	remaining, err := q.CountWorktreeTabs(ctx, "wt-fileonly")
	require.NoError(t, err)
	assert.Equal(t, int64(0), remaining, "the shared teardown drops the tab's worktree link")

	// ...and with zero links the directory is NOT a GC candidate, so it survives
	// exactly as it would after an online KEEP close.
	candidates, err := q.ListOrphanCandidateWorktrees(ctx)
	require.NoError(t, err)
	gotIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		gotIDs = append(gotIDs, c.ID)
	}
	assert.NotContains(t, gotIDs, "wt-fileonly",
		"an offline close pins KEEP, so the worktree must not become a reap candidate -- the online close keeps it indefinitely")
}

func TestWorktreeLiveness_FileLeg_IsUserScoped(t *testing.T) {
	t.Parallel()

	// A file tab id is unique only within a user (worker_tab_payloads is keyed by
	// (user_id, tab_id)), so the worktree_tab_liveness FILE leg must scope its
	// join by user. Two users share the tab id "file-dup": user A's link is a
	// strand (its worker_tab_payloads row is gone -- file tabs hard-delete on
	// close), while user B has an identically-id'd LIVE file tab. Without user
	// scoping, user A's strand borrows user B's liveness and user A's worktree is
	// never reclaimed.
	svc, _, _ := setupTestService(t)
	q := svc.Queries
	ctx := context.Background()

	// User A: worktree whose only link is a FILE strand -- no backing
	// worker_tab_payloads row.
	_, cwErr := q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-userA", WorktreePath: "/r/userA", RepoRoot: "/r", BranchName: "b"})
	require.NoError(t, cwErr)
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-userA", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "file-dup", UserID: "user-A"}))

	// User B: a LIVE file tab with the SAME tab id but a different user.
	require.NoError(t, q.UpsertWorkerTabPayload(ctx, db.UpsertWorkerTabPayloadParams{UserID: "user-B", TabID: "file-dup", TabType: int64(leapmuxv1.TabType_TAB_TYPE_FILE), Payload: mustMarshalFilePayload("/r/userB/x", "")}))

	// User A's strand must read as dead -- it must NOT match user B's live tab.
	gotA, err := q.CountLiveWorktreeRefs(ctx, "wt-userA")
	require.NoError(t, err)
	assert.Equal(t, int64(0), gotA, "user A's FILE strand must not borrow user B's live file tab")

	// ...so user A's all-strand worktree is a reclaimable orphan candidate.
	candidates, err := q.ListOrphanCandidateWorktrees(ctx)
	require.NoError(t, err)
	gotIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		gotIDs = append(gotIDs, c.ID)
	}
	assert.Contains(t, gotIDs, "wt-userA", "user A's all-strand worktree must be an orphan candidate")

	// Sanity: a worktree linked to its OWN user's live file tab still counts,
	// so the user-scoped leg is matching, not just failing closed.
	_, cwErr = q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-userB", WorktreePath: "/r/userB", RepoRoot: "/r", BranchName: "b"})
	require.NoError(t, cwErr)
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-userB", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "file-dup", UserID: "user-B"}))
	gotB, err := q.CountLiveWorktreeRefs(ctx, "wt-userB")
	require.NoError(t, err)
	assert.Equal(t, int64(1), gotB, "user B's link to its own live file tab counts")
	candidates, err = q.ListOrphanCandidateWorktrees(ctx)
	require.NoError(t, err)
	gotIDs = gotIDs[:0]
	for _, c := range candidates {
		gotIDs = append(gotIDs, c.ID)
	}
	assert.NotContains(t, gotIDs, "wt-userB", "a worktree with a live same-user file tab is not an orphan")
}

// TestReconcileOnce_LocalProbeFailureIsNotConvergence pins what reconcileOnce's
// bool actually promises.
//
// It used to mean "did the hub leg run", so a pass whose LOCAL SQLite probes all
// errored reported true -- indistinguishable, at the row level, from an idle
// worker with nothing to reconcile. The caller cleared its backoff and armed no
// retry, and the drift sat until the next interval tick an hour later. Busy is
// exactly the state the concurrency that motivated the retry produces, so the
// one pass most in need of a retry was the one that never got one.
func TestReconcileOnce_LocalProbeFailureIsNotConvergence(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	q := svc.Queries
	ctx := context.Background()

	listCalls := 0
	listFn := func(context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
		listCalls++
		return &leapmuxv1.ListOwnedTabsForWorkerResponse{OwnerUserId: "user-1"}, nil
	}
	rec := NewOrphanReconciler(q, svc.TabPayloads, listFn, OrphanReconcilerOptions{
		CloseTab: svc.CloseTabForReconcile,
	})

	// A healthy, genuinely idle worker converges and skips the hub RPC.
	require.True(t, rec.reconcileOnce(ctx), "an idle worker has converged")
	require.Zero(t, listCalls, "and short-circuits before the hub RPC")

	// Break every local read the same way a transient DB error would. Closing
	// is the bluntest form of the failure, and the only one reachable without a
	// query-level seam; what the assertion turns on is the error, not the cause.
	require.NoError(t, svc.DB.Close())

	assert.False(t, rec.reconcileOnce(ctx),
		"a pass whose local probes all errored has NOT converged and must be retried")
}

// The tab arms carry the same shape of elapsed-time grace as the worktree GC
// above, for a sharper reason: a client tombstones its tab over the CRDT
// transport BEFORE it sends the close RPC over the channel, and the hub nudges
// this worker the moment it applies that tombstone. A pass that reaps on the
// first absence therefore preempts the user's own close -- and because the
// convergence teardown drops the tab's worktree_tabs link, the REMOVE that
// close carries then finds no link and degrades to KEEP. The worktree survives
// with zero links, which excludes it from ListOrphanCandidateWorktrees forever.

// TestReconcileTabs_DefersTheReapUntilTheGraceElapses pins that one pass is not
// enough to tear a tab down, and that a later pass past the window still does.
func TestReconcileTabs_DefersTheReapUntilTheGraceElapses(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	q := svc.Queries
	ctx := context.Background()

	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "racing-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	// The hub already applied the client's TombstoneTab, so its owned-tab list
	// no longer names this agent. That is exactly what the nudge delivers.
	listFn := func(context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
		return &leapmuxv1.ListOwnedTabsForWorkerResponse{OwnerUserId: "user-1"}, nil
	}
	clock := time.Now()
	rec := NewOrphanReconciler(q, svc.TabPayloads, listFn, OrphanReconcilerOptions{
		CloseTab: svc.CloseTabForReconcile,
		Now:      func() time.Time { return clock },
		TabGrace: 10 * time.Second,
	})

	assert.False(t, rec.reconcileOnce(ctx),
		"a deferred reap is outstanding work, so the pass has NOT converged")
	row, err := q.GetAgentByID(ctx, "racing-agent")
	require.NoError(t, err)
	require.False(t, row.ClosedAt.Valid,
		"the first pass must not preempt the close RPC that is still in flight")

	clock = clock.Add(11 * time.Second)
	assert.True(t, rec.reconcileOnce(ctx), "the reap is due, so this pass converges")
	row, err = q.GetAgentByID(ctx, "racing-agent")
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "a tab absent past the grace is still reaped")
}

// TestReconcileTabs_RestartsTheClockWhenTheHubListsTheTabAgain pins that the
// grace measures CONTINUOUS absence. A tab the hub names again has to serve a
// fresh window, so a flapping list can never accumulate its way to a reap.
func TestReconcileTabs_RestartsTheClockWhenTheHubListsTheTabAgain(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	q := svc.Queries
	ctx := context.Background()

	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "flapping-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	present := false
	listFn := func(context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
		resp := &leapmuxv1.ListOwnedTabsForWorkerResponse{OwnerUserId: "user-1"}
		if present {
			resp.Tabs = []*leapmuxv1.OwnedTab{{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "flapping-agent",
			}}
		}
		return resp, nil
	}
	clock := time.Now()
	rec := NewOrphanReconciler(q, svc.TabPayloads, listFn, OrphanReconcilerOptions{
		CloseTab: svc.CloseTabForReconcile,
		Now:      func() time.Time { return clock },
		TabGrace: 10 * time.Second,
	})

	rec.reconcileOnce(ctx) // absent: starts the clock
	present = true
	clock = clock.Add(6 * time.Second)
	rec.reconcileOnce(ctx) // present: drops out of the map
	present = false
	clock = clock.Add(6 * time.Second)
	rec.reconcileOnce(ctx) // absent again: 12s total elapsed, but only 0s continuous

	row, err := q.GetAgentByID(ctx, "flapping-agent")
	require.NoError(t, err)
	assert.False(t, row.ClosedAt.Valid,
		"the clock restarts on every reappearance, so accumulated absence must not reap")
}

// TestReconcileTabs_RemoveCloseKeepsItsWorktreeLinkAcrossARacingPass is the
// regression for the production failure: an in-flight CloseAgent(REMOVE) whose
// CRDT tombstone already reached the hub must still find its worktree link.
func TestReconcileTabs_RemoveCloseKeepsItsWorktreeLinkAcrossARacingPass(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	q := svc.Queries
	ctx := context.Background()

	_, cwErr := q.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID: "wt-racing", WorktreePath: "/r/racing", RepoRoot: "/r", BranchName: "doomed",
	})
	require.NoError(t, cwErr)
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{
		ID: "racing-agent", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	svc.registerTabForWorktree("wt-racing", leapmuxv1.TabType_TAB_TYPE_AGENT, "racing-agent")

	listFn := func(context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
		return &leapmuxv1.ListOwnedTabsForWorkerResponse{OwnerUserId: "user-1"}, nil
	}
	clock := time.Now()
	rec := NewOrphanReconciler(q, svc.TabPayloads, listFn, OrphanReconcilerOptions{
		CloseTab: svc.CloseTabForReconcile,
		Now:      func() time.Time { return clock },
		TabGrace: 10 * time.Second,
	})

	rec.reconcileOnce(ctx)

	// The link survives, so the REMOVE that follows can still ref-count to zero
	// and reclaim the worktree. Without the grace this count is 0, the close
	// degrades to KEEP, and the directory is stranded with no link to make it a
	// GC candidate.
	remaining, err := q.CountWorktreeTabs(ctx, "wt-racing")
	require.NoError(t, err)
	assert.Equal(t, int64(1), remaining,
		"a racing pass must not drop the worktree link the in-flight REMOVE needs")
}
