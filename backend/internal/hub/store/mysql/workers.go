package mysql

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// workerStore implements store.WorkerStore backed by MySQL.
type workerStore struct{ conn *mysqlConn }

var _ store.WorkerStore = (*workerStore)(nil)

func (s *workerStore) Create(ctx context.Context, p store.CreateWorkerParams) error {
	return mapErr(s.conn.q.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID:             p.ID,
		AuthToken:      p.AuthToken,
		RegisteredBy:   p.RegisteredBy.String(),
		AutoRegistered: p.AutoRegistered,
	}))
}

func (s *workerStore) GetByID(ctx context.Context, id string) (*store.Worker, error) {
	row, err := s.conn.q.GetWorkerByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorker(row), nil
}

func (s *workerStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Worker, error) {
	row, err := s.conn.q.GetWorkerByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorker(row), nil
}

func (s *workerStore) GetAdmin(ctx context.Context, id string) (*store.WorkerWithOwner, error) {
	row, err := s.conn.q.GetWorkerAdmin(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := workerWithOwner(row.Worker, row.OwnerUsername, row.OwnerDeleted)
	return &out, nil
}

func (s *workerStore) GetByAuthToken(ctx context.Context, token string) (*store.Worker, error) {
	row, err := s.conn.q.GetWorkerByAuthToken(ctx, token)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorker(row), nil
}

func (s *workerStore) GetOwned(ctx context.Context, p store.GetOwnedWorkerParams) (*store.Worker, error) {
	return store.GetOwnedWorker(ctx, p, s.GetByID)
}

func (s *workerStore) ListByUserID(ctx context.Context, p store.ListWorkersByUserIDParams) (store.Page[store.Worker], error) {
	owner, ok := userid.OwnerFilter(p.RegisteredBy)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return store.Page[store.Worker]{}, nil
	}
	return queryPage(ctx, p.Limit,
		func() (gendb.ListWorkersByUserIDParams, error) {
			return listWorkersByUserIDParams(owner, p.Cursor, p.Limit)
		},
		s.conn.q.ListWorkersByUserID,
		func(r gendb.Worker) store.Worker { return *fromDBWorker(r) })
}

func (s *workerStore) ListAdmin(ctx context.Context, p store.ListWorkersAdminParams) (store.Page[store.WorkerWithOwner], error) {
	status, err := store.WorkerAdminListStatusOf(p.Status)
	if err != nil {
		return store.Page[store.WorkerWithOwner]{}, err
	}
	switch status {
	case leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED:
		if p.UserID != nil {
			return queryPage(ctx, p.Limit,
				func() (gendb.ListWorkersAdminLiveByUserParams, error) {
					return listWorkersAdminLiveByUserParams(*p.UserID, p.Cursor, p.Limit)
				},
				s.conn.q.ListWorkersAdminLiveByUser, fromDBListWorkersAdminLiveByUserRow)
		}
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminLiveParams, error) { return listWorkersAdminLiveParams(p.Cursor, p.Limit) },
			s.conn.q.ListWorkersAdminLive, fromDBListWorkersAdminLiveRow)
	case leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE:
		if p.UserID != nil {
			return queryPage(ctx, p.Limit,
				func() (gendb.ListWorkersAdminActiveByUserParams, error) {
					return listWorkersAdminActiveByUserParams(*p.UserID, p.Cursor, p.Limit)
				},
				s.conn.q.ListWorkersAdminActiveByUser, fromDBListWorkersAdminActiveByUserRow)
		}
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminActiveParams, error) {
				return listWorkersAdminActiveParams(p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdminActive, fromDBListWorkersAdminActiveRow)
	case leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING:
		if p.UserID != nil {
			return queryPage(ctx, p.Limit,
				func() (gendb.ListWorkersAdminDeregisteringByUserParams, error) {
					return listWorkersAdminDeregisteringByUserParams(*p.UserID, p.Cursor, p.Limit)
				},
				s.conn.q.ListWorkersAdminDeregisteringByUser, fromDBListWorkersAdminDeregisteringByUserRow)
		}
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminDeregisteringParams, error) {
				return listWorkersAdminDeregisteringParams(p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdminDeregistering, fromDBListWorkersAdminDeregisteringRow)
	case leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED:
		if p.UserID != nil {
			return queryPage(ctx, p.Limit,
				func() (gendb.ListWorkersAdminDeletedByUserParams, error) {
					return listWorkersAdminDeletedByUserParams(*p.UserID, p.Cursor, p.Limit)
				},
				s.conn.q.ListWorkersAdminDeletedByUser, fromDBListWorkersAdminDeletedByUserRow)
		}
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminDeletedParams, error) {
				return listWorkersAdminDeletedParams(p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdminDeleted, fromDBListWorkersAdminDeletedRow)
	default:
		return store.Page[store.WorkerWithOwner]{}, store.ErrInvalidArgument
	}
}

