package service

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	store           store.Store
	workerMgr       *workermgr.Manager
	channelMgr      *channelmgr.Manager
	pending         *workermgr.PendingRequests
	authFreshness   AuthFreshnessChecker
	closeDispatcher *workerCloseDispatcher
}

func (s *ChannelService) enqueueChannelCloses(closed []channelmgr.ClosedChannel) {
	s.closeDispatcher.enqueueChannelCloses(closed)
}

// AuthFreshnessChecker rejects channel opens that authenticated from a cache
// generation older than the latest local revocation sweep.
type AuthFreshnessChecker interface {
	IsAuthContextCurrent(user *auth.UserInfo) bool
	// CurrentCredentialExpiry returns the latest known expiry for the request's
	// credential, so a channel is armed at the current (not stale connect-time)
	// deadline even when a concurrent session slide raced its registration. It
	// takes a context because a session cache-miss falls back to an authoritative
	// DB read of the session's current expiry.
	CurrentCredentialExpiry(ctx context.Context, user *auth.UserInfo) auth.CredentialDeadline
}

// NewChannelService creates a new ChannelService.
func NewChannelService(
	st store.Store,
	wMgr *workermgr.Manager,
	cMgr *channelmgr.Manager,
	pr *workermgr.PendingRequests,
	freshness AuthFreshnessChecker,
) *ChannelService {
	if isNilDependency(freshness) {
		panic("channel service requires an auth freshness checker")
	}
	s := &ChannelService{
		store:           st,
		workerMgr:       wMgr,
		channelMgr:      cMgr,
		pending:         pr,
		closeDispatcher: newWorkerCloseDispatcher(wMgr),
		authFreshness:   freshness,
	}
	return s
}

// GetWorkerHandshakeParams returns the persisted public key material and the
// live encryption mode a client needs to start a Noise_NK handshake.
func (s *ChannelService) GetWorkerHandshakeParams(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetWorkerHandshakeParamsRequest],
) (*connect.Response[leapmuxv1.GetWorkerHandshakeParamsResponse], error) {
	user, err := s.requireCurrentAuth(ctx)
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

// accessibleWorkspaceIDs resolves the workspace-id set announced to the target
// worker on channel open. Sessions and API tokens get every workspace the user
// can read across all orgs -- owner-or-grant, not org-scoped -- so a workspace
// shared with the user from another org is operable over their worker channel,
// matching this commit's cross-org read-ACL. A delegation bearer is re-verified
// against current grants and pinned to its single mint-scope workspace so a
// stolen token cannot pivot the channel beyond that scope.
func (s *ChannelService) accessibleWorkspaceIDs(ctx context.Context, user *auth.UserInfo) ([]string, error) {
	if user.Credential.IsDelegation() {
		// Re-verify the pin against current grants so revoked / transferred
		// workspaces are caught at channel open time.
		hasAccess, err := auth.WorkspaceCanRead(ctx, s.store, user.Credential.WorkspaceScopeID(), user.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("verify delegation scope: %w", err))
		}
		if !hasAccess {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("delegation token scope is not accessible"))
		}
		return []string{user.Credential.WorkspaceScopeID()}, nil
	}
	workspaces, err := s.store.Workspaces().ListAllAccessible(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list accessible workspaces: %w", err))
	}
	ids := make([]string, len(workspaces))
	for i, ws := range workspaces {
		ids[i] = ws.ID
	}
	return ids, nil
}

