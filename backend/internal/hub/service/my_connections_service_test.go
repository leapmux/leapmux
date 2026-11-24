package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/util/verifycode"
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
		Issuance:         store.APITokenIssuanceAuthorizationCode,
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

	resp, err := env.client.ListMyAPITokens(authedReq(context.Background(), &leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.GetTokens(), 2)

	byID := map[string]*leapmuxv1.MyAPIToken{}
	for _, tok := range resp.GetTokens() {
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

	resp, err := env.client.ListMyAPITokens(authedReq(context.Background(), &leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)

	// Serialize the WHOLE response and search it, so this covers a field added
	// to the message later without anybody remembering to extend it.
	raw, err := protojson.Marshal(resp)
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
		ID: otherID, Username: "other", PasswordHash: "hash", DisplayName: "Other", FirstCredentialExempt: true,
	}))
	expires := time.Now().Add(time.Hour)
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		Issuance: store.APITokenIssuanceAuthorizationCode,
		ID:       id.Generate(), UserID: userid.MustNew(otherID), ClientID: oauthapp.ControlCLIClientID, InstallationName: "theirs", GrantedScopes: authscope.NonAdminGrant().String(),
		SecretHash: []byte("x"), ExpiresAt: &expires,
	}))

	resp, err := env.client.ListMyAPITokens(authedReq(context.Background(), &leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.GetTokens(), 1)
	assert.Equal(t, mine, resp.GetTokens()[0].GetId())
}

// TestListMyAPITokens_PagesThroughTheTokens pins the page wiring: a page limit
// caps one reply, the reply states where the next page starts, and the final
// page states that no rows remain. The two pages hold every token once.
func TestListMyAPITokens_PagesThroughTheTokens(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	seeded := []string{
		seedAPIToken(t, env, "first", "workspace:read"),
		seedAPIToken(t, env, "second", "workspace:read"),
		seedAPIToken(t, env, "third", "workspace:read"),
	}
	list := func(page *leapmuxv1.PageRequest) *leapmuxv1.ListMyAPITokensResponse {
		t.Helper()
		resp, err := env.client.ListMyAPITokens(authedReq(context.Background(),
			&leapmuxv1.ListMyAPITokensRequest{Page: page}, env.token))
		require.NoError(t, err)
		return resp
	}

	first := list(&leapmuxv1.PageRequest{Limit: 2})
	require.Len(t, first.GetTokens(), 2, "the limit caps one page")
	require.NotEmpty(t, first.GetPage().GetNextCursor(), "a full page states where the next one starts")

	last := list(&leapmuxv1.PageRequest{Limit: 2, Cursor: first.GetPage().GetNextCursor()})
	require.Len(t, last.GetTokens(), 1)
	require.NotNil(t, last.GetPage(), "the final page still carries its page message")
	assert.Empty(t, last.GetPage().GetNextCursor(), "the final page states that no rows remain")

	var listed []string
	for _, tok := range append(first.GetTokens(), last.GetTokens()...) {
		listed = append(listed, tok.GetId())
	}
	assert.ElementsMatch(t, seeded, listed, "the pages hold every token once")
}

