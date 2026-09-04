package requestsource_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/hub/requestsource"
	"github.com/leapmux/leapmux/internal/hub/settings"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

type observedRequest struct {
	clientIP  string
	remote    string
	scheme    string
	tlsActive bool
}

func observeRequest(t *testing.T, trusted requestsource.TrustedRanges, configure func(*http.Request)) observedRequest {
	t.Helper()
	var observed observedRequest
	handler := requestsource.Middleware(nil, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		observed = observedRequest{
			clientIP:  peer.ClientIP(r.Context()),
			remote:    r.RemoteAddr,
			scheme:    r.URL.Scheme,
			tlsActive: r.TLS != nil,
		}
	}))

	request := httptest.NewRequest(http.MethodGet, "http://hub.example.test/", nil)
	configure(request)
	// A manager-less middleware uses no trusted ranges. To test explicit
	// ranges, exercise the same request through a manager below.
	if len(trusted.Selectors()) == 0 {
		handler.ServeHTTP(httptest.NewRecorder(), request)
		return observed
	}

	store := hubtestutil.OpenTestStore(t)
	manager := settings.NewManager(store, nil, requestsource.SettingsDescriptors())
	require.NoError(t, manager.Load(context.Background()))
	encoded, err := json.Marshal(trusted)
	require.NoError(t, err)
	require.NoError(t, manager.Update(context.Background(), requestsource.KeyTrustedProxyRanges, encoded))
	requestsource.Middleware(manager, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		observed = observedRequest{
			clientIP:  peer.ClientIP(r.Context()),
			remote:    r.RemoteAddr,
			scheme:    r.URL.Scheme,
			tlsActive: r.TLS != nil,
		}
	})).ServeHTTP(httptest.NewRecorder(), request)
	return observed
}

func mustTrusted(t *testing.T, selectors ...string) requestsource.TrustedRanges {
	t.Helper()
	trusted, err := requestsource.NewTrustedRanges(selectors)
	require.NoError(t, err)
	return trusted
}

func TestMiddleware_DirectPeerIgnoresForgedHeaders(t *testing.T) {
	t.Parallel()
	observed := observeRequest(t, requestsource.TrustedRanges{}, func(r *http.Request) {
		r.RemoteAddr = "203.0.113.9:4327"
		r.Header.Set("Forwarded", "for=198.51.100.1;proto=https")
		r.Header.Set("X-Forwarded-For", "198.51.100.2")
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	assert.Equal(t, "203.0.113.9", observed.clientIP)
	assert.Equal(t, "203.0.113.9:4327", observed.remote)
	assert.Equal(t, "http", observed.scheme)
}

func TestMiddleware_WalksTrustedProxyChainFromTheRight(t *testing.T) {
	t.Parallel()
	observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
		r.RemoteAddr = "10.0.0.3:4327"
		r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1, 10.0.0.2")
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	assert.Equal(t, "198.51.100.7", observed.clientIP)
	assert.Equal(t, "https", observed.scheme)
}

func TestMiddleware_UsesSelectedForwardedElementProtocol(t *testing.T) {
	t.Parallel()
	observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
		r.RemoteAddr = "10.0.0.3:4327"
		r.Header.Set("Forwarded", "for=198.51.100.7;proto=https, for=10.0.0.2;proto=http")
	})
	assert.Equal(t, "198.51.100.7", observed.clientIP)
	assert.Equal(t, "https", observed.scheme)
}

func TestMiddleware_ForwardedProtocolDefaultsToHTTP(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		protocol string
	}{
		{name: "absent"},
		{name: "empty", protocol: `;proto=""`},
		{name: "invalid", protocol: ";proto=ssh"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
				r.RemoteAddr = "10.0.0.3:4327"
				r.Header.Set("Forwarded", "for=198.51.100.7"+test.protocol+", for=10.0.0.2")
			})
			assert.Equal(t, "198.51.100.7", observed.clientIP)
			assert.Equal(t, "http", observed.scheme)
		})
	}
}

func TestMiddleware_AcceptsQuotedForwardedIPv6(t *testing.T) {
	t.Parallel()
	observed := observeRequest(t, mustTrusted(t, "2001:db8:1::/64"), func(r *http.Request) {
		r.RemoteAddr = "[2001:db8:1::3]:4327"
		r.Header.Set("Forwarded", `for="[2001:db8:2::7]:4711";proto="HTTPS", for="[2001:db8:1::2]"`)
	})
	assert.Equal(t, "2001:db8:2::7", observed.clientIP)
	assert.Equal(t, "https", observed.scheme)
}

