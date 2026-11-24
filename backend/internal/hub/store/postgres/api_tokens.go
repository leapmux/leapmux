package postgres

import (
	"context"
	"fmt"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type apiTokenStore struct{ conn *pgConn }

var _ store.APITokenStore = (*apiTokenStore)(nil)

// joinedClient carries the four oauth_clients columns every api_tokens read
// selects alongside the row. It is a PARAMETER of the converter rather than a
// later assignment, so a query that forgets the join fails to compile instead
// of producing a credential whose app is silently blank -- which would read as
// "not revoked", "may not elevate" and, worse, "reaches nothing", since an
// empty ceiling narrows every grant to the empty set.
type joinedClient struct {
	Name             keystore.Ciphertext
	Scopes           keystore.Ciphertext
	ElevationAllowed bool
	RevokedAt        *time.Time
}

// joinedClientOf adapts the generated row's four client columns. See the
// sqlite twin.
func joinedClientOf(name, scopes keystore.Ciphertext, elevationAllowed bool, revokedAt pgtime.NullTime) joinedClient {
	return joinedClient{Name: name, Scopes: scopes, ElevationAllowed: elevationAllowed, RevokedAt: revokedAt.Ptr()}
}

func (c *pgConn) fromDBAPIToken(t gendb.ApiToken, jc joinedClient, clientCache *sqlutil.JoinedClientCache) (store.APIToken, error) {
	var clientName, clientScopes string
	var err error
	if clientCache != nil {
		clientName, clientScopes, err = clientCache.Open(t.ClientID, jc.Name, jc.Scopes)
	} else {
		clientName, err = c.ks.OpenString(jc.Name, sqlutil.OAuthClientNameAAD(t.ClientID))
		if err == nil {
			clientScopes, err = c.ks.OpenString(jc.Scopes, sqlutil.OAuthClientScopesAAD(t.ClientID))
		}
	}
	if err != nil {
		return store.APIToken{}, err
	}
	installationName, err := c.ks.OpenString(t.InstallationName, sqlutil.APITokenInstallationNameAAD(t.ID))
	if err != nil {
		return store.APIToken{}, err
	}
	grantedScopes, err := c.ks.OpenString(t.GrantedScopes, sqlutil.APITokenGrantedScopesAAD(t.ID))
	if err != nil {
		return store.APIToken{}, err
	}
	return store.APIToken{
		ClientName:               clientName,
		ClientScopes:             clientScopes,
		ClientElevationAllowed:   jc.ElevationAllowed,
		ClientRevokedAt:          jc.RevokedAt,
		ID:                       t.ID,
		UserID:                   t.UserID,
		ClientID:                 t.ClientID,
		InstallationName:         installationName,
		GrantedScopes:            grantedScopes,
		Issuance:                 store.APITokenIssuance(t.Issuance),
		SecretHash:               t.SecretHash,
		RefreshHash:              t.RefreshHash,
		PreviousRefreshHash:      t.PreviousRefreshHash,
		PreviousRefreshExpiresAt: t.PreviousRefreshExpiresAt.Ptr(),
		CreatedAt:                t.CreatedAt.Time,
		AuthGeneration:           t.AuthGeneration,
		LastUsedAt:               t.LastUsedAt.Ptr(),
		ExpiresAt:                t.ExpiresAt.Ptr(),
		RefreshExpiresAt:         t.RefreshExpiresAt.Ptr(),
		RevokedAt:                t.RevokedAt.Ptr(),
		ElevationProvenAt:        t.ElevationProvenAt.Ptr(),
		ElevationExpiresAt:       t.ElevationExpiresAt.Ptr(),
	}, nil
}

func (s *apiTokenStore) Create(ctx context.Context, p store.CreateAPITokenParams) error {
	installationName, err := s.conn.ks.SealString(p.InstallationName, sqlutil.APITokenInstallationNameAAD(p.ID))
	if err != nil {
		return err
	}
	grantedScopes, err := s.conn.ks.SealString(p.GrantedScopes, sqlutil.APITokenGrantedScopesAAD(p.ID))
	if err != nil {
		return err
	}
	return (&pgStore{conn: s.conn}).RunInUserAuthTransaction(ctx, p.UserID, func(tx store.Store) error {
		return mapErr(tx.(*pgStore).conn.q.CreateAPIToken(ctx, gendb.CreateAPITokenParams{
			ID:               p.ID,
			UserID:           p.UserID.String(),
			ClientID:         p.ClientID,
			InstallationName: installationName,
			GrantedScopes:    grantedScopes,
			Issuance:         leapmuxv1.ApiTokenIssuance(p.Issuance),
			SecretHash:       p.SecretHash,
			RefreshHash:      p.RefreshHash,
			ExpiresAt:        pgtime.NewNull(p.ExpiresAt),
			RefreshExpiresAt: pgtime.NewNull(p.RefreshExpiresAt),
		}))
	})
}

func (s *apiTokenStore) GetByID(ctx context.Context, id string) (*store.APIToken, error) {
	t, err := s.conn.q.GetAPITokenByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out, err := s.conn.fromDBAPIToken(t.ApiToken, joinedClientOf(t.ClientName, t.ClientScopes, t.ClientElevationAllowed, t.ClientRevokedAt), nil)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListByUser pages one user's OWN live tokens (the account settings device
// list). Keyset on created_at, like every other listing in the hub.
func (s *apiTokenStore) ListByUser(ctx context.Context, p store.ListAPITokensByUserParams) (store.Page[store.APIToken], error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return store.Page[store.APIToken]{}, nil
	}
	clientCache := sqlutil.NewJoinedClientCache(s.conn.ks)
	return queryPageErr(ctx, p.Limit,
		func() (gendb.ListAPITokensByUserParams, error) {
			return listAPITokensByUserParams(owner, p.ClientID, p.Cursor, p.Limit)
		},
		s.conn.q.ListAPITokensByUser,
		func(r gendb.ListAPITokensByUserRow) (store.APIToken, error) {
			out, err := s.conn.fromDBAPIToken(r.ApiToken, joinedClientOf(r.ClientName, r.ClientScopes, r.ClientElevationAllowed, r.ClientRevokedAt), clientCache)
			if err != nil {
				return store.APIToken{}, err
			}
			// Only this listing joins the vouch columns; every other
			// reader of an api_tokens row asks validation questions, and
			// the connected-app list is the one surface that labels rows
			// with whether somebody vouched for the app.
			out.ClientVerifiedAt = r.ClientVerifiedAt.Ptr()
			out.ClientRegistrationSource = r.ClientRegistrationSource
			return out, nil
		})
}

// apiTokenWithOwner assembles the JOINed listing row shared by the ListAll and
// ListAllByUser query twins (mirroring workerWithOwner), so a field addition
// to APITokenWithOwner edits one site instead of one closure per query.
func (c *pgConn) apiTokenWithOwner(t gendb.ApiToken, jc joinedClient, clientCache *sqlutil.JoinedClientCache, ownerUsername string, ownerDeleted bool) (store.APITokenWithOwner, error) {
	tok, err := c.fromDBAPIToken(t, jc, clientCache)
	if err != nil {
		return store.APITokenWithOwner{}, err
	}
	return store.APITokenWithOwner{APIToken: tok, OwnerUsername: ownerUsername, OwnerDeleted: ownerDeleted}, nil
}

func (s *apiTokenStore) ListAll(ctx context.Context, p store.ListAllAPITokensParams) (store.Page[store.APITokenWithOwner], error) {
	clientCache := sqlutil.NewJoinedClientCache(s.conn.ks)
	// The admin token listing is a 2x2 matrix over (user_id nil/set) x
	// (include_revoked false/true), dispatched to four generated queries
	// (mirroring workers.go ListAdmin). The revoked dimension is split rather
	// than an `(narg IS NULL OR revoked_at IS NULL)` probe because the live
	// listings' partial keyset indexes are only planner-eligible when the query
	// textually filters `revoked_at IS NULL`; the probe would deoptimize the
	// COMMON live path. The IncludingRevoked forensics variants intentionally
	// have no matching index -- see api_tokens.sql.
	switch {
	case p.UserID != nil && p.IncludeRevoked:
		return queryPageErr(ctx, p.Limit,
			func() (gendb.ListAllAPITokensByUserIncludingRevokedParams, error) {
				return listAllAPITokensByUserIncludingRevokedParams(*p.UserID, p.ClientID, p.Cursor, p.Limit)
			},
			s.conn.q.ListAllAPITokensByUserIncludingRevoked,
			func(r gendb.ListAllAPITokensByUserIncludingRevokedRow) (store.APITokenWithOwner, error) {
				return s.conn.apiTokenWithOwner(r.ApiToken, joinedClientOf(r.ClientName, r.ClientScopes, r.ClientElevationAllowed, r.ClientRevokedAt), clientCache, r.OwnerUsername, r.OwnerDeleted)
			})
	case p.UserID != nil:
		return queryPageErr(ctx, p.Limit,
			func() (gendb.ListAllAPITokensByUserParams, error) {
				return listAllAPITokensByUserParams(*p.UserID, p.ClientID, p.Cursor, p.Limit)
			},
			s.conn.q.ListAllAPITokensByUser,
			func(r gendb.ListAllAPITokensByUserRow) (store.APITokenWithOwner, error) {
				return s.conn.apiTokenWithOwner(r.ApiToken, joinedClientOf(r.ClientName, r.ClientScopes, r.ClientElevationAllowed, r.ClientRevokedAt), clientCache, r.OwnerUsername, r.OwnerDeleted)
			})
	case p.IncludeRevoked:
		return queryPageErr(ctx, p.Limit,
			func() (gendb.ListAllAPITokensIncludingRevokedParams, error) {
				return listAllAPITokensIncludingRevokedParams(p.ClientID, p.Cursor, p.Limit)
			},
			s.conn.q.ListAllAPITokensIncludingRevoked,
			func(r gendb.ListAllAPITokensIncludingRevokedRow) (store.APITokenWithOwner, error) {
				return s.conn.apiTokenWithOwner(r.ApiToken, joinedClientOf(r.ClientName, r.ClientScopes, r.ClientElevationAllowed, r.ClientRevokedAt), clientCache, r.OwnerUsername, r.OwnerDeleted)
			})
	default:
		return queryPageErr(ctx, p.Limit,
			func() (gendb.ListAllAPITokensParams, error) {
				return listAllAPITokensParams(p.ClientID, p.Cursor, p.Limit)
			},
			s.conn.q.ListAllAPITokens,
			func(r gendb.ListAllAPITokensRow) (store.APITokenWithOwner, error) {
				return s.conn.apiTokenWithOwner(r.ApiToken, joinedClientOf(r.ClientName, r.ClientScopes, r.ClientElevationAllowed, r.ClientRevokedAt), clientCache, r.OwnerUsername, r.OwnerDeleted)
			})
	}
}

func (s *apiTokenStore) Touch(ctx context.Context, id string) error {
	return mapErr(s.conn.q.TouchAPIToken(ctx, id))
}

func (s *apiTokenStore) RotateRefresh(ctx context.Context, p store.RotateAPITokenRefreshParams) (int64, error) {
	newGrantedScopes, err := s.conn.ks.SealString(p.NewGrantedScopes, sqlutil.APITokenGrantedScopesAAD(p.ID))
	if err != nil {
		return 0, err
	}
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		n, err := conn.q.RotateAPITokenRefresh(ctx, gendb.RotateAPITokenRefreshParams{
			ID:                   p.ID,
			NewGrantedScopes:     newGrantedScopes,
			NewSecretHash:        p.NewSecretHash,
			NewExpiresAt:         pgtime.NewNull(p.NewExpiresAt),
			NewRefreshHash:       p.NewRefreshHash,
			NewRefreshExpiresAt:  pgtime.NewNull(p.NewRefreshExpiresAt),
			PrevRefreshHash:      p.PreviousRefreshHash,
			PrevRefreshExpiresAt: pgtime.NewNull(p.PreviousRefreshExpiresAt),
		})
		if err != nil || n == 0 {
			return nil, mapErr(err)
		}
		if n != 1 {
			return nil, fmt.Errorf("rotate API token %q: updated %d rows", p.ID, n)
		}
		row, err := conn.q.GetAPITokenByID(ctx, p.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		return &store.CredentialEvent{Kind: store.RevocationEventKindAPITokenRotation, SubjectID: row.ApiToken.ID, UserID: row.ApiToken.UserID}, nil
	}, emitCredentialEvent)
}