// A cursor the store cannot parse is the caller's input, so the listing
// answers InvalidArgument and states how to recover, never Internal.
func TestListMyAPITokens_RefusesAMalformedCursor(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	_, err := env.client.ListMyAPITokens(authedReq(context.Background(), &leapmuxv1.ListMyAPITokensRequest{
		Page: &leapmuxv1.PageRequest{Cursor: "not-a-cursor"},
	}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "omit it to start from the first page")
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

	_, err := env.client.RevokeMyAPIToken(authedReq(ctx, &leapmuxv1.RevokeMyAPITokenRequest{Id: tokenID}, env.token))
	require.NoError(t, err, "revocation must work on a session that proved nothing")

	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt)

	// It leaves the listing.
	resp, err := env.client.ListMyAPITokens(authedReq(ctx, &leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)
	assert.Empty(t, resp.GetTokens())
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
		ID: otherID, Username: "victim", PasswordHash: "hash", DisplayName: "Victim", FirstCredentialExempt: true,
	}))
	victimToken := id.Generate()
	expires := time.Now().Add(time.Hour)
	require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		Issuance: store.APITokenIssuanceAuthorizationCode,
		ID:       victimToken, UserID: userid.MustNew(otherID), ClientID: oauthapp.ControlCLIClientID, InstallationName: "victim-laptop", GrantedScopes: authscope.NonAdminGrant().String(),
		SecretHash: []byte("x"), ExpiresAt: &expires,
	}))

	_, err := env.client.RevokeMyAPIToken(authedReq(ctx, &leapmuxv1.RevokeMyAPITokenRequest{Id: victimToken}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, missingErr := env.client.RevokeMyAPIToken(authedReq(ctx, &leapmuxv1.RevokeMyAPITokenRequest{Id: "no-such-token"}, env.token))
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

	_, err := env.client.RevokeMyAPIToken(authedReq(ctx, &leapmuxv1.RevokeMyAPITokenRequest{Id: tokenID}, env.token))
	require.NoError(t, err)
	_, err = env.client.RevokeMyAPIToken(authedReq(ctx, &leapmuxv1.RevokeMyAPITokenRequest{Id: tokenID}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// registerSecondApp writes a hub-wide registration the disconnect tests use as
// the app that must SURVIVE, so a cascade that took every credential the
// account holds would fail rather than pass.
func registerSecondApp(t *testing.T, env *userTestEnv) string {
	t.Helper()
	clientID := id.Generate()
	_, createErr := env.store.OAuthClients().Create(context.Background(), store.CreateOAuthClientParams{
		ClientID:           clientID,
		ClientName:         "Other app",
		RedirectURIs:       "https://other.example.com/callback",
		Scopes:             "workspace:read",
		GrantTypes:         "authorization_code refresh_token",
		RegistrationSource: store.OAuthClientSourceAdmin,
	})
	require.NoError(t, createErr)
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

	resp, err := env.client.DisconnectApp(authedReq(ctx,
		&leapmuxv1.DisconnectAppRequest{ClientId: oauthapp.ControlCLIClientID}, env.token))
	require.NoError(t, err)
	assert.EqualValues(t, 2, resp.GetRevokedCredentialCount(),
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
		ID: otherID, Username: "victim", PasswordHash: "hash", DisplayName: "Victim", FirstCredentialExempt: true,
	}))
	victimToken := id.Generate()
	require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		Issuance: store.APITokenIssuanceAuthorizationCode,
		ID:       victimToken, UserID: userid.MustNew(otherID), ClientID: oauthapp.ControlCLIClientID,
		InstallationName: "victim-laptop", GrantedScopes: "workspace:read", SecretHash: []byte("x"),
	}))

	_, err := env.client.DisconnectApp(authedReq(ctx,
		&leapmuxv1.DisconnectAppRequest{ClientId: oauthapp.ControlCLIClientID}, env.token))
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

	resp, err := env.client.DisconnectApp(authedReq(ctx,
		&leapmuxv1.DisconnectAppRequest{ClientId: oauthapp.ControlCLIClientID}, env.token))
	require.NoError(t, err)
	assert.EqualValues(t, 0, resp.GetRevokedCredentialCount())

	unknown, err := env.client.DisconnectApp(authedReq(ctx,
		&leapmuxv1.DisconnectAppRequest{ClientId: "no-such-app"}, env.token))
	require.NoError(t, err, "an unknown client_id must not confirm whether the app exists")
	assert.EqualValues(t, 0, unknown.GetRevokedCredentialCount())
}

