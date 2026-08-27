package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// seedAPIToken writes one api_tokens row for the env's user, held by the
// built-in control-CLI app, and returns its id -- with a recognisable secret
// so a leak test can look for it.
func seedAPIToken(t *testing.T, env *userTestEnv, installationName, grant string) string {
	t.Helper()
	return seedAPITokenForApp(t, env, oauthapp.ControlCLIClientID, installationName, grant)
}

// seedAPITokenForApp is seedAPIToken with the APP named, so a test can put two
// installations of one app beside an installation of another and watch which
// ones a disconnect takes.
func seedAPITokenForApp(t *testing.T, env *userTestEnv, clientID, installationName, grant string) string {
	t.Helper()
	tokenID := id.Generate()
	expires := time.Now().Add(time.Hour)
	refreshExpires := time.Now().Add(90 * 24 * time.Hour)
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientID:         clientID,
		InstallationName: installationName,
		GrantedScopes:    grant,
		SecretHash:       []byte("SECRET-HASH-MUST-NOT-LEAK"),
		RefreshHash:      []byte("REFRESH-HASH-MUST-NOT-LEAK"),
		ExpiresAt:        &expires,
		RefreshExpiresAt: &refreshExpires,
	}))
	return tokenID
}

func TestListMyAPITokens_ReportsTheAccountsOwnCredentials(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	laptop := seedAPIToken(t, env, "alice@laptop", "workspace:read")
	ciBot := seedAPIToken(t, env, "ci-bot", "admin:read admin:users admin:settings admin:workers")

	resp, err := env.client.ListMyAPITokens(context.Background(), authedReq(&leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTokens(), 2)

	byID := map[string]*leapmuxv1.MyAPIToken{}
	for _, tok := range resp.Msg.GetTokens() {
		byID[tok.GetId()] = tok
	}
	require.Contains(t, byID, laptop)
	require.Contains(t, byID, ciBot)
	assert.Equal(t, "alice@laptop", byID[laptop].GetInstallationName())
	assert.Equal(t, oauthapp.ControlCLIName, byID[laptop].GetClientName(),
		"the listing groups by APP, so it reports the registration's name and not the installation label")
	assert.Equal(t, []string{"workspace:read"}, byID[laptop].GetGrantedScopes())
	assert.Equal(t, []string{"admin:read", "admin:settings", "admin:users", "admin:workers"},
		byID[ciBot].GetGrantedScopes(), "an audit of hub administration reads this list for an admin: prefix")
	assert.NotNil(t, byID[laptop].GetRefreshExpiresAt(), "the deadline that sends a device back to a browser")
	// The handler derives `current` from the CALLER's own credential; a cookie
	// is not a CLI credential, so it marks nothing.
	assert.False(t, byID[laptop].GetCurrent())
	assert.False(t, byID[ciBot].GetCurrent())
}

// TestListMyAPITokens_LeaksNoSecret is the pin the plan asks for: the mapper
// copies METADATA only. A secret or a hash reaching the wire here would be a
// credential given to anything that can read one response.
func TestListMyAPITokens_LeaksNoSecret(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	seedAPIToken(t, env, "alice@laptop", "workspace:read")

	resp, err := env.client.ListMyAPITokens(context.Background(), authedReq(&leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)

	// Serialize the WHOLE response and search it, so this covers a field added
	// to the message later without anybody remembering to extend it.
	raw, err := protojson.Marshal(resp.Msg)
	require.NoError(t, err)
	body := string(raw)
	for _, forbidden := range []string{"SECRET-HASH", "REFRESH-HASH", "secretHash", "refreshHash", "secret_hash", "refresh_hash"} {
		assert.NotContains(t, body, forbidden, "no secret material may leave the store layer")
	}

	// And the message itself declares no such field, so the search above
	// cannot pass merely because somebody renamed the value.
	var shape map[string]any
	require.NoError(t, json.Unmarshal(raw, &shape))
	for _, tok := range shape["tokens"].([]any) {
		for key := range tok.(map[string]any) {
			assert.False(t, strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "hash"),
				"MyAPIToken must declare no secret-bearing field; found %q", key)
		}
	}
}

// TestListMyAPITokens_IsScopedToTheCaller pins tenancy: the listing is
// per-user, which is also what makes RevokeMyAPIToken's uniform NotFound
// safe (a caller has no legitimate way to learn another user's token id).
func TestListMyAPITokens_IsScopedToTheCaller(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	mine := seedAPIToken(t, env, "mine", "workspace:read")

	otherID := id.Generate()
	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID: otherID, Username: "other", PasswordHash: "hash", DisplayName: "Other", PasswordSet: true,
	}))
	expires := time.Now().Add(time.Hour)
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: id.Generate(), UserID: userid.MustNew(otherID), ClientID: oauthapp.ControlCLIClientID, InstallationName: "theirs", GrantedScopes: authscope.NonAdminGrant().String(),
		SecretHash: []byte("x"), ExpiresAt: &expires,
	}))

	resp, err := env.client.ListMyAPITokens(context.Background(), authedReq(&leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTokens(), 1)
	assert.Equal(t, mine, resp.Msg.GetTokens()[0].GetId())
}