func (s *apiTokenStore) Revoke(ctx context.Context, id string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		row, err := conn.q.RevokeAPIToken(ctx, id)
		return revokedCredentialEvent(row.ID, row.UserID, store.RevocationEventKindAPIToken, err)
	}, emitCredentialEvent)
}

func (s *apiTokenStore) RevokeOwned(ctx context.Context, p store.RevokeOwnedAPITokenParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, store.ErrInvalidArgument
	}
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		row, err := conn.q.RevokeOwnedAPIToken(ctx, gendb.RevokeOwnedAPITokenParams{ID: p.ID, UserID: owner})
		return revokedCredentialEvent(row.ID, row.UserID, store.RevocationEventKindAPIToken, err)
	}, emitCredentialEvent)
}

func (s *apiTokenStore) RevokeByUser(ctx context.Context, userID userid.UserID) (int64, error) {
	return s.RevokeOthers(ctx, store.RevokeOtherAPITokensParams{UserID: userID})
}

// RevokeOthers is the whole-set revocation with ONE exclusion; RevokeByUser
// above is this statement with no exclusion. One statement rather than two,
// so the predicate that decides which credentials survive a password change
// has one home.
func (s *apiTokenStore) RevokeOthers(ctx context.Context, p store.RevokeOtherAPITokensParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller specifies no user, so a bulk mutation must refuse
		// rather than address every blank-owner row -- or report success when
		// it changed nothing. See userid.OwnerFilter.
		return 0, store.ErrInvalidArgument
	}
	n, err := s.conn.q.RevokeOtherUserAPITokens(ctx, gendb.RevokeOtherUserAPITokensParams{
		UserID: owner,
		KeepID: p.KeepID,
	})
	return n, mapErr(err)
}