// TestDisconnectApp_RefusesAnEmptyClientID. Empty is the value that would take
// the store statement's WHOLE-SET arm, so it must never reach it.
func TestDisconnectApp_RefusesAnEmptyClientID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)
	mine := seedAPIToken(t, env, "alice@laptop", "workspace:read")

	_, err := env.client.DisconnectApp(authedReq(ctx, &leapmuxv1.DisconnectAppRequest{}, env.token))
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
	resp, err := env.client.DisconnectApp(authedReq(ctx,
		&leapmuxv1.DisconnectAppRequest{ClientId: oauthapp.ControlCLIClientID}, env.token))
	require.NoError(t, err, "disconnecting must not demand a proven factor")
	assert.EqualValues(t, 1, resp.GetRevokedCredentialCount())
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
	_, createErr := env.store.OAuthClients().Create(ctx, store.CreateOAuthClientParams{
		ClientID:           vouchedApp,
		ClientName:         "Vouched app",
		RedirectURIs:       "https://vouched.example.com/callback",
		Scopes:             "workspace:read",
		GrantTypes:         "authorization_code refresh_token",
		RegistrationSource: store.OAuthClientSourceAdmin,
		VerifiedAt:         &vouchedAt,
		VerifiedBy:         env.userID,
	})
	require.NoError(t, createErr)
	// An UNvouched third-party app.
	plainApp := registerSecondApp(t, env)

	seedAPITokenForApp(t, env, vouchedApp, "alice@laptop", "workspace:read")
	seedAPITokenForApp(t, env, plainApp, "alice@laptop", "workspace:read")
	seedAPIToken(t, env, "alice@laptop", "workspace:read") // the built-in CLI

	resp, err := env.client.ListMyAPITokens(authedReq(ctx,
		&leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)

	verifiedByClient := map[string]bool{}
	for _, row := range resp.GetTokens() {
		verifiedByClient[row.GetClientId()] = row.GetClientVerified()
	}
	assert.True(t, verifiedByClient[vouchedApp], "an administrator vouched; the list states it")
	assert.True(t, verifiedByClient[oauthapp.ControlCLIClientID],
		"a built-in registration is verified by construction: the build is its author")
	assert.False(t, verifiedByClient[plainApp], "nobody vouched; the badge stays")
}

// TestDisconnectApp_SpendsOutstandingOneShotGrants pins the disconnect's
// second half: an authorization code the account allowed seconds before
// disconnecting, and a device grant the account approved but the app had not
// polled yet, must die with the authorization -- otherwise each stays
// redeemable into a fresh credential for its whole TTL, for an app the
// account just cut off.
func TestDisconnectApp_SpendsOutstandingOneShotGrants(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	ctx := context.Background()
	uid := userid.MustNew(env.userID)

	code := id.Generate()
	require.NoError(t, env.store.OAuthAuthorizationCodes().Create(ctx, store.CreateOAuthAuthorizationCodeParams{
		Code:          code,
		UserID:        uid,
		ClientID:      oauthapp.ControlCLIClientID,
		CodeChallenge: "challenge",
		RedirectURI:   "http://127.0.0.1:54321/callback",
		GrantedScopes: "workspace:read",
		ExpiresAt:     time.Now().Add(10 * time.Minute),
		Redemption:    store.RedemptionKindToken,
	}))

	deviceCode := id.Generate()
	userCode := id.Generate()
	require.NoError(t, env.deviceAuths.Create(service.CreateDeviceAuthorizationParams{
		DeviceCodeHash: hubtestutil.TestSecretHasher().Hash(deviceCode),
		UserCode:       userCode,
		ClientID:       oauthapp.ControlCLIClientID,
		ExpiresAt:      time.Now().Add(10 * time.Minute),
	}))
	approved, err := env.deviceAuths.ApproveByUserCode(service.ApproveDeviceAuthorizationParams{
		UserCode: userCode,
		UserID:   uid,
	}, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, approved)

	_, err = env.client.DisconnectApp(authedReq(ctx, &leapmuxv1.DisconnectAppRequest{
		ClientId: oauthapp.ControlCLIClientID,
	}, env.token))
	require.NoError(t, err)

	_, err = env.store.OAuthAuthorizationCodes().GetActive(ctx, code, time.Now().UTC())
	assert.Error(t, err, "the outstanding authorization code must die with the disconnect")

	row, ok := env.deviceAuths.Get(hubtestutil.TestSecretHasher().Hash(deviceCode))
	require.True(t, ok)
	assert.NotNil(t, row.ConsumedAt, "the approved-but-unpolled device grant must die with the disconnect")
}

type beforeUserAuthTxStore struct {
	store.Store
	before func()
	once   sync.Once
}

func (s *beforeUserAuthTxStore) RunInUserAuthTransaction(ctx context.Context, uid userid.UserID, fn func(store.Store) error) error {
	if s.before != nil {
		s.once.Do(s.before)
	}
	return s.Store.RunInUserAuthTransaction(ctx, uid, fn)
}

