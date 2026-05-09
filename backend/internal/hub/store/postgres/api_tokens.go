package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type apiTokenStore struct{ conn *pgConn }

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
		PreviousRefreshExpiresAt: tsToTimePtr(t.PreviousRefreshExpiresAt),
		Scope:                    t.Scope,
		CreatedAt:                tsToTime(t.CreatedAt),
		LastUsedAt:               tsToTimePtr(t.LastUsedAt),
		LastRotatedAt:            tsToTimePtr(t.LastRotatedAt),
		ExpiresAt:                tsToTimePtr(t.ExpiresAt),
		RefreshExpiresAt:         tsToTimePtr(t.RefreshExpiresAt),
		RevokedAt:                tsToTimePtr(t.RevokedAt),
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
		ExpiresAt:        timePtrToTs(p.ExpiresAt),
		RefreshExpiresAt: timePtrToTs(p.RefreshExpiresAt),
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
		NewExpiresAt:         timePtrToTs(p.NewExpiresAt),
		NewRefreshHash:       p.NewRefreshHash,
		NewRefreshExpiresAt:  timePtrToTs(p.NewRefreshExpiresAt),
		PrevRefreshHash:      p.PreviousRefreshHash,
		PrevRefreshExpiresAt: timePtrToTs(p.PreviousRefreshExpiresAt),
	}))
}

func (s *apiTokenStore) Revoke(ctx context.Context, id string) (int64, error) {
	return s.conn.q.RevokeAPIToken(ctx, id)
}

func (s *apiTokenStore) RevokeByUser(ctx context.Context, userID string) (int64, error) {
	return s.conn.q.RevokeAPITokensByUser(ctx, userID)
}

func (s *apiTokenStore) ListRevokedSince(ctx context.Context, since time.Time) ([]store.TokenRevocationRecord, error) {
	rows, err := s.conn.q.ListAPITokensRevokedSince(ctx, timeToTs(since))
	if err != nil {
		return nil, mapErr(err)
	}
	return mapRevocations(rows,
		func(r gendb.ListAPITokensRevokedSinceRow) string { return r.ID },
		func(r gendb.ListAPITokensRevokedSinceRow) string { return r.UserID },
		func(r gendb.ListAPITokensRevokedSinceRow) pgtype.Timestamptz { return r.RevokedAt },
	), nil
}

func (s *apiTokenStore) DeleteRevokedBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.conn.q.DeleteRevokedAPITokensBefore(ctx, timeToTs(cutoff))
}

// MaxRevokedAt returns the latest api_tokens.revoked_at, or the zero
// time when the table holds no revoked rows. The watcher uses this
// at hub bootstrap to seed its high-water mark in O(log N) via the
// existing revoked_at index, instead of materializing every row.
func (s *apiTokenStore) MaxRevokedAt(ctx context.Context) (time.Time, error) {
	ts, err := s.conn.q.MaxAPITokenRevokedAt(ctx)
	if err != nil {
		mapped := mapErr(err)
		if errors.Is(mapped, store.ErrNotFound) {
			return time.Time{}, nil
		}
		return time.Time{}, mapped
	}
	if t := tsToTimePtr(ts); t != nil {
		return *t, nil
	}
	return time.Time{}, nil
}
