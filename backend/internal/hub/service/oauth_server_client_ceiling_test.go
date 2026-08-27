package service_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// The app's REGISTERED permission ceiling, read at validation.
//
// oauth_clients.scopes is what an app may ask for, and loadBearer narrows every
// stored grant to it at the moment the row is read. That is the same rule
// elevation_allowed already carries one column over, and it exists for the same
// reason: an owner who takes a permission off a registration means the app
// should no longer have it, and a ceiling applied only at the consent would
// make that edit a silent no-op for every credential already issued.
//
// AppService.applyCeilingChange is the other half -- the cache and the open
// channels, which no column change reaches by itself. It is tested in
// app_service_test.go, where the RPC lives.

// mintAppCredentialWithGrant mints a credential for one app with a stated
// grant, so a test can move the app's ceiling underneath it.
func mintAppCredentialWithGrant(t *testing.T, env *apiAuthEnv, clientID, granted string) string {
	t.Helper()
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.userID), ClientID: clientID,
		InstallationName: "laptop", GrantedScopes: granted,
		SecretHash: env.validator.HashSecret(secret),
	}))
	return auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
}

// TestClientCeiling_NarrowsAStoredGrantAtValidation is the rule itself.
//
// The credential is consented two permissions and the registration then loses
// one. Validation must answer with the intersection, at the next request, with
// no re-mint and no re-consent.
func TestClientCeiling_NarrowsAStoredGrantAtValidation(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Two-permission app",
		Scopes:     "workspace:read file:read worker:read",
	})
	bearer := mintAppCredentialWithGrant(t, env, clientID, "workspace:read file:read worker:read")

	before, err := env.validator.ValidateBearer(ctx, bearer)
	require.NoError(t, err)
	assert.True(t, before.Scopes.Allows(leapmuxv1.Scope_SCOPE_FILE_READ),
		"the consented grant holds while the registration does")

	// The owner takes file:read off the REGISTRATION. The credential's own
	// granted_scopes column is untouched.
	rows, err := env.store.OAuthClients().Update(ctx, store.UpdateOAuthClientParams{
		ClientID: clientID, ClientName: "Two-permission app",
		RedirectURIs: "https://app.example.com/callback",
		Scopes:       "workspace:read worker:read",
		GrantTypes:   "authorization_code refresh_token",
		CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	row, err := env.store.APITokens().GetByID(ctx, tokenIDOf(t, bearer))
	require.NoError(t, err)
	assert.Contains(t, row.GrantedScopes, "file:read",
		"the stored grant is untouched; the ceiling is what moved")

	after, err := env.validator.ValidateBearer(ctx, bearer)
	require.NoError(t, err, "the credential still authenticates; it just reaches less")
	assert.False(t, after.Scopes.Allows(leapmuxv1.Scope_SCOPE_FILE_READ),
		"a permission removed from the registration must be gone at the next validation")
	assert.True(t, after.Scopes.Allows(leapmuxv1.Scope_SCOPE_WORKSPACE_READ),
		"and the permissions the registration kept must survive")
}

// TestClientCeiling_NarrowsAndNeverWidens is the direction that matters.
//
// Putting a permission back on the registration must NOT hand it to a
// credential whose owner never consented to it: the stored grant is the other
// half of the intersection, and a ceiling that granted would make an app's own
// registration a way to widen what an account agreed to.
func TestClientCeiling_NarrowsAndNeverWidens(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Wide app",
		Scopes:     "workspace:read file:read terminal:read terminal:write worker:read",
	})
	// CONSENTED to one permission out of the app's wider ceiling.
	bearer := mintAppCredentialWithGrant(t, env, clientID, "workspace:read")

	user, err := env.validator.ValidateBearer(ctx, bearer)
	require.NoError(t, err)
	for _, scope := range []leapmuxv1.Scope{
		leapmuxv1.Scope_SCOPE_FILE_READ,
		leapmuxv1.Scope_SCOPE_TERMINAL_WRITE,
	} {
		token, _ := authscope.Token(scope)
		assert.Falsef(t, user.Scopes.Allows(scope),
			"the app's registration reaches %s but this account never consented to it", token)
	}
	assert.True(t, user.Scopes.Allows(leapmuxv1.Scope_SCOPE_WORKSPACE_READ))
}

