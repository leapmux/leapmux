package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type oauthProviderStore struct {
	conn *sqliteConn
}

var _ store.OAuthProviderStore = (*oauthProviderStore)(nil)

func (c *sqliteConn) fromDBOAuthProvider(p gendb.OauthProvider) (*store.OAuthProvider, error) {
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
			TrustEmail:   ptrconv.Int64ToBool(p.TrustEmail),
			Enabled:      ptrconv.Int64ToBool(p.Enabled),
			CreatedAt:    p.CreatedAt.Time,
		},
		ClientSecret: clientSecret,
	}, nil
}

func (c *sqliteConn) fromDBListEnabledOAuthProvidersRow(r gendb.ListEnabledOAuthProvidersRow) (store.OAuthProviderSummary, error) {
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
		TrustEmail:   ptrconv.Int64ToBool(r.TrustEmail),
		Enabled:      ptrconv.Int64ToBool(r.Enabled),
		CreatedAt:    r.CreatedAt.Time,
	}, nil
}

func (c *sqliteConn) fromDBListAllOAuthProvidersRow(r gendb.ListAllOAuthProvidersRow) (store.OAuthProviderSummary, error) {
	return c.fromDBListEnabledOAuthProvidersRow(gendb.ListEnabledOAuthProvidersRow(r))
}

func (s *oauthProviderStore) Create(ctx context.Context, p store.CreateOAuthProviderParams) error {
	sealed, err := sqlutil.SealOAuthProvider(s.conn.ks, p)
	if err != nil {
		return err
	}
	return s.conn.queryError(s.conn.q.CreateOAuthProvider(ctx, gendb.CreateOAuthProviderParams{
		ID:           p.ID,
		ProviderType: p.ProviderType,
		Name:         sealed.Name,
		IssuerUrl:    sealed.IssuerURL,
		ClientID:     sealed.ClientID,
		ClientSecret: sealed.ClientSecret,
		Scopes:       sealed.Scopes,
		TrustEmail:   ptrconv.BoolToInt64(p.TrustEmail),
		Enabled:      ptrconv.BoolToInt64(p.Enabled),
	}))
}

func (s *oauthProviderStore) GetByID(ctx context.Context, id string) (*store.OAuthProvider, error) {
	p, err := s.conn.q.GetOAuthProviderByID(ctx, id)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	return s.conn.fromDBOAuthProvider(p)
}

func (s *oauthProviderStore) ListEnabled(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	rows, err := s.conn.q.ListEnabledOAuthProviders(ctx)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	return store.MapSliceErr(rows, s.conn.fromDBListEnabledOAuthProvidersRow)
}

func (s *oauthProviderStore) ListAll(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	rows, err := s.conn.q.ListAllOAuthProviders(ctx)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	return store.MapSliceErr(rows, s.conn.fromDBListAllOAuthProvidersRow)
}

func (s *oauthProviderStore) UpdateEnabled(ctx context.Context, p store.UpdateOAuthProviderEnabledParams) error {
	return s.conn.queryError(s.conn.q.UpdateOAuthProviderEnabled(ctx, gendb.UpdateOAuthProviderEnabledParams{
		ID:      p.ID,
		Enabled: ptrconv.BoolToInt64(p.Enabled),
	}))
}

func (s *oauthProviderStore) UpdateClientSecret(ctx context.Context, p store.UpdateOAuthProviderClientSecretParams) error {
	sealed, err := s.conn.ks.SealBytes(p.ClientSecret, sqlutil.OAuthProviderClientSecretAAD(p.ID))
	if err != nil {
		return err
	}
	return s.conn.queryError(s.conn.q.UpdateOAuthProviderClientSecret(ctx, gendb.UpdateOAuthProviderClientSecretParams{
		ID:           p.ID,
		ClientSecret: sealed,
	}))
}

func (s *oauthProviderStore) Delete(ctx context.Context, id string) error {
	return s.conn.queryError(s.conn.q.DeleteOAuthProvider(ctx, id))
}
