package mysql

import (
	"context"
	"database/sql"
	"errors"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type oauthClientStore struct{ conn *mysqlConn }

var _ store.OAuthClientStore = (*oauthClientStore)(nil)

// Get and List project icon length instead of its bytes. Their generated row
// types have identical fields and support explicit conversion.
func (c *mysqlConn) fromGetOAuthClientRow(r gendb.GetOAuthClientRow) (store.OAuthClient, error) {
	clientName, err := c.ks.OpenString(r.ClientName, keystore.OAuthClientNameAAD(r.ClientID))
	if err != nil {
		return store.OAuthClient{}, err
	}
	clientURI, err := c.ks.OpenString(r.ClientUri, keystore.OAuthClientURIAAD(r.ClientID))
	if err != nil {
		return store.OAuthClient{}, err
	}
	redirectURIs, err := c.ks.OpenString(r.RedirectUris, keystore.OAuthClientRedirectURIsAAD(r.ClientID))
	if err != nil {
		return store.OAuthClient{}, err
	}
	scopes, err := c.ks.OpenString(r.Scopes, keystore.OAuthClientScopesAAD(r.ClientID))
	if err != nil {
		return store.OAuthClient{}, err
	}
	grantTypes, err := c.ks.OpenString(r.GrantTypes, keystore.OAuthClientGrantTypesAAD(r.ClientID))
	if err != nil {
		return store.OAuthClient{}, err
	}
	return store.OAuthClient{
		ClientID:           r.ClientID,
		OwnerUserID:        r.OwnerUserID.String,
		SecretHash:         r.SecretHash,
		ClientName:         clientName,
		HasIcon:            sqlutil.CoerceInt64(r.IconBytes) > 0,
		ClientURI:          clientURI,
		RedirectURIs:       redirectURIs,
		Scopes:             scopes,
		GrantTypes:         grantTypes,
		ElevationAllowed:   r.ElevationAllowed,
		RegistrationSource: r.RegistrationSource,
		VerifiedAt:         r.VerifiedAt.Ptr(),
		VerifiedBy:         r.VerifiedByUserID.String,
		CreatedAt:          r.CreatedAt.Time,
		UpdatedAt:          r.UpdatedAt.Time,
		RevokedAt:          r.RevokedAt.Ptr(),
	}, nil
}

func (c *mysqlConn) fromListRow(r gendb.ListOAuthClientsRow) (store.OAuthClient, error) {
	return c.fromGetOAuthClientRow(gendb.GetOAuthClientRow(r))
}

func (s *oauthClientStore) Create(ctx context.Context, p store.CreateOAuthClientParams) (*store.OAuthClient, error) {
	clientName, err := s.conn.ks.SealString(p.ClientName, keystore.OAuthClientNameAAD(p.ClientID))
	if err != nil {
		return nil, err
	}
	iconBlob, err := s.conn.ks.SealNonEmptyBytes(p.IconBlob, keystore.OAuthClientIconBlobAAD(p.ClientID))
	if err != nil {
		return nil, err
	}
	iconMediaType, err := s.conn.ks.SealNonEmptyString(p.IconMediaType, keystore.OAuthClientIconMediaTypeAAD(p.ClientID))
	if err != nil {
		return nil, err
	}
	clientURI, err := s.conn.ks.SealString(p.ClientURI, keystore.OAuthClientURIAAD(p.ClientID))
	if err != nil {
		return nil, err
	}
	redirectURIs, err := s.conn.ks.SealString(p.RedirectURIs, keystore.OAuthClientRedirectURIsAAD(p.ClientID))
	if err != nil {
		return nil, err
	}
	scopes, err := s.conn.ks.SealString(p.Scopes, keystore.OAuthClientScopesAAD(p.ClientID))
	if err != nil {
		return nil, err
	}
	grantTypes, err := s.conn.ks.SealString(p.GrantTypes, keystore.OAuthClientGrantTypesAAD(p.ClientID))
	if err != nil {
		return nil, err
	}
	// MySQL has no INSERT ... RETURNING, so the stored row comes from the
	// follow-up read this dialect's established idiom uses (see users.sql's
	// Create). One extra indexed round trip on a rare elevated verb.
	if err := s.conn.queryError(s.conn.q.CreateOAuthClient(ctx, gendb.CreateOAuthClientParams{
		ClientID: p.ClientID,
		// An EMPTY owner is SQL NULL, which is what makes the app hub-wide.
		// Binding "" instead would create a row nobody owns and nobody sees,
		// because no users.id is ever empty.
		OwnerUserID:        sqlutil.NullNonEmpty(p.OwnerUserID),
		SecretHash:         p.SecretHash,
		ClientName:         clientName,
		IconBlob:           iconBlob,
		IconMediaType:      iconMediaType,
		ClientUri:          clientURI,
		RedirectUris:       redirectURIs,
		Scopes:             scopes,
		GrantTypes:         grantTypes,
		ElevationAllowed:   p.ElevationAllowed,
		RegistrationSource: p.RegistrationSource,
		VerifiedAt:         sqltime.NewMySQLNullTime(p.VerifiedAt),
		VerifiedByUserID:   sqlutil.NullNonEmpty(p.VerifiedBy),
	})); err != nil {
		return nil, err
	}
	return s.Get(ctx, p.ClientID)
}

func (s *oauthClientStore) Get(ctx context.Context, clientID string) (*store.OAuthClient, error) {
	row, err := s.conn.q.GetOAuthClient(ctx, clientID)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	out, err := s.conn.fromGetOAuthClientRow(row)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *oauthClientStore) LockForElevation(ctx context.Context, clientID string) error {
	if !s.conn.inTx() {
		return store.ErrLockingReadOutsideTransaction
	}
	_, err := s.conn.q.LockOAuthClientForElevation(ctx, clientID)
	return s.conn.queryError(err)
}

func (s *oauthClientStore) LockActive(ctx context.Context, clientID string) error {
	if !s.conn.inTx() {
		return store.ErrLockingReadOutsideTransaction
	}
	_, err := s.conn.q.LockActiveOAuthClient(ctx, clientID)
	return s.conn.queryError(err)
}

func (s *oauthClientStore) GetIcon(ctx context.Context, clientID string) (*store.OAuthClientIcon, error) {
	row, err := s.conn.q.GetOAuthClientIcon(ctx, clientID)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	iconBlob, err := s.conn.ks.OpenOptionalBytes(row.IconBlob, keystore.OAuthClientIconBlobAAD(clientID))
	if err != nil {
		return nil, err
	}
	iconMediaType, err := s.conn.ks.OpenStringOrDefault(row.IconMediaType, keystore.OAuthClientIconMediaTypeAAD(clientID), "")
	if err != nil {
		return nil, err
	}
	return &store.OAuthClientIcon{
		IconBlob:           iconBlob,
		IconMediaType:      iconMediaType,
		VerifiedAt:         row.VerifiedAt.Ptr(),
		RegistrationSource: row.RegistrationSource,
		RevokedAt:          row.RevokedAt.Ptr(),
	}, nil
}

func (s *oauthClientStore) UpsertBuiltIn(ctx context.Context, p store.UpsertBuiltInClientParams) error {
	clientName, err := s.conn.ks.SealString(p.ClientName, keystore.OAuthClientNameAAD(p.ClientID))
	if err != nil {
		return err
	}
	clientURI, err := s.conn.ks.SealString(p.ClientURI, keystore.OAuthClientURIAAD(p.ClientID))
	if err != nil {
		return err
	}
	redirectURIs, err := s.conn.ks.SealString(p.RedirectURIs, keystore.OAuthClientRedirectURIsAAD(p.ClientID))
	if err != nil {
		return err
	}
	scopes, err := s.conn.ks.SealString(p.Scopes, keystore.OAuthClientScopesAAD(p.ClientID))
	if err != nil {
		return err
	}
	grantTypes, err := s.conn.ks.SealString(p.GrantTypes, keystore.OAuthClientGrantTypesAAD(p.ClientID))
	if err != nil {
		return err
	}
	return s.conn.queryError(s.conn.q.UpsertBuiltInOAuthClient(ctx, gendb.UpsertBuiltInOAuthClientParams{
		ClientID:           p.ClientID,
		ClientName:         clientName,
		ClientUri:          clientURI,
		RedirectUris:       redirectURIs,
		Scopes:             scopes,
		GrantTypes:         grantTypes,
		ElevationAllowed:   p.ElevationAllowed,
		RegistrationSource: p.RegistrationSource,
		CreatedAt:          sqltime.NewMySQLTime(p.CreatedAt),
		UpdatedAt:          sqltime.NewMySQLTime(p.UpdatedAt),
	}))
}

func (s *oauthClientStore) List(ctx context.Context, p store.ListOAuthClientsParams) (store.Page[store.OAuthClient], error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing. Binding "" would still list every
		// hub-wide app, which is arguably harmless -- but this listing answers
		// "what may THIS user see", and there is no such user.
		return store.Page[store.OAuthClient]{}, nil
	}
	return store.QueryPageErr(ctx, p.Limit,
		func() (gendb.ListOAuthClientsParams, error) {
			return listOAuthClientsParams(owner, p)
		},
		s.conn.q.ListOAuthClients, s.conn.fromListRow, s.conn.queryError)
}

