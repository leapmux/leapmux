package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/pkce"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// isolateCLIEnv gives one test its own credential store AND cuts it off from the
// worker-spawned environment.
//
// The config dir alone is not isolation. `NewClientFromEnv` prefers
// LEAPMUX_CONTROL_SOCK over the stored credential, because a CLI invoked inside an
// agent must reach its own worker. A developer who runs this suite from inside a
// LeapMux agent inherits that socket, so the client under test talks to a real worker
// instead of the test's httptest server: the verb then prints the worker's answer, and
// the assertions read empty fields and absent keys rather than a wrong value. CI has
// none of these set, so the failure appears only on the machine the product itself is
// used on.
//
// The other inherited variables steer the command the same way one step down.
// LEAPMUX_HUB is the fallback when no --hub is passed, and the tab, tile, workspace and
// worker ids are what "self" and every omitted target resolve to. The helper therefore
// clears every LEAPMUX_ variable, so a variable that the CLI learns to read later is
// cleared too.
func isolateCLIEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if key, _, _ := strings.Cut(kv, "="); strings.HasPrefix(key, "LEAPMUX_") {
			t.Setenv(key, "")
		}
	}
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
}

// isolateCLIEnv is what keeps every verb in this package pointed at the test's own
// httptest server. Each variable it clears steers client construction somewhere else,
// so dropping one silently sends a test to a real worker or a real hub.
func TestIsolateCLIEnv_CutsTheWorkerSpawnedEnvironment(t *testing.T) {
	// The environment a LeapMux-spawned agent inherits, which is where this suite runs
	// when a developer uses the product to work on it.
	inherited := []string{
		"LEAPMUX_CONTROL_SOCK", "LEAPMUX_CONTROL_TOKEN", "LEAPMUX_HUB",
		"LEAPMUX_CONTROL_TAB_ID", "LEAPMUX_CONTROL_WORKER_ID", "LEAPMUX_CONTROL_A_VARIABLE_OF_A_LATER_RELEASE",
	}
	for _, key := range inherited {
		t.Setenv(key, "inherited")
	}

	t.Run("isolated", func(t *testing.T) {
		isolateCLIEnv(t)
		for _, key := range inherited {
			assert.Emptyf(t, os.Getenv(key), "%s must not reach a test", key)
		}
		assert.NotEmpty(t, os.Getenv("LEAPMUX_CONTROL_CONFIG_DIR"), "the credential store is still isolated")

		// The point of clearing the socket: NewClientFromEnv does not capture
		// an inherited worker socket, and NewClientOrAnonymous returns a direct
		// HubClient for the requested hub.
		c, err := control.NewClientOrAnonymous("https://hub.example")
		require.NoError(t, err)
		assert.Equal(t, "https://hub.example", c.HubURL)
		_, envErr := control.NewClientFromEnv("")
		require.Error(t, envErr, "an inherited worker socket must not capture NewClientFromEnv")
	})
}

// TestPKCEChallenge_DerivesFromVerifier pins the PKCE challenge as
// SHA-256 of the verifier, base64url-encoded without padding (RFC
// 7636 §4.2). Without this, a regression in the encoder would silently
// break the OAuth flow against a hub that strict-checks the challenge.
func TestPKCEChallenge_DerivesFromVerifier(t *testing.T) {
	verifier := "test-verifier-with-some-entropy-padding-1234567890"
	got := pkce.S256(verifier)

	sum := sha256.Sum256([]byte(verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	assert.Equal(t, want, got)

	// PKCE challenges are URL-safe and unpadded — verify directly.
	assert.NotContains(t, got, "=", "RawURLEncoding produces unpadded output")
	assert.NotContains(t, got, "+", "RawURLEncoding swaps + for -")
	assert.NotContains(t, got, "/", "RawURLEncoding swaps / for _")
}

// TestPKCEChallenge_DifferentVerifiersDiffer is a sanity check
// against a constant-output regression in the helper.
func TestPKCEChallenge_DifferentVerifiersDiffer(t *testing.T) {
	a := pkce.S256("verifier-a")
	b := pkce.S256("verifier-b")
	assert.NotEqual(t, a, b)
}

// TestPersistTokenResponse_WritesCredentials covers the happy-path of
// the token-exchange persistence step: the CLI decodes a valid hub response
// into a CredentialFile on disk under the test's isolated config dir.
func TestPersistTokenResponse_WritesCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	body := strings.NewReader(`{
		"access_token": "lmx_a_at_xyz",
		"refresh_token": "lmx_a_rt_xyz",
		"expires_in": 3600,
		"refresh_expires_in": 7776000,
		"token_id": "tok-1",
		"scope": "admin:read",
		"user_id": "user-1",
		"username": "alice"
	}`)
	result, err := persistTokenResponse("https://hub.example", body, oauthapp.ControlCLIClientID, "admin:read")
	require.NoError(t, err)
	assert.Equal(t, &loginResult{HubURL: "https://hub.example", Username: "alice", UserID: "user-1", Scope: "admin:read"}, result,
		"a login that got every permission it asked for carries no warning")

	loaded, err := control.LoadCredentials("https://hub.example")
	require.NoError(t, err)
	assert.Equal(t, "https://hub.example", loaded.HubURL)
	assert.Equal(t, "lmx_a_at_xyz", loaded.AccessToken)
	assert.Equal(t, "lmx_a_rt_xyz", loaded.RefreshToken)
	assert.Equal(t, "user-1", loaded.UserID)
	assert.Equal(t, "alice", loaded.Username)
	assert.Equal(t, "tok-1", loaded.TokenID)
	assert.Equal(t, "admin:read", loaded.Scope)
	// expires_at = now + expires_in; allow 1m skew for slow CI.
	assert.WithinDuration(t, time.Now().Add(time.Hour), loaded.ExpiresAt, time.Minute)
	assert.WithinDuration(t, time.Now().Add(90*24*time.Hour), loaded.RefreshExpiresAt, time.Minute)
}

// TestPersistTokenResponse_WarnsWhenAScopeWasNotGranted pins the device-code
// case the CLI cannot control: the person at the browser decides, and they may
// hold an account that cannot grant part of the ask -- so a login that comes
// back narrower must SAY so, rather than let the first call that needs the
// missing permission fail with an error that specifies nothing the user did.
func TestPersistTokenResponse_WarnsWhenAScopeWasNotGranted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	body := strings.NewReader(`{
		"access_token": "lmx_a_at_xyz",
		"refresh_token": "lmx_a_rt_xyz",
		"expires_in": 3600,
		"scope": "workspace:read",
		"user_id": "user-1",
		"username": "alice"
	}`)
	result, err := persistTokenResponse("https://hub.example", body, oauthapp.ControlCLIClientID, "admin:read")
	require.NoError(t, err)

	assert.Equal(t, "workspace:read", result.Scope)
	assert.Contains(t, result.Warning, "admin:read",
		"the warning must NAME the permission that was refused")

	loaded, err := control.LoadCredentials("https://hub.example")
	require.NoError(t, err)
	assert.Equal(t, "workspace:read", loaded.Scope,
		"the file must record what was GRANTED, not what was asked")
}

// TestRunAuthLogin_WarnsWhenTheRevokeFails pins safe post-login credential
// rotation: once the new login succeeds and is saved, a hub that refuses to
// revoke the previous credential does not fail the login, but warns with the
// Preferences path where the user can finish revoking the old grant.
func TestRunAuthLogin_WarnsWhenTheRevokeFails(t *testing.T) {
	isolateCLIEnv(t)

	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/revoke":
			w.WriteHeader(http.StatusInternalServerError)
		case "/oauth/device-authorization":
			_, _ = w.Write([]byte(`{"device_code":"dc-1","user_code":"ABCD-1234","verification_uri":"https://hub.example/activate","expires_in":60,"interval":0}`))
		case "/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"lmx_new_tok","refresh_token":"lmx_new_rt","expires_in":3600,"token_id":"tok-new","scope":"workspace:read","user_id":"user-new","username":"new-user"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	liveCredentialFor(t, srv.URL)

	o := runControl(t, "auth", "login", "--hub", srv.URL, "--device-code")
	data := o.Data(t)
	assert.Equal(t, "new-user", data["username"])
	assert.Contains(t, data["warning"], "could not revoke",
		"a revoke that did not happen must say so")
	assert.Contains(t, o.Stderr, "could not revoke",
		"a revoke that did not happen must warn on stderr")
	assert.Contains(t, o.Stderr, "Preferences › Apps › Connected apps",
		"the warning must state the group that holds the row, not the one a reorg removed")
	loaded, err := control.LoadCredentials(srv.URL)
	require.NoError(t, err, "the new credential must be saved even when revoking the old one fails")
	assert.Equal(t, "lmx_new_tok", loaded.AccessToken)
}

