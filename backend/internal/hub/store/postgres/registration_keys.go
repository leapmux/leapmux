package postgres

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
)

type registrationKeyStore struct {
	conn *pgConn
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
		CreatedBy: p.CreatedBy.String(),
		ExpiresAt: pgtime.New(p.ExpiresAt),
	}))
}

func (s *registrationKeyStore) GetByID(ctx context.Context, id string) (*store.WorkerRegistrationKey, error) {
	r, err := s.conn.q.GetRegistrationKeyByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBRegistrationKey(r), nil
}

func (s *registrationKeyStore) GetOwned(ctx context.Context, p store.GetOwnedRegistrationKeyParams) (*store.WorkerRegistrationKey, error) {
	owner, ok := store.OwnerFilter(p.CreatedBy)
	if !ok {
		// A blank bind parameter would MATCH a blank created_by column
		// rather than fail to match, so an unminted caller must be refused
		// before the query, not by it.
		return nil, store.ErrNotFound
	}
	r, err := s.conn.q.GetOwnedRegistrationKey(ctx, gendb.GetOwnedRegistrationKeyParams{
		ID:        p.ID,
		CreatedBy: owner,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBRegistrationKey(r), nil
}

func (s *registrationKeyStore) Extend(ctx context.Context, p store.ExtendRegistrationKeyParams) (int64, error) {
	owner, ok := store.OwnerFilter(p.CreatedBy)
	if !ok {
		return 0, nil // an unminted caller owns nothing; see OwnerFilter
	}
	return rowsAffected(s.conn.q.ExtendRegistrationKey(ctx, gendb.ExtendRegistrationKeyParams{
		ID:           p.ID,
		CreatedBy:    owner,
		NewExpiresAt: pgtime.New(p.ExpiresAt),
		Now:          pgtime.New(time.Now()),
	}))
}

func (s *registrationKeyStore) SoftDelete(ctx context.Context, p store.SoftDeleteRegistrationKeyParams) (int64, error) {
	owner, ok := store.OwnerFilter(p.CreatedBy)
	if !ok {
		return 0, nil // an unminted caller owns nothing; see OwnerFilter
	}
	return rowsAffected(s.conn.q.SoftDeleteRegistrationKey(ctx, gendb.SoftDeleteRegistrationKeyParams{
		ID:        p.ID,
		CreatedBy: owner,
		ExpiresAt: pgtime.New(time.Now().Add(store.RegistrationKeySoftDeleteOffset)),
	}))
}

func (s *registrationKeyStore) Consume(ctx context.Context, id string) (*store.WorkerRegistrationKey, error) {
	now := time.Now()
	r, err := s.conn.q.ConsumeRegistrationKey(ctx, gendb.ConsumeRegistrationKeyParams{
		ID:            id,
		Now:           pgtime.New(now),
		SoftDeletedAt: pgtime.New(now.Add(store.RegistrationKeySoftDeleteOffset)),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBRegistrationKey(r), nil
}

func (s *registrationKeyStore) AdminSoftDelete(ctx context.Context, id string) (int64, error) {
	return rowsAffected(s.conn.q.AdminSoftDeleteRegistrationKey(ctx, gendb.AdminSoftDeleteRegistrationKeyParams{
		ID:        id,
		ExpiresAt: pgtime.New(time.Now().Add(store.RegistrationKeySoftDeleteOffset)),
	}))
}

func (s *registrationKeyStore) ListAdmin(ctx context.Context, p store.ListRegistrationKeysAdminParams) (store.Page[store.WorkerRegistrationKeyWithCreator], error) {
	// Leave Valid=false on the pgtime.NullTime value so the
	// `($N::timestamptz IS NULL OR …)` short-circuits keep every row /
	// start from the head.
	var nowTs pgtime.NullTime
	if !p.IncludeExpired {
		nowTs = pgtime.NullOf(time.Now())
	}
	return queryPage(ctx, p.Limit,
		func() (gendb.ListRegistrationKeysAdminParams, error) {
			return listRegistrationKeysAdminParams(p.Cursor, p.Limit, nowTs)
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
