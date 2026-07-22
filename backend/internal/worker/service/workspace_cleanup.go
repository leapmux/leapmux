package service

import (
	"context"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// registerCleanupHandlers registers workspace cleanup inner RPC handlers.
func registerCleanupHandlers(d registrar, svc *Service) {
	registerWorkspaceGated(d, "CleanupWorkspace", handleCleanupWorkspace(svc))
}

// handleCleanupWorkspace cleans up all local resources (agents, terminals,
// worktrees) for a deleted workspace. This is called via E2EE channel by the
// frontend after the hub deletes the workspace. Workspace access is enforced
// by registerWorkspaceGated before this runs.
func handleCleanupWorkspace(svc *Service) func(_ context.Context, _ string, r *leapmuxv1.CleanupWorkspaceRequest, sender channel.ResponseWriter) {
	return func(_ context.Context, _ string, r *leapmuxv1.CleanupWorkspaceRequest, sender channel.ResponseWriter) {
		workspaceID := r.GetWorkspaceId()
		// Gate on the channel's accessible set already ran in
		// registerWorkspaceGated. The hub removes the workspace before
		// calling CleanupWorkspace, but the accessible set is add-only
		// per-channel — a user who previously owned the workspace (i.e.
		// was told about it at handshake or via AddAccessibleWorkspaceID)
		// can still clean up. Fabricated/foreign IDs are rejected upstream.

		// 1. Stop all active agents for this workspace.
		agentIDs, err := svc.Queries.ListOpenAgentIDsByWorkspaceID(bgCtx(), workspaceID)
		if err != nil {
			slog.Error("cleanup workspace: failed to list active agents",
				"workspace_id", workspaceID, "error", err)
		}
		for _, row := range agentIDs {
			svc.Agents.StopAgent(row)
			svc.Output.CleanupAgent(row)
			_ = svc.Queries.CloseAgent(bgCtx(), row)

			svc.unregisterTab(leapmuxv1.TabType_TAB_TYPE_AGENT, row)
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
			svc.Terminals.RemoveTerminal(ts.ID)
			svc.unregisterTab(leapmuxv1.TabType_TAB_TYPE_TERMINAL, ts.ID)
		}

		// 4. Soft-delete active terminals for the workspace.
		if err := svc.Queries.CloseOpenTerminalsByWorkspace(bgCtx(), workspaceID); err != nil {
			slog.Error("cleanup workspace: failed to close terminals",
				"workspace_id", workspaceID, "error", err)
		}

		sendProtoResponse(sender, &leapmuxv1.CleanupWorkspaceResponse{})
	}
}