// TestRunAuthLogin_CleansUpAnUnreadableCredentialFile pins the corrupt-file
// half of safe credential rotation: a corrupt file is replaced once the new
// login succeeds, and the warning states that its old grant could not be
// revoked.
func TestRunAuthLogin_CleansUpAnUnreadableCredentialFile(t *testing.T) {
	isolateCLIEnv(t)

	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/device-authorization":
			_, _ = w.Write([]byte(`{"device_code":"dc-2","user_code":"EFGH-5678","verification_uri":"https://hub.example/activate","expires_in":60,"interval":0}`))
		case "/oauth/token":
			_, _ = w.Write([]byte(`{"access_token":"lmx_fresh_tok","refresh_token":"lmx_fresh_rt","expires_in":3600,"token_id":"tok-fresh","scope":"workspace:read","user_id":"user-fresh","username":"fresh-user"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	path, err := control.CredentialsPath(srv.URL)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("{not credential json"), 0o600))

	o := runControl(t, "auth", "login", "--hub", srv.URL, "--device-code")
	data := o.Data(t)
	assert.Equal(t, "fresh-user", data["username"])
	assert.Contains(t, o.Stderr, "did not parse",
		"an unreadable file must be reported, not silently overwritten")
	assert.Contains(t, o.Stderr, "cannot revoke",
		"the warning must state what the CLI could not do about the grant")
	loaded, err := control.LoadCredentials(srv.URL)
	require.NoError(t, err, "the new credential must replace the corrupt file")
	assert.Equal(t, "lmx_fresh_tok", loaded.AccessToken)
}

// TestPersistTokenResponse_RejectsMalformedJSON pins the failure path:
// a hub returning HTML or partial JSON should produce an error envelope
// rather than crash, and the CLI should write no credential file.
func TestPersistTokenResponse_RejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	result, err := persistTokenResponse("https://hub.example", strings.NewReader(`{not json`), oauthapp.ControlCLIClientID, "")
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Equal(t, "internal", failureOf(t, err).Error["code"])

	// No credential file should have been written on the failure path.
	_, err = control.LoadCredentials("https://hub.example")
	assert.ErrorIs(t, err, control.ErrNotLoggedIn)
}

// TestRunAuthLogout_RevokesAndRemovesCreds exercises the full logout
// path: with credentials on disk, `auth logout` posts to the hub's
// /oauth/revoke endpoint with the bearer in both the form body and
// the Authorization header, then removes the local credential file.
func TestRunAuthLogout_RevokesAndRemovesCreds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	revoked := false
	gotAuth := ""
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/revoke" {
			revoked = true
			gotAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL:      srv.URL,
		AccessToken: "lmx_a_at_logout",
		Username:    "alice",
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	out := runControl(t, "auth", "logout", "--hub", srv.URL).ok(t)

	assert.True(t, revoked, "logout must hit /oauth/revoke")
	assert.Equal(t, "Bearer lmx_a_at_logout", gotAuth)

	// Credentials must be gone from disk.
	_, err := control.LoadCredentials(srv.URL)
	assert.ErrorIs(t, err, control.ErrNotLoggedIn)

	var env struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, srv.URL, env.Data["hub_url"])
}

// TestRunAuthLogout_WarnsWhereToFinishTheJob pins the REFUSAL warning's
// destination. The warning once named "Command-line credentials", a
// Preferences row that no longer exists -- so the one user who needed it was
// sent hunting for a panel the rename had already taken away. The row it
// names must be the one the Preferences dialog actually draws.
func TestRunAuthLogout_WarnsWhereToFinishTheJob(t *testing.T) {
	isolateCLIEnv(t)
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/oauth/revoke", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL:      srv.URL,
		AccessToken: "lmx_a_at_live",
		Username:    "alice",
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	out := runControl(t, "auth", "logout", "--hub", srv.URL).ok(t)

	var env struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Contains(t, env.Data["warning"], "could not revoke",
		"a logout whose revoke failed must say so")
	assert.Contains(t, env.Data["warning"], "Preferences",
		"the warning must state where the operator can finish the job")
	assert.Contains(t, env.Data["warning"], "Connected apps",
		"and it must name the Preferences row that exists, not one a rename removed")

	// The local file goes either way: logout stays locally idempotent.
	_, err := control.LoadCredentials(srv.URL)
	assert.ErrorIs(t, err, control.ErrNotLoggedIn)
}

// TestRunAuthLogout_SucceedsWhenTheRowIsAlreadyGone is the credential that
// nobody used for months.
//
// The hub hard-deletes an api_tokens row once both of its deadlines close,
// and it answers a bearer whose row it cannot find with 401. Reporting that
// as a failed revoke told the user to finish the job under
// Preferences › Apps › Connected apps -- where the row is already gone, so
// there is nothing to act on and the warning can only worry them.
func TestRunAuthLogout_SucceedsWhenTheRowIsAlreadyGone(t *testing.T) {
	for name, status := range map[string]int{
		"the hub hard-deleted the row": http.StatusUnauthorized,
		"a proxy answers not found":    http.StatusNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			isolateCLIEnv(t)
			srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/oauth/revoke", r.URL.Path)
				w.WriteHeader(status)
			}))
			t.Cleanup(srv.Close)

			require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
				HubURL:      srv.URL,
				AccessToken: "lmx_a_at_stale",
				ExpiresAt:   time.Now().Add(-time.Hour),
			}))

			out := runControl(t, "auth", "logout", "--hub", srv.URL).ok(t)

			var env struct {
				Data map[string]string `json:"data"`
			}
			require.NoError(t, json.Unmarshal(out, &env))
			assert.NotContains(t, env.Data, "warning",
				"a row that is already gone is a revoke that already happened")

			_, err := control.LoadCredentials(srv.URL)
			assert.ErrorIs(t, err, control.ErrNotLoggedIn)
		})
	}
}

// TestRunAuthLogout_WarnsWhenTheHubRefusesTheRevoke keeps the other
// polarity: a refusal the operator CAN act on must still be reported, so the
// credential does not stay live behind a clean result.
func TestRunAuthLogout_WarnsWhenTheHubRefusesTheRevoke(t *testing.T) {
	isolateCLIEnv(t)
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL: srv.URL, AccessToken: "lmx_a_at_live", ExpiresAt: time.Now().Add(time.Hour),
	}))

	out := runControl(t, "auth", "logout", "--hub", srv.URL).ok(t)
	assert.Contains(t, string(out), "could not revoke")
	assert.Contains(t, string(out), "Preferences")
}

// TestRunAuthLogout_ToleratesMissingCreds covers the safe-to-rerun
// case: no credential file means there's nothing to revoke locally,
// but the command should still exit cleanly with a JSON envelope so
// scripts can use it without first checking.
func TestRunAuthLogout_ToleratesMissingCreds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	// No revoke endpoint is called when no credential file exists, so an
	// unreachable hub URL still succeeds without a warning.
	out := runControl(t, "auth", "logout", "--hub", "http://127.0.0.1:1").ok(t)
	var env struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "http://127.0.0.1:1", env.Data["hub_url"])
}

// TestRunAuthLogout_RequiresHub guards the early-validation path: no hub
// selected means no server-side revoke is even possible, so the CLI refuses
// instead of silently doing nothing, and states how to select one.
func TestRunAuthLogout_RequiresHub(t *testing.T) {
	isolateCLIEnv(t)
	f := runControl(t, "auth", "logout").requireFailure(t, cli.CodeNotLoggedIn)
	assert.Equal(t, "no hub selected", f.Message)
	assert.Contains(t, strings.Join(f.Hints, "\n"), "--hub")
}

// TestRunAuthStatus_ReportsExpiry exercises the user-facing health
// check: with valid credentials, the envelope carries username,
// user_id, expires_at, and a derived `expired` boolean so scripts
// don't have to reparse the timestamp.
func TestRunAuthStatus_ReportsExpiry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	require.NoError(t, control.SaveCredentials("https://hub.example", control.CredentialFile{
		HubURL:    "https://hub.example",
		Username:  "alice",
		UserID:    "u1",
		ExpiresAt: time.Now().Add(time.Hour),
	}))

	out := runControl(t, "auth", "status", "--hub", "https://hub.example").ok(t)
	data := envelopeData(t, out)
	assert.Equal(t, "alice", data["username"])
	assert.Equal(t, "u1", data["user_id"])
	assert.Equal(t, false, data["expired"])
}

// TestRunAuthStatus_ReportsExpired covers the expired-credentials
// case so scripts can detect "log in again" without comparing
// timestamps.
func TestRunAuthStatus_ReportsExpired(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	require.NoError(t, control.SaveCredentials("https://hub.example", control.CredentialFile{
		HubURL:    "https://hub.example",
		Username:  "bob",
		ExpiresAt: time.Now().Add(-time.Hour),
	}))

	out := runControl(t, "auth", "status", "--hub", "https://hub.example").ok(t)
	data := envelopeData(t, out)
	assert.Equal(t, true, data["expired"])
}

func TestRunAuthStatus_ZeroExpiresAtReportsNotExpired(t *testing.T) {
	isolateCLIEnv(t)

	require.NoError(t, control.SaveCredentials("https://hub.example", control.CredentialFile{
		HubURL:   "https://hub.example",
		Username: "carol",
		UserID:   "u-zero",
	}))

	out := runControl(t, "auth", "status", "--hub", "https://hub.example").ok(t)
	data := envelopeData(t, out)
	assert.Equal(t, false, data["expired"], "a zero ExpiresAt must not report as expired")
}

// TestRunAuthStatus_NotLoggedInWhenMissing covers the negative
// branch: status against a hub the user never logged into surfaces
// `not_logged_in` rather than crashing or silently succeeding.
func TestRunAuthStatus_NotLoggedInWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	f := runControl(t, "auth", "status", "--hub", "https://never-logged-in.example").requireFailure(t, cli.CodeNotLoggedIn)
	require.NotEmpty(t, f.Hints)
	assert.Contains(t, f.Hints[0], "auth login", "the hint states the command that signs in")
}

// TestRunAuthList_PrintsAllConfiguredHubs is the multi-hub case:
// `auth list` enumerates every credential file under the config
// directory so users can audit their CLI footprint at a glance.
func TestRunAuthList_PrintsAllConfiguredHubs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	require.NoError(t, control.SaveCredentials("https://a.example", control.CredentialFile{HubURL: "https://a.example", Username: "alice", ExpiresAt: time.Now().Add(time.Hour)}))
	require.NoError(t, control.SaveCredentials("https://b.example", control.CredentialFile{HubURL: "https://b.example", Username: "bob", ExpiresAt: time.Now().Add(time.Hour)}))

	rows := credentialRows(t, runControl(t, "auth", "list").ok(t))
	require.Len(t, rows, 2)

	// Order is filesystem-dependent; index by hub_url to stay stable.
	byHub := map[string]map[string]any{}
	for _, e := range rows {
		byHub[e["hub_url"].(string)] = e
		assert.Equal(t, false, e["default"], "with two credentials and no `auth use`, neither is the default")
	}
	assert.Equal(t, "alice", byHub["https://a.example"]["username"])
	assert.Equal(t, "bob", byHub["https://b.example"]["username"])

	t.Run("matches non-canonical default hub from context", func(t *testing.T) {
		isolateCLIEnv(t)
		require.NoError(t, control.SaveCredentials("https://hub.example", control.CredentialFile{
			HubURL: "https://hub.example", Username: "alice", ExpiresAt: time.Now().Add(time.Hour),
		}))
		require.NoError(t, control.SaveCredentials("https://other.example", control.CredentialFile{
			HubURL: "https://other.example", Username: "bob", ExpiresAt: time.Now().Add(time.Hour),
		}))
		require.NoError(t, control.SaveContext(control.ContextConfig{Hub: "HTTPS://HUB.EXAMPLE/"}))

		rows := credentialRows(t, runControl(t, "auth", "list").ok(t))
		require.Len(t, rows, 2)
		byHub := map[string]map[string]any{}
		for _, r := range rows {
			byHub[r["hub_url"].(string)] = r
		}
		assert.Equal(t, true, byHub["https://hub.example"]["default"])
		assert.Equal(t, false, byHub["https://other.example"]["default"])
	})
}

// TestRunAuthList_ToleratesMissingConfigDir covers the first-run
// case where nothing created the config directory yet. The
// command should print an empty list, not error out — scripts using
// it as a presence check shouldn't have to set up the directory
// themselves.
func TestRunAuthList_ToleratesMissingConfigDir(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir()+"/never-created")

	out := runControl(t, "auth", "list").ok(t)
	assert.JSONEq(t, `{"data":{"credentials":[]}}`, string(out),
		"an empty list prints as an empty array, not as null")
}

// credentialRows decodes the credentials of an `auth list` or an
// `auth credentials` envelope.
func credentialRows(t *testing.T, out []byte) []map[string]any {
	t.Helper()
	var env struct {
		Data struct {
			Credentials []map[string]any `json:"credentials"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "stdout: %s", out)
	return env.Data.Credentials
}

// TestRunAuthLogin_DeviceCodeFlowFinishesOnAuthorizedPoll exercises
// the full RFC 8628 path against a fake hub: the CLI requests a
// device code, waits for the polling interval, hits /oauth/token,
// and persists the issued tokens. Pinned at the smallest interval
// the hub server allows so the test isn't slow.
func TestRunAuthLogin_DeviceCodeFlowFinishesOnAuthorizedPoll(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	tokenHits := 0
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/device-authorization":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "dev-code-1",
				"user_code":        "WDJB-MJHT",
				"verification_uri": "https://example/activate",
				"expires_in":       60,
				"interval":         1,
			})
		case "/oauth/token":
			tokenHits++
			if tokenHits == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "lmx_a_at_dev",
				"refresh_token": "lmx_a_rt_dev",
				"expires_in":    3600,
				"user_id":       "user-dev",
				"username":      "devuser",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	o := runControl(t, "auth", "login", "--hub", srv.URL, "--device-code", "--device-name", "ci-runner")
	data := o.Data(t)
	assert.Equal(t, "devuser", data["username"])
	assert.Contains(t, o.Stderr, "WDJB-MJHT", "the code that the person types reaches stderr")
	assert.NotContains(t, o.Stdout, "WDJB-MJHT", "stdout holds the result alone")

	creds, err := control.LoadCredentials(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "lmx_a_at_dev", creds.AccessToken)
	assert.Equal(t, "user-dev", creds.UserID)
	assert.Equal(t, "devuser", creds.Username)
	assert.GreaterOrEqual(t, tokenHits, 2, "should poll once before the authorization completes")
}

