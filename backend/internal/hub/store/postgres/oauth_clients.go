package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type oauthClientStore struct{ conn *pgConn }

var _ store.OAuthClientStore = (*oauthClientStore)(nil)

// The full-row queries project every column except the icon BYTES, so each
// maps through one of these; they state the same 17 fields because the
// generated Row types are distinct per query and Go has no structural typing.
// The icon reads HasIcon from the projected LENGTH, never the blob itself.
func fromGetOAuthClientRow(c gendb.GetOAuthClientRow) store.OAuthClient {
	return store.OAuthClient{
		ClientID:           c.ClientID,
		OwnerUserID:        c.OwnerUserID.String,
		CreatedBy:          c.CreatedByUserID.String,
		SecretHash:         c.SecretHash,
		ClientName:         c.ClientName,
		HasIcon:            sqlutil.CoerceInt64(c.IconBytes) > 0,
		ClientURI:          c.ClientUri,
		RedirectURIs:       c.RedirectUris,
		Scopes:             c.Scopes,
		GrantTypes:         c.GrantTypes,
		ElevationAllowed:   c.ElevationAllowed,
		RegistrationSource: c.RegistrationSource,
		VerifiedAt:         c.VerifiedAt.Ptr(),
		VerifiedBy:         c.VerifiedByUserID.String,
		CreatedAt:          c.CreatedAt.Time,
		UpdatedAt:          c.UpdatedAt.Time,
		RevokedAt:          c.RevokedAt.Ptr(),
	}
}

func fromListVisibleToRow(c gendb.ListOAuthClientsVisibleToRow) store.OAuthClient {
	return store.OAuthClient{
		ClientID:           c.ClientID,
		OwnerUserID:        c.OwnerUserID.String,
		CreatedBy:          c.CreatedByUserID.String,
		SecretHash:         c.SecretHash,
		ClientName:         c.ClientName,
		HasIcon:            sqlutil.CoerceInt64(c.IconBytes) > 0,
		ClientURI:          c.ClientUri,
		RedirectURIs:       c.RedirectUris,
		Scopes:             c.Scopes,
		GrantTypes:         c.GrantTypes,
		ElevationAllowed:   c.ElevationAllowed,
		RegistrationSource: c.RegistrationSource,
		VerifiedAt:         c.VerifiedAt.Ptr(),
		VerifiedBy:         c.VerifiedByUserID.String,
		CreatedAt:          c.CreatedAt.Time,
		UpdatedAt:          c.UpdatedAt.Time,
		RevokedAt:          c.RevokedAt.Ptr(),
	}
}

func fromListOwnedByRow(c gendb.ListOAuthClientsOwnedByRow) store.OAuthClient {
	return store.OAuthClient{
		ClientID:           c.ClientID,
		OwnerUserID:        c.OwnerUserID.String,
		CreatedBy:          c.CreatedByUserID.String,
		SecretHash:         c.SecretHash,
		ClientName:         c.ClientName,
		HasIcon:            sqlutil.CoerceInt64(c.IconBytes) > 0,
		ClientURI:          c.ClientUri,
		RedirectURIs:       c.RedirectUris,
		Scopes:             c.Scopes,
		GrantTypes:         c.GrantTypes,
		ElevationAllowed:   c.ElevationAllowed,
		RegistrationSource: c.RegistrationSource,
		VerifiedAt:         c.VerifiedAt.Ptr(),
		VerifiedBy:         c.VerifiedByUserID.String,
		CreatedAt:          c.CreatedAt.Time,
		UpdatedAt:          c.UpdatedAt.Time,
		RevokedAt:          c.RevokedAt.Ptr(),
	}
}

