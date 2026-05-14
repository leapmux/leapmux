package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/util/pkce"
)

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

// TestDefaultDeviceName_FallsBackToHostnameOnEmptyUser exercises the
// fallback when neither USER nor USERNAME is set (containers, minimal
// CI runners). The result should still be informative — never empty.
func TestDefaultDeviceName_FallsBackToHostnameOnEmptyUser(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "")
	got := defaultDeviceName()
	assert.NotEmpty(t, got)
	assert.NotContains(t, got, "@", "no user → hostname-only, not user@host")
}

// TestDefaultDeviceName_PrefersUSEROverUSERNAME documents the
// POSIX-first lookup order: USER wins on Linux/macOS; USERNAME is the
// Windows-side fallback.
func TestDefaultDeviceName_PrefersUSEROverUSERNAME(t *testing.T) {
	t.Setenv("USER", "alice")
	t.Setenv("USERNAME", "bob")
	got := defaultDeviceName()
	assert.True(t, strings.HasPrefix(got, "alice@"), "USER should win, got %q", got)
}

// TestDefaultDeviceName_FallsBackToUSERNAME covers the Windows path
// where only USERNAME is populated.
func TestDefaultDeviceName_FallsBackToUSERNAME(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "winuser")
	got := defaultDeviceName()
	assert.True(t, strings.HasPrefix(got, "winuser@"))
}

// TestPersistTokenResponse_WritesCredentials covers the happy-path of
// the token-exchange persistence step: a valid hub response is decoded
// into a CredentialFile on disk under the test's isolated config dir.
func TestPersistTokenResponse_WritesCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", dir)
	withCapturedStdout(t, func() {
		body := strings.NewReader(`{
			"access_token": "lmx_a_at_xyz",
			"refresh_token": "lmx_a_rt_xyz",
			"expires_in": 3600,
			"user_id": "user-1",
			"username": "alice"
		}`)
		err := persistTokenResponse("https://hub.example", body)
		require.NoError(t, err)
	})

	loaded, err := remote.LoadCredentials("https://hub.example")
	require.NoError(t, err)
	assert.Equal(t, "https://hub.example", loaded.HubURL)
	assert.Equal(t, "lmx_a_at_xyz", loaded.AccessToken)
	assert.Equal(t, "lmx_a_rt_xyz", loaded.RefreshToken)
	assert.Equal(t, "user-1", loaded.UserID)
	assert.Equal(t, "alice", loaded.Username)
	// expires_at = now + expires_in; allow 1m skew for slow CI.
	assert.WithinDuration(t, time.Now().Add(time.Hour), loaded.ExpiresAt, time.Minute)
}

// TestPersistTokenResponse_RejectsMalformedJSON pins the failure path:
// a hub returning HTML or partial JSON should produce an error envelope
// rather than crash, and no credential file should be written.
func TestPersistTokenResponse_RejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", dir)
	out := withCapturedStdout(t, func() {
		err := persistTokenResponse("https://hub.example", strings.NewReader(`{not json`))
		require.Error(t, err)
		assert.True(t, remote.IsEmitted(err))
	})

	var env struct {
		Error map[string]string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "token_exchange_failed", env.Error["code"])

	// No credential file should have been written on the failure path.
	_, err := remote.LoadCredentials("https://hub.example")
	assert.ErrorIs(t, err, remote.ErrNotLoggedIn)
}

// TestRunAuthLogout_RevokesAndRemovesCreds exercises the full logout
// path: with credentials on disk, RunAuthLogout posts to the hub's
// /auth/cli/revoke endpoint with the bearer in both the form body and
// the Authorization header, then removes the local credential file.
func TestRunAuthLogout_RevokesAndRemovesCreds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", dir)

	revoked := false
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/cli/revoke" {
			revoked = true
			gotAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	require.NoError(t, remote.SaveCredentials(srv.URL, remote.CredentialFile{
		HubURL:      srv.URL,
		AccessToken: "lmx_a_at_logout",
		Username:    "alice",
		ExpiresAt:   time.Now().Add(time.Hour),
	}))

	out := withCapturedStdout(t, func() {
		err := RunAuthLogout(fakeCmdCtx{}, []string{"--hub", srv.URL})
		require.NoError(t, err)
	})

	assert.True(t, revoked, "logout must hit /auth/cli/revoke")
	assert.Equal(t, "Bearer lmx_a_at_logout", gotAuth)

	// Credentials must be gone from disk.
	_, err := remote.LoadCredentials(srv.URL)
	assert.ErrorIs(t, err, remote.ErrNotLoggedIn)

	var env struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, srv.URL, env.Data["hub_url"])
}

// TestRunAuthLogout_ToleratesMissingCreds covers the safe-to-rerun
// case: no credential file means there's nothing to revoke locally,
// but the command should still exit cleanly with a JSON envelope so
// scripts can use it without first checking.
func TestRunAuthLogout_ToleratesMissingCreds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", dir)

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
// --hub means no server-side revoke is even possible, so we surface
// invalid_request instead of silently no-op'ing.
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
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", dir)

	require.NoError(t, remote.SaveCredentials("https://hub.example", remote.CredentialFile{
		HubURL:    "https://hub.example",
		Username:  "alice",
		UserID:    "u1",
		ExpiresAt: time.Now().Add(time.Hour),
	}))

	out := withCapturedStdout(t, func() {
		err := RunAuthStatus(fakeCmdCtx{}, []string{"--hub", "https://hub.example"})
		require.NoError(t, err)
	})
	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, "alice", env.Data["username"])
	assert.Equal(t, "u1", env.Data["user_id"])
	assert.Equal(t, false, env.Data["expired"])
}

