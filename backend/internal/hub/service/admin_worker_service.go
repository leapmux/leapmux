package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/cleanup"
	"github.com/leapmux/leapmux/internal/hub/store"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminWorkerService implements the leapmux.v1.AdminWorkerService
// ConnectRPC handler: the cross-user worker administration surface
// (listing, inspection, force-deregister, registration keys). The
// per-user worker view lives in WorkerManagementService.
type AdminWorkerService struct {
	store store.Store
	// deregisterEffects runs the out-of-database half of a deregistration.
	// The same instance reaches WorkerManagementService, so an
	// administrator's force-deregister does exactly what the owner's does.
	deregisterEffects *WorkerDeregisterEffects
}

func NewAdminWorkerService(st store.Store, deregisterEffects *WorkerDeregisterEffects) *AdminWorkerService {
	return &AdminWorkerService{store: st, deregisterEffects: deregisterEffects}
}

func adminWorkerToProto(w store.WorkerWithOwner) *leapmuxv1.AdminWorker {
	out := &leapmuxv1.AdminWorker{
		Id:             w.ID,
		RegisteredBy:   w.RegisteredBy,
		OwnerUsername:  w.OwnerUsername,
		OwnerDeleted:   w.OwnerDeleted,
		Status:         w.Status,
		AutoRegistered: w.AutoRegistered,
		CreatedAt:      timestamppb.New(w.CreatedAt),
		LastSeenAt:     optTimestamp(w.LastSeenAt),
	}
	return out
}

func (s *AdminWorkerService) ListWorkers(ctx context.Context, req *connect.Request[leapmuxv1.AdminWorkerServiceListWorkersRequest]) (*connect.Response[leapmuxv1.AdminWorkerServiceListWorkersResponse], error) {
	userFilter, err := resolveAdminUserFilter(ctx, s.store, req.Msg.GetUserId(), req.Msg.GetUsername())
	if err != nil {
		return nil, err
	}
	params := store.ListWorkersAdminParams{
		UserID:     userFilter,
		PageParams: NormalizePageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
	}
	if st := req.Msg.GetStatus(); st != leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED {
		params.Status = &st
	}
	page, err := s.store.Workers().ListAdmin(ctx, params)
	if err != nil {
		return nil, storeConnectError(err, "list workers")
	}
	workers := make([]*leapmuxv1.AdminWorker, 0, len(page.Rows))
	for _, w := range page.Rows {
		workers = append(workers, adminWorkerToProto(w))
	}
	return connect.NewResponse(&leapmuxv1.AdminWorkerServiceListWorkersResponse{Workers: workers, NextCursor: page.NextCursor}), nil
}

func (s *AdminWorkerService) GetWorker(ctx context.Context, req *connect.Request[leapmuxv1.AdminWorkerServiceGetWorkerRequest]) (*connect.Response[leapmuxv1.AdminWorkerServiceGetWorkerResponse], error) {
	if err := requireField(req.Msg.GetId(), "id"); err != nil {
		return nil, err
	}
	// GetAdmin, not a worker read plus an owner read: the owner projection
	// is one LEFT JOIN, and rebuilding it in Go reported a deleted owner's
	// username where the listing reports "", left owner_deleted at false
	// for every row, and discarded a real store fault as "no owner".
	w, err := s.store.Workers().GetAdmin(ctx, req.Msg.GetId())
	if err != nil {
		return nil, storeConnectError(err, "get worker")
	}
	return connect.NewResponse(&leapmuxv1.AdminWorkerServiceGetWorkerResponse{
		Worker: adminWorkerToProto(*w),
	}), nil
}

