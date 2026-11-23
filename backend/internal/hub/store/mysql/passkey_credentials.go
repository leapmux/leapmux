package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type passkeyCredentialStore struct {
	conn *mysqlConn
}

var _ store.PasskeyCredentialStore = (*passkeyCredentialStore)(nil)

func (c *mysqlConn) fromDBPasskeyCredential(r gendb.PasskeyCredential) (store.PasskeyCredential, error) {
	publicKey, err := c.ks.OpenBytes(r.PublicKey, keystore.PasskeyPublicKeyAAD(r.ID))
	if err != nil {
		return store.PasskeyCredential{}, err
	}
	aaguid, err := c.ks.OpenOptionalBytes(r.Aaguid, keystore.PasskeyAAGUIDAAD(r.ID))
	if err != nil {
		return store.PasskeyCredential{}, err
	}
	transports, err := c.ks.OpenString(r.Transports, keystore.PasskeyTransportsAAD(r.ID))
	if err != nil {
		return store.PasskeyCredential{}, err
	}
	friendlyName, err := c.ks.OpenString(r.FriendlyName, keystore.PasskeyFriendlyNameAAD(r.ID))
	if err != nil {
		return store.PasskeyCredential{}, err
	}
	return store.PasskeyCredential{
		ID:             r.ID,
		UserID:         r.UserID,
		CredentialID:   r.CredentialID,
		PublicKey:      publicKey,
		SignCount:      r.SignCount,
		AAGUID:         aaguid,
		BackupEligible: r.BackupEligible,
		BackupState:    r.BackupState,
		Transports:     transports,
		FriendlyName:   friendlyName,
		CreatedAt:      r.CreatedAt.Time,
		LastUsedAt:     r.LastUsedAt.Ptr(),
	}, nil
}

func (s *passkeyCredentialStore) Create(ctx context.Context, p store.CreatePasskeyCredentialParams) error {
	owner, ok := userid.New(p.UserID)
	if !ok {
		return store.ErrInvalidArgument
	}
	publicKey, err := s.conn.ks.SealBytes(p.PublicKey, keystore.PasskeyPublicKeyAAD(p.ID))
	if err != nil {
		return err
	}
	aaguid, err := s.conn.ks.SealNonEmptyBytes(p.AAGUID, keystore.PasskeyAAGUIDAAD(p.ID))
	if err != nil {
		return err
	}
	transports, err := s.conn.ks.SealString(p.Transports, keystore.PasskeyTransportsAAD(p.ID))
	if err != nil {
		return err
	}
	friendlyName, err := s.conn.ks.SealString(p.FriendlyName, keystore.PasskeyFriendlyNameAAD(p.ID))
	if err != nil {
		return err
	}
	return s.conn.queryError(s.conn.q.CreatePasskeyCredential(ctx, gendb.CreatePasskeyCredentialParams{
		ID:             p.ID,
		UserID:         owner.String(),
		CredentialID:   p.CredentialID,
		PublicKey:      publicKey,
		SignCount:      p.SignCount,
		Aaguid:         aaguid,
		BackupEligible: p.BackupEligible,
		BackupState:    p.BackupState,
		Transports:     transports,
		FriendlyName:   friendlyName,
		CreatedAt:      sqltime.NewMySQLTime(p.CreatedAt),
		LastUsedAt:     sqltime.NewMySQLNullTime(p.LastUsedAt),
	}))
}

func (s *passkeyCredentialStore) GetByID(ctx context.Context, id string) (*store.PasskeyCredential, error) {
	row, err := s.conn.q.GetPasskeyCredentialByID(ctx, id)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	out, err := s.conn.fromDBPasskeyCredential(row)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *passkeyCredentialStore) GetByCredentialID(ctx context.Context, credentialID []byte) (*store.PasskeyCredential, error) {
	row, err := s.conn.q.GetPasskeyCredentialByCredentialID(ctx, credentialID)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	out, err := s.conn.fromDBPasskeyCredential(row)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *passkeyCredentialStore) ListByUser(ctx context.Context, userID string) ([]store.PasskeyCredential, error) {
	owner, ok := userid.New(userID)
	if !ok {
		return nil, store.ErrInvalidArgument
	}
	rows, err := s.conn.q.ListPasskeyCredentialsByUser(ctx, owner.String())
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	return store.MapSliceErr(rows, s.conn.fromDBPasskeyCredential)
}

func (s *passkeyCredentialStore) CountByUser(ctx context.Context, userID string) (int64, error) {
	owner, ok := userid.New(userID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	count, err := s.conn.q.CountPasskeyCredentialsByUser(ctx, owner.String())
	if err != nil {
		return 0, s.conn.queryError(err)
	}
	return count, nil
}

func (s *passkeyCredentialStore) UpdateSignCount(ctx context.Context, p store.UpdatePasskeySignCountParams) error {
	owner, ok := userid.New(p.UserID)
	if !ok {
		return store.ErrInvalidArgument
	}
	n, err := s.conn.rowsAffected(s.conn.q.UpdatePasskeySignCount(ctx, gendb.UpdatePasskeySignCountParams{
		SignCount:    p.SignCount,
		LastUsedAt:   sqltime.MySQLNullTimeOf(p.LastUsedAt),
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
	sealedName, err := s.conn.ks.SealString(friendlyName, keystore.PasskeyFriendlyNameAAD(id))
	if err != nil {
		return err
	}
	return s.conn.queryError(s.conn.q.UpdatePasskeyFriendlyName(ctx, gendb.UpdatePasskeyFriendlyNameParams{
		FriendlyName: sealedName,
		ID:           id,
		UserID:       owner.String(),
	}))
}

func (s *passkeyCredentialStore) Delete(ctx context.Context, id, userID string) error {
	owner, ok := userid.New(userID)
	if !ok {
		return store.ErrInvalidArgument
	}
	return s.conn.queryError(s.conn.q.DeletePasskeyCredential(ctx, gendb.DeletePasskeyCredentialParams{
		ID:     id,
		UserID: owner.String(),
	}))
}

func (s *passkeyCredentialStore) DeleteAllByUser(ctx context.Context, userID string) error {
	owner, ok := userid.New(userID)
	if !ok {
		return store.ErrInvalidArgument
	}
	return s.conn.queryError(s.conn.q.DeleteAllPasskeyCredentialsByUser(ctx, owner.String()))
}
