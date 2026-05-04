package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
)

type registrationKeyStore struct {
	conn *mysqlConn
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

// Consume runs the SELECT FOR UPDATE + UPDATE pair in a transaction so
// concurrent callers cannot both observe the same live row. MySQL has no
// UPDATE ... RETURNING, so the row lock is doing the heavy lifting here.
func (s *registrationKeyStore) Consume(ctx context.Context, id string) (*store.WorkerRegistrationKey, error) {
	tx, err := s.conn.shared.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = tx.Rollback() }()

	q := s.conn.q.WithTx(tx)
	now := time.Now().UTC()
	r, err := q.GetActiveRegistrationKeyForUpdate(ctx, gendb.GetActiveRegistrationKeyForUpdateParams{
		ID:        id,
		ExpiresAt: now,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, mapErr(err)
	}
	// Internal soft-delete: ownership is already enforced by the row
	// lock above, so this UPDATE skips the created_by filter that the
	// user-facing SoftDelete query carries.
	if err := q.ConsumeSoftDeleteRegistrationKey(ctx, gendb.ConsumeSoftDeleteRegistrationKeyParams{
		ID:        id,
		ExpiresAt: now.Add(store.RegistrationKeySoftDeleteOffset),
	}); err != nil {
		return nil, mapErr(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, mapErr(err)
	}
	return fromDBRegistrationKey(r), nil
}
