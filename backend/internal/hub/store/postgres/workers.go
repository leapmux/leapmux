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
	owner, ok := store.OwnerFilter(p.RegisteredBy)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
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
	// The admin worker listing is a 2x2 matrix over (status nil/set) x (user_id
	// nil/set), dispatched to four generated queries. The user_id dimension is a
	// required-equality query rather than an opt-in `(narg IS NULL OR registered_by
	// = narg)` probe: that probe made sqlc emit UserID as an untyped interface{},
	// and binding NULL raised SQLSTATE 42P08 "could not determine data type of
	// parameter" (YugabyteDB inherits the break via this store). Splitting
	// user_id into its own query yields a typed `string` param and restores the
	// index seek. See workers.sql for the per-query index rationale.
	switch {
	case p.Status != nil && p.UserID != nil:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminByUserAndStatusParams, error) {
				return listWorkersAdminByUserAndStatusParams(*p.Status, *p.UserID, p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdminByUserAndStatus,
			fromDBListWorkersAdminByUserAndStatusRow)
	case p.Status != nil:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminByStatusParams, error) {
				return listWorkersAdminByStatusParams(*p.Status, p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdminByStatus,
			fromDBListWorkersAdminByStatusRow)
	case p.UserID != nil:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminByUserParams, error) {
				return listWorkersAdminByUserParams(*p.UserID, p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdminByUser,
			fromDBListWorkersAdminByUserRow)
	default:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminParams, error) {
				return listWorkersAdminParams(p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdmin,
			fromDBListWorkersAdminRow)
	}
}

func (s *workerStore) SetStatus(ctx context.Context, p store.SetWorkerStatusParams) error {
	return mapErr(s.conn.q.SetWorkerStatus(ctx, gendb.SetWorkerStatusParams{
		Status: p.Status,
		ID:     p.ID,
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
	owner, ok := store.OwnerFilter(p.RegisteredBy)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
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
	owner, ok := store.OwnerFilter(registeredBy)
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
		Status:          w.Status,
		CreatedAt:       w.CreatedAt.Time,
		LastSeenAt:      w.LastSeenAt.Ptr(),
		PublicKey:       w.PublicKey,
		MlkemPublicKey:  w.MlkemPublicKey,
		SlhdsaPublicKey: w.SlhdsaPublicKey,
		AutoRegistered:  w.AutoRegistered,
		DeletedAt:       w.DeletedAt.Ptr(),
	}
}

// workerWithOwner is the shared body of the four admin-worker Row mappers. The
// queries select sqlc.embed(w) so every admin Row is {Worker, OwnerUsername};
// this helper keeps the gendb.Worker -> store.Worker mapping in one site.
func workerWithOwner(w gendb.Worker, ownerUsername string, ownerDeleted bool) store.WorkerWithOwner {
	return store.WorkerWithOwner{Worker: *fromDBWorker(w), OwnerUsername: ownerUsername, OwnerDeleted: ownerDeleted}
}

func fromDBListWorkersAdminRow(r gendb.ListWorkersAdminRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}

func fromDBListWorkersAdminByUserRow(r gendb.ListWorkersAdminByUserRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}

func fromDBListWorkersAdminByStatusRow(r gendb.ListWorkersAdminByStatusRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}

func fromDBListWorkersAdminByUserAndStatusRow(r gendb.ListWorkersAdminByUserAndStatusRow) store.WorkerWithOwner {
	return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
}
