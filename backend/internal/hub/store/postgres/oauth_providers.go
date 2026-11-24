package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type oauthProviderStore struct {
	conn *pgConn
}

var _ store.OAuthProviderStore = (*oauthProviderStore)(nil)

func (c *pgConn) fromDBOAuthProvider(p gendb.OauthProvider) (*store.OAuthProvider, error) {
	sum, err := sqlutil.OpenOAuthProviderSummary(c.ks, p.ID, p.Name, p.IssuerUrl, p.ClientID, p.Scopes)
	if err != nil {
		return nil, err
	}
	clientSecret, err := c.ks.OpenBytes(p.ClientSecret, sqlutil.OAuthProviderClientSecretAAD(p.ID))
	if err != nil {
		return nil, err
	}
	return &store.OAuthProvider{
		OAuthProviderSummary: store.OAuthProviderSummary{
			ID:           p.ID,
			ProviderType: p.ProviderType,
			Name:         sum.Name,
			IssuerURL:    sum.IssuerURL,
			ClientID:     sum.ClientID,
			Scopes:       sum.Scopes,
			TrustEmail:   p.TrustEmail,
			Enabled:      p.Enabled,
			CreatedAt:    p.CreatedAt.Time,
		},
		ClientSecret: clientSecret,
	}, nil
}

func (c *pgConn) fromDBListEnabledOAuthProvidersRow(r gendb.ListEnabledOAuthProvidersRow) (store.OAuthProviderSummary, error) {
	sum, err := sqlutil.OpenOAuthProviderSummary(c.ks, r.ID, r.Name, r.IssuerUrl, r.ClientID, r.Scopes)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	return store.OAuthProviderSummary{
		ID:           r.ID,
		ProviderType: r.ProviderType,
		Name:         sum.Name,
		IssuerURL:    sum.IssuerURL,
		ClientID:     sum.ClientID,
		Scopes:       sum.Scopes,
		TrustEmail:   r.TrustEmail,
		Enabled:      r.Enabled,
		CreatedAt:    r.CreatedAt.Time,
	}, nil
}

func (c *pgConn) fromDBListAllOAuthProvidersRow(r gendb.ListAllOAuthProvidersRow) (store.OAuthProviderSummary, error) {
	return c.fromDBListEnabledOAuthProvidersRow(gendb.ListEnabledOAuthProvidersRow(r))
}

func (s *oauthProviderStore) Create(ctx context.Context, p store.CreateOAuthProviderParams) error {
	sealed, err := sqlutil.SealOAuthProvider(s.conn.ks, p)
	if err != nil {
		return err
	}
	return mapErr(s.conn.q.CreateOAuthProvider(ctx, gendb.CreateOAuthProviderParams{
		ID:           p.ID,
		ProviderType: p.ProviderType,
		Name:         sealed.Name,
		IssuerUrl:    sealed.IssuerURL,
		ClientID:     sealed.ClientID,
		ClientSecret: sealed.ClientSecret,
		Scopes:       sealed.Scopes,
		TrustEmail:   p.TrustEmail,
		Enabled:      p.Enabled,
	}))
}

func (s *oauthProviderStore) GetByID(ctx context.Context, id string) (*store.OAuthProvider, error) {
	p, err := s.conn.q.GetOAuthProviderByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return s.conn.fromDBOAuthProvider(p)
}

func (s *oauthProviderStore) ListEnabled(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	rows, err := s.conn.q.ListEnabledOAuthProviders(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSliceErr(rows, s.conn.fromDBListEnabledOAuthProvidersRow)
}

func (s *oauthProviderStore) ListAll(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	rows, err := s.conn.q.ListAllOAuthProviders(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSliceErr(rows, s.conn.fromDBListAllOAuthProvidersRow)
}

func (s *oauthProviderStore) UpdateEnabled(ctx context.Context, p store.UpdateOAuthProviderEnabledParams) error {
	return mapErr(s.conn.q.UpdateOAuthProviderEnabled(ctx, gendb.UpdateOAuthProviderEnabledParams{
		Enabled: p.Enabled,
		ID:      p.ID,
	}))
}

func (s *oauthProviderStore) UpdateClientSecret(ctx context.Context, p store.UpdateOAuthProviderClientSecretParams) error {
	sealed, err := s.conn.ks.SealBytes(p.ClientSecret, sqlutil.OAuthProviderClientSecretAAD(p.ID))
	if err != nil {
		return err
	}
	return mapErr(s.conn.q.UpdateOAuthProviderClientSecret(ctx, gendb.UpdateOAuthProviderClientSecretParams{
		ClientSecret: sealed,
		ID:           p.ID,
	}))
}

func (s *oauthProviderStore) Delete(ctx context.Context, id string) error {
	return mapErr(s.conn.q.DeleteOAuthProvider(ctx, id))
}
