package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type passkeyCredentialStore struct {
	conn *sqliteConn
}

var _ store.PasskeyCredentialStore = (*passkeyCredentialStore)(nil)

func fromDBPasskeyCredential(c gendb.PasskeyCredential) store.PasskeyCredential {
	return store.PasskeyCredential{
		ID:             c.ID,
		UserID:         c.UserID,
		CredentialID:   c.CredentialID,
		PublicKey:      c.PublicKey,
		SignCount:      c.SignCount,
		AAGUID:         c.Aaguid,
		BackupEligible: ptrconv.Int64ToBool(c.BackupEligible),
		BackupState:    ptrconv.Int64ToBool(c.BackupState),
		Transports:     c.Transports,
		FriendlyName:   c.FriendlyName,
		KeyVersion:     c.KeyVersion,
		CreatedAt:      c.CreatedAt.Time,
		LastUsedAt:     c.LastUsedAt.Ptr(),
	}
}

func fromDBPasskeyCredentials(rows []gendb.PasskeyCredential) []store.PasskeyCredential {
	return store.MapSlice(rows, fromDBPasskeyCredential)
}

func (s *passkeyCredentialStore) Create(ctx context.Context, p store.CreatePasskeyCredentialParams) error {
	owner, ok := userid.New(p.UserID)
	if !ok {
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.CreatePasskeyCredential(ctx, gendb.CreatePasskeyCredentialParams{
		ID:             p.ID,
		UserID:         owner.String(),
		CredentialID:   p.CredentialID,
		PublicKey:      p.PublicKey,
		SignCount:      p.SignCount,
		Aaguid:         p.AAGUID,
		BackupEligible: ptrconv.BoolToInt64(p.BackupEligible),
		BackupState:    ptrconv.BoolToInt64(p.BackupState),
		Transports:     p.Transports,
		FriendlyName:   p.FriendlyName,
		KeyVersion:     p.KeyVersion,
		CreatedAt:      sqltime.NewSQLiteTime(p.CreatedAt),
		LastUsedAt:     sqltime.NewSQLiteNullTime(p.LastUsedAt),
	}))
}

func (s *passkeyCredentialStore) GetByID(ctx context.Context, id string) (*store.PasskeyCredential, error) {
	row, err := s.conn.q.GetPasskeyCredentialByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBPasskeyCredential(row)
	return &out, nil
}

func (s *passkeyCredentialStore) GetByCredentialID(ctx context.Context, credentialID []byte) (*store.PasskeyCredential, error) {
	row, err := s.conn.q.GetPasskeyCredentialByCredentialID(ctx, credentialID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBPasskeyCredential(row)
	return &out, nil
}

func (s *passkeyCredentialStore) ListByUser(ctx context.Context, userID string) ([]store.PasskeyCredential, error) {
	owner, ok := userid.New(userID)
	if !ok {
		return nil, store.ErrInvalidArgument
	}
	rows, err := s.conn.q.ListPasskeyCredentialsByUser(ctx, owner.String())
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBPasskeyCredentials(rows), nil
}

func (s *passkeyCredentialStore) CountByUser(ctx context.Context, userID string) (int64, error) {
	owner, ok := userid.New(userID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	count, err := s.conn.q.CountPasskeyCredentialsByUser(ctx, owner.String())
	if err != nil {
		return 0, mapErr(err)
	}
	return count, nil
}

func (s *passkeyCredentialStore) UpdateSignCount(ctx context.Context, p store.UpdatePasskeySignCountParams) error {
	owner, ok := userid.New(p.UserID)
	if !ok {
		return store.ErrInvalidArgument
	}
	n, err := rowsAffected(s.conn.q.UpdatePasskeySignCount(ctx, gendb.UpdatePasskeySignCountParams{
		SignCount:    p.SignCount,
		LastUsedAt:   sqltime.SQLiteNullTimeOf(p.LastUsedAt),
		CredentialID: p.CredentialID,
		UserID:       owner.String(),
	}))
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *passkeyCredentialStore) UpdateFriendlyName(ctx context.Context, id, userID, friendlyName string) error {
	owner, ok := userid.New(userID)
	if !ok {
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.UpdatePasskeyFriendlyName(ctx, gendb.UpdatePasskeyFriendlyNameParams{
		FriendlyName: friendlyName,
		ID:           id,
		UserID:       owner.String(),
	}))
}

func (s *passkeyCredentialStore) UpdatePublicKey(ctx context.Context, p store.UpdatePasskeyPublicKeyParams) error {
	owner, ok := userid.New(p.UserID)
	if !ok {
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.UpdatePasskeyPublicKey(ctx, gendb.UpdatePasskeyPublicKeyParams{
		PublicKey:  p.PublicKey,
		KeyVersion: p.KeyVersion,
		ID:         p.ID,
		UserID:     owner.String(),
	}))
}

func (s *passkeyCredentialStore) Delete(ctx context.Context, id, userID string) error {
	owner, ok := userid.New(userID)
	if !ok {
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeletePasskeyCredential(ctx, gendb.DeletePasskeyCredentialParams{
		ID:     id,
		UserID: owner.String(),
	}))
}

func (s *passkeyCredentialStore) DeleteAllByUser(ctx context.Context, userID string) error {
	owner, ok := userid.New(userID)
	if !ok {
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteAllPasskeyCredentialsByUser(ctx, owner.String()))
}

func (s *passkeyCredentialStore) ListByKeyVersion(ctx context.Context, keyVersion int64) ([]store.PasskeyCredential, error) {
	rows, err := s.conn.q.ListPasskeyCredentialsByKeyVersion(ctx, keyVersion)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBPasskeyCredentials(rows), nil
}

func (s *passkeyCredentialStore) CountByKeyVersion(ctx context.Context, keyVersion int64) (int64, error) {
	count, err := s.conn.q.CountPasskeyCredentialsByKeyVersion(ctx, keyVersion)
	if err != nil {
		return 0, mapErr(err)
	}
	return count, nil
}
