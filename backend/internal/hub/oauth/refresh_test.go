package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/stretchr/testify/assert"
)

func TestTokenSet_StringRedacted(t *testing.T) {
	ts := TokenSet{
		AccessToken:  "super-secret-access",
		RefreshToken: "super-secret-refresh",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(1 * time.Hour).UTC(),
	}

	assert.Equal(t, "[REDACTED TokenSet]", ts.String())
	assert.Equal(t, "[REDACTED TokenSet]", ts.GoString())
	assert.NotContains(t, ts.String(), "super-secret")
}

func TestUserClaims_DoesNotContainTokens(t *testing.T) {
	// If a token field is ever added to UserClaims, this reflection-based
	// check fails — the type system guarantee in provider.go is enforced
	// for future maintainers, not just by today's struct definition.
	typ := reflect.TypeOf(UserClaims{})
	for i := range typ.NumField() {
		name := strings.ToLower(typ.Field(i).Name)
		assert.NotContains(t, name, "token", "UserClaims must not contain token fields, got %q", typ.Field(i).Name)
		assert.NotContains(t, name, "secret", "UserClaims must not contain secret fields, got %q", typ.Field(i).Name)
		assert.NotContains(t, name, "password", "UserClaims must not contain password fields, got %q", typ.Field(i).Name)
	}
}

func TestProvidersClassifyRefreshResponses(t *testing.T) {
	for _, provider := range []string{"github", "oidc"} {
		for _, tc := range []struct {
			name     string
			status   int
			body     string
			rejected bool
		}{
			{"invalid grant", 400, `{"error":"invalid_grant"}`, true},
			{"github rejected", 200, `{"error":"bad_refresh_token"}`, false},
			{"github rejected bad request", 400, `{"error":"bad_refresh_token"}`, false},
			{"throttled", 429, `{"error":"bad_refresh_token"}`, false},
			{"outage", 503, `{"error":"invalid_grant"}`, false},
			{"github outage", 503, `{"error":"bad_refresh_token"}`, false},
			{"invalid client", 400, `{"error":"invalid_client"}`, false},
			{"unknown", 400, `{"error":"unknown"}`, false},
			{"malformed", 400, `invalid`, false},
			{"success", 200, `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"bearer","expires_in":3600}`, false},
		} {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					require.NoError(t, r.ParseForm())
					assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
					assert.Equal(t, "old-refresh", r.Form.Get("refresh_token"))
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = fmt.Fprint(w, tc.body)
				}))
				defer srv.Close()
				cfg := &oauth2.Config{ClientID: "cid", ClientSecret: "secret", Endpoint: oauth2.Endpoint{TokenURL: srv.URL, AuthStyle: oauth2.AuthStyleInParams}}
				var refresh func(context.Context, string) (*TokenSet, error)
				if provider == "github" {
					refresh = (&GitHubProvider{oauth2Config: cfg}).Refresh
				} else {
					refresh = (&OIDCProvider{oauth2Config: cfg}).Refresh
				}
				tokens, err := refresh(t.Context(), "old-refresh")
				if tc.name == "success" {
					require.NoError(t, err)
					assert.Equal(t, "new-access", tokens.AccessToken)
					assert.Equal(t, "new-refresh", tokens.RefreshToken)
					assert.Equal(t, "bearer", tokens.TokenType)
					assert.WithinDuration(t, time.Now().Add(time.Hour), tokens.ExpiresAt, time.Minute)
					return
				}
				require.Error(t, err)
				assert.Nil(t, tokens)
				rejected := tc.rejected || provider == "github" && (tc.name == "github rejected" || tc.name == "github rejected bad request")
				if rejected {
					assert.ErrorIs(t, err, ErrRefreshTokenRejected)
				} else {
					assert.NotErrorIs(t, err, ErrRefreshTokenRejected)
				}
				var original *oauth2.RetrieveError
				if tc.name != "malformed" {
					require.ErrorAs(t, err, &original)
					assert.Equal(t, tc.status, original.Response.StatusCode)
				}
			})
		}
	}
}
