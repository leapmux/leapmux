package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect/v2"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/cleanup"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/nilcheck"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminWorkerService implements the leapmux.v1.AdminWorkerService
// ConnectRPC handler: the cross-user worker administration surface
// (listing, inspection, force-deregister, registration keys). The
// per-user worker view lives in WorkerManagementService.
type AdminWorkerService struct {
	store store.Store
	// workers answers whether a Worker is connected right now.
	workers workerPresence
	// deregisterEffects runs the out-of-database half of a deregistration.
	// WorkerManagementService holds the same instance, so an administrator's
	// force-deregister does exactly what the owner's does.
	deregisterEffects *WorkerDeregisterEffects
}

// workerPresence is the one registry question this service asks: whether a
// Worker holds a connection. *workermgr.Manager satisfies it.
type workerPresence interface {
	OnlineForTrustedPath(workerID string) bool
}

func NewAdminWorkerService(st store.Store, workers workerPresence, deregisterEffects *WorkerDeregisterEffects) *AdminWorkerService {
	// A missing registry must never read as "every Worker is offline": that
	// would silently disable the bundled-Worker guard below.
	if nilcheck.IsNilDependency(workers) {
		panic("service: NewAdminWorkerService requires a worker registry")
	}
	return &AdminWorkerService{store: st, workers: workers, deregisterEffects: deregisterEffects}
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

func (s *AdminWorkerService) ListWorkers(ctx context.Context, req *leapmuxv1.AdminWorkerServiceListWorkersRequest) (*leapmuxv1.AdminWorkerServiceListWorkersResponse, error) {
	userFilter, err := resolveAdminUserFilter(ctx, s.store, req.GetUser())
	if err != nil {
		return nil, err
	}
	params := store.ListWorkersAdminParams{
		UserID:     userFilter,
		PageParams: pageParams(req.GetPage()),
	}
	if st := req.GetStatus(); st != leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED {
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
	return &leapmuxv1.AdminWorkerServiceListWorkersResponse{Workers: workers, Page: pageResponse(page.NextCursor)}, nil
}

func (s *AdminWorkerService) GetWorker(ctx context.Context, req *leapmuxv1.AdminWorkerServiceGetWorkerRequest) (*leapmuxv1.AdminWorkerServiceGetWorkerResponse, error) {
	if err := requireField(req.GetId(), "id"); err != nil {
		return nil, err
	}
	// GetAdmin, not a worker read plus an owner read: the owner projection
	// is one LEFT JOIN, and rebuilding it in Go reported a deleted owner's
	// username where the listing reports "", left owner_deleted at false
	// for every row, and discarded a real store fault as "no owner".
	w, err := s.store.Workers().GetAdmin(ctx, req.GetId())
	if err != nil {
		return nil, storeConnectError(err, "get worker")
	}
	return &leapmuxv1.AdminWorkerServiceGetWorkerResponse{
		Worker: adminWorkerToProto(*w),
	}, nil
}

func (s *AdminWorkerService) DeregisterWorker(ctx context.Context, req *leapmuxv1.AdminWorkerServiceDeregisterWorkerRequest) (*leapmuxv1.AdminWorkerServiceDeregisterWorkerResponse, error) {
	if err := requireField(req.GetId(), "id"); err != nil {
		return nil, err
	}
	// Read the row FIRST: the workers-changed notification goes to the
	// worker's OWNER, and after the status flip the id is still
	// resolvable but the intent is no longer obvious to a later reader.
	w, err := s.store.Workers().GetAdmin(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The SAME message the row-count check below gives, so an
			// unknown id and an already-inactive worker read alike: from the
			// caller's side they are one condition.
			return nil, connect.Errorf(connect.CodeNotFound, "worker %s not found or not active", req.GetId())
		}
		return nil, storeConnectError(err, "deregister worker")
	}
	if err := refuseConnectedBundledWorker(&w.Worker, s.workers.OnlineForTrustedPath(w.ID)); err != nil {
		return nil, err
	}
	n, err := s.store.Workers().ForceDeregister(ctx, req.GetId())
	if err != nil {
		return nil, storeConnectError(err, "deregister worker")
	}
	if n == 0 {
		return nil, connect.Errorf(connect.CodeNotFound, "worker %s not found or not active", req.GetId())
	}
	if err := s.deregisterEffects.Apply(ctx, req.GetId(), w.RegisteredBy); err != nil {
		return nil, wrappedConnectError(connect.CodeInternal, err)
	}
	return &leapmuxv1.AdminWorkerServiceDeregisterWorkerResponse{}, nil
}

func (s *AdminWorkerService) ListRegistrationKeys(ctx context.Context, req *leapmuxv1.ListRegistrationKeysRequest) (*leapmuxv1.ListRegistrationKeysResponse, error) {
	page, err := s.store.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
		PageParams:     pageParams(req.GetPage()),
		IncludeExpired: req.GetIncludeExpired(),
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
	return &leapmuxv1.ListRegistrationKeysResponse{Keys: keys, Page: pageResponse(page.NextCursor)}, nil
}

func (s *AdminWorkerService) RevokeRegistrationKey(ctx context.Context, req *leapmuxv1.AdminWorkerServiceRevokeRegistrationKeyRequest) (*leapmuxv1.AdminWorkerServiceRevokeRegistrationKeyResponse, error) {
	if err := requireField(req.GetId(), "id"); err != nil {
		return nil, err
	}
	n, err := s.store.RegistrationKeys().AdminSoftDelete(ctx, req.GetId())
	if err != nil {
		return nil, storeConnectError(err, "revoke registration key")
	}
	if n == 0 {
		return nil, s.deadRegistrationKeyError(ctx, req.GetId())
	}
	return &leapmuxv1.AdminWorkerServiceRevokeRegistrationKeyResponse{}, nil
}

// deadRegistrationKeyError answers a revoke that changed no row. The revoke
// query matches only a live key, so the row is either absent or dead, and this
// read tells a mistyped id from a key that an operator can still see in the
// listing with expired keys. The owner's revoke keeps both cases as one answer,
// because there the difference would disclose that another owner's key exists.
// This surface has no ownership filter, so it has nothing to hide.
func (s *AdminWorkerService) deadRegistrationKeyError(ctx context.Context, keyID string) error {
	if _, err := s.store.RegistrationKeys().GetByID(ctx, keyID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.Errorf(connect.CodeNotFound, "registration key not found: %s", keyID)
		}
		return storeConnectError(err, "revoke registration key")
	}
	return connect.Errorf(connect.CodeFailedPrecondition,
		"registration key %s is already revoked, consumed or expired", keyID)
}

