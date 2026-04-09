package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type oauthProviderStore struct {
	q *gendb.Queries
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
			CreatedAt:    tsToTime(p.CreatedAt),
		},
		ClientSecret: p.ClientSecret,
	}
}

func fromDBOAuthProviders(rows []gendb.OauthProvider) []store.OAuthProvider {
	return sqlutil.MapSlice(rows, func(r gendb.OauthProvider) store.OAuthProvider { return *fromDBOAuthProvider(r) })
}

type oauthProviderSummaryRow interface {
	gendb.ListAllOAuthProvidersRow | gendb.ListEnabledOAuthProvidersRow
}

func fromDBOAuthProviderSummary[R oauthProviderSummaryRow](r R) store.OAuthProviderSummary {
	type concrete struct {
		ID           string             `json:"id"`
		ProviderType string             `json:"provider_type"`
		Name         string             `json:"name"`
		IssuerUrl    string             `json:"issuer_url"`
		ClientID     string             `json:"client_id"`
		Scopes       string             `json:"scopes"`
		TrustEmail   bool               `json:"trust_email"`
		Enabled      bool               `json:"enabled"`
		CreatedAt    pgtype.Timestamptz `json:"created_at"`
	}
	c := concrete(r)
	return store.OAuthProviderSummary{
		ID:           c.ID,
		ProviderType: c.ProviderType,
		Name:         c.Name,
		IssuerURL:    c.IssuerUrl,
		ClientID:     c.ClientID,
		Scopes:       c.Scopes,
		TrustEmail:   c.TrustEmail,
		Enabled:      c.Enabled,
		CreatedAt:    tsToTime(c.CreatedAt),
	}
}

func (s *oauthProviderStore) Create(ctx context.Context, p store.CreateOAuthProviderParams) error {
	return mapErr(s.q.CreateOAuthProvider(ctx, gendb.CreateOAuthProviderParams{
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
	p, err := s.q.GetOAuthProviderByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOAuthProvider(p), nil
}

func (s *oauthProviderStore) ListEnabled(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	rows, err := s.q.ListEnabledOAuthProviders(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return sqlutil.MapSlice(rows, fromDBOAuthProviderSummary[gendb.ListEnabledOAuthProvidersRow]), nil
}

func (s *oauthProviderStore) ListAll(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	rows, err := s.q.ListAllOAuthProviders(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return sqlutil.MapSlice(rows, fromDBOAuthProviderSummary[gendb.ListAllOAuthProvidersRow]), nil
}

func (s *oauthProviderStore) ListAllWithSecrets(ctx context.Context) ([]store.OAuthProvider, error) {
	rows, err := s.q.ListAllOAuthProvidersWithSecrets(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOAuthProviders(rows), nil
}

func (s *oauthProviderStore) UpdateEnabled(ctx context.Context, p store.UpdateOAuthProviderEnabledParams) error {
	return mapErr(s.q.UpdateOAuthProviderEnabled(ctx, gendb.UpdateOAuthProviderEnabledParams{
		Enabled: p.Enabled,
		ID:      p.ID,
	}))
}

func (s *oauthProviderStore) UpdateClientSecret(ctx context.Context, id string, clientSecret []byte) error {
	return mapErr(s.q.UpdateOAuthProviderClientSecret(ctx, gendb.UpdateOAuthProviderClientSecretParams{
		ClientSecret: clientSecret,
		ID:           id,
	}))
}

func (s *oauthProviderStore) Delete(ctx context.Context, id string) error {
	return mapErr(s.q.DeleteOAuthProvider(ctx, id))
}
