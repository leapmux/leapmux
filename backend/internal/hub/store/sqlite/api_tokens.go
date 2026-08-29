package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type apiTokenStore struct{ conn *sqliteConn }

var _ store.APITokenStore = (*apiTokenStore)(nil)

// joinedClient carries the four oauth_clients columns every api_tokens read
// selects alongside the row. It is a PARAMETER of the converter rather than a
// later assignment, so a query that forgets the join fails to compile instead
// of producing a credential whose app is silently blank -- which would read as
// "not revoked", "may not elevate" and, worse, "reaches nothing", since an
// empty ceiling narrows every grant to the empty set.
type joinedClient struct {
	Name             string
	Scopes           string
	ElevationAllowed bool
	RevokedAt        *time.Time
}

// joinedClientOf adapts the generated row's four client columns. One helper
// per dialect, because only the nullable timestamp differs between them.
//
// POSITIONAL, so a fifth joined column is a compile error at all seven call
// sites rather than a field somebody fills in six of them.
func joinedClientOf(name, scopes string, elevationAllowed bool, revokedAt sqltime.SQLiteNullTime) joinedClient {
	return joinedClient{Name: name, Scopes: scopes, ElevationAllowed: elevationAllowed, RevokedAt: revokedAt.Ptr()}
}

func fromDBAPIToken(t gendb.ApiToken, c joinedClient) store.APIToken {
	return store.APIToken{
		ClientName:               c.Name,
		ClientScopes:             c.Scopes,
		ClientElevationAllowed:   c.ElevationAllowed,
		ClientRevokedAt:          c.RevokedAt,
		ID:                       t.ID,
		UserID:                   t.UserID,
		ClientID:                 t.ClientID,
		InstallationName:         t.InstallationName,
		GrantedScopes:            t.GrantedScopes,
		SecretHash:               t.SecretHash,
		RefreshHash:              t.RefreshHash,
		PreviousRefreshHash:      t.PreviousRefreshHash,
		PreviousRefreshExpiresAt: t.PreviousRefreshExpiresAt.Ptr(),
		CreatedAt:                t.CreatedAt.Time,
		AuthGeneration:           t.AuthGeneration,
		LastUsedAt:               t.LastUsedAt.Ptr(),
		LastRotatedAt:            t.LastRotatedAt.Ptr(),
		ExpiresAt:                t.ExpiresAt.Ptr(),
		RefreshExpiresAt:         t.RefreshExpiresAt.Ptr(),
		RevokedAt:                t.RevokedAt.Ptr(),
		ElevationProvenAt:        t.ElevationProvenAt.Ptr(),
		ElevationExpiresAt:       t.ElevationExpiresAt.Ptr(),
	}
}

func (s *apiTokenStore) Create(ctx context.Context, p store.CreateAPITokenParams) error {
	return (&sqliteStore{conn: s.conn}).RunInUserAuthTransaction(ctx, p.UserID, func(tx store.Store) error {
		return mapErr(tx.(*sqliteStore).conn.q.CreateAPIToken(ctx, gendb.CreateAPITokenParams{
			ID:               p.ID,
			UserID:           p.UserID.String(),
			ClientID:         p.ClientID,
			InstallationName: p.InstallationName,
			GrantedScopes:    p.GrantedScopes,
			SecretHash:       p.SecretHash,
			RefreshHash:      p.RefreshHash,
			ExpiresAt:        sqltime.NewSQLiteNullTime(p.ExpiresAt),
			RefreshExpiresAt: sqltime.NewSQLiteNullTime(p.RefreshExpiresAt),
		}))
	})
}

func (s *apiTokenStore) GetByID(ctx context.Context, id string) (*store.APIToken, error) {
	t, err := s.conn.q.GetAPITokenByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBAPIToken(t.ApiToken, joinedClientOf(t.ClientName, t.ClientScopes, t.ClientElevationAllowed, t.ClientRevokedAt))
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
	return queryPage(ctx, p.Limit,
		func() (gendb.ListAPITokensByUserParams, error) {
			return listAPITokensByUserParams(owner, p.ClientID, p.Cursor, p.Limit)
		},
		s.conn.q.ListAPITokensByUser,
		func(r gendb.ListAPITokensByUserRow) store.APIToken {
			out := fromDBAPIToken(r.ApiToken, joinedClientOf(r.ClientName, r.ClientScopes, r.ClientElevationAllowed, r.ClientRevokedAt))
			// Only this listing joins the vouch columns; every other
			// reader of an api_tokens row asks validation questions, and
			// the connected-app list is the one surface that labels rows
			// with whether somebody vouched for the app.
			out.ClientVerifiedAt = r.ClientVerifiedAt.Ptr()
			out.ClientRegistrationSource = r.ClientRegistrationSource
			return out
		})
}