// TestRevokeMyAPIToken_NeedsNoElevation is deliberate, and stated so it
// cannot be "tightened" by accident: revoking only REDUCES access, and
// demanding a fresh factor from somebody who believes a credential is stolen
// is the wrong failure mode.
func TestRevokeMyAPIToken_NeedsNoElevation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)
	tokenID := seedAPIToken(t, env, "alice@laptop", "workspace:read")

	_, err := env.client.RevokeMyAPIToken(ctx, authedReq(&leapmuxv1.RevokeMyAPITokenRequest{Id: tokenID}, env.token))
	require.NoError(t, err, "revocation must work on a session that proved nothing")

	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt)

	// It leaves the listing.
	resp, err := env.client.ListMyAPITokens(ctx, authedReq(&leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetTokens())
}

// TestRevokeMyAPIToken_RefusesAnotherUsersToken pins that the owner check
// lives in the statement, and that the refusal does not confirm the id
// exists: a missing token and somebody else's answer identically.
func TestRevokeMyAPIToken_RefusesAnotherUsersToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)

	otherID := id.Generate()
	require.NoError(t, env.store.Users().Create(ctx, store.CreateUserParams{
		ID: otherID, Username: "victim", PasswordHash: "hash", DisplayName: "Victim", PasswordSet: true,
	}))
	victimToken := id.Generate()
	expires := time.Now().Add(time.Hour)
	require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID: victimToken, UserID: userid.MustNew(otherID), ClientID: oauthapp.ControlCLIClientID, InstallationName: "victim-laptop", GrantedScopes: authscope.NonAdminGrant().String(),
		SecretHash: []byte("x"), ExpiresAt: &expires,
	}))

	_, err := env.client.RevokeMyAPIToken(ctx, authedReq(&leapmuxv1.RevokeMyAPITokenRequest{Id: victimToken}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, missingErr := env.client.RevokeMyAPIToken(ctx, authedReq(&leapmuxv1.RevokeMyAPITokenRequest{Id: "no-such-token"}, env.token))
	require.Error(t, missingErr)
	assert.Equal(t, connect.CodeOf(err), connect.CodeOf(missingErr),
		"a token that belongs to somebody else must be indistinguishable from one that does not exist")

	row, err := env.store.APITokens().GetByID(ctx, victimToken)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt, "the victim's credential must survive")
}

