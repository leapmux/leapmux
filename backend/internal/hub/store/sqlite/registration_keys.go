package sqlite

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
)

type registrationKeyStore struct {
	conn *sqliteConn
}

var _ store.RegistrationKeyStore = (*registrationKeyStore)(nil)

func fromDBRegistrationKey(r gendb.WorkerRegistrationKey) *store.WorkerRegistrationKey {
	return &store.WorkerRegistrationKey{
		ID:        r.ID,
		CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt,
		ExpiresAt: r.ExpiresAt,
	}
}

func (s *registrationKeyStore) Create(ctx context.Context, p store.CreateRegistrationKeyParams) error {
	return mapErr(s.conn.q.CreateRegistrationKey(ctx, gendb.CreateRegistrationKeyParams{
		ID:        p.ID,
		CreatedBy: p.CreatedBy,
		ExpiresAt: p.ExpiresAt.UTC(),
	}))
}

func (s *registrationKeyStore) GetByID(ctx context.Context, id string) (*store.WorkerRegistrationKey, error) {
	r, err := s.conn.q.GetRegistrationKeyByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBRegistrationKey(r), nil
}

func (s *registrationKeyStore) GetOwned(ctx context.Context, id, createdBy string) (*store.WorkerRegistrationKey, error) {
	r, err := s.conn.q.GetOwnedRegistrationKey(ctx, gendb.GetOwnedRegistrationKeyParams{
		ID:        id,
		CreatedBy: createdBy,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBRegistrationKey(r), nil
}

func (s *registrationKeyStore) Extend(ctx context.Context, p store.ExtendRegistrationKeyParams) (int64, error) {
	return rowsAffected(s.conn.q.ExtendRegistrationKey(ctx, gendb.ExtendRegistrationKeyParams{
		ID:           p.ID,
		CreatedBy:    p.CreatedBy,
		NewExpiresAt: p.ExpiresAt.UTC(),
		Now:          time.Now().UTC(),
	}))
}

func (s *registrationKeyStore) SoftDelete(ctx context.Context, p store.SoftDeleteRegistrationKeyParams) (int64, error) {
	return rowsAffected(s.conn.q.SoftDeleteRegistrationKey(ctx, gendb.SoftDeleteRegistrationKeyParams{
		ID:        p.ID,
		CreatedBy: p.CreatedBy,
		ExpiresAt: time.Now().UTC().Add(store.RegistrationKeySoftDeleteOffset),
	}))
}

func (s *registrationKeyStore) Consume(ctx context.Context, id string) (*store.WorkerRegistrationKey, error) {
	now := time.Now().UTC()
	r, err := s.conn.q.ConsumeRegistrationKey(ctx, gendb.ConsumeRegistrationKeyParams{
		ID:            id,
		Now:           now,
		SoftDeletedAt: now.Add(store.RegistrationKeySoftDeleteOffset),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBRegistrationKey(r), nil
}

func (s *registrationKeyStore) AdminSoftDelete(ctx context.Context, id string) (int64, error) {
	return rowsAffected(s.conn.q.AdminSoftDeleteRegistrationKey(ctx, gendb.AdminSoftDeleteRegistrationKeyParams{
		ID:        id,
		ExpiresAt: time.Now().UTC().Add(store.RegistrationKeySoftDeleteOffset),
	}))
}

func (s *registrationKeyStore) ListAdmin(ctx context.Context, p store.ListRegistrationKeysAdminParams) (store.Page[store.WorkerRegistrationKeyWithCreator], error) {
	// `now` is bound as a time.Time and compared against expires_at through a
	// strftime('%Y-%m-%dT%H:%M:%fZ', sqlc.narg(now)) wrap in the SQL itself (see
	// worker_registration_keys.sql ListRegistrationKeysAdmin), so the comparison
	// is canonical-layout against canonical-layout -- expires_at is written
	// canonical by every Create/Extend/SoftDelete/Consume/AdminSoftDelete path.
	// The cursor timestamp is compared against created_at (set via SQL DEFAULT
	// strftime to ms precision), so the cursor must be formatted via decodeCursorParams
	// to match that format. The id half of the composite cursor is the
	// deterministic tiebreaker for rows sharing a millisecond.
	var nowArg any
	if !p.IncludeExpired {
		nowArg = time.Now().UTC()
	}
	return queryPage(ctx, p.Limit,
		func() (gendb.ListRegistrationKeysAdminParams, error) {
			return listRegistrationKeysAdminParams(p.Cursor, p.Limit, nowArg)
		},
		s.conn.q.ListRegistrationKeysAdmin,
		fromDBListRegistrationKeysAdminRow)
}

func fromDBListRegistrationKeysAdminRow(r gendb.ListRegistrationKeysAdminRow) store.WorkerRegistrationKeyWithCreator {
	return store.WorkerRegistrationKeyWithCreator{
		WorkerRegistrationKey: store.WorkerRegistrationKey{
			ID:        r.ID,
			CreatedBy: r.CreatedBy,
			CreatedAt: r.CreatedAt,
			ExpiresAt: r.ExpiresAt,
		},
		CreatorUsername: r.CreatorUsername,
		CreatorDeleted:  r.CreatorDeleted,
	}
}
