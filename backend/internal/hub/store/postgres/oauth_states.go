package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
)

type oauthStateStore struct {
	conn *pgConn
}

var _ store.OAuthStateStore = (*oauthStateStore)(nil)

func fromDBOAuthState(s gendb.OauthState) *store.OAuthState {
	return &store.OAuthState{
		State:        s.State,
		ProviderID:   s.ProviderID,
		PkceVerifier: s.PkceVerifier,
		NonceHash:    s.NonceHash,
		RedirectURI:  s.RedirectUri,
		Purpose:      s.Purpose,
		SessionID:    s.SessionID,
		ExpiresAt:    s.ExpiresAt.Time,
		CreatedAt:    s.CreatedAt.Time,
	}
}

func (s *oauthStateStore) Create(ctx context.Context, p store.CreateOAuthStateParams) error {
	return mapErr(s.conn.q.CreateOAuthState(ctx, gendb.CreateOAuthStateParams{
		State:        p.State,
		ProviderID:   p.ProviderID,
		PkceVerifier: p.PkceVerifier,
		NonceHash:    p.NonceHash,
		RedirectUri:  p.RedirectURI,
		Purpose:      p.Purpose,
		SessionID:    p.SessionID,
		ExpiresAt:    pgtime.New(p.ExpiresAt),
	}))
}

func (s *oauthStateStore) Get(ctx context.Context, state string) (*store.OAuthState, error) {
	row, err := s.conn.q.GetOAuthState(ctx, state)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOAuthState(row), nil
}

func (s *oauthStateStore) Delete(ctx context.Context, state string) (int64, error) {
	n, err := s.conn.q.DeleteOAuthState(ctx, state)
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}
