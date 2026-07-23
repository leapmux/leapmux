package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/util/userid"
)

const (
	// RegistrationKeyTTL is the lifetime granted to a freshly minted (or
	// extended) registration key.
	RegistrationKeyTTL = 5 * time.Minute
	// RegistrationKeyExtendBuffer is the leave-window before expiry within
	// which Extend is allowed. We refuse extensions when more than this
	// much time remains so a runaway frontend can't keep a key alive
	// forever.
	RegistrationKeyExtendBuffer = 2 * time.Minute
)

// WorkerManagementService implements the Hub-side service called by Frontend
// to manage worker registration keys and worker rows.
type WorkerManagementService struct {
	store       store.Store
	workerMgr   *workermgr.Manager
	broadcaster *HubEventBroadcaster
	notifier    *notifier.Notifier
	mail        mail.Sender
	renderer    mail.Renderer
	cfg         *config.Config
	// scopeCache is the delegation-scope memo SubmitOps resolves through;
	// DeregisterWorker evicts the deregistered worker synchronously so the
	// containment action is immediate rather than lagged by the cache TTL.
	scopeCache *auth.DelegationScopeCache
}

// NewWorkerManagementService creates a new WorkerManagementService.
//
// cfg is required so the service can reject EmailRegistrationInstructions
// when SMTP isn't configured (defense-in-depth — the frontend already
// hides the button). renderer carries the hub URL used in the
// registration email's footer. scopeCache may be nil (tests); a private
// cache is constructed then, so the field is never nil -- production passes
// the instance shared with CRDTService so the eviction reaches the cache
// SubmitOps resolves through.
func NewWorkerManagementService(st store.Store, mgr *workermgr.Manager, b *HubEventBroadcaster, n *notifier.Notifier, sender mail.Sender, renderer mail.Renderer, cfg *config.Config, scopeCache *auth.DelegationScopeCache) *WorkerManagementService {
	if scopeCache == nil {
		scopeCache = auth.NewDelegationScopeCache(st)
	}
	return &WorkerManagementService{store: st, workerMgr: mgr, broadcaster: b, notifier: n, mail: sender, renderer: renderer, cfg: cfg, scopeCache: scopeCache}
}

func (s *WorkerManagementService) CreateRegistrationKey(
	ctx context.Context,
	_ *connect.Request[leapmuxv1.CreateRegistrationKeyRequest],
) (*connect.Response[leapmuxv1.CreateRegistrationKeyResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	key := id.Generate()
	expiresAt := time.Now().UTC().Add(RegistrationKeyTTL)
	if err := s.store.RegistrationKeys().Create(ctx, store.CreateRegistrationKeyParams{
		ID:        key,
		CreatedBy: user.ID,
		ExpiresAt: expiresAt,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create registration key: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.CreateRegistrationKeyResponse{
		RegistrationKey: key,
		ExpiresAt:       timestamppb.New(expiresAt),
	}), nil
}

func (s *WorkerManagementService) ExtendRegistrationKey(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ExtendRegistrationKeyRequest],
) (*connect.Response[leapmuxv1.ExtendRegistrationKeyResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	keyID := req.Msg.GetRegistrationKey()
	if keyID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("registration_key is required"))
	}

	// SELECT gives us the row needed for the anti-spam buffer check and
	// surfaces cross-user access as NotFound (matching the oracle
	// convention used elsewhere). Ownership and liveness are enforced
	// atomically by the UPDATE.
	row, err := s.getOwnedKey(ctx, keyID, user.ID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	// Anti-spam: refuse extensions while plenty of time still remains.
	// The frontend dialog only attempts an extension within the last ~2
	// minutes anyway; this guard makes it safe even if a bug or hostile
	// caller calls Extend in a tight loop. A dead row trivially passes
	// this check (negative remaining < buffer) and is rejected by the
	// UPDATE's expires_at > now guard below.
	if row.ExpiresAt.Sub(now) >= RegistrationKeyExtendBuffer {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("extension not allowed yet"))
	}

	newExpiresAt := now.Add(RegistrationKeyTTL)
	rows, err := s.store.RegistrationKeys().Extend(ctx, store.ExtendRegistrationKeyParams{
		ID:        keyID,
		CreatedBy: user.ID,
		ExpiresAt: newExpiresAt,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("extend registration key: %w", err))
	}
	// 0 rows means the row was already dead (soft-deleted or expired)
	// when we ran the UPDATE — either it was already past its TTL, or a
	// concurrent Consume burned it between our SELECT and UPDATE. Either
	// way, the caller must mint a fresh key.
	if rows == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("registration key has expired"))
	}

	return connect.NewResponse(&leapmuxv1.ExtendRegistrationKeyResponse{
		ExpiresAt: timestamppb.New(newExpiresAt),
	}), nil
}

