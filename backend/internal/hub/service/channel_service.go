package service

import (
	"context"
	"database/sql"
	"fmt"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
)

// ChannelService implements the Hub-side relay for encrypted Frontend ↔ Worker channels.
type ChannelService struct {
	queries    *db.Queries
	workerMgr  *workermgr.Manager
	channelMgr *channelmgr.Manager
	pending    *workermgr.PendingRequests
}

// NewChannelService creates a new ChannelService.
func NewChannelService(
	q *db.Queries,
	wMgr *workermgr.Manager,
	cMgr *channelmgr.Manager,
	pr *workermgr.PendingRequests,
) *ChannelService {
	return &ChannelService{
		queries:    q,
		workerMgr:  wMgr,
		channelMgr: cMgr,
		pending:    pr,
	}
}

func (s *ChannelService) GetWorkerPublicKey(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetWorkerPublicKeyRequest],
) (*connect.Response[leapmuxv1.GetWorkerPublicKeyResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}

	// Verify user has access to this worker (owns it or has a grant).
	if _, err := s.verifyWorkerAccess(ctx, user, workerID); err != nil {
		return nil, err
	}

	keys, err := s.queries.GetWorkerPublicKey(ctx, workerID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if len(keys.PublicKey) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker has no public key"))
	}

	return connect.NewResponse(&leapmuxv1.GetWorkerPublicKeyResponse{
		PublicKey:       keys.PublicKey,
		MlkemPublicKey:  keys.MlkemPublicKey,
		SlhdsaPublicKey: keys.SlhdsaPublicKey,
	}), nil
}

func (s *ChannelService) GetWorkerEncryptionMode(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetWorkerEncryptionModeRequest],
) (*connect.Response[leapmuxv1.GetWorkerEncryptionModeResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}

	// Verify user has access to this worker.
	if _, err := s.verifyWorkerAccess(ctx, user, workerID); err != nil {
		return nil, err
	}

	// Read encryption mode from the live connection (not DB).
	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("worker is offline"))
	}

	encMode := conn.EncryptionMode
	if encMode == leapmuxv1.EncryptionMode_ENCRYPTION_MODE_UNSPECIFIED {
		encMode = leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM
	}

	return connect.NewResponse(&leapmuxv1.GetWorkerEncryptionModeResponse{
		EncryptionMode: encMode,
	}), nil
}

func (s *ChannelService) OpenChannel(
	ctx context.Context,
	req *connect.Request[leapmuxv1.OpenChannelRequest],
) (*connect.Response[leapmuxv1.OpenChannelResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}

	// Verify user has access to this worker.
	_, err = s.verifyWorkerAccess(ctx, user, workerID)
	if err != nil {
		return nil, err
	}

	// Check worker is online.
	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("worker is offline"))
	}

	channelID := id.Generate()

	// Register in channel manager (no cancel func yet — WebSocket will set it).
	s.channelMgr.Register(channelID, workerID, user.ID, nil)

	// Query accessible workspaces for this user in their personal org.
	workspaces, err := s.queries.ListAccessibleWorkspaces(ctx, db.ListAccessibleWorkspacesParams{
		UserID: user.ID,
		OrgID:  user.OrgID,
	})
	if err != nil {
		s.channelMgr.Unregister(channelID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list accessible workspaces: %w", err))
	}
	accessibleWSIDs := make([]string, len(workspaces))
	for i, ws := range workspaces {
		accessibleWSIDs[i] = ws.ID
	}

	// Relay handshake to worker via bidi stream and wait for response.
	resp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ChannelOpen{
			ChannelOpen: &leapmuxv1.ChannelOpenRequest{
				ChannelId:              channelID,
				UserId:                 user.ID,
				HandshakePayload:       req.Msg.GetHandshakePayload(),
				AccessibleWorkspaceIds: accessibleWSIDs,
			},
		},
	})
	if err != nil {
		// Cleanup on failure.
		s.channelMgr.Unregister(channelID)
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("worker handshake failed: %w", err))
	}

	openResp := resp.GetChannelOpenResp()
	if openResp == nil {
		s.channelMgr.Unregister(channelID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected response from worker"))
	}

	if openResp.GetError() != "" {
		s.channelMgr.Unregister(channelID)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("worker rejected channel: %s", openResp.GetError()))
	}

	return connect.NewResponse(&leapmuxv1.OpenChannelResponse{
		ChannelId:        channelID,
		HandshakePayload: openResp.GetHandshakePayload(),
	}), nil
}

