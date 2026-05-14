package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type apiTokenStore struct{ conn *mysqlConn }

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
		LastUsedAt:               sqlutil.NullTimePtr(t.LastUsedAt),
		LastRotatedAt:            sqlutil.NullTimePtr(t.LastRotatedAt),
		ExpiresAt:                sqlutil.NullTimePtr(t.ExpiresAt),
		RefreshExpiresAt:         sqlutil.NullTimePtr(t.RefreshExpiresAt),
		RevokedAt:                sqlutil.NullTimePtr(t.RevokedAt),
	}
}

func (s *apiTokenStore) Create(ctx context.Context, p store.CreateAPITokenParams) error {
	return mapErr(s.conn.q.CreateAPIToken(ctx, gendb.CreateAPITokenParams{
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
	out := make([]store.APIToken, len(rows))
	for i, r := range rows {
		out[i] = fromDBAPIToken(r)
	}
	return out, nil
}

func (s *apiTokenStore) Touch(ctx context.Context, id string) error {
	return mapErr(s.conn.q.TouchAPIToken(ctx, id))
}

func (s *apiTokenStore) RotateRefresh(ctx context.Context, p store.RotateAPITokenRefreshParams) error {
	return mapErr(s.conn.q.RotateAPITokenRefresh(ctx, gendb.RotateAPITokenRefreshParams{
		ID:                   p.ID,
		NewSecretHash:        p.NewSecretHash,
		NewExpiresAt:         sqlutil.ToNullTime(p.NewExpiresAt),
		NewRefreshHash:       p.NewRefreshHash,
		NewRefreshExpiresAt:  sqlutil.ToNullTime(p.NewRefreshExpiresAt),
		PrevRefreshHash:      p.PreviousRefreshHash,
		PrevRefreshExpiresAt: sqlutil.ToNullTime(p.PreviousRefreshExpiresAt),
	}))
}

func (s *apiTokenStore) Revoke(ctx context.Context, id string) (int64, error) {
	return rowsAffected(s.conn.q.RevokeAPIToken(ctx, id))
}

func (s *apiTokenStore) RevokeByUser(ctx context.Context, userID string) (int64, error) {
	return rowsAffected(s.conn.q.RevokeAPITokensByUser(ctx, userID))
}

func (s *apiTokenStore) ListRevokedSince(ctx context.Context, since time.Time) ([]store.TokenRevocationRecord, error) {
	rows, err := s.conn.q.ListAPITokensRevokedSince(ctx, sql.NullTime{Time: since.UTC(), Valid: true})
	if err != nil {
		return nil, mapErr(err)
	}
	return sqlutil.MapRevocations(rows,
		func(r gendb.ListAPITokensRevokedSinceRow) string { return r.ID },
		func(r gendb.ListAPITokensRevokedSinceRow) string { return r.UserID },
		func(r gendb.ListAPITokensRevokedSinceRow) sql.NullTime { return r.RevokedAt },
	), nil
}

func (s *apiTokenStore) DeleteRevokedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteRevokedAPITokensBefore(ctx, sql.NullTime{Time: cutoff.UTC(), Valid: true}))
}

// MaxRevokedAt returns the latest api_tokens.revoked_at, or the zero
// time when the table holds no revoked rows. The watcher uses this
// at hub bootstrap to seed its high-water mark in O(log N) via the
// existing revoked_at index, instead of materializing every row.
func (s *apiTokenStore) MaxRevokedAt(ctx context.Context) (time.Time, error) {
	t, err := s.conn.q.MaxAPITokenRevokedAt(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, mapErr(err)
	}
	if !t.Valid {
		return time.Time{}, nil
	}
	return t.Time, nil
}
