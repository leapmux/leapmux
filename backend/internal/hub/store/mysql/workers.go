package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

// workerStore implements store.WorkerStore backed by MySQL.
type workerStore struct{ conn *mysqlConn }

var _ store.WorkerStore = (*workerStore)(nil)

func (s *workerStore) Create(ctx context.Context, p store.CreateWorkerParams) error {
	return mapErr(s.conn.q.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID:              p.ID,
		AuthToken:       p.AuthToken,
		RegisteredBy:    p.RegisteredBy,
		PublicKey:       p.PublicKey,
		MlkemPublicKey:  p.MlkemPublicKey,
		SlhdsaPublicKey: p.SlhdsaPublicKey,
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
	return store.GetOwnedWorker(ctx, p, s.GetByID, s.hasAccess)
}

func (s *workerStore) hasAccess(ctx context.Context, workerID, userID string) (bool, error) {
	ok, err := s.conn.q.HasWorkerAccess(ctx, gendb.HasWorkerAccessParams{
		WorkerID: workerID,
		UserID:   userID,
	})
	return ok, mapErr(err)
}

func (s *workerStore) ListByUserID(ctx context.Context, p store.ListWorkersByUserIDParams) ([]store.Worker, error) {
	params, err := listWorkersByUserIDParams(p.RegisteredBy, p.Cursor, p.Limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.conn.q.ListWorkersByUserID(ctx, params)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorkers(rows), nil
}

func (s *workerStore) ListOwned(ctx context.Context, p store.ListOwnedWorkersParams) ([]store.Worker, error) {
	params, err := listOwnedWorkersParams(p.UserID, p.Cursor, p.Limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.conn.q.ListOwnedWorkers(ctx, params)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorkers(rows), nil
}

func (s *workerStore) ListAdmin(ctx context.Context, p store.ListWorkersAdminParams) ([]store.WorkerWithOwner, error) {
	switch {
	case p.UserID == nil && p.Status == nil:
		params, err := listWorkersAdminAllParams(p.Cursor, p.Limit)
		if err != nil {
			return nil, err
		}
		rows, err := s.conn.q.ListWorkersAdminAll(ctx, params)
		if err != nil {
			return nil, mapErr(err)
		}
		return store.MapSlice(rows, fromDBListWorkersAdminAllRow), nil

	case p.UserID == nil && p.Status != nil:
		params, err := listWorkersAdminByStatusParams(*p.Status, p.Cursor, p.Limit)
		if err != nil {
			return nil, err
		}
		rows, err := s.conn.q.ListWorkersAdminByStatus(ctx, params)
		if err != nil {
			return nil, mapErr(err)
		}
		return store.MapSlice(rows, fromDBListWorkersAdminByStatusRow), nil

	case p.UserID != nil && p.Status == nil:
		params, err := listWorkersAdminByUserParams(*p.UserID, p.Cursor, p.Limit)
		if err != nil {
			return nil, err
		}
		rows, err := s.conn.q.ListWorkersAdminByUser(ctx, params)
		if err != nil {
			return nil, mapErr(err)
		}
		return store.MapSlice(rows, fromDBListWorkersAdminByUserRow), nil

	default: // both non-nil
		params, err := listWorkersAdminByUserAndStatusParams(*p.UserID, *p.Status, p.Cursor, p.Limit)
		if err != nil {
			return nil, err
		}
		rows, err := s.conn.q.ListWorkersAdminByUserAndStatus(ctx, params)
		if err != nil {
			return nil, mapErr(err)
		}
		return store.MapSlice(rows, fromDBListWorkersAdminByUserAndStatusRow), nil
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
	return rowsAffected(s.conn.q.DeregisterWorker(ctx, gendb.DeregisterWorkerParams{
		ID:           p.ID,
		RegisteredBy: p.RegisteredBy,
	}))
}

func (s *workerStore) ForceDeregister(ctx context.Context, id string) (int64, error) {
	return rowsAffected(s.conn.q.ForceDeregisterWorker(ctx, id))
}

func (s *workerStore) MarkDeleted(ctx context.Context, id string) error {
	return mapErr(s.conn.q.MarkWorkerDeleted(ctx, id))
}

func (s *workerStore) MarkAllDeletedByUser(ctx context.Context, registeredBy string) error {
	return mapErr(s.conn.q.MarkAllWorkersDeletedByUser(ctx, registeredBy))
}

func fromDBWorker(w gendb.Worker) *store.Worker {
	return &store.Worker{
		ID:              w.ID,
		AuthToken:       w.AuthToken,
		RegisteredBy:    w.RegisteredBy,
		Status:          w.Status,
		CreatedAt:       w.CreatedAt,
		LastSeenAt:      ptrconv.NullTimeToPtr(w.LastSeenAt),
		PublicKey:       w.PublicKey,
		MlkemPublicKey:  w.MlkemPublicKey,
		SlhdsaPublicKey: w.SlhdsaPublicKey,
		DeletedAt:       ptrconv.NullTimeToPtr(w.DeletedAt),
	}
}

func fromDBWorkers(rows []gendb.Worker) []store.Worker {
	return store.MapSlice(rows, func(r gendb.Worker) store.Worker { return *fromDBWorker(r) })
}

func fromDBListWorkersAdminAllRow(r gendb.ListWorkersAdminAllRow) store.WorkerWithOwner {
	return store.WorkerWithOwner{
		Worker: store.Worker{
			ID:              r.ID,
			AuthToken:       r.AuthToken,
			RegisteredBy:    r.RegisteredBy,
			Status:          r.Status,
			CreatedAt:       r.CreatedAt,
			LastSeenAt:      ptrconv.NullTimeToPtr(r.LastSeenAt),
			PublicKey:       r.PublicKey,
			MlkemPublicKey:  r.MlkemPublicKey,
			SlhdsaPublicKey: r.SlhdsaPublicKey,
			DeletedAt:       ptrconv.NullTimeToPtr(r.DeletedAt),
		},
		OwnerUsername: r.OwnerUsername,
	}
}

func fromDBListWorkersAdminByStatusRow(r gendb.ListWorkersAdminByStatusRow) store.WorkerWithOwner {
	return store.WorkerWithOwner{
		Worker: store.Worker{
			ID:              r.ID,
			AuthToken:       r.AuthToken,
			RegisteredBy:    r.RegisteredBy,
			Status:          r.Status,
			CreatedAt:       r.CreatedAt,
			LastSeenAt:      ptrconv.NullTimeToPtr(r.LastSeenAt),
			PublicKey:       r.PublicKey,
			MlkemPublicKey:  r.MlkemPublicKey,
			SlhdsaPublicKey: r.SlhdsaPublicKey,
			DeletedAt:       ptrconv.NullTimeToPtr(r.DeletedAt),
		},
		OwnerUsername: r.OwnerUsername,
	}
}

func fromDBListWorkersAdminByUserRow(r gendb.ListWorkersAdminByUserRow) store.WorkerWithOwner {
	return store.WorkerWithOwner{
		Worker: store.Worker{
			ID:              r.ID,
			AuthToken:       r.AuthToken,
			RegisteredBy:    r.RegisteredBy,
			Status:          r.Status,
			CreatedAt:       r.CreatedAt,
			LastSeenAt:      ptrconv.NullTimeToPtr(r.LastSeenAt),
			PublicKey:       r.PublicKey,
			MlkemPublicKey:  r.MlkemPublicKey,
			SlhdsaPublicKey: r.SlhdsaPublicKey,
			DeletedAt:       ptrconv.NullTimeToPtr(r.DeletedAt),
		},
		OwnerUsername: r.OwnerUsername,
	}
}

func fromDBListWorkersAdminByUserAndStatusRow(r gendb.ListWorkersAdminByUserAndStatusRow) store.WorkerWithOwner {
	return store.WorkerWithOwner{
		Worker: store.Worker{
			ID:              r.ID,
			AuthToken:       r.AuthToken,
			RegisteredBy:    r.RegisteredBy,
			Status:          r.Status,
			CreatedAt:       r.CreatedAt,
			LastSeenAt:      ptrconv.NullTimeToPtr(r.LastSeenAt),
			PublicKey:       r.PublicKey,
			MlkemPublicKey:  r.MlkemPublicKey,
			SlhdsaPublicKey: r.SlhdsaPublicKey,
			DeletedAt:       ptrconv.NullTimeToPtr(r.DeletedAt),
		},
		OwnerUsername: r.OwnerUsername,
	}
}
