package service

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
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
	// Owner-only surface: keep the delegation-bearer rejection visible at the
	// handler alongside the sibling lifecycle mutations, so a delegation token
	// can never reach it even if the procedure is ever added to the interceptor
	// allowlist.
	if err := rejectDelegationBearer(user, "workspace sharing mutation"); err != nil {
		return nil, err
	}
	workspaceID := req.Msg.GetWorkspaceId()
	unlock, err := s.sharingLocks.lock(ctx, workspaceID)
	if err != nil {
		return nil, workspaceLockError(err)
	}
	defer unlock()
	ws, err := loadWorkspaceForOwnerWrite(ctx, s.store, workspaceID, user.ID)
	if err != nil {
		return nil, err
	}
	// Resolve the process-local manager before committing so a bootstrap
	// failure cannot produce a successful ACL mutation with stale subscribers
	// in this Hub. Registry intentionally has no cross-Hub coherence mechanism;
	// this reconciles only subscribers owned by this process.
	var mgr *crdt.Manager
	if s.registry != nil {
		mgr, err = s.registry.Get(ctx, ws.OrgID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get CRDT manager: %w", err))
		}
	}

	// Single tx body for both branches: PRIVATE clears the shared set,
	// MEMBERS clears and then bulk-grants the normalized list. The
	// pre-mutation owner re-check from the prior split was redundant
	// (the outer loadWorkspaceForOwnerWrite is the contract gate;
	// cooperative ownership doesn't change inside a single request).
	var grantParams []store.GrantWorkspaceAccessParams
	var desiredUserIDs []string
	switch req.Msg.GetShareMode() {
	case leapmuxv1.ShareMode_SHARE_MODE_PRIVATE:
		// no grants — Clear-only
	case leapmuxv1.ShareMode_SHARE_MODE_MEMBERS:
		desiredUserIDs = normalizeWorkspaceShareUserIDs(req.Msg.GetUserIds(), ws.OwnerUserID)
		grantParams = make([]store.GrantWorkspaceAccessParams, len(desiredUserIDs))
		for i, uid := range desiredUserIDs {
			grantParams[i] = store.GrantWorkspaceAccessParams{
				WorkspaceID: workspaceID,
				UserID:      uid,
			}
		}
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid share mode"))
	}

	// Validate the target users before entering the commit: this is a pure
	// existence precondition, not part of the atomic ACL-write-plus-filter-
	// transition. Running it first lets an invalid request fail fast before any
	// Phase-1 filter revoke, and shrinks the aclTransitionMu-serialized window
	// (which serializes concurrent ACL transitions of this org). It does NOT
	// hold m.projection -- CommitWorkspaceAccessTransition runs its DB commit
	// with no manager lock held -- so it never blocks CRDT broadcasts or
	// subscribes.
	if err := validateWorkspaceShareUsers(ctx, s.store, desiredUserIDs); err != nil {
		return nil, err
	}

	allowedUserIDs := make(map[string]struct{}, len(desiredUserIDs)+1)
	allowedUserIDs[ws.OwnerUserID] = struct{}{}
	for _, userID := range desiredUserIDs {
		allowedUserIDs[userID] = struct{}{}
	}
	var removedUserIDs []string
	commit := func() error {
		return s.store.RunInTransaction(ctx, func(tx store.Store) error {
			current, err := tx.WorkspaceAccess().ListByWorkspaceID(ctx, workspaceID)
			if err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("list current workspace access: %w", err))
			}
			desired := make(map[string]struct{}, len(grantParams))
			for _, grant := range grantParams {
				desired[grant.UserID] = struct{}{}
			}
			removedUserIDs = make([]string, 0, len(current))
			for _, access := range current {
				if _, retained := desired[access.UserID]; !retained {
					removedUserIDs = append(removedUserIDs, access.UserID)
				}
			}
			if err := tx.WorkspaceAccess().Clear(ctx, workspaceID); err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("clear workspace access: %w", err))
			}
			if len(grantParams) > 0 {
				if err := tx.WorkspaceAccess().BulkGrant(ctx, grantParams); err != nil {
					return connect.NewError(connect.CodeInternal, fmt.Errorf("grant workspace access: %w", err))
				}
			}
			return nil
		})
	}
	if mgr != nil {
		err = mgr.CommitWorkspaceAccessTransition(workspaceID, allowedUserIDs, commit)
	} else {
		err = commit()
	}
	if err != nil {
		return nil, err
	}
	s.channelCloser.CloseChannelsByUsersForWorkspace(workspaceID, removedUserIDs)
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
	// Owner-only surface: reject delegation bearers at the handler, uniformly
	// with the sharing mutation and the workspace lifecycle handlers.
	if err := rejectDelegationBearer(user, "list workspace shares"); err != nil {
		return nil, err
	}
	if _, err := loadWorkspaceForOwnerWrite(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID); err != nil {
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
