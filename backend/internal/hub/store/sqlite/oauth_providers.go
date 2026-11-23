package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type oauthProviderStore struct {
	conn *sqliteConn
}

var _ store.OAuthProviderStore = (*oauthProviderStore)(nil)

func (c *sqliteConn) fromDBOAuthProvider(p gendb.OauthProvider) (*store.OAuthProvider, error) {
	name, err := c.ks.OpenString(p.Name, keystore.OAuthProviderNameAAD(p.ID))
	if err != nil {
		return nil, err
	}
	issuerURL, err := c.ks.OpenString(p.IssuerUrl, keystore.OAuthProviderIssuerURLAAD(p.ID))
	if err != nil {
		return nil, err
	}
	clientID, err := c.ks.OpenString(p.ClientID, keystore.OAuthProviderClientIDAAD(p.ID))
	if err != nil {
		return nil, err
	}
	clientSecret, err := c.ks.OpenBytes(p.ClientSecret, keystore.OAuthProviderClientSecretAAD(p.ID))
	if err != nil {
		return nil, err
	}
	scopes, err := c.ks.OpenString(p.Scopes, keystore.OAuthProviderScopesAAD(p.ID))
	if err != nil {
		return nil, err
	}
	return &store.OAuthProvider{
		OAuthProviderSummary: store.OAuthProviderSummary{
			ID:           p.ID,
			ProviderType: p.ProviderType,
			Name:         name,
			IssuerURL:    issuerURL,
			ClientID:     clientID,
			Scopes:       scopes,
			TrustEmail:   ptrconv.Int64ToBool(p.TrustEmail),
			Enabled:      ptrconv.Int64ToBool(p.Enabled),
			CreatedAt:    p.CreatedAt.Time,
		},
		ClientSecret: clientSecret,
	}, nil
}

func (c *sqliteConn) openOAuthProviderSummary(id string, nameCt, issuerURLCt, clientIDCt, scopesCt keystore.Ciphertext) (name, issuerURL, clientID, scopes string, err error) {
	if name, err = c.ks.OpenString(nameCt, keystore.OAuthProviderNameAAD(id)); err != nil {
		return "", "", "", "", err
	}
	if issuerURL, err = c.ks.OpenString(issuerURLCt, keystore.OAuthProviderIssuerURLAAD(id)); err != nil {
		return "", "", "", "", err
	}
	if clientID, err = c.ks.OpenString(clientIDCt, keystore.OAuthProviderClientIDAAD(id)); err != nil {
		return "", "", "", "", err
	}
	if scopes, err = c.ks.OpenString(scopesCt, keystore.OAuthProviderScopesAAD(id)); err != nil {
		return "", "", "", "", err
	}
	return name, issuerURL, clientID, scopes, nil
}

func (c *sqliteConn) fromDBListEnabledOAuthProvidersRow(r gendb.ListEnabledOAuthProvidersRow) (store.OAuthProviderSummary, error) {
	name, issuerURL, clientID, scopes, err := c.openOAuthProviderSummary(r.ID, r.Name, r.IssuerUrl, r.ClientID, r.Scopes)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	return store.OAuthProviderSummary{
		ID:           r.ID,
		ProviderType: r.ProviderType,
		Name:         name,
		IssuerURL:    issuerURL,
		ClientID:     clientID,
		Scopes:       scopes,
		TrustEmail:   ptrconv.Int64ToBool(r.TrustEmail),
		Enabled:      ptrconv.Int64ToBool(r.Enabled),
		CreatedAt:    r.CreatedAt.Time,
	}, nil
}

func (c *sqliteConn) fromDBListAllOAuthProvidersRow(r gendb.ListAllOAuthProvidersRow) (store.OAuthProviderSummary, error) {
	name, issuerURL, clientID, scopes, err := c.openOAuthProviderSummary(r.ID, r.Name, r.IssuerUrl, r.ClientID, r.Scopes)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	return store.OAuthProviderSummary{
		ID:           r.ID,
		ProviderType: r.ProviderType,
		Name:         name,
		IssuerURL:    issuerURL,
		ClientID:     clientID,
		Scopes:       scopes,
		TrustEmail:   ptrconv.Int64ToBool(r.TrustEmail),
		Enabled:      ptrconv.Int64ToBool(r.Enabled),
		CreatedAt:    r.CreatedAt.Time,
	}, nil
}

func (s *oauthProviderStore) Create(ctx context.Context, p store.CreateOAuthProviderParams) error {
	name, err := s.conn.ks.SealString(p.Name, keystore.OAuthProviderNameAAD(p.ID))
	if err != nil {
		return err
	}
	issuerURL, err := s.conn.ks.SealString(p.IssuerURL, keystore.OAuthProviderIssuerURLAAD(p.ID))
	if err != nil {
		return err
	}
	clientID, err := s.conn.ks.SealString(p.ClientID, keystore.OAuthProviderClientIDAAD(p.ID))
	if err != nil {
		return err
	}
	clientSecret, err := s.conn.ks.SealBytes(p.ClientSecret, keystore.OAuthProviderClientSecretAAD(p.ID))
	if err != nil {
		return err
	}
	scopes, err := s.conn.ks.SealString(p.Scopes, keystore.OAuthProviderScopesAAD(p.ID))
	if err != nil {
		return err
	}
	return s.conn.queryError(s.conn.q.CreateOAuthProvider(ctx, gendb.CreateOAuthProviderParams{
		ID:           p.ID,
		ProviderType: p.ProviderType,
		Name:         name,
		IssuerUrl:    issuerURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       scopes,
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

func (s *oauthProviderStore) Delete(ctx context.Context, id string) error {
	return s.conn.queryError(s.conn.q.DeleteOAuthProvider(ctx, id))
}