func (s *oauthClientStore) Create(ctx context.Context, p store.CreateOAuthClientParams) error {
	return mapErr(s.conn.q.CreateOAuthClient(ctx, gendb.CreateOAuthClientParams{
		ClientID: p.ClientID,
		// An EMPTY owner is SQL NULL, which is what makes the app hub-wide.
		// Binding "" instead would create a row nobody owns and nobody sees,
		// because no users.id is ever empty.
		OwnerUserID:        textNonEmpty(p.OwnerUserID),
		CreatedByUserID:    textNonEmpty(p.CreatedBy),
		SecretHash:         p.SecretHash,
		ClientName:         p.ClientName,
		IconBlob:           p.IconBlob,
		IconMediaType:      p.IconMediaType,
		ClientUri:          p.ClientURI,
		RedirectUris:       p.RedirectURIs,
		Scopes:             p.Scopes,
		GrantTypes:         p.GrantTypes,
		ElevationAllowed:   p.ElevationAllowed,
		RegistrationSource: p.RegistrationSource,
		VerifiedAt:         pgtime.NewNull(p.VerifiedAt),
		VerifiedByUserID:   textNonEmpty(p.VerifiedBy),
	}))
}

func (s *oauthClientStore) Get(ctx context.Context, clientID string) (*store.OAuthClient, error) {
	row, err := s.conn.q.GetOAuthClient(ctx, clientID)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromGetOAuthClientRow(row)
	return &out, nil
}

func (s *oauthClientStore) GetIcon(ctx context.Context, clientID string) (*store.OAuthClientIcon, error) {
	row, err := s.conn.q.GetOAuthClientIcon(ctx, clientID)
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.OAuthClientIcon{
		IconBlob:           row.IconBlob,
		IconMediaType:      row.IconMediaType,
		VerifiedAt:         row.VerifiedAt.Ptr(),
		RegistrationSource: row.RegistrationSource,
		RevokedAt:          row.RevokedAt.Ptr(),
	}, nil
}

func (s *oauthClientStore) ListVisibleTo(ctx context.Context, p store.ListOAuthClientsParams) (store.Page[store.OAuthClient], error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing. Binding "" would still list every
		// hub-wide app, which is arguably harmless -- but this listing answers
		// "what may THIS user authorize", and there is no such user.
		return store.Page[store.OAuthClient]{}, nil
	}
	return queryPage(ctx, p.Limit,
		func() (gendb.ListOAuthClientsVisibleToParams, error) {
			return listOAuthClientsVisibleToParams(owner, p.Cursor, p.Limit, p.IncludeRevoked)
		},
		s.conn.q.ListOAuthClientsVisibleTo, fromListVisibleToRow)
}

