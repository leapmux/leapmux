package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestGitHub_AuthURL_IncludesStateAndScopes(t *testing.T) {
	p := NewGitHubProvider("gh-client-id", "gh-secret", "http://localhost/callback", []string{"read:user", "user:email"})

	url := p.AuthURL("test-state", "unused-challenge")

	assert.Contains(t, url, "state=test-state")
	assert.Contains(t, url, "client_id=gh-client-id")
	assert.Contains(t, url, "scope=read")
}

// mockGitHubAPI creates a test server that handles /user and /user/emails.
func mockGitHubAPI(t *testing.T, user map[string]interface{}, emails []map[string]interface{}) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(user)
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(emails)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHub_Exchange_Success(t *testing.T) {
	// Mock GitHub token endpoint.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "gh-access-token",
			"token_type":   "bearer",
		})
	}))
	t.Cleanup(tokenSrv.Close)

	// Mock GitHub API with verified primary email.
	apiSrv := mockGitHubAPI(t,
		map[string]interface{}{"id": 42, "login": "octocat", "name": "The Octocat"},
		[]map[string]interface{}{
			{"email": "octocat@github.com", "primary": true, "verified": true},
			{"email": "other@example.com", "primary": false, "verified": true},
		},
	)

	p := &GitHubProvider{
		oauth2Config: &oauth2.Config{
			ClientID:     "gh-client-id",
			ClientSecret: "gh-secret",
			RedirectURL:  "http://localhost/callback",
			Endpoint:     oauth2.Endpoint{TokenURL: tokenSrv.URL},
			Scopes:       []string{"read:user"},
		},
	}

	tokenSet, claims, err := p.exchangeWithBaseURL(context.Background(), "valid-code", apiSrv.URL)
	require.NoError(t, err)

	assert.Equal(t, "gh-access-token", tokenSet.AccessToken)
	assert.Equal(t, "bearer", tokenSet.TokenType)
	assert.Equal(t, "42", claims.Subject)
	assert.Equal(t, "octocat", claims.Name)
	assert.Equal(t, "The Octocat", claims.DisplayName)
	assert.Equal(t, "octocat@github.com", claims.Email)
}

func TestGitHub_Exchange_UnverifiedEmail(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "gh-access-token",
			"token_type":   "bearer",
		})
	}))
	t.Cleanup(tokenSrv.Close)

	// Primary email is not verified.
	apiSrv := mockGitHubAPI(t,
		map[string]interface{}{"id": 42, "login": "octocat", "name": "The Octocat"},
		[]map[string]interface{}{
			{"email": "unverified@example.com", "primary": true, "verified": false},
		},
	)

	p := &GitHubProvider{
		oauth2Config: &oauth2.Config{
			ClientID:     "gh-client-id",
			ClientSecret: "gh-secret",
			Endpoint:     oauth2.Endpoint{TokenURL: tokenSrv.URL},
		},
	}

	_, claims, err := p.exchangeWithBaseURL(context.Background(), "valid-code", apiSrv.URL)
	require.NoError(t, err)
	assert.Empty(t, claims.Email, "unverified email must not be returned")
}

func TestGitHub_Exchange_NoEmails(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "gh-access-token",
			"token_type":   "bearer",
		})
	}))
	t.Cleanup(tokenSrv.Close)

	// No emails returned.
	apiSrv := mockGitHubAPI(t,
		map[string]interface{}{"id": 42, "login": "octocat", "name": "The Octocat"},
		[]map[string]interface{}{},
	)

	p := &GitHubProvider{
		oauth2Config: &oauth2.Config{
			ClientID:     "gh-client-id",
			ClientSecret: "gh-secret",
			Endpoint:     oauth2.Endpoint{TokenURL: tokenSrv.URL},
		},
	}

	_, claims, err := p.exchangeWithBaseURL(context.Background(), "valid-code", apiSrv.URL)
	require.NoError(t, err)
	assert.Empty(t, claims.Email)
}

func TestGitHub_Exchange_InvalidCode(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
	}))
	t.Cleanup(tokenSrv.Close)

	p := &GitHubProvider{
		oauth2Config: &oauth2.Config{
			ClientID:     "gh-client-id",
			ClientSecret: "gh-secret",
			Endpoint:     oauth2.Endpoint{TokenURL: tokenSrv.URL},
		},
	}

	_, _, err := p.Exchange(context.Background(), "invalid-code", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github exchange")
}
