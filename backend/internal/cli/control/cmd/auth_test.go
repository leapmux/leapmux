package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/oauthapp"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/util/pkce"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// isolateCLIEnv gives one test its own credential store AND cuts it off from the
// worker-spawned environment.
//
// The config dir alone is not isolation. `NewClientFromEnv` -- which every verb that
// calls requireClient goes through -- prefers LEAPMUX_CONTROL_SOCK over the --hub flag
// and over the stored credential, because a CLI invoked inside an agent must reach its
// own worker. A developer who runs this suite from inside a LeapMux agent inherits that
// socket, so the client under test talks to a real worker instead of the test's httptest
// server: the verb then emits the worker-IPC envelope, and the assertions read empty
// fields and absent keys rather than a wrong value. CI has none of these set, so the
// failure appears only on the machine the product itself is used on.
//
// LEAPMUX_HUB is cleared for the same reason one step down: it is the fallback when no
// --hub is passed, and a test that means "no hub configured" must not inherit one.
func isolateCLIEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	for _, key := range []string{"LEAPMUX_CONTROL_SOCK", "LEAPMUX_CONTROL_TOKEN", "LEAPMUX_HUB"} {
		t.Setenv(key, "")
	}
}

// isolateCLIEnv is what keeps every verb in this package pointed at the test's own
// httptest server. Each variable it clears steers client construction somewhere else,
// so dropping one silently sends a test to a real worker or a real hub.
func TestIsolateCLIEnv_CutsTheWorkerSpawnedEnvironment(t *testing.T) {
	// The environment a LeapMux-spawned agent inherits, which is where this suite runs
	// when a developer uses the product to work on it.
	t.Setenv("LEAPMUX_CONTROL_SOCK", "unix:/tmp/real-worker.sock")
	t.Setenv("LEAPMUX_CONTROL_TOKEN", "real-token")
	t.Setenv("LEAPMUX_HUB", "https://real-hub.example")

	t.Run("isolated", func(t *testing.T) {
		isolateCLIEnv(t)
		for _, key := range []string{"LEAPMUX_CONTROL_SOCK", "LEAPMUX_CONTROL_TOKEN", "LEAPMUX_HUB"} {
			assert.Emptyf(t, os.Getenv(key), "%s must not reach a test", key)
		}
		assert.NotEmpty(t, os.Getenv("LEAPMUX_CONTROL_CONFIG_DIR"), "the credential store is still isolated")

		// The point of clearing the socket: the client follows --hub rather than the
		// inherited worker, so a verb reaches the test's server.
		c, err := control.NewClientOrAnonymous("https://hub.example")
		require.NoError(t, err)
		assert.False(t, c.IsWorkerIPC(), "an inherited worker socket must not capture the client")
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
	withCapturedStdout(t, func() {
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
		err := persistTokenResponse("https://hub.example", body, oauthapp.ControlCLIClientID, "admin:read")
		require.NoError(t, err)
	})

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
	out := withCapturedStdout(t, func() {
		body := strings.NewReader(`{
			"access_token": "lmx_a_at_xyz",
			"refresh_token": "lmx_a_rt_xyz",
			"expires_in": 3600,
			"scope": "workspace:read",
			"user_id": "user-1",
			"username": "alice"
		}`)
		require.NoError(t, persistTokenResponse("https://hub.example", body, oauthapp.ControlCLIClientID, "admin:read"))
	})

	data := envelopeData(t, out)
	assert.Equal(t, "workspace:read", data["scope"])
	assert.Contains(t, data["warning"], "admin:read",
		"the warning must NAME the permission that was refused")

	loaded, err := control.LoadCredentials("https://hub.example")
	require.NoError(t, err)
	assert.Equal(t, "workspace:read", loaded.Scope,
		"the file must record what was GRANTED, not what was asked")
}

// TestRunAuthLogin_WarnsWhenTheRevokeFails pins the best-effort half of the
// up-front cleanup. A hub that refuses the revoke must not fail the login,
// but the refusal must be STATED, with the one place the operator can finish
// the job -- and that place must be the row the Preferences dialog actually
// draws (Apps, not the Account group it moved out of).
func TestRunAuthLogin_WarnsWhenTheRevokeFails(t *testing.T) {
	isolateCLIEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/revoke":
			w.WriteHeader(http.StatusInternalServerError)
		case "/oauth/device-authorization":
			http.Error(w, "refused", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	liveCredentialFor(t, srv.URL)

	var runErr error
	out := withCapturedStdout(t, func() {
		runErr = RunAuthLogin(fakeCmdCtx{}, []string{"--hub", srv.URL, "--device-code"})
	})
	require.Error(t, runErr, "the failed authorization is the login's own error")

	assert.Contains(t, string(out), "could not revoke",
		"a revoke that did not happen must say so")
	assert.Contains(t, string(out), "Preferences › Apps › Connected apps",
		"the warning must name the group that holds the row, not the one a reorg removed")
	_, err := control.LoadCredentials(srv.URL)
	assert.ErrorIs(t, err, control.ErrNotLoggedIn,
		"the local file goes either way: the cleanup stays locally idempotent")
}

// TestRunAuthLogin_CleansUpAnUnreadableCredentialFile pins the corrupt-file
// half of the cleanup. A file that does not parse carries tokens this CLI
// cannot revoke, but the login must not leave it in place either: the file
// goes, and the warning states that its grant may still live on the hub.
func TestRunAuthLogin_CleansUpAnUnreadableCredentialFile(t *testing.T) {
	isolateCLIEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "refused", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	path, err := control.CredentialsPath(srv.URL)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("{not credential json"), 0o600))

	var runErr error
	out := withCapturedStdout(t, func() {
		runErr = RunAuthLogin(fakeCmdCtx{}, []string{"--hub", srv.URL, "--device-code"})
	})
	require.Error(t, runErr, "the failed authorization is the login's own error")

	assert.Contains(t, string(out), "does not parse",
		"an unreadable file must be reported, not silently overwritten")
	assert.Contains(t, string(out), "cannot revoke",
		"the warning must state what the CLI could not do about the grant")
	_, err = control.LoadCredentials(srv.URL)
	assert.ErrorIs(t, err, control.ErrNotLoggedIn,
		"the unreadable file must not survive the login attempt")
}

// TestPersistTokenResponse_RejectsMalformedJSON pins the failure path:
// a hub returning HTML or partial JSON should produce an error envelope
// rather than crash, and the CLI should write no credential file.
func TestPersistTokenResponse_RejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	out := withCapturedStdout(t, func() {
		err := persistTokenResponse("https://hub.example", strings.NewReader(`{not json`), oauthapp.ControlCLIClientID, "")
		require.Error(t, err)
		assert.True(t, control.IsEmitted(err))
	})

	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "token_exchange_failed", env.Error["code"])

	// No credential file should have been written on the failure path.
	_, err := control.LoadCredentials("https://hub.example")
	assert.ErrorIs(t, err, control.ErrNotLoggedIn)
}