func TestMiddleware_ForwardedParserKeepsQuotedDelimitersInsideAnElement(t *testing.T) {
	t.Parallel()
	observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
		r.RemoteAddr = "10.0.0.3:4327"
		r.Header.Set("Forwarded", `for=198.51.100.7;host="hub,edge;one";proto=https, for=10.0.0.2`)
	})
	assert.Equal(t, "198.51.100.7", observed.clientIP)
	assert.Equal(t, "https", observed.scheme)
}

func TestMiddleware_PrefersForwardedWithoutFallback(t *testing.T) {
	t.Parallel()
	observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
		r.RemoteAddr = "10.0.0.3:4327"
		r.Header.Set("Forwarded", `for="[2001:db8::1]`)
		r.Header.Set("X-Forwarded-For", "198.51.100.7")
		r.Header.Set("X-Forwarded-Proto", "https")
	})
	assert.Empty(t, observed.clientIP)
	assert.Equal(t, "http", observed.scheme)
}

func TestMiddleware_MalformedXForwardedForProducesUnknownClient(t *testing.T) {
	t.Parallel()
	observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
		r.RemoteAddr = "10.0.0.3:4327"
		r.Header.Set("X-Forwarded-For", "198.51.100.7, not-an-address")
	})
	assert.Empty(t, observed.clientIP)
	assert.Equal(t, "http", observed.scheme)
}

func TestMiddleware_AllTrustedChainProducesUnknownClient(t *testing.T) {
	t.Parallel()
	observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
		r.RemoteAddr = "10.0.0.3:4327"
		r.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	})
	assert.Empty(t, observed.clientIP)
}

func TestMiddleware_XForwardedProtocolAlignment(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		protocol string
		want     string
	}{
		{"global", "https", "https"},
		{"aligned", "https, http", "https"},
		{"misaligned", "https, http, https", "http"},
		{"invalid", "ssh", "http"},
		{"empty", "", "http"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
				r.RemoteAddr = "10.0.0.3:4327"
				r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.2")
				if test.protocol != "" {
					r.Header.Set("X-Forwarded-Proto", test.protocol)
				}
			})
			assert.Equal(t, "198.51.100.7", observed.clientIP)
			assert.Equal(t, test.want, observed.scheme)
		})
	}
}

func TestMiddleware_PreservesActualTLS(t *testing.T) {
	t.Parallel()
	observed := observeRequest(t, requestsource.TrustedRanges{}, func(r *http.Request) {
		r.RemoteAddr = "203.0.113.9:4327"
		r.TLS = &tls.ConnectionState{}
	})
	assert.Equal(t, "https", observed.scheme)
	assert.True(t, observed.tlsActive)
}

func TestMiddleware_ProviderRangesTrustTheirPeers(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"cloudflare", "cloudfront"} {
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			trusted := mustTrusted(t, token)
			peerAddress := trusted.Prefixes()[0].Addr()
			observed := observeRequest(t, trusted, func(r *http.Request) {
				r.RemoteAddr = netip.AddrPortFrom(peerAddress, 4327).String()
				r.Header.Set("X-Forwarded-For", "198.51.100.7")
			})
			assert.Equal(t, "198.51.100.7", observed.clientIP)
		})
	}
}

func TestMiddleware_AppliesHotSettingChanges(t *testing.T) {
	store := hubtestutil.OpenTestStore(t)
	manager := settings.NewManager(store, nil, requestsource.SettingsDescriptors())
	require.NoError(t, manager.Load(context.Background()))

	var clientIP string
	handler := requestsource.Middleware(manager, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		clientIP = peer.ClientIP(r.Context())
	}))
	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://hub.example.test/", nil)
		r.RemoteAddr = "10.0.0.3:4327"
		r.Header.Set("X-Forwarded-For", "198.51.100.7")
		return r
	}

	handler.ServeHTTP(httptest.NewRecorder(), request())
	assert.Equal(t, "10.0.0.3", clientIP)

	require.NoError(t, manager.Update(context.Background(), requestsource.KeyTrustedProxyRanges,
		json.RawMessage(`["10.0.0.0/8"]`)))
	handler.ServeHTTP(httptest.NewRecorder(), request())
	assert.Equal(t, "198.51.100.7", clientIP)
}
