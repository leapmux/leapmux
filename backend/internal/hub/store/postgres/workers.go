package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	leapmux "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

// workerStore implements store.WorkerStore backed by PostgreSQL.
type workerStore struct{ q *gendb.Queries }

var _ store.WorkerStore = (*workerStore)(nil)

func (s *workerStore) Create(ctx context.Context, p store.CreateWorkerParams) error {
	return mapErr(s.q.CreateWorker(ctx, gendb.CreateWorkerParams{
		ID:              p.ID,
		AuthToken:       p.AuthToken,
		RegisteredBy:    p.RegisteredBy,
		PublicKey:       p.PublicKey,
		MlkemPublicKey:  p.MlkemPublicKey,
		SlhdsaPublicKey: p.SlhdsaPublicKey,
	}))
}

func (s *workerStore) GetByID(ctx context.Context, id string) (*store.Worker, error) {
	row, err := s.q.GetWorkerByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorker(row), nil
}

func (s *workerStore) GetByAuthToken(ctx context.Context, token string) (*store.Worker, error) {
	row, err := s.q.GetWorkerByAuthToken(ctx, token)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorker(row), nil
}

func (s *workerStore) GetPublicKey(ctx context.Context, id string) (*store.WorkerPublicKeys, error) {
	row, err := s.q.GetWorkerPublicKey(ctx, id)
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
	ok, err := s.q.HasWorkerAccess(ctx, gendb.HasWorkerAccessParams{
		WorkerID: workerID,
		UserID:   userID,
	})
	return ok, mapErr(err)
}