func (s *WorkerManagementService) DeleteRegistrationKey(
	ctx context.Context,
	req *connect.Request[leapmuxv1.DeleteRegistrationKeyRequest],
) (*connect.Response[leapmuxv1.DeleteRegistrationKeyResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	keyID := req.Msg.GetRegistrationKey()
	if keyID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("registration_key is required"))
	}

	// One conditional UPDATE: ownership lives in the WHERE clause, so a
	// missing key and someone-else's key both surface as 0 rows-affected
	// and map to NotFound (same oracle convention as GetOwned).
	// Idempotent on already-consumed rows — the dialog's onCleanup is
	// free to fire after a successful registration.
	rows, err := s.store.RegistrationKeys().SoftDelete(ctx, store.SoftDeleteRegistrationKeyParams{
		ID:        keyID,
		CreatedBy: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("soft delete registration key: %w", err))
	}
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("registration key not found"))
	}

	return connect.NewResponse(&leapmuxv1.DeleteRegistrationKeyResponse{}), nil
}

func (s *WorkerManagementService) EmailRegistrationInstructions(
	ctx context.Context,
	req *connect.Request[leapmuxv1.EmailRegistrationInstructionsRequest],
) (*connect.Response[leapmuxv1.EmailRegistrationInstructionsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	keyID := req.Msg.GetRegistrationKey()
	command := req.Msg.GetCommand()
	if keyID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("registration_key is required"))
	}
	if command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("command is required"))
	}

	// All recipient details ride on the cached UserInfo, so this whole
	// handler is one query (the getOwnedKey SELECT). Worst-case
	// staleness is sessionCacheTTL — see UserInfo's doc comment.
	if !user.EmailVerified || user.Email == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("verified email address required to email instructions"))
	}

	// Defense in depth: reject if SMTP isn't configured. The frontend
	// hides this button when GetSystemInfo reports email_enabled=false,
	// but a direct RPC client could still hit us. Returning an explicit
	// FailedPrecondition is friendlier than the disabledSender's
	// ErrEmailDisabled bubbling up as a generic Unavailable.
	if s.cfg == nil || s.cfg.SmtpHost == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("hub is not configured to send email"))
	}

	// Ownership + liveness checks: never email instructions for a dead
	// key — the recipient would just be confused.
	row, err := s.getOwnedKey(ctx, keyID, user.ID)
	if err != nil {
		return nil, err
	}
	if !time.Now().UTC().Before(row.ExpiresAt) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("registration key has expired"))
	}

	if err := s.mail.Send(ctx, s.renderer.RegistrationInstructions(user.Email, command)); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("send mail: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.EmailRegistrationInstructionsResponse{}), nil
}