// TestRunAuthLogout_RevokesAndRemovesCreds exercises the full logout
// path: with credentials on disk, RunAuthLogout posts to the hub's
// /oauth/revoke endpoint with the bearer in both the form body and
// the Authorization header, then removes the local credential file.
func TestRunAuthLogout_RevokesAndRemovesCreds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	revoked := false
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	out := withCapturedStdout(t, func() {
		err := RunAuthLogout(fakeCmdCtx{}, []string{"--hub", srv.URL})
		require.NoError(t, err)
	})

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

// TestRunAuthLogout_SucceedsWhenTheRowIsAlreadyGone is the credential that
// nobody used for months.
//
// The hub hard-deletes an api_tokens row once both of its deadlines close,
// and it answers a bearer whose row it cannot find with 401. Reporting that
// as a failed revoke told the user to finish the job under
// Preferences › Apps › Connected apps -- where the row is already gone, so
// there is nothing to act on and the warning can only worry them.
// TestRunAuthLogout_WarnsWhereToFinishTheJob pins the REFUSAL warning's
// destination. The warning once named "Command-line credentials", a
// Preferences row that no longer exists -- so the one user who needed it was
// sent hunting for a panel the rename had already taken away. The row it
// names must be the one the Preferences dialog actually draws.
func TestRunAuthLogout_WarnsWhereToFinishTheJob(t *testing.T) {
	isolateCLIEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/oauth/revoke", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL:      srv.URL,
		AccessToken: "lmx_a_at_live",
		Username:    "alice",
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAuthLogout(fakeCmdCtx{}, []string{"--hub", srv.URL}))
	})

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

