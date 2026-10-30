package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// workerStore implements store.WorkerStore backed by PostgreSQL.
type workerStore struct{ conn *pgConn }

var _ store.WorkerStore = (*workerStore)(nil)

func (s *workerStore) Create(ctx context.Context, p store.CreateWorkerParams) error {
	return mapErr(s.conn.q.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID:              p.ID,
		AuthToken:       p.AuthToken,
		RegisteredBy:    p.RegisteredBy.String(),
		PublicKey:       p.PublicKey,
		MlkemPublicKey:  p.MlkemPublicKey,
		SlhdsaPublicKey: p.SlhdsaPublicKey,
		AutoRegistered:  p.AutoRegistered,
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

func (s *workerStore) GetPublicKey(ctx context.Context, id string) (*store.WorkerPublicKeys, error) {
	row, err := s.conn.q.GetWorkerPublicKey(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.WorkerPublicKeys{
		PublicKey:       row.PublicKey,
		MlkemPublicKey:  row.MlkemPublicKey,
		SlhdsaPublicKey: row.SlhdsaPublicKey,
	}, nil
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
	// The admin worker listing is a 2x2 matrix over (user_id none/set) x (live
	// or deleted), dispatched to four generated queries; the status filter
	// picks the half and, for the live half, the states it includes (see
	// store.WorkerAdminListScopeOf). The user_id dimension is a
	// required-equality query rather than an opt-in OR-probe, which defeated
	// SQLite's index seek and emitted an untyped interface{} param that broke
	// Postgres (SQLSTATE 42P08). See workers.sql for the per-query index
	// rationale.
	scope, err := store.WorkerAdminListScopeOf(p.Status)
	if err != nil {
		return store.Page[store.WorkerWithOwner]{}, err
	}
	switch {
	case scope.Deleted && p.UserID != nil:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminDeletedByUserParams, error) {
				return listWorkersAdminDeletedByUserParams(*p.UserID, p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdminDeletedByUser,
			fromDBListWorkersAdminDeletedByUserRow)
	case scope.Deleted:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminDeletedParams, error) {
				return listWorkersAdminDeletedParams(p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdminDeleted,
			fromDBListWorkersAdminDeletedRow)
	case p.UserID != nil:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminLiveByUserParams, error) {
				return listWorkersAdminLiveByUserParams(scope, *p.UserID, p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdminLiveByUser,
			fromDBListWorkersAdminLiveByUserRow)
	default:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminLiveParams, error) {
				return listWorkersAdminLiveParams(scope, p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdminLive,
			fromDBListWorkersAdminLiveRow)
	}
}

func (s *workerStore) SetStatus(ctx context.Context, p store.SetWorkerStatusParams) error {
	deregistering, err := p.DeregisteringFlag()
	if err != nil {
		return err
	}
	return mapErr(s.conn.q.SetWorkerStatus(ctx, gendb.SetWorkerStatusParams{
		Deregistering: deregistering,
		ID:            p.ID,
	}))
}

func (s *workerStore) UpdateLastSeen(ctx context.Context, id string) error {
	return mapErr(s.conn.q.UpdateWorkerLastSeen(ctx, id))
}

func (s *workerStore) UpdatePublicKey(ctx context.Context, p store.UpdateWorkerPublicKeyParams) error {
	return mapErr(s.conn.q.UpdateWorkerPublicKey(ctx, gendb.UpdateWorkerPublicKeyParams{
		PublicKey:       p.PublicKey,
		MlkemPublicKey:  p.MlkemPublicKey,
		SlhdsaPublicKey: p.SlhdsaPublicKey,
		ID:              p.ID,
	}))
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
		PublicKey:       w.PublicKey,
		MlkemPublicKey:  w.MlkemPublicKey,
		SlhdsaPublicKey: w.SlhdsaPublicKey,
		AutoRegistered:  w.AutoRegistered,
		DeregisteringAt: w.DeregisteringAt.Ptr(),
		DeletedAt:       w.DeletedAt.Ptr(),
	}
}

// workerWithOwner is the shared body of the four admin-worker Row mappers. The
// queries select sqlc.embed(w) so every admin Row is {Worker, OwnerUsername};
// this helper keeps the gendb.Worker -> store.Worker mapping in one site.
func workerWithOwner(w gendb.Worker, ownerUsername string, ownerDeleted bool) store.WorkerWithOwner {
	return store.WorkerWithOwner{Worker: *fromDBWorker(w), OwnerUsername: ownerUsername, OwnerDeleted: ownerDeleted}
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