// apiTokenWithOwner assembles the JOINed listing row shared by the ListAll and
// ListAllByUser query twins (mirroring workerWithOwner), so a field addition
// to APITokenWithOwner edits one site instead of one closure per query.
func apiTokenWithOwner(t gendb.ApiToken, c joinedClient, ownerUsername string, ownerDeleted bool) store.APITokenWithOwner {
	return store.APITokenWithOwner{APIToken: fromDBAPIToken(t, c), OwnerUsername: ownerUsername, OwnerDeleted: ownerDeleted}
}

func (s *apiTokenStore) ListAll(ctx context.Context, p store.ListAllAPITokensParams) (store.Page[store.APITokenWithOwner], error) {
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
		return queryPage(ctx, p.Limit,
			func() (gendb.ListAllAPITokensByUserIncludingRevokedParams, error) {
				return listAllAPITokensByUserIncludingRevokedParams(*p.UserID, p.ClientID, p.Cursor, p.Limit)
			},
			s.conn.q.ListAllAPITokensByUserIncludingRevoked,
			func(r gendb.ListAllAPITokensByUserIncludingRevokedRow) store.APITokenWithOwner {
				return apiTokenWithOwner(r.ApiToken, joinedClientOf(r.ClientName, r.ClientScopes, r.ClientElevationAllowed, r.ClientRevokedAt), r.OwnerUsername, r.OwnerDeleted)
			})
	case p.UserID != nil:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListAllAPITokensByUserParams, error) {
				return listAllAPITokensByUserParams(*p.UserID, p.ClientID, p.Cursor, p.Limit)
			},
			s.conn.q.ListAllAPITokensByUser,
			func(r gendb.ListAllAPITokensByUserRow) store.APITokenWithOwner {
				return apiTokenWithOwner(r.ApiToken, joinedClientOf(r.ClientName, r.ClientScopes, r.ClientElevationAllowed, r.ClientRevokedAt), r.OwnerUsername, r.OwnerDeleted)
			})
	case p.IncludeRevoked:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListAllAPITokensIncludingRevokedParams, error) {
				return listAllAPITokensIncludingRevokedParams(p.ClientID, p.Cursor, p.Limit)
			},
			s.conn.q.ListAllAPITokensIncludingRevoked,
			func(r gendb.ListAllAPITokensIncludingRevokedRow) store.APITokenWithOwner {
				return apiTokenWithOwner(r.ApiToken, joinedClientOf(r.ClientName, r.ClientScopes, r.ClientElevationAllowed, r.ClientRevokedAt), r.OwnerUsername, r.OwnerDeleted)
			})
	default:
		return queryPage(ctx, p.Limit,
			func() (gendb.ListAllAPITokensParams, error) {
				return listAllAPITokensParams(p.ClientID, p.Cursor, p.Limit)
			},
			s.conn.q.ListAllAPITokens,
			func(r gendb.ListAllAPITokensRow) store.APITokenWithOwner {
				return apiTokenWithOwner(r.ApiToken, joinedClientOf(r.ClientName, r.ClientScopes, r.ClientElevationAllowed, r.ClientRevokedAt), r.OwnerUsername, r.OwnerDeleted)
			})
	}
}

func (s *apiTokenStore) Touch(ctx context.Context, id string) error {
	return mapErr(s.conn.q.TouchAPIToken(ctx, id))
}

func (s *apiTokenStore) RotateRefresh(ctx context.Context, p store.RotateAPITokenRefreshParams) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		n, err := rowsAffected(conn.q.RotateAPITokenRefresh(ctx, gendb.RotateAPITokenRefreshParams{
			ID:                   p.ID,
			NewGrantedScopes:     p.NewGrantedScopes,
			NewSecretHash:        p.NewSecretHash,
			NewExpiresAt:         sqltime.NewSQLiteNullTime(p.NewExpiresAt),
			NewRefreshHash:       p.NewRefreshHash,
			NewRefreshExpiresAt:  sqltime.NewSQLiteNullTime(p.NewRefreshExpiresAt),
			PrevRefreshHash:      p.PreviousRefreshHash,
			PrevRefreshExpiresAt: sqltime.NewSQLiteNullTime(p.PreviousRefreshExpiresAt),
		}))
		if err != nil || n == 0 {
			return nil, err
		}
		if n != 1 {
			return nil, fmt.Errorf("rotate API token %q: updated %d rows", p.ID, n)
		}
		row, err := conn.q.GetAPITokenByID(ctx, p.ID)
		if err != nil {
			return nil, mapErr(err)
		}
		rotatedAt, err := sqlutil.RequireTime(row.ApiToken.LastRotatedAt.Time, row.ApiToken.LastRotatedAt.Valid, "last_rotated_at")
		if err != nil {
			return nil, err
		}
		return &store.CredentialEvent{Kind: store.RevocationEventKindAPITokenRotation, SubjectID: row.ApiToken.ID, UserID: row.ApiToken.UserID, At: rotatedAt}, nil
	}, emitCredentialEvent)
}

