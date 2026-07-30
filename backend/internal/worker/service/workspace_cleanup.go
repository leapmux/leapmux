package service

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// registerCleanupHandlers registers workspace cleanup inner RPC handlers.
func registerCleanupHandlers(d registrar, svc *Service) {
	registerOwnerGated(d, "CleanupWorkspace", dispatchTracked, handleCleanupWorkspace(svc))
}

// handleCleanupWorkspace tears down the local resources behind the tabs a
// just-deleted workspace held: agent subprocesses, terminal PTYs and the DB
// rows.
//
// It does NOT remove worktrees, and it deliberately leaves each tab's
// worktree_tabs link in place rather than unregistering it. Dropping the link
// is what a single-tab KEEP close does, and it is right there -- the user asked
// to keep the directory. Here the workspace is gone, so no intent survives for
// the directory to serve, and a zero-link worktree is excluded from
// ListOrphanCandidateWorktrees forever: dropping the link would strand the
// directory PERMANENTLY rather than merely reclaiming it late. Left as a strand,
// the orphan reconciler reclaims it (and refuses to, if it holds uncommitted or
// unpushed work -- see Service.worktreeHoldsUnsavedWork).
//
// The workspace itself is not named. This worker tracks no workspace id, so the
// tab set arrives in the request -- taken from DeleteWorkspaceResponse.worker_tabs,
// which the Hub reads from workspace_tab_owned INSIDE the delete transaction.
//
// Callers used to resolve it themselves from workspace_tab_rendered, which is a
// strict SUBSET of the owned projection: a projection-hidden tab was never named
// at all, a tab a peer opened mid-delete was missed, and a failed read degraded to
// "close nothing". The reconciler still converges anything left over against the
// Hub's owned view, so a dropped RPC costs latency rather than correctness -- but
// it is no longer what covers for a structurally incomplete list.
//
// Registered as TRACKED so a concurrent Shutdown drains the close flow (stop →
// DB close → unregister → optional worktree remove) before tearing down the DB
// pool, matching CloseAgent / CloseTerminal, which do the same work one tab at
// a time.
func handleCleanupWorkspace(svc *Service) func(_ context.Context, userID userid.UserID, r *leapmuxv1.CleanupWorkspaceRequest, sender channel.ResponseWriter) {
	return func(_ context.Context, userID userid.UserID, r *leapmuxv1.CleanupWorkspaceRequest, sender channel.ResponseWriter) {
		for _, tab := range r.GetTabs() {
			tabID := tab.GetTabId()
			if tabID == "" {
				continue
			}
			// UNSPECIFIED action and the keep-the-link policy are pinned by
			// closeTabForDeletedWorkspace, which also owns the per-type dispatch --
			// so this loop cannot pair a REMOVE with a convergence close, and a new
			// tab type is handled in one place rather than here as well.
			svc.closeTabForDeletedWorkspace(tab.GetTabType(), userID.String(), tabID)
		}

		sendProtoResponse(sender, &leapmuxv1.CleanupWorkspaceResponse{})
	}
}
