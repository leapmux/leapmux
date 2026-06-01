package service

import (
	"context"
	"testing"

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

func TestReconcileWorktrees_ReapsStrandOnlyAfterTwoPasses(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	q := svc.Queries
	ctx := context.Background()

	var reaped []string
	reap := func(rctx context.Context, wt db.Worktree) {
		reaped = append(reaped, wt.ID)
		_ = q.DeleteWorktree(rctx, wt.ID)
		_ = q.DeleteWorktreeTabsByWorktreeID(rctx, wt.ID)
	}
	rec := NewOrphanReconciler(q, svc.FileTabPaths, nil, OrphanReconcilerOptions{ReapWorktree: reap})

	// Strand: the worktree's only link points at a CLOSED agent (no live
	// reference) — the exact residue the startup guards can leave behind.
	require.NoError(t, q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-strand", WorktreePath: "/r/strand", RepoRoot: "/r", BranchName: "b"}))
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a-closed", WorkspaceID: "ws-1", WorkingDir: "/r/strand", HomeDir: "/r/strand"}))
	require.NoError(t, q.CloseAgent(ctx, "a-closed"))
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-strand", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a-closed"}))

	// Live: linked to an OPEN agent — never a candidate.
	require.NoError(t, q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-live", WorktreePath: "/r/live", RepoRoot: "/r", BranchName: "b"}))
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a-open", WorkspaceID: "ws-1", WorkingDir: "/r/live", HomeDir: "/r/live"}))
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-live", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a-open"}))

	// Zero-link: freshly created, its tab hasn't linked yet (mid-creation)
	// — must never be reaped.
	require.NoError(t, q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-fresh", WorktreePath: "/r/fresh", RepoRoot: "/r", BranchName: "b"}))

	rec.reconcileOnce(ctx)
	assert.Empty(t, reaped, "first pass records the strand but must not reap it")

	rec.reconcileOnce(ctx)
	assert.Equal(t, []string{"wt-strand"}, reaped, "only the strand that persisted across two passes is reaped")

	strand, err := q.GetWorktreeByID(ctx, "wt-strand")
	require.NoError(t, err)
	assert.True(t, strand.DeletedAt.Valid, "strand row soft-deleted")
	for _, id := range []string{"wt-live", "wt-fresh"} {
		row, err := q.GetWorktreeByID(ctx, id)
		require.NoError(t, err)
		assert.False(t, row.DeletedAt.Valid, "%s must remain", id)
	}
}

func TestReconcileWorktrees_SparesWorktreeReLinkedBetweenPasses(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	q := svc.Queries
	ctx := context.Background()

	var reaped []string
	reap := func(_ context.Context, wt db.Worktree) { reaped = append(reaped, wt.ID) }
	rec := NewOrphanReconciler(q, svc.FileTabPaths, nil, OrphanReconcilerOptions{ReapWorktree: reap})

	require.NoError(t, q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-reuse", WorktreePath: "/r/reuse", RepoRoot: "/r", BranchName: "b"}))
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a1-closed", WorkspaceID: "ws-1", WorkingDir: "/r/reuse", HomeDir: "/r/reuse"}))
	require.NoError(t, q.CloseAgent(ctx, "a1-closed"))
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-reuse", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a1-closed"}))

	rec.reconcileOnce(ctx)
	assert.Empty(t, reaped, "first pass only records")

	// Reuse race: between passes a NEW agent opens in the worktree and
	// links it before the predecessor's strand is cleaned, so the worktree
	// now has a live reference.
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a2-open", WorkspaceID: "ws-1", WorkingDir: "/r/reuse", HomeDir: "/r/reuse"}))
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-reuse", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: "a2-open"}))

	rec.reconcileOnce(ctx)
	assert.Empty(t, reaped, "a worktree re-linked by a live tab between passes must not be reaped")
	row, err := q.GetWorktreeByID(ctx, "wt-reuse")
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "reused worktree must remain")
}

