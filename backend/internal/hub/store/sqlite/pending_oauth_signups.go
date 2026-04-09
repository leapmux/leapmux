package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
)

type pendingOAuthSignupStore struct {
	conn *sqliteConn
}

var _ store.PendingOAuthSignupStore = (*pendingOAuthSignupStore)(nil)

func fromDBPendingOAuthSignup(p gendb.PendingOauthSignup) *store.PendingOAuthSignup {
	return &store.PendingOAuthSignup{
		Token:           p.Token,
		ProviderID:      p.ProviderID,
		ProviderSubject: p.ProviderSubject,
		Email:           p.Email,
		DisplayName:     p.DisplayName,
		AccessToken:     p.AccessToken,
		RefreshToken:    p.RefreshToken,
		TokenType:       p.TokenType,
		TokenExpiresAt:  p.TokenExpiresAt,
		KeyVersion:      p.KeyVersion,
		RedirectURI:     p.RedirectUri,
		ExpiresAt:       p.ExpiresAt,
		CreatedAt:       p.CreatedAt,
	}
}

func (s *pendingOAuthSignupStore) Create(ctx context.Context, p store.CreatePendingOAuthSignupParams) error {
	return mapErr(s.conn.q.CreatePendingOAuthSignup(ctx, gendb.CreatePendingOAuthSignupParams{
		Token:           p.Token,
		ProviderID:      p.ProviderID,
		ProviderSubject: p.ProviderSubject,
		Email:           store.NormalizeEmail(p.Email),
		DisplayName:     p.DisplayName,
		AccessToken:     p.AccessToken,
		RefreshToken:    p.RefreshToken,
		TokenType:       p.TokenType,
		TokenExpiresAt:  p.TokenExpiresAt.UTC(),
		KeyVersion:      p.KeyVersion,
		RedirectUri:     p.RedirectURI,
		ExpiresAt:       p.ExpiresAt.UTC(),
	}))
}

func (s *pendingOAuthSignupStore) Get(ctx context.Context, token string) (*store.PendingOAuthSignup, error) {
	row, err := s.conn.q.GetPendingOAuthSignup(ctx, token)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBPendingOAuthSignup(row), nil
}

func (s *pendingOAuthSignupStore) Delete(ctx context.Context, token string) error {
	return mapErr(s.conn.q.DeletePendingOAuthSignup(ctx, token))
}
