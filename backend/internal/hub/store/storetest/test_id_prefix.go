package storetest

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

// makePrefixedID builds a deterministic id.GeneratedLength (48-byte) ID
// starting with prefix and ending with suffix so tests can exercise both the
// exact-match path (len >= 48) and the half-open prefix range path (len < 48).
func makePrefixedID(t *testing.T, prefix, suffix string) string {
	t.Helper()
	padLen := id.GeneratedLength - len(prefix) - len(suffix)
	require.GreaterOrEqual(t, padLen, 0, "prefix+suffix must fit within id.GeneratedLength")
	return prefix + strings.Repeat("0", padLen) + suffix
}

// collectIDPrefixPages drains a keyset-paginated prefix listing using pageSize
// rows per page, asserting strict ascending ID order, zero duplicates, accurate
// HasMore/NextCursor termination, and returning the collected IDs plus the page
// count.
func collectIDPrefixPages[T store.PageCursorer](
	t *testing.T,
	pageSize int,
	fetch func(cursor string) (store.Page[T], error),
) ([]string, int) {
	t.Helper()
	var ids []string
	seen := make(map[string]struct{})
	cursor := ""
	pages := 0
	const safetyCap = 50
	for i := 0; i < safetyCap; i++ {
		page, err := fetch(cursor)
		require.NoError(t, err)
		pages++
		require.LessOrEqual(t, len(page.Rows), pageSize, "page size exceeded limit")
		for _, row := range page.Rows {
			_, rowID := row.PageCursor()
			if len(ids) > 0 {
				assert.Less(t, ids[len(ids)-1], rowID, "prefix listing must order by id ASC without duplicates")
			}
			_, dup := seen[rowID]
			assert.False(t, dup, "duplicate id %q across pages", rowID)
			seen[rowID] = struct{}{}
			ids = append(ids, rowID)
		}
		if !page.HasMore() {
			assert.Empty(t, page.NextCursor, "final page must not carry NextCursor")
			return ids, pages
		}
		assert.Len(t, page.Rows, pageSize, "non-final page must be full")
		require.NotEmpty(t, page.NextCursor, "non-final page must carry NextCursor")
		cursor = page.NextCursor
	}
	require.Failf(t, "prefix pagination did not terminate", "exceeded %d pages", safetyCap)
	return ids, pages
}

func seedPrefixedUser(t *testing.T, st store.Store, userID, username string) {
	t.Helper()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID:                    userID,
		Username:              username,
		PasswordHash:          "hash-" + username,
		DisplayName:           "Display " + username,
		Email:                 username + "@example.com",
		EmailVerified:         true,
		FirstCredentialExempt: true,
	}))
}

func seedPrefixedSession(t *testing.T, st store.Store, sessID, userID string, expiresAt time.Time) {
	t.Helper()
	require.NoError(t, st.Sessions().Create(context.Background(), store.CreateSessionParams{
		ID:         sessID,
		UserID:     userid.MustNew(userID),
		SecretHash: FixtureSecretHash(sessID),
		ExpiresAt:  expiresAt,
		UserAgent:  "test-agent",
		IPAddress:  "127.0.0.1",
	}))
}

func seedPrefixedAPIToken(t *testing.T, st store.Store, tokenID, userID, clientID string) {
	t.Helper()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientID:         clientID,
		InstallationName: "inst-" + tokenID[len(tokenID)-2:],
		GrantedScopes:    authscope.NonAdminGrant().String(),
		Issuance:         store.APITokenIssuanceAuthorizationCode,
		SecretHash:       FixtureSecretHash("secret-" + tokenID),
		RefreshHash:      FixtureSecretHash("refresh-" + tokenID),
	}))
}

func seedPrefixedDelegationToken(t *testing.T, st store.Store, tokenID, userID, workerID string) {
	t.Helper()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:            tokenID,
		UserID:        userid.MustNew(userID),
		WorkerID:      workerID,
		GrantedScopes: "workspace:read workspace:write worker:read",
		SecretHash:    FixtureSecretHash("del-" + tokenID),
		ExpiresAt:     time.Now().UTC().Add(time.Hour),
	}))
}

func seedPrefixedOAuthClient(t *testing.T, st store.Store, clientID, ownerUserID string) {
	t.Helper()
	source := store.OAuthClientSourceAdmin
	if ownerUserID != "" {
		source = store.OAuthClientSourceUser
	}
	_, err := st.OAuthClients().Create(context.Background(), store.CreateOAuthClientParams{
		ClientID:           clientID,
		OwnerUserID:        ownerUserID,
		ClientName:         "App " + clientID,
		ClientURI:          "https://app.example.com",
		RedirectURIs:       "http://127.0.0.1/callback",
		Scopes:             "workspace:read",
		GrantTypes:         "authorization_code",
		RegistrationSource: source,
	})
	require.NoError(t, err)
}

func seedPrefixedWorker(t *testing.T, st store.Store, workerID, registeredBy string) {
	t.Helper()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:            workerID,
		AuthTokenHash: FixtureSecretHash("worker-" + workerID),
		RegisteredBy:  userid.MustNew(registeredBy),
		PublicKeys:    WorkerKeys(workerkeystest.SeededWorkerFill),
	}))
}

func seedPrefixedRegistrationKey(t *testing.T, st store.Store, keyID, createdBy string, expiresAt time.Time) {
	t.Helper()
	require.NoError(t, st.RegistrationKeys().Create(context.Background(), store.CreateRegistrationKeyParams{
		ID:        keyID,
		KeyHash:   FixtureSecretHash("regkey-" + keyID),
		CreatedBy: userid.MustNew(createdBy),
		ExpiresAt: expiresAt,
	}))
}

func seedPrefixedOAuthProvider(t *testing.T, st store.Store, provID, name string, enabled bool) {
	t.Helper()
	require.NoError(t, st.OAuthProviders().Create(context.Background(), store.CreateOAuthProviderParams{
		ID:           provID,
		ProviderType: leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_OIDC,
		Name:         name,
		IssuerURL:    "https://issuer.example.com",
		ClientID:     "client-" + name,
		ClientSecret: []byte("secret-" + name),
		Scopes:       "openid profile email",
		TrustEmail:   true,
		Enabled:      enabled,
	}))
}

func seedPrefixedWorkspace(t *testing.T, st store.Store, wsID, ownerID, title string) {
	t.Helper()
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID:          wsID,
		OwnerUserID: userid.MustNew(ownerID),
		Title:       title,
	}))
}

