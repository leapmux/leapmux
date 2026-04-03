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
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// WorkerManagementService implements the Hub-side service called by Frontend
// to manage worker registrations and workers.
type WorkerManagementService struct {
	queries    *db.Queries
	workerMgr  *workermgr.Manager
	channelMgr *channelmgr.Manager
	notifier   *notifier.Notifier
	soloMode   bool
}

// NewWorkerManagementService creates a new WorkerManagementService.
func NewWorkerManagementService(q *db.Queries, mgr *workermgr.Manager, cMgr *channelmgr.Manager, n *notifier.Notifier, soloMode bool) *WorkerManagementService {
	return &WorkerManagementService{queries: q, workerMgr: mgr, channelMgr: cMgr, notifier: n, soloMode: soloMode}
}

func (s *WorkerManagementService) ApproveRegistration(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ApproveRegistrationRequest],
) (*connect.Response[leapmuxv1.ApproveRegistrationResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	regID := req.Msg.GetRegistrationToken()
	if regID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("registration_token is required"))
	}

	// Resolve org ID - user picks which org to register the worker in.
	orgID, err := auth.ResolveOrgID(ctx, s.queries, user, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	// Look up the registration.
	reg, err := s.queries.GetRegistrationByID(ctx, regID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("registration not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	if reg.Status != leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("registration is %s, not pending", reg.Status))
	}

	// Create the worker record.
	workerID := id.Generate()
	authToken := id.Generate()

	// Copy public keys from registration to worker (may be empty if worker didn't send them).
	publicKey := reg.PublicKey
	if publicKey == nil {
		publicKey = []byte{}
	}
	mlkemPK := reg.MlkemPublicKey
	if mlkemPK == nil {
		mlkemPK = []byte{}
	}
	slhdsaPK := reg.SlhdsaPublicKey
	if slhdsaPK == nil {
		slhdsaPK = []byte{}
	}

	if err := s.queries.CreateWorker(ctx, db.CreateWorkerParams{
		ID:              workerID,
		OrgID:           orgID,
		AuthToken:       authToken,
		RegisteredBy:    user.ID,
		PublicKey:       publicKey,
		MlkemPublicKey:  mlkemPK,
		SlhdsaPublicKey: slhdsaPK,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create worker: %w", err))
	}

	// Update the registration to approved.
	if err := s.queries.ApproveRegistration(ctx, db.ApproveRegistrationParams{
		ID:         regID,
		WorkerID:   sql.NullString{String: workerID, Valid: true},
		ApprovedBy: sql.NullString{String: user.ID, Valid: true},
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("approve registration: %w", err))
	}

	// Wake up any long-polling worker waiting on this registration.
	s.workerMgr.NotifyRegistrationChange(regID)

	notifyWorkersChanged(s.channelMgr, user.ID)

	return connect.NewResponse(&leapmuxv1.ApproveRegistrationResponse{
		WorkerId: workerID,
	}), nil
}

func (s *WorkerManagementService) ListWorkers(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListWorkersRequest],
) (*connect.Response[leapmuxv1.ListWorkersResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgID, err := auth.ResolveOrgID(ctx, s.queries, user, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	limit := int64(50)
	offset := int64(0)
	if req.Msg.GetPage() != nil {
		if req.Msg.GetPage().GetLimit() > 0 {
			limit = int64(req.Msg.GetPage().GetLimit())
		}
		if req.Msg.GetPage().GetCursor() != "" {
			_, _ = fmt.Sscanf(req.Msg.GetPage().GetCursor(), "%d", &offset)
		}
	}

	workers, err := s.queries.ListWorkersByOrgID(ctx, db.ListWorkersByOrgIDParams{
		OrgID:  orgID,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoWorkers := make([]*leapmuxv1.Worker, len(workers))
	for i, b := range workers {
		protoWorkers[i] = s.workerToProto(&b)
	}

	hasMore := int64(len(workers)) == limit
	var nextCursor string
	if hasMore {
		nextCursor = fmt.Sprintf("%d", offset+limit)
	}

	return connect.NewResponse(&leapmuxv1.ListWorkersResponse{
		Workers: protoWorkers,
		Page: &leapmuxv1.PageResponse{
			NextCursor: nextCursor,
			HasMore:    hasMore,
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

	worker, err := s.queries.GetWorkerByIDInternal(ctx, req.Msg.GetWorkerId())
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Verify the user owns this worker.
	_, err = s.queries.GetOwnedWorker(ctx, db.GetOwnedWorkerParams{
		UserID:   user.ID,
		WorkerID: worker.ID,
		OrgID:    worker.OrgID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.GetWorkerResponse{
		Worker: s.workerToProto(&worker),
	}), nil
}

func (s *WorkerManagementService) DeregisterWorker(
	ctx context.Context,
	req *connect.Request[leapmuxv1.DeregisterWorkerRequest],
) (*connect.Response[leapmuxv1.DeregisterWorkerResponse], error) {
	if s.soloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker deregistration is not available in solo mode"))
	}
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	orgID, err := auth.ResolveOrgID(ctx, s.queries, user, "")
	if err != nil {
		return nil, err
	}

	result, err := s.queries.DeregisterWorker(ctx, db.DeregisterWorkerParams{
		ID:           req.Msg.GetWorkerId(),
		OrgID:        orgID,
		RegisteredBy: user.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
	}

	if err := s.notifier.SendDeregister(ctx, req.Msg.GetWorkerId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("send deregister: %w", err))
	}

	notifyWorkersChanged(s.channelMgr, user.ID)

	return connect.NewResponse(&leapmuxv1.DeregisterWorkerResponse{}), nil
}

func (s *WorkerManagementService) GetRegistration(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetRegistrationRequest],
) (*connect.Response[leapmuxv1.GetRegistrationResponse], error) {
	_, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	regID := req.Msg.GetRegistrationToken()
	if regID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("registration_token is required"))
	}

	reg, err := s.queries.GetRegistrationByID(ctx, regID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("registration not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	status := reg.Status

	var fingerprint string
	if len(reg.PublicKey) > 0 {
		fingerprint = noiseutil.CompositeKeyFingerprint(reg.PublicKey, reg.MlkemPublicKey, reg.SlhdsaPublicKey)
	}

	return connect.NewResponse(&leapmuxv1.GetRegistrationResponse{
		RegistrationToken:    reg.ID,
		Version:              reg.Version,
		Status:               status,
		PublicKeyFingerprint: fingerprint,
	}), nil
}

func (s *WorkerManagementService) workerToProto(b *db.Worker) *leapmuxv1.Worker {
	lastSeen := ""
	if b.LastSeenAt.Valid {
		lastSeen = timefmt.Format(b.LastSeenAt.Time)
	}

	return &leapmuxv1.Worker{
		Id:           b.ID,
		OrgId:        b.OrgID,
		Online:       s.workerMgr.IsOnline(b.ID),
		CreatedAt:    timefmt.Format(b.CreatedAt),
		LastSeenAt:   lastSeen,
		RegisteredBy: b.RegisteredBy,
	}
}
