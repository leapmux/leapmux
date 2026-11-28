package mysql

import (
	"context"
	"database/sql"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// workerStore implements store.WorkerStore backed by MySQL.
type workerStore struct{ conn *mysqlConn }

var _ store.WorkerStore = (*workerStore)(nil)

func (s *workerStore) Create(ctx context.Context, p store.CreateWorkerParams) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return s.conn.withTransaction(ctx, func(conn *mysqlConn) error {
		if err := conn.q.CreateWorker(ctx, gendb.CreateWorkerParams{
			ID:                    p.ID,
			AuthTokenHash:         p.AuthTokenHash,
			RegisteredBy:          p.RegisteredBy.String(),
			AutoRegistered:        p.AutoRegistered,
			X25519PublicKey:       p.PublicKeys.X25519,
			MlkemPublicKey:        p.PublicKeys.Mlkem,
			SlhdsaPublicKey:       p.PublicKeys.Slhdsa,
			RegistrationAttemptID: stringToNull(p.RegistrationAttemptID),
			RegistrationKeyHash:   bytesToNull(p.RegistrationKeyHashOrNil()),
		}); err != nil {
			return conn.queryError(err)
		}
		return conn.queryError(conn.q.AdvanceWorkerEnrollment(ctx, p.RegisteredBy.String()))
	})
}

func (s *workerStore) GetByID(ctx context.Context, id string) (*store.Worker, error) {
	row, err := s.conn.q.GetWorkerByID(ctx, id)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	return fromDBWorker(row), nil
}

func (s *workerStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Worker, error) {
	row, err := s.conn.q.GetWorkerByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	return fromDBWorker(row), nil
}

func (s *workerStore) GetAdmin(ctx context.Context, id string) (*store.WorkerWithOwner, error) {
	row, err := s.conn.q.GetWorkerAdmin(ctx, id)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	out := workerWithOwner(row.Worker, row.OwnerUsername, row.OwnerDeleted)
	return &out, nil
}

func (s *workerStore) GetByAuthTokenHashIncludeDeleted(ctx context.Context, hash []byte) (*store.Worker, error) {
	row, err := s.conn.q.GetWorkerByAuthTokenHashIncludeDeleted(ctx, hash)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	return fromDBWorker(row), nil
}

func (s *workerStore) RotateAuthToken(ctx context.Context, p store.RotateWorkerAuthTokenParams) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	n, err := s.conn.q.RotateWorkerAuthToken(ctx, gendb.RotateWorkerAuthTokenParams{ID: p.ID, AuthTokenHash: p.AuthTokenHash, ExpectedAuthTokenHash: p.ExpectedAuthTokenHash})
	return n, s.conn.queryError(err)
}

