package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// WorkerManagementService implements the Hub-side service called by Frontend
// to manage worker registrations and workers.
type WorkerManagementService struct {
	store       store.Store
	workerMgr   *workermgr.Manager
	broadcaster *HubEventBroadcaster
	notifier    *notifier.Notifier
	soloMode    bool
}

// NewWorkerManagementService creates a new WorkerManagementService.
func NewWorkerManagementService(st store.Store, mgr *workermgr.Manager, b *HubEventBroadcaster, n *notifier.Notifier, soloMode bool) *WorkerManagementService {
	return &WorkerManagementService{store: st, workerMgr: mgr, broadcaster: b, notifier: n, soloMode: soloMode}
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

	// Look up the registration.
	reg, err := s.store.Registrations().GetByID(ctx, regID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
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

	if err := s.store.Workers().Create(ctx, store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       authToken,
		RegisteredBy:    user.ID,
		PublicKey:       ptrconv.OrEmpty(reg.PublicKey),
		MlkemPublicKey:  ptrconv.OrEmpty(reg.MlkemPublicKey),
		SlhdsaPublicKey: ptrconv.OrEmpty(reg.SlhdsaPublicKey),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create worker: %w", err))
	}

	// Update the registration to approved.
	if err := s.store.Registrations().Approve(ctx, store.ApproveRegistrationParams{
		ID:         regID,
		WorkerID:   ptrconv.Ptr(workerID),
		ApprovedBy: ptrconv.Ptr(user.ID),
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("approve registration: %w", err))
	}

	// Wake up any long-polling worker waiting on this registration.
	s.workerMgr.NotifyRegistrationChange(regID)

	s.broadcaster.NotifyWorkersChanged(user.ID)

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

	workers, err := s.store.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
		RegisteredBy: user.ID,
		Cursor:       cursor,
		Limit:        limit,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	protoWorkers := make([]*leapmuxv1.Worker, len(workers))
	for i := range workers {
		protoWorkers[i] = s.workerToProto(&workers[i])
	}

	hasMore := int64(len(workers)) == limit
	var nextCursor string
	if hasMore {
		nextCursor = workers[len(workers)-1].CreatedAt.UTC().Format(time.RFC3339Nano)
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
		Worker: s.workerToProto(worker),
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

	if err := s.notifier.SendDeregister(ctx, req.Msg.GetWorkerId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("send deregister: %w", err))
	}

	s.broadcaster.NotifyWorkersChanged(user.ID)

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

	reg, err := s.store.Registrations().GetByID(ctx, regID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
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

func (s *WorkerManagementService) workerToProto(b *store.Worker) *leapmuxv1.Worker {
	lastSeen := ""
	if b.LastSeenAt != nil {
		lastSeen = timefmt.Format(*b.LastSeenAt)
	}

	return &leapmuxv1.Worker{
		Id:           b.ID,
		Online:       s.workerMgr.IsOnline(b.ID),
		CreatedAt:    timefmt.Format(b.CreatedAt),
		LastSeenAt:   lastSeen,
		RegisteredBy: b.RegisteredBy,
	}
}