// getOwnedKey thinly wraps RegistrationKeys().GetOwned with the
// service's standard error mapping. Ownership is enforced inside the
// SQL WHERE clause -- plus the store's zero-id refusal, since a blank
// created_by parameter would MATCH a blank column rather than fail to:
// "missing" and "not yours" both surface as NotFound, matching the
// convention used elsewhere (Workers().GetOwned) and avoiding an oracle
// that would let callers probe other users' key IDs.
func (s *WorkerManagementService) getOwnedKey(ctx context.Context, id string, userID userid.UserID) (*store.WorkerRegistrationKey, error) {
	row, err := s.store.RegistrationKeys().GetOwned(ctx, store.GetOwnedRegistrationKeyParams{ID: id, CreatedBy: userID})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("registration key not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return row, nil
}

func (s *WorkerManagementService) ListWorkers(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListWorkersRequest],
) (*connect.Response[leapmuxv1.ListWorkersResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	limit := int64(50)
	cursor := ""
	if req.Msg.GetPage() != nil {
		if req.Msg.GetPage().GetLimit() > 0 {
			limit = int64(req.Msg.GetPage().GetLimit())
		}
		if req.Msg.GetPage().GetCursor() != "" {
			cursor = req.Msg.GetPage().GetCursor()
		}
	}

	page, err := s.store.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
		RegisteredBy: user.ID,
		PageParams:   store.PageParams{Cursor: cursor, Limit: limit},
	})
	if err != nil {
		// A malformed or stale opaque cursor is bad client input, not a server
		// fault: the store's cursor decode wraps store.ErrInvalidCursor before
		// any query runs, and it must surface as 400 InvalidArgument rather
		// than the 500 Internal that genuine store failures map to.
		if errors.Is(err, store.ErrInvalidCursor) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoWorkers := make([]*leapmuxv1.Worker, len(page.Rows))
	for i := range page.Rows {
		protoWorkers[i] = s.workerToProto(&page.Rows[i], user.OrgID)
	}

	return connect.NewResponse(&leapmuxv1.ListWorkersResponse{
		Workers: protoWorkers,
		Page: &leapmuxv1.PageResponse{
			NextCursor: page.NextCursor,
			HasMore:    page.HasMore(),
		},
	}), nil
}

func (s *WorkerManagementService) GetWorker(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetWorkerRequest],
) (*connect.Response[leapmuxv1.GetWorkerResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	worker, err := s.store.Workers().GetOwned(ctx, store.GetOwnedWorkerParams{
		UserID:   user.ID,
		WorkerID: req.Msg.GetWorkerId(),
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.GetWorkerResponse{
		Worker: s.workerToProto(worker, user.OrgID),
	}), nil
}

func (s *WorkerManagementService) DeregisterWorker(
	ctx context.Context,
	req *connect.Request[leapmuxv1.DeregisterWorkerRequest],
) (*connect.Response[leapmuxv1.DeregisterWorkerResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	// Refuse the auto-registered local worker. The solo launcher would
	// just re-register it on next start, so honoring the deregister
	// would only produce a transient outage and a confusing reappearing
	// row. The frontend already hides the menu item; this is the
	// defense-in-depth check against a hand-crafted RPC call.
	worker, err := s.store.Workers().GetOwned(ctx, store.GetOwnedWorkerParams{
		UserID:   user.ID,
		WorkerID: req.Msg.GetWorkerId(),
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if worker.AutoRegistered {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the bundled local worker cannot be deregistered"))
	}

	rows, err := s.store.Workers().Deregister(ctx, store.DeregisterWorkerParams{
		ID:           req.Msg.GetWorkerId(),
		RegisteredBy: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
	}

	// Deregistration is the operator's containment action against a compromised
	// worker: evict its memoized delegation scope so outstanding tokens minted on
	// it lose their cross-worker reach on the next SubmitOps, not a cache TTL
	// later.
	s.scopeCache.EvictWorker(req.Msg.GetWorkerId())

	if err := s.notifier.SendDeregister(ctx, req.Msg.GetWorkerId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("send deregister: %w", err))
	}

	s.broadcaster.NotifyWorkersChanged(user.ID.String())

	return connect.NewResponse(&leapmuxv1.DeregisterWorkerResponse{}), nil
}

// workerToProto converts a store.Worker into the wire-side Worker
// message. orgID is the caller's org — workers are owned by a single
// user, that user has one org, and every Workers().Get* /
// ListByUserID call upstream is already filtered by the caller's
// user_id, so the caller's org is the worker's org. Passing it as a
// parameter (rather than joining users.org_id at the SQL layer)
// avoids a per-worker round-trip in ListWorkers.
func (s *WorkerManagementService) workerToProto(b *store.Worker, orgID string) *leapmuxv1.Worker {
	lastSeen := ""
	if b.LastSeenAt != nil {
		lastSeen = timefmt.Format(*b.LastSeenAt)
	}

	return &leapmuxv1.Worker{
		Id:             b.ID,
		OrgId:          orgID,
		Online:         s.workerMgr.OnlineForTrustedPath(b.ID),
		CreatedAt:      timefmt.Format(b.CreatedAt),
		LastSeenAt:     lastSeen,
		RegisteredBy:   b.RegisteredBy,
		AutoRegistered: b.AutoRegistered,
	}
}
