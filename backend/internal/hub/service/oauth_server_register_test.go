package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// RFC 7591 dynamic registration and the same-origin icon it can lead to.
//
// Both are ANONYMOUS surfaces -- /oauth/register when the setting is on, and
// /oauth/apps/<id>/icon always -- so every refusal here is one an unauthenticated
// caller meets, and the shape of each refusal is the whole test.

// openRegistration turns the setting on for this hub.
func openRegistration(t *testing.T, env *apiAuthEnv, on bool) {
	t.Helper()
	require.NoError(t, settings.KeyOpenAppRegistration.Set(context.Background(), env.set, on))
}

// postRegistration posts one client-metadata document.
func postRegistration(t *testing.T, env *apiAuthEnv, doc map[string]any) *http.Response {
	t.Helper()
	body, err := json.Marshal(doc)
	require.NoError(t, err)
	resp, err := http.Post(env.server.URL+"/oauth/register", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// authorizationServerMetadata reads the RFC 8414 discovery document.
//
// It is what a conformant client library fetches BEFORE it holds anything, and
// it derives every endpoint from what the document says -- so a test that
// changes what the hub accepts must check the document beside it.
func authorizationServerMetadata(t *testing.T, env *apiAuthEnv) map[string]any {
	t.Helper()
	resp, err := http.Get(env.server.URL + "/.well-known/oauth-authorization-server")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return decodeJSONBody(t, resp)
}

// TestRegister_TheSettingIsTheWholeGate drives both poles of the setting in
// one test, and reads the metadata document beside each.
//
// The two must agree: a hub that refuses the endpoint while advertising
// `registration_endpoint` makes a conformant client library report the refusal
// as a hub failure, and a hub that serves it while hiding the endpoint is one
// no library ever finds.
func TestRegister_TheSettingIsTheWholeGate(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)

	t.Run("off: refused, and the metadata omits the endpoint", func(t *testing.T) {
		resp := postRegistration(t, env, map[string]any{
			"client_name":   "Anonymous app",
			"redirect_uris": []string{"https://anon.example.com/callback"},
		})
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		body := decodeJSONBody(t, resp)
		assert.Equal(t, "access_denied", body["error"])
		assert.Contains(t, body["error_description"], "administrator",
			"the refusal must name the remedy, which is a person rather than a retry")

		_, ok := authorizationServerMetadata(t, env)["registration_endpoint"]
		assert.False(t, ok, "the endpoint must be absent while the setting is off")
	})

	openRegistration(t, env, true)

	t.Run("on: registered, and the metadata names the endpoint", func(t *testing.T) {
		resp := postRegistration(t, env, map[string]any{
			"client_name":   "Anonymous app",
			"redirect_uris": []string{"https://anon.example.com/callback"},
			"scope":         "workspace:read",
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		body := decodeJSONBody(t, resp)

		clientID, _ := body["client_id"].(string)
		require.NotEmpty(t, clientID, "RFC 7591 section 3.2.1 makes the SERVER the issuer of client_id")
		assert.Empty(t, body["client_secret"], "an omitted token_endpoint_auth_method is a PUBLIC client")
		assert.Equal(t, "none", body["token_endpoint_auth_method"])
		assert.Equal(t, "Anonymous app", body["client_name"])
		assert.Equal(t, "workspace:read", body["scope"])
		// The RFC 7591 section 2 default, which is what an app that states no
		// grant means.
		assert.ElementsMatch(t,
			[]any{"authorization_code", "refresh_token"}, body["grant_types"])

		endpoint, _ := authorizationServerMetadata(t, env)["registration_endpoint"].(string)
		assert.True(t, strings.HasSuffix(endpoint, "/oauth/register"), "got %q", endpoint)

		// The ROW, which is what a consent screen reads. A dynamically
		// registered app is hub-wide, unverified, and never seeded with the
		// step-up flag.
		row, err := env.store.OAuthClients().Get(context.Background(), clientID)
		require.NoError(t, err)
		assert.True(t, row.IsHubWide(), "the request carries no account to scope it to")
		assert.False(t, row.IsVerified(), "nobody vouched for a self-registered app")
		assert.False(t, row.ElevationAllowed,
			"an app that may re-arm an elevation window makes the account's most sensitive changes")
		assert.Equal(t, store.OAuthClientSourceDynamic, row.RegistrationSource)
	})
}

// TestRegister_ConfidentialClientGetsItsSecretOnce pins the one crossing.
//
// The store keeps a HASH, so a registrant that loses the value has no second
// read and the hub has no rotation verb pretending otherwise.
func TestRegister_ConfidentialClientGetsItsSecretOnce(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	openRegistration(t, env, true)

	resp := postRegistration(t, env, map[string]any{
		"client_name":                "Server-side app",
		"redirect_uris":              []string{"https://confidential.example.com/callback"},
		"token_endpoint_auth_method": "client_secret_basic",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	body := decodeJSONBody(t, resp)

	secret, _ := body["client_secret"].(string)
	require.NotEmpty(t, secret)
	assert.Equal(t, "client_secret_basic", body["token_endpoint_auth_method"])

	clientID, _ := body["client_id"].(string)
	row, err := env.store.OAuthClients().Get(context.Background(), clientID)
	require.NoError(t, err)
	require.NotEmpty(t, row.SecretHash, "a confidential client stores a hash")
	assert.NotContains(t, string(row.SecretHash), secret, "the plaintext must not reach the row")
	assert.Equal(t, env.validator.HashSecret(secret), row.SecretHash,
		"the stored hash must be the one the token endpoint compares")
}

// TestRegister_RefusesInvalidMetadata walks the closed set of refusals, each
// with the RFC 7591 section 3.2.2 error code that names WHICH field is wrong.
//
// One code for everything would make a registrant guess, and the endpoint is
// the first thing an integrator meets.
func TestRegister_RefusesInvalidMetadata(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	openRegistration(t, env, true)

	cases := map[string]struct {
		doc  map[string]any
		code string
		says string
	}{
		"no name": {
			doc:  map[string]any{"redirect_uris": []string{"https://a.example.com/cb"}},
			code: "invalid_client_metadata", says: "client_name",
		},
		"a redirect with a fragment": {
			doc: map[string]any{
				"client_name":   "Fragmented",
				"redirect_uris": []string{"https://a.example.com/cb#done"},
			},
			code: "invalid_redirect_uri", says: "fragment",
		},
		"the code grant with no redirect": {
			doc: map[string]any{
				"client_name": "Nowhere to go",
				"grant_types": []string{"authorization_code"},
			},
			code: "invalid_redirect_uri", says: "redirect",
		},
		"an unsupported grant": {
			doc: map[string]any{
				"client_name":   "Implicit",
				"redirect_uris": []string{"https://a.example.com/cb"},
				"grant_types":   []string{"implicit"},
			},
			code: "invalid_client_metadata", says: "implicit",
		},
		"an unknown scope token": {
			doc: map[string]any{
				"client_name":   "Inventive",
				"redirect_uris": []string{"https://a.example.com/cb"},
				"scope":         "workspace:read also:give:them:everything",
			},
			code: "invalid_client_metadata", says: "also:give:them:everything",
		},
		"an unknown auth method": {
			doc: map[string]any{
				"client_name":                "Creative",
				"redirect_uris":              []string{"https://a.example.com/cb"},
				"token_endpoint_auth_method": "private_key_jwt",
			},
			code: "invalid_client_metadata", says: "token_endpoint_auth_method",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resp := postRegistration(t, env, tc.doc)
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			body := decodeJSONBody(t, resp)
			assert.Equal(t, tc.code, body["error"])
			description, _ := body["error_description"].(string)
			assert.Contains(t, description, tc.says)
		})
	}
}

// TestRegister_RefusesEverythingButPOST, because a GET that registered would
// let a link register an app.
func TestRegister_RefusesEverythingButPOST(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	openRegistration(t, env, true)

	resp, err := http.Get(env.server.URL + "/oauth/register")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// TestRegister_RefusesABodyPastTheCap.
//
// The endpoint is anonymous when the setting is on, so without a cap an
// unauthenticated caller makes the hub buffer whatever it sends. The refusal
// comes from the READER rather than from a field rule, so the body is one
// oversized document rather than one oversized field: a per-field limit
// reached only after the whole document was already buffered.
func TestRegister_RefusesABodyPastTheCap(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	openRegistration(t, env, true)

	// Past 16 KiB, which is the cap handleRegister hands the reader.
	resp := postRegistration(t, env, map[string]any{
		"client_name":   "Fine",
		"redirect_uris": []string{"https://a.example.com/cb"},
		"client_uri":    "https://" + strings.Repeat("x", 32<<10) + ".example.com",
	})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body := decodeJSONBody(t, resp)
	assert.Equal(t, "invalid_client_metadata", body["error"])
	assert.Contains(t, body["error_description"], "not a JSON object",
		"the refusal must come from the capped reader, not from the client_uri length rule")
}

// --- The icon endpoint ---

// seedIconApp writes one app with an icon, so the icon endpoint's three
// refusals can each be driven by moving one field.
func seedIconApp(t *testing.T, env *apiAuthEnv, mutate func(*store.CreateOAuthClientParams)) string {
	t.Helper()
	ctx := context.Background()
	params := store.CreateOAuthClientParams{
		ClientID:           id.Generate(),
		ClientName:         "Icon app",
		RedirectURIs:       "https://icon.example.com/callback",
		Scopes:             "workspace:read",
		GrantTypes:         "authorization_code refresh_token",
		RegistrationSource: store.OAuthClientSourceAdmin,
	}
	if mutate != nil {
		mutate(&params)
	}
	require.NoError(t, env.store.OAuthClients().Create(ctx, params))
	_, err := env.store.OAuthClients().SetIcon(ctx, store.SetOAuthClientIconParams{
		ClientID: params.ClientID, IconBlob: []byte("\x89PNG\r\n\x1a\nfake"), IconMediaType: "image/png",
		CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
	})
	require.NoError(t, err)
	return params.ClientID
}

// vouch records an administrator's verification directly on the row.
func vouch(t *testing.T, env *apiAuthEnv, clientID string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := env.store.OAuthClients().SetVerified(context.Background(), store.SetOAuthClientVerifiedParams{
		ClientID: clientID, VerifiedAt: &now, VerifiedBy: env.userID,
	})
	require.NoError(t, err)
}

// TestAppIcon_ServesAVerifiedAppAndNothingElse is the whole endpoint: one
// success and the four ways it answers 404.
//
// ONE answer for every refusal, and that is the point -- a 403 for "not
// verified" and a 404 for "no such app" would let any anonymous caller
// enumerate the registrations on the hub.
func TestAppIcon_ServesAVerifiedAppAndNothingElse(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	get := func(path string) *http.Response {
		t.Helper()
		resp, err := http.Get(env.server.URL + path)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	verified := seedIconApp(t, env, nil)
	vouch(t, env, verified)

	t.Run("a verified app serves its bytes", func(t *testing.T) {
		resp := get("/oauth/apps/" + verified + "/icon")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "image/png", resp.Header.Get("Content-Type"))
		// The headers that contain a stored blob somebody else chose: no
		// sniffing, and a policy that loads nothing at all.
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
		assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "default-src 'none'")
		assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "sandbox")
	})

	t.Run("an UNVERIFIED app is a 404", func(t *testing.T) {
		unverified := seedIconApp(t, env, nil)
		assert.Equal(t, http.StatusNotFound, get("/oauth/apps/"+unverified+"/icon").StatusCode,
			"the consent page shows a monogram for one, so serving its bytes would gain a registrant an icon it never displays")
	})

	t.Run("a REVOKED app is a 404", func(t *testing.T) {
		revoked := seedIconApp(t, env, nil)
		vouch(t, env, revoked)
		_, err := env.store.OAuthClients().Revoke(context.Background(), store.OAuthClientOwnershipParams{
			ClientID: revoked, CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
		})
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, get("/oauth/apps/"+revoked+"/icon").StatusCode)
	})

	t.Run("an unknown client id is a 404", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, get("/oauth/apps/no-such-app/icon").StatusCode)
	})

	t.Run("a built-in app with no icon is a 404", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound,
			get("/oauth/apps/"+oauthapp.ControlCLIClientID+"/icon").StatusCode)
	})

	t.Run("an asset that is not the icon is a 404", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, get("/oauth/apps/"+verified+"/secret").StatusCode)
		assert.Equal(t, http.StatusNotFound, get("/oauth/apps/"+verified).StatusCode)
	})
}