func (s *oauthClientStore) Update(ctx context.Context, p store.UpdateOAuthClientParams) (int64, error) {
	// The guard is visible AT the query, deliberately: an unminted caller must
	// be refused before it binds. A zero id unwraps to "", which would MATCH
	// every blank-owner row instead of none -- and although a hub-wide app's
	// owner is SQL NULL rather than "", depending on that is exactly the
	// reasoning userid.OwnerFilter exists to remove.
	owner, ok := userid.OwnerFilter(p.CallerUserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	clientName, err := s.conn.ks.SealString(p.ClientName, keystore.OAuthClientNameAAD(p.ClientID))
	if err != nil {
		return 0, err
	}
	clientURI, err := s.conn.ks.SealString(p.ClientURI, keystore.OAuthClientURIAAD(p.ClientID))
	if err != nil {
		return 0, err
	}
	redirectURIs, err := s.conn.ks.SealString(p.RedirectURIs, keystore.OAuthClientRedirectURIsAAD(p.ClientID))
	if err != nil {
		return 0, err
	}
	scopes, err := s.conn.ks.SealString(p.Scopes, keystore.OAuthClientScopesAAD(p.ClientID))
	if err != nil {
		return 0, err
	}
	grantTypes, err := s.conn.ks.SealString(p.GrantTypes, keystore.OAuthClientGrantTypesAAD(p.ClientID))
	if err != nil {
		return 0, err
	}
	return s.conn.rowsAffected(s.conn.q.UpdateOAuthClient(ctx, gendb.UpdateOAuthClientParams{
		BuiltinSource: store.OAuthClientSourceBuiltin,
		ClientID:      p.ClientID,
		ClientName:    clientName,
		ClientUri:     clientURI,
		RedirectUris:  redirectURIs,
		Scopes:        scopes,
		GrantTypes:    grantTypes,
		CallerIsAdmin: p.CallerIsAdmin,
		// An unminted caller matches no OWNED row, because no users.id is ever
		// empty -- so the hub-wide arm (which needs CallerIsAdmin) is the only
		// one such a caller could satisfy, and only if the service really did
		// resolve an administrator.
		CallerUserID: sql.NullString{String: owner, Valid: true},
	}))
}

func (s *oauthClientStore) SetElevationAllowed(ctx context.Context, p store.SetOAuthClientElevationAllowedParams) (int64, error) {
	// The guard is visible AT the query, deliberately: an unminted caller must
	// be refused before it binds. A zero id unwraps to "", which would MATCH
	// every blank-owner row instead of none -- and although a hub-wide app's
	// owner is SQL NULL rather than "", depending on that is exactly the
	// reasoning userid.OwnerFilter exists to remove.
	owner, ok := userid.OwnerFilter(p.CallerUserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	return s.conn.rowsAffected(s.conn.q.SetOAuthClientElevationAllowed(ctx, gendb.SetOAuthClientElevationAllowedParams{
		ClientID:         p.ClientID,
		ElevationAllowed: p.ElevationAllowed,
		CallerIsAdmin:    p.CallerIsAdmin,
		CallerUserID:     sql.NullString{String: owner, Valid: true},
	}))
}

func (s *oauthClientStore) SetIcon(ctx context.Context, p store.SetOAuthClientIconParams) (int64, error) {
	// The guard is visible AT the query, deliberately: an unminted caller must
	// be refused before it binds. A zero id unwraps to "", which would MATCH
	// every blank-owner row instead of none -- and although a hub-wide app's
	// owner is SQL NULL rather than "", depending on that is exactly the
	// reasoning userid.OwnerFilter exists to remove.
	owner, ok := userid.OwnerFilter(p.CallerUserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	iconBlob, err := s.conn.ks.SealNonEmptyBytes(p.IconBlob, keystore.OAuthClientIconBlobAAD(p.ClientID))
	if err != nil {
		return 0, err
	}
	iconMediaType, err := s.conn.ks.SealNonEmptyString(p.IconMediaType, keystore.OAuthClientIconMediaTypeAAD(p.ClientID))
	if err != nil {
		return 0, err
	}
	return s.conn.rowsAffected(s.conn.q.SetOAuthClientIcon(ctx, gendb.SetOAuthClientIconParams{
		BuiltinSource: store.OAuthClientSourceBuiltin,
		ClientID:      p.ClientID,
		IconBlob:      iconBlob,
		IconMediaType: iconMediaType,
		CallerIsAdmin: p.CallerIsAdmin,
		CallerUserID:  sql.NullString{String: owner, Valid: true},
	}))
}

func (s *oauthClientStore) SetVerified(ctx context.Context, p store.SetOAuthClientVerifiedParams) (int64, error) {
	return s.conn.rowsAffected(s.conn.q.SetOAuthClientVerified(ctx, gendb.SetOAuthClientVerifiedParams{
		ClientID:         p.ClientID,
		VerifiedAt:       sqltime.NewMySQLNullTime(p.VerifiedAt),
		VerifiedByUserID: sqlutil.NullNonEmpty(p.VerifiedBy),
		CallerIsAdmin:    p.CallerIsAdmin,
	}))
}

func (s *oauthClientStore) Revoke(ctx context.Context, p store.OAuthClientOwnershipParams) (int64, error) {
	// The guard is visible AT the query, deliberately: an unminted caller must
	// be refused before it binds. A zero id unwraps to "", which would MATCH
	// every blank-owner row instead of none -- and although a hub-wide app's
	// owner is SQL NULL rather than "", depending on that is exactly the
	// reasoning userid.OwnerFilter exists to remove.
	owner, ok := userid.OwnerFilter(p.CallerUserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	return s.conn.rowsAffected(s.conn.q.RevokeOAuthClient(ctx, gendb.RevokeOAuthClientParams{
		BuiltinSource: store.OAuthClientSourceBuiltin,
		ClientID:      p.ClientID,
		CallerIsAdmin: p.CallerIsAdmin,
		CallerUserID:  sql.NullString{String: owner, Valid: true},
	}))
}

// Delete hard-deletes an app that never held a credential.
//
// It clears the app's DEVICE GRANTS and AUTHORIZATION CODES first, in the same
// transaction. Those reference the app under the same RESTRICT key but are
// one-shot artifacts of a flow -- ten minutes of life each -- so an
// app that ran a single abandoned device flow would otherwise be undeletable,
// and the operator met a foreign-key error naming a table they have no surface
// for. api_tokens is deliberately not cleared: a revoked credential is history,
// and the delete refuses while one exists.
//
// One TRANSACTION, because two of the three statements must not land without
// the third: clearing the grants and then failing the delete would discard live
// device flows for an app that survives.
func (s *oauthClientStore) Delete(ctx context.Context, p store.OAuthClientOwnershipParams) (int64, error) {
	// The guard is visible AT the query, deliberately: an unminted caller must
	// be refused before it binds. A zero id unwraps to "", which would MATCH
	// every blank-owner row instead of none -- and although a hub-wide app's
	// owner is SQL NULL rather than "", depending on that is exactly the
	// reasoning userid.OwnerFilter exists to remove.
	owner, ok := userid.OwnerFilter(p.CallerUserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	var deleted int64
	err := (&mysqlStore{conn: s.conn}).RunInTransaction(ctx, func(tx store.Store) error {
		deleted = 0
		conn := tx.(*mysqlStore).conn
		q := conn.q
		params := gendb.DeleteOAuthClientParams{
			BuiltinSource: store.OAuthClientSourceBuiltin,
			ClientID:      p.ClientID,
			CallerIsAdmin: p.CallerIsAdmin,
			CallerUserID:  sql.NullString{String: owner, Valid: true},
		}
		if _, err := q.LockDeletableOAuthClient(ctx, gendb.LockDeletableOAuthClientParams(params)); err != nil {
			if errors.Is(conn.queryError(err), store.ErrNotFound) {
				return nil
			}
			return conn.queryError(err)
		}
		if _, err := q.DeleteAuthorizationCodesForOAuthClient(ctx, p.ClientID); err != nil {
			return conn.queryError(err)
		}
		n, err := conn.rowsAffected(q.DeleteOAuthClient(ctx, params))
		deleted = n
		return err
	})
	if err != nil {
		return 0, mapErr(err)
	}
	return deleted, nil
}

func (s *oauthClientStore) ListTokenRefs(ctx context.Context, clientID string) ([]store.APITokenRef, error) {
	return s.conn.tokenRefsOf(s.conn.q.ListAPITokenIDsForOAuthClient(ctx, clientID))
}

func (s *oauthClientStore) ListUserTokenRefs(ctx context.Context, clientID string, user userid.UserID) ([]store.APITokenRef, error) {
	if user.IsZero() {
		return nil, store.ErrInvalidArgument
	}
	return s.conn.userTokenRefsOf(s.conn.q.ListUserAPITokenIDsForOAuthClient(ctx, gendb.ListUserAPITokenIDsForOAuthClientParams{
		ClientID: clientID,
		UserID:   user.String(),
	}))
}

// userTokenRefsOf maps the per-user query rows. Individual generated token
// rows support explicit conversion, but their slice types do not.
func (c *mysqlConn) userTokenRefsOf(rows []gendb.ListUserAPITokenIDsForOAuthClientRow, err error) ([]store.APITokenRef, error) {
	if err != nil {
		return nil, c.queryError(err)
	}
	out := make([]store.APITokenRef, 0, len(rows))
	for _, r := range rows {
		grantedScopes, err := c.ks.OpenString(r.GrantedScopes, keystore.APITokenGrantedScopesAAD(r.ID))
		if err != nil {
			return nil, err
		}
		out = append(out, store.APITokenRef{ID: r.ID, UserID: r.UserID, GrantedScopes: grantedScopes})
	}
	return out, nil
}

func (s *oauthClientStore) RevokeTokens(ctx context.Context, clientID string) (int64, error) {
	return s.conn.rowsAffected(s.conn.q.RevokeAPITokensForOAuthClient(ctx, clientID))
}

func (s *oauthClientStore) RevokeUserTokens(ctx context.Context, clientID string, user userid.UserID) (int64, error) {
	if user.IsZero() {
		return 0, store.ErrInvalidArgument
	}
	return s.conn.rowsAffected(s.conn.q.RevokeUserAPITokensForOAuthClient(ctx, gendb.RevokeUserAPITokensForOAuthClientParams{
		ClientID: clientID,
		UserID:   user.String(),
	}))
}

// tokenRefsOf maps the listing statements' rows onto the store shape both
// cascades' callers read.
func (c *mysqlConn) tokenRefsOf(rows []gendb.ListAPITokenIDsForOAuthClientRow, err error) ([]store.APITokenRef, error) {
	if err != nil {
		return nil, c.queryError(err)
	}
	out := make([]store.APITokenRef, 0, len(rows))
	for _, r := range rows {
		grantedScopes, err := c.ks.OpenString(r.GrantedScopes, keystore.APITokenGrantedScopesAAD(r.ID))
		if err != nil {
			return nil, err
		}
		out = append(out, store.APITokenRef{ID: r.ID, UserID: r.UserID, GrantedScopes: grantedScopes})
	}
	return out, nil
}

// CountTokens counts EVERY credential row, revoked included, because that is
// what the RESTRICT foreign key counts.
func (s *oauthClientStore) CountTokens(ctx context.Context, clientID string) (int64, error) {
	n, err := s.conn.q.CountAPITokensForOAuthClient(ctx, clientID)
	if err != nil {
		return 0, s.conn.queryError(err)
	}
	return n, nil
}

func (s *oauthClientStore) CountLiveTokens(ctx context.Context, clientID string) (int64, error) {
	n, err := s.conn.q.CountLiveAPITokensForOAuthClient(ctx, clientID)
	if err != nil {
		return 0, s.conn.queryError(err)
	}
	return n, nil
}

func (s *oauthClientStore) CountLiveTokensByClients(ctx context.Context, clientIDs []string) (map[string]int64, error) {
	rows, err := s.conn.q.CountLiveAPITokensForOAuthClients(ctx, clientIDs)
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.ClientID] = r.LiveCredentials
	}
	return out, nil
}
