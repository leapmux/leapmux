package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
)

type oauthTokenStore struct {
	conn *sqliteConn
}

var _ store.OAuthTokenStore = (*oauthTokenStore)(nil)

func fromDBOAuthToken(t gendb.OauthToken) store.OAuthToken {
	return store.OAuthToken{
		UserID:       t.UserID,
		ProviderID:   t.ProviderID,
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		TokenType:    t.TokenType,
		ExpiresAt:    t.ExpiresAt,
		KeyVersion:   t.KeyVersion,
		UpdatedAt:    t.UpdatedAt,
	}
}

func fromDBOAuthTokens(rows []gendb.OauthToken) []store.OAuthToken {
	return store.MapSlice(rows, fromDBOAuthToken)
}

func (s *oauthTokenStore) Upsert(ctx context.Context, p store.UpsertOAuthTokensParams) error {
	return mapErr(s.conn.q.UpsertOAuthTokens(ctx, gendb.UpsertOAuthTokensParams{
		UserID:       p.UserID,
		ProviderID:   p.ProviderID,
		AccessToken:  p.AccessToken,
		RefreshToken: p.RefreshToken,
		TokenType:    p.TokenType,
		ExpiresAt:    p.ExpiresAt.UTC(),
		KeyVersion:   p.KeyVersion,
	}))
}

func (s *oauthTokenStore) Get(ctx context.Context, p store.GetOAuthTokensParams) (*store.OAuthToken, error) {
	t, err := s.conn.q.GetOAuthTokens(ctx, gendb.GetOAuthTokensParams{
		UserID:     p.UserID,
		ProviderID: p.ProviderID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBOAuthToken(t)
	return &out, nil
}

func (s *oauthTokenStore) ListExpiring(ctx context.Context) ([]store.OAuthToken, error) {
	rows, err := s.conn.q.ListExpiringOAuthTokens(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOAuthTokens(rows), nil
}

func (s *oauthTokenStore) ListByKeyVersion(ctx context.Context, keyVersion int64) ([]store.OAuthToken, error) {
	rows, err := s.conn.q.ListOAuthTokensByKeyVersion(ctx, keyVersion)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOAuthTokens(rows), nil
}

func (s *oauthTokenStore) CountByKeyVersion(ctx context.Context, keyVersion int64) (int64, error) {
	count, err := s.conn.q.CountOAuthTokensByKeyVersion(ctx, keyVersion)
	if err != nil {
		return 0, mapErr(err)
	}
	return count, nil
}

func (s *oauthTokenStore) DeleteByProvider(ctx context.Context, providerID string) error {
	return mapErr(s.conn.q.DeleteOAuthTokensByProvider(ctx, providerID))
}

func (s *oauthTokenStore) DeleteByUser(ctx context.Context, userID string) error {
	return mapErr(s.conn.q.DeleteOAuthTokensByUser(ctx, userID))
}

func (s *oauthTokenStore) DeleteByUserAndProvider(ctx context.Context, p store.DeleteOAuthTokensByUserAndProviderParams) error {
	return mapErr(s.conn.q.DeleteOAuthTokensByUserAndProvider(ctx, gendb.DeleteOAuthTokensByUserAndProviderParams{
		UserID:     p.UserID,
		ProviderID: p.ProviderID,
	}))
}