// TestRunAuthLogin_DeviceCodeFlowSurfacesAccessDenied pins the
// negative path: when the hub returns `access_denied`, the CLI exits
// non-zero with a parseable error envelope rather than retrying
// indefinitely.
func TestRunAuthLogin_DeviceCodeFlowSurfacesAccessDenied(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/device-authorization":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "dev-code-deny",
				"user_code":        "DENY-CODE",
				"verification_uri": "https://example/activate",
				"expires_in":       60,
				"interval":         1,
			})
		case "/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "access_denied"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// The verification URI and the user code go to stderr, so stdout is the
	// envelope alone and decodes whole.
	out := runControl(t, "auth", "login", "--hub", srv.URL, "--device-code").failed(t)
	envErr := envelopeError(t, out)
	assert.Equal(t, "internal", envErr["code"])
	assert.Contains(t, envErr["message"], "access_denied")
}

// TestRunAuthLogin_DeviceCodeFlowDialsSocketHub pins the socket-login
// hole the HTTP-only tests cannot see: http.DefaultClient cannot dial
// a unix:/npipe: hub URL, so without hubtransport the device-code
// flow silently cannot log in against a hub reached over its IPC
// listener. Serving the RFC 8628 endpoints on a locallisten socket
// and driving --hub unix:… --device-code proves the CLI actually
// dials the socket (the handler runs) against the placeholder
// http://localhost origin the local transport uses, and that the
// credential file is still keyed by the unix: URL.
func TestRunAuthLogin_DeviceCodeFlowDialsSocketHub(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	sockURL := locallistentest.UniqueListenURL(t, "cli-auth")
	ln, err := locallisten.Listen(sockURL)
	require.NoError(t, err)

	var (
		mu        sync.Mutex
		seenHost  string
		authHits  int
		tokenHits int
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/device-authorization", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenHost = r.Host
		authHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "dev-code-sock",
			"user_code":        "SOCK-CODE",
			"verification_uri": "https://example/activate",
			"expires_in":       60,
			"interval":         1,
		})
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokenHits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "lmx_a_at_sock",
			"refresh_token": "lmx_a_rt_sock",
			"expires_in":    3600,
			"user_id":       "user-sock",
			"username":      "sockuser",
		})
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(ln) }()
	require.NoError(t, locallisten.WaitReady(context.Background(), sockURL))

	runControl(t, "auth", "login", "--hub", sockURL, "--device-code", "--device-name", "ci-runner").ok(t)

	creds, err := control.LoadCredentials(sockURL)
	require.NoError(t, err)
	assert.Equal(t, "lmx_a_at_sock", creds.AccessToken)
	assert.Equal(t, "user-sock", creds.UserID)
	assert.Equal(t, sockURL, creds.HubURL, "credentials stay keyed by the unix:/npipe: hub URL, not the placeholder origin")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, authHits, "device-authorization must arrive on the socket — DefaultClient cannot dial it")
	assert.Equal(t, 1, tokenHits, "the token poll must also arrive on the socket")
	assert.Equal(t, "localhost", seenHost, "hubtransport dials the socket against the placeholder http://localhost origin")
}

// TestRunAuthLogin_LocalRedirectRefusesSocketURL pins the other socket
// login hole: PKCE needs a browser-reachable hub origin, so a unix:/npipe:
// --hub without --device-code must refuse by stating the working flag
// rather than failing later with a scheme error.
func TestRunAuthLogin_LocalRedirectRefusesSocketURL(t *testing.T) {
	isolateCLIEnv(t)
	sockURL := locallistentest.UniqueListenURL(t, "cli-pkce")
	f := runControl(t, "auth", "login", "--hub", sockURL).requireFailure(t, cli.CodeUsage)
	assert.Contains(t, strings.Join(f.Hints, "\n"), "--device-code")
}

// TestRunAuthLogin_LocalRedirectLoginWithOpenBrowserReplaced drives the whole
// local-redirect login without a browser: the opener is replaced, and the
// test answers the callback of the URL the CLI printed. The credential file
// must land on disk from that exchange alone -- and no real browser may
// start, which the replaced opener makes structural rather than observed.
func TestRunAuthLogin_LocalRedirectLoginWithOpenBrowserReplaced(t *testing.T) {
	isolateCLIEnv(t)

	var (
		mu        sync.Mutex
		opened    string
		openHits  int
		tokenForm url.Values
	)
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		tokenForm = r.Form
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "lmx_a_at_loop",
			"refresh_token": "lmx_a_rt_loop",
			"expires_in":    3600,
			"user_id":       "user-loop",
			"username":      "loopuser",
			"token_id":      "tok-loop",
		})
	}))
	defer srv.Close()

	prev := control.OpenBrowser
	control.OpenBrowser = func(authorizeURL string) error {
		mu.Lock()
		opened = authorizeURL
		openHits++
		mu.Unlock()
		// The test is the person at the browser: it answers the callback of
		// the authorize URL, which carries the loopback redirect and the
		// state in its query.
		u, err := url.Parse(authorizeURL)
		require.NoError(t, err)
		q := u.Query()
		resp, err := http.Get(q.Get("redirect_uri") + "?" + url.Values{
			"state": {q.Get("state")},
			"code":  {"C-9"},
		}.Encode())
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return nil
	}
	t.Cleanup(func() { control.OpenBrowser = prev })

	out := runControl(t, "auth", "login", "--hub", srv.URL, "--device-name", "test-laptop")
	assert.Equal(t, "loopuser", out.Data(t)["username"])
	assert.Len(t, out.lines(), 1)

	creds, err := control.LoadCredentials(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "lmx_a_at_loop", creds.AccessToken)
	assert.Equal(t, "user-loop", creds.UserID)
	assert.Equal(t, "loopuser", creds.Username)
	assert.Equal(t, "tok-loop", creds.TokenID)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, openHits, "one login opens the detour once")
	assert.Contains(t, opened, "/oauth/authorize?")
	assert.Contains(t, out.Stderr, opened, "the opener gets the URL the CLI printed")
	assert.NotContains(t, out.Stdout, opened)
	require.NotNil(t, tokenForm)
	assert.Equal(t, "authorization_code", tokenForm.Get("grant_type"))
	assert.Equal(t, "C-9", tokenForm.Get("code"))
	assert.NotEmpty(t, tokenForm.Get("code_verifier"))
}

// TestRunAuthLogin_DeviceCodeFlowSocketUnreachable pins the error
// path of the same socket dial: when the unix:/npipe: path has no
// listener, the CLI must still use hubtransport (so the failure is a
// dial error that reports the hub as unavailable) rather than
// http.DefaultClient's "unsupported protocol scheme" rejection, which
// is what a missing socket client looks like.
func TestRunAuthLogin_DeviceCodeFlowSocketUnreachable(t *testing.T) {
	isolateCLIEnv(t)
	sockURL := locallistentest.UniqueListenURL(t, "cli-auth-down")
	f := runControl(t, "auth", "login", "--hub", sockURL, "--device-code").requireFailure(t, cli.CodeUnavailable)
	assert.NotContains(t, f.Message, "unsupported protocol scheme",
		"a scheme error means DefaultClient saw the unix:/npipe: URL; hubtransport must dial it")
	assert.True(t, f.Retryable, "a hub that is down now may be up on the next try")
}

