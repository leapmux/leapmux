package sqlite

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type registrationKeyStore struct {
	conn *sqliteConn
}

var _ store.RegistrationKeyStore = (*registrationKeyStore)(nil)

func fromDBRegistrationKey(r gendb.WorkerRegistrationKey) *store.WorkerRegistrationKey {
	return &store.WorkerRegistrationKey{
		ID:        r.ID,
		CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt.Time,
		ExpiresAt: r.ExpiresAt.Time,
	}
}

func (s *registrationKeyStore) Create(ctx context.Context, p store.CreateRegistrationKeyParams) error {
	return mapErr(s.conn.q.CreateRegistrationKey(ctx, gendb.CreateRegistrationKeyParams{
		ID:        p.ID,
		CreatedBy: p.CreatedBy,
		ExpiresAt: sqltime.NewSQLiteTime(p.ExpiresAt),
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
		NewExpiresAt: sqltime.NewSQLiteTime(p.ExpiresAt),
		Now:          sqltime.NewSQLiteTime(time.Now()),
	}))
}

func (s *registrationKeyStore) SoftDelete(ctx context.Context, p store.SoftDeleteRegistrationKeyParams) (int64, error) {
	return rowsAffected(s.conn.q.SoftDeleteRegistrationKey(ctx, gendb.SoftDeleteRegistrationKeyParams{
		ID:        p.ID,
		CreatedBy: p.CreatedBy,
		ExpiresAt: sqltime.NewSQLiteTime(time.Now().Add(store.RegistrationKeySoftDeleteOffset)),
	}))
}

func (s *registrationKeyStore) Consume(ctx context.Context, id string) (*store.WorkerRegistrationKey, error) {
	now := time.Now()
	r, err := s.conn.q.ConsumeRegistrationKey(ctx, gendb.ConsumeRegistrationKeyParams{
		ID:            id,
		Now:           sqltime.NewSQLiteTime(now),
		SoftDeletedAt: sqltime.NewSQLiteTime(now.Add(store.RegistrationKeySoftDeleteOffset)),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBRegistrationKey(r), nil
}

func (s *registrationKeyStore) AdminSoftDelete(ctx context.Context, id string) (int64, error) {
	return rowsAffected(s.conn.q.AdminSoftDeleteRegistrationKey(ctx, gendb.AdminSoftDeleteRegistrationKeyParams{
		ID:        id,
		ExpiresAt: sqltime.NewSQLiteTime(time.Now().Add(store.RegistrationKeySoftDeleteOffset)),
	}))
}

func (s *registrationKeyStore) ListAdmin(ctx context.Context, p store.ListRegistrationKeysAdminParams) (store.Page[store.WorkerRegistrationKeyWithCreator], error) {
	// `now` is compared against expires_at through sqlc.narg(now) in the SQL (see
	// worker_registration_keys.sql ListRegistrationKeysAdmin). The GENERATED Now
	// field stays interface{} because the sqlite engine does not resolve the
	// `narg IS NULL OR col > narg` OR-chain to a column type, but the builder
	// takes a typed SQLiteNullTime (matching the mysql/postgres siblings) so a
	// raw time.Time cannot reach the bind; Value() emits the canonical layout,
	// so the comparison is canonical-layout against canonical-layout --
	// expires_at is written canonical by every Create/Extend/SoftDelete/Consume/
	// AdminSoftDelete path. An invalid (zero-value) SQLiteNullTime binds NULL,
	// selecting expired rows too. The cursor timestamp is
	// compared against created_at (set via SQL DEFAULT strftime to ms precision),
	// so the cursor is formatted via decodeCursorParams to match that format. The
	// id half of the composite cursor is the deterministic tiebreaker for rows
	// sharing a millisecond.
	var now sqltime.SQLiteNullTime
	if !p.IncludeExpired {
		now = sqltime.SQLiteNullTimeOf(time.Now())
	}
	return queryPage(ctx, p.Limit,
		func() (gendb.ListRegistrationKeysAdminParams, error) {
			return listRegistrationKeysAdminParams(p.Cursor, p.Limit, now)
		},
		s.conn.q.ListRegistrationKeysAdmin,
		fromDBListRegistrationKeysAdminRow)
}

func fromDBListRegistrationKeysAdminRow(r gendb.ListRegistrationKeysAdminRow) store.WorkerRegistrationKeyWithCreator {
	return store.WorkerRegistrationKeyWithCreator{
		WorkerRegistrationKey: store.WorkerRegistrationKey{
			ID:        r.ID,
			CreatedBy: r.CreatedBy,
			CreatedAt: r.CreatedAt.Time,
			ExpiresAt: r.ExpiresAt.Time,
		},
		CreatorUsername: r.CreatorUsername,
		CreatorDeleted:  r.CreatorDeleted,
	}
}
