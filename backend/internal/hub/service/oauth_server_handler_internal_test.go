package service

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"

	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

func TestConsentRouteMatchesEveryRegisteredGETRoute(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	handler := &OAuthServerHandler{sessionHasher: hubtestutil.TestSecretHasher()}
	handler.RegisterRoutes(mux)
	for _, route := range oauthConsentRoutes {
		t.Run(route.path, func(t *testing.T) {
			allowsGET := slices.Contains(route.methods, http.MethodGet)
			for _, suffix := range []string{"", "?client_id=abc", "#done"} {
				target := route.path + suffix
				assert.Equal(t, allowsGET, isConsentRoute(target), target)
			}

			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, route.path, nil))
			if allowsGET {
				assert.Equal(t, http.StatusFound, response.Code, "a GET consent route starts by requiring sign-in")
			} else {
				assert.Equal(t, http.StatusMethodNotAllowed, response.Code, "the route must refuse GET")
			}

			for _, method := range route.methods {
				if method == http.MethodGet {
					continue
				}
				t.Run(method, func(t *testing.T) {
					response := httptest.NewRecorder()
					mux.ServeHTTP(response, httptest.NewRequest(method, route.path, nil))
					want := http.StatusUnauthorized
					if method == http.MethodHead {
						want = http.StatusFound
					}
					assert.Equal(t, want, response.Code, "the route must accept this method and require sign-in")
				})
			}
		})
	}
}

func TestConsentRouteRejectsOtherPaths(t *testing.T) {
	t.Parallel()

	for _, redirect := range []string{
		"/oauth/authorizeX",
		"/oauth/authorize/extra",
		"/oauth/authorize/",
		"/oauth/token",
		"/",
		"",
		"/login?to=/oauth/authorize",
	} {
		assert.False(t, isConsentRoute(redirect), redirect)
	}
}

func TestConsentRouteMatchesEscapedPathSegments(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	new(OAuthServerHandler).RegisterRoutes(mux)
	getRoutes := make(map[string]bool, len(oauthConsentRoutes))
	for _, route := range oauthConsentRoutes {
		getRoutes[route.path] = slices.Contains(route.methods, http.MethodGet)
	}
	tests := []struct {
		path        string
		wantPattern string
	}{
		{"/oauth/%61uthorize?client_id=abc", "/oauth/authorize"},
		{"/%6Fauth/devi%63e", "/oauth/device"},
		{"/oauth/%63onsent", "/oauth/consent"},
		{"/oauth%2Fauthorize", ""},
		{"/oauth%2fdevice", ""},
		{"/oauth/%2Fauthorize", ""},
		{"/oauth/authorize%2F", ""},
		{"/oauth/%2561uthorize", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			_, pattern := mux.Handler(req)
			assert.Equal(t, tt.wantPattern, pattern)
			assert.Equal(t, getRoutes[tt.wantPattern], isConsentRoute(tt.path))
		})
	}
	assert.False(t, isConsentRoute("/oauth/%6zuthorize"))
}
