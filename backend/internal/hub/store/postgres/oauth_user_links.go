package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type oauthUserLinkStore struct {
	conn *pgConn
}

var _ store.OAuthUserLinkStore = (*oauthUserLinkStore)(nil)

func fromDBOAuthUserLink(l gendb.OauthUserLink) store.OAuthUserLink {
	return store.OAuthUserLink{
		UserID:          l.UserID,
		ProviderID:      l.ProviderID,
		ProviderSubject: l.ProviderSubject,
		CreatedAt:       l.CreatedAt.Time,
	}
}

func fromDBOAuthUserLinks(rows []gendb.OauthUserLink) []store.OAuthUserLink {
	return store.MapSlice(rows, fromDBOAuthUserLink)
}

func (s *oauthUserLinkStore) Create(ctx context.Context, p store.CreateOAuthUserLinkParams) error {
	return mapErr(s.conn.q.CreateOAuthUserLink(ctx, gendb.CreateOAuthUserLinkParams{
		UserID:          p.UserID.String(),
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

func (s *oauthUserLinkStore) ListByUser(ctx context.Context, userID userid.UserID) ([]store.OAuthUserLink, error) {
	owner, ok := userid.OwnerFilter(userID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return nil, nil
	}
	rows, err := s.conn.q.ListOAuthUserLinksByUser(ctx, owner)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOAuthUserLinks(rows), nil
}

func (s *oauthUserLinkStore) Delete(ctx context.Context, p store.DeleteOAuthUserLinkParams) error {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. This method reports only an error,
		// so returning nil would tell the caller the mutation SUCCEEDED while
		// addressing no row -- the shape a revocation must never have. See
		// userid.OwnerFilter.
		return store.ErrInvalidArgument
	}
	return mapErr(s.conn.q.DeleteOAuthUserLink(ctx, gendb.DeleteOAuthUserLinkParams{
		UserID:     owner,
		ProviderID: p.ProviderID,
	}))
}

func (s *oauthUserLinkStore) DeleteByProvider(ctx context.Context, providerID string) error {
	return mapErr(s.conn.q.DeleteOAuthUserLinksByProvider(ctx, providerID))
}

func (s *oauthUserLinkStore) CountUsersOrphanedByProvider(ctx context.Context, providerID string) (int64, error) {
	n, err := s.conn.q.CountUsersOrphanedByProvider(ctx, providerID)
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}