// TestRunAuthLogin_UnsupportedHubSchemeIsReportedAtTheFlag covers a --hub value
// no transport can be built for.
//
// The transport factory could not fail before: a socket client that would not
// build fell through to a plain client, and the request then failed later with
// "unsupported protocol scheme" -- a message from net/http that specifies
// neither the flag nor the schemes that ARE accepted. Reporting it where the
// URL is read gives the user both.
func TestRunAuthLogin_UnsupportedHubSchemeIsReportedAtTheFlag(t *testing.T) {
	isolateCLIEnv(t)
	f := runControl(t, "auth", "login", "--hub", "ftp://hub.example", "--device-code").requireFailure(t, cli.CodeUsage)
	assert.Contains(t, f.Message, "ftp://hub.example", "the message must identify the URL that was refused")
	assert.Contains(t, f.Message, "http://", "and the schemes that are accepted")
}

// TestRevokeBearer_NoOpOnEmptyBearer covers the safe-rerun path of
// `auth logout` when no credentials existed in the first place.
func TestRevokeBearer_NoOpOnEmptyBearer(t *testing.T) {
	require.NoError(t, revokeBearer(context.Background(), nil, "https://hub.example", "", oauthapp.ControlCLIClientID))
}

// TestRevokeBearer_SendsAuthorizationHeader pins the wire format so
// the hub's revoke handler can authenticate the caller. Token in the
// form body alone wouldn't satisfy the interceptor since /oauth/revoke
// also requires Bearer to identify the caller.
func TestRevokeBearer_SendsAuthorizationHeader(t *testing.T) {
	gotAuth := ""
	gotForm := ""
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotForm = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	require.NoError(t, revokeBearer(context.Background(), nil, srv.URL, "lmx_a_secret", oauthapp.ControlCLIClientID))
	assert.Equal(t, "Bearer lmx_a_secret", gotAuth)
	assert.Contains(t, gotForm, "token=lmx_a_secret")
	// The public client identifies itself per RFC 7009 section 2.1 so the hub
	// can bind the revocation to the app the credential was issued to.
	assert.Contains(t, gotForm, "client_id="+oauthapp.ControlCLIClientID)
}

// TestRevokeBearer_PropagatesNetworkError verifies that transport failures
// return an error so callers like `auth logout` can report a warning.
func TestRevokeBearer_PropagatesNetworkError(t *testing.T) {
	err := revokeBearer(context.Background(), nil, "http://127.0.0.1:1", "lmx_a_secret", oauthapp.ControlCLIClientID)
	require.Error(t, err)
}

// TestRunAuthCredentials_ListsTheAccountCredentials pins the verb that makes
// MyAPIToken.current reachable at all.
//
// `current` marks the row the REQUEST authenticated with, and the hub derives
// it from the caller's own credential -- so a browser session, which is what
// every other caller of ListMyAPITokens is, always reads false. This command
// authenticates with the credential itself, which is the whole point.
func TestRunAuthCredentials_ListsTheAccountCredentials(t *testing.T) {
	hubURL := startMyTokensHub(t, &stubMyTokensService{})

	rows := credentialRows(t, runControl(t, "auth", "credentials", "--hub", hubURL).ok(t))
	require.Len(t, rows, 2)
	assert.Equal(t, "alice@laptop", rows[0]["installation_name"])
	assert.Equal(t, oauthapp.ControlCLIClientID, rows[0]["client_id"])
	assert.Contains(t, rows[0]["granted_scopes"], "workspace:read",
		"the listing prints the GRANT, so a reader can see what each app can do")
	assert.Equal(t, true, rows[0]["current"], "the credential making the request must be marked")
	assert.Equal(t, false, rows[1]["current"])
	assert.Equal(t, []any{}, rows[1]["granted_scopes"], "empty granted_scopes must marshal as [], not null")
	// An unset timestamp leaves the key out, never prints the Unix epoch.
	assert.NotContains(t, rows[0], "last_used_at")
}

// stubMyTokensService answers ListMyAPITokens and nothing else, recording
// the page limit each request asked for.
type stubMyTokensService struct {
	leapmuxv1connect.UnimplementedUserServiceHandler
	mu     sync.Mutex
	limits []int64
}

func (s *stubMyTokensService) ListMyAPITokens(
	_ context.Context,
	req *leapmuxv1.ListMyAPITokensRequest,
) (*leapmuxv1.ListMyAPITokensResponse, error) {
	s.mu.Lock()
	s.limits = append(s.limits, int64(req.GetPage().GetLimit()))
	s.mu.Unlock()
	return &leapmuxv1.ListMyAPITokensResponse{
		Tokens: []*leapmuxv1.MyAPIToken{
			{Id: "tok-1", ClientId: oauthapp.ControlCLIClientID, InstallationName: "alice@laptop", GrantedScopes: []string{"account:read", "account:write", "agent:read", "agent:write", "file:read", "git:read", "git:write", "terminal:read", "terminal:write", "tunnel:open", "worker:admin", "worker:read", "workspace:read", "workspace:write"}, Current: true},
			{Id: "tok-2", ClientId: oauthapp.ControlCLIClientID, InstallationName: "ci"},
		},
	}, nil
}

func (s *stubMyTokensService) requestedLimits() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.limits...)
}

// TestRunAuthCredentials_AsksForTheHubsMaximumPage pins the page size to the
// hub's OWN constant rather than a number restated here.
//
// An omitted limit resolves to contracts.DefaultPageLimit, so the listing loop
// would take ten times the requests and cover a tenth of what its own limit
// claims; a hand-copied 500 answers correctly only until the hub changes it.
func TestRunAuthCredentials_AsksForTheHubsMaximumPage(t *testing.T) {
	stub := &stubMyTokensService{}
	hubURL := startMyTokensHub(t, stub)

	runControl(t, "auth", "credentials", "--hub", hubURL).ok(t)

	limits := stub.requestedLimits()
	require.NotEmpty(t, limits)
	assert.Equal(t, int64(contracts.MaxPageLimit), limits[0])
}

// startMyTokensHub serves handler as the hub's UserService and saves a live
// credential for it, so `auth credentials` reaches the handler with a bearer.
// It returns the --hub value.
//
// The handler is a REAL UserService handler, so the test runs the client's
// own codec and wire shape, not a hand-written JSON body that the proto
// client would refuse.
func startMyTokensHub(t *testing.T, handler leapmuxv1connect.UserServiceHandler) string {
	t.Helper()
	mux := http.NewServeMux()
	connectServer := connect.NewServer()
	leapmuxv1connect.RegisterUserServiceHandler(connectServer, handler)
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
	srv := hubtransporttest.NewServer(t, mux)
	t.Cleanup(srv.Close)

	isolateCLIEnv(t)
	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL: srv.URL, AccessToken: "lmx_a_test", RefreshToken: "lmx_r_test",
		ExpiresAt: time.Now().Add(time.Hour), UserID: "u-1", Username: "alice",
	}))
	return srv.URL
}

// pagedMyTokensService answers ListMyAPITokens one page at a time. pages
// maps a request cursor to its page. A cursor that pages does not hold
// fails with failure, or with InvalidArgument when failure is nil.
type pagedMyTokensService struct {
	leapmuxv1connect.UnimplementedUserServiceHandler
	pages   map[string]*leapmuxv1.ListMyAPITokensResponse
	failure error

	mu      sync.Mutex
	cursors []string
}

func (s *pagedMyTokensService) ListMyAPITokens(
	_ context.Context,
	req *leapmuxv1.ListMyAPITokensRequest,
) (*leapmuxv1.ListMyAPITokensResponse, error) {
	cursor := req.GetPage().GetCursor()
	s.mu.Lock()
	s.cursors = append(s.cursors, cursor)
	s.mu.Unlock()
	if page, ok := s.pages[cursor]; ok {
		return page, nil
	}
	if s.failure != nil {
		return nil, s.failure
	}
	return nil, connect.NewError(connect.CodeInvalidArgument, "unknown cursor "+cursor)
}

func (s *pagedMyTokensService) requestedCursors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cursors...)
}

// `auth credentials` prints EVERY page, not the first. A credential past the
// first page is one the operator cannot see, and so cannot revoke. The second
// request must carry the cursor that the first reply's page held, and a row
// that a keyset boundary repeats prints once.
func TestRunAuthCredentials_ReadsEveryPage(t *testing.T) {
	stub := &pagedMyTokensService{pages: map[string]*leapmuxv1.ListMyAPITokensResponse{
		"": {
			Tokens: []*leapmuxv1.MyAPIToken{{Id: "tok-1"}, {Id: "tok-2"}},
			Page:   &leapmuxv1.PageResponse{NextCursor: "page-2"},
		},
		"page-2": {
			Tokens: []*leapmuxv1.MyAPIToken{{Id: "tok-2"}, {Id: "tok-3"}},
		},
	}}
	hubURL := startMyTokensHub(t, stub)

	rows := credentialRows(t, runControl(t, "auth", "credentials", "--hub", hubURL).ok(t))
	ids := make([]any, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row["id"])
	}
	assert.Equal(t, []any{"tok-1", "tok-2", "tok-3"}, ids, "every page, and the repeated row once")
	assert.Equal(t, []string{"", "page-2"}, stub.requestedCursors(),
		"the second request carries the cursor from the first reply's page")
}

