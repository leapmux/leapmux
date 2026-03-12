package service

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// registerTabMoveHandlers registers the MoveTabWorkspace inner RPC handler.
func registerTabMoveHandlers(d *channel.Dispatcher, svc *Context) {
	d.Register("MoveTabWorkspace", handleMoveTabWorkspace(svc))
}

// handleMoveTabWorkspace updates an agent's or terminal's workspace_id in the
// worker DB. The frontend calls this when dragging a tab between workspaces.
func handleMoveTabWorkspace(svc *Context) channel.HandlerFunc {
	return func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		// Verify the target workspace is accessible to this channel's user.
		chID := sender.ChannelID()
		if chID != "" {
			awsIDs := svc.Channels.AccessibleWorkspaceIDs(chID)
			if awsIDs != nil && !awsIDs[newWsID] {
				sendPermissionDenied(sender, "target workspace not accessible")
				return
			}
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