func (s *AdminWorkerService) DeregisterWorker(ctx context.Context, req *connect.Request[leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest]) (*connect.Response[leapmuxv1.AdminWorkerServiceDeregisterWorkerResponse], error) {
	if err := requireField(req.Msg.GetId(), "id"); err != nil {
		return nil, err
	}
	// Read the row FIRST: the workers-changed notification goes to the
	// worker's OWNER, and after the status flip the id is still
	// resolvable but the intent is no longer obvious to a later reader.
	w, err := s.store.Workers().GetAdmin(ctx, req.Msg.GetId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The SAME message the row-count check below gives, so an
			// unknown id and an already-inactive worker read alike: from the
			// caller's side they are one condition.
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker %s not found or not active", req.Msg.GetId()))
		}
		return nil, storeConnectError(err, "deregister worker")
	}
	n, err := s.store.Workers().ForceDeregister(ctx, req.Msg.GetId())
	if err != nil {
		return nil, storeConnectError(err, "deregister worker")
	}
	if n == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker %s not found or not active", req.Msg.GetId()))
	}
	if err := s.deregisterEffects.Apply(ctx, req.Msg.GetId(), w.RegisteredBy); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.AdminWorkerServiceDeregisterWorkerResponse{}), nil
}

func (s *AdminWorkerService) ListRegistrationKeys(ctx context.Context, req *connect.Request[leapmuxv1.ListRegistrationKeysRequest]) (*connect.Response[leapmuxv1.ListRegistrationKeysResponse], error) {
	page, err := s.store.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
		PageParams:     NormalizePageParams(req.Msg.GetCursor(), req.Msg.GetLimit()),
		IncludeExpired: req.Msg.GetIncludeExpired(),
	})
	if err != nil {
		return nil, storeConnectError(err, "list registration keys")
	}
	keys := make([]*leapmuxv1.AdminRegistrationKey, 0, len(page.Rows))
	for _, k := range page.Rows {
		keys = append(keys, &leapmuxv1.AdminRegistrationKey{
			Id:              k.ID,
			CreatedBy:       k.CreatedBy,
			CreatorUsername: k.CreatorUsername,
			CreatorDeleted:  k.CreatorDeleted,
			CreatedAt:       timestamppb.New(k.CreatedAt),
			ExpiresAt:       timestamppb.New(k.ExpiresAt),
		})
	}
	return connect.NewResponse(&leapmuxv1.ListRegistrationKeysResponse{Keys: keys, NextCursor: page.NextCursor}), nil
}

func (s *AdminWorkerService) RevokeRegistrationKey(ctx context.Context, req *connect.Request[leapmuxv1.RevokeRegistrationKeyRequest]) (*connect.Response[leapmuxv1.RevokeRegistrationKeyResponse], error) {
	if err := requireField(req.Msg.GetId(), "id"); err != nil {
		return nil, err
	}
	n, err := s.store.RegistrationKeys().AdminSoftDelete(ctx, req.Msg.GetId())
	if err != nil {
		return nil, storeConnectError(err, "revoke registration key")
	}
	if n == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("registration key not found: %s", req.Msg.GetId()))
	}
	return connect.NewResponse(&leapmuxv1.RevokeRegistrationKeyResponse{}), nil
}

// maxRegistrationKeyPurgeBatches is the runaway limit on one purge, not
// the expected pass count: the drain normally ends when a pass deletes
// nothing.
const maxRegistrationKeyPurgeBatches = 100

func (s *AdminWorkerService) PurgeExpiredRegistrationKeys(ctx context.Context, _ *connect.Request[leapmuxv1.PurgeExpiredRegistrationKeysRequest]) (*connect.Response[leapmuxv1.PurgeExpiredRegistrationKeysResponse], error) {
	// Drain through the shared helper, so a full backlog purge runs in the
	// query's own batches and cannot hold one long write lock. The helper
	// ends on a pass that deletes NOTHING, never on a short page: a short-
	// page test needs the query's LIMIT restated in Go, and the two drift.
	cutoff := time.Now().UTC()
	total, err := cleanup.DrainUntilEmpty(ctx, maxRegistrationKeyPurgeBatches, func() (int64, error) {
		return s.store.Cleanup().HardDeleteExpiredRegistrationKeysBefore(ctx, cutoff)
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("purge expired registration keys: %w", err))
	}
	return connect.NewResponse(&leapmuxv1.PurgeExpiredRegistrationKeysResponse{Purged: total}), nil
}