func (s *workerStore) GetByRegistrationAttemptID(ctx context.Context, attemptID string) (*store.Worker, error) {
	row, err := s.conn.q.GetWorkerByRegistrationAttemptID(ctx, stringToNull(attemptID))
	if err != nil {
		return nil, s.conn.queryError(err)
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
	if !hasPrefix {
		return store.QueryPage(ctx, p.Limit,
			func() (gendb.ListWorkersByUserIDParams, error) {
				return listWorkersByUserIDParams(owner, p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersByUserID,
			func(r gendb.Worker) store.Worker { return *fromDBWorker(r) }, s.conn.queryError)
	}
	idGt, fetchLimit, empty, err := prepareIDPrefixPage(filter, p.Cursor, p.Limit)
	if err != nil || empty {
		return store.Page[store.Worker]{}, err
	}
	var rows []gendb.Worker
	if filter.Exact {
		rows, err = s.conn.q.ListWorkersByUserIDAndExactID(ctx, gendb.ListWorkersByUserIDAndExactIDParams{
			RegisteredBy: owner,
			ID:           filter.Lower,
		})
	} else {
		rows, err = s.conn.q.ListWorkersByUserIDAndIDPrefix(ctx, gendb.ListWorkersByUserIDAndIDPrefixParams{
			RegisteredBy: owner,
			IDLo:         filter.Lower,
			IDHi:         filter.Upper,
			IDGt:         idGt,
			Limit:        fetchLimit,
		})
	}
	if err != nil {
		return store.Page[store.Worker]{}, s.conn.queryError(err)
	}
	mapped := store.MapSlice(rows, func(r gendb.Worker) store.Worker { return *fromDBWorker(r) })
	return store.NewPage(mapped, p.Limit), nil
}

func (s *workerStore) InventorySnapshot(ctx context.Context, owner userid.UserID) (store.WorkerInventorySnapshot, error) {
	userID, ok := userid.OwnerFilter(owner)
	if !ok {
		return store.WorkerInventorySnapshot{}, store.ErrNotFound
	}
	rows, err := s.conn.q.GetWorkerInventorySnapshot(ctx, gendb.GetWorkerInventorySnapshotParams{UserID: userID})
	if err != nil {
		return store.WorkerInventorySnapshot{}, s.conn.queryError(err)
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
		var userID string
		if p.UserID != nil {
			uid, ok := userid.New(*p.UserID)
			if !ok {
				return store.Page[store.WorkerWithOwner]{}, nil
			}
			userID = uid.String()
		}
		var statusFilter int64
		switch status {
		case leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED:
			statusFilter = 0
		case leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE:
			statusFilter = 1
		case leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED:
			statusFilter = 2
		default:
			return store.Page[store.WorkerWithOwner]{}, store.ErrInvalidArgument
		}
		if filter.Exact {
			return store.QueryPage(ctx, p.Limit,
				func() (gendb.ListWorkersAdminByExactIDParams, error) {
					return gendb.ListWorkersAdminByExactIDParams{
						ID:           filter.Lower,
						UserID:       userID,
						StatusFilter: statusFilter,
					}, nil
				},
				s.conn.q.ListWorkersAdminByExactID,
				func(r gendb.ListWorkersAdminByExactIDRow) store.WorkerWithOwner {
					return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
				}, s.conn.queryError)
		}
		return store.QueryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminByIDPrefixParams, error) {
				return gendb.ListWorkersAdminByIDPrefixParams{
					IDLo:         filter.Lower,
					IDHi:         filter.Upper,
					IDGt:         idGt,
					UserID:       userID,
					StatusFilter: statusFilter,
					Limit:        fetchLimit,
				}, nil
			},
			s.conn.q.ListWorkersAdminByIDPrefix,
			func(r gendb.ListWorkersAdminByIDPrefixRow) store.WorkerWithOwner {
				return workerWithOwner(r.Worker, r.OwnerUsername, r.OwnerDeleted)
			}, s.conn.queryError)
	}
	switch status {
	case leapmuxv1.WorkerStatus_WORKER_STATUS_UNSPECIFIED, leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE:
		if p.UserID != nil {
			return store.QueryPage(ctx, p.Limit,
				func() (gendb.ListWorkersAdminLiveByUserParams, error) {
					return listWorkersAdminLiveByUserParams(*p.UserID, p.Cursor, p.Limit)
				},
				s.conn.q.ListWorkersAdminLiveByUser, fromDBListWorkersAdminLiveByUserRow, s.conn.queryError)
		}
		return store.QueryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminLiveParams, error) { return listWorkersAdminLiveParams(p.Cursor, p.Limit) },
			s.conn.q.ListWorkersAdminLive, fromDBListWorkersAdminLiveRow, s.conn.queryError)
	case leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED:
		if p.UserID != nil {
			return store.QueryPage(ctx, p.Limit,
				func() (gendb.ListWorkersAdminDeletedByUserParams, error) {
					return listWorkersAdminDeletedByUserParams(*p.UserID, p.Cursor, p.Limit)
				},
				s.conn.q.ListWorkersAdminDeletedByUser, fromDBListWorkersAdminDeletedByUserRow, s.conn.queryError)
		}
		return store.QueryPage(ctx, p.Limit,
			func() (gendb.ListWorkersAdminDeletedParams, error) {
				return listWorkersAdminDeletedParams(p.Cursor, p.Limit)
			},
			s.conn.q.ListWorkersAdminDeleted, fromDBListWorkersAdminDeletedRow, s.conn.queryError)
	default:
		return store.Page[store.WorkerWithOwner]{}, store.ErrInvalidArgument
	}
}

func (s *workerStore) UpdateLastSeen(ctx context.Context, id string) error {
	return s.conn.queryError(s.conn.q.UpdateWorkerLastSeen(ctx, id))
}

func (s *workerStore) Deregister(ctx context.Context, p store.DeregisterWorkerParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.RegisteredBy)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, nil
	}
	return s.conn.rowsAffected(s.conn.q.DeregisterWorker(ctx, gendb.DeregisterWorkerParams{
		ID:           p.ID,
		RegisteredBy: owner,
	}))
}

func (s *workerStore) ForceDeregister(ctx context.Context, id string) (int64, error) {
	return s.conn.rowsAffected(s.conn.q.ForceDeregisterWorker(ctx, id))
}

func (s *workerStore) MarkDeleted(ctx context.Context, id string) error {
	return s.conn.queryError(s.conn.q.MarkWorkerDeleted(ctx, id))
}

func (s *workerStore) MarkAllDeletedByUser(ctx context.Context, registeredBy userid.UserID) error {
	owner, ok := userid.OwnerFilter(registeredBy)
	if !ok {
		// Binding "" here would address every blank-registrant row for
		// deletion; reporting success having deleted nothing is no better.
		return store.ErrInvalidArgument
	}
	return s.conn.queryError(s.conn.q.MarkAllWorkersDeletedByUser(ctx, owner))
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
		RegistrationKeyHash:   nullToBytes(w.RegistrationKeyHash),
	}
}

// stringToNull binds an empty attempt identity as SQL NULL, the shape
// the schema CHECKs pair with a NULL digest.
func stringToNull(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// bytesToNull binds an absent digest as SQL NULL. The driver carries
// VARBINARY through the nullable string type.
func bytesToNull(b []byte) sql.NullString {
	if len(b) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: string(b), Valid: true}
}

// nullToBytes reads the digest back; an empty one stays empty.
func nullToBytes(ns sql.NullString) []byte {
	if !ns.Valid {
		return nil
	}
	return []byte(ns.String)
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
