package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
)

type oauthProviderStore struct {
	conn *mysqlConn
}

var _ store.OAuthProviderStore = (*oauthProviderStore)(nil)

func fromDBOAuthProvider(p gendb.OauthProvider) *store.OAuthProvider {
	return &store.OAuthProvider{
		OAuthProviderSummary: store.OAuthProviderSummary{
			ID:           p.ID,
			ProviderType: p.ProviderType,
			Name:         p.Name,
			IssuerURL:    p.IssuerUrl,
			ClientID:     p.ClientID,
			Scopes:       p.Scopes,
			TrustEmail:   p.TrustEmail,
			Enabled:      p.Enabled,
			CreatedAt:    p.CreatedAt,
		},
		ClientSecret: p.ClientSecret,
	}
}

func fromDBOAuthProviders(rows []gendb.OauthProvider) []store.OAuthProvider {
	return store.MapSlice(rows, func(r gendb.OauthProvider) store.OAuthProvider { return *fromDBOAuthProvider(r) })
}

func fromDBListEnabledOAuthProvidersRow(r gendb.ListEnabledOAuthProvidersRow) store.OAuthProviderSummary {
	return store.OAuthProviderSummary{
		ID:           r.ID,
		ProviderType: r.ProviderType,
		Name:         r.Name,
		IssuerURL:    r.IssuerUrl,
		ClientID:     r.ClientID,
		Scopes:       r.Scopes,
		TrustEmail:   r.TrustEmail,
		Enabled:      r.Enabled,
		CreatedAt:    r.CreatedAt,
	}
}

func fromDBListAllOAuthProvidersRow(r gendb.ListAllOAuthProvidersRow) store.OAuthProviderSummary {
	return store.OAuthProviderSummary{
		ID:           r.ID,
		ProviderType: r.ProviderType,
		Name:         r.Name,
		IssuerURL:    r.IssuerUrl,
		ClientID:     r.ClientID,
		Scopes:       r.Scopes,
		TrustEmail:   r.TrustEmail,
		Enabled:      r.Enabled,
		CreatedAt:    r.CreatedAt,
	}
}

func (s *oauthProviderStore) Create(ctx context.Context, p store.CreateOAuthProviderParams) error {
	return mapErr(s.conn.q.CreateOAuthProvider(ctx, gendb.CreateOAuthProviderParams{
		ID:           p.ID,
		ProviderType: p.ProviderType,
		Name:         p.Name,
		IssuerUrl:    p.IssuerURL,
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		Scopes:       p.Scopes,
		TrustEmail:   p.TrustEmail,
		Enabled:      p.Enabled,
	}))
}

func (s *oauthProviderStore) GetByID(ctx context.Context, id string) (*store.OAuthProvider, error) {
	p, err := s.conn.q.GetOAuthProviderByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOAuthProvider(p), nil
}

func (s *oauthProviderStore) ListEnabled(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	rows, err := s.conn.q.ListEnabledOAuthProviders(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBListEnabledOAuthProvidersRow), nil
}

func (s *oauthProviderStore) ListAll(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	rows, err := s.conn.q.ListAllOAuthProviders(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBListAllOAuthProvidersRow), nil
}

func (s *oauthProviderStore) ListAllWithSecrets(ctx context.Context) ([]store.OAuthProvider, error) {
	rows, err := s.conn.q.ListAllOAuthProvidersWithSecrets(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOAuthProviders(rows), nil
}

func (s *oauthProviderStore) UpdateEnabled(ctx context.Context, p store.UpdateOAuthProviderEnabledParams) error {
	return mapErr(s.conn.q.UpdateOAuthProviderEnabled(ctx, gendb.UpdateOAuthProviderEnabledParams{
		ID:      p.ID,
		Enabled: p.Enabled,
	}))
}

func (s *oauthProviderStore) UpdateClientSecret(ctx context.Context, id string, clientSecret []byte) error {
	return mapErr(s.conn.q.UpdateOAuthProviderClientSecret(ctx, gendb.UpdateOAuthProviderClientSecretParams{
		ClientSecret: clientSecret,
		ID:           id,
	}))
}

func (s *oauthProviderStore) Delete(ctx context.Context, id string) error {
	return mapErr(s.conn.q.DeleteOAuthProvider(ctx, id))
}
