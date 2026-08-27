package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// testOAuthClients covers the app registry across every dialect.
//
// It runs on Postgres and MySQL under -tags integration as well as on SQLite,
// which is the point: the visibility rule is a NULL comparison, the half-vouch
// rule is a CHECK constraint, and the delete refusal is a RESTRICT foreign key.
// All three are things the dialects express differently and sqlite alone would
// not settle.
func (s *Suite) testOAuthClients(t *testing.T) {
	ctx := context.Background()

	t.Run("the migration seeds the built-in registrations", func(t *testing.T) {
		st := s.NewStore(t)
		for _, clientID := range oauthapp.BuiltInClientIDs() {
			app, err := st.OAuthClients().Get(ctx, clientID)
			require.NoErrorf(t, err, "the migration must seed %s", clientID)
			assert.Equal(t, store.OAuthClientSourceBuiltin, app.RegistrationSource)
			assert.Empty(t, app.OwnerUserID, "a built-in registration is hub-wide")
			// Against the column's FALSE default, and stated in the migration
			// on purpose: a seeded row that lost it would take
			// `leapmux control admin ...` away with no error anybody could
			// trace back to a schema edit.
			assert.Truef(t, app.ElevationAllowed,
				"%s must ship with the step-up leg available", clientID)
		}

		cli, err := st.OAuthClients().Get(ctx, oauthapp.ControlCLIClientID)
		require.NoError(t, err)
		assert.Equal(t, oauthapp.ControlCLIName, cli.ClientName)
		assert.Contains(t, cli.RedirectURIs, oauthapp.ControlCLIRedirectURI,
			"the CLI's loopback address is a constant of the build")
		// The ceiling and the flow set are as constant as the address: the ids
		// and names were pinned here already, but the SCOPE string was not --
		// so one dialect's migration could drift from the other two with no
		// test noticing, and a CLI credential would silently authenticate
		// against a narrower or wider set depending on the store engine.
		assert.Equal(t, oauthapp.ControlCLIScopes, cli.Scopes,
			"the CLI's seeded ceiling is a constant of the build")
		assert.Equal(t, oauthapp.ControlCLIGrantTypes, cli.GrantTypes,
			"the CLI's seeded flow set is a constant of the build")

		// The service account runs NO flow, which is what makes it an answer to
		// "which app holds this credential" rather than a client.
		svc, err := st.OAuthClients().Get(ctx, oauthapp.ServiceAccountClientID)
		require.NoError(t, err)
		assert.Empty(t, svc.RedirectURIs)
		assert.Empty(t, svc.GrantTypes)
		assert.Equal(t, oauthapp.ServiceAccountScopes, svc.Scopes,
			"the service account's seeded ceiling is a constant of the build")
	})

	t.Run("visibility follows one column", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "alice")
		bob := SeedUser(t, st, "bob")

		private := seedApp(t, st, "alice's app", alice.ID, store.OAuthClientSourceUser)
		hubWide := seedApp(t, st, "everyone's app", "", store.OAuthClientSourceAdmin)

		// Alice AUTHORIZES her own app plus the catalogue.
		visible := listIDs(t, st.OAuthClients().ListVisibleTo, ctx, alice.ID)
		assert.Contains(t, visible, private)
		assert.Contains(t, visible, hubWide)

		// Bob sees the catalogue and NOT Alice's app. This is the whole rule,
		// and it is a NULL comparison rather than a flag: owner_user_id IS NULL
		// means hub-wide, and there is no second column to disagree.
		bobVisible := listIDs(t, st.OAuthClients().ListVisibleTo, ctx, bob.ID)
		assert.NotContains(t, bobVisible, private, "another user's private app must be invisible")
		assert.Contains(t, bobVisible, hubWide)

		// OWNED-BY answers a different question: what may this account EDIT.
		// The catalogue is authorizable by everybody and editable by nobody
		// through this listing.
		owned := listIDs(t, st.OAuthClients().ListOwnedBy, ctx, alice.ID)
		assert.Contains(t, owned, private)
		assert.NotContains(t, owned, hubWide, "the hub-wide catalogue is not an account's own")
		assert.Empty(t, listIDs(t, st.OAuthClients().ListOwnedBy, ctx, bob.ID))

		// And a ZERO caller owns nothing. An unminted id unwraps to "", which
		// would match every blank-owner row -- that is, the whole hub-wide
		// catalogue -- if the owner filter were not there.
		zero, err := st.OAuthClients().ListOwnedBy(ctx, store.ListOAuthClientsParams{
			PageParams: store.PageParams{Limit: 50},
		})
		require.NoError(t, err)
		assert.Empty(t, zero.Rows, "an unminted caller owns nothing, not everything hub-wide")
	})

	t.Run("a vouch moves both columns or neither", func(t *testing.T) {
		st := s.NewStore(t)
		admin := SeedUser(t, st, "vouching-admin")
		clientID := seedApp(t, st, "unverified", "", store.OAuthClientSourceDynamic)

		app, err := st.OAuthClients().Get(ctx, clientID)
		require.NoError(t, err)
		assert.False(t, app.IsVerified(), "a self-registered app starts unverified")

		now := time.Now().UTC().Truncate(time.Second)
		_, err = st.OAuthClients().SetVerified(ctx, store.SetOAuthClientVerifiedParams{
			ClientID: clientID, VerifiedAt: &now, VerifiedBy: admin.ID,
		})
		require.NoError(t, err)
		app, err = st.OAuthClients().Get(ctx, clientID)
		require.NoError(t, err)
		require.NotNil(t, app.VerifiedAt)
		assert.Equal(t, admin.ID, app.VerifiedBy)

		// Withdrawing clears BOTH. The CHECK constraint refuses a half-vouch,
		// so a store that cleared one column would fail here on every dialect
		// rather than leaving a row the consent page cannot describe.
		_, err = st.OAuthClients().SetVerified(ctx, store.SetOAuthClientVerifiedParams{ClientID: clientID})
		require.NoError(t, err)
		app, err = st.OAuthClients().Get(ctx, clientID)
		require.NoError(t, err)
		assert.Nil(t, app.VerifiedAt)
		assert.Empty(t, app.VerifiedBy)
	})

	t.Run("deleting the vouching administrator leaves the vouch", func(t *testing.T) {
		st := s.NewStore(t)
		admin := SeedUser(t, st, "departing-admin")
		clientID := seedApp(t, st, "vouched-for", "", store.OAuthClientSourceAdmin)

		now := time.Now().UTC().Truncate(time.Second)
		_, err := st.OAuthClients().SetVerified(ctx, store.SetOAuthClientVerifiedParams{
			ClientID: clientID, VerifiedAt: &now, VerifiedBy: admin.ID,
		})
		require.NoError(t, err)

		// The delete must SUCCEED. verified_by_user_id carries no foreign key
		// precisely so it can: an ON DELETE SET NULL would clear it while
		// verified_at stayed set, which the half-vouch CHECK forbids -- so the
		// user delete would fail, on every dialect that accepted the schema at
		// all.
		require.NoError(t, st.Users().Delete(ctx, admin.ID))
		purged, err := st.Cleanup().HardDeleteUsersBefore(ctx, time.Now().Add(time.Hour))
		require.NoError(t, err)
		require.EqualValues(t, 1, purged)

		// The vouch SURVIVES, attributed to an id that no longer resolves. An
		// administrator vouched for this app on this date, and deleting their
		// account does not unmake that; the surface renders a name it cannot
		// resolve as no name.
		app, err := st.OAuthClients().Get(ctx, clientID)
		require.NoError(t, err)
		assert.True(t, app.IsVerified(), "the vouch is a fact, not a live reference")
		assert.Equal(t, admin.ID, app.VerifiedBy)
	})

	t.Run("deleting the owner takes the private app and leaves the catalogue", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "departing")
		private := seedApp(t, st, "alice's app", alice.ID, store.OAuthClientSourceUser)
		hubWide := seedApp(t, st, "the catalogue", "", store.OAuthClientSourceAdmin)

		// A HARD delete, because a soft delete leaves owner_user_id pointing at
		// a row the cascade never fires for. The cleanup sweep is the only
		// surface that performs one, so the test drives it the way production
		// does rather than reaching past it.
		require.NoError(t, st.Users().Delete(ctx, alice.ID))
		purged, err := st.Cleanup().HardDeleteUsersBefore(ctx, time.Now().Add(time.Hour))
		require.NoError(t, err)
		require.EqualValues(t, 1, purged)

		_, err = st.OAuthClients().Get(ctx, private)
		RequireNotFound(t, err)
		_, err = st.OAuthClients().Get(ctx, hubWide)
		assert.NoError(t, err, "a hub-wide app has no owner to lose")
	})

	t.Run("a revocation cascades to every account's credentials", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "one")
		bob := SeedUser(t, st, "two")
		clientID := seedApp(t, st, "shared", "", store.OAuthClientSourceAdmin)

		tokens := map[string]string{}
		for _, owner := range []string{alice.ID, bob.ID} {
			tokenID := id.Generate()
			require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
				ID: tokenID, UserID: userid.MustNew(owner), ClientID: clientID,
				InstallationName: "laptop", GrantedScopes: "workspace:read",
				SecretHash: []byte("hash"),
			}))
			tokens[owner] = tokenID
		}

		// The refs are read BEFORE the cascade, which is what lets the caller
		// apply each row's lifecycle effects after the commit -- effects
		// accumulate, and a retried transaction would double-apply them.
		refs, err := st.OAuthClients().ListTokenRefs(ctx, store.RevokeAPITokensForClientParams{ClientID: clientID})
		require.NoError(t, err)
		assert.Len(t, refs, 2)
		// Each ref carries its own GRANT, because a caller that is NARROWING
		// the app's ceiling rather than retiring it decides per credential
		// which ones actually lose something. A blank here would make that
		// caller read every credential as losing nothing.
		for _, ref := range refs {
			assert.NotEmptyf(t, ref.GrantedScopes,
				"the ref for %s must carry the grant a ceiling change is measured against", ref.ID)
		}

		n, err := st.OAuthClients().RevokeTokens(ctx, store.RevokeAPITokensForClientParams{ClientID: clientID})
		require.NoError(t, err)
		assert.EqualValues(t, 2, n, "an app revocation reaches every account")

		for owner, tokenID := range tokens {
			row, err := st.APITokens().GetByID(ctx, tokenID)
			require.NoError(t, err)
			assert.NotNilf(t, row.RevokedAt, "the credential held for %s must be revoked", owner)
		}

		// A DISCONNECT names one user and takes that account's rows alone.
		other := seedApp(t, st, "second", "", store.OAuthClientSourceAdmin)
		for _, owner := range []string{alice.ID, bob.ID} {
			require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
				ID: id.Generate(), UserID: userid.MustNew(owner), ClientID: other,
				InstallationName: "laptop", GrantedScopes: "workspace:read",
				SecretHash: []byte("hash"),
			}))
		}
		n, err = st.OAuthClients().RevokeTokens(ctx, store.RevokeAPITokensForClientParams{
			ClientID: other, UserID: alice.ID,
		})
		require.NoError(t, err)
		assert.EqualValues(t, 1, n, "a disconnect is one account's decision")
	})

	// The app's REGISTERED ceiling, joined onto every credential read.
	//
	// loadBearer narrows a stored grant to it at validation, so a dialect whose
	// join dropped the column would hand every credential an EMPTY ceiling --
	// and an empty ceiling intersects every grant to nothing. The failure is
	// total and silent at the schema level, which is why it belongs here rather
	// than in a single-dialect test: three hand-written SELECTs must agree.
	t.Run("every credential read carries its app's permission ceiling", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "ceiling-reader")
		owner := userid.MustNew(alice.ID)
		clientID := seedApp(t, st, "ceilinged", alice.ID, store.OAuthClientSourceUser)

		tokenID := id.Generate()
		require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID: tokenID, UserID: owner, ClientID: clientID,
			InstallationName: "laptop", GrantedScopes: "workspace:read",
			SecretHash: []byte("hash"),
		}))

		// seedApp registers workspace:read, so a blank answer here is the
		// dropped-join failure rather than an app that really reaches nothing.
		row, err := st.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		assert.Equal(t, "workspace:read", row.ClientScopes,
			"the validation path reads the ceiling off this join")

		// And the LISTINGS carry it too, on the same join. A read that answered
		// it in one query and not another would make the ceiling depend on
		// which surface asked.
		page, err := st.APITokens().ListByUser(ctx, store.ListAPITokensByUserParams{
			UserID: owner, PageParams: store.PageParams{Limit: 50},
		})
		require.NoError(t, err)
		require.Len(t, page.Rows, 1)
		assert.Equal(t, "workspace:read", page.Rows[0].ClientScopes)
	})

	t.Run("a delete is refused while any credential row exists", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "deleter")
		owner := userid.MustNew(alice.ID)
		clientID := seedApp(t, st, "held", alice.ID, store.OAuthClientSourceUser)

		tokenID := id.Generate()
		require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID: tokenID, UserID: owner, ClientID: clientID,
			InstallationName: "laptop", GrantedScopes: "workspace:read", SecretHash: []byte("hash"),
		}))

		held, err := st.OAuthClients().CountTokens(ctx, clientID)
		require.NoError(t, err)
		assert.EqualValues(t, 1, held)

		// REVOKING does not release the foreign key, and CountTokens is what
		// says so. CountLiveTokens answers a different question, and using it
		// as the delete precondition told an operator to revoke and then
		// refused the delete anyway.
		_, err = st.APITokens().Revoke(ctx, tokenID)
		require.NoError(t, err)
		live, err := st.OAuthClients().CountLiveTokens(ctx, clientID)
		require.NoError(t, err)
		assert.Zero(t, live)
		held, err = st.OAuthClients().CountTokens(ctx, clientID)
		require.NoError(t, err)
		assert.EqualValues(t, 1, held, "a revoked credential is history and still holds the key")
	})

	t.Run("a delete clears the flow's one-shot rows", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "flow-owner")
		owner := userid.MustNew(alice.ID)
		clientID := seedApp(t, st, "ran-a-flow", alice.ID, store.OAuthClientSourceUser)

		// An ABANDONED device flow and an unredeemed authorization code. Both
		// reference the app under the same RESTRICT key as a credential, but
		// neither is history: a device grant lives ten minutes and a code one.
		require.NoError(t, st.DeviceAuthorizations().Create(ctx, store.CreateDeviceAuthorizationParams{
			ClientID:   clientID,
			DeviceCode: id.Generate(), UserCode: "ABC-DEF",
			IntervalSeconds: 5, ExpiresAt: time.Now().Add(10 * time.Minute),
		}))
		require.NoError(t, st.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
			Code: id.Generate(), UserID: owner, ClientID: clientID,
			CodeChallenge: "challenge", RedirectURI: "https://app.example.com/callback",
			GrantedScopes: "workspace:read", InstallationName: "laptop",
			ExpiresAt: time.Now().Add(time.Minute),
		}))

		rows, err := st.OAuthClients().Delete(ctx, store.OAuthClientOwnershipParams{
			ClientID: clientID, CallerUserID: owner,
		})
		require.NoError(t, err, "an abandoned flow must not make an app undeletable")
		assert.EqualValues(t, 1, rows)

		_, err = st.OAuthClients().Get(ctx, clientID)
		RequireNotFound(t, err)
	})

	t.Run("a built-in registration refuses every write but its elevation", func(t *testing.T) {
		st := s.NewStore(t)
		admin := SeedUser(t, st, "builtin-admin")
		owner := userid.MustNew(admin.ID)

		for _, write := range []struct {
			name string
			call func() (int64, error)
		}{
			{"update", func() (int64, error) {
				return st.OAuthClients().Update(ctx, store.UpdateOAuthClientParams{
					ClientID: oauthapp.ControlCLIClientID, ClientName: "renamed",
					CallerUserID: owner, CallerIsAdmin: true,
				})
			}},
			{"revoke", func() (int64, error) {
				return st.OAuthClients().Revoke(ctx, store.OAuthClientOwnershipParams{
					ClientID: oauthapp.ControlCLIClientID, CallerUserID: owner, CallerIsAdmin: true,
				})
			}},
			{"delete", func() (int64, error) {
				return st.OAuthClients().Delete(ctx, store.OAuthClientOwnershipParams{
					ClientID: oauthapp.ControlCLIClientID, CallerUserID: owner, CallerIsAdmin: true,
				})
			}},
		} {
			rows, err := write.call()
			require.NoErrorf(t, err, "%s must refuse by matching no row, not by failing", write.name)
			assert.Zerof(t, rows, "%s must not touch a built-in registration", write.name)
		}

		app, err := st.OAuthClients().Get(ctx, oauthapp.ControlCLIClientID)
		require.NoError(t, err)
		assert.Equal(t, oauthapp.ControlCLIName, app.ClientName)
		assert.Nil(t, app.RevokedAt)

		// The ONE exception, and it has to be one: an operator who does not
		// want `leapmux control admin` to elevate must be able to say so.
		rows, err := st.OAuthClients().SetElevationAllowed(ctx, store.SetOAuthClientElevationAllowedParams{
			ClientID: oauthapp.ControlCLIClientID, ElevationAllowed: false,
			CallerUserID: owner, CallerIsAdmin: true,
		})
		require.NoError(t, err)
		assert.EqualValues(t, 1, rows)
		app, err = st.OAuthClients().Get(ctx, oauthapp.ControlCLIClientID)
		require.NoError(t, err)
		assert.False(t, app.ElevationAllowed)
	})

	t.Run("a write refuses an unminted caller", func(t *testing.T) {
		st := s.NewStore(t)
		alice := SeedUser(t, st, "guarded")
		clientID := seedApp(t, st, "guarded-app", alice.ID, store.OAuthClientSourceUser)

		// A ZERO caller id unwraps to "", which would MATCH every blank-owner
		// row -- the whole hub-wide catalogue -- rather than none. Each write
		// refuses it outright instead, which is what userid.OwnerFilter is for.
		for name, call := range map[string]func() (int64, error){
			"update": func() (int64, error) {
				return st.OAuthClients().Update(ctx, store.UpdateOAuthClientParams{
					ClientID: clientID, ClientName: "stolen",
				})
			},
			"revoke": func() (int64, error) {
				return st.OAuthClients().Revoke(ctx, store.OAuthClientOwnershipParams{ClientID: clientID})
			},
			"delete": func() (int64, error) {
				return st.OAuthClients().Delete(ctx, store.OAuthClientOwnershipParams{ClientID: clientID})
			},
			"set elevation": func() (int64, error) {
				return st.OAuthClients().SetElevationAllowed(ctx, store.SetOAuthClientElevationAllowedParams{
					ClientID: clientID, ElevationAllowed: true,
				})
			},
			"set icon": func() (int64, error) {
				return st.OAuthClients().SetIcon(ctx, store.SetOAuthClientIconParams{
					ClientID: clientID, IconBlob: []byte("x"), IconMediaType: "image/png",
				})
			},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := call()
				assert.ErrorIs(t, err, store.ErrInvalidArgument)
			})
		}

		app, err := st.OAuthClients().Get(ctx, clientID)
		require.NoError(t, err)
		assert.Equal(t, "guarded-app", app.ClientName)
		assert.Nil(t, app.RevokedAt)
	})
}