func TestRunAuthLogout_SucceedsWhenTheRowIsAlreadyGone(t *testing.T) {
	for name, status := range map[string]int{
		"the hub hard-deleted the row": http.StatusUnauthorized,
		"a proxy answers not found":    http.StatusNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			isolateCLIEnv(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/oauth/revoke", r.URL.Path)
				w.WriteHeader(status)
			}))
			t.Cleanup(srv.Close)

			require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
				HubURL:      srv.URL,
				AccessToken: "lmx_a_at_stale",
				ExpiresAt:   time.Now().Add(-time.Hour),
			}))

			out := withCapturedStdout(t, func() {
				require.NoError(t, RunAuthLogout(fakeCmdCtx{}, []string{"--hub", srv.URL}))
			})

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL: srv.URL, AccessToken: "lmx_a_at_live", ExpiresAt: time.Now().Add(time.Hour),
	}))

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAuthLogout(fakeCmdCtx{}, []string{"--hub", srv.URL}))
	})
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

	// No revoke endpoint hit when there are no creds; an unreachable
	// hub URL is fine because revokeBearer swallows transport errors
	// (logout is best-effort on the server side).
	out := withCapturedStdout(t, func() {
		err := RunAuthLogout(fakeCmdCtx{}, []string{"--hub", "http://127.0.0.1:1"})
		require.NoError(t, err)
	})
	var env struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "http://127.0.0.1:1", env.Data["hub_url"])
}

// TestRunAuthLogout_RequiresHub guards the early-validation path: no
// --hub means no server-side revoke is even possible, so the CLI surfaces
// invalid_request instead of silently doing nothing.
func TestRunAuthLogout_RequiresHub(t *testing.T) {
	t.Setenv("LEAPMUX_HUB", "") // Block env-var fallback so the flag is actually missing.
	out := withCapturedStdout(t, func() {
		err := RunAuthLogout(fakeCmdCtx{}, nil)
		require.Error(t, err)
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "invalid_request", env.Error["code"])
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

	out := withCapturedStdout(t, func() {
		err := RunAuthStatus(fakeCmdCtx{}, []string{"--hub", "https://hub.example"})
		require.NoError(t, err)
	})
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

	out := withCapturedStdout(t, func() {
		err := RunAuthStatus(fakeCmdCtx{}, []string{"--hub", "https://hub.example"})
		require.NoError(t, err)
	})
	data := envelopeData(t, out)
	assert.Equal(t, true, data["expired"])
}

// TestRunAuthStatus_NotLoggedInWhenMissing covers the negative
// branch: status against a hub the user never logged into surfaces
// `not_logged_in` rather than crashing or silently succeeding.
func TestRunAuthStatus_NotLoggedInWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	out := withCapturedStdout(t, func() {
		err := RunAuthStatus(fakeCmdCtx{}, []string{"--hub", "https://never-logged-in.example"})
		require.Error(t, err)
		assert.True(t, control.IsEmitted(err))
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "not_logged_in", env.Error["code"])
}

// TestRunAuthList_PrintsAllConfiguredHubs is the multi-hub case:
// `auth list` enumerates every credential file under the config
// directory so users can audit their CLI footprint at a glance.
func TestRunAuthList_PrintsAllConfiguredHubs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	require.NoError(t, control.SaveCredentials("https://a.example", control.CredentialFile{HubURL: "https://a.example", Username: "alice", ExpiresAt: time.Now().Add(time.Hour)}))
	require.NoError(t, control.SaveCredentials("https://b.example", control.CredentialFile{HubURL: "https://b.example", Username: "bob", ExpiresAt: time.Now().Add(time.Hour)}))

	out := withCapturedStdout(t, func() {
		err := RunAuthList(fakeCmdCtx{}, nil)
		require.NoError(t, err)
	})
	var env struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Len(t, env.Data, 2)

	// Order is filesystem-dependent; index by hub_url to stay stable.
	byHub := map[string]map[string]any{}
	for _, e := range env.Data {
		byHub[e["hub_url"].(string)] = e
	}
	assert.Equal(t, "alice", byHub["https://a.example"]["username"])
	assert.Equal(t, "bob", byHub["https://b.example"]["username"])
}

