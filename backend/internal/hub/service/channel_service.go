package service

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
)

// ChannelService implements the Hub-side relay for encrypted Frontend <-> Worker channels.
type ChannelService struct {
	store      store.Store
	workerMgr  *workermgr.Manager
	channelMgr *channelmgr.Manager
	pending    *workermgr.PendingRequests
}

// NewChannelService creates a new ChannelService.
func NewChannelService(
	st store.Store,
	wMgr *workermgr.Manager,
	cMgr *channelmgr.Manager,
	pr *workermgr.PendingRequests,
) *ChannelService {
	return &ChannelService{
		store:      st,
		workerMgr:  wMgr,
		channelMgr: cMgr,
		pending:    pr,
	}
}

// GetWorkerHandshakeParams returns the persisted public key material and the
// live encryption mode a client needs to start a Noise_NK handshake.
func (s *ChannelService) GetWorkerHandshakeParams(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetWorkerHandshakeParamsRequest],
) (*connect.Response[leapmuxv1.GetWorkerHandshakeParamsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}

	if _, err := s.verifyWorkerAccess(ctx, user, workerID); err != nil {
		return nil, err
	}

	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("worker is offline"))
	}

	keys, err := s.store.Workers().GetPublicKey(ctx, workerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if len(keys.PublicKey) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker has no public key"))
	}

	encMode := conn.EncryptionMode
	if encMode == leapmuxv1.EncryptionMode_ENCRYPTION_MODE_UNSPECIFIED {
		encMode = leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM
	}

	return connect.NewResponse(&leapmuxv1.GetWorkerHandshakeParamsResponse{
		PublicKey:       keys.PublicKey,
		MlkemPublicKey:  keys.MlkemPublicKey,
		SlhdsaPublicKey: keys.SlhdsaPublicKey,
		EncryptionMode:  encMode,
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

	// Register in channel manager (no cancel func yet -- WebSocket will set it).
	// BearerTokenID is recorded so per-token revoke paths
	// (CloseChannelsByBearer / CloseChannelsByUser) can find every
	// channel an `lmx_…` token authorized.
	s.channelMgr.RegisterWithBearer(channelID, workerID, user.ID, user.BearerTokenID, nil)

	// Build the accessible-workspace list announced to the target
	// worker. Sessions/api tokens get the user's full grant set;
	// delegation tokens get pinned to a single workspace so a stolen
	// bearer cannot pivot the channel beyond its mint scope.
	var accessibleWSIDs []string
	if user.DelegationWorkspaceID != "" {
		// Re-verify the pin against current grants so revoked /
		// transferred workspaces are caught at channel open time.
		hasAccess, accErr := auth.WorkspaceCanRead(ctx, s.store, user.DelegationWorkspaceID, user.ID)
		if accErr != nil {
			s.channelMgr.Unregister(channelID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("verify delegation scope: %w", accErr))
		}
		if !hasAccess {
			s.channelMgr.Unregister(channelID)
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("delegation token scope is not accessible"))
		}
		accessibleWSIDs = []string{user.DelegationWorkspaceID}
	} else {
		workspaces, err := s.store.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: user.ID,
			OrgID:  user.OrgID,
		})
		if err != nil {
			s.channelMgr.Unregister(channelID)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list accessible workspaces: %w", err))
		}
		accessibleWSIDs = make([]string, len(workspaces))
		for i, ws := range workspaces {
			accessibleWSIDs[i] = ws.ID
		}
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

	// Notify worker (best effort -- worker may be offline).
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

	// A delegation-token caller is pinned to a single workspace by
	// design; refusing widening here keeps PrepareWorkspaceAccess from
	// becoming a back door around the OpenChannel narrowing.
	if user.DelegationWorkspaceID != "" && user.DelegationWorkspaceID != workspaceID {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("delegation token cannot prepare access to a different workspace"))
	}

	// Workspace must exist AND the user must be owner-or-granted. The
	// existence check separately surfaces a distinct NotFound error
	// (so callers can tell "wrong id" from "permission denied") before
	// deferring to the canonical owner-or-grant predicate.
	ws, err := s.store.Workspaces().GetByID(ctx, workspaceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("workspace not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if ws.OwnerUserID != user.ID {
		hasAccess, err := auth.WorkspaceCanRead(ctx, s.store, workspaceID, user.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check workspace access: %w", err))
		}
		if !hasAccess {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("no access to workspace"))
		}
	}

	// Find all channels for this user on the specified worker and push the update.
	channelIDs := s.channelMgr.GetChannelIDsForUser(user.ID)
	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("worker is offline"))
	}

	// Send a ChannelAccessUpdate to each matching channel and wait for
	// the worker to ack before returning. Without the ack the caller
	// races the worker's AddAccessibleWorkspaceID handler — the next
	// inner RPC on the channel (e.g. OpenAgent / ListAgents) can arrive
	// first and fail the worker-side requireAccessibleWorkspace check.
	//
	// Fan out across channels with a bounded errgroup so a user with
	// many open agents on the same worker doesn't pay N×RTT serial
	// latency. The worker already serializes writes on the underlying
	// conn; pipelining only the waits is the win.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for _, chID := range channelIDs {
		if s.channelMgr.GetWorkerID(chID) != workerID {
			continue
		}
		chID := chID
		g.Go(func() error {
			resp, err := s.pending.SendAndWait(gctx, conn, &leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_ChannelAccessUpdate{
					ChannelAccessUpdate: &leapmuxv1.ChannelAccessUpdate{
						ChannelId:   chID,
						WorkspaceId: workspaceID,
					},
				},
			})
			if err != nil {
				return connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to update worker: %w", err))
			}
			if resp.GetChannelAccessUpdateAck() == nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected response from worker for channel access update"))
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	return connect.NewResponse(&leapmuxv1.PrepareWorkspaceAccessResponse{}), nil
}