func TestWorktreeLiveness_CountAndCandidates_AcrossTabTypes(t *testing.T) {
	// The worktree_tab_liveness view is the single definition of "is this
	// link live?" backing both CountLiveWorktreeRefs and
	// ListOrphanCandidateWorktrees. Pin its predicate across all three tab
	// tables: an agent/terminal counts live while closed_at IS NULL; a FILE
	// tab counts live while its worker_file_tabs row is present (file tabs
	// are hard-deleted on close, so a missing row is a dead link).
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	q := svc.Queries
	ctx := context.Background()

	link := func(wtID, tabID string, tabType leapmuxv1.TabType) {
		require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: wtID, TabType: tabType, TabID: tabID}))
	}
	// FILE links carry the org so worktree_tab_liveness scopes its
	// worker_file_tabs join by (org_id, tab_id); these all use org-1.
	linkFile := func(wtID, tabID string) {
		require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: wtID, TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: tabID, OrgID: "org-1"}))
	}
	mkWorktree := func(id string) {
		require.NoError(t, q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: id, WorktreePath: "/r/" + id, RepoRoot: "/r", BranchName: "b"}))
	}

	// --- live references: each counts 1, never an orphan candidate ---
	mkWorktree("wt-live-agent")
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a-open", WorkspaceID: "ws-1", WorkingDir: "/r/wt-live-agent", HomeDir: "/r/wt-live-agent"}))
	link("wt-live-agent", "a-open", leapmuxv1.TabType_TAB_TYPE_AGENT)

	mkWorktree("wt-live-term")
	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{ID: "t-open", WorkspaceID: "ws-1", Screen: []byte{}}))
	link("wt-live-term", "t-open", leapmuxv1.TabType_TAB_TYPE_TERMINAL)

	mkWorktree("wt-live-file")
	require.NoError(t, q.UpsertWorkerFileTab(ctx, db.UpsertWorkerFileTabParams{OrgID: "org-1", TabID: "f-open", WorkspaceID: "ws-1", FilePath: "/r/wt-live-file/x"}))
	linkFile("wt-live-file", "f-open")

	// --- strands: each counts 0, all-strand worktrees are orphan candidates ---
	mkWorktree("wt-dead-agent")
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "a-closed", WorkspaceID: "ws-1", WorkingDir: "/r/wt-dead-agent", HomeDir: "/r/wt-dead-agent"}))
	require.NoError(t, q.CloseAgent(ctx, "a-closed"))
	link("wt-dead-agent", "a-closed", leapmuxv1.TabType_TAB_TYPE_AGENT)

	mkWorktree("wt-dead-term")
	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{ID: "t-closed", WorkspaceID: "ws-1", Screen: []byte{}}))
	require.NoError(t, q.CloseTerminal(ctx, "t-closed"))
	link("wt-dead-term", "t-closed", leapmuxv1.TabType_TAB_TYPE_TERMINAL)

	// A FILE link whose worker_file_tabs row never existed (or was
	// hard-deleted on close) -- a dead link, since file-tab liveness is
	// row-presence, not a closed_at flag.
	mkWorktree("wt-dead-file")
	linkFile("wt-dead-file", "f-gone")

	// --- mixed: a live agent + a closed-terminal strand on one worktree ->
	// counts only the live ref, so it is NOT a candidate ---
	mkWorktree("wt-mixed")
	require.NoError(t, q.CreateAgent(ctx, db.CreateAgentParams{ID: "m-agent", WorkspaceID: "ws-1", WorkingDir: "/r/wt-mixed", HomeDir: "/r/wt-mixed"}))
	link("wt-mixed", "m-agent", leapmuxv1.TabType_TAB_TYPE_AGENT)
	require.NoError(t, q.UpsertTerminal(ctx, db.UpsertTerminalParams{ID: "m-term", WorkspaceID: "ws-1", Screen: []byte{}}))
	require.NoError(t, q.CloseTerminal(ctx, "m-term"))
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

func TestWorktreeLiveness_FileLeg_IsOrgScoped(t *testing.T) {
	// A file tab id is unique only within an org (worker_file_tabs is keyed by
	// (org_id, tab_id)), so the worktree_tab_liveness FILE leg must scope its
	// join by org. Two orgs share the tab id "file-dup": org A's link is a
	// strand (its worker_file_tabs row is gone -- file tabs hard-delete on
	// close), while org B has an identically-id'd LIVE file tab. Without org
	// scoping, org A's strand borrows org B's liveness and org A's worktree is
	// never reclaimed.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	q := svc.Queries
	ctx := context.Background()

	// Org A: worktree whose only link is a FILE strand -- no backing
	// worker_file_tabs row.
	require.NoError(t, q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-orgA", WorktreePath: "/r/orgA", RepoRoot: "/r", BranchName: "b"}))
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-orgA", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "file-dup", OrgID: "org-A"}))

	// Org B: a LIVE file tab with the SAME tab id but a different org.
	require.NoError(t, q.UpsertWorkerFileTab(ctx, db.UpsertWorkerFileTabParams{OrgID: "org-B", TabID: "file-dup", WorkspaceID: "ws-1", FilePath: "/r/orgB/x"}))

	// Org A's strand must read as dead -- it must NOT match org B's live tab.
	gotA, err := q.CountLiveWorktreeRefs(ctx, "wt-orgA")
	require.NoError(t, err)
	assert.Equal(t, int64(0), gotA, "org A's FILE strand must not borrow org B's live file tab")

	// ...so org A's all-strand worktree is a reclaimable orphan candidate.
	candidates, err := q.ListOrphanCandidateWorktrees(ctx)
	require.NoError(t, err)
	gotIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		gotIDs = append(gotIDs, c.ID)
	}
	assert.Contains(t, gotIDs, "wt-orgA", "org A's all-strand worktree must be an orphan candidate")

	// Sanity: a worktree linked to its OWN org's live file tab still counts,
	// so the org-scoped leg is matching, not just failing closed.
	require.NoError(t, q.CreateWorktree(ctx, db.CreateWorktreeParams{ID: "wt-orgB", WorktreePath: "/r/orgB", RepoRoot: "/r", BranchName: "b"}))
	require.NoError(t, q.AddWorktreeTab(ctx, db.AddWorktreeTabParams{WorktreeID: "wt-orgB", TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabID: "file-dup", OrgID: "org-B"}))
	gotB, err := q.CountLiveWorktreeRefs(ctx, "wt-orgB")
	require.NoError(t, err)
	assert.Equal(t, int64(1), gotB, "org B's link to its own live file tab counts")
	candidates, err = q.ListOrphanCandidateWorktrees(ctx)
	require.NoError(t, err)
	gotIDs = gotIDs[:0]
	for _, c := range candidates {
		gotIDs = append(gotIDs, c.ID)
	}
	assert.NotContains(t, gotIDs, "wt-orgB", "a worktree with a live same-org file tab is not an orphan")
}