// TestRunAuthList_ToleratesMissingConfigDir covers the first-run
// case where nothing created the config directory yet. The
// command should print an empty list, not error out — scripts using
// it as a presence check shouldn't have to set up the directory
// themselves.
func TestRunAuthList_ToleratesMissingConfigDir(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir()+"/never-created")

	out := withCapturedStdout(t, func() {
		err := RunAuthList(fakeCmdCtx{}, nil)
		require.NoError(t, err)
	})
	var env struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Empty(t, env.Data)
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	withCapturedStdout(t, func() {
		err := RunAuthLogin(fakeCmdCtx{}, []string{"--hub", srv.URL, "--device-code", "--device-name", "ci-runner"})
		require.NoError(t, err)
	})

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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	out := withCapturedStdout(t, func() {
		err := RunAuthLogin(fakeCmdCtx{}, []string{"--hub", srv.URL, "--device-code"})
		require.Error(t, err)
	})
	// runDeviceCodeLogin prints the verification URI / user code to
	// stdout as plain prose before the JSON envelope; isolate the
	// envelope by scanning for the first '{' the way `jq` would.
	envBytes := jsonTail(t, out)
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(envBytes, &env))
	assert.Equal(t, "device_grant_failed", env.Error["code"])
	assert.Contains(t, env.Error["message"], "access_denied")
}

// TestRunAuthLogin_DeviceCodeFlowDialsSocketHub pins the socket-login
// hole the HTTP-only tests cannot see: http.DefaultClient cannot dial
// a unix:/npipe: hub URL, so without cliHTTPClient the device-code
// flow silently cannot log in against a hub reached over its IPC
// listener. Serving the RFC 8628 endpoints on a locallisten socket
// and driving --hub unix:… --device-code proves the CLI actually
// dials the socket (the handler runs) against the placeholder
// http://localhost origin LocalHTTPClient uses, and that the
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

	withCapturedStdout(t, func() {
		err := RunAuthLogin(fakeCmdCtx{}, []string{"--hub", sockURL, "--device-code", "--device-name", "ci-runner"})
		require.NoError(t, err)
	})

	creds, err := control.LoadCredentials(sockURL)
	require.NoError(t, err)
	assert.Equal(t, "lmx_a_at_sock", creds.AccessToken)
	assert.Equal(t, "user-sock", creds.UserID)
	assert.Equal(t, sockURL, creds.HubURL, "credentials stay keyed by the unix:/npipe: hub URL, not the placeholder origin")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, authHits, "device-authorization must arrive on the socket — DefaultClient cannot dial it")
	assert.Equal(t, 1, tokenHits, "the token poll must also arrive on the socket")
	assert.Equal(t, "localhost", seenHost, "LocalHTTPClient dials the socket against the placeholder http://localhost origin")
}

// TestRunAuthLogin_LocalRedirectRefusesSocketURL pins the other socket
// login hole: PKCE needs a browser-reachable hub origin, so a unix:/npipe:
// --hub without --device-code must refuse by stating the working flag
// rather than failing later with a scheme error.
func TestRunAuthLogin_LocalRedirectRefusesSocketURL(t *testing.T) {
	sockURL := locallistentest.UniqueListenURL(t, "cli-pkce")
	out := withCapturedStdout(t, func() {
		err := RunAuthLogin(fakeCmdCtx{}, []string{"--hub", sockURL})
		require.Error(t, err)
		assert.True(t, control.IsEmitted(err))
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "invalid_request", env.Error["code"])
	assert.Contains(t, env.Error["message"], "--device-code")
}

// TestRunAuthLogin_DeviceCodeFlowSocketUnreachable pins the error
// path of the same socket dial: when the unix:/npipe: path has no
// listener, the CLI must still use cliHTTPClient (so the failure is a
// dial error wrapped as device_authorization_failed) rather than
// http.DefaultClient's "unsupported protocol scheme" rejection, which
// is what a missing socket client looks like.
func TestRunAuthLogin_DeviceCodeFlowSocketUnreachable(t *testing.T) {
	sockURL := locallistentest.UniqueListenURL(t, "cli-auth-down")
	out := withCapturedStdout(t, func() {
		err := RunAuthLogin(fakeCmdCtx{}, []string{"--hub", sockURL, "--device-code"})
		require.Error(t, err)
		assert.True(t, control.IsEmitted(err))
	})
	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "device_authorization_failed", env.Error["code"])
	assert.NotContains(t, env.Error["message"], "unsupported protocol scheme",
		"a scheme error means DefaultClient saw the unix:/npipe: URL; cliHTTPClient must dial it")
}