// RefreshAuthGeneration stamps the kept command-line credential onto the
// user's current auth_generation, exactly as the session twin does for the
// kept session. See the sessionStore method for why a no-op re-stamp still
// counts one row on each dialect.
func (s *apiTokenStore) RefreshAuthGeneration(ctx context.Context, p store.RefreshAPITokenAuthGenerationParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, nil
	}
	n, err := s.conn.q.RefreshUserAPITokenAuthGeneration(ctx, gendb.RefreshUserAPITokenAuthGenerationParams{
		TokenID: p.TokenID,
		UserID:  owner,
	})
	return n, mapErr(err)
}

// Elevate, SlideElevation and DropElevation are the only writers of
// elevation_proven_at / elevation_expires_at on api_tokens. Elevate and
// DropElevation emit a user_info event so a cross-process UserInfo cache
// re-reads the deadline; the slide does not, because a stale SHORTER deadline
// fails closed. The session store holds the same trio, for the same reasons.
func (s *apiTokenStore) Elevate(ctx context.Context, p store.ElevateAPITokenParams, now time.Time) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, store.ErrInvalidArgument
	}
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		n, err := conn.q.ElevateAPIToken(ctx, gendb.ElevateAPITokenParams{
			ElevationProvenAt:  pgtime.NullOf(p.ElevationProvenAt),
			ElevationExpiresAt: pgtime.NullOf(p.ClampedExpiresAt()),
			ID:                 p.TokenID,
			UserID:             owner,
			Now:                pgtime.NullOf(now),
		})
		if err != nil {
			return nil, mapErr(err)
		}
		if n == 0 {
			return nil, nil
		}
		return store.UserInfoEvent(owner), nil
	}, emitCredentialEvent)
}

func (s *apiTokenStore) SlideElevation(ctx context.Context, p store.SlideAPITokenElevationParams, now time.Time) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	n, err := s.conn.q.SlideAPITokenElevation(ctx, gendb.SlideAPITokenElevationParams{
		WindowDeadline: pgtime.NullOf(p.WindowDeadline),
		MaxTotalMicros: store.ElevationMaxTotal.Microseconds(),
		ID:             p.TokenID,
		UserID:         owner,
		Now:            pgtime.NullOf(now),
	})
	return n, mapErr(err)
}

func (s *apiTokenStore) DropElevation(ctx context.Context, p store.DropAPITokenElevationParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *pgConn) (*store.CredentialEvent, error) {
		n, err := conn.q.DropAPITokenElevation(ctx, gendb.DropAPITokenElevationParams{
			ID:     p.TokenID,
			UserID: owner,
		})
		if err != nil {
			return nil, mapErr(err)
		}
		if n == 0 {
			return nil, nil
		}
		return store.UserInfoEvent(owner), nil
	}, emitCredentialEvent)
}