// TestClientCeiling_AnUnreadableRegistrationRefusesTheCredential.
//
// A ceiling nobody can parse is not a ceiling, so the credential is refused
// rather than admitted at its stored grant. It is the same answer an unreadable
// GRANT already gets, and the failure it prevents is the one that looks safe:
// admitting on a value the hub could not read.
func TestClientCeiling_AnUnreadableRegistrationRefusesTheCredential(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Drifted app",
		Scopes:     "workspace:read",
	})
	bearer := mintAppCredentialWithGrant(t, env, clientID, "workspace:read")
	require.NotNil(t, mustValidate(t, env, bearer), "the credential authenticates while the ceiling reads")

	// A vocabulary that drifted: the registration names a permission this hub
	// no longer knows. Written through the store statement production uses, so
	// the row is one the hub could really hold.
	rows, err := env.store.OAuthClients().Update(ctx, store.UpdateOAuthClientParams{
		ClientID: clientID, ClientName: "Drifted app",
		RedirectURIs: "https://app.example.com/callback",
		Scopes:       "workspace:read invented:permission",
		GrantTypes:   "authorization_code refresh_token",
		CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	_, err = env.validator.ValidateBearer(ctx, bearer)
	require.Error(t, err, "a ceiling the hub cannot read must refuse rather than admit")
}

// mustValidate is the positive control the drift test above needs: without it,
// that test would pass for a credential that never authenticated at all.
func mustValidate(t *testing.T, env *apiAuthEnv, bearer string) *auth.UserInfo {
	t.Helper()
	user, err := env.validator.ValidateBearer(context.Background(), bearer)
	require.NoError(t, err)
	return user
}

// TestClientCeiling_TheRefreshLegMeasuresAgainstWhatTheCredentialReaches.
//
// The refresh leg reads the STORED consent, which the ceiling does not rewrite
// -- so after an owner narrows a registration the two differ, and the leg has
// to answer for the reachable set rather than the stored one:
//
//   - an ask for a permission the registration no longer allows is refused,
//     so the app learns at the refresh rather than at its next call, and
//   - the `scope` it reports names what the credential can actually do. RFC
//     6749 section 5.1 makes that field the grant, and reporting the stored
//     value would name a permission the very next request is refused.
func TestClientCeiling_TheRefreshLegMeasuresAgainstWhatTheCredentialReaches(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Refreshing app",
		Scopes:     "workspace:read file:read worker:read",
	})

	tokenID := id.Generate()
	refresh := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.userID), ClientID: clientID,
		InstallationName: "laptop",
		GrantedScopes:    "workspace:read file:read worker:read",
		SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      env.validator.HashSecret(refresh),
	}))

	// The owner takes file:read off the REGISTRATION. The credential's own
	// consent is untouched.
	rows, err := env.store.OAuthClients().Update(ctx, store.UpdateOAuthClientParams{
		ClientID: clientID, ClientName: "Refreshing app",
		RedirectURIs: "https://app.example.com/callback",
		Scopes:       "workspace:read worker:read",
		GrantTypes:   "authorization_code refresh_token",
		CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	refuse := func() (int, map[string]any) {
		return postTokenForm(t, env, url.Values{
			"grant_type":    {service.GrantTypeRefreshToken},
			"client_id":     {clientID},
			"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, refresh)},
			"scope":         {"workspace:read file:read"},
		})
	}
	status, body := refuse()
	assert.Equal(t, http.StatusBadRequest, status,
		"an ask for a permission the registration no longer allows must be refused")
	assert.Equal(t, "invalid_scope", body["error"])

	// An ask for NOTHING keeps the consent and reports the reachable set.
	status, body = postTokenForm(t, env, url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {clientID},
		"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, refresh)},
	})
	require.Equal(t, http.StatusOK, status, "the credential still refreshes; it just reaches less")
	reported, _ := body["scope"].(string)
	assert.NotContains(t, strings.Fields(reported), "file:read",
		"the response must name what the credential reaches, not what the column keeps")
	assert.Contains(t, strings.Fields(reported), "workspace:read")

	// The CONSENT survives in the column, so putting the permission back on the
	// registration restores it rather than requiring a fresh authorization.
	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.Contains(t, strings.Fields(row.GrantedScopes), "file:read",
		"the ceiling narrows what a credential reaches and never rewrites what its owner agreed to")
}

// TestClientCeiling_TheGraceRetryReportsTheReachableGrantToo.
//
// The retry path re-emits the pair a racing caller already minted, without
// rotating anything -- so it reads the row rather than a freshly computed
// grant, and it is the ONE response a client is most likely to act on twice.
// Reporting the stored column there would name a permission the app's next
// call is refused, on exactly the answer a retry lands in.
func TestClientCeiling_TheGraceRetryReportsTheReachableGrantToo(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Retrying app",
		Scopes:     "workspace:read file:read worker:read",
	})

	tokenID := id.Generate()
	prev := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.userID), ClientID: clientID,
		InstallationName: "laptop",
		GrantedScopes:    "workspace:read file:read worker:read",
		SecretHash:       env.validator.HashSecret(auth.MintAccessSecret()),
		RefreshHash:      env.validator.HashSecret(prev),
	}))

	refresh := func() (int, map[string]any) {
		return postTokenForm(t, env, url.Values{
			"grant_type":    {service.GrantTypeRefreshToken},
			"client_id":     {clientID},
			"refresh_token": {auth.FormatBearer(auth.BearerKindAPI, tokenID, prev)},
		})
	}
	status, _ := refresh()
	require.Equal(t, http.StatusOK, status, "the first exchange rotates")

	// The owner narrows the registration between the rotation and the retry.
	rows, err := env.store.OAuthClients().Update(ctx, store.UpdateOAuthClientParams{
		ClientID: clientID, ClientName: "Retrying app",
		RedirectURIs: "https://app.example.com/callback",
		Scopes:       "workspace:read worker:read",
		GrantTypes:   "authorization_code refresh_token",
		CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	// The rotated-out secret again, inside the grace window: the RETRY arm.
	status, body := refresh()
	require.Equal(t, http.StatusOK, status, "a replay inside the grace window re-emits the same pair")
	reported, _ := body["scope"].(string)
	assert.NotContains(t, strings.Fields(reported), "file:read",
		"the retry response must name what the credential reaches, not what the column keeps")
	assert.Contains(t, strings.Fields(reported), "workspace:read")
}