// TestRunAuthStatus_ReportsExpired covers the expired-credentials
// case so scripts can detect "log in again" without comparing
// timestamps.
func TestRunAuthStatus_ReportsExpired(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", dir)

	require.NoError(t, remote.SaveCredentials("https://hub.example", remote.CredentialFile{
		HubURL:    "https://hub.example",
		Username:  "bob",
		ExpiresAt: time.Now().Add(-time.Hour),
	}))

	out := withCapturedStdout(t, func() {
		err := RunAuthStatus(fakeCmdCtx{}, []string{"--hub", "https://hub.example"})
		require.NoError(t, err)
	})
	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Equal(t, true, env.Data["expired"])
}

// TestRunAuthStatus_NotLoggedInWhenMissing covers the negative
// branch: status against a hub the user never logged into surfaces
// `not_logged_in` rather than crashing or silently succeeding.
func TestRunAuthStatus_NotLoggedInWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", dir)

	out := withCapturedStdout(t, func() {
		err := RunAuthStatus(fakeCmdCtx{}, []string{"--hub", "https://never-logged-in.example"})
		require.Error(t, err)
		assert.True(t, remote.IsEmitted(err))
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
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", dir)

	require.NoError(t, remote.SaveCredentials("https://a.example", remote.CredentialFile{HubURL: "https://a.example", Username: "alice", ExpiresAt: time.Now().Add(time.Hour)}))
	require.NoError(t, remote.SaveCredentials("https://b.example", remote.CredentialFile{HubURL: "https://b.example", Username: "bob", ExpiresAt: time.Now().Add(time.Hour)}))

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
// case where the config directory hasn't been created yet. The
// command should print an empty list, not error out — scripts using
// it as a presence check shouldn't have to set up the directory
// themselves.
func TestRunAuthList_ToleratesMissingConfigDir(t *testing.T) {
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", t.TempDir()+"/never-created")

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
// device code, waits for the polling interval, hits /auth/cli/token,
// and persists the issued tokens. Pinned at the smallest interval
// the hub server allows so the test isn't slow.
func TestRunAuthLogin_DeviceCodeFlowFinishesOnAuthorizedPoll(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", dir)

	tokenHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/cli/device-authorization":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "dev-code-1",
				"user_code":        "WDJB-MJHT",
				"verification_uri": "https://example/activate",
				"expires_in":       60,
				"interval":         1,
			})
		case "/auth/cli/token":
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

	creds, err := remote.LoadCredentials(srv.URL)
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
	t.Setenv("LEAPMUX_REMOTE_CONFIG_DIR", dir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/cli/device-authorization":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":      "dev-code-deny",
				"user_code":        "DENY-CODE",
				"verification_uri": "https://example/activate",
				"expires_in":       60,
				"interval":         1,
			})
		case "/auth/cli/token":
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
	require.NoError(t, revokeBearer("https://hub.example", ""))
}

// TestRevokeBearer_SendsAuthorizationHeader pins the wire format so
// the hub's revoke handler can authenticate the caller. Token in the
// form body alone wouldn't satisfy the interceptor since /auth/cli/revoke
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

	require.NoError(t, revokeBearer(srv.URL, "lmx_a_secret"))
	assert.Equal(t, "Bearer lmx_a_secret", gotAuth)
	assert.Contains(t, gotForm, "token=lmx_a_secret")
}

// TestRevokeBearer_PropagatesNetworkError documents that
// transport-level failures are surfaced to the caller — `auth logout`
// chooses to swallow this error itself, but the helper must still
// report it so future callers can react.
func TestRevokeBearer_PropagatesNetworkError(t *testing.T) {
	err := revokeBearer("http://127.0.0.1:1", "lmx_a_secret")
	require.Error(t, err)
}

// TestEmittedError_IsEmittedTrueOnEmitErrorReturn closes the loop on
// the EmittedError marker: the error returned from EmitError must be
// recognised by IsEmitted so main.handleRunError can suppress its
// plain-text fallback.
func TestEmittedError_IsEmittedTrueOnEmitErrorReturn(t *testing.T) {
	var buf bytes.Buffer
	prev := remote.Out
	remote.Out = &buf
	t.Cleanup(func() { remote.Out = prev })

	err := remote.EmitError("some_code", "some message")
	require.Error(t, err)
	assert.True(t, remote.IsEmitted(err))
	assert.Contains(t, err.Error(), "some_code")

	// Plain Go errors must NOT be flagged as emitted, otherwise the
	// CLI would silently swallow legitimate non-emitted failures.
	assert.False(t, remote.IsEmitted(fmt.Errorf("plain error")))
	assert.False(t, remote.IsEmitted(nil))
}