func (s *workerStore) ListByUserID(ctx context.Context, p store.ListWorkersByUserIDParams) ([]store.Worker, error) {
	cursor, err := parseCursorToTs(p.Cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListWorkersByUserID(ctx, gendb.ListWorkersByUserIDParams{
		RegisteredBy: p.RegisteredBy,
		Cursor:       cursor,
		Limit:        int32(p.Limit),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorkers(rows), nil
}

func (s *workerStore) ListOwned(ctx context.Context, p store.ListOwnedWorkersParams) ([]store.Worker, error) {
	cursor, err := parseCursorToTs(p.Cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListOwnedWorkers(ctx, gendb.ListOwnedWorkersParams{
		UserID: p.UserID,
		Cursor: cursor,
		Limit:  int32(p.Limit),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorkers(rows), nil
}

func (s *workerStore) ListAdmin(ctx context.Context, p store.ListWorkersAdminParams) ([]store.WorkerWithOwner, error) {
	cursor, err := parseCursorToTs(p.Cursor)
	if err != nil {
		return nil, err
	}
	switch {
	case p.UserID == nil && p.Status == nil:
		rows, err := s.q.ListWorkersAdminAll(ctx, gendb.ListWorkersAdminAllParams{
			Cursor: cursor,
			Limit:  int32(p.Limit),
		})
		if err != nil {
			return nil, mapErr(err)
		}
		return fromDBWorkersAdmin(rows), nil

	case p.UserID == nil && p.Status != nil:
		rows, err := s.q.ListWorkersAdminByStatus(ctx, gendb.ListWorkersAdminByStatusParams{
			Status: *p.Status,
			Cursor: cursor,
			Limit:  int32(p.Limit),
		})
		if err != nil {
			return nil, mapErr(err)
		}
		return fromDBWorkersAdmin(rows), nil

	case p.UserID != nil && p.Status == nil:
		rows, err := s.q.ListWorkersAdminByUser(ctx, gendb.ListWorkersAdminByUserParams{
			UserID: *p.UserID,
			Cursor: cursor,
			Limit:  int32(p.Limit),
		})
		if err != nil {
			return nil, mapErr(err)
		}
		return fromDBWorkersAdmin(rows), nil

	default: // both non-nil
		rows, err := s.q.ListWorkersAdminByUserAndStatus(ctx, gendb.ListWorkersAdminByUserAndStatusParams{
			UserID: *p.UserID,
			Status: *p.Status,
			Cursor: cursor,
			Limit:  int32(p.Limit),
		})
		if err != nil {
			return nil, mapErr(err)
		}
		return fromDBWorkersAdmin(rows), nil
	}
}

func (s *workerStore) SetStatus(ctx context.Context, p store.SetWorkerStatusParams) error {
	return mapErr(s.q.SetWorkerStatus(ctx, gendb.SetWorkerStatusParams{
		Status: p.Status,
		ID:     p.ID,
	}))
}

func (s *workerStore) UpdateLastSeen(ctx context.Context, id string) error {
	return mapErr(s.q.UpdateWorkerLastSeen(ctx, id))
}

func (s *workerStore) UpdatePublicKey(ctx context.Context, p store.UpdateWorkerPublicKeyParams) error {
	return mapErr(s.q.UpdateWorkerPublicKey(ctx, gendb.UpdateWorkerPublicKeyParams{
		PublicKey:       p.PublicKey,
		MlkemPublicKey:  p.MlkemPublicKey,
		SlhdsaPublicKey: p.SlhdsaPublicKey,
		ID:              p.ID,
	}))
}

func (s *workerStore) Deregister(ctx context.Context, p store.DeregisterWorkerParams) (int64, error) {
	return rowsAffected(s.q.DeregisterWorker(ctx, gendb.DeregisterWorkerParams{
		ID:           p.ID,
		RegisteredBy: p.RegisteredBy,
	}))
}

func (s *workerStore) ForceDeregister(ctx context.Context, id string) (int64, error) {
	return rowsAffected(s.q.ForceDeregisterWorker(ctx, id))
}

func (s *workerStore) MarkDeleted(ctx context.Context, id string) error {
	return mapErr(s.q.MarkWorkerDeleted(ctx, id))
}

func (s *workerStore) MarkAllDeletedByUser(ctx context.Context, registeredBy string) error {
	return mapErr(s.q.MarkAllWorkersDeletedByUser(ctx, registeredBy))
}

func fromDBWorker(w gendb.Worker) *store.Worker {
	return &store.Worker{
		ID:              w.ID,
		AuthToken:       w.AuthToken,
		RegisteredBy:    w.RegisteredBy,
		Status:          w.Status,
		CreatedAt:       tsToTime(w.CreatedAt),
		LastSeenAt:      tsToTimePtr(w.LastSeenAt),
		PublicKey:       w.PublicKey,
		MlkemPublicKey:  w.MlkemPublicKey,
		SlhdsaPublicKey: w.SlhdsaPublicKey,
		DeletedAt:       tsToTimePtr(w.DeletedAt),
	}
}

func fromDBWorkers(rows []gendb.Worker) []store.Worker {
	return store.MapSlice(rows, func(r gendb.Worker) store.Worker { return *fromDBWorker(r) })
}

// workerAdminRow is satisfied by all four sqlc-generated ListWorkersAdmin*Row
// types, which share identical fields.
type workerAdminRow interface {
	~struct {
		ID              string               `json:"id"`
		AuthToken       string               `json:"auth_token"`
		RegisteredBy    string               `json:"registered_by"`
		Status          leapmux.WorkerStatus `json:"status"`
		CreatedAt       pgtype.Timestamptz   `json:"created_at"`
		LastSeenAt      pgtype.Timestamptz   `json:"last_seen_at"`
		PublicKey       []byte               `json:"public_key"`
		MlkemPublicKey  []byte               `json:"mlkem_public_key"`
		SlhdsaPublicKey []byte               `json:"slhdsa_public_key"`
		DeletedAt       pgtype.Timestamptz   `json:"deleted_at"`
		OwnerUsername   string               `json:"owner_username"`
	}
}

func fromDBWorkersAdmin[R workerAdminRow](rows []R) []store.WorkerWithOwner {
	return store.MapSlice(rows, func(r R) store.WorkerWithOwner {
		type concrete struct {
			ID              string               `json:"id"`
			AuthToken       string               `json:"auth_token"`
			RegisteredBy    string               `json:"registered_by"`
			Status          leapmux.WorkerStatus `json:"status"`
			CreatedAt       pgtype.Timestamptz   `json:"created_at"`
			LastSeenAt      pgtype.Timestamptz   `json:"last_seen_at"`
			PublicKey       []byte               `json:"public_key"`
			MlkemPublicKey  []byte               `json:"mlkem_public_key"`
			SlhdsaPublicKey []byte               `json:"slhdsa_public_key"`
			DeletedAt       pgtype.Timestamptz   `json:"deleted_at"`
			OwnerUsername   string               `json:"owner_username"`
		}
		c := concrete(r)
		return sqlutil.WorkerAdminFields{
			ID:              c.ID,
			AuthToken:       c.AuthToken,
			RegisteredBy:    c.RegisteredBy,
			Status:          c.Status,
			CreatedAt:       tsToTime(c.CreatedAt),
			LastSeenAt:      tsToTimePtr(c.LastSeenAt),
			PublicKey:       c.PublicKey,
			MlkemPublicKey:  c.MlkemPublicKey,
			SlhdsaPublicKey: c.SlhdsaPublicKey,
			DeletedAt:       tsToTimePtr(c.DeletedAt),
			OwnerUsername:   c.OwnerUsername,
		}.ToWorkerWithOwner()
	})
}
