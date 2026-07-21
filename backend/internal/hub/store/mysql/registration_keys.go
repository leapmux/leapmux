package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type registrationKeyStore struct {
	conn *mysqlConn
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
		ExpiresAt: sqltime.NewMySQLTime(p.ExpiresAt),
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
		NewExpiresAt: sqltime.NewMySQLTime(p.ExpiresAt),
		Now:          sqltime.NewMySQLTime(time.Now()),
	}))
}

func (s *registrationKeyStore) SoftDelete(ctx context.Context, p store.SoftDeleteRegistrationKeyParams) (int64, error) {
	return rowsAffected(s.conn.q.SoftDeleteRegistrationKey(ctx, gendb.SoftDeleteRegistrationKeyParams{
		ID:        p.ID,
		CreatedBy: p.CreatedBy,
		ExpiresAt: sqltime.NewMySQLTime(time.Now().Add(store.RegistrationKeySoftDeleteOffset)),
	}))
}

// Consume runs the SELECT FOR UPDATE + UPDATE pair in a transaction so
// concurrent callers cannot both observe the same live row. MySQL has no
// UPDATE ... RETURNING, so the row lock is doing the heavy lifting here.
func (s *registrationKeyStore) Consume(ctx context.Context, id string) (*store.WorkerRegistrationKey, error) {
	var consumed *store.WorkerRegistrationKey
	err := s.conn.withTransaction(ctx, func(conn *mysqlConn) error {
		now := sqltime.NewMySQLTime(time.Now())
		r, err := conn.q.GetActiveRegistrationKeyForUpdate(ctx, gendb.GetActiveRegistrationKeyForUpdateParams{
			ID:        id,
			ExpiresAt: now,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return store.ErrNotFound
			}
			return mapErr(err)
		}
		// Internal soft-delete: ownership is already enforced by the row
		// lock above, so this UPDATE skips the created_by filter that the
		// user-facing SoftDelete query carries.
		if err := conn.q.ConsumeSoftDeleteRegistrationKey(ctx, gendb.ConsumeSoftDeleteRegistrationKeyParams{
			ID:        id,
			ExpiresAt: sqltime.NewMySQLTime(now.Add(store.RegistrationKeySoftDeleteOffset)),
		}); err != nil {
			return mapErr(err)
		}
		consumed = fromDBRegistrationKey(r)
		return nil
	})
	return consumed, err
}

func (s *registrationKeyStore) AdminSoftDelete(ctx context.Context, id string) (int64, error) {
	return rowsAffected(s.conn.q.AdminSoftDeleteRegistrationKey(ctx, gendb.AdminSoftDeleteRegistrationKeyParams{
		ID:        id,
		ExpiresAt: sqltime.NewMySQLTime(time.Now().Add(store.RegistrationKeySoftDeleteOffset)),
	}))
}

func (s *registrationKeyStore) ListAdmin(ctx context.Context, p store.ListRegistrationKeysAdminParams) (store.Page[store.WorkerRegistrationKeyWithCreator], error) {
	// now is the expiry probe: a real instant when active-only (IncludeExpired=false)
	// so the `(narg(now) IS NULL OR expires_at > narg(now))` predicate's second
	// branch filters live rows; a zero (invalid) NullTime when IncludeExpired=true
	// so the IS NULL branch surfaces every row.
	var now sqltime.MySQLNullTime
	if !p.IncludeExpired {
		now = sqltime.MySQLNullTimeOf(time.Now())
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