func (s *ChannelService) OpenChannel(
	ctx context.Context,
	req *connect.Request[leapmuxv1.OpenChannelRequest],
) (*connect.Response[leapmuxv1.OpenChannelResponse], error) {
	user, err := s.requireCurrentAuth(ctx)
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
	// The credential identity is recorded so per-token revoke paths
	// (CloseChannelsByBearer / CloseChannelsByUserRevocation) can find every
	// channel an `lmx_…` token authorized.
	s.channelMgr.RegisterWithAuthInfo(channelID, workerID, user.ID, channelAuthInfo(user), nil)
	openAttempted := false
	registrationCommitted := false
	defer func() {
		if !registrationCommitted {
			closed := s.channelMgr.CloseByID(channelID)
			if openAttempted {
				s.notifyWorkersClosed(closed)
			}
		}
	}()
	if err := s.validateCurrentAuth(user); err != nil {
		return nil, err
	}

	// Build the accessible-workspace list announced to the target worker.
	accessibleWSIDs, err := s.accessibleWorkspaceIDs(ctx, user)
	if err != nil {
		return nil, err
	}
	// Relay the handshake while holding the channel operation lock. Revocation
	// teardown waits for this attempt, guaranteeing its close cannot reach the
	// worker before a later open for the same channel.
	var resp *leapmuxv1.ConnectRequest
	_, channelLive, err := s.channelMgr.UseChannelIf(
		channelID,
		func(info channelmgr.ChannelInfo) bool {
			return userCanUseChannel(user, info.AuthInfo, info.UserID)
		},
		func(channelmgr.ChannelInfo) error {
			if err := s.validateCurrentAuth(user); err != nil {
				return err
			}
			openAttempted = true
			var sendErr error
			resp, sendErr = s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_ChannelOpen{
					ChannelOpen: &leapmuxv1.ChannelOpenRequest{
						ChannelId:              channelID,
						UserId:                 user.ID,
						HandshakePayload:       req.Msg.GetHandshakePayload(),
						AccessibleWorkspaceIds: accessibleWSIDs,
					},
				},
			})
			return sendErr
		},
	)
	if !channelLive {
		if currentErr := s.validateCurrentAuth(user); currentErr != nil {
			return nil, currentErr
		}
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("channel closed before open attempt"))
	}
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnauthenticated {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("worker handshake failed: %w", err))
	}

	openResp := resp.GetChannelOpenResp()
	if openResp == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected response from worker"))
	}

	if openResp.GetError() != "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("worker rejected channel: %s", openResp.GetError()))
	}
	if err := s.ensureRegisteredChannelStillAuthorized(user, channelID); err != nil {
		return nil, err
	}
	// Arm the channel at the credential's CURRENT deadline, not the value
	// captured when this request was validated. A concurrent session slide may
	// have extended the deadline after that capture but before the channel was
	// indexed (so RescheduleExpiryBySession could not re-time it); re-reading here
	// -- after registration, so any later slide is caught by the rescheduled flag
	// inside ScheduleExpiry -- keeps a still-valid channel from being torn down at
	// the stale connect-time deadline.
	expiresAt := s.authFreshness.CurrentCredentialExpiry(ctx, user)
	if !s.channelMgr.ScheduleExpiry(channelID, expiresAt, func(closed channelmgr.ClosedChannel) {
		s.notifyWorkersClosed([]channelmgr.ClosedChannel{closed})
	}) {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("channel closed before expiry scheduling completed"))
	}
	registrationCommitted = true

	return connect.NewResponse(&leapmuxv1.OpenChannelResponse{
		ChannelId:        channelID,
		HandshakePayload: openResp.GetHandshakePayload(),
	}), nil
}

func (s *ChannelService) requireCurrentAuth(ctx context.Context) (*auth.UserInfo, error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.validateCurrentAuth(user); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *ChannelService) validateCurrentAuth(user *auth.UserInfo) error {
	// IsAuthContextCurrent covers session, bearer, AND user-wide revocation --
	// not only the credential generation -- so it is called inline here rather
	// than behind a generation-specific name.
	if !s.authFreshness.IsAuthContextCurrent(user) {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication was revoked"))
	}
	if !user.CredentialCurrent(time.Now()) {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication expired"))
	}
	return nil
}

func (s *ChannelService) ensureRegisteredChannelStillAuthorized(
	user *auth.UserInfo,
	channelID string,
) error {
	if err := s.validateCurrentAuth(user); err != nil {
		return err
	}
	info, ok := s.channelMgr.GetChannelInfo(channelID)
	if ok && userCanUseChannel(user, info.AuthInfo, info.UserID) {
		return nil
	}
	if err := s.validateCurrentAuth(user); err != nil {
		return err
	}
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("channel closed before open completed"))
}