// A page that fails part-way through the listing fails the whole verb. A
// partial listing would hide the credentials on the pages it never read,
// from somebody who audits what can reach their account.
func TestRunAuthCredentials_AFailedLaterPageFailsTheListing(t *testing.T) {
	stub := &pagedMyTokensService{
		pages: map[string]*leapmuxv1.ListMyAPITokensResponse{
			"": {
				Tokens: []*leapmuxv1.MyAPIToken{{Id: "tok-1"}},
				Page:   &leapmuxv1.PageResponse{NextCursor: "page-2"},
			},
		},
		failure: connect.NewError(connect.CodeUnavailable, "hub is restarting"),
	}
	hubURL := startMyTokensHub(t, stub)

	out := runControl(t, "auth", "credentials", "--hub", hubURL).failed(t)

	var env struct {
		Data  json.RawMessage `json:"data"`
		Error map[string]any  `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env), "exactly one envelope: %s", out)
	assert.Equal(t, "unavailable", env.Error["code"])
	assert.Contains(t, env.Error["message"], "hub is restarting")
	assert.Empty(t, env.Data, "a failed listing prints no partial data")
	assert.NotContains(t, string(out), "tok-1", "the rows of the first page must not print")
	assert.Equal(t, []string{"", "page-2"}, stub.requestedCursors())
}

// --- the hub check behind auth status / whoami ------------------------
//
// Both commands print what the hub would actually DO with the bearer, not
// what the credential file says: one authenticated GetCurrentUser, with
// three endings -- confirmed (the hub's own username and admin flag),
// refused (not logged in; the refresh path already deleted the file), and
// unreachable (the local answer with a warning naming what was not
// verified).

// currentUserServer answers the one Connect RPC the hub check makes. The
// client speaks binary proto, so the body is a marshalled message, not JSON.
func currentUserServer(t *testing.T, username string, isAdmin bool) *httptest.Server {
	t.Helper()
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/leapmux.v1.AuthService/GetCurrentUser" {
			http.NotFound(w, r)
			return
		}
		body, err := proto.Marshal(&leapmuxv1.GetCurrentUserResponse{
			SessionReport: &leapmuxv1.SessionReport{User: &leapmuxv1.User{Id: "u-hub", Username: username, IsAdmin: isAdmin}},
		})
		if !assert.NoError(t, err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/proto")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// currentUserPermissionDeniedServer answers GetCurrentUser the way the hub
// answers a credential minted without account:read (`auth login --scope`):
// the credential is valid, but this one read sits beyond its ceiling.
func currentUserPermissionDeniedServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/leapmux.v1.AuthService/GetCurrentUser" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code": "permission_denied"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// liveCredentialFor writes a credential whose access token has not lapsed,
// so the check goes straight to the RPC without a rotation first.
func liveCredentialFor(t *testing.T, hubURL string) {
	t.Helper()
	require.NoError(t, control.SaveCredentials(hubURL, control.CredentialFile{
		HubURL:      hubURL,
		AccessToken: "lmx_a_at_status",
		// A refresh secret, so the refusal's repair can RUN: without one the
		// 401 has nothing to rotate and nothing to delete, and the file
		// survives a refusal that proved it worthless.
		RefreshToken: "lmx_a_rt_status",
		Username:     "local-name",
		UserID:       "u-local",
		ExpiresAt:    time.Now().Add(time.Hour),
		// The deadline a real login always carries, so the UTC assertions
		// cover both printed timestamps.
		RefreshExpiresAt: time.Now().Add(90 * 24 * time.Hour),
	}))
}

func TestRunAuthStatus_ConfirmsWithTheHub(t *testing.T) {
	isolateCLIEnv(t)
	srv := currentUserServer(t, "renamed-on-the-hub", true)
	liveCredentialFor(t, srv.URL)

	out := runControl(t, "auth", "status", "--hub", srv.URL).ok(t)

	data := envelopeData(t, out)
	assert.Equal(t, true, data["hub_checked"])
	// The hub's username WINS: the local copy is stale the moment an account
	// is renamed, and status is exactly where that must not mislead.
	assert.Equal(t, "renamed-on-the-hub", data["username"])
	assert.Equal(t, true, data["is_admin"])
	// The credential file's zone is wherever it was written; the OUTPUT is
	// always UTC, so every deadline the CLI prints ends in Z and sorts beside
	// the hub's own timestamps.
	for _, key := range []string{"expires", "refresh_expires"} {
		assert.Truef(t, strings.HasSuffix(data[key].(string), "Z"), "%s must end in Z", key)
	}
}

// TestRunAuthList_PrintsUTC pins the same rule for `auth list`: its rows
// read the credential FILE, whose zone is the writer's, and the envelope
// must not leak it.
func TestRunAuthList_PrintsUTC(t *testing.T) {
	isolateCLIEnv(t)
	srv := currentUserServer(t, "unused", false)
	liveCredentialFor(t, srv.URL)

	rows := credentialRows(t, runControl(t, "auth", "list").ok(t))
	require.Len(t, rows, 1)
	assert.True(t, strings.HasSuffix(rows[0]["expires"].(string), "Z"),
		"auth list must print the file's deadline in UTC")
	assert.Equal(t, true, rows[0]["default"], "the only stored credential is the default")
}

func TestRunAuthStatus_WarnsWhenTheHubCannotBeAsked(t *testing.T) {
	isolateCLIEnv(t)
	// A server that is already closed: the URL stays valid, the hub is gone.
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	liveCredentialFor(t, srv.URL)

	out := runControl(t, "auth", "status", "--hub", srv.URL).ok(t)

	data := envelopeData(t, out)
	// Offline stays USEFUL: the local answer prints, and the warning names
	// exactly what was not verified.
	assert.Equal(t, false, data["hub_checked"])
	assert.Equal(t, "local-name", data["username"])
	assert.Contains(t, data["warning"], "could not be reached")
}

func TestRunAuthStatus_RefusedMeansSignedOut(t *testing.T) {
	isolateCLIEnv(t)
	// The hub answers the RPC itself with 401, and the rotation the
	// interceptor then attempts with invalid_grant -- which deletes the
	// credential file on the way through.
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/leapmux.v1.AuthService/GetCurrentUser":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthenticated"}`))
		case "/oauth/token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	liveCredentialFor(t, srv.URL)

	out := runControl(t, "auth", "status", "--hub", srv.URL).failed(t)

	env := envelopeError(t, out)
	assert.Equal(t, "not_logged_in", env["code"],
		"a credential the hub refused is a signed-out state, whatever the file said")
	// And the file is GONE: the refusal cleaned up after itself, so the next
	// command does not re-learn it.
	_, err := control.LoadCredentials(srv.URL)
	assert.ErrorIs(t, err, control.ErrNotLoggedIn)
}

func TestRunWhoami_ConfirmsWithTheHub(t *testing.T) {
	isolateCLIEnv(t)
	srv := currentUserServer(t, "hub-name", false)
	liveCredentialFor(t, srv.URL)

	out := runControl(t, "whoami", "--hub", srv.URL).ok(t)

	data := envelopeData(t, out)
	assert.Equal(t, "hub-name", data["username"])
	assert.Equal(t, "u-hub", data["user_id"])
	assert.Equal(t, false, data["is_admin"])
}

// TestRunAuthStatus_FallsBackWhenTheCredentialLacksAccountRead and the
// whoami twin below pin the PermissionDenied classification: a valid
// credential that merely lacks account:read is NOT a signed-out state and
// NOT a failed call. The local answer stands with a warning that states
// the permission the confirmation could not cross -- and whoami keeps
// is_admin present (null, the honest unknown), so its direct endings carry
// one shape.
func TestRunAuthStatus_FallsBackWhenTheCredentialLacksAccountRead(t *testing.T) {
	isolateCLIEnv(t)
	srv := currentUserPermissionDeniedServer(t)
	liveCredentialFor(t, srv.URL)

	out := runControl(t, "auth", "status", "--hub", srv.URL).ok(t)

	data := envelopeData(t, out)
	assert.Equal(t, false, data["hub_checked"])
	assert.Equal(t, "local-name", data["username"],
		"a scope-limited credential still has a local identity to report")
	assert.Contains(t, data["warning"], "account:read",
		"the warning must name the permission the confirmation could not cross")
}

func TestRunWhoami_FallsBackWhenTheCredentialLacksAccountRead(t *testing.T) {
	isolateCLIEnv(t)
	srv := currentUserPermissionDeniedServer(t)
	liveCredentialFor(t, srv.URL)

	out := runControl(t, "whoami", "--hub", srv.URL).ok(t)

	data := envelopeData(t, out)
	assert.Equal(t, "local-name", data["username"])
	assert.Contains(t, data, "is_admin", "is_admin key must be present on fallback")
	assert.Nil(t, data["is_admin"],
		"is_admin stays present on the fallback as null -- the one value this ending cannot know")
	assert.Contains(t, data["warning"], "account:read")
}

// TestRunAuthLogin_RotatesExistingCredentialAfterSuccess pins safe credential
// rotation: the new login completes and saves first, and only then is the old
// credential revoked.
func TestRunAuthLogin_RotatesExistingCredentialAfterSuccess(t *testing.T) {
	isolateCLIEnv(t)

	var order []string
	var revokedToken string
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/device-authorization":
			order = append(order, "device-auth")
			_, _ = w.Write([]byte(`{"device_code":"dc-3","user_code":"IJKL-9012","verification_uri":"https://hub.example/activate","expires_in":60,"interval":0}`))
		case "/oauth/token":
			order = append(order, "token")
			_, _ = w.Write([]byte(`{"access_token":"lmx_rotated_tok","refresh_token":"lmx_rotated_rt","expires_in":3600,"token_id":"tok-rot","scope":"workspace:read","user_id":"user-rot","username":"rotated-user"}`))
		case "/oauth/revoke":
			order = append(order, "revoke")
			_ = r.ParseForm()
			revokedToken = r.FormValue("token")
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	liveCredentialFor(t, srv.URL)

	data := runControl(t, "auth", "login", "--hub", srv.URL, "--device-code").Data(t)
	assert.Equal(t, "rotated-user", data["username"])
	assert.Equal(t, []string{"device-auth", "token", "revoke"}, order,
		"old credential must be revoked only after the new login completes")
	assert.Equal(t, "lmx_a_at_status", revokedToken,
		"revoke must target the previous access token, not the newly minted one")
	loaded, err := control.LoadCredentials(srv.URL)
	require.NoError(t, err)
	assert.Equal(t, "lmx_rotated_tok", loaded.AccessToken)
}

// TestRunAuthLogin_RefusedFlagKeepsTheExistingLogin verifies that a refused
// flag fails before any network call and leaves the existing credential
// untouched. The scope below is the empty-token form splitScopeFlag rejects.
func TestRunAuthLogin_RefusedFlagKeepsTheExistingLogin(t *testing.T) {
	isolateCLIEnv(t)

	revoked := false
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/revoke" {
			revoked = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	liveCredentialFor(t, srv.URL)

	runControl(t, "auth", "login", "--hub", srv.URL, "--scope", ",").requireFailure(t, cli.CodeUsage)
	assert.False(t, revoked, "a refused flag must not revoke the working credential")
	_, err := control.LoadCredentials(srv.URL)
	require.NoError(t, err, "a refused flag must not delete the working credential either")
}

