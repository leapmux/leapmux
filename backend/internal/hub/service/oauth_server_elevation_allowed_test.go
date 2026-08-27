package service_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// mintAppCredential creates a credential held by one app, so a test can vary
// that app's elevation_allowed and watch what the credential can do.
func mintAppCredential(t *testing.T, env *apiAuthEnv, clientID string) (tokenID, bearer string) {
	t.Helper()
	tokenID = id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(env.userID), ClientID: clientID,
		InstallationName: "laptop", GrantedScopes: authscope.NonAdminGrant().String(),
		SecretHash: env.validator.HashSecret(secret),
	}))
	return tokenID, auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
}

// An app is refused the step-up leg unless its owner allowed it.
//
// The step-up leg is what lets a credential make the account's most sensitive
// changes, and it is orthogonal to every permission: the window MULTIPLIES
// whatever the grant already allows, so no scope could express it and mean
// anything. It is therefore its own decision, and the default is no.
func TestElevationAllowed_RefusesAnAppWhoseOwnerDidNotAllowIt(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	// The column's default is FALSE, and this registration takes it.
	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Third-party app", RedirectURIs: "https://app.example.com/callback",
	})
	tokenID, bearer := mintAppCredential(t, env, clientID)

	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/oauth/step-up",
		strings.NewReader(url.Values{"installation_name": {"laptop"}}.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+bearer)
	refused, err := env.server.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = refused.Body.Close() }()

	assert.GreaterOrEqual(t, refused.StatusCode, 400,
		"an app whose owner did not allow the step-up leg must be refused")

	// The STORE refuses it too, whatever the endpoint answered. See
	// TestElevationAllowed_TheStoreRefusesIndependentlyOfTheEndpoint.
	now := time.Now().UTC()
	n, err := env.store.APITokens().Elevate(context.Background(), store.ElevateAPITokenParams{
		TokenID: tokenID, UserID: userid.MustNew(env.userID),
		ElevationProvenAt: now, ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	assert.Zero(t, n, "no window may be written for an app that may not elevate")
}

// Turning the flag OFF closes a live window on the next request.
//
// loadBearer re-reads elevation_allowed every time it validates a credential
// and ZEROES the window when the flag is off, rather than trusting what was
// true when the window opened. So an owner who revokes the right does not wait
// out the remaining two hours.
func TestElevationAllowed_TurningItOffClosesALiveWindow(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	clientID := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Allowed app", RedirectURIs: "https://app.example.com/callback",
		ElevationAllowed: true,
	})
	tokenID, bearer := mintAppCredential(t, env, clientID)

	// A LIVE window on the credential.
	now := time.Now().UTC()
	n, err := env.store.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
		TokenID: tokenID, UserID: userid.MustNew(env.userID),
		ElevationProvenAt: now, ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "the store must admit the elevation while the app is allowed")

	user, err := env.validator.ValidateBearer(ctx, bearer)
	require.NoError(t, err)
	require.True(t, user.Elevated(now), "the window is live before the flag changes")

	// The owner takes the right away.
	rows, err := env.store.OAuthClients().SetElevationAllowed(ctx, store.SetOAuthClientElevationAllowedParams{
		ClientID: clientID, ElevationAllowed: false,
		CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, rows)

	// The cached validation must not answer for the row that changed.
	//
	// Dropped BY HAND here, because this test drives the STORE statement
	// directly and nothing else is present to do it. In production the write
	// arrives through AppService.SetAppElevationAllowed, which drops the cached
	// UserInfo of every credential the app holds --
	// TestAppService_TurningElevationOffDropsTheCachedWindow drives that path
	// with nothing hand-invalidated, which is what proves "on the next request"
	// rather than "on the next cache miss".
	env.cache.InvalidateBearer(auth.NewBearerRef(auth.BearerKindAPI, tokenID))

	after, err := env.validator.ValidateBearer(ctx, bearer)
	require.NoError(t, err, "the credential itself still authenticates")
	assert.False(t, after.Elevated(now),
		"the window closes on the next request, not when it would have expired")
}

// The STORE refuses the elevation independently of the endpoint.
//
// Two surfaces write that window -- the step-up approval and the admin mint --
// and a guard that lived only in a handler would be one `if` away from being
// skipped by a third. The statement itself matches no row for an app that is
// not allowed, so the refusal holds whatever calls it.
func TestElevationAllowed_TheStoreRefusesIndependentlyOfTheEndpoint(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	ctx := context.Background()
	owner := userid.MustNew(env.userID)
	now := time.Now().UTC()

	refused := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Not allowed", RedirectURIs: "https://a.example.com/callback",
	})
	allowed := seedTestApp(t, env, store.CreateOAuthClientParams{
		ClientName: "Allowed", RedirectURIs: "https://b.example.com/callback",
		ElevationAllowed: true,
	})

	refusedToken, _ := mintAppCredential(t, env, refused)
	allowedToken, _ := mintAppCredential(t, env, allowed)

	n, err := env.store.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
		TokenID: refusedToken, UserID: owner,
		ElevationProvenAt: now, ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err, "the refusal is a matched-no-row, not an error")
	assert.Zero(t, n, "the statement must match no row for an app that may not elevate")

	// The same call against an ALLOWED app writes the window, so the zero above
	// is the flag rather than a statement that never matches anything.
	n, err = env.store.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
		TokenID: allowedToken, UserID: owner,
		ElevationProvenAt: now, ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)

	// A REVOKED app cannot elevate either, however its flag reads: the same
	// predicate tests both, so retiring an app closes this door with it.
	_, err = env.store.OAuthClients().Revoke(ctx, store.OAuthClientOwnershipParams{
		ClientID: allowed, CallerUserID: owner, CallerIsAdmin: true,
	})
	require.NoError(t, err)

	secondToken, _ := mintAppCredential(t, env, allowed)
	n, err = env.store.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
		TokenID: secondToken, UserID: owner,
		ElevationProvenAt: now, ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	assert.Zero(t, n, "a retired app may not elevate whatever its flag says")
}