// jsonTail returns the JSON envelope at the end of out, skipping the
// device-code flow's plain-prose preamble.
func jsonTail(t *testing.T, out []byte) []byte {
	t.Helper()
	idx := bytes.IndexByte(out, '{')
	require.GreaterOrEqual(t, idx, 0, "expected a JSON envelope somewhere in stdout")
	return out[idx:]
}

// TestRevokeBearer_NoOpOnEmptyBearer covers the safe-rerun path of
// `auth logout` when no credentials existed in the first place.
func TestRevokeBearer_NoOpOnEmptyBearer(t *testing.T) {
	require.NoError(t, revokeBearer("https://hub.example", "", oauthapp.ControlCLIClientID))
}

// TestRevokeBearer_SendsAuthorizationHeader pins the wire format so
// the hub's revoke handler can authenticate the caller. Token in the
// form body alone wouldn't satisfy the interceptor since /oauth/revoke
// also requires Bearer to identify the caller.
func TestRevokeBearer_SendsAuthorizationHeader(t *testing.T) {
	gotAuth := ""
	gotForm := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotForm = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	require.NoError(t, revokeBearer(srv.URL, "lmx_a_secret", oauthapp.ControlCLIClientID))
	assert.Equal(t, "Bearer lmx_a_secret", gotAuth)
	assert.Contains(t, gotForm, "token=lmx_a_secret")
	// The public client names itself, per RFC 7009 section 2.1, so the hub
	// can bind the revocation to the app the credential was issued to.
	assert.Contains(t, gotForm, "client_id="+oauthapp.ControlCLIClientID)
}

// TestRevokeBearer_PropagatesNetworkError documents that
// transport-level failures are surfaced to the caller — `auth logout`
// chooses to swallow this error itself, but the helper must still
// report it so future callers can react.
func TestRevokeBearer_PropagatesNetworkError(t *testing.T) {
	err := revokeBearer("http://127.0.0.1:1", "lmx_a_secret", oauthapp.ControlCLIClientID)
	require.Error(t, err)
}

// TestEmittedError_IsEmittedTrueOnEmitErrorReturn closes the loop on
// the EmittedError marker: the error returned from EmitError must be
// recognised by IsEmitted so main.handleRunError can suppress its
// plain-text fallback.
func TestEmittedError_IsEmittedTrueOnEmitErrorReturn(t *testing.T) {
	var buf bytes.Buffer
	prev := control.Out
	control.Out = &buf
	t.Cleanup(func() { control.Out = prev })

	err := control.EmitError("some_code", "some message")
	require.Error(t, err)
	assert.True(t, control.IsEmitted(err))
	assert.Contains(t, err.Error(), "some_code")

	// Plain Go errors must NOT be flagged as emitted, otherwise the
	// CLI would silently swallow legitimate non-emitted failures.
	assert.False(t, control.IsEmitted(fmt.Errorf("plain error")))
	assert.False(t, control.IsEmitted(nil))
}

