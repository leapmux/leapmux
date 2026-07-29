package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/nilcheck"
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

// workerUnreachableMetaKey marks a CodeUnavailable as the Hub's own verdict about
// a specific WORKER, as opposed to the many transport-shaped Unavailables a client
// also sees (an edge 503, a proxy hiccup, the Hub restarting).
//
// The distinction is load-bearing on the client: an offline close skips the
// uncommitted-work dialog and commits a tombstone, so it must fire on a positive
// "this worker is offline" and not on "something between us broke". Both arrive as
// CodeUnavailable from the same RPC, so the client cannot tell them apart from the
// code alone -- hence an explicit header rather than a guess about which
// Unavailables carry response metadata. See isWorkerUnreachable in
// frontend/src/api/workerErrors.ts, which reads it.
const workerUnreachableMetaKey = "leapmux-worker-unreachable"

// errWorkerUnreachable builds the Hub's worker-offline verdict, tagged so a client
// may trust it as a statement about the worker.
func errWorkerUnreachable(err error) error {
	cErr := connect.NewError(connect.CodeUnavailable, err)
	cErr.Meta().Set(workerUnreachableMetaKey, "1")
	return cErr
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

// NewChannelService creates a new ChannelService. The Hub's resolved payload
// budget is read from channelMgr.MaxMessageSize() at open time.
func NewChannelService(
	st store.Store,
	wMgr *workermgr.Manager,
	cMgr *channelmgr.Manager,
	pr *workermgr.PendingRequests,
	freshness AuthFreshnessChecker,
) *ChannelService {
	if nilcheck.IsNilDependency(freshness) {
		panic("channel service requires an auth freshness checker")
	}
	return &ChannelService{
		store:           st,
		workerMgr:       wMgr,
		channelMgr:      cMgr,
		pending:         pr,
		closeDispatcher: newWorkerCloseDispatcher(wMgr),
		authFreshness:   freshness,
	}
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

	conn, err := s.requireOnlineWorker(ctx, user, workerID)
	if err != nil {
		return nil, err
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
	user, err := s.requireCurrentAuth(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}

	// Verify access (and delegation scope) and fetch the live connection in one
	// step, so the online check can never run ahead of the access check.
	conn, err := s.requireOnlineWorker(ctx, user, workerID)
	if err != nil {
		return nil, err
	}

	channelID := id.Generate()

	// Register in channel manager (no cancel func yet -- WebSocket will set it).
	// The credential identity is recorded so per-token revoke paths
	// (CloseChannelsByBearer / CloseChannelsByUserRevocation) can find every
	// channel an `lmx_…` token authorized.
	if !s.channelMgr.RegisterWithAuthInfo(channelID, workerID, user.ID.String(), channelAuthInfo(user), nil) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("channel id collision"))
	}
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

	// Relay the handshake while holding the channel operation lock. Revocation
	// teardown waits for this attempt, guaranteeing its close cannot reach the
	// worker before a later open for the same channel. Echo validation and
	// SetChannelMaxMessageSize also run under opMu so RelayWorkerMessage (which
	// acquires the same opMu) cannot track w2fe chunks at the hub-default
	// ceiling before the negotiated per-channel limit is installed.
	var openResp *leapmuxv1.ChannelOpenResponse
	var openReject error
	var effectiveMax int
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
			resp, sendErr := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_ChannelOpen{
					ChannelOpen: &leapmuxv1.ChannelOpenRequest{
						ChannelId:        channelID,
						UserId:           user.ID.String(),
						HandshakePayload: req.Msg.GetHandshakePayload(),
						MaxMessageSize:   uint64(s.channelMgr.MaxMessageSize()),
					},
				},
			})
			if sendErr != nil {
				return sendErr
			}
			openResp = resp.GetChannelOpenResp()
			if openResp == nil {
				openReject = connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected response from worker"))
				return nil
			}
			// Fail closed on structured reject: ErrorCode alone (empty Error) must
			// not fall through to the success path.
			if openResp.GetError() != "" || openResp.GetErrorCode() != leapmuxv1.ChannelOpenErrorCode_CHANNEL_OPEN_ERROR_CODE_UNSPECIFIED {
				msg := openResp.GetError()
				if msg == "" {
					msg = openResp.GetErrorCode().String()
				}
				code := channelwire.ConnectCodeFromChannelOpenError(openResp.GetErrorCode())
				openReject = connect.NewError(code, fmt.Errorf("worker rejected channel: %s", msg))
				return nil
			}
			var adoptErr error
			effectiveMax, adoptErr = channelwire.AdoptWireMaxMessageSize(openResp.GetMaxMessageSize())
			if adoptErr != nil {
				if openResp.GetMaxMessageSize() == 0 {
					openReject = connect.NewError(connect.CodeInternal, fmt.Errorf(
						"worker echoed no max_message_size (0); upgrade the worker or check Hub↔Worker version skew"))
				} else {
					openReject = connect.NewError(connect.CodeInternal, fmt.Errorf("worker echoed invalid max_message_size: %w", adoptErr))
				}
				return nil
			}
			hubMax := s.channelMgr.MaxMessageSize()
			if effectiveMax > hubMax {
				openReject = connect.NewError(connect.CodeInternal, fmt.Errorf(
					"worker echoed max_message_size %d above hub max %d", effectiveMax, hubMax))
				return nil
			}
			s.channelMgr.SetChannelMaxMessageSize(channelID, effectiveMax)
			return nil
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
		return nil, errWorkerUnreachable(fmt.Errorf("worker handshake failed: %w", err))
	}
	if openReject != nil {
		return nil, openReject
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
		// The channel closed between handshake success and expiry scheduling --
		// most likely a concurrent bearer/user revocation sweep that found the
		// channel in the reverse index after RegisterWithAuthInfo published it and
		// tore it down. Re-validate auth so a revoked credential earns
		// CodeUnauthenticated (the error it just earned) instead of a generic
		// CodeUnavailable that reads as a transient server fault a client would
		// retry. Mirrors the same re-check the !channelLive branch above does for
		// the pre-handshake close.
		if currentErr := s.validateCurrentAuth(user); currentErr != nil {
			return nil, currentErr
		}
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("channel closed before expiry scheduling completed"))
	}
	registrationCommitted = true

	return connect.NewResponse(&leapmuxv1.OpenChannelResponse{
		ChannelId:        channelID,
		HandshakePayload: openResp.GetHandshakePayload(),
		UserId:           user.ID.String(),
		MaxMessageSize:   uint64(effectiveMax),
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

// requireOnlineWorker verifies the caller may reach workerID -- ownership plus, for
// a delegation bearer, that the token's minter is entitled to reach it (via
// WorkerReachAuthorizer) -- and returns its live connection. Bundling the scope check
// with the registry read is what keeps a worker-directed entrypoint from reaching
// ConnForTrustedPath with a user-supplied id: ConnForTrustedPath is an unfiltered map read, so
// reaching it with an arbitrary worker id would turn the offline/online split into
// a cross-tenant liveness oracle for any caller holding one readable workspace.
// Every legitimate user-gated caller already satisfies the check, so the gate is a
// property of this one primitive rather than a line each entrypoint must remember.
//
// The remaining structure that keeps ConnForTrustedPath from growing unscoped callers:
//
//   - The method name itself (ConnForTrustedPath) signals "no auth here"; rg ConnForTrustedPath
//     is the audit trail.
//   - audit.workerReachSites classifies every call site IN THE REPOSITORY
//     (reachEstablishedChan / reachServerInitiated / reachStoreScoped);
//     TestRepoInvariants fails any new call that is not listed, wherever it is
//     written. A package that takes *workermgr.Manager wholesale and reads a
//     trusted-path accessor does not compile-and-pass: it fails the net until
//     someone adds a classified entry, which is a reviewed decision rather than
//     an omission.
//   - Downstream consumers (notifier, channel-close dispatcher) hold narrow
//     interfaces that expose ConnForTrustedPath but not the rest of *workermgr.Manager,
//     so they cannot grow into Register / OnlineForTrustedPath / WaitFor* casually.
//
// What that does NOT cover, stated plainly so the next reader does not
// over-trust it: ConnForTrustedPath and the liveness probes stay exported on
// *workermgr.Manager, so the gate is "you must justify this in a table", not
// "you cannot write this". Making them unreachable is not simply a matter of an
// internal/ package -- Go's internal/ visibility is path-based, and every
// trusted consumer (notifier, channel-close dispatcher, ws_channel_relay,
// worker_mgmt_service) is a SIBLING of workermgr rather than a descendant, so
// an internal registry package would cut off the legitimate callers along with
// the illegitimate ones. The classification table is the enforcement, not a
// placeholder for a structural change that would work.
func (s *ChannelService) requireOnlineWorker(ctx context.Context, user *auth.UserInfo, workerID string) (*workermgr.Conn, error) {
	// ConnForUser runs the authorizer the registry was CONSTRUCTED with before
	// it touches the map, so the access check cannot be skipped by reaching the
	// registry directly -- that is the whole point of routing through it rather
	// than checking here and then reading.
	conn, err := s.workerMgr.ConnForUser(ctx, user, workerID)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, errWorkerUnreachable(errors.New("worker is offline"))
	}
	return conn, nil
}
