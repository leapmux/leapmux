package mysql

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type oauthProviderStore struct {
	conn *mysqlConn
}

var _ store.OAuthProviderStore = (*oauthProviderStore)(nil)

func (c *mysqlConn) fromDBOAuthProvider(p gendb.OauthProvider) (*store.OAuthProvider, error) {
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
			ResponseMode: p.ResponseMode,
			Enabled:      p.Enabled,
			CreatedAt:    p.CreatedAt.Time,
		},
		ClientSecret: clientSecret,
	}, nil
}

func (c *mysqlConn) fromDBListEnabledOAuthProvidersRow(r gendb.ListEnabledOAuthProvidersRow) (store.OAuthProviderSummary, error) {
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
		ResponseMode: r.ResponseMode,
		Enabled:      r.Enabled,
		CreatedAt:    r.CreatedAt.Time,
	}, nil
}

func (c *mysqlConn) fromDBListAllOAuthProvidersRow(r gendb.ListAllOAuthProvidersRow) (store.OAuthProviderSummary, error) {
	return c.fromDBListEnabledOAuthProvidersRow(gendb.ListEnabledOAuthProvidersRow(r))
}

func (s *oauthProviderStore) Create(ctx context.Context, p store.CreateOAuthProviderParams) error {
	sealed, err := sqlutil.SealOAuthProvider(s.conn.ks, p)
	if err != nil {
		return err
	}
	mode := p.ResponseMode
	if mode == leapmuxv1.OAuthResponseMode_O_AUTH_RESPONSE_MODE_UNSPECIFIED {
		mode = leapmuxv1.OAuthResponseMode_O_AUTH_RESPONSE_MODE_QUERY
	}
	return s.conn.queryError(s.conn.q.CreateOAuthProvider(ctx, gendb.CreateOAuthProviderParams{
		ID:           p.ID,
		ProviderType: p.ProviderType,
		Name:         sealed.Name,
		IssuerUrl:    sealed.IssuerURL,
		ClientID:     sealed.ClientID,
		ClientSecret: sealed.ClientSecret,
		Scopes:       sealed.Scopes,
		TrustEmail:   p.TrustEmail,
		ResponseMode: mode,
		Enabled:      p.Enabled,
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

func (s *oauthProviderStore) ListByIDPrefix(ctx context.Context, idPrefix string) ([]store.OAuthProviderSummary, error) {
	filter, hasPrefix, err := store.ParseIDPrefix(idPrefix)
	if err != nil {
		return nil, err
	}
	if !hasPrefix {
		return s.ListAll(ctx)
	}
	if !filter.Exact && !filter.HasUpper {
		return nil, nil
	}
	if filter.Exact {
		rows, err := s.conn.q.ListOAuthProvidersByExactID(ctx, filter.Lower)
		if err != nil {
			return nil, s.conn.queryError(err)
		}
		return store.MapSliceErr(rows, func(r gendb.ListOAuthProvidersByExactIDRow) (store.OAuthProviderSummary, error) {
			return s.conn.fromDBListEnabledOAuthProvidersRow(gendb.ListEnabledOAuthProvidersRow(r))
		})
	}
	rows, err := s.conn.q.ListOAuthProvidersByIDPrefix(ctx, gendb.ListOAuthProvidersByIDPrefixParams{
		IDLo: filter.Lower,
		IDHi: filter.Upper,
	})
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	return store.MapSliceErr(rows, func(r gendb.ListOAuthProvidersByIDPrefixRow) (store.OAuthProviderSummary, error) {
		return s.conn.fromDBListEnabledOAuthProvidersRow(gendb.ListEnabledOAuthProvidersRow(r))
	})
}

func (s *oauthProviderStore) UpdateEnabled(ctx context.Context, p store.UpdateOAuthProviderEnabledParams) error {
	return s.conn.queryError(s.conn.q.UpdateOAuthProviderEnabled(ctx, gendb.UpdateOAuthProviderEnabledParams{
		ID:      p.ID,
		Enabled: p.Enabled,
	}))
}

func (s *oauthProviderStore) UpdateClientSecret(ctx context.Context, p store.UpdateOAuthProviderClientSecretParams) error {
	sealed, err := s.conn.ks.SealBytes(p.ClientSecret, sqlutil.OAuthProviderClientSecretAAD(p.ID))
	if err != nil {
		return err
	}
	rows, err := s.conn.q.UpdateOAuthProviderClientSecret(ctx, gendb.UpdateOAuthProviderClientSecretParams{
		ID:           p.ID,
		ClientSecret: sealed,
	})
	if err != nil {
		return s.conn.queryError(err)
	}
	if rows == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *oauthProviderStore) Delete(ctx context.Context, id string) error {
	return s.conn.queryError(s.conn.q.DeleteOAuthProvider(ctx, id))
}