// TestRunAuthCredentials_ListsTheAccountCredentials pins the verb that makes
// MyAPIToken.current reachable at all.
//
// `current` marks the row the REQUEST authenticated with, and the hub derives
// it from the caller's own credential -- so a browser session, which is what
// every other caller of ListMyAPITokens is, always reads false. This command
// authenticates with the credential itself, which is the whole point.
func TestRunAuthCredentials_ListsTheAccountCredentials(t *testing.T) {
	// A REAL UserService handler, so the client's own codec and the wire
	// shape are exercised rather than a hand-written JSON body the proto
	// client would refuse.
	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewUserServiceHandler(&stubMyTokensService{})
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	isolateCLIEnv(t)
	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL: srv.URL, AccessToken: "lmx_a_test", RefreshToken: "lmx_r_test",
		ExpiresAt: time.Now().Add(time.Hour), UserID: "u-1", Username: "alice",
	}))

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAuthCredentials(fakeCmdCtx{}, []string{"--hub", srv.URL}))
	})

	var env struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Len(t, env.Data, 2)
	assert.Equal(t, "alice@laptop", env.Data[0]["installation_name"])
	assert.Equal(t, oauthapp.ControlCLIClientID, env.Data[0]["client_id"])
	assert.Contains(t, env.Data[0]["granted_scopes"], "workspace:read",
		"the listing prints the GRANT, so a reader can see what each app can do")
	assert.Equal(t, true, env.Data[0]["current"], "the credential making the request must be marked")
	assert.Equal(t, false, env.Data[1]["current"])
	// An unset timestamp reads as null, not as the Unix epoch.
	assert.Nil(t, env.Data[0]["last_used_at"])
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
	req *connect.Request[leapmuxv1.ListMyAPITokensRequest],
) (*connect.Response[leapmuxv1.ListMyAPITokensResponse], error) {
	s.mu.Lock()
	s.limits = append(s.limits, req.Msg.GetLimit())
	s.mu.Unlock()
	return connect.NewResponse(&leapmuxv1.ListMyAPITokensResponse{
		Tokens: []*leapmuxv1.MyAPIToken{
			{Id: "tok-1", ClientId: oauthapp.ControlCLIClientID, InstallationName: "alice@laptop", GrantedScopes: []string{"account:read", "account:write", "agent:read", "agent:write", "file:read", "git:read", "git:write", "terminal:read", "terminal:write", "tunnel:open", "worker:admin", "worker:read", "workspace:read", "workspace:write"}, Current: true},
			{Id: "tok-2", ClientId: oauthapp.ControlCLIClientID, InstallationName: "ci"},
		},
	}), nil
}

func (s *stubMyTokensService) requestedLimits() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.limits...)
}

// TestRunAuthCredentials_AsksForTheHubsMaximumPage pins the page size to the
// hub's OWN constant rather than a number restated here.
//
// An omitted limit resolves to service.DefaultPageLimit, so the listing loop
// would take ten times the requests and cover a tenth of what its own limit
// claims; a hand-copied 500 answers correctly only until the hub changes it.
func TestRunAuthCredentials_AsksForTheHubsMaximumPage(t *testing.T) {
	stub := &stubMyTokensService{}
	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewUserServiceHandler(stub)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	isolateCLIEnv(t)
	require.NoError(t, control.SaveCredentials(srv.URL, control.CredentialFile{
		HubURL: srv.URL, AccessToken: "lmx_a_test", RefreshToken: "lmx_r_test",
		ExpiresAt: time.Now().Add(time.Hour), UserID: "u-1", Username: "alice",
	}))

	withCapturedStdout(t, func() {
		require.NoError(t, RunAuthCredentials(fakeCmdCtx{}, []string{"--hub", srv.URL}))
	})

	limits := stub.requestedLimits()
	require.NotEmpty(t, limits)
	assert.Equal(t, int64(service.MaxPageLimit), limits[0])
}

// TestCallbackHandlerNeverBlocksOnDuplicates pins the local-redirect login's
// wedge fix: the browser can re-GET the success page (or a stale tab can
// re-send a bad state) after the CLI already holds its one outcome. A
// blocking send would park the handler goroutine and the deferred
// srv.Shutdown would wait for it forever -- a login that already succeeded
// hanging the CLI. Every duplicate here must answer within the timeout.
func TestCallbackHandlerNeverBlocksOnDuplicates(t *testing.T) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	// Pre-fill both channels: the CLI has consumed neither, so both buffers
	// are full -- the exact state a duplicate arrival meets.
	codeCh <- "code-1"
	errCh <- errors.New("first error")

	handler := callbackHandler("state-1", codeCh, errCh)
	serve := func(path, state, code string) bool {
		req := httptest.NewRequest(http.MethodGet, path+"?state="+state+"&code="+code, nil)
		done := make(chan struct{})
		go func() {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			close(done)
		}()
		select {
		case <-done:
			return true
		case <-time.After(2 * time.Second):
			return false
		}
	}

	require.True(t, serve("/callback", "state-1", "code-1"),
		"a duplicate success callback must answer, not block")
	require.True(t, serve("/callback", "wrong", "code-1"),
		"a duplicate bad-state callback must answer, not block")
	require.True(t, serve("/callback", "state-1", "code-1"),
		"a third duplicate must still answer")
	// The FIRST outcome survives; duplicates dropped rather than replaced it.
	require.Equal(t, "code-1", <-codeCh)
	require.EqualError(t, <-errCh, "first error")
}

