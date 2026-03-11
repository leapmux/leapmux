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

	// handshake_payload may be empty for disabled encryption mode (solo/loopback).

	// Verify user has access to this worker and get the worker's org.
	worker, err := s.verifyWorkerAccess(ctx, user, workerID)
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

	// Query accessible workspaces for this user in the worker's org.
	workspaces, err := s.queries.ListAccessibleWorkspaces(ctx, db.ListAccessibleWorkspacesParams{
		UserID: user.ID,
		OrgID:  worker.OrgID,
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

// verifyWorkerAccess checks that the user is a member of the worker's org.
// All org members may open E2EE channels to any worker in their org;
// fine-grained workspace access is enforced by the Worker itself.
// Returns the worker record for downstream use (e.g. querying accessible workspaces).
func (s *ChannelService) verifyWorkerAccess(ctx context.Context, user *auth.UserInfo, workerID string) (db.Worker, error) {
	// Look up the worker without org restriction.
	worker, err := s.queries.GetWorkerByIDInternal(ctx, workerID)
	if err != nil {
		if err == sql.ErrNoRows {
			return db.Worker{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return db.Worker{}, connect.NewError(connect.CodeInternal, err)
	}

	// Check if the user is a member of the worker's org.
	isMember, err := s.queries.IsOrgMember(ctx, db.IsOrgMemberParams{
		OrgID:  worker.OrgID,
		UserID: user.ID,
	})
	if err != nil {
		return db.Worker{}, connect.NewError(connect.CodeInternal, err)
	}
	if !isMember {
		return db.Worker{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
	}

	return worker, nil
}