// CloseChannelsByBearer force-closes every open channel that was
// authenticated by the given bearer token id. Used by per-token
// revocation paths (`/worker/delegation-tokens/revoke`,
// `admin api-token revoke`) so an open Noise_NK session does not
// outlive the bearer that authorized it.
//
// Returns the number of channels torn down. The hub frontends
// receive a CHANNEL_CLOSE notification (handled inside channelmgr)
// and the workers receive a `ChannelClose` payload over their
// existing bidi stream — same code path as a user-initiated
// `CloseChannel`.
func (s *ChannelService) CloseChannelsByBearer(tokenID string) int {
	closed := s.channelMgr.CloseByBearer(tokenID)
	s.notifyWorkersClosed(closed)
	return len(closed)
}

// CloseChannelsByUser force-closes every channel owned by userID.
// Used by user-revocation paths (password change, account deletion,
// admin force-logout-all) so spawned-agent channels die alongside
// the row-level revocation rather than lingering until the row's
// TTL expires.
func (s *ChannelService) CloseChannelsByUser(userID string) int {
	closed := s.channelMgr.CloseByUser(userID)
	s.notifyWorkersClosed(closed)
	return len(closed)
}

// notifyWorkersClosed sends ChannelClose to each worker for every
// torn-down channel. Best effort: workers that disappear between the
// channelmgr close and this notify will see the close on next
// reconnect; queued channels for an offline worker are silently
// skipped.
func (s *ChannelService) notifyWorkersClosed(closed []channelmgr.ClosedChannel) {
	for _, cc := range closed {
		conn := s.workerMgr.Get(cc.WorkerID)
		if conn == nil {
			continue
		}
		_ = conn.Send(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_ChannelClose{
				ChannelClose: &leapmuxv1.ChannelCloseNotification{
					ChannelId: cc.ChannelID,
				},
			},
		})
	}
}

// verifyWorkerAccess checks that the user owns the worker or has been granted access.
// Returns the worker record for downstream use (e.g. querying accessible workspaces).
func (s *ChannelService) verifyWorkerAccess(ctx context.Context, user *auth.UserInfo, workerID string) (*store.Worker, error) {
	worker, ok, err := auth.WorkerCanUse(ctx, s.store, workerID, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if worker == nil || !ok || worker.Status != leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
	}

	return worker, nil
}