// TestCallbackHandlerReportsTheServerErrorParameter pins the RFC 6749 section
// 4.1.2.1 branch of the local-redirect callback: the hub redirects every
// refusal it could validate back with `error` (a Deny, an invalid_scope, a
// server error), and folding those into the state-mismatch branch told a user
// who deliberately refused that the state check had failed -- a CSRF suspicion
// they can neither check nor act on. The state check stays first: a mismatched
// state is a mismatch whatever else the query carries.
func TestCallbackHandlerReportsTheServerErrorParameter(t *testing.T) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	handler := callbackHandler("state-1", codeCh, errCh)

	serve := func(t *testing.T, query string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/callback?"+query, nil))
		return rec
	}

	// A refusal with the matching state reports the ACTUAL cause.
	rec := serve(t, "state=state-1&error=access_denied&error_description=the+account+owner+refused")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	select {
	case err := <-errCh:
		require.ErrorContains(t, err, "access_denied")
		require.ErrorContains(t, err, "the account owner refused")
	default:
		t.Fatal("the refusal must reach the CLI's error channel")
	}
	select {
	case code := <-codeCh:
		t.Fatalf("a refused authorization must not carry a code: %q", code)
	default:
	}

	// A mismatched state is reported as the mismatch even when an error rides
	// along: the state check decides first.
	rec = serve(t, "state=wrong&error=access_denied")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	select {
	case err := <-errCh:
		require.ErrorContains(t, err, "callback state mismatch")
	default:
		t.Fatal("a mismatched state must reach the CLI's error channel")
	}

	// Neither a code nor an error is its own answer, not silence about which.
	rec = serve(t, "state=state-1")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	select {
	case err := <-errCh:
		require.ErrorContains(t, err, "neither a code nor an error")
	default:
		t.Fatal("an empty callback must reach the CLI's error channel")
	}
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/leapmux.v1.AuthService/GetCurrentUser" {
			http.NotFound(w, r)
			return
		}
		body, err := proto.Marshal(&leapmuxv1.GetCurrentUserResponse{
			User: &leapmuxv1.User{Id: "u-hub", Username: username, IsAdmin: isAdmin},
		})
		require.NoError(t, err)
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAuthStatus(fakeCmdCtx{}, []string{"--hub", srv.URL}))
	})

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

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAuthList(fakeCmdCtx{}, nil))
	})

	var env struct {
		Data []map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Len(t, env.Data, 1)
	assert.True(t, strings.HasSuffix(env.Data[0]["expires"].(string), "Z"),
		"auth list must print the file's deadline in UTC")
}

func TestRunAuthStatus_WarnsWhenTheHubCannotBeAsked(t *testing.T) {
	isolateCLIEnv(t)
	// A server that is already closed: the URL stays valid, the hub is gone.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	liveCredentialFor(t, srv.URL)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAuthStatus(fakeCmdCtx{}, []string{"--hub", srv.URL}))
	})

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	// An emitted error returns non-nil (it is what sets the exit code), so
	// the assertion is on the envelope it wrote, not on NoError.
	var runErr error
	out := withCapturedStdout(t, func() {
		runErr = RunAuthStatus(fakeCmdCtx{}, []string{"--hub", srv.URL})
	})
	require.Error(t, runErr)
	assert.True(t, control.IsEmitted(runErr), "the refusal reached the user as an envelope")

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

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunWhoami(fakeCmdCtx{}, []string{"--hub", srv.URL}))
	})

	data := envelopeData(t, out)
	assert.Equal(t, "hub-name", data["username"])
	assert.Equal(t, "u-hub", data["user_id"])
	assert.Equal(t, false, data["is_admin"])
}

// TestRunAuthStatus_FallsBackWhenTheCredentialLacksAccountRead and the
// whoami twin below pin the PermissionDenied classification: a valid
// credential that merely lacks account:read is NOT a signed-out state and
// NOT an rpc failure. The local answer stands with a warning that states
// the permission the confirmation could not cross -- and whoami keeps
// is_admin present (null, the honest unknown), so its direct endings carry
// one shape.
func TestRunAuthStatus_FallsBackWhenTheCredentialLacksAccountRead(t *testing.T) {
	isolateCLIEnv(t)
	srv := currentUserPermissionDeniedServer(t)
	liveCredentialFor(t, srv.URL)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAuthStatus(fakeCmdCtx{}, []string{"--hub", srv.URL}))
	})

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

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunWhoami(fakeCmdCtx{}, []string{"--hub", srv.URL}))
	})

	data := envelopeData(t, out)
	assert.Equal(t, "local-name", data["username"])
	assert.Nil(t, data["is_admin"],
		"is_admin stays present on the fallback as null -- the one value this ending cannot know")
	assert.Contains(t, data["warning"], "account:read")
}