// TestRevokeMyAPIToken_TwiceIsNotFound pins that the handler does not
// silently report an already-revoked row as revoked again: the listing shows
// live rows only, so a second attempt is against something the caller can no
// longer see.
func TestRevokeMyAPIToken_TwiceIsNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)
	tokenID := seedAPIToken(t, env, "alice@laptop", "workspace:read")

	_, err := env.client.RevokeMyAPIToken(ctx, authedReq(&leapmuxv1.RevokeMyAPITokenRequest{Id: tokenID}, env.token))
	require.NoError(t, err)
	_, err = env.client.RevokeMyAPIToken(ctx, authedReq(&leapmuxv1.RevokeMyAPITokenRequest{Id: tokenID}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// registerSecondApp writes a hub-wide registration the disconnect tests use as
// the app that must SURVIVE, so a cascade that took every credential the
// account holds would fail rather than pass.
func registerSecondApp(t *testing.T, env *userTestEnv) string {
	t.Helper()
	clientID := id.Generate()
	require.NoError(t, env.store.OAuthClients().Create(context.Background(), store.CreateOAuthClientParams{
		ClientID:           clientID,
		ClientName:         "Other app",
		RedirectURIs:       "https://other.example.com/callback",
		Scopes:             "workspace:read",
		GrantTypes:         "authorization_code refresh_token",
		RegistrationSource: store.OAuthClientSourceAdmin,
	}))
	return clientID
}

// TestDisconnectApp_TakesEveryInstallationOfThatAppAndNothingElse is the whole
// verb.
//
// Disconnecting is what somebody does on deciding an app should no longer
// reach their account, so it must take EVERY machine that app runs on. An
// ending that took one installation would leave the app working everywhere
// else, which is the outcome the verb exists to prevent -- and that is what
// RevokeMyAPIToken is for instead.
//
// A second app is seeded so the cascade's other bound is tested too: an
// assertion that only checked the two rows disappearing would pass for a
// statement that revoked the account's whole credential set.
func TestDisconnectApp_TakesEveryInstallationOfThatAppAndNothingElse(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)
	laptop := seedAPIToken(t, env, "alice@laptop", "workspace:read")
	desktop := seedAPIToken(t, env, "alice@desktop", "workspace:read")
	otherApp := registerSecondApp(t, env)
	survivor := seedAPITokenForApp(t, env, otherApp, "alice@laptop", "workspace:read")

	resp, err := env.client.DisconnectApp(ctx,
		authedReq(&leapmuxv1.DisconnectAppRequest{ClientId: oauthapp.ControlCLIClientID}, env.token))
	require.NoError(t, err)
	assert.EqualValues(t, 2, resp.Msg.GetRevokedCredentialCount(),
		"a disconnect must take every installation of the app it names")

	for _, tokenID := range []string{laptop, desktop} {
		row, err := env.store.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		assert.NotNil(t, row.RevokedAt, "installation %q must be retired", row.InstallationName)
	}
	other, err := env.store.APITokens().GetByID(ctx, survivor)
	require.NoError(t, err)
	assert.Nil(t, other.RevokedAt, "another app's credential must survive")

	// The REGISTRATION is untouched. An account disconnecting an app says
	// nothing about whether the app should exist, and the two verbs live on
	// different services for that reason.
	app, err := env.store.OAuthClients().Get(ctx, oauthapp.ControlCLIClientID)
	require.NoError(t, err)
	assert.Nil(t, app.RevokedAt)
}

// TestDisconnectApp_IsScopedToTheCaller pins the cascade's user bound.
//
// The SAME store statement retires an app for everybody when its user is
// empty (AppService.RevokeApp). Binding the caller here is what keeps this
// self-service surface from reaching another account's credentials, and an
// empty id would take the whole-set arm.
func TestDisconnectApp_IsScopedToTheCaller(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)
	mine := seedAPIToken(t, env, "alice@laptop", "workspace:read")

	otherID := id.Generate()
	require.NoError(t, env.store.Users().Create(ctx, store.CreateUserParams{
		ID: otherID, Username: "victim", PasswordHash: "hash", DisplayName: "Victim", PasswordSet: true,
	}))
	victimToken := id.Generate()
	require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID: victimToken, UserID: userid.MustNew(otherID), ClientID: oauthapp.ControlCLIClientID,
		InstallationName: "victim-laptop", GrantedScopes: "workspace:read", SecretHash: []byte("x"),
	}))

	_, err := env.client.DisconnectApp(ctx,
		authedReq(&leapmuxv1.DisconnectAppRequest{ClientId: oauthapp.ControlCLIClientID}, env.token))
	require.NoError(t, err)

	row, err := env.store.APITokens().GetByID(ctx, mine)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt, "the caller's own credential must go")
	victim, err := env.store.APITokens().GetByID(ctx, victimToken)
	require.NoError(t, err)
	assert.Nil(t, victim.RevokedAt, "another account's credential must survive")
}

