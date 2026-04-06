package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type pendingOAuthSignupStore struct {
	q *gendb.Queries
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
		TokenExpiresAt:  tsToTime(p.TokenExpiresAt),
		KeyVersion:      int64(p.KeyVersion),
		RedirectURI:     p.RedirectUri,
		ExpiresAt:       tsToTime(p.ExpiresAt),
		CreatedAt:       tsToTime(p.CreatedAt),
	}
}

func (s *pendingOAuthSignupStore) Create(ctx context.Context, p store.CreatePendingOAuthSignupParams) error {
	return mapErr(s.q.CreatePendingOAuthSignup(ctx, gendb.CreatePendingOAuthSignupParams{
		Token:           p.Token,
		ProviderID:      p.ProviderID,
		ProviderSubject: p.ProviderSubject,
		Email:           store.NormalizeEmail(p.Email),
		DisplayName:     p.DisplayName,
		AccessToken:     p.AccessToken,
		RefreshToken:    p.RefreshToken,
		TokenType:       p.TokenType,
		TokenExpiresAt:  timeToTs(p.TokenExpiresAt),
		KeyVersion:      int32(p.KeyVersion),
		RedirectUri:     p.RedirectURI,
		ExpiresAt:       timeToTs(p.ExpiresAt),
	}))
}

func (s *pendingOAuthSignupStore) Get(ctx context.Context, token string) (*store.PendingOAuthSignup, error) {
	row, err := s.q.GetPendingOAuthSignup(ctx, token)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBPendingOAuthSignup(row), nil
}

func (s *pendingOAuthSignupStore) Delete(ctx context.Context, token string) error {
	return mapErr(s.q.DeletePendingOAuthSignup(ctx, token))
}