// TestRunAuthLogin_CleansUpTheExistingLoginFirst pins the whole point of the
// up-front cleanup: the old credential is revoked and deleted BEFORE the flow
// starts, so a login that never completes still leaves no orphaned login
// behind. The fake hub refuses the device authorization, which is exactly
// the "attempting a login" that must not rescue the old credential.
func TestRunAuthLogin_CleansUpTheExistingLoginFirst(t *testing.T) {
	isolateCLIEnv(t)

	revoked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/revoke":
			revoked = true
			w.WriteHeader(http.StatusOK)
		case "/oauth/device-authorization":
			// The flow dies here; the cleanup must already have happened.
			http.Error(w, "refused", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	liveCredentialFor(t, srv.URL)

	var runErr error
	out := withCapturedStdout(t, func() {
		runErr = RunAuthLogin(fakeCmdCtx{}, []string{"--hub", srv.URL, "--device-code"})
	})
	require.Error(t, runErr, "the failed authorization is the login's own error")

	assert.True(t, revoked, "the existing login must be revoked before the flow starts")
	_, err := control.LoadCredentials(srv.URL)
	assert.ErrorIs(t, err, control.ErrNotLoggedIn,
		"a login that never completed must not leave the old credential on disk")
	assert.Contains(t, string(out), "existing login", "the cleanup warns before it cleans")
	assert.Contains(t, string(out), "local-name", "the warning identifies the login it removes")
}

// TestRunAuthLogin_RefusedFlagKeepsTheExistingLogin pins the ORDER around
// the up-front cleanup: every input is validated first, so a flag the CLI
// refuses must not cost the machine its working credential. The scope below
// is exactly the value splitScopeFlag exists to refuse.
func TestRunAuthLogin_RefusedFlagKeepsTheExistingLogin(t *testing.T) {
	isolateCLIEnv(t)

	revoked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/revoke" {
			revoked = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	liveCredentialFor(t, srv.URL)

	var runErr error
	out := withCapturedStdout(t, func() {
		runErr = RunAuthLogin(fakeCmdCtx{}, []string{"--hub", srv.URL, "--scope", ","})
	})
	require.Error(t, runErr)
	assert.True(t, control.IsEmitted(runErr))
	assert.Equal(t, "invalid_request", envelopeError(t, out)["code"],
		"the refused flag is the login's own error")
	assert.False(t, revoked, "a refused flag must not revoke the working credential")
	_, err := control.LoadCredentials(srv.URL)
	require.NoError(t, err, "a refused flag must not delete the working credential either")
}

// TestRunAuthStatus_FallsBackWhenTheProxyDropsTheRoute pins the transport
// ending a status command meets behind a reverse proxy: a 404 -- which
// connect maps to Unimplemented -- is a hub that could not be ASKED, so the
// local answer stands with a warning, not an rpc_failed exit that hides the
// fields the file still answers.
func TestRunAuthStatus_FallsBackWhenTheProxyDropsTheRoute(t *testing.T) {
	isolateCLIEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // a proxy that no longer routes the hub
	}))
	t.Cleanup(srv.Close)
	liveCredentialFor(t, srv.URL)

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAuthStatus(fakeCmdCtx{}, []string{"--hub", srv.URL}))
	})

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	out := withCapturedStdout(t, func() {
		require.NoError(t, RunAuthStatus(fakeCmdCtx{}, []string{"--hub", srv.URL}))
	})
	assert.Less(t, time.Since(started).Seconds(), 15.0,
		"a hung hub must not hold the command past the check's own budget")

	data := envelopeData(t, out)
	assert.Equal(t, false, data["hub_checked"])
	assert.Equal(t, "local-name", data["username"],
		"a rotation that could not run leaves the local identity, not a refusal")
}
