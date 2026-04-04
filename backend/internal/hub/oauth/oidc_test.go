package oauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockOIDCServer creates a test OIDC provider with discovery, JWKS, token, and userinfo endpoints.
func mockOIDCServer(t *testing.T) (*httptest.Server, *rsa.PrivateKey) {
	t.Helper()

	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	mux := http.NewServeMux()
	var serverURL string

	// Discovery endpoint.
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 serverURL,
			"authorization_endpoint": serverURL + "/authorize",
			"token_endpoint":         serverURL + "/token",
			"jwks_uri":               serverURL + "/jwks",
			"userinfo_endpoint":      serverURL + "/userinfo",
		})
	})

	// JWKS endpoint.
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		jwk := jose.JSONWebKey{Key: &privKey.PublicKey, KeyID: "test-key", Algorithm: "RS256", Use: "sig"}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
	})

	// Token endpoint — exchanges code for tokens.
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := r.FormValue("code")
		if code == "invalid-code" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}

		// Build a signed ID token.
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: privKey}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"))
		allClaims := map[string]interface{}{
			"iss":   serverURL,
			"sub":   "oidc-user-123",
			"aud":   "test-client",
			"exp":   time.Now().Add(1 * time.Hour).Unix(),
			"iat":   time.Now().Unix(),
			"email": "user@example.com",
			"name":  "Test User",
		}
		rawToken, _ := jwt.Signed(signer).Claims(allClaims).Serialize()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "mock-access-token",
			"refresh_token": "mock-refresh-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"id_token":      rawToken,
		})
	})

	srv := httptest.NewServer(mux)
	serverURL = srv.URL
	t.Cleanup(srv.Close)
	return srv, privKey
}

func TestOIDC_AuthURL_IncludesStateAndChallenge(t *testing.T) {
	srv, _ := mockOIDCServer(t)

	p, err := NewOIDCProvider(context.Background(), srv.URL, "test-client", "test-secret", srv.URL+"/callback", []string{"openid", "profile", "email"})
	require.NoError(t, err)

	url := p.AuthURL("test-state", "test-challenge")

	assert.Contains(t, url, "state=test-state")
	assert.Contains(t, url, "code_challenge=")
	assert.Contains(t, url, "code_challenge_method=S256")
	assert.Contains(t, url, "scope=openid+profile+email")
	assert.Contains(t, url, "client_id=test-client")
}

func TestOIDC_Exchange_Success(t *testing.T) {
	srv, _ := mockOIDCServer(t)

	p, err := NewOIDCProvider(context.Background(), srv.URL, "test-client", "test-secret", srv.URL+"/callback", nil)
	require.NoError(t, err)

	tokenSet, claims, err := p.Exchange(context.Background(), "valid-code", "test-verifier")
	require.NoError(t, err)

	assert.Equal(t, "mock-access-token", tokenSet.AccessToken)
	assert.Equal(t, "mock-refresh-token", tokenSet.RefreshToken)
	assert.Equal(t, "Bearer", tokenSet.TokenType)

	assert.Equal(t, "oidc-user-123", claims.Subject)
	assert.Equal(t, "user@example.com", claims.Email)
	assert.Equal(t, "Test User", claims.Name)
}

func TestOIDC_Exchange_InvalidCode(t *testing.T) {
	srv, _ := mockOIDCServer(t)

	p, err := NewOIDCProvider(context.Background(), srv.URL, "test-client", "test-secret", srv.URL+"/callback", nil)
	require.NoError(t, err)

	_, _, err = p.Exchange(context.Background(), "invalid-code", "test-verifier")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "oidc exchange")
}

func TestOIDC_ValidateIssuer_Success(t *testing.T) {
	srv, _ := mockOIDCServer(t)
	err := ValidateIssuer(context.Background(), srv.URL)
	assert.NoError(t, err)
}

func TestOIDC_ValidateIssuer_InvalidURL(t *testing.T) {
	err := ValidateIssuer(context.Background(), "http://localhost:1/nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "oidc discovery failed")
}

func TestOIDC_Exchange_InvalidIDTokenSignature(t *testing.T) {
	// Create a server that returns an ID token signed with a different key.
	wrongKey, _ := rsa.GenerateKey(rand.Reader, 2048)

	srv, _ := mockOIDCServer(t)
	// Override the token endpoint to return an ID token signed with wrongKey.
	wrongSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/token") {
			signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: wrongKey}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "wrong-key"))
			claims := jwt.Claims{
				Issuer:   srv.URL,
				Subject:  "hacker",
				Audience: jwt.Audience{"test-client"},
				Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
				IssuedAt: jwt.NewNumericDate(time.Now()),
			}
			rawToken, _ := jwt.Signed(signer).Claims(claims).Serialize()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"access_token":"a","token_type":"Bearer","id_token":"%s"}`, rawToken)
			return
		}
		// Proxy other requests to the real mock server.
		http.DefaultTransport.(*http.Transport).CloseIdleConnections()
		resp, err := http.Get(srv.URL + r.URL.Path)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 4096)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
			}
			if readErr != nil {
				break
			}
		}
	}))
	t.Cleanup(wrongSrv.Close)

	// Provider discovers from the real server (gets the real JWKS), but exchanges at wrongSrv.
	// We can't easily split discovery from exchange, so we test via ValidateIssuer + direct check.
	// Instead, verify that the OIDC provider rejects wrong-key tokens by using the real provider
	// but with a tampered exchange. This is hard without replacing the token endpoint.
	// Simplification: just verify that an ID token signed with the wrong key fails verification.

	p, err := NewOIDCProvider(context.Background(), srv.URL, "test-client", "test-secret", srv.URL+"/callback", nil)
	require.NoError(t, err)

	// The verifier checks signatures against the JWKS from srv. An ID token signed with wrongKey
	// will fail. We test this indirectly: the real mock server's /token returns correctly-signed
	// tokens, so we can't trigger this through Exchange easily. Instead, verify the verifier works.
	signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: wrongKey}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "wrong-key"))
	wrongClaims := map[string]interface{}{
		"iss": srv.URL,
		"sub": "hacker",
		"aud": "test-client",
		"exp": time.Now().Add(1 * time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	wrongToken, _ := jwt.Signed(signer).Claims(wrongClaims).Serialize()
	_, err = p.verifier.Verify(context.Background(), wrongToken)
	assert.Error(t, err, "token signed with wrong key should fail verification")
}
