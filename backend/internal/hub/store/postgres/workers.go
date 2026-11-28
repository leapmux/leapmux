package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// workerStore implements store.WorkerStore backed by PostgreSQL.
type workerStore struct{ conn *pgConn }

var _ store.WorkerStore = (*workerStore)(nil)

func (s *workerStore) Create(ctx context.Context, p store.CreateWorkerParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return s.conn.withTransaction(ctx, func(conn *pgConn) error {
		if err := conn.q.CreateWorker(ctx, gendb.CreateWorkerParams{
			ID:                    p.ID,
			AuthTokenHash:         p.AuthTokenHash,
			RegisteredBy:          p.RegisteredBy.String(),
			AutoRegistered:        p.AutoRegistered,
			X25519PublicKey:       p.PublicKeys.X25519,
			MlkemPublicKey:        p.PublicKeys.Mlkem,
			SlhdsaPublicKey:       p.PublicKeys.Slhdsa,
			RegistrationAttemptID: stringToText(p.RegistrationAttemptID),
			RegistrationKeyHash:   p.RegistrationKeyHashOrNil(),
		}); err != nil {
			return mapErr(err)
		}
		return mapErr(conn.q.AdvanceWorkerEnrollment(ctx, p.RegisteredBy.String()))
	})
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

func (s *workerStore) GetByAuthTokenHashIncludeDeleted(ctx context.Context, hash []byte) (*store.Worker, error) {
	row, err := s.conn.q.GetWorkerByAuthTokenHashIncludeDeleted(ctx, hash)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBWorker(row), nil
}

func (s *workerStore) RotateAuthToken(ctx context.Context, p store.RotateWorkerAuthTokenParams) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	n, err := s.conn.q.RotateWorkerAuthToken(ctx, gendb.RotateWorkerAuthTokenParams{ID: p.ID, AuthTokenHash: p.AuthTokenHash, ExpectedAuthTokenHash: p.ExpectedAuthTokenHash})
	return n, mapErr(err)
}

func (s *workerStore) GetByRegistrationAttemptID(ctx context.Context, attemptID string) (*store.Worker, error) {
	row, err := s.conn.q.GetWorkerByRegistrationAttemptID(ctx, stringToText(attemptID))
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
	filter, hasPrefix, err := store.ParseIDPrefix(p.IDPrefix)
	if err != nil {
		return store.Page[store.Worker]{}, err
	}
	mapWorker := func(r gendb.Worker) store.Worker { return *fromDBWorker(r) }
	if !hasPrefix {
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersByUserIDParams, error) {
				return listWorkersByUserIDParams(owner, p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersByUserID, mapWorker)
	}
	idGt, fetchLimit, empty, err := prepareIDPrefixPage(filter, p.Cursor, p.Limit)
	if err != nil || empty {
		return store.Page[store.Worker]{}, err
	}
	if filter.Exact {
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersByUserIDExactParams, error) {
				return gendb.ListWorkersByUserIDExactParams{
					RegisteredBy: owner,
					ID:           filter.Lower,
				}, nil
			},
			s.conn.q.ListWorkersByUserIDExact, mapWorker)
	}
	return queryPage(ctx, p.Limit,
		func() (gendb.ListWorkersByUserIDPrefixParams, error) {
			return gendb.ListWorkersByUserIDPrefixParams{
				RegisteredBy: owner,
				IDLo:         filter.Lower,
				IDHi:         filter.Upper,
				IDGt:         idGt,
				Limit:        fetchLimit,
			}, nil
		},
		s.conn.q.ListWorkersByUserIDPrefix, mapWorker)
}

func (s *workerStore) InventorySnapshot(ctx context.Context, owner userid.UserID) (store.WorkerInventorySnapshot, error) {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.WorkerInventorySnapshot{}, store.ErrNotFound
	}
	rows, err := s.conn.q.GetWorkerInventorySnapshot(ctx, userID)
	if err != nil {
		return store.WorkerInventorySnapshot{}, mapErr(err)
	}
	return store.AssembleWorkerInventorySnapshot(rows, func(r gendb.GetWorkerInventorySnapshotRow) (int64, string) {
		return r.Revision, r.WorkerID
	}), nil
}

func (s *workerStore) ListAdmin(ctx context.Context, p store.ListWorkersAdminParams) (store.Page[store.WorkerWithOwner], error) {
	status, err := store.WorkerAdminListStatusOf(p.Status)
	if err != nil {
		return store.Page[store.WorkerWithOwner]{}, err
	}
	filter, hasPrefix, err := store.ParseIDPrefix(p.IDPrefix)
	if err != nil {
		return store.Page[store.WorkerWithOwner]{}, err
	}
	if hasPrefix {
		idGt, fetchLimit, empty, err := prepareIDPrefixPage(filter, p.Cursor, p.Limit)
		if err != nil || empty {
			return store.Page[store.WorkerWithOwner]{}, err
		}
		userID := ""
		if p.UserID != nil {
			if *p.UserID == "" {
				return store.Page[store.WorkerWithOwner]{}, nil
			}
			userID = *p.UserID
		}
		if filter.Exact {
			return queryPage(ctx, p.Limit,
				func() (gendb.ListWorkersAdminByIDExactParams, error) {
					return gendb.ListWorkersAdminByIDExactParams{
						ID:           filter.Lower,
						UserID:       userID,
						StatusFilter: int32(status),
					}, nil
				},
				s.conn.q.ListWorkersAdminByIDExact,
				func(r gendb.ListWorkersAdminByIDExactRow) store.WorkerWithOwner {
					return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
				})
		}
		return queryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminByIDPrefixParams, error) {
				return gendb.ListWorkersAdminByIDPrefixParams{
					IDLo:         filter.Lower,
					IDHi:         filter.Upper,
					IDGt:         idGt,
					UserID:       userID,
					StatusFilter: int32(status),
					Limit:        fetchLimit,
				}, nil
			},
			s.conn.q.ListWorkersAdminByIDPrefix,
			func(r gendb.ListWorkersAdminByIDPrefixRow) store.WorkerWithOwner {
				return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
			})
	}
	switch status {
	case leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE:
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
		ID:            w.ID,
		AuthTokenHash: w.AuthTokenHash,
		RegisteredBy:  w.RegisteredBy,
		PublicKeys: store.WorkerPublicKeys{
			X25519: w.X25519PublicKey,
			Mlkem:  w.MlkemPublicKey,
			Slhdsa: w.SlhdsaPublicKey,
		},
		Status:         store.WorkerStatusOf(w.DeletedAt.Ptr()),
		CreatedAt:      w.CreatedAt.Time,
		LastSeenAt:     w.LastSeenAt.Ptr(),
		AutoRegistered: w.AutoRegistered,
		DeletedAt:      w.DeletedAt.Ptr(),

		RegistrationAttemptID: w.RegistrationAttemptID.String,
		RegistrationKeyHash:   w.RegistrationKeyHash,
	}
}

// stringToText binds an empty attempt identity as SQL NULL, the shape
// the schema CHECKs pair with a NULL digest.
func stringToText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
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
