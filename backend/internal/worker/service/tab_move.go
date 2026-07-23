package service

import (
	"context"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// registerTabMoveHandlers registers the MoveTabWorkspace inner RPC handler.
// gateInBody, probe-enforced: dual source+dest + TabType switch cannot use a
// single structural extractor.
func registerTabMoveHandlers(d registrar, svc *Service) {
	registerInBodyGated(d, "MoveTabWorkspace", handleMoveTabWorkspace(svc))
}

// handleMoveTabWorkspace updates an agent's or terminal's workspace_id in the
// worker DB. The frontend calls this when dragging a tab between workspaces.
func handleMoveTabWorkspace(svc *Service) channel.HandlerFunc {
	return func(_ context.Context, _ string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.MoveTabWorkspaceRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		newWsID := r.GetNewWorkspaceId()
		tabID := r.GetTabId()
		if newWsID == "" || tabID == "" {
			sendInvalidArgument(sender, "new_workspace_id and tab_id are required")
			return
		}

		// Verify the **source** tab's current workspace is accessible on this
		// channel. Without this check, a user could steal a tab from another
		// user's workspace by calling MoveTabWorkspace with tabID=<theirs>,
		// newWorkspaceId=<mine>. requireAccessibleAgentID / TerminalID also
		// return NOT_FOUND when the tab id is unknown; the id-only variants
		// suffice because only the authorization decision is needed here.
		switch r.GetTabType() {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			if !svc.requireAccessibleAgentID(sender, tabID) {
				return
			}
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			if !svc.requireAccessibleTerminalID(sender, tabID) {
				return
			}
		default:
			sendInvalidArgument(sender, "unsupported tab type for workspace move")
			return
		}

		// Verify the target workspace is accessible to this channel's user.
		if !svc.requireAccessibleWorkspace(sender, newWsID) {
			return
		}

		switch r.GetTabType() {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			if err := svc.Queries.UpdateAgentWorkspace(bgCtx(), db.UpdateAgentWorkspaceParams{
				WorkspaceID: newWsID,
				ID:          tabID,
			}); err != nil {
				slog.Error("MoveTabWorkspace: failed to update agent workspace",
					"agent_id", tabID, "new_workspace_id", newWsID, "error", err)
				sendInternalError(sender, "failed to update agent workspace")
				return
			}

		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			if err := svc.Queries.UpdateTerminalWorkspace(bgCtx(), db.UpdateTerminalWorkspaceParams{
				WorkspaceID: newWsID,
				ID:          tabID,
			}); err != nil {
				slog.Error("MoveTabWorkspace: failed to update terminal workspace",
					"terminal_id", tabID, "new_workspace_id", newWsID, "error", err)
				sendInternalError(sender, "failed to update terminal workspace")
				return
			}

		default:
			sendInvalidArgument(sender, "unsupported tab type for workspace move")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.MoveTabWorkspaceResponse{})
	}
}