func (s *ChannelService) CloseChannel(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CloseChannelRequest],
) (*connect.Response[leapmuxv1.CloseChannelResponse], error) {
	user, err := s.requireCurrentAuth(ctx)
	if err != nil {
		return nil, err
	}

	channelID := req.Msg.GetChannelId()
	if channelID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("channel_id is required"))
	}

	// Verify the channel exists, belongs to this user, and is inside the
	// caller's bearer scope. Delegation bearers may close only channels
	// opened by the same delegation token.
	closed := s.channelMgr.CloseByIDIf(channelID, func(info channelmgr.ChannelInfo) bool {
		return userCanUseChannel(user, info.AuthInfo, info.UserID)
	})
	if len(closed) == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("channel not found"))
	}

	s.notifyWorkersClosed(closed)

	return connect.NewResponse(&leapmuxv1.CloseChannelResponse{}), nil
}

func (s *ChannelService) PrepareWorkspaceAccess(
	ctx context.Context,
	req *connect.Request[leapmuxv1.PrepareWorkspaceAccessRequest],
) (*connect.Response[leapmuxv1.PrepareWorkspaceAccessResponse], error) {
	user, err := s.requireCurrentAuth(ctx)
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
	if delegationWorkspaceMismatch(user, workspaceID) {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("delegation token cannot prepare access to a different workspace"))
	}

	// Workspace must exist AND the user must be owner-or-granted. Shares the
	// read handlers' load-and-authorize core (owner-or-grant, NotFound for a
	// missing id vs PermissionDenied for no access) so the two paths cannot
	// drift on what a non-owner without a grant sees. The delegation-scope guard
	// above is kept separate because prepare-access deliberately answers a
	// scoped-bearer mismatch with PermissionDenied, not the read loader's
	// NotFound.
	if _, err := loadReadableWorkspace(ctx, s.store, workspaceID, user); err != nil {
		return nil, err
	}

	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("worker is offline"))
	}
	channelIDs := s.channelMgr.AuthorizedChannelIDsForUserWorker(user.ID, workerID, channelWorkspaceUpdateAuthorized(user.Credential, workspaceID))

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
func (s *ChannelService) CloseChannelsByBearer(ref auth.BearerRef) int {
	return s.finishChannelClose(s.channelMgr.CloseByBearer(ref))
}

func (s *ChannelService) CloseChannelsBySession(sessionID string) int {
	return s.finishChannelClose(s.channelMgr.CloseBySession(sessionID))
}

func (s *ChannelService) CloseChannelsByUsersForWorkspace(workspaceID string, userIDs []string) int {
	if workspaceID == "" {
		return 0
	}
	return s.finishChannelClose(s.channelMgr.CloseByUsers(userIDs, channelClosedByWorkspaceRemoval(workspaceID)))
}

// CloseChannelsByUserRevocation force-closes channels owned by userID whose
// authentication basis predates a user-wide revocation event.
func (s *ChannelService) CloseChannelsByUserRevocation(userID string, userAuthGeneration int64) int {
	return s.finishChannelClose(s.channelMgr.CloseByUserRevocation(userID, userAuthGeneration))
}

// RestampSessionGeneration advances the generation stamped on a session's
// channels so a following user-wide revocation spares the surviving session
// (e.g. the acting session after its own password change).
func (s *ChannelService) RestampSessionGeneration(sessionID string, generation int64) {
	s.channelMgr.RestampSessionGeneration(sessionID, generation)
}

func (s *ChannelService) finishChannelClose(closed []channelmgr.ClosedChannel) int {
	s.notifyWorkersClosed(closed)
	return len(closed)
}

// notifyWorkersClosed queues ChannelClose for each torn-down channel. Local
// teardown never waits on a slow worker stream. Delivery is best effort: closes
// for offline workers are skipped (a reconnecting worker tears its own channels
// down on the dropped stream), while the pending-close queue itself is unbounded
// so a revocation burst is never dropped for capacity.
func (s *ChannelService) notifyWorkersClosed(closed []channelmgr.ClosedChannel) {
	s.closeDispatcher.enqueueChannelCloses(closed)
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
