package sqlite

import (
	"context"
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type apiTokenStore struct{ conn *sqliteConn }

var _ store.APITokenStore = (*apiTokenStore)(nil)

func fromDBAPIToken(t gendb.ApiToken) store.APIToken {
	return store.APIToken{
		ID:                       t.ID,
		UserID:                   t.UserID,
		ClientType:               t.ClientType,
		ClientName:               t.ClientName,
		SecretHash:               t.SecretHash,
		RefreshHash:              t.RefreshHash,
		PreviousRefreshHash:      t.PreviousRefreshHash,
		PreviousRefreshExpiresAt: sqlutil.NullTimePtr(t.PreviousRefreshExpiresAt),
		Scope:                    t.Scope,
		CreatedAt:                t.CreatedAt,
		AuthGeneration:           t.AuthGeneration,
		LastUsedAt:               sqlutil.NullTimePtr(t.LastUsedAt),
		LastRotatedAt:            sqlutil.NullTimePtr(t.LastRotatedAt),
		ExpiresAt:                sqlutil.NullTimePtr(t.ExpiresAt),
		RefreshExpiresAt:         sqlutil.NullTimePtr(t.RefreshExpiresAt),
		RevokedAt:                sqlutil.NullTimePtr(t.RevokedAt),
	}
}

func (s *apiTokenStore) Create(ctx context.Context, p store.CreateAPITokenParams) error {
	return (&sqliteStore{conn: s.conn}).RunInUserAuthTransaction(ctx, p.UserID, func(tx store.Store) error {
		return mapErr(tx.(*sqliteStore).conn.q.CreateAPIToken(ctx, gendb.CreateAPITokenParams{
			ID:               p.ID,
			UserID:           p.UserID,
			ClientType:       p.ClientType,
			ClientName:       p.ClientName,
			SecretHash:       p.SecretHash,
			RefreshHash:      p.RefreshHash,
			Scope:            p.Scope,
			ExpiresAt:        sqlutil.ToNullTime(p.ExpiresAt),
			RefreshExpiresAt: sqlutil.ToNullTime(p.RefreshExpiresAt),
		}))
	})
}

func (s *apiTokenStore) GetByID(ctx context.Context, id string) (*store.APIToken, error) {
	t, err := s.conn.q.GetAPITokenByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBAPIToken(t)
	return &out, nil
}

func (s *apiTokenStore) ListByUser(ctx context.Context, p store.ListAPITokensByUserParams) ([]store.APIToken, error) {
	rows, err := s.conn.q.ListAPITokensByUser(ctx, gendb.ListAPITokensByUserParams{
		UserID:     p.UserID,
		ClientType: p.ClientType,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBAPIToken), nil
}

func (s *apiTokenStore) Touch(ctx context.Context, id string) error {
	return mapErr(s.conn.q.TouchAPIToken(ctx, id))
}

func (s *apiTokenStore) RotateRefresh(ctx context.Context, p store.RotateAPITokenRefreshParams) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		n, err := rowsAffected(conn.q.RotateAPITokenRefresh(ctx, gendb.RotateAPITokenRefreshParams{
			ID:                   p.ID,
			NewSecretHash:        p.NewSecretHash,
			NewExpiresAt:         sqlutil.ToNullTime(p.NewExpiresAt),
			NewRefreshHash:       p.NewRefreshHash,
			NewRefreshExpiresAt:  sqlutil.ToNullTime(p.NewRefreshExpiresAt),
			PrevRefreshHash:      p.PreviousRefreshHash,
			PrevRefreshExpiresAt: sqlutil.ToNullTime(p.PreviousRefreshExpiresAt),
		}))
		if err != nil || n == 0 {
			return nil, err
		}
		if n != 1 {
			return nil, fmt.Errorf("rotate API token %q: updated %d rows", p.ID, n)
		}
		row, err := conn.q.GetAPITokenByID(ctx, p.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		rotatedAt, err := sqlutil.RequireTime(row.LastRotatedAt.Time, row.LastRotatedAt.Valid, "last_rotated_at")
		if err != nil {
			return nil, err
		}
		return &store.CredentialEvent{Kind: store.RevocationEventKindAPITokenRotation, SubjectID: row.ID, UserID: row.UserID, At: rotatedAt}, nil
	}, emitCredentialEvent)
}

func (s *apiTokenStore) Revoke(ctx context.Context, id string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		row, err := conn.q.RevokeAPIToken(ctx, id)
		return revokedCredentialEvent(row.ID, row.UserID, row.RevokedAt, store.RevocationEventKindAPIToken, err)
	}, emitCredentialEvent)
}

func (s *apiTokenStore) RevokeByUser(ctx context.Context, userID string) (int64, error) {
	return rowsAffected(s.conn.q.RevokeAPITokensByUserFast(ctx, userID))
}
