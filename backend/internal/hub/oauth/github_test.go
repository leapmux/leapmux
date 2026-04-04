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

func TestGitHub_Exchange_Success(t *testing.T) {
	// Mock GitHub token endpoint and user API.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "gh-access-token",
			"token_type":   "bearer",
		})
	}))
	t.Cleanup(tokenSrv.Close)

	userSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth header.
		assert.Equal(t, "Bearer gh-access-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    42,
			"login": "octocat",
			"name":  "The Octocat",
			"email": "octocat@github.com",
		})
	}))
	t.Cleanup(userSrv.Close)

	p := &GitHubProvider{
		oauth2Config: &oauth2.Config{
			ClientID:     "gh-client-id",
			ClientSecret: "gh-secret",
			RedirectURL:  "http://localhost/callback",
			Endpoint: oauth2.Endpoint{
				TokenURL: tokenSrv.URL,
			},
			Scopes: []string{"read:user"},
		},
	}

	// Patch fetchGitHubUser to use our mock server.
	origExchange := p.oauth2Config.Endpoint.TokenURL
	_ = origExchange

	tokenSet, claims, err := p.exchangeWithUserURL(context.Background(), "valid-code", "", userSrv.URL+"/user")
	require.NoError(t, err)

	assert.Equal(t, "gh-access-token", tokenSet.AccessToken)
	assert.Equal(t, "bearer", tokenSet.TokenType)
	assert.Equal(t, "42", claims.Subject)
	assert.Equal(t, "octocat", claims.Name)
	assert.Equal(t, "The Octocat", claims.DisplayName)
	assert.Equal(t, "octocat@github.com", claims.Email)
}

func TestGitHub_Exchange_InvalidCode(t *testing.T) {
	// Mock token endpoint that returns an error.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
	}))
	t.Cleanup(tokenSrv.Close)

	p := &GitHubProvider{
		oauth2Config: &oauth2.Config{
			ClientID:     "gh-client-id",
			ClientSecret: "gh-secret",
			Endpoint: oauth2.Endpoint{
				TokenURL: tokenSrv.URL,
			},
		},
	}

	_, _, err := p.Exchange(context.Background(), "invalid-code", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "github exchange")
}