func seedPrefixedSection(t *testing.T, st store.Store, secID, userID, name, pos string) {
	t.Helper()
	require.NoError(t, st.WorkspaceSections().Create(context.Background(), store.CreateWorkspaceSectionParams{
		ID:          secID,
		UserID:      userid.MustNew(userID),
		Name:        name,
		Position:    pos,
		SectionType: leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
		Sidebar:     leapmuxv1.Sidebar_SIDEBAR_LEFT,
	}))
}

func (s *Suite) testIDPrefix(t *testing.T) {
	const invalidUTF8 = "abc\xff"

	t.Run("users ListAll", func(t *testing.T) {
		st := s.NewStore(t)

		u1 := makePrefixedID(t, "pfxA", "1")
		u2 := makePrefixedID(t, "pfxA", "2")
		uDel := makePrefixedID(t, "pfxA", "3")
		u3 := makePrefixedID(t, "pfxA", "4")
		u4 := makePrefixedID(t, "pfxA", "5")
		uLower := makePrefixedID(t, "pfxa", "1")
		uNeighbor := makePrefixedID(t, "pfxB", "1")

		// Insert out of ID order so ORDER BY id ASC is genuinely tested.
		seedPrefixedUser(t, st, u4, "u4")
		seedPrefixedUser(t, st, u2, "u2")
		seedPrefixedUser(t, st, uDel, "udel")
		seedPrefixedUser(t, st, u1, "u1")
		seedPrefixedUser(t, st, u3, "u3")
		seedPrefixedUser(t, st, uLower, "ulower")
		seedPrefixedUser(t, st, uNeighbor, "uneighbor")
		require.NoError(t, st.Users().Delete(ctx, uDel))

		// Full 48-char exact match.
		exact, err := st.Users().ListAll(ctx, store.ListAllUsersParams{
			IDPrefix:   u2,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, exact.Rows, 1)
		assert.Equal(t, u2, exact.Rows[0].ID)
		assert.False(t, exact.HasMore())

		// Exact match excludes soft-deleted user.
		deletedExact, err := st.Users().ListAll(ctx, store.ListAllUsersParams{
			IDPrefix:   uDel,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, deletedExact.Rows)
		assert.False(t, deletedExact.HasMore())

		// Short prefix range scan with multi-page keyset pagination.
		ids, pages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.UserListItem], error) {
			return st.Users().ListAll(ctx, store.ListAllUsersParams{
				IDPrefix:   "pfxA",
				PageParams: store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{u1, u2, u3, u4}, ids)
		assert.Equal(t, 2, pages)

		// Case-sensitivity (lowercase vs uppercase).
		lowerPage, err := st.Users().ListAll(ctx, store.ListAllUsersParams{
			IDPrefix:   "pfxa",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, lowerPage.Rows, 1)
		assert.Equal(t, uLower, lowerPage.Rows[0].ID)

		wrongCase, err := st.Users().ListAll(ctx, store.ListAllUsersParams{
			IDPrefix:   "PfxA",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, wrongCase.Rows)

		// Non-matching prefix.
		noMatch, err := st.Users().ListAll(ctx, store.ListAllUsersParams{
			IDPrefix:   "zzzNoMatch",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, noMatch.Rows)
		assert.False(t, noMatch.HasMore())

		// Invalid UTF-8 returns ErrInvalidArgument.
		_, err = st.Users().ListAll(ctx, store.ListAllUsersParams{
			IDPrefix:   invalidUTF8,
			PageParams: store.PageParams{Limit: 10},
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)
	})

	t.Run("sessions ListByUserID and ListAllActive", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "sess-alice")
		bob := SeedUser(t, st, "sess-bob")
		aliceUID := userid.MustNew(alice.ID)

		now := time.Now().UTC()
		live := now.Add(24 * time.Hour)
		expired := now.Add(-time.Hour)

		s1 := makePrefixedID(t, "pfxA", "1")
		s2 := makePrefixedID(t, "pfxA", "2")
		sExp := makePrefixedID(t, "pfxA", "3")
		s3 := makePrefixedID(t, "pfxA", "4")
		sBob := makePrefixedID(t, "pfxA", "5")
		sLower := makePrefixedID(t, "pfxa", "1")
		sNeighbor := makePrefixedID(t, "pfxB", "1")

		seedPrefixedSession(t, st, s3, alice.ID, live)
		seedPrefixedSession(t, st, s1, alice.ID, live)
		seedPrefixedSession(t, st, sExp, alice.ID, expired)
		seedPrefixedSession(t, st, s2, alice.ID, live)
		seedPrefixedSession(t, st, sBob, bob.ID, live)
		seedPrefixedSession(t, st, sLower, alice.ID, live)
		seedPrefixedSession(t, st, sNeighbor, alice.ID, live)

		// ListByUserID: exact match, tenancy isolation, expiry exclusion.
		exact, err := st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
			UserID:     aliceUID,
			IDPrefix:   s2,
			PageParams: store.PageParams{Limit: 10},
		}, now)
		require.NoError(t, err)
		require.Len(t, exact.Rows, 1)
		assert.Equal(t, s2, exact.Rows[0].ID)

		otherOwnerExact, err := st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
			UserID:     aliceUID,
			IDPrefix:   sBob,
			PageParams: store.PageParams{Limit: 10},
		}, now)
		require.NoError(t, err)
		assert.Empty(t, otherOwnerExact.Rows, "another user's session must never be returned")

		expiredExact, err := st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
			UserID:     aliceUID,
			IDPrefix:   sExp,
			PageParams: store.PageParams{Limit: 10},
		}, now)
		require.NoError(t, err)
		assert.Empty(t, expiredExact.Rows)

		// ListByUserID: paginated prefix scan enforces tenancy and excludes expired.
		userIDs, userPages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.UserSession], error) {
			return st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
				UserID:     aliceUID,
				IDPrefix:   "pfxA",
				PageParams: store.PageParams{Cursor: cursor, Limit: 2},
			}, now)
		})
		assert.Equal(t, []string{s1, s2, s3}, userIDs)
		assert.Equal(t, 2, userPages)

		lowerUser, err := st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
			UserID:     aliceUID,
			IDPrefix:   "pfxa",
			PageParams: store.PageParams{Limit: 10},
		}, now)
		require.NoError(t, err)
		require.Len(t, lowerUser.Rows, 1)
		assert.Equal(t, sLower, lowerUser.Rows[0].ID)

		_, err = st.Sessions().ListByUserID(ctx, store.ListUserSessionsParams{
			UserID:     aliceUID,
			IDPrefix:   invalidUTF8,
			PageParams: store.PageParams{Limit: 10},
		}, now)
		require.ErrorIs(t, err, store.ErrInvalidArgument)

		// ListAllActive: exact match across users.
		adminExact, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			IDPrefix:   sBob,
			PageParams: store.PageParams{Limit: 10},
		}, now)
		require.NoError(t, err)
		require.Len(t, adminExact.Rows, 1)
		assert.Equal(t, sBob, adminExact.Rows[0].ID)
		assert.Equal(t, "sess-bob", adminExact.Rows[0].Username)

		// ListAllActive: paginated prefix scan across both users, excluding expired.
		allIDs, allPages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.ActiveSession], error) {
			return st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
				IDPrefix:   "pfxA",
				PageParams: store.PageParams{Cursor: cursor, Limit: 2},
			}, now)
		})
		assert.Equal(t, []string{s1, s2, s3, sBob}, allIDs)
		assert.Equal(t, 2, allPages)

		noMatchAdmin, err := st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			IDPrefix:   "PfxA",
			PageParams: store.PageParams{Limit: 10},
		}, now)
		require.NoError(t, err)
		assert.Empty(t, noMatchAdmin.Rows)

		_, err = st.Sessions().ListAllActive(ctx, store.ListAllActiveSessionsParams{
			IDPrefix:   invalidUTF8,
			PageParams: store.PageParams{Limit: 10},
		}, now)
		require.ErrorIs(t, err, store.ErrInvalidArgument)
	})

	t.Run("api_tokens ListByUser and ListAll", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "apitok-alice")
		bob := SeedUser(t, st, "apitok-bob")
		aliceUID := userid.MustNew(alice.ID)

		t1 := makePrefixedID(t, "pfxA", "1")
		t2 := makePrefixedID(t, "pfxA", "2")
		tRev := makePrefixedID(t, "pfxA", "3")
		t3 := makePrefixedID(t, "pfxA", "4")
		tBob := makePrefixedID(t, "pfxA", "5")
		tLower := makePrefixedID(t, "pfxa", "1")
		tNeighbor := makePrefixedID(t, "pfxB", "1")

		seedPrefixedAPIToken(t, st, t3, alice.ID, oauthapp.ServiceAccountClientID)
		seedPrefixedAPIToken(t, st, t1, alice.ID, oauthapp.ControlCLIClientID)
		seedPrefixedAPIToken(t, st, tRev, alice.ID, oauthapp.ControlCLIClientID)
		seedPrefixedAPIToken(t, st, t2, alice.ID, oauthapp.ControlCLIClientID)
		seedPrefixedAPIToken(t, st, tBob, bob.ID, oauthapp.ControlCLIClientID)
		seedPrefixedAPIToken(t, st, tLower, alice.ID, oauthapp.ControlCLIClientID)
		seedPrefixedAPIToken(t, st, tNeighbor, alice.ID, oauthapp.ControlCLIClientID)
		_, err := st.APITokens().Revoke(ctx, tRev)
		require.NoError(t, err)

		// ListByUser: exact match, tenancy isolation, revoked exclusion.
		exact, err := st.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
			UserID:     aliceUID,
			IDPrefix:   t2,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, exact.Rows, 1)
		assert.Equal(t, t2, exact.Rows[0].ID)

		otherOwnerExact, err := st.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
			UserID:     aliceUID,
			IDPrefix:   tBob,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, otherOwnerExact.Rows)

		revokedUserExact, err := st.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
			UserID:     aliceUID,
			IDPrefix:   tRev,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, revokedUserExact.Rows)

		// ListByUser: multi-page range scan.
		byUserIDs, byUserPages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.APIToken], error) {
			return st.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
				UserID:     aliceUID,
				IDPrefix:   "pfxA",
				PageParams: store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{t1, t2, t3}, byUserIDs)
		assert.Equal(t, 2, byUserPages)

		// ListByUser: ClientID filter composes with IDPrefix.
		byClient, err := st.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
			UserID:     aliceUID,
			ClientID:   oauthapp.ServiceAccountClientID,
			IDPrefix:   "pfxA",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, byClient.Rows, 1)
		assert.Equal(t, t3, byClient.Rows[0].ID)

		lowerUser, err := st.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
			UserID:     aliceUID,
			IDPrefix:   "pfxa",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, lowerUser.Rows, 1)
		assert.Equal(t, tLower, lowerUser.Rows[0].ID)

		_, err = st.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
			UserID:     aliceUID,
			IDPrefix:   invalidUTF8,
			PageParams: store.PageParams{Limit: 10},
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)

		// ListAll: exact match with and without IncludeRevoked.
		revHidden, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
			IDPrefix:       tRev,
			IncludeRevoked: false,
			PageParams:     store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, revHidden.Rows)

		revShown, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
			IDPrefix:       tRev,
			IncludeRevoked: true,
			PageParams:     store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, revShown.Rows, 1)
		assert.Equal(t, tRev, revShown.Rows[0].ID)
		assert.Equal(t, "apitok-alice", revShown.Rows[0].OwnerUsername)

		// ListAll: paginated range scan (live only vs include revoked).
		liveIDs, livePages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.APITokenWithOwner], error) {
			return st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
				IDPrefix:       "pfxA",
				IncludeRevoked: false,
				PageParams:     store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{t1, t2, t3, tBob}, liveIDs)
		assert.Equal(t, 2, livePages)

		allWithRevIDs, allWithRevPages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.APITokenWithOwner], error) {
			return st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
				IDPrefix:       "pfxA",
				IncludeRevoked: true,
				PageParams:     store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{t1, t2, tRev, t3, tBob}, allWithRevIDs)
		assert.Equal(t, 3, allWithRevPages)

		// ListAll: UserID filter composes with IDPrefix.
		aliceOnly, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
			UserID:         &alice.ID,
			IDPrefix:       "pfxA",
			IncludeRevoked: false,
			PageParams:     store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, aliceOnly.Rows, 3)
		assert.Equal(t, []string{t1, t2, t3}, []string{aliceOnly.Rows[0].ID, aliceOnly.Rows[1].ID, aliceOnly.Rows[2].ID})

		// ListAll: ClientID filter composes with IDPrefix.
		clientOnly, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
			ClientID:   oauthapp.ServiceAccountClientID,
			IDPrefix:   "pfxA",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, clientOnly.Rows, 1)
		assert.Equal(t, t3, clientOnly.Rows[0].ID)

		_, err = st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
			IDPrefix:   invalidUTF8,
			PageParams: store.PageParams{Limit: 10},
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)
	})

	t.Run("delegation_tokens ListAll", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "del-alice")
		bob := SeedUser(t, st, "del-bob")
		wAlice := SeedWorker(t, st, alice.ID)
		wBob := SeedWorker(t, st, bob.ID)

		d1 := makePrefixedID(t, "pfxA", "1")
		d2 := makePrefixedID(t, "pfxA", "2")
		dRev := makePrefixedID(t, "pfxA", "3")
		d3 := makePrefixedID(t, "pfxA", "4")
		dBob := makePrefixedID(t, "pfxA", "5")
		dLower := makePrefixedID(t, "pfxa", "1")
		dNeighbor := makePrefixedID(t, "pfxB", "1")

		seedPrefixedDelegationToken(t, st, d3, alice.ID, wAlice.ID)
		seedPrefixedDelegationToken(t, st, d1, alice.ID, wAlice.ID)
		seedPrefixedDelegationToken(t, st, dRev, alice.ID, wAlice.ID)
		seedPrefixedDelegationToken(t, st, d2, alice.ID, wAlice.ID)
		seedPrefixedDelegationToken(t, st, dBob, bob.ID, wBob.ID)
		seedPrefixedDelegationToken(t, st, dLower, alice.ID, wAlice.ID)
		seedPrefixedDelegationToken(t, st, dNeighbor, alice.ID, wAlice.ID)
		_, err := st.DelegationTokens().Revoke(ctx, dRev)
		require.NoError(t, err)

		// Exact match.
		exact, err := st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
			IDPrefix:   d2,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, exact.Rows, 1)
		assert.Equal(t, d2, exact.Rows[0].ID)

		// Revoked exact match.
		revHidden, err := st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
			IDPrefix:       dRev,
			IncludeRevoked: false,
			PageParams:     store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, revHidden.Rows)

		revShown, err := st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
			IDPrefix:       dRev,
			IncludeRevoked: true,
			PageParams:     store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, revShown.Rows, 1)
		assert.Equal(t, dRev, revShown.Rows[0].ID)

		// Paginated range scan (live only vs include revoked).
		liveIDs, livePages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.DelegationTokenWithOwner], error) {
			return st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
				IDPrefix:       "pfxA",
				IncludeRevoked: false,
				PageParams:     store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{d1, d2, d3, dBob}, liveIDs)
		assert.Equal(t, 2, livePages)

		allIDs, allPages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.DelegationTokenWithOwner], error) {
			return st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
				IDPrefix:       "pfxA",
				IncludeRevoked: true,
				PageParams:     store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{d1, d2, dRev, d3, dBob}, allIDs)
		assert.Equal(t, 3, allPages)

		// UserID filter composes with IDPrefix.
		aliceOnly, err := st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
			UserID:     &alice.ID,
			IDPrefix:   "pfxA",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, aliceOnly.Rows, 3)
		assert.Equal(t, []string{d1, d2, d3}, []string{aliceOnly.Rows[0].ID, aliceOnly.Rows[1].ID, aliceOnly.Rows[2].ID})

		// Case-sensitivity and invalid UTF-8.
		lowerPage, err := st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
			IDPrefix:   "pfxa",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, lowerPage.Rows, 1)
		assert.Equal(t, dLower, lowerPage.Rows[0].ID)

		_, err = st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
			IDPrefix:   invalidUTF8,
			PageParams: store.PageParams{Limit: 10},
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)
	})

	t.Run("oauth_clients List", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "app-alice")
		bob := SeedUser(t, st, "app-bob")
		aliceUID := userid.MustNew(alice.ID)

		a1 := makePrefixedID(t, "pfxA", "1")
		a2 := makePrefixedID(t, "pfxA", "2")
		aRev := makePrefixedID(t, "pfxA", "3")
		a3 := makePrefixedID(t, "pfxA", "4")
		aHub := makePrefixedID(t, "pfxA", "5")
		aBob := makePrefixedID(t, "pfxA", "6")
		aLower := makePrefixedID(t, "pfxa", "1")
		aNeighbor := makePrefixedID(t, "pfxB", "1")

		seedPrefixedOAuthClient(t, st, "leapmux-cli", "")
		seedPrefixedOAuthClient(t, st, a3, alice.ID)
		seedPrefixedOAuthClient(t, st, a1, alice.ID)
		seedPrefixedOAuthClient(t, st, aRev, alice.ID)
		seedPrefixedOAuthClient(t, st, a2, alice.ID)
		seedPrefixedOAuthClient(t, st, aHub, "")
		seedPrefixedOAuthClient(t, st, aBob, bob.ID)
		seedPrefixedOAuthClient(t, st, aLower, alice.ID)
		seedPrefixedOAuthClient(t, st, aNeighbor, alice.ID)

		_, err := st.OAuthClients().Revoke(ctx, store.OAuthClientOwnershipParams{
			ClientID:      aRev,
			CallerUserID:  aliceUID,
			CallerIsAdmin: false,
		})
		require.NoError(t, err)

		// Built-in / shorter IDs (e.g. "leapmux-cli", "leapmux-control-cli").
		shortPage, err := st.OAuthClients().List(ctx, store.ListOAuthClientsParams{
			UserID:         aliceUID,
			IncludeHubWide: true,
			IDPrefix:       "leapmux-c",
			PageParams:     store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		gotShort := make([]string, 0, len(shortPage.Rows))
		for _, r := range shortPage.Rows {
			gotShort = append(gotShort, r.ClientID)
		}
		assert.Equal(t, []string{"leapmux-cli", oauthapp.ControlCLIClientID}, gotShort)

		shortWrongCase, err := st.OAuthClients().List(ctx, store.ListOAuthClientsParams{
			UserID:         aliceUID,
			IncludeHubWide: true,
			IDPrefix:       "Leapmux-c",
			PageParams:     store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, shortWrongCase.Rows)

		// Private listing: exact match, tenancy isolation, hub-wide excluded, revoked excluded.
		exact, err := st.OAuthClients().List(ctx, store.ListOAuthClientsParams{
			UserID:     aliceUID,
			IDPrefix:   a2,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, exact.Rows, 1)
		assert.Equal(t, a2, exact.Rows[0].ClientID)

		for _, forbidden := range []string{aBob, aHub, aRev} {
			res, err := st.OAuthClients().List(ctx, store.ListOAuthClientsParams{
				UserID:     aliceUID,
				IDPrefix:   forbidden,
				PageParams: store.PageParams{Limit: 10},
			})
			require.NoError(t, err)
			assert.Empty(t, res.Rows, "private live listing must exclude %s", forbidden)
		}

		// Private paginated range scan.
		privIDs, privPages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.OAuthClient], error) {
			return st.OAuthClients().List(ctx, store.ListOAuthClientsParams{
				UserID:     aliceUID,
				IDPrefix:   "pfxA",
				PageParams: store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{a1, a2, a3}, privIDs)
		assert.Equal(t, 2, privPages)

		// IncludeRevoked brings back aRev.
		withRevIDs, _ := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.OAuthClient], error) {
			return st.OAuthClients().List(ctx, store.ListOAuthClientsParams{
				UserID:         aliceUID,
				IncludeRevoked: true,
				IDPrefix:       "pfxA",
				PageParams:     store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{a1, a2, aRev, a3}, withRevIDs)

		// IncludeHubWide includes aHub (still excludes bob's private app).
		withHubIDs, withHubPages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.OAuthClient], error) {
			return st.OAuthClients().List(ctx, store.ListOAuthClientsParams{
				UserID:         aliceUID,
				IncludeHubWide: true,
				IDPrefix:       "pfxA",
				PageParams:     store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{a1, a2, a3, aHub}, withHubIDs)
		assert.Equal(t, 2, withHubPages)

		// HubWideOnly returns only hub-wide rows.
		hubOnly, err := st.OAuthClients().List(ctx, store.ListOAuthClientsParams{
			UserID:      aliceUID,
			HubWideOnly: true,
			IDPrefix:    "pfxA",
			PageParams:  store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, hubOnly.Rows, 1)
		assert.Equal(t, aHub, hubOnly.Rows[0].ClientID)

		// Case-sensitivity and invalid UTF-8.
		lowerPage, err := st.OAuthClients().List(ctx, store.ListOAuthClientsParams{
			UserID:     aliceUID,
			IDPrefix:   "pfxa",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, lowerPage.Rows, 1)
		assert.Equal(t, aLower, lowerPage.Rows[0].ClientID)

		_, err = st.OAuthClients().List(ctx, store.ListOAuthClientsParams{
			UserID:     aliceUID,
			IDPrefix:   invalidUTF8,
			PageParams: store.PageParams{Limit: 10},
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)
	})

	t.Run("workers ListByUserID and ListAdmin", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "wrk-alice")
		bob := SeedUser(t, st, "wrk-bob")
		aliceUID := userid.MustNew(alice.ID)

		w1 := makePrefixedID(t, "pfxA", "1")
		w2 := makePrefixedID(t, "pfxA", "2")
		wDereg := makePrefixedID(t, "pfxA", "3")
		wDel := makePrefixedID(t, "pfxA", "4")
		w3 := makePrefixedID(t, "pfxA", "5")
		wBob := makePrefixedID(t, "pfxA", "6")
		wLower := makePrefixedID(t, "pfxa", "1")
		wNeighbor := makePrefixedID(t, "pfxB", "1")

		seedPrefixedWorker(t, st, w3, alice.ID)
		seedPrefixedWorker(t, st, w1, alice.ID)
		seedPrefixedWorker(t, st, wDereg, alice.ID)
		seedPrefixedWorker(t, st, wDel, alice.ID)
		seedPrefixedWorker(t, st, w2, alice.ID)
		seedPrefixedWorker(t, st, wBob, bob.ID)
		seedPrefixedWorker(t, st, wLower, alice.ID)
		seedPrefixedWorker(t, st, wNeighbor, alice.ID)

		_, err := st.Workers().Deregister(ctx, store.DeregisterWorkerParams{ID: wDereg, RegisteredBy: aliceUID})
		require.NoError(t, err)
		require.NoError(t, st.Workers().MarkDeleted(ctx, wDel))

		// ListByUserID: exact match, tenancy, active-only.
		exact, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: aliceUID,
			IDPrefix:     w2,
			PageParams:   store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, exact.Rows, 1)
		assert.Equal(t, w2, exact.Rows[0].ID)

		for _, excluded := range []string{wBob, wDereg, wDel} {
			res, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
				RegisteredBy: aliceUID,
				IDPrefix:     excluded,
				PageParams:   store.PageParams{Limit: 10},
			})
			require.NoError(t, err)
			assert.Empty(t, res.Rows, "ListByUserID must exclude %s", excluded)
		}

		userIDs, userPages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.Worker], error) {
			return st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
				RegisteredBy: aliceUID,
				IDPrefix:     "pfxA",
				PageParams:   store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{w1, w2, w3}, userIDs)
		assert.Equal(t, 2, userPages)

		lowerUser, err := st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: aliceUID,
			IDPrefix:     "pfxa",
			PageParams:   store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, lowerUser.Rows, 1)
		assert.Equal(t, wLower, lowerUser.Rows[0].ID)

		_, err = st.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
			RegisteredBy: aliceUID,
			IDPrefix:     invalidUTF8,
			PageParams:   store.PageParams{Limit: 10},
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)

		// ListAdmin: default (live = active) excludes deleted/deregistered.
		liveAdminIDs, liveAdminPages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.WorkerWithOwner], error) {
			return st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
				IDPrefix:   "pfxA",
				PageParams: store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{w1, w2, w3, wBob}, liveAdminIDs)
		assert.Equal(t, 2, liveAdminPages)

		// ListAdmin: status filters compose with IDPrefix (both range and exact).
		activeStatus := leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE
		deletedStatus := leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED

		activeIDs, _ := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.WorkerWithOwner], error) {
			return st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
				Status:     &activeStatus,
				IDPrefix:   "pfxA",
				PageParams: store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{w1, w2, w3, wBob}, activeIDs)

		delHidden, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			IDPrefix:   wDel,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, delHidden.Rows)

		delShown, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			Status:     &deletedStatus,
			IDPrefix:   wDel,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, delShown.Rows, 1)
		assert.Equal(t, wDel, delShown.Rows[0].ID)

		deregShown, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			Status:     &deletedStatus,
			IDPrefix:   wDereg,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, deregShown.Rows, 1)
		assert.Equal(t, wDereg, deregShown.Rows[0].ID)

		// ListAdmin: UserID filter composes with IDPrefix.
		aliceAdmin, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			UserID:     &alice.ID,
			IDPrefix:   "pfxA",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{w1, w2, w3}, func() []string {
			out := make([]string, len(aliceAdmin.Rows))
			for i, r := range aliceAdmin.Rows {
				out[i] = r.ID
			}
			return out
		}())

		_, err = st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			IDPrefix:   invalidUTF8,
			PageParams: store.PageParams{Limit: 10},
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)
	})

	t.Run("registration_keys ListAdmin", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "regkey-alice")
		live := time.Now().UTC().Add(time.Hour)
		expired := time.Now().UTC().Add(-time.Hour)

		k1 := makePrefixedID(t, "pfxA", "1")
		k2 := makePrefixedID(t, "pfxA", "2")
		kExp := makePrefixedID(t, "pfxA", "3")
		k3 := makePrefixedID(t, "pfxA", "4")
		kLower := makePrefixedID(t, "pfxa", "1")
		kNeighbor := makePrefixedID(t, "pfxB", "1")

		seedPrefixedRegistrationKey(t, st, k3, alice.ID, live)
		seedPrefixedRegistrationKey(t, st, k1, alice.ID, live)
		seedPrefixedRegistrationKey(t, st, kExp, alice.ID, expired)
		seedPrefixedRegistrationKey(t, st, k2, alice.ID, live)
		seedPrefixedRegistrationKey(t, st, kLower, alice.ID, live)
		seedPrefixedRegistrationKey(t, st, kNeighbor, alice.ID, live)

		// Exact match (live and expired).
		exact, err := st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
			IDPrefix:   k2,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, exact.Rows, 1)
		assert.Equal(t, k2, exact.Rows[0].ID)

		expHidden, err := st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
			IDPrefix:       kExp,
			IncludeExpired: false,
			PageParams:     store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, expHidden.Rows)

		expShown, err := st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
			IDPrefix:       kExp,
			IncludeExpired: true,
			PageParams:     store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, expShown.Rows, 1)
		assert.Equal(t, kExp, expShown.Rows[0].ID)

		// Paginated range scan (live only vs include expired).
		liveIDs, livePages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.WorkerRegistrationKeyWithCreator], error) {
			return st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
				IDPrefix:       "pfxA",
				IncludeExpired: false,
				PageParams:     store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{k1, k2, k3}, liveIDs)
		assert.Equal(t, 2, livePages)

		allIDs, allPages := collectIDPrefixPages(t, 2, func(cursor string) (store.Page[store.WorkerRegistrationKeyWithCreator], error) {
			return st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
				IDPrefix:       "pfxA",
				IncludeExpired: true,
				PageParams:     store.PageParams{Cursor: cursor, Limit: 2},
			})
		})
		assert.Equal(t, []string{k1, k2, kExp, k3}, allIDs)
		assert.Equal(t, 2, allPages)

		// Case-sensitivity and invalid UTF-8.
		lowerPage, err := st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
			IDPrefix:   "pfxa",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		require.Len(t, lowerPage.Rows, 1)
		assert.Equal(t, kLower, lowerPage.Rows[0].ID)

		_, err = st.RegistrationKeys().ListAdmin(ctx, store.ListRegistrationKeysAdminParams{
			IDPrefix:   invalidUTF8,
			PageParams: store.PageParams{Limit: 10},
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)
	})

	t.Run("oauth_providers ListByIDPrefix", func(t *testing.T) {
		st := s.NewStore(t)

		p1 := makePrefixedID(t, "pfxA", "1")
		p2 := makePrefixedID(t, "pfxA", "2")
		p3 := makePrefixedID(t, "pfxA", "3")
		pLower := makePrefixedID(t, "pfxa", "1")
		pNeighbor := makePrefixedID(t, "pfxB", "1")

		seedPrefixedOAuthProvider(t, st, p3, "p3", false)
		seedPrefixedOAuthProvider(t, st, p1, "p1", true)
		seedPrefixedOAuthProvider(t, st, p2, "p2", true)
		seedPrefixedOAuthProvider(t, st, pLower, "plower", true)
		seedPrefixedOAuthProvider(t, st, pNeighbor, "pneighbor", true)
		seedPrefixedOAuthProvider(t, st, "github-corp", "gh", true)

		exact, err := st.OAuthProviders().ListByIDPrefix(ctx, p2)
		require.NoError(t, err)
		require.Len(t, exact, 1)
		assert.Equal(t, p2, exact[0].ID)

		prefixed, err := st.OAuthProviders().ListByIDPrefix(ctx, "pfxA")
		require.NoError(t, err)
		require.Len(t, prefixed, 3)
		assert.Equal(t, []string{p1, p2, p3}, []string{prefixed[0].ID, prefixed[1].ID, prefixed[2].ID})

		shortCustom, err := st.OAuthProviders().ListByIDPrefix(ctx, "github-")
		require.NoError(t, err)
		require.Len(t, shortCustom, 1)
		assert.Equal(t, "github-corp", shortCustom[0].ID)

		lower, err := st.OAuthProviders().ListByIDPrefix(ctx, "pfxa")
		require.NoError(t, err)
		require.Len(t, lower, 1)
		assert.Equal(t, pLower, lower[0].ID)

		wrongCase, err := st.OAuthProviders().ListByIDPrefix(ctx, "PfxA")
		require.NoError(t, err)
		assert.Empty(t, wrongCase)

		noMatch, err := st.OAuthProviders().ListByIDPrefix(ctx, "zzzNoMatch")
		require.NoError(t, err)
		assert.Empty(t, noMatch)

		_, err = st.OAuthProviders().ListByIDPrefix(ctx, invalidUTF8)
		require.ErrorIs(t, err, store.ErrInvalidArgument)
	})

	t.Run("workspaces ListAccessible", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "ws-alice")
		bob := SeedUser(t, st, "ws-bob")
		aliceUID := userid.MustNew(alice.ID)

		ws1 := makePrefixedID(t, "pfxA", "1")
		ws2 := makePrefixedID(t, "pfxA", "2")
		wsDel := makePrefixedID(t, "pfxA", "3")
		ws3 := makePrefixedID(t, "pfxA", "4")
		wsBob := makePrefixedID(t, "pfxA", "5")
		wsLower := makePrefixedID(t, "pfxa", "1")
		wsNeighbor := makePrefixedID(t, "pfxB", "1")

		seedPrefixedWorkspace(t, st, ws3, alice.ID, "WS 3")
		seedPrefixedWorkspace(t, st, ws1, alice.ID, "WS 1")
		seedPrefixedWorkspace(t, st, wsDel, alice.ID, "WS Del")
		seedPrefixedWorkspace(t, st, ws2, alice.ID, "WS 2")
		seedPrefixedWorkspace(t, st, wsBob, bob.ID, "WS Bob")
		seedPrefixedWorkspace(t, st, wsLower, alice.ID, "WS Lower")
		seedPrefixedWorkspace(t, st, wsNeighbor, alice.ID, "WS B")

		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsDel, OwnerUserID: aliceUID})
		require.NoError(t, err)

		exact, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID:   aliceUID,
			IDPrefix: ws2,
		})
		require.NoError(t, err)
		require.Len(t, exact, 1)
		assert.Equal(t, ws2, exact[0].ID)

		for _, excluded := range []string{wsDel, wsBob} {
			res, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
				UserID:   aliceUID,
				IDPrefix: excluded,
			})
			require.NoError(t, err)
			assert.Empty(t, res, "ListAccessible must exclude %s", excluded)
		}

		prefixed, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID:   aliceUID,
			IDPrefix: "pfxA",
		})
		require.NoError(t, err)
		require.Len(t, prefixed, 3)
		assert.Equal(t, []string{ws1, ws2, ws3}, []string{prefixed[0].ID, prefixed[1].ID, prefixed[2].ID})

		lower, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID:   aliceUID,
			IDPrefix: "pfxa",
		})
		require.NoError(t, err)
		require.Len(t, lower, 1)
		assert.Equal(t, wsLower, lower[0].ID)

		noMatch, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID:   aliceUID,
			IDPrefix: "PfxA",
		})
		require.NoError(t, err)
		assert.Empty(t, noMatch)

		_, err = st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID:   aliceUID,
			IDPrefix: invalidUTF8,
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)
	})

	t.Run("workspace_tab_index ListRenderedByIDPrefix", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "tab-alice")
		bob := SeedUser(t, st, "tab-bob")
		aliceUID := userid.MustNew(alice.ID)
		bobUID := userid.MustNew(bob.ID)

		wsA1 := SeedWorkspace(t, st, alice.ID, "Alice WS 1")
		wsA2 := SeedWorkspace(t, st, alice.ID, "Alice WS 2")
		wsADel := SeedWorkspace(t, st, alice.ID, "Alice WS Deleted")
		wsB1 := SeedWorkspace(t, st, bob.ID, "Bob WS 1")
		wAlice := SeedWorker(t, st, alice.ID)
		wBob := SeedWorker(t, st, bob.ID)

		tab1 := makePrefixedID(t, "pfxA", "1")
		tab2 := makePrefixedID(t, "pfxA", "2")
		tabHidden := makePrefixedID(t, "pfxA", "3")
		tab3 := makePrefixedID(t, "pfxA", "4")
		tabDeletedWS := makePrefixedID(t, "pfxA", "5")
		tabLower := makePrefixedID(t, "pfxa", "1")
		tabNeighbor := makePrefixedID(t, "pfxB", "1")

		for _, row := range []store.UpsertTabParams{
			{UserID: aliceUID, WorkspaceID: wsA2, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tab3, WorkerID: wAlice.ID, TileID: "t3", Position: "a0", Rendered: true},
			{UserID: aliceUID, WorkspaceID: wsA1, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tab1, WorkerID: wAlice.ID, TileID: "t1", Position: "a1", Rendered: true},
			{UserID: aliceUID, WorkspaceID: wsA1, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabHidden, WorkerID: wAlice.ID, TileID: "th", Position: "a2", Rendered: false},
			{UserID: aliceUID, WorkspaceID: wsA1, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tab2, WorkerID: wAlice.ID, TileID: "t2", Position: "a3", Rendered: true},
			{UserID: aliceUID, WorkspaceID: wsADel, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabDeletedWS, WorkerID: wAlice.ID, TileID: "td", Position: "a4", Rendered: true},
			{UserID: aliceUID, WorkspaceID: wsA1, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabLower, WorkerID: wAlice.ID, TileID: "tl", Position: "a5", Rendered: true},
			{UserID: aliceUID, WorkspaceID: wsA1, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tabNeighbor, WorkerID: wAlice.ID, TileID: "tn", Position: "a6", Rendered: true},
			// Bob owns a tab with the exact same tab_id as alice's tab1.
			{UserID: bobUID, WorkspaceID: wsB1, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: tab1, WorkerID: wBob.ID, TileID: "tb", Position: "a0", Rendered: true},
		} {
			require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, row))
		}
		_, err := st.Workspaces().SoftDelete(ctx, store.SoftDeleteWorkspaceParams{ID: wsADel, OwnerUserID: aliceUID})
		require.NoError(t, err)

		// Exact match across all non-deleted workspaces: owner tenancy enforced.
		exact, err := st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
			UserID:   aliceUID,
			IDPrefix: tab1,
		})
		require.NoError(t, err)
		require.Len(t, exact, 1)
		assert.Equal(t, tab1, exact[0].TabID)
		assert.Equal(t, wsA1, exact[0].WorkspaceID)
		assert.Equal(t, "t1", exact[0].TileID)

		// Unrendered and deleted-workspace tabs are excluded.
		for _, excluded := range []string{tabHidden, tabDeletedWS} {
			res, err := st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
				UserID:   aliceUID,
				IDPrefix: excluded,
			})
			require.NoError(t, err)
			assert.Empty(t, res, "ListRenderedByIDPrefix must exclude %s", excluded)
		}

		// Range scan across all non-deleted workspaces, ordered by tab_id ASC.
		allAlice, err := st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
			UserID:   aliceUID,
			IDPrefix: "pfxA",
		})
		require.NoError(t, err)
		require.Len(t, allAlice, 3)
		assert.Equal(t, []string{tab1, tab2, tab3}, []string{allAlice[0].TabID, allAlice[1].TabID, allAlice[2].TabID})

		// WorkspaceIDs filter narrows both range scan and exact match.
		ws1Only, err := st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
			UserID:       aliceUID,
			WorkspaceIDs: []string{wsA1},
			IDPrefix:     "pfxA",
		})
		require.NoError(t, err)
		require.Len(t, ws1Only, 2)
		assert.Equal(t, []string{tab1, tab2}, []string{ws1Only[0].TabID, ws1Only[1].TabID})

		ws1ExactMiss, err := st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
			UserID:       aliceUID,
			WorkspaceIDs: []string{wsA1},
			IDPrefix:     tab3,
		})
		require.NoError(t, err)
		assert.Empty(t, ws1ExactMiss)

		ws1ExactHit, err := st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
			UserID:       aliceUID,
			WorkspaceIDs: []string{wsA1},
			IDPrefix:     tab1,
		})
		require.NoError(t, err)
		require.Len(t, ws1ExactHit, 1)
		assert.Equal(t, tab1, ws1ExactHit[0].TabID)

		// Passing another user's workspace returns nothing.
		foreignWS, err := st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
			UserID:       aliceUID,
			WorkspaceIDs: []string{wsB1},
			IDPrefix:     "pfxA",
		})
		require.NoError(t, err)
		assert.Empty(t, foreignWS)

		// Case-sensitivity and invalid UTF-8.
		lower, err := st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
			UserID:   aliceUID,
			IDPrefix: "pfxa",
		})
		require.NoError(t, err)
		require.Len(t, lower, 1)
		assert.Equal(t, tabLower, lower[0].TabID)

		wrongCase, err := st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
			UserID:   aliceUID,
			IDPrefix: "PfxA",
		})
		require.NoError(t, err)
		assert.Empty(t, wrongCase)

		_, err = st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
			UserID:   aliceUID,
			IDPrefix: invalidUTF8,
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)
	})

	t.Run("workspace_sections ListByIDPrefix", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "sec-alice")
		bob := SeedUser(t, st, "sec-bob")
		aliceUID := userid.MustNew(alice.ID)

		sec1 := makePrefixedID(t, "pfxA", "1")
		sec2 := makePrefixedID(t, "pfxA", "2")
		sec3 := makePrefixedID(t, "pfxA", "3")
		secBob := makePrefixedID(t, "pfxA", "4")
		secLower := makePrefixedID(t, "pfxa", "1")
		secNeighbor := makePrefixedID(t, "pfxB", "1")

		seedPrefixedSection(t, st, sec3, alice.ID, "Sec 3", "a3")
		seedPrefixedSection(t, st, sec1, alice.ID, "Sec 1", "a1")
		seedPrefixedSection(t, st, sec2, alice.ID, "Sec 2", "a2")
		seedPrefixedSection(t, st, secBob, bob.ID, "Sec Bob", "a1")
		seedPrefixedSection(t, st, secLower, alice.ID, "Sec Lower", "a4")
		seedPrefixedSection(t, st, secNeighbor, alice.ID, "Sec B", "a5")

		exact, err := st.WorkspaceSections().ListByIDPrefix(ctx, aliceUID, sec2)
		require.NoError(t, err)
		require.Len(t, exact, 1)
		assert.Equal(t, sec2, exact[0].ID)

		otherOwner, err := st.WorkspaceSections().ListByIDPrefix(ctx, aliceUID, secBob)
		require.NoError(t, err)
		assert.Empty(t, otherOwner)

		prefixed, err := st.WorkspaceSections().ListByIDPrefix(ctx, aliceUID, "pfxA")
		require.NoError(t, err)
		require.Len(t, prefixed, 3)
		assert.Equal(t, []string{sec1, sec2, sec3}, []string{prefixed[0].ID, prefixed[1].ID, prefixed[2].ID})

		lower, err := st.WorkspaceSections().ListByIDPrefix(ctx, aliceUID, "pfxa")
		require.NoError(t, err)
		require.Len(t, lower, 1)
		assert.Equal(t, secLower, lower[0].ID)

		wrongCase, err := st.WorkspaceSections().ListByIDPrefix(ctx, aliceUID, "PfxA")
		require.NoError(t, err)
		assert.Empty(t, wrongCase)

		_, err = st.WorkspaceSections().ListByIDPrefix(ctx, aliceUID, invalidUTF8)
		require.ErrorIs(t, err, store.ErrInvalidArgument)
	})

	t.Run("edge boundaries (zero limit, empty user filter, cursor past range, MaxRune, zero owner)", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "edge-alice")
		aliceUID := userid.MustNew(alice.ID)
		uid := makePrefixedID(t, "edgA", "1")
		seedPrefixedUser(t, st, uid, "edg-user-1")

		// Zero limit with active IDPrefix returns an empty non-continuable page.
		zeroPage, err := st.Users().ListAll(ctx, store.ListAllUsersParams{
			IDPrefix:   "edgA",
			PageParams: store.PageParams{Limit: 0},
		})
		require.NoError(t, err)
		assert.Empty(t, zeroPage.Rows)
		assert.False(t, zeroPage.HasMore())

		// Cursor past prefix upper bound returns empty; malformed cursor fails with ErrInvalidCursor.
		pastCursor := store.EncodeCursor(time.Now().UTC(), "edgZ")
		pastPage, err := st.Users().ListAll(ctx, store.ListAllUsersParams{
			IDPrefix:   "edgA",
			PageParams: store.PageParams{Cursor: pastCursor, Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, pastPage.Rows)

		_, err = st.Users().ListAll(ctx, store.ListAllUsersParams{
			IDPrefix:   "edgA",
			PageParams: store.PageParams{Cursor: "not-a-valid-cursor", Limit: 10},
		})
		require.ErrorIs(t, err, store.ErrInvalidCursor)

		// All-MaxRune prefix has no upper bound and returns empty cleanly.
		maxRunePrefix := strings.Repeat(string(utf8.MaxRune), 2)
		maxPage, err := st.Users().ListAll(ctx, store.ListAllUsersParams{
			IDPrefix:   maxRunePrefix,
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, maxPage.Rows)

		// Non-nil empty *string UserID matches zero rows (never widens to all users).
		emptyUser := ""
		seedPrefixedWorker(t, st, makePrefixedID(t, "edgA", "w1"), alice.ID)
		wkPage, err := st.Workers().ListAdmin(ctx, store.ListWorkersAdminParams{
			UserID:     &emptyUser,
			IDPrefix:   "edgA",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, wkPage.Rows)

		tokPage, err := st.APITokens().ListAll(ctx, store.ListAllAPITokensParams{
			UserID:     &emptyUser,
			IDPrefix:   "edgA",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, tokPage.Rows)

		delPage, err := st.DelegationTokens().ListAll(ctx, store.ListAllDelegationTokensParams{
			UserID:     &emptyUser,
			IDPrefix:   "edgA",
			PageParams: store.PageParams{Limit: 10},
		})
		require.NoError(t, err)
		assert.Empty(t, delPage.Rows)

		// Zero owner UserID returns nil on tab & section prefix lookups; blank tab prefix returns ErrInvalidArgument.
		tabs, err := st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
			IDPrefix: "edgA",
		})
		require.NoError(t, err)
		assert.Empty(t, tabs)

		_, err = st.WorkspaceTabIndex().ListRenderedByIDPrefix(ctx, store.ListRenderedTabsByIDPrefixParams{
			UserID:   aliceUID,
			IDPrefix: "   ",
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)

		secsZero, err := st.WorkspaceSections().ListByIDPrefix(ctx, userid.UserID{}, "edgA")
		require.NoError(t, err)
		assert.Empty(t, secsZero)

		seedPrefixedSection(t, st, makePrefixedID(t, "edgA", "s1"), alice.ID, "Edge Sec", "a1")
		allSecs, err := st.WorkspaceSections().ListByIDPrefix(ctx, aliceUID, "")
		require.NoError(t, err)
		assert.NotEmpty(t, allSecs)
	})
}
