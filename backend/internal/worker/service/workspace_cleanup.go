package service

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// registerCleanupHandlers registers workspace cleanup inner RPC handlers.
func registerCleanupHandlers(d *channel.Dispatcher, svc *Context) {
	d.Register("CleanupWorkspace", handleCleanupWorkspace(svc))
}

// handleCleanupWorkspace cleans up all local resources (agents, terminals,
// worktrees) for a deleted workspace. This is called via E2EE channel by the
// frontend after the hub deletes the workspace.
func handleCleanupWorkspace(svc *Context) channel.HandlerFunc {
	return func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.CleanupWorkspaceRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		workspaceID := r.GetWorkspaceId()
		if workspaceID == "" {
			sendInvalidArgument(sender, "workspace_id is required")
			return
		}

		// 1. Stop all active agents for this workspace.
		agentIDs, err := svc.Queries.ListOpenAgentIDsByWorkspaceID(bgCtx(), workspaceID)
		if err != nil {
			slog.Error("cleanup workspace: failed to list active agents",
				"workspace_id", workspaceID, "error", err)
		}
		for _, row := range agentIDs {
			svc.Agents.StopAgent(row)
			_ = svc.Queries.CloseAgent(bgCtx(), row)

			svc.unregisterTabAndCleanup(leapmuxv1.TabType_TAB_TYPE_AGENT, row, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED)
		}

		// 2. Close all agents (including already-closed ones) for consistency.
		if err := svc.Queries.CloseOpenAgentsByWorkspace(bgCtx(), workspaceID); err != nil {
			slog.Error("cleanup workspace: failed to close agents",
				"workspace_id", workspaceID, "error", err)
		}

		// 3. Remove terminals for this workspace.
		terminals, err := svc.Queries.ListTerminalsByWorkspace(bgCtx(), workspaceID)
		if err != nil {
			slog.Error("cleanup workspace: failed to list terminals",
				"workspace_id", workspaceID, "error", err)
		}
		for _, ts := range terminals {
			if svc.Terminals.HasTerminal(ts.ID) {
				svc.Terminals.RemoveTerminal(ts.ID)
			}

			svc.unregisterTabAndCleanup(leapmuxv1.TabType_TAB_TYPE_TERMINAL, ts.ID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED)
		}

		// 4. Soft-delete active terminals for the workspace.
		if err := svc.Queries.CloseOpenTerminalsByWorkspace(bgCtx(), workspaceID); err != nil {
			slog.Error("cleanup workspace: failed to close terminals",
				"workspace_id", workspaceID, "error", err)
		}

		sendProtoResponse(sender, &leapmuxv1.CleanupWorkspaceResponse{})
	}
}
