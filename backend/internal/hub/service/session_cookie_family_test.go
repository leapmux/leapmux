package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/requestsource"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

func TestSessionCookieFamilyPolicyTransition(t *testing.T) {
	for _, transition := range []struct {
		name                                         string
		initial, configured, expectedSecure, trusted bool
	}{
		{"secure to plain", true, false, false, false}, {"plain to secure", false, true, true, false}, {"trusted HTTPS remains secure", true, false, true, true},
	} {
		t.Run(transition.name, func(t *testing.T) {
			for _, logout := range []bool{false, true} {
				t.Run(map[bool]string{false: "direct relogin", true: "logout then relogin"}[logout], func(t *testing.T) {
					st := hubtestutil.OpenTestStore(t)
					hubtestutil.CreateTestAdmin(t, st)
					admin, err := st.Users().GetByUsername(t.Context(), hubtestutil.TestAdminUsername)
					require.NoError(t, err)
					const nextUsername = "next-account"
					require.NoError(t, st.Users().Create(t.Context(), store.CreateUserParams{
						ID: id.Generate(), Username: nextUsername, PasswordHash: admin.PasswordHash, FirstCredentialExempt: true,
					}))
					set := servicetest.NewSettingsManager(t, st, nil)
					require.NoError(t, settings.KeySecureCookies.Set(t.Context(), set, transition.initial))
					if transition.trusted {
						require.NoError(t, set.Update(t.Context(), requestsource.KeyTrustedProxyRanges, json.RawMessage(`["127.0.0.1/32","::1/128"]`)))
					}
					interceptor, registry := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st, SessionHasher: hubtestutil.TestSecretHasher(), Policy: func(ctx context.Context) auth.Policy { return auth.PolicyFromSettings(ctx, set.Snapshot(ctx)) }})
					t.Cleanup(registry.Stop)
					svc := service.NewAuthService(servicetest.AuthServiceDeps(st, testConfig(), set, auth.NewCredentialLifecycleEffects(registry, nil, nil)))
					server := connect.NewServer(interceptor)
					mountAuthAndSystem(server, svc, set)
					mux := http.NewServeMux()
					connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(0))
					upstream := httptest.NewServer(requestsource.Middleware(requestsource.RangesFromSettings(set), mux))
					t.Cleanup(upstream.Close)
					target, err := url.Parse(upstream.URL)
					require.NoError(t, err)
					forward := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
						r.SetURL(target)
						r.SetXForwarded()
						// Model a client outside the proxy ranges; the local test client
						// shares its loopback address with the trusted proxy.
						r.Out.Header.Set("X-Forwarded-For", "198.51.100.7")
					}}
					proxy := httptest.NewTLSServer(forward)
					t.Cleanup(proxy.Close)
					client := proxy.Client()
					client.Jar, err = cookiejar.New(nil)
					require.NoError(t, err)
					browser := newAuthTestClient(client, proxy.URL)
					origin, err := url.Parse(proxy.URL)
					require.NoError(t, err)
					login := func(username string, secure bool) string {
						callCtx, callInfo := connect.NewClientContext(t.Context())
						_, err := browser.Login(callCtx, &leapmuxv1.LoginRequest{Username: username, Password: hubtestutil.TestAdminPassword})
						require.NoError(t, err)
						activeName := auth.CookieName
						if secure {
							activeName = auth.SecureCookieName
						}
						for _, line := range callInfo.ResponseHeader().Values("Set-Cookie") {
							cookie, parseErr := http.ParseSetCookie(line)
							require.NoError(t, parseErr)
							if cookie.Name == activeName && cookie.Value != "" {
								assert.Equal(t, secure, cookie.Secure)
								return cookie.Value
							}
						}
						t.Fatal("the response did not issue the expected active session cookie")
						return ""
					}
					first := login(hubtestutil.TestAdminUsername, transition.initial || transition.trusted)
					_, err = browser.GetCurrentUser(t.Context(), &leapmuxv1.GetCurrentUserRequest{})
					require.NoError(t, err)
					require.NoError(t, settings.KeySecureCookies.Set(t.Context(), set, transition.configured))
					if logout {
						_, err = browser.Logout(t.Context(), &leapmuxv1.LogoutRequest{})
						if !transition.initial && transition.expectedSecure {
							// Secure policy deliberately refuses the old plain spelling.
							require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
						} else {
							require.NoError(t, err)
							sessionID, _, ok := auth.ParseSessionToken(first)
							require.True(t, ok)
							_, err = st.Sessions().GetByID(t.Context(), sessionID, time.Now().UTC())
							require.ErrorIs(t, err, store.ErrNotFound)
						}
					}
					second := login(nextUsername, transition.expectedSecure)
					require.True(t, first != second, "a new login must issue a different session")
					alternate := auth.SecureCookieName
					if transition.expectedSecure {
						alternate = auth.CookieName
					}
					for _, cookie := range client.Jar.Cookies(origin) {
						assert.NotEqual(t, alternate, cookie.Name, "the alternate spelling must not shadow the new session")
					}
					current, err := browser.GetCurrentUser(t.Context(), &leapmuxv1.GetCurrentUserRequest{})
					require.NoError(t, err, "the new login must authenticate after the cookie policy changes")
					assert.Equal(t, nextUsername, current.GetSessionReport().GetUser().GetUsername())
				})
			}
		})
	}
}
