package sqlite

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
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
			TrustEmail:   ptrconv.Int64ToBool(p.TrustEmail),
			Enabled:      ptrconv.Int64ToBool(p.Enabled),
			CreatedAt:    p.CreatedAt,
		},
		ClientSecret: p.ClientSecret,
	}
}

func fromDBOAuthProviders(rows []gendb.OauthProvider) []store.OAuthProvider {
	return store.MapSlice(rows, func(r gendb.OauthProvider) store.OAuthProvider { return *fromDBOAuthProvider(r) })
}

type oauthProviderSummaryRow interface {
	gendb.ListAllOAuthProvidersRow | gendb.ListEnabledOAuthProvidersRow
}

func fromDBOAuthProviderSummary[R oauthProviderSummaryRow](r R) store.OAuthProviderSummary {
	type concrete struct {
		ID           string    `json:"id"`
		ProviderType string    `json:"provider_type"`
		Name         string    `json:"name"`
		IssuerUrl    string    `json:"issuer_url"`
		ClientID     string    `json:"client_id"`
		Scopes       string    `json:"scopes"`
		TrustEmail   int64     `json:"trust_email"`
		Enabled      int64     `json:"enabled"`
		CreatedAt    time.Time `json:"created_at"`
	}
	c := concrete(r)
	return store.OAuthProviderSummary{
		ID:           c.ID,
		ProviderType: c.ProviderType,
		Name:         c.Name,
		IssuerURL:    c.IssuerUrl,
		ClientID:     c.ClientID,
		Scopes:       c.Scopes,
		TrustEmail:   ptrconv.Int64ToBool(c.TrustEmail),
		Enabled:      ptrconv.Int64ToBool(c.Enabled),
		CreatedAt:    c.CreatedAt,
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
		TrustEmail:   ptrconv.BoolToInt64(p.TrustEmail),
		Enabled:      ptrconv.BoolToInt64(p.Enabled),
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
	return store.MapSlice(rows, fromDBOAuthProviderSummary[gendb.ListEnabledOAuthProvidersRow]), nil
}

func (s *oauthProviderStore) ListAll(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	rows, err := s.q.ListAllOAuthProviders(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBOAuthProviderSummary[gendb.ListAllOAuthProvidersRow]), nil
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
		ID:      p.ID,
		Enabled: ptrconv.BoolToInt64(p.Enabled),
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