// TestRunAuthStatus_FallsBackWhenTheProxyDropsTheRoute pins the transport
// ending a status command meets behind a reverse proxy: a 404 -- which
// connect maps to Unimplemented -- is a hub that could not be ASKED, so the
// local answer stands with a warning, not a failed exit that hides the
// fields the file still answers.
func TestRunAuthStatus_FallsBackWhenTheProxyDropsTheRoute(t *testing.T) {
	isolateCLIEnv(t)
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // a proxy that no longer routes the hub
	}))
	t.Cleanup(srv.Close)
	liveCredentialFor(t, srv.URL)

	out := runControl(t, "auth", "status", "--hub", srv.URL).ok(t)

	data := envelopeData(t, out)
	assert.Equal(t, false, data["hub_checked"])
	assert.Equal(t, "local-name", data["username"])
	assert.Contains(t, data["warning"], "could not be reached")
}

// TestRunAuthStatus_HungHubCannotHoldTheCommand pins the budget's promise:
// the five-second cap bounds the WHOLE check, the interceptor's token
// rotation included. A hub that accepts connections and never answers must
// not hold `auth status` for the rotation's 30-second budget before the
// local-answer fallback fires -- and a rotation that dies at the deadline
// must read as unreachable, not as a refused credential, because the hub
// never answered anything.
func TestRunAuthStatus_HungHubCannotHoldTheCommand(t *testing.T) {
	isolateCLIEnv(t)
	hang := make(chan struct{})
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hang // accept, then stall every route the check touches
	}))
	// LIFO: unblock the stalled handlers BEFORE the server's Close waits
	// for them.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(hang) })
	// The access token sits inside refreshSkew, so the check first tries to
	// rotate it against the stalled hub.
	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL:           srv.URL,
		AccessToken:      "lmx_a_at_stale",
		RefreshToken:     "lmx_a_rt_stale",
		Username:         "local-name",
		UserID:           "u-local",
		ExpiresAt:        time.Now().Add(30 * time.Second),
		RefreshExpiresAt: time.Now().Add(90 * 24 * time.Hour),
	}))

	started := time.Now()
	out := runControl(t, "auth", "status", "--hub", srv.URL).ok(t)
	assert.Less(t, time.Since(started).Seconds(), 15.0,
		"a hung hub must not hold the command past the check's own budget")

	data := envelopeData(t, out)
	assert.Equal(t, false, data["hub_checked"])
	assert.Equal(t, "local-name", data["username"],
		"a rotation that could not run leaves the local identity, not a refusal")
}

// `auth login` with no hub selected states the flag that selects one. The
// generic no-hub hint would point at `auth login` itself.
func TestRunAuthLogin_RefusesWithoutAHub(t *testing.T) {
	isolateCLIEnv(t)
	f := runControl(t, "auth", "login").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "no hub selected", f.Message)
	assert.Equal(t, []string{"pass --hub <url> to choose the hub to sign in to"}, f.Hints)
}

// The device name defaults to <user>@<host> of this machine, computed when
// the login runs: a declared default would print this machine's name in the
// help, in `leapmux commands` and in the generated docs. A blank value takes
// the default too.
func TestRunAuthLogin_ComputesTheDefaultDeviceNameWhenItRuns(t *testing.T) {
	for _, args := range [][]string{nil, {"--device-name", "  "}} {
		isolateCLIEnv(t)
		var mu sync.Mutex
		var sent string
		srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/oauth/device-authorization" {
				if assert.NoError(t, r.ParseForm()) {
					mu.Lock()
					sent = r.PostForm.Get("installation_name")
					mu.Unlock()
				}
			}
			http.Error(w, "refused", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		runControl(t, append([]string{"auth", "login", "--hub", srv.URL, "--device-code"}, args...)...).failed(t)

		mu.Lock()
		assert.Equal(t, control.DefaultDeviceName(), sent, "args %v", args)
		mu.Unlock()
	}

	tree := NewCommand()
	login, _, err := tree.Find([]string{"auth", "login"})
	require.NoError(t, err)
	assert.Empty(t, login.Flags().Lookup("device-name").DefValue, "the declared default must not read this machine")
}

type localSoloAuthHub struct {
	leapmuxv1connect.UnimplementedAuthServiceHandler
	leapmuxv1connect.UnimplementedUserServiceHandler
	changedPassword string
}

func (h *localSoloAuthHub) GetCurrentUser(context.Context, *leapmuxv1.GetCurrentUserRequest) (*leapmuxv1.GetCurrentUserResponse, error) {
	return &leapmuxv1.GetCurrentUserResponse{
		SessionReport: &leapmuxv1.SessionReport{
			User: &leapmuxv1.User{Id: "u-solo", Username: "solo", IsAdmin: true},
		},
	}, nil
}

func (h *localSoloAuthHub) ListMyAPITokens(context.Context, *leapmuxv1.ListMyAPITokensRequest) (*leapmuxv1.ListMyAPITokensResponse, error) {
	return &leapmuxv1.ListMyAPITokensResponse{
		Tokens: []*leapmuxv1.MyAPIToken{{Id: "tok-solo", InstallationName: "local"}},
	}, nil
}

func (h *localSoloAuthHub) ChangePassword(_ context.Context, req *leapmuxv1.ChangePasswordRequest) (*leapmuxv1.ChangePasswordResponse, error) {
	h.changedPassword = req.GetNewPassword()
	return &leapmuxv1.ChangePasswordResponse{
		User: &leapmuxv1.User{Id: "u-solo", Username: "solo"},
	}, nil
}

// auth status, auth credentials, and auth change-password work over a local
// solo Hub IPC socket without stored credentials.
func TestRunAuth_LocalIPCHubWorksWithoutStoredCredentials(t *testing.T) {
	isolateCLIEnv(t)

	hub := &localSoloAuthHub{}
	connectServer := connect.NewServer()
	leapmuxv1connect.RegisterAuthServiceHandler(connectServer, hub)
	leapmuxv1connect.RegisterUserServiceHandler(connectServer, hub)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
	sockURL := hubtransporttest.NewSocketServer(t, "solo-auth", mux)

	status := runControl(t, "auth", "status", "--hub", sockURL).Data(t)
	assert.Equal(t, sockURL, status["hub_url"])
	assert.Equal(t, "solo", status["username"])
	assert.Equal(t, "u-solo", status["user_id"])
	assert.Equal(t, true, status["hub_checked"])
	assert.Equal(t, true, status["is_admin"])

	creds := credentialRows(t, runControl(t, "auth", "credentials", "--hub", sockURL).ok(t))
	require.Len(t, creds, 1)
	assert.Equal(t, "tok-solo", creds[0]["id"])

	pwOut := runControl(t, "auth", "change-password", "--hub", sockURL, "--password", "new-secret-pw").Data(t)
	assert.Equal(t, true, pwOut["changed"])
	assert.Equal(t, "solo", pwOut["username"])
	assert.Equal(t, "new-secret-pw", hub.changedPassword)
}

// `auth login` without --hub or LEAPMUX_HUB ignores an auto-discovered local
// solo IPC socket and asks for --hub <url>, while an explicit --hub unix:...
// still reports the unix-origin error.
func TestRunAuthLogin_IgnoresAutoDiscoveredLocalSoloSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix domain socket discovery applies only to non-Windows hosts")
	}
	isolateCLIEnv(t)

	root, err := os.MkdirTemp("", "lmxlogin-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	cfgDir := filepath.Join(root, "control")
	soloDir := filepath.Join(root, "solo", "hub")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	require.NoError(t, os.MkdirAll(soloDir, 0o700))
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)

	soloSock := filepath.Join(soloDir, "hub.sock")
	ln, err := net.Listen("unix", soloSock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	require.Equal(t, "unix:"+soloSock, control.DiscoverLocalHub())

	f := runControl(t, "auth", "login").requireFailure(t, cli.CodeUsage)
	assert.Equal(t, "no hub selected", f.Message)
	assert.Equal(t, []string{"pass --hub <url> to choose the hub to sign in to"}, f.Hints)

	// Explicit --hub unix:... keeps the local-origin error and --device-code hint.
	fExplicit := runControl(t, "auth", "login", "--hub", "unix:"+soloSock).requireFailure(t, cli.CodeUsage)
	assert.Contains(t, fExplicit.Message, "unix:")
	assert.Contains(t, strings.Join(fExplicit.Hints, "\n"), "--device-code")
}

func TestRunAuthStatus_LocalIPCHubUnreachableAndRefused(t *testing.T) {
	t.Run("unreachable local socket without credentials reports local-hub warning", func(t *testing.T) {
		isolateCLIEnv(t)
		downSock := locallistentest.UniqueListenURL(t, "solo-down")

		data := runControl(t, "auth", "status", "--hub", downSock).Data(t)
		assert.Equal(t, downSock, data["hub_url"])
		assert.Equal(t, false, data["hub_checked"])
		assert.Equal(t, "the local hub could not be reached to confirm this session", data["warning"])
	})

	t.Run("local socket refusal hints --device-code", func(t *testing.T) {
		isolateCLIEnv(t)
		mux := http.NewServeMux()
		mux.HandleFunc("/leapmux.v1.AuthService/GetCurrentUser", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthenticated","message":"rejected"}`))
		})
		sockURL := hubtransporttest.NewSocketServer(t, "solo-refused", mux)

		f := runControl(t, "auth", "status", "--hub", sockURL).requireFailure(t, cli.CodeNotLoggedIn)
		assert.Equal(t, []string{`run "leapmux auth login --hub ` + sockURL + ` --device-code" to sign in again`}, f.Hints)
	})
}

