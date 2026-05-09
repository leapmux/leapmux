package service

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
)

func normalizeWorkspaceShareUserIDs(userIDs []string, ownerUserID string) []string {
	seen := make(map[string]struct{}, len(userIDs))
	normalized := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		if userID == "" || userID == ownerUserID {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		normalized = append(normalized, userID)
	}
	return normalized
}

func (s *WorkspaceService) UpdateWorkspaceSharing(
	ctx context.Context,
	req *connect.Request[leapmuxv1.UpdateWorkspaceSharingRequest],
) (*connect.Response[leapmuxv1.UpdateWorkspaceSharingResponse], error) {
	if s.soloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("workspace sharing is not available in solo mode"))
	}
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	ws, err := s.loadWorkspaceForOwnerWrite(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID)
	if err != nil {
		return nil, err
	}

	// Single tx body for both branches: PRIVATE clears the shared set,
	// MEMBERS clears and then bulk-grants the normalized list. The
	// pre-mutation owner re-check from the prior split was redundant
	// (the outer loadWorkspaceForOwnerWrite is the contract gate;
	// cooperative ownership doesn't change inside a single request).
	var grantParams []store.GrantWorkspaceAccessParams
	switch req.Msg.GetShareMode() {
	case leapmuxv1.ShareMode_SHARE_MODE_PRIVATE:
		// no grants — Clear-only
	case leapmuxv1.ShareMode_SHARE_MODE_MEMBERS:
		normalizedUserIDs := normalizeWorkspaceShareUserIDs(req.Msg.GetUserIds(), ws.OwnerUserID)
		if err := s.validateWorkspaceShareUsers(ctx, s.store, normalizedUserIDs); err != nil {
			return nil, err
		}
		grantParams = make([]store.GrantWorkspaceAccessParams, len(normalizedUserIDs))
		for i, uid := range normalizedUserIDs {
			grantParams[i] = store.GrantWorkspaceAccessParams{
				WorkspaceID: req.Msg.GetWorkspaceId(),
				UserID:      uid,
			}
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid share mode"))
	}

	err = s.store.RunInTransaction(ctx, func(tx store.Store) error {
		if err := tx.WorkspaceAccess().Clear(ctx, req.Msg.GetWorkspaceId()); err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("clear workspace access: %w", err))
		}
		if len(grantParams) > 0 {
			if err := tx.WorkspaceAccess().BulkGrant(ctx, grantParams); err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("grant workspace access: %w", err))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&leapmuxv1.UpdateWorkspaceSharingResponse{}), nil
}

func (s *WorkspaceService) ListWorkspaceShares(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListWorkspaceSharesRequest],
) (*connect.Response[leapmuxv1.ListWorkspaceSharesResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.loadWorkspaceForOwnerWrite(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID); err != nil {
		return nil, err
	}
	accessEntries, err := s.store.WorkspaceAccess().ListByWorkspaceID(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list workspace shares: %w", err))
	}
	shareMode := leapmuxv1.ShareMode_SHARE_MODE_PRIVATE
	var members []*leapmuxv1.WorkspaceShareMember
	if len(accessEntries) > 0 {
		shareMode = leapmuxv1.ShareMode_SHARE_MODE_MEMBERS
		members = make([]*leapmuxv1.WorkspaceShareMember, len(accessEntries))
		for i, entry := range accessEntries {
			members[i] = &leapmuxv1.WorkspaceShareMember{
				UserId: entry.UserID,
			}
		}
	}
	return connect.NewResponse(&leapmuxv1.ListWorkspaceSharesResponse{
		ShareMode: shareMode,
		Members:   members,
	}), nil
}
