package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
)

type oauthUserLinkStore struct {
	conn *mysqlConn
}

var _ store.OAuthUserLinkStore = (*oauthUserLinkStore)(nil)

func fromDBOAuthUserLink(l gendb.OauthUserLink) store.OAuthUserLink {
	return store.OAuthUserLink{
		UserID:          l.UserID,
		ProviderID:      l.ProviderID,
		ProviderSubject: l.ProviderSubject,
		CreatedAt:       l.CreatedAt,
	}
}

func fromDBOAuthUserLinks(rows []gendb.OauthUserLink) []store.OAuthUserLink {
	return store.MapSlice(rows, fromDBOAuthUserLink)
}

func (s *oauthUserLinkStore) Create(ctx context.Context, p store.CreateOAuthUserLinkParams) error {
	return mapErr(s.conn.q.CreateOAuthUserLink(ctx, gendb.CreateOAuthUserLinkParams{
		UserID:          p.UserID,
		ProviderID:      p.ProviderID,
		ProviderSubject: p.ProviderSubject,
	}))
}

func (s *oauthUserLinkStore) Get(ctx context.Context, p store.GetOAuthUserLinkParams) (*store.OAuthUserLink, error) {
	l, err := s.conn.q.GetOAuthUserLink(ctx, gendb.GetOAuthUserLinkParams{
		ProviderID:      p.ProviderID,
		ProviderSubject: p.ProviderSubject,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBOAuthUserLink(l)
	return &out, nil
}

func (s *oauthUserLinkStore) ListByUser(ctx context.Context, userID string) ([]store.OAuthUserLink, error) {
	rows, err := s.conn.q.ListOAuthUserLinksByUser(ctx, userID)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOAuthUserLinks(rows), nil
}

func (s *oauthUserLinkStore) Delete(ctx context.Context, p store.DeleteOAuthUserLinkParams) error {
	return mapErr(s.conn.q.DeleteOAuthUserLink(ctx, gendb.DeleteOAuthUserLinkParams{
		UserID:     p.UserID,
		ProviderID: p.ProviderID,
	}))
}

func (s *oauthUserLinkStore) DeleteByProvider(ctx context.Context, providerID string) error {
	return mapErr(s.conn.q.DeleteOAuthUserLinksByProvider(ctx, providerID))
}
