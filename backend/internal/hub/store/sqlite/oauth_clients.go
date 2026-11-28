package sqlite

import (
	"context"
	"database/sql"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type oauthClientStore struct{ conn *sqliteConn }

var _ store.OAuthClientStore = (*oauthClientStore)(nil)

// Get and List project icon length instead of its bytes. Their generated row
// types have identical fields and support explicit conversion.
// Create returns icon bytes, so its mapper derives HasIcon from the blob.
func (c *sqliteConn) fromOAuthClientFields(
	clientID string,
	ownerUserID sql.NullString,
	secretHash []byte,
	clientNameCt, clientURICt, redirectURIsCt, scopesCt, grantTypesCt keystore.Ciphertext,
	hasIcon bool,
	elevationAllowed bool,
	registrationSource leapmuxv1.AppRegistrationSource,
	verifiedAt sqltime.SQLiteNullTime,
	verifiedByUserID sql.NullString,
	createdAt, updatedAt sqltime.SQLiteTime,
	revokedAt sqltime.SQLiteNullTime,
) (store.OAuthClient, error) {
	opened, err := sqlutil.OpenOAuthClientFields(c.ks, clientID, clientNameCt, clientURICt, redirectURIsCt, scopesCt, grantTypesCt)
	if err != nil {
		return store.OAuthClient{}, err
	}
	return store.OAuthClient{
		ClientID:           clientID,
		OwnerUserID:        ownerUserID.String,
		SecretHash:         secretHash,
		ClientName:         opened.ClientName,
		HasIcon:            hasIcon,
		ClientURI:          opened.ClientURI,
		RedirectURIs:       opened.RedirectURIs,
		Scopes:             opened.Scopes,
		GrantTypes:         opened.GrantTypes,
		ElevationAllowed:   elevationAllowed,
		RegistrationSource: registrationSource,
		VerifiedAt:         verifiedAt.Ptr(),
		VerifiedBy:         verifiedByUserID.String,
		CreatedAt:          createdAt.Time,
		UpdatedAt:          updatedAt.Time,
		RevokedAt:          revokedAt.Ptr(),
	}, nil
}

func (c *sqliteConn) fromGetOAuthClientRow(r gendb.GetOAuthClientRow) (store.OAuthClient, error) {
	return c.fromOAuthClientFields(
		r.ClientID, r.OwnerUserID, r.SecretHash,
		r.ClientName, r.ClientUri, r.RedirectUris, r.Scopes, r.GrantTypes,
		sqlutil.CoerceInt64(r.IconBytes) > 0,
		r.ElevationAllowed, r.RegistrationSource,
		r.VerifiedAt, r.VerifiedByUserID, r.CreatedAt, r.UpdatedAt, r.RevokedAt,
	)
}

func (c *sqliteConn) fromCreateRow(r gendb.CreateOAuthClientRow) (store.OAuthClient, error) {
	return c.fromOAuthClientFields(
		r.ClientID, r.OwnerUserID, r.SecretHash,
		r.ClientName, r.ClientUri, r.RedirectUris, r.Scopes, r.GrantTypes,
		r.IconBlob.Valid(),
		r.ElevationAllowed, r.RegistrationSource,
		r.VerifiedAt, r.VerifiedByUserID, r.CreatedAt, r.UpdatedAt, r.RevokedAt,
	)
}

func (c *sqliteConn) fromListRow(r gendb.ListOAuthClientsRow) (store.OAuthClient, error) {
	return c.fromGetOAuthClientRow(gendb.GetOAuthClientRow(r))
}

func (s *oauthClientStore) Create(ctx context.Context, p store.CreateOAuthClientParams) (*store.OAuthClient, error) {
	sealed, err := sqlutil.SealOAuthClientFields(s.conn.ks, p.ClientID, p.ClientName, p.ClientURI, p.RedirectURIs, p.Scopes, p.GrantTypes)
	if err != nil {
		return nil, err
	}
	iconBlob, iconMediaType, err := sqlutil.SealOAuthClientIcon(s.conn.ks, p.ClientID, p.IconBlob, p.IconMediaType)
	if err != nil {
		return nil, err
	}
	row, err := s.conn.q.CreateOAuthClient(ctx, gendb.CreateOAuthClientParams{
		ClientID: p.ClientID,
		// An EMPTY owner is SQL NULL, which is what makes the app hub-wide.
		// Binding "" instead would create a row nobody owns and nobody sees,
		// because no users.id is ever empty.
		OwnerUserID:        sqlutil.NullNonEmpty(p.OwnerUserID),
		SecretHash:         p.SecretHash,
		ClientName:         sealed.ClientName,
		IconBlob:           iconBlob,
		IconMediaType:      iconMediaType,
		ClientUri:          sealed.ClientURI,
		RedirectUris:       sealed.RedirectURIs,
		Scopes:             sealed.Scopes,
		GrantTypes:         sealed.GrantTypes,
		ElevationAllowed:   p.ElevationAllowed,
		RegistrationSource: p.RegistrationSource,
		VerifiedAt:         sqltime.NewSQLiteNullTime(p.VerifiedAt),
		VerifiedByUserID:   sqlutil.NullNonEmpty(p.VerifiedBy),
	})
	if err != nil {
		return nil, s.conn.queryError(err)
	}
	out, err := s.conn.fromCreateRow(row)
	if err != nil {
		return nil, err
	}
	return &out, nil
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
	iconBlob, iconMediaType, err := sqlutil.OpenOAuthClientIcon(s.conn.ks, clientID, row.IconBlob, row.IconMediaType)
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
	sealed, err := sqlutil.SealOAuthClientFields(s.conn.ks, p.ClientID, p.ClientName, p.ClientURI, p.RedirectURIs, p.Scopes, p.GrantTypes)
	if err != nil {
		return err
	}
	return s.conn.queryError(s.conn.q.UpsertBuiltInOAuthClient(ctx, gendb.UpsertBuiltInOAuthClientParams{
		ClientID:           p.ClientID,
		ClientName:         sealed.ClientName,
		ClientUri:          sealed.ClientURI,
		RedirectUris:       sealed.RedirectURIs,
		Scopes:             sealed.Scopes,
		GrantTypes:         sealed.GrantTypes,
		ElevationAllowed:   p.ElevationAllowed,
		RegistrationSource: p.RegistrationSource,
		CreatedAt:          sqltime.NewSQLiteTime(p.CreatedAt),
		UpdatedAt:          sqltime.NewSQLiteTime(p.UpdatedAt),
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
	filter, idGt, fetchLimit, active, empty, err := parseIDPrefixPage(p.IDPrefix, p.Cursor, p.Limit)
	if err != nil {
		return store.Page[store.OAuthClient]{}, err
	}
	if active {
		if empty {
			return store.Page[store.OAuthClient]{}, nil
		}
		ownerNull := sql.NullString{String: owner, Valid: true}
		includeRevoked := ptrconv.BoolToInt64(p.IncludeRevoked)
		hubWideOnly := ptrconv.BoolToInt64(p.HubWideOnly)
		includeHubWide := ptrconv.BoolToInt64(p.IncludeHubWide)
		if filter.Exact {
			return store.QueryPageErr(ctx, p.Limit,
				func() (gendb.ListOAuthClientsByIDExactParams, error) {
					return gendb.ListOAuthClientsByIDExactParams{
						ClientID:       filter.Lower,
						IncludeRevoked: includeRevoked,
						HubWideOnly:    hubWideOnly,
						UserID:         ownerNull,
						IncludeHubWide: includeHubWide,
					}, nil
				},
				s.conn.q.ListOAuthClientsByIDExact,
				func(r gendb.ListOAuthClientsByIDExactRow) (store.OAuthClient, error) {
					return s.conn.fromGetOAuthClientRow(gendb.GetOAuthClientRow(r))
				}, s.conn.queryError)
		}
		return store.QueryPageErr(ctx, p.Limit,
			func() (gendb.ListOAuthClientsByIDPrefixParams, error) {
				return gendb.ListOAuthClientsByIDPrefixParams{
					IDLo:           filter.Lower,
					IDHi:           filter.Upper,
					IDGt:           idGt,
					IncludeRevoked: includeRevoked,
					HubWideOnly:    hubWideOnly,
					UserID:         ownerNull,
					IncludeHubWide: includeHubWide,
					Limit:          fetchLimit,
				}, nil
			},
			s.conn.q.ListOAuthClientsByIDPrefix,
			func(r gendb.ListOAuthClientsByIDPrefixRow) (store.OAuthClient, error) {
				return s.conn.fromGetOAuthClientRow(gendb.GetOAuthClientRow(r))
			}, s.conn.queryError)
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
	sealed, err := sqlutil.SealOAuthClientFields(s.conn.ks, p.ClientID, p.ClientName, p.ClientURI, p.RedirectURIs, p.Scopes, p.GrantTypes)
	if err != nil {
		return 0, err
	}
	return s.conn.rowsAffected(s.conn.q.UpdateOAuthClient(ctx, gendb.UpdateOAuthClientParams{
		BuiltinSource: store.OAuthClientSourceBuiltin,
		ClientID:      p.ClientID,
		ClientName:    sealed.ClientName,
		ClientUri:     sealed.ClientURI,
		RedirectUris:  sealed.RedirectURIs,
		Scopes:        sealed.Scopes,
		GrantTypes:    sealed.GrantTypes,
		CallerIsAdmin: p.CallerIsAdmin,
		CallerUserID:  sql.NullString{String: owner, Valid: true},
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
	iconBlob, iconMediaType, err := sqlutil.SealOAuthClientIcon(s.conn.ks, p.ClientID, p.IconBlob, p.IconMediaType)
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
		VerifiedAt:       sqltime.NewSQLiteNullTime(p.VerifiedAt),
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
	err := (&sqliteStore{conn: s.conn}).RunInTransaction(ctx, func(tx store.Store) error {
		deleted = 0
		conn := tx.(*sqliteStore).conn
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
	rows, err := s.conn.q.ListUserAPITokenIDsForOAuthClient(ctx, gendb.ListUserAPITokenIDsForOAuthClientParams{
		ClientID: clientID,
		UserID:   user.String(),
	})
	converted := make([]gendb.ListAPITokenIDsForOAuthClientRow, len(rows))
	for i, r := range rows {
		converted[i] = gendb.ListAPITokenIDsForOAuthClientRow(r)
	}
	return s.conn.tokenRefsOf(converted, err)
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
func (c *sqliteConn) tokenRefsOf(rows []gendb.ListAPITokenIDsForOAuthClientRow, err error) ([]store.APITokenRef, error) {
	if err != nil {
		return nil, c.queryError(err)
	}
	out := make([]store.APITokenRef, 0, len(rows))
	for _, r := range rows {
		grantedScopes, err := c.ks.OpenString(r.GrantedScopes, sqlutil.APITokenGrantedScopesAAD(r.ID))
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