func (s *apiTokenStore) Revoke(ctx context.Context, id string) (int64, error) {
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		row, err := conn.q.RevokeAPIToken(ctx, id)
		return revokedCredentialEvent(row.ID, row.UserID, row.RevokedAt, store.RevocationEventKindAPIToken, err)
	}, emitCredentialEvent)
}

func (s *apiTokenStore) RevokeOwned(ctx context.Context, p store.RevokeOwnedAPITokenParams) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		// An unminted caller owns nothing; binding "" would MATCH every
		// blank-owner row rather than none. See userid.OwnerFilter.
		return 0, store.ErrInvalidArgument
	}
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		row, err := conn.q.RevokeOwnedAPIToken(ctx, gendb.RevokeOwnedAPITokenParams{ID: p.ID, UserID: owner})
		return revokedCredentialEvent(row.ID, row.UserID, row.RevokedAt, store.RevocationEventKindAPIToken, err)
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
	return rowsAffected(s.conn.q.RevokeOtherUserAPITokens(ctx, gendb.RevokeOtherUserAPITokensParams{
		UserID: owner,
		KeepID: p.KeepID,
	}))
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
	return rowsAffected(s.conn.q.RefreshUserAPITokenAuthGeneration(ctx, gendb.RefreshUserAPITokenAuthGenerationParams{
		TokenID: p.TokenID,
		UserID:  owner,
	}))
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
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		n, err := rowsAffected(conn.q.ElevateAPIToken(ctx, gendb.ElevateAPITokenParams{
			ElevationProvenAt:  sqltime.SQLiteNullTimeOf(p.ElevationProvenAt),
			ElevationExpiresAt: sqltime.SQLiteNullTimeOf(p.ClampedExpiresAt()),
			ID:                 p.TokenID,
			UserID:             owner,
			Now:                sqltime.SQLiteNullTimeOf(now),
		}))
		if err != nil || n == 0 {
			return nil, err
		}
		return store.UserInfoEvent(owner, now.UTC()), nil
	}, emitCredentialEvent)
}

func (s *apiTokenStore) SlideElevation(ctx context.Context, p store.SlideAPITokenElevationParams, now time.Time) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	// WindowDeadline is an untyped sqlc parameter (it sits inside min(), which
	// carries no column type), so the canonical layout is this bind's
	// responsibility -- see the session store's SlideElevation for the whole
	// note, and TestAllDatetimeColumnsStoreCanonicalLayout for the fixture
	// that fails on a raw bind.
	return rowsAffected(s.conn.q.SlideAPITokenElevation(ctx, gendb.SlideAPITokenElevationParams{
		WindowDeadline: sqltime.NewSQLiteTime(p.WindowDeadline),
		MaxTotalMicros: store.ElevationMaxTotal.Microseconds(),
		ID:             p.TokenID,
		UserID:         owner,
		Now:            sqltime.SQLiteNullTimeOf(now),
	}))
}

func (s *apiTokenStore) DropElevation(ctx context.Context, p store.DropAPITokenElevationParams, now time.Time) (int64, error) {
	owner, ok := userid.OwnerFilter(p.UserID)
	if !ok {
		return 0, store.ErrInvalidArgument
	}
	return store.RunCredentialMutation(ctx, s.conn.withTransaction, func(ctx context.Context, conn *sqliteConn) (*store.CredentialEvent, error) {
		n, err := rowsAffected(conn.q.DropAPITokenElevation(ctx, gendb.DropAPITokenElevationParams{
			ID:     p.TokenID,
			UserID: owner,
		}))
		if err != nil || n == 0 {
			return nil, err
		}
		return store.UserInfoEvent(owner, now.UTC()), nil
	}, emitCredentialEvent)
}