// seedApp registers one app. An EMPTY owner is hub-wide, which is the same
// convention the column carries.
func seedApp(t *testing.T, st store.Store, name, owner, source string) string {
	t.Helper()
	clientID := id.Generate()
	require.NoError(t, st.OAuthClients().Create(context.Background(), store.CreateOAuthClientParams{
		ClientID:           clientID,
		OwnerUserID:        owner,
		CreatedBy:          owner,
		ClientName:         name,
		RedirectURIs:       "https://app.example.com/callback",
		Scopes:             "workspace:read",
		GrantTypes:         "authorization_code refresh_token",
		RegistrationSource: source,
	}))
	return clientID
}

// listIDs pages one listing to the end and returns the client ids, so a caller
// asserts on membership rather than on page boundaries.
func listIDs(
	t *testing.T,
	list func(context.Context, store.ListOAuthClientsParams) (store.Page[store.OAuthClient], error),
	ctx context.Context,
	userID string,
) []string {
	t.Helper()
	var out []string
	cursor := ""
	for {
		page, err := list(ctx, store.ListOAuthClientsParams{
			UserID:     userid.MustNew(userID),
			PageParams: store.PageParams{Cursor: cursor, Limit: 50},
		})
		require.NoError(t, err)
		for _, row := range page.Rows {
			out = append(out, row.ClientID)
		}
		if page.NextCursor == "" {
			return out
		}
		cursor = page.NextCursor
	}
}
