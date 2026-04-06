package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
)

type oauthStateStore struct {
	q *gendb.Queries
}

var _ store.OAuthStateStore = (*oauthStateStore)(nil)

func fromDBOAuthState(s gendb.OauthState) *store.OAuthState {
	return &store.OAuthState{
		State:        s.State,
		ProviderID:   s.ProviderID,
		PkceVerifier: s.PkceVerifier,
		RedirectURI:  s.RedirectUri,
		ExpiresAt:    s.ExpiresAt,
		CreatedAt:    s.CreatedAt,
	}
}

func (s *oauthStateStore) Create(ctx context.Context, p store.CreateOAuthStateParams) error {
	return mapErr(s.q.CreateOAuthState(ctx, gendb.CreateOAuthStateParams{
		State:        p.State,
		ProviderID:   p.ProviderID,
		PkceVerifier: p.PkceVerifier,
		RedirectUri:  p.RedirectURI,
		ExpiresAt:    p.ExpiresAt,
	}))
}

func (s *oauthStateStore) Get(ctx context.Context, state string) (*store.OAuthState, error) {
	row, err := s.q.GetOAuthState(ctx, state)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOAuthState(row), nil
}

func (s *oauthStateStore) Delete(ctx context.Context, state string) error {
	return mapErr(s.q.DeleteOAuthState(ctx, state))
}
