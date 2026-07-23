package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type oauthTokenStore struct {
	conn *mysqlConn
}

var _ store.OAuthTokenStore = (*oauthTokenStore)(nil)

func fromDBOAuthToken(t gendb.OauthToken) store.OAuthToken {
	return store.OAuthToken{
		UserID:       t.UserID,
		ProviderID:   t.ProviderID,
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		TokenType:    t.TokenType,
		ExpiresAt:    t.ExpiresAt.Time,
		KeyVersion:   t.KeyVersion,
		UpdatedAt:    t.UpdatedAt.Time,
	}
}

func fromDBOAuthTokens(rows []gendb.OauthToken) []store.OAuthToken {
	return store.MapSlice(rows, fromDBOAuthToken)
}

func (s *oauthTokenStore) Upsert(ctx context.Context, p store.UpsertOAuthTokensParams) error {
	return mapErr(s.conn.q.UpsertOAuthTokens(ctx, gendb.UpsertOAuthTokensParams{
		UserID:       p.UserID.String(),
		ProviderID:   p.ProviderID,
		AccessToken:  p.AccessToken,
		RefreshToken: p.RefreshToken,
		TokenType:    p.TokenType,
		ExpiresAt:    sqltime.NewMySQLTime(p.ExpiresAt),
		KeyVersion:   p.KeyVersion,
	}))
}

func (s *oauthTokenStore) Get(ctx context.Context, p store.GetOAuthTokensParams) (*store.OAuthToken, error) {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See store.OwnerFilter.
		return nil, store.ErrNotFound
	}
	t, err := s.conn.q.GetOAuthTokens(ctx, gendb.GetOAuthTokensParams{
		UserID:     owner,
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

func (s *oauthTokenStore) DeleteByUser(ctx context.Context, userID userid.UserID) error {
	owner, ok := store.OwnerFilter(userID)
	if !ok {
		// An unminted caller names no user, so a bulk mutation must refuse
		// rather than address every blank-owner row -- or report success
		// having changed nothing. See store.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteOAuthTokensByUser(ctx, owner))
}

func (s *oauthTokenStore) DeleteByUserAndProvider(ctx context.Context, p store.DeleteOAuthTokensByUserAndProviderParams) error {
	owner, ok := store.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. This method reports only an error,
		// so returning nil would tell the caller the mutation SUCCEEDED while
		// addressing no row -- the shape a revocation must never have. See
		// store.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteOAuthTokensByUserAndProvider(ctx, gendb.DeleteOAuthTokensByUserAndProviderParams{
		UserID:     owner,
		ProviderID: p.ProviderID,
	}))
}