func (s *workerStore) UpdateLastSeen(ctx context.Context, id string) error {
	return mapErr(s.conn.q.UpdateWorkerLastSeen(ctx, id))
}

func (s *workerStore) Deregister(ctx context.Context, p store.DeregisterWorkerParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.RegisteredBy)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, nil
	}
	return rowsAffected(s.conn.q.DeregisterWorker(ctx, gendb.DeregisterWorkerParams{
		ID:           p.ID,
		RegisteredBy: owner,
	}))
}

func (s *workerStore) ForceDeregister(ctx context.Context, id string) (int64, error) {
	return rowsAffected(s.conn.q.ForceDeregisterWorker(ctx, id))
}

func (s *workerStore) MarkDeleted(ctx context.Context, id string) error {
	return mapErr(s.conn.q.MarkWorkerDeleted(ctx, id))
}

func (s *workerStore) MarkAllDeletedByUser(ctx context.Context, registeredBy userid.UserID) error {
	owner, ok := userid.OwnerFilter(registeredBy)
	if !ok {
		// Binding "" here would address every blank-registrant row for
		// deletion; reporting success having deleted nothing is no better.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.MarkAllWorkersDeletedByUser(ctx, owner))
}

func fromDBWorker(w gendb.Worker) *store.Worker {
	return &store.Worker{
		ID:              w.ID,
		AuthToken:       w.AuthToken,
		RegisteredBy:    w.RegisteredBy,
		Status:          store.WorkerStatusOf(w.DeregisteringAt.Ptr(), w.DeletedAt.Ptr()),
		CreatedAt:       w.CreatedAt.Time,
		LastSeenAt:      w.LastSeenAt.Ptr(),
		AutoRegistered:  w.AutoRegistered,
		DeregisteringAt: w.DeregisteringAt.Ptr(),
		DeletedAt:       w.DeletedAt.Ptr(),
	}
}

// workerWithOwner is the shared body of the eight admin-worker Row mappers. The
// queries select sqlc.embed(w) so every admin Row is {Worker, OwnerUsername};
// this helper keeps the gendb.Worker -> store.Worker mapping in one site.
func workerWithOwner(w gendb.Worker, ownerUsername string, ownerDeleted bool) store.WorkerWithOwner {
	return store.WorkerWithOwner{Worker: *fromDBWorker(w), OwnerUsername: ownerUsername, OwnerDeleted: ownerDeleted}
}

func fromDBListWorkersAdminActiveRow(r gendb.ListWorkersAdminActiveRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}

func fromDBListWorkersAdminActiveByUserRow(r gendb.ListWorkersAdminActiveByUserRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}

func fromDBListWorkersAdminDeregisteringRow(r gendb.ListWorkersAdminDeregisteringRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}

func fromDBListWorkersAdminDeregisteringByUserRow(r gendb.ListWorkersAdminDeregisteringByUserRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}

func fromDBListWorkersAdminLiveRow(r gendb.ListWorkersAdminLiveRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}

func fromDBListWorkersAdminLiveByUserRow(r gendb.ListWorkersAdminLiveByUserRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}

func fromDBListWorkersAdminDeletedRow(r gendb.ListWorkersAdminDeletedRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}

func fromDBListWorkersAdminDeletedByUserRow(r gendb.ListWorkersAdminDeletedByUserRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}