// TestAppIcon_RefusesAStoredMediaTypeOutsideTheSet.
//
// The STORED value is checked rather than trusted, because a Content-Type the
// registrant chose is a sniffing surface -- and image/svg+xml in particular is
// a script-execution one. The intake refuses it too, so this drives a row
// written past that check: a restored backup, or a hand-edited row.
func TestAppIcon_RefusesAStoredMediaTypeOutsideTheSet(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	clientID := seedIconApp(t, env, nil)
	vouch(t, env, clientID)
	_, err := env.store.OAuthClients().SetIcon(context.Background(), store.SetOAuthClientIconParams{
		ClientID: clientID, IconBlob: []byte("<svg/>"), IconMediaType: "image/svg+xml",
		CallerUserID: userid.MustNew(env.userID), CallerIsAdmin: true,
	})
	require.NoError(t, err)

	resp, err := http.Get(env.server.URL + "/oauth/apps/" + clientID + "/icon")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.NotContains(t, service.AllowedIconMediaTypes(), "image/svg+xml",
		"an SVG is a document that can carry script; it must stay off the set")
}

// TestRegister_RefusesAnAdminCeiling pins the one ceiling an ANONYMOUS
// registrant cannot state. Without the refusal, an open-registration hub let a
// caller register an app carrying admin:users, the administrator's consent
// screen then offered the admin bullets, and one elevated click handed an
// anonymous registrant's app hub-user administration -- exactly the outcome
// RegisterApp's refusal to a non-administrator exists to prevent.
func TestRegister_RefusesAnAdminCeiling(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	openRegistration(t, env, true)

	resp := postRegistration(t, env, map[string]any{
		"client_name":   "LeapMux Admin Tools",
		"redirect_uris": []string{"https://anon.example.com/callback"},
		"scope":         "workspace:read admin:users",
	})
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body := decodeJSONBody(t, resp)
	assert.Equal(t, "invalid_client_metadata", body["error"])
	assert.Contains(t, body["error_description"], "admin:users",
		"the refusal states the scope that caused it")
}

// TestRegister_EchoesTheRequestedAuthMethod pins the RFC 7591 section 3.2.1
// echo: a registrant that asked for client_secret_post is told it registered
// client_secret_post, because a hardcoded basic answer made a conformant
// library reconfigure itself or abort the registration.
func TestRegister_EchoesTheRequestedAuthMethod(t *testing.T) {
	t.Parallel()

	env := setupAPIAuth(t)
	openRegistration(t, env, true)

	resp := postRegistration(t, env, map[string]any{
		"client_name":                "Server app",
		"redirect_uris":              []string{"https://server.example.com/callback"},
		"token_endpoint_auth_method": "client_secret_post",
	})
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	body := decodeJSONBody(t, resp)
	assert.Equal(t, "client_secret_post", body["token_endpoint_auth_method"])
	assert.NotEmpty(t, body["client_secret"], "the requested method is a confidential one")
}
