package service

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// registerCleanupHandlers registers workspace cleanup inner RPC handlers.
func registerCleanupHandlers(d registrar, svc *Service) {
	registerOwnerGuarded(d, "CleanupWorkspace", leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE, dispatchTracked, handleCleanupWorkspace(svc))
}

// handleCleanupWorkspace stops the processes and closes the rows for a deleted workspace.
// It preserves Git directories and branches. The reconciler removes the remaining dead links.
// The Hub supplies its owned tab set from inside the workspace deletion transaction.
// This includes tabs that the rendered projection hides and tabs created before deletion commits.
// The tracked handler lets shutdown wait until teardown completes.
func handleCleanupWorkspace(svc *Service) func(_ context.Context, caller channel.Caller, r *leapmuxv1.CleanupWorkspaceRequest, sender channel.ResponseWriter) {
	return func(_ context.Context, caller channel.Caller, r *leapmuxv1.CleanupWorkspaceRequest, sender channel.ResponseWriter) {
		for _, tab := range r.GetTabs() {
			tabID := tab.GetTabId()
			if tabID == "" {
				continue
			}
			// UNSPECIFIED action and the keep-the-link policy are pinned by
			// closeTabForDeletedWorkspace, which also owns the per-type dispatch --
			// so this loop cannot pair a REMOVE with a convergence close, and a new
			// tab type is handled in one place rather than here as well.
			svc.closeTabForDeletedWorkspace(tab.GetTabType(), caller.UserID.String(), tabID)
		}

		sendProtoResponse(sender, &leapmuxv1.CleanupWorkspaceResponse{})
	}
}
