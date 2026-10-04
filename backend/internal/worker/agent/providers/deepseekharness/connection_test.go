package deepseekharness

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testToken = base64.RawURLEncoding.EncodeToString(make([]byte, 32))

func TestParseNativeAnnouncement(t *testing.T) {
	valid := "http://127.0.0.1:34567/?token=" + testToken
	parsed, origin, err := parseNativeAnnouncement(valid)
	require.NoError(t, err)
	assert.Equal(t, valid, parsed.String())
	assert.Equal(t, "http://127.0.0.1:34567", origin)
	for _, raw := range []string{
		"", "https://127.0.0.1:34567/?token=" + testToken, "http://example.com:34567/?token=" + testToken,
		"http://localhost:34567/?token=" + testToken, "http://127.0.0.1/?token=" + testToken,
		"http://user:password@127.0.0.1:34567/?token=" + testToken,
		"http://127.0.0.1:34567/api?token=" + testToken, valid + "#fragment", valid + "&other=1", valid + "&token=" + testToken,
		"http://127.0.0.1:0/?token=" + testToken, "http://127.0.0.1:99999/?token=" + testToken,
		"http://127.0.0.1:34567/?token=", "http://127.0.0.1:34567/?token=not-a-32-byte-token",
		"http://127.0.0.1:34567/%2f?token=" + testToken, "http://127.0.0.1:34567/?token=%xx",
	} {
		t.Run(raw, func(t *testing.T) {
			_, _, err := parseNativeAnnouncement(raw)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "password")
		})
	}
}

func TestAuthenticateNativeUsesHostBoundCookie(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/session/value" {
			assert.NotEmpty(t, r.Header.Get("Cookie"))
			assert.Empty(t, r.URL.Query().Get("token"))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		assert.Equal(t, testToken, r.URL.Query().Get("token"))
		assert.Equal(t, "/", r.URL.Path)
		u, err := url.Parse(server.URL)
		require.NoError(t, err)
		digest := sha256.Sum256([]byte(u.Host))
		http.SetCookie(w, &http.Cookie{Name: "dsh-auth-" + base64.RawURLEncoding.EncodeToString(digest[:]), Value: "private-session", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 100})
		w.Header().Set("Location", "./")
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer server.Close()
	endpoint, err := authenticateNative(context.Background(), server.URL+"/?token="+testToken)
	require.NoError(t, err)
	defer endpoint.Close()
	require.NoError(t, endpoint.Do(context.Background(), http.MethodPost, "/api/session/value", nil, nil))
}

func TestAuthenticateNativeRefusesInvalidCookieAndRedirect(t *testing.T) {
	for _, kind := range []string{"status", "redirect", "missing", "wrong-authority", "not-http-only", "not-strict", "domain", "wrong-path", "expired"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				digest := sha256.Sum256([]byte(r.Host))
				cookie := http.Cookie{Name: "dsh-auth-" + base64.RawURLEncoding.EncodeToString(digest[:]), Value: "private", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 1}
				switch kind {
				case "wrong-authority":
					cookie.Name = "dsh-auth-another"
				case "not-http-only":
					cookie.HttpOnly = false
				case "not-strict":
					cookie.SameSite = http.SameSiteLaxMode
				case "domain":
					cookie.Domain = "127.0.0.1"
				case "wrong-path":
					cookie.Path = "/api"
				case "expired":
					cookie.MaxAge = -1
				}
				if kind != "missing" {
					http.SetCookie(w, &cookie)
				}
				location := "./"
				if kind == "redirect" {
					location = "http://example.com/"
				}
				w.Header().Set("Location", location)
				status := http.StatusSeeOther
				if kind == "status" {
					status = http.StatusOK
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			endpoint, err := authenticateNative(context.Background(), server.URL+"/?token="+testToken)
			require.Error(t, err)
			assert.Nil(t, endpoint)
			assert.False(t, strings.Contains(err.Error(), testToken))
		})
	}
}

func TestAuthenticateNativeTransportErrorKeepsTokenPrivate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := authenticateNative(ctx, "http://127.0.0.1:34567/?token="+testToken)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), testToken)
	assert.ErrorIs(t, err, context.Canceled)
}