func (s *ChannelService) CloseChannel(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CloseChannelRequest],
) (*connect.Response[leapmuxv1.CloseChannelResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	channelID := req.Msg.GetChannelId()
	if channelID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("channel_id is required"))
	}

	// Verify the channel exists and belongs to this user (in-memory).
	ownerID := s.channelMgr.GetUserID(channelID)
	if ownerID == "" {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("channel not found"))
	}
	if ownerID != user.ID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("channel not found"))
	}

	// Notify worker (best effort — worker may be offline).
	if workerID := s.channelMgr.GetWorkerID(channelID); workerID != "" {
		if conn := s.workerMgr.Get(workerID); conn != nil {
			_ = conn.Send(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_ChannelClose{
					ChannelClose: &leapmuxv1.ChannelCloseNotification{
						ChannelId: channelID,
					},
				},
			})
		}
	}

	// Cleanup.
	s.channelMgr.Unregister(channelID)

	return connect.NewResponse(&leapmuxv1.CloseChannelResponse{}), nil
}

func (s *ChannelService) PrepareWorkspaceAccess(
	ctx context.Context,
	req *connect.Request[leapmuxv1.PrepareWorkspaceAccessRequest],
) (*connect.Response[leapmuxv1.PrepareWorkspaceAccessResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}
	workspaceID := req.Msg.GetWorkspaceId()
	if workspaceID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workspace_id is required"))
	}

	// Verify the user has access to the workspace (owner or explicit grant).
	hasAccess, err := s.queries.HasWorkspaceAccess(ctx, db.HasWorkspaceAccessParams{
		WorkspaceID: workspaceID,
		UserID:      user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check workspace access: %w", err))
	}
	// Also check ownership.
	ws, err := s.queries.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if ws.OwnerUserID != user.ID && !hasAccess {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("no access to workspace"))
	}

	// Find all channels for this user on the specified worker and push the update.
	channelIDs := s.channelMgr.GetChannelIDsForUser(user.ID)
	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("worker is offline"))
	}

	for _, chID := range channelIDs {
		if s.channelMgr.GetWorkerID(chID) != workerID {
			continue
		}
		if err := conn.Send(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_ChannelAccessUpdate{
				ChannelAccessUpdate: &leapmuxv1.ChannelAccessUpdate{
					ChannelId:   chID,
					WorkspaceId: workspaceID,
				},
			},
		}); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to update worker: %w", err))
		}
	}

	return connect.NewResponse(&leapmuxv1.PrepareWorkspaceAccessResponse{}), nil
}

// verifyWorkerAccess checks that the user owns the worker or has been granted access.
// Returns the worker record for downstream use (e.g. querying accessible workspaces).
func (s *ChannelService) verifyWorkerAccess(ctx context.Context, user *auth.UserInfo, workerID string) (db.Worker, error) {
	worker, err := s.queries.GetWorkerByID(ctx, workerID)
	if err != nil {
		if err == sql.ErrNoRows {
			return db.Worker{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return db.Worker{}, connect.NewError(connect.CodeInternal, err)
	}

	// Owner always has access.
	if worker.RegisteredBy == user.ID {
		return worker, nil
	}

	// Check explicit access grant.
	hasAccess, err := s.queries.HasWorkerAccess(ctx, db.HasWorkerAccessParams{
		WorkerID: workerID,
		UserID:   user.ID,
	})
	if err != nil {
		return db.Worker{}, connect.NewError(connect.CodeInternal, err)
	}
	if !hasAccess {
		return db.Worker{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
	}

	return worker, nil
}