// maxRegistrationKeyPurgeBatches is the runaway limit on one purge, not
// the expected pass count: the drain normally ends when a pass deletes
// nothing.
const maxRegistrationKeyPurgeBatches = 100

func (s *AdminWorkerService) PurgeExpiredRegistrationKeys(ctx context.Context, _ *leapmuxv1.PurgeExpiredRegistrationKeysRequest) (*leapmuxv1.PurgeExpiredRegistrationKeysResponse, error) {
	// Drain through the shared helper, so a full backlog purge runs in the
	// query's own batches and cannot hold one long write lock. The helper
	// ends on a pass that deletes NOTHING, never on a short page: a short-
	// page test needs the query's LIMIT restated in Go, and the two drift.
	cutoff := time.Now().UTC()
	total, err := cleanup.DrainUntilEmpty(ctx, maxRegistrationKeyPurgeBatches, func() (int64, error) {
		return s.store.Cleanup().HardDeleteExpiredRegistrationKeysBefore(ctx, cutoff)
	})
	if err != nil {
		cause := fmt.Errorf("purge expired registration keys: %w", err)
		return nil, wrappedConnectError(connect.CodeInternal, cause)
	}
	return &leapmuxv1.PurgeExpiredRegistrationKeysResponse{Purged: total}, nil
}