func TestRunWhoami_HubClientRefusedAndUnreachable(t *testing.T) {
	t.Run("refused credential returns not_logged_in", func(t *testing.T) {
		isolateCLIEnv(t)
		srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/leapmux.v1.AuthService/GetCurrentUser":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":"unauthenticated"}`))
			case "/oauth/token":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(srv.Close)
		liveCredentialFor(t, srv.URL)

		runControl(t, "whoami", "--hub", srv.URL).requireFailure(t, cli.CodeNotLoggedIn)
	})

	t.Run("unreachable hub falls back to cached identity with warning", func(t *testing.T) {
		isolateCLIEnv(t)
		srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		srv.Close()
		liveCredentialFor(t, srv.URL)

		data := runControl(t, "whoami", "--hub", srv.URL).Data(t)
		assert.Equal(t, srv.URL, data["hub_url"])
		assert.Equal(t, "u-local", data["user_id"])
		assert.Equal(t, "local-name", data["username"])
		assert.Contains(t, data, "is_admin")
		assert.Nil(t, data["is_admin"])
		assert.Contains(t, data["warning"], "could not be reached")
	})

	t.Run("unreachable anonymous local hub uses local-hub warning", func(t *testing.T) {
		isolateCLIEnv(t)
		downSock := locallistentest.UniqueListenURL(t, "whoami-down")

		data := runControl(t, "whoami", "--hub", downSock).Data(t)
		assert.Equal(t, downSock, data["hub_url"])
		assert.Contains(t, data, "is_admin")
		assert.Nil(t, data["is_admin"])
		assert.Equal(t, localHubUnreachableWarning, data["warning"])
	})
}

func TestRunWhoami_OverTabIPCDoesNotCallGetCurrentUserAndEnrichesConcurrently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}
	clearRemoteEnv(t)

	locateArrived := make(chan struct{})
	workerArrived := make(chan struct{})
	var (
		mu         sync.Mutex
		procedures []string
	)
	hub := &whoamiConcurrentHub{
		onCall: func(ctx context.Context, proc string) ([]byte, error) {
			mu.Lock()
			procedures = append(procedures, proc)
			mu.Unlock()
			switch proc {
			case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
				close(locateArrived)
				select {
				case <-workerArrived:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return proto.Marshal(&leapmuxv1.LocateTabResponse{
					Tab: &leapmuxv1.WorkspaceTab{
						WorkspaceId: "ws-1",
						TileId:      "tile-1",
						WorkerId:    "worker-A",
						Tab:         &leapmuxv1.TabIdent{TabId: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
					},
				})
			case leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure:
				return proto.Marshal(&leapmuxv1.GetWorkspaceResponse{
					Workspace: &leapmuxv1.Workspace{Id: "ws-1", Title: "Concurrent Workspace"},
				})
			default:
				return nil, connect.NewError(connect.CodePermissionDenied, "unexpected procedure: "+proc)
			}
		},
	}
	var workerOnce sync.Once
	worker := &whoamiConcurrentWorker{
		onDispatch: func(ctx context.Context, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
			workerOnce.Do(func() { close(workerArrived) })
			select {
			case <-locateArrived:
			case <-ctx.Done():
				_ = rw.SendError(4, ctx.Err().Error())
				return
			}
			switch {
			case strings.HasSuffix(req.GetMethod(), "/GetWorkerSystemInfo"):
				writeProtoResponse(rw, &leapmuxv1.GetWorkerSystemInfoResponse{Name: "laptop"})
			case strings.HasSuffix(req.GetMethod(), "/ListAgents"):
				writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{
					Agents: []*leapmuxv1.AgentInfo{{
						Id:            "agent-1",
						Title:         "Builder",
						WorkingDir:    "/repo",
						AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
						GitStatus:     &leapmuxv1.GitRepoStatus{Branch: "main"},
					}},
				})
			default:
				_ = rw.SendError(12, "unimplemented: "+req.GetMethod())
			}
		},
	}

	sockURL := shortIPCSocket(t)
	rawToken := controlipc.MintToken()
	srv, err := controlipc.Listen(controlipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: controlipc.TokenInfo{
			UserID:   userid.MustNew("u-spawn"),
			WorkerID: "worker-A",
			TabID:    "agent-1",
			TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
		},
		Router: &controlipc.Router{
			WorkerID:        "worker-A",
			UserID:          userid.MustNew("u-spawn"),
			Hub:             hub,
			LocalDispatcher: worker,
			Sources:         spawnTabSources{},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("LEAPMUX_CONTROL_SOCK", sockURL)
	t.Setenv("LEAPMUX_CONTROL_TOKEN", rawToken)

	data := runControl(t, "whoami").Data(t)
	assert.Contains(t, data, "is_admin")
	assert.Nil(t, data["is_admin"])
	assert.Equal(t, "ws-1", data["workspace_id"])
	assert.Equal(t, "Concurrent Workspace", data["workspace_title"])
	assert.Equal(t, "tile-1", data["tile_id"])
	assert.Equal(t, "laptop", data["worker_name"])
	assert.Equal(t, "Builder", data["tab_title"])
	assert.Equal(t, "/repo", data["working_dir"])
	assert.Equal(t, "main", data["git_branch"])

	mu.Lock()
	defer mu.Unlock()
	assert.NotContains(t, procedures, leapmuxv1connect.AuthServiceGetCurrentUserProcedure,
		"whoami over tab IPC must not call AuthService/GetCurrentUser")
}

func TestRunWhoami_OverTabIPCWithEmptyWorkerID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}

	t.Run("locates tab first then enriches workspace and worker concurrently", func(t *testing.T) {
		clearRemoteEnv(t)

		var (
			locateDone         atomic.Bool
			workerCalls        atomic.Int32
			workerBeforeLocate atomic.Bool
			workspaceArrived   = make(chan struct{})
			workerArrived      = make(chan struct{})
			workerOnce         sync.Once
		)
		hub := &whoamiConcurrentHub{
			onCall: func(ctx context.Context, proc string) ([]byte, error) {
				switch proc {
				case leapmuxv1connect.WorkspaceServiceLocateTabProcedure:
					if workerCalls.Load() > 0 {
						workerBeforeLocate.Store(true)
					}
					locateDone.Store(true)
					return proto.Marshal(&leapmuxv1.LocateTabResponse{
						Tab: &leapmuxv1.WorkspaceTab{
							WorkspaceId: "ws-empty-wk",
							TileId:      "tile-empty-wk",
							WorkerId:    "worker-A",
							Tab:         &leapmuxv1.TabIdent{TabId: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT},
						},
					})
				case leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure:
					close(workspaceArrived)
					select {
					case <-workerArrived:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return proto.Marshal(&leapmuxv1.GetWorkspaceResponse{
						Workspace: &leapmuxv1.Workspace{Id: "ws-empty-wk", Title: "Located Workspace"},
					})
				default:
					return nil, connect.NewError(connect.CodePermissionDenied, "unexpected procedure: "+proc)
				}
			},
		}
		worker := &whoamiConcurrentWorker{
			onDispatch: func(ctx context.Context, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
				workerCalls.Add(1)
				if !locateDone.Load() {
					workerBeforeLocate.Store(true)
				}
				workerOnce.Do(func() { close(workerArrived) })
				select {
				case <-workspaceArrived:
				case <-ctx.Done():
					_ = rw.SendError(4, ctx.Err().Error())
					return
				}
				switch {
				case strings.HasSuffix(req.GetMethod(), "/GetWorkerSystemInfo"):
					writeProtoResponse(rw, &leapmuxv1.GetWorkerSystemInfoResponse{Name: "located-worker"})
				case strings.HasSuffix(req.GetMethod(), "/ListAgents"):
					writeProtoResponse(rw, &leapmuxv1.ListAgentsResponse{
						Agents: []*leapmuxv1.AgentInfo{{
							Id:            "agent-1",
							Title:         "Located Agent",
							WorkingDir:    "/located",
							AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
							GitStatus:     &leapmuxv1.GitRepoStatus{Branch: "feat/empty-worker"},
						}},
					})
				default:
					_ = rw.SendError(12, "unimplemented: "+req.GetMethod())
				}
			},
		}

		sockURL := shortIPCSocket(t)
		rawToken := controlipc.MintToken()
		srv, err := controlipc.Listen(controlipc.Options{
			SocketURL: sockURL,
			Token:     rawToken,
			TokenInfo: controlipc.TokenInfo{
				UserID:   userid.MustNew("u-spawn"),
				WorkerID: "",
				TabID:    "agent-1",
				TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
			},
			Router: &controlipc.Router{
				WorkerID:        "worker-A",
				UserID:          userid.MustNew("u-spawn"),
				Hub:             hub,
				LocalDispatcher: worker,
				Sources:         spawnTabSources{},
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = srv.Close() })
		t.Setenv("LEAPMUX_CONTROL_SOCK", sockURL)
		t.Setenv("LEAPMUX_CONTROL_TOKEN", rawToken)

		data := runControl(t, "whoami").Data(t)
		assert.False(t, workerBeforeLocate.Load(), "LocateTab must complete before any Worker RPC")
		assert.GreaterOrEqual(t, workerCalls.Load(), int32(2), "Worker details must be queried after LocateTab")
		assert.Equal(t, "ws-empty-wk", data["workspace_id"])
		assert.Equal(t, "Located Workspace", data["workspace_title"])
		assert.Equal(t, "tile-empty-wk", data["tile_id"])
		assert.Equal(t, "located-worker", data["worker_name"])
		assert.Equal(t, "Located Agent", data["tab_title"])
		assert.Equal(t, "/located", data["working_dir"])
		assert.Equal(t, "feat/empty-worker", data["git_branch"])
	})

	t.Run("returns base fields and skips worker calls when LocateTab fails", func(t *testing.T) {
		clearRemoteEnv(t)

		var (
			mu          sync.Mutex
			procedures  []string
			workerCalls atomic.Int32
		)
		hub := &whoamiConcurrentHub{
			onCall: func(_ context.Context, proc string) ([]byte, error) {
				mu.Lock()
				procedures = append(procedures, proc)
				mu.Unlock()
				return nil, connect.NewError(connect.CodeUnavailable, "hub unavailable")
			},
		}
		worker := &whoamiConcurrentWorker{
			onDispatch: func(_ context.Context, _ *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
				workerCalls.Add(1)
				_ = rw.SendError(13, "unexpected worker call")
			},
		}

		sockURL := shortIPCSocket(t)
		rawToken := controlipc.MintToken()
		srv, err := controlipc.Listen(controlipc.Options{
			SocketURL: sockURL,
			Token:     rawToken,
			TokenInfo: controlipc.TokenInfo{
				UserID:   userid.MustNew("u-spawn"),
				WorkerID: "",
				TabID:    "agent-1",
				TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
			},
			Router: &controlipc.Router{
				WorkerID:        "worker-A",
				UserID:          userid.MustNew("u-spawn"),
				Hub:             hub,
				LocalDispatcher: worker,
				Sources:         spawnTabSources{},
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = srv.Close() })
		t.Setenv("LEAPMUX_CONTROL_SOCK", sockURL)
		t.Setenv("LEAPMUX_CONTROL_TOKEN", rawToken)

		out := runControl(t, "whoami")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		data := out.Data(t)
		assert.Equal(t, "u-spawn", data["user_id"])
		assert.Equal(t, "agent-1", data["tab_id"])
		assert.Equal(t, "agent", data["tab_type"])
		assert.NotContains(t, data, "workspace_id")
		assert.NotContains(t, data, "workspace_title")
		assert.NotContains(t, data, "worker_name")
		assert.NotContains(t, data, "tab_title")
		assert.Zero(t, workerCalls.Load(), "LocateTab failure must make zero Worker calls")

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, []string{leapmuxv1connect.WorkspaceServiceLocateTabProcedure}, procedures)
	})
}

type whoamiConcurrentHub struct {
	onCall func(ctx context.Context, proc string) ([]byte, error)
}

func (h *whoamiConcurrentHub) CallInner(ctx context.Context, _ userid.UserID, procedure string, _ []byte) ([]byte, error) {
	return h.onCall(ctx, procedure)
}

type whoamiConcurrentWorker struct {
	onDispatch func(ctx context.Context, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter)
}

func (w *whoamiConcurrentWorker) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	w.onDispatch(ctx, req, rw)
}

func TestRunAuthUse_NormalizesStoredHubURL(t *testing.T) {
	isolateCLIEnv(t)
	require.NoError(t, control.SaveCredentials("https://hub.example", control.CredentialFile{
		HubURL: "https://hub.example", Username: "alice", ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, control.SaveCredentials("https://other.example", control.CredentialFile{
		HubURL: "https://other.example", Username: "bob", ExpiresAt: time.Now().Add(time.Hour),
	}))

	useData := runControl(t, "auth", "use", "https://HUB.EXAMPLE/").Data(t)
	assert.Equal(t, "https://hub.example", useData["hub_url"])

	rows := credentialRows(t, runControl(t, "auth", "list").ok(t))
	require.Len(t, rows, 2)
	byHub := map[string]map[string]any{}
	for _, r := range rows {
		byHub[r["hub_url"].(string)] = r
	}
	assert.Equal(t, true, byHub["https://hub.example"]["default"])
	assert.Equal(t, false, byHub["https://other.example"]["default"])

	runControl(t, "auth", "logout", "--hub", "https://HUB.EXAMPLE/").ok(t)
	cfg, err := control.LoadContext()
	require.NoError(t, err)
	assert.Empty(t, cfg.Hub, "auth logout with non-canonical hub URL must still clear the default hub")
}

func TestRunAuthLogout_CorruptCredentialFileWarnsAndDeletes(t *testing.T) {
	isolateCLIEnv(t)
	hub := "https://corrupt.example"
	path, err := control.CredentialsPath(hub)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("{corrupt"), 0o600))

	data := runControl(t, "auth", "logout", "--hub", hub).Data(t)
	assert.Equal(t, hub, data["hub_url"])
	assert.Contains(t, data["warning"], "could not be read")
	assert.Contains(t, data["warning"], "Preferences › Apps › Connected apps")

	_, err = control.LoadCredentials(hub)
	assert.ErrorIs(t, err, control.ErrNotLoggedIn, "corrupt credential file must be deleted on logout")
}

func TestRunAuthChangePassword_ChecksHubBeforePromptAndRespectsStdin(t *testing.T) {
	t.Run("missing credentials fail before password prompt", func(t *testing.T) {
		isolateCLIEnv(t)
		runControl(t, "auth", "change-password", "--hub", "https://no-creds.example").
			requireFailure(t, cli.CodeNotLoggedIn)
	})

	t.Run("non-file stdin returns clean usage error without doubled prefix", func(t *testing.T) {
		isolateCLIEnv(t)
		liveCredentialFor(t, "https://pw.example")
		f := runControl(t, "auth", "change-password", "--hub", "https://pw.example").
			requireFailure(t, cli.CodeUsage)
		assert.Equal(t, "--password is required (stdin is not a terminal)", f.Message)
		assert.NotContains(t, f.Message, "usage:")
	})

	t.Run("non-terminal os.File stdin returns clean usage error without doubled prefix", func(t *testing.T) {
		isolateCLIEnv(t)
		liveCredentialFor(t, "https://pw-pipe.example")
		r, w, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = r.Close()
			_ = w.Close()
		})
		var stdout, stderr bytes.Buffer
		code := cli.Execute(
			context.Background(),
			NewCommand(),
			[]string{"auth", "change-password", "--hub", "https://pw-pipe.example", "--json"},
			r, &stdout, &stderr,
		)
		out := cliOutcome{Stdout: stdout.String(), Stderr: stderr.String(), Code: code}
		f := out.requireFailure(t, cli.CodeUsage)
		assert.True(t, strings.HasPrefix(f.Message, "--password is required"), "got: %s", f.Message)
		assert.NotContains(t, f.Message, "usage:")
	})

	t.Run("canceled context returns cancel cause instead of non-terminal usage error", func(t *testing.T) {
		root := NewCommand()
		cmd, _, err := root.Find([]string{"auth", "change-password"})
		require.NoError(t, err)

		cause := errors.New("prompt canceled by caller")
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)

		inv := &cli.Invocation{
			Command: cmd,
			Context: ctx,
			Stdin:   strings.NewReader(""),
			Printer: &cli.Printer{Out: io.Discard, Err: io.Discard, Format: cli.FormatText},
		}

		pw, err := requirePasswordFromInv(inv, "password", "New password: ")
		assert.Empty(t, pw)
		require.Error(t, err)
		assert.ErrorIs(t, err, cause)
		assert.NotEqual(t, cli.CodeUsage, cli.FromError(err).Code)
	})
}

func TestOpenDirectHubSession_OmitsRPCDeadline(t *testing.T) {
	isolateCLIEnv(t)
	hub := "https://direct-session.example"
	liveCredentialFor(t, hub)

	root := NewCommand()
	cmd, _, err := root.Find([]string{"auth", "credentials"})
	require.NoError(t, err)
	require.NoError(t, root.PersistentFlags().Set("hub", hub))

	var stderr bytes.Buffer
	inv := &cli.Invocation{
		Command: cmd,
		Context: context.Background(),
		Printer: &cli.Printer{Out: io.Discard, Err: &stderr, Format: cli.FormatText},
	}

	s, c, err := openDirectHubSession(inv)
	require.NoError(t, err)
	t.Cleanup(s.Close)

	assert.Equal(t, hub, c.HubURL)
	_, hasDeadline := s.ctx.Deadline()
	assert.False(t, hasDeadline, "openDirectHubSession must not impose a 30s rpcDeadline on interactive/step-up flows")
	s.Close()
	assert.ErrorIs(t, s.ctx.Err(), context.Canceled)
}

func TestNoticeDiscoveredLocalHub(t *testing.T) {
	localHub := "unix:/tmp/solo/hub/hub.sock"

	t.Run("emits notice in text mode for auto-discovered local hub", func(t *testing.T) {
		var stderr bytes.Buffer
		inv := &cli.Invocation{
			Printer: &cli.Printer{Out: io.Discard, Err: &stderr, Format: cli.FormatText},
		}
		noticeDiscoveredLocalHub(inv, &control.HubClient{
			HubURL: localHub,
			Source: control.HubSourceDiscovered,
		})
		assert.Contains(t, stderr.String(), "using discovered local Hub "+localHub+" (override with --hub or LEAPMUX_HUB)")
	})

	t.Run("skips notice in non-text mode, non-discovered sources, or worker IPC peers", func(t *testing.T) {
		cases := []struct {
			name   string
			format cli.Format
			peer   control.Peer
		}{
			{name: "json format", format: cli.FormatJSON, peer: &control.HubClient{HubURL: localHub, Source: control.HubSourceDiscovered}},
			{name: "explicit --hub", format: cli.FormatText, peer: &control.HubClient{HubURL: localHub, Source: control.HubSourceFlag}},
			{name: "LEAPMUX_HUB set", format: cli.FormatText, peer: &control.HubClient{HubURL: localHub, Source: control.HubSourceEnv}},
			{name: "saved context", format: cli.FormatText, peer: &control.HubClient{HubURL: localHub, Source: control.HubSourceContext}},
			{name: "sole credential", format: cli.FormatText, peer: &control.HubClient{HubURL: "https://hub.example", Source: control.HubSourceSingleCredential}},
			{name: "worker IPC peer", format: cli.FormatText, peer: &control.WorkerIPCClient{SocketURL: "unix:/tmp/tab.sock"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var stderr bytes.Buffer
				inv := &cli.Invocation{
					Printer: &cli.Printer{Out: io.Discard, Err: &stderr, Format: tc.format},
				}
				noticeDiscoveredLocalHub(inv, tc.peer)
				assert.Empty(t, stderr.String())
			})
		}
	})
}

func TestAuthCommands_NoticeAutoDiscoveredLocalHubInTextMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix domain socket discovery applies only to non-Windows hosts")
	}
	isolateCLIEnv(t)

	root, err := os.MkdirTemp("", "lmxnot-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	cfgDir := filepath.Join(root, "control")
	soloDir := filepath.Join(root, "solo", "hub")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	require.NoError(t, os.MkdirAll(soloDir, 0o700))
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)

	soloSock := filepath.Join(soloDir, "hub.sock")
	sockURL := "unix:" + soloSock
	hub := &localSoloAuthHub{}
	connectServer := connect.NewServer()
	leapmuxv1connect.RegisterAuthServiceHandler(connectServer, hub)
	leapmuxv1connect.RegisterUserServiceHandler(connectServer, hub)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
	ln, err := locallisten.Listen(sockURL)
	require.NoError(t, err)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(ln) }()
	require.NoError(t, locallisten.WaitReady(context.Background(), sockURL))

	for _, args := range [][]string{
		{"auth", "status"},
		{"auth", "credentials"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.Execute(context.Background(), NewCommand(), args, strings.NewReader(""), &stdout, &stderr)
			require.Equal(t, cli.ExitOK, code, "stderr: %s", stderr.String())
			assert.Contains(t, stderr.String(), "using discovered local Hub "+sockURL)
		})
	}
}