// TestDisconnectApp_AnAccountThatHoldsNothingSucceeds.
//
// ZERO retired rows is the caller's goal already holding, not a failure. A
// NotFound would make a client that raced a second tab report an error for the
// state it wanted, and it would confirm whether an unknown client_id names a
// real app -- which the catalogue's visibility rule refuses.
func TestDisconnectApp_AnAccountThatHoldsNothingSucceeds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)

	resp, err := env.client.DisconnectApp(ctx,
		authedReq(&leapmuxv1.DisconnectAppRequest{ClientId: oauthapp.ControlCLIClientID}, env.token))
	require.NoError(t, err)
	assert.EqualValues(t, 0, resp.Msg.GetRevokedCredentialCount())

	unknown, err := env.client.DisconnectApp(ctx,
		authedReq(&leapmuxv1.DisconnectAppRequest{ClientId: "no-such-app"}, env.token))
	require.NoError(t, err, "an unknown client_id must not confirm whether the app exists")
	assert.EqualValues(t, 0, unknown.Msg.GetRevokedCredentialCount())
}

// TestDisconnectApp_RefusesAnEmptyClientID. Empty is the value that would take
// the store statement's WHOLE-SET arm, so it must never reach it.
func TestDisconnectApp_RefusesAnEmptyClientID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)
	mine := seedAPIToken(t, env, "alice@laptop", "workspace:read")

	_, err := env.client.DisconnectApp(ctx, authedReq(&leapmuxv1.DisconnectAppRequest{}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	row, err := env.store.APITokens().GetByID(ctx, mine)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt, "the refusal must write nothing")
}

// TestDisconnectApp_NeedsNoElevation, for the reason the whole file states:
// ending an app's access only REDUCES it, and demanding a fresh factor from
// somebody who just realized an app is malicious is the wrong failure mode.
func TestDisconnectApp_NeedsNoElevation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)
	seedAPIToken(t, env, "alice@laptop", "workspace:read")

	// env.token is a plain session with no elevation window.
	resp, err := env.client.DisconnectApp(ctx,
		authedReq(&leapmuxv1.DisconnectAppRequest{ClientId: oauthapp.ControlCLIClientID}, env.token))
	require.NoError(t, err, "disconnecting must not demand a proven factor")
	assert.EqualValues(t, 1, resp.Msg.GetRevokedCredentialCount())
}

// TestListMyAPITokens_StatesWhetherSomebodyVouchedForTheApp pins the vouch on
// the connected-app list. The proto field existed and the panel rendered its
// badge, but the JOIN, the row and the mapper all omitted the fact -- so every
// app, including one an administrator vouched for and the hub's own built-in
// CLI, labelled "unverified" on exactly the screen a person consults to decide
// what to disconnect.
func TestListMyAPITokens_StatesWhetherSomebodyVouchedForTheApp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)

	// A vouched third-party app.
	vouchedApp := id.Generate()
	vouchedAt := time.Now().UTC()
	require.NoError(t, env.store.OAuthClients().Create(ctx, store.CreateOAuthClientParams{
		ClientID:           vouchedApp,
		ClientName:         "Vouched app",
		RedirectURIs:       "https://vouched.example.com/callback",
		Scopes:             "workspace:read",
		GrantTypes:         "authorization_code refresh_token",
		RegistrationSource: store.OAuthClientSourceAdmin,
		VerifiedAt:         &vouchedAt,
		VerifiedBy:         env.userID,
	}))
	// An UNvouched third-party app.
	plainApp := registerSecondApp(t, env)

	seedAPITokenForApp(t, env, vouchedApp, "alice@laptop", "workspace:read")
	seedAPITokenForApp(t, env, plainApp, "alice@laptop", "workspace:read")
	seedAPIToken(t, env, "alice@laptop", "workspace:read") // the built-in CLI

	resp, err := env.client.ListMyAPITokens(ctx,
		authedReq(&leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)

	verifiedByClient := map[string]bool{}
	for _, row := range resp.Msg.GetTokens() {
		verifiedByClient[row.GetClientId()] = row.GetClientVerified()
	}
	assert.True(t, verifiedByClient[vouchedApp], "an administrator vouched; the list states it")
	assert.True(t, verifiedByClient[oauthapp.ControlCLIClientID],
		"a built-in registration is verified by construction: the build is its author")
	assert.False(t, verifiedByClient[plainApp], "nobody vouched; the badge stays")
}