func (s *oauthClientStore) ListOwnedBy(ctx context.Context, p store.ListOAuthClientsParams) (store.Page[store.OAuthClient], error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		return store.Page[store.OAuthClient]{}, nil
	}
	return queryPage(ctx, p.Limit,
		func() (gendb.ListOAuthClientsOwnedByParams, error) {
			return listOAuthClientsOwnedByParams(owner, p.Cursor, p.Limit, p.IncludeRevoked)
		},
		s.conn.q.ListOAuthClientsOwnedBy, fromListOwnedByRow)
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
	return rowsAffected(s.conn.q.UpdateOAuthClient(ctx, gendb.UpdateOAuthClientParams{
		ClientID:         p.ClientID,
		ClientName:       p.ClientName,
		ClientUri:        p.ClientURI,
		RedirectUris:     p.RedirectURIs,
		Scopes:           p.Scopes,
		GrantTypes:       p.GrantTypes,
		ElevationAllowed: p.ElevationAllowed,
		CallerIsAdmin:    p.CallerIsAdmin,
		// An unminted caller matches no OWNED row, because no users.id is ever
		// empty -- so the hub-wide arm (which needs CallerIsAdmin) is the only
		// one such a caller could satisfy, and only if the service really did
		// resolve an administrator.
		CallerUserID: pgtype.Text{String: owner, Valid: true},
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
	return rowsAffected(s.conn.q.SetOAuthClientElevationAllowed(ctx, gendb.SetOAuthClientElevationAllowedParams{
		ClientID:         p.ClientID,
		ElevationAllowed: p.ElevationAllowed,
		CallerIsAdmin:    p.CallerIsAdmin,
		CallerUserID:     pgtype.Text{String: owner, Valid: true},
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
	return rowsAffected(s.conn.q.SetOAuthClientIcon(ctx, gendb.SetOAuthClientIconParams{
		ClientID:      p.ClientID,
		IconBlob:      p.IconBlob,
		IconMediaType: p.IconMediaType,
		CallerIsAdmin: p.CallerIsAdmin,
		CallerUserID:  pgtype.Text{String: owner, Valid: true},
	}))
}

func (s *oauthClientStore) SetVerified(ctx context.Context, p store.SetOAuthClientVerifiedParams) (int64, error) {
	return rowsAffected(s.conn.q.SetOAuthClientVerified(ctx, gendb.SetOAuthClientVerifiedParams{
		ClientID:         p.ClientID,
		VerifiedAt:       pgtime.NewNull(p.VerifiedAt),
		VerifiedByUserID: textNonEmpty(p.VerifiedBy),
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
	return rowsAffected(s.conn.q.RevokeOAuthClient(ctx, gendb.RevokeOAuthClientParams{
		ClientID:      p.ClientID,
		CallerIsAdmin: p.CallerIsAdmin,
		CallerUserID:  pgtype.Text{String: owner, Valid: true},
	}))
}

// Delete hard-deletes an app that never held a credential.
//
// It clears the app's DEVICE GRANTS and AUTHORIZATION CODES first, in the same
// transaction. Those reference the app under the same RESTRICT key but are
// one-shot artifacts of a flow -- ten minutes and one minute of life -- so an
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
	err := (&pgStore{conn: s.conn}).RunInTransaction(ctx, func(tx store.Store) error {
		q := tx.(*pgStore).conn.q
		if _, err := q.DeleteEphemeralGrantsForOAuthClient(ctx, p.ClientID); err != nil {
			return err
		}
		if _, err := q.DeleteAuthorizationCodesForOAuthClient(ctx, p.ClientID); err != nil {
			return err
		}
		n, err := rowsAffected(q.DeleteOAuthClient(ctx, gendb.DeleteOAuthClientParams{
			ClientID:      p.ClientID,
			CallerIsAdmin: p.CallerIsAdmin,
			CallerUserID:  pgtype.Text{String: owner, Valid: true},
		}))
		deleted = n
		return err
	})
	if err != nil {
		return 0, mapErr(err)
	}
	return deleted, nil
}

func (s *oauthClientStore) ListTokenRefs(ctx context.Context, p store.RevokeAPITokensForClientParams) ([]store.APITokenRef, error) {
	rows, err := s.conn.q.ListAPITokenIDsForOAuthClient(ctx, gendb.ListAPITokenIDsForOAuthClientParams{
		ClientID: p.ClientID,
		UserID:   p.UserID,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]store.APITokenRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.APITokenRef{ID: r.ID, UserID: r.UserID, GrantedScopes: r.GrantedScopes})
	}
	return out, nil
}

func (s *oauthClientStore) RevokeTokens(ctx context.Context, p store.RevokeAPITokensForClientParams) (int64, error) {
	return rowsAffected(s.conn.q.RevokeAPITokensForOAuthClient(ctx, gendb.RevokeAPITokensForOAuthClientParams{
		ClientID: p.ClientID,
		UserID:   p.UserID,
	}))
}

// CountTokens counts EVERY credential row, revoked included, because that is
// what the RESTRICT foreign key counts.
func (s *oauthClientStore) CountTokens(ctx context.Context, clientID string) (int64, error) {
	n, err := s.conn.q.CountAPITokensForOAuthClient(ctx, clientID)
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

func (s *oauthClientStore) CountLiveTokens(ctx context.Context, clientID string) (int64, error) {
	n, err := s.conn.q.CountLiveAPITokensForOAuthClient(ctx, clientID)
	if err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

func (s *oauthClientStore) CountLiveTokensByClients(ctx context.Context, clientIDs []string) (map[string]int64, error) {
	rows, err := s.conn.q.CountLiveAPITokensForOAuthClients(ctx, clientIDs)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.ClientID] = r.LiveCredentials
	}
	return out, nil
}