func TestDisconnectApp_ConcurrentWithDeviceTokenMintLeavesNoLiveToken(t *testing.T) {
	t.Parallel()

	t.Run("concurrent race leaves no live token and revokes any committed winner", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		st := hubtestutil.OpenTestStore(t)
		hubtestutil.CreateTestAdmin(t, st)
		admin, err := st.Users().GetByUsername(ctx, hubtestutil.TestAdminUsername)
		require.NoError(t, err)
		uid := userid.MustNew(admin.ID)
		userInfo := &auth.UserInfo{ID: uid, Username: admin.Username, IsAdmin: true}

		pepper := []byte("0123456789abcdef0123456789abcdef")
		tv, err := auth.NewTokenValidator(st, pepper)
		require.NoError(t, err)
		closer := &recordingBearerCloser{}
		lifecycle := auth.NewCredentialLifecycleEffects(nil, closer, nil)
		set := servicetest.NewSettingsManager(t, st, nil)
		deviceAuths := service.NewMemoryDeviceAuthorizationStore()

		userSvc := service.NewUserService(service.UserServiceDeps{
			Store:                st,
			Config:               testConfig(),
			Settings:             set,
			Lifecycle:            lifecycle,
			Mail:                 mail.NewStubSender(),
			Renderer:             mail.Renderer{},
			DeviceAuthorizations: deviceAuths,
		})
		mux := http.NewServeMux()
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		newOAuthServerHandler(service.OAuthServerDeps{
			SessionHasher:        hubtestutil.TestSecretHasher(),
			Store:                st,
			Validator:            tv,
			Lifecycle:            lifecycle,
			Settings:             set,
			DeviceAuthorizations: deviceAuths,
			HubURL:               func() string { return srv.URL },
		}).RegisterRoutes(mux)

		for range 20 {
			deviceCode := id.Generate()
			userCode := verifycode.Generate()
			now := time.Now().UTC()
			require.NoError(t, deviceAuths.Create(service.CreateDeviceAuthorizationParams{
				DeviceCodeHash: tv.HashSecret(deviceCode),
				UserCode:       userCode,
				ClientID:       oauthapp.ControlCLIClientID,
				DeviceName:     "racer",
				ExpiresAt:      now.Add(10 * time.Minute),
			}))
			approved, err := deviceAuths.ApproveByUserCode(service.ApproveDeviceAuthorizationParams{
				UserCode:       userCode,
				UserID:         uid,
				GrantedScopes:  "workspace:read",
				AuthGeneration: admin.AuthGeneration,
			}, now)
			require.NoError(t, err)
			require.True(t, approved)

			start := make(chan struct{})
			var (
				wg            sync.WaitGroup
				disconnectErr error
				tokenStatus   int
				mintedID      string
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, disconnectErr = userSvc.DisconnectApp(auth.WithUser(ctx, userInfo), &leapmuxv1.DisconnectAppRequest{
					ClientId: oauthapp.ControlCLIClientID,
				})
			}()
			go func() {
				defer wg.Done()
				<-start
				resp, err := srv.Client().PostForm(srv.URL+"/oauth/token", url.Values{
					"grant_type":  {service.GrantTypeDeviceCode},
					"client_id":   {oauthapp.ControlCLIClientID},
					"device_code": {deviceCode},
				})
				if !assert.NoError(t, err) {
					return
				}
				defer func() { _ = resp.Body.Close() }()
				tokenStatus = resp.StatusCode
				if tokenStatus == http.StatusOK {
					var body struct {
						TokenID string `json:"token_id"`
					}
					if assert.NoError(t, json.NewDecoder(resp.Body).Decode(&body)) {
						mintedID = body.TokenID
					}
				}
			}()
			close(start)
			wg.Wait()

			require.NoError(t, disconnectErr)
			live, err := st.OAuthClients().ListUserTokenRefs(ctx, oauthapp.ControlCLIClientID, uid)
			require.NoError(t, err)
			assert.Empty(t, live, "no live api_tokens row may survive DisconnectApp")
			if tokenStatus == http.StatusOK {
				require.NotEmpty(t, mintedID)
				row, err := st.APITokens().GetByID(ctx, mintedID)
				require.NoError(t, err)
				assert.NotNil(t, row.RevokedAt, "a token committed before DisconnectApp must be revoked")
				assert.True(t, closer.closed(mintedID), "DisconnectApp must pass the committed winner to BearerRevokedBatch")
			}
		}
	})

	t.Run("token committed just before RunInUserAuthTransaction is revoked and evicted", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		st := hubtestutil.OpenTestStore(t)
		hubtestutil.CreateTestAdmin(t, st)
		admin, err := st.Users().GetByUsername(ctx, hubtestutil.TestAdminUsername)
		require.NoError(t, err)
		uid := userid.MustNew(admin.ID)
		userInfo := &auth.UserInfo{ID: uid, Username: admin.Username, IsAdmin: true}

		closer := &recordingBearerCloser{}
		lifecycle := auth.NewCredentialLifecycleEffects(nil, closer, nil)
		set := servicetest.NewSettingsManager(t, st, nil)
		deviceAuths := service.NewMemoryDeviceAuthorizationStore()

		lateTokenID := id.Generate()
		wrapped := &beforeUserAuthTxStore{
			Store: st,
			before: func() {
				// Simulate a concurrent /oauth/token committing just before
				// DisconnectApp acquires the user-auth transaction lock.
				expires := time.Now().Add(time.Hour)
				require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
					Issuance:         store.APITokenIssuanceDeviceCode,
					ID:               lateTokenID,
					UserID:           uid,
					ClientID:         oauthapp.ControlCLIClientID,
					InstallationName: "just-before-tx",
					GrantedScopes:    "workspace:read",
					SecretHash:       []byte("late-hash"),
					ExpiresAt:        &expires,
				}))
			},
		}
		userSvc := service.NewUserService(service.UserServiceDeps{
			Store:                wrapped,
			Config:               testConfig(),
			Settings:             set,
			Lifecycle:            lifecycle,
			Mail:                 mail.NewStubSender(),
			Renderer:             mail.Renderer{},
			DeviceAuthorizations: deviceAuths,
		})

		resp, err := userSvc.DisconnectApp(auth.WithUser(ctx, userInfo), &leapmuxv1.DisconnectAppRequest{
			ClientId: oauthapp.ControlCLIClientID,
		})
		require.NoError(t, err)
		assert.EqualValues(t, 1, resp.GetRevokedCredentialCount())

		row, err := st.APITokens().GetByID(ctx, lateTokenID)
		require.NoError(t, err)
		assert.NotNil(t, row.RevokedAt)
		assert.True(t, closer.closed(lateTokenID), "BearerRevokedBatch must receive tokens committed just before the transaction")
	})
}

type failUserAuthTxStore struct {
	store.Store
	err error
}

func (s failUserAuthTxStore) RunInUserAuthTransaction(context.Context, userid.UserID, func(store.Store) error) error {
	return s.err
}

func TestDisconnectApp_AbortsDeviceInvalidationWhenTransactionFails(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	ctx := context.Background()
	uid := userid.MustNew(env.userID)
	now := time.Now().UTC()

	deviceCode := id.Generate()
	hash := hubtestutil.TestSecretHasher().Hash(deviceCode)
	require.NoError(t, env.deviceAuths.Create(service.CreateDeviceAuthorizationParams{
		DeviceCodeHash: hash,
		UserCode:       id.Generate(),
		ClientID:       oauthapp.ControlCLIClientID,
		ExpiresAt:      now.Add(10 * time.Minute),
	}))
	rowBefore, ok := env.deviceAuths.Get(hash)
	require.True(t, ok)
	approved, err := env.deviceAuths.ApproveByUserCode(service.ApproveDeviceAuthorizationParams{
		UserCode:      rowBefore.UserCode,
		UserID:        uid,
		GrantedScopes: "workspace:read",
	}, now)
	require.NoError(t, err)
	require.True(t, approved)

	userSvc := service.NewUserService(service.UserServiceDeps{
		Store:                failUserAuthTxStore{Store: env.store, err: errors.New("forced user auth tx failure")},
		Config:               testConfig(),
		Settings:             servicetest.NewSettingsManager(t, env.store, nil),
		Lifecycle:            auth.NewCredentialLifecycleEffects(nil, nil, nil),
		Mail:                 mail.NewStubSender(),
		Renderer:             mail.Renderer{},
		DeviceAuthorizations: env.deviceAuths,
	})

	_, err = userSvc.DisconnectApp(auth.WithUser(ctx, &auth.UserInfo{ID: uid, Username: "alice"}), &leapmuxv1.DisconnectAppRequest{
		ClientId: oauthapp.ControlCLIClientID,
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))

	row, ok := env.deviceAuths.Get(hash)
	require.True(t, ok)
	assert.Nil(t, row.ConsumedAt, "a rolled-back DisconnectApp must leave the approved device grant unconsumed")
	res, reserved := env.deviceAuths.ReserveConsume(hash, now)
	require.True(t, reserved, "AbortInvalidate must unblock ReserveConsume after a failed DisconnectApp")
	env.deviceAuths.ReleaseConsume(res)
}
