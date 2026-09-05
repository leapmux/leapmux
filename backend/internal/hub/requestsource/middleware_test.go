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
	clientIP string
	remote   string
	// scheme is the VERIFIED protocol, read from the context where the
	// middleware records it. It is not `r.URL.Scheme`: the middleware leaves
	// the URL alone, because nothing in the hub read the scheme there.
	scheme    string
	tlsActive bool
}

func observeRequest(t *testing.T, trusted requestsource.TrustedRanges, configure func(*http.Request)) observedRequest {
	t.Helper()
	var observed observedRequest
	// The trust set is stated DIRECTLY. The middleware takes the one value it
	// reads rather than a settings manager, so a test that only needs a trust
	// set does not open a store to say so. TestMiddleware_AppliesHotSettingChanges
	// covers the settings-backed path.
	ranges := func(context.Context) requestsource.TrustedRanges { return trusted }

	request := httptest.NewRequest(http.MethodGet, "http://hub.example.test/", nil)
	configure(request)
	requestsource.Middleware(ranges, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		observed = observedRequest{
			clientIP:  peer.ClientIP(r.Context()),
			remote:    r.RemoteAddr,
			scheme:    observedScheme(r),
			tlsActive: r.TLS != nil,
		}
	})).ServeHTTP(httptest.NewRecorder(), request)
	return observed
}

// observedScheme renders the recorded protocol the way these tests spell it.
func observedScheme(r *http.Request) string {
	if peer.IsHTTPS(r.Context()) {
		return "https"
	}
	return "http"
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

// Each proxy in a chain may append its OWN header line rather than extend the
// one before it. net/http keeps those lines apart, and RFC 9110 says the two
// forms mean the same thing, so the chain must read the same either way.
func TestMiddleware_ReadsRepeatedHeaderLinesAsOneChain(t *testing.T) {
	t.Parallel()
	t.Run("X-Forwarded-For and X-Forwarded-Proto", func(t *testing.T) {
		t.Parallel()
		observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
			r.RemoteAddr = "10.0.0.3:4327"
			r.Header.Add("X-Forwarded-For", "198.51.100.7")
			r.Header.Add("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
			r.Header.Add("X-Forwarded-Proto", "https")
			r.Header.Add("X-Forwarded-Proto", "http, http")
		})
		assert.Equal(t, "198.51.100.7", observed.clientIP)
		assert.Equal(t, "https", observed.scheme,
			"three protocols align with three addresses, and the client sits first")
	})

	t.Run("Forwarded", func(t *testing.T) {
		t.Parallel()
		observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
			r.RemoteAddr = "10.0.0.3:4327"
			r.Header.Add("Forwarded", "for=198.51.100.7;proto=https")
			r.Header.Add("Forwarded", "for=10.0.0.2;proto=http")
		})
		assert.Equal(t, "198.51.100.7", observed.clientIP)
		assert.Equal(t, "https", observed.scheme)
	})
}

// A repeated parameter inside one Forwarded element is REFUSED, never
// resolved. RFC 7239 forbids it, and two `for` values in one element ask the
// parser to pick which client address the sender meant -- so a parser that
// picked would let whoever writes the second value choose the answer.
func TestMiddleware_RefusesARepeatedForwardedParameter(t *testing.T) {
	t.Parallel()
	for _, header := range []string{
		`for=198.51.100.7;for=198.51.100.8`,
		`for=198.51.100.7;proto=https;proto=http`,
		// Case-folded: the parameter name is case-insensitive, so `For` and
		// `for` are one name and not two.
		`For=198.51.100.7;for=198.51.100.8`,
	} {
		t.Run(header, func(t *testing.T) {
			t.Parallel()
			observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
				r.RemoteAddr = "10.0.0.3:4327"
				r.Header.Set("Forwarded", header)
			})
			assert.Empty(t, observed.clientIP)
			assert.Equal(t, "http", observed.scheme)
		})
	}
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
	for _, test := range []struct {
		name  string
		lines []string
	}{
		{"a value that is not an address", []string{"198.51.100.7, not-an-address"}},
		{"an empty element", []string{"198.51.100.7, , 10.0.0.1"}},
		// One EMPTY line among several. Each line is its own list, so an empty
		// one is an empty list and not a separator that the join swallows.
		{"an empty repeated line", []string{"198.51.100.7", "", "10.0.0.1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
				r.RemoteAddr = "10.0.0.3:4327"
				for _, line := range test.lines {
					r.Header.Add("X-Forwarded-For", line)
				}
			})
			assert.Empty(t, observed.clientIP)
			assert.Equal(t, "http", observed.scheme)
		})
	}
}

func TestMiddleware_AllTrustedChainProducesUnknownClient(t *testing.T) {
	t.Parallel()
	observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
		r.RemoteAddr = "10.0.0.3:4327"
		r.Header.Set("X-Forwarded-For", "10.0.0.1, 10.0.0.2")
	})
	assert.Empty(t, observed.clientIP)
}

// absentProtocol marks the subtest that sends NO X-Forwarded-Proto. An empty
// string cannot mark it, because an empty string is itself an input this table
// exercises.
const absentProtocol = "\x00absent"

const xForwardedProtocolHeaderName = "X-Forwarded-Proto"

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
		// PRESENT and empty, which is not the same input as absent: the
		// present one reaches splitElements, whose "empty header element"
		// refusal is what produces the fallback. Setting the header through
		// the map is the only way to send it, because Header.Set with an
		// empty value would be indistinguishable from the absent case.
		{"present but empty", "", "http"},
		{"absent", absentProtocol, "http"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
				r.RemoteAddr = "10.0.0.3:4327"
				r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.2")
				if test.protocol != absentProtocol {
					r.Header[xForwardedProtocolHeaderName] = []string{test.protocol}
				}
			})
			assert.Equal(t, "198.51.100.7", observed.clientIP)
			assert.Equal(t, test.want, observed.scheme)
		})
	}
}

// The middleware hands the next handler its own Request and its own URL. It
// must not write the effective scheme back onto the caller's request: net/http
// owns that value, and the URL is the one field this middleware changes, so a
// shared URL would leak the rewrite out of the handler chain.
func TestMiddleware_LeavesTheCallersRequestAlone(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "http://hub.example.test/path?q=1", nil)
	request.RemoteAddr = "203.0.113.9:4327"
	request.TLS = &tls.ConnectionState{}
	originalURL := request.URL
	originalScheme := request.URL.Scheme

	var seen *http.Request
	requestsource.Middleware(requestsource.RangesFromSettings(nil), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r
	})).ServeHTTP(httptest.NewRecorder(), request)

	require.NotNil(t, seen)
	// The URL is UNCHANGED and unshared-by-nobody: the middleware no longer
	// rewrites it. The verified protocol lives on the context, which is where
	// the cookie policy and the base-URL builder read it, so there is no
	// per-request URL copy to make and no field of the request to write.
	assert.Same(t, originalURL, seen.URL, "the middleware leaves the URL alone")
	assert.Equal(t, originalScheme, seen.URL.Scheme, "the URL scheme is not the hub's answer")
	assert.Equal(t, "/path", seen.URL.Path)
	assert.Equal(t, "q=1", seen.URL.RawQuery)
	assert.True(t, peer.IsHTTPS(seen.Context()), "the handler reads the effective protocol from the context")
	assert.Equal(t, "203.0.113.9:4327", seen.RemoteAddr, "RemoteAddr stays the physical peer")
	assert.NotNil(t, seen.TLS, "Request.TLS is untouched")
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
	handler := requestsource.Middleware(requestsource.RangesFromSettings(manager), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
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

// The chain walk is this package's own, so these pin the rule it implements
// rather than the library's behaviour it used to delegate to.
func TestMiddleware_RightmostUntrustedSelection(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		header   string
		clientIP string
	}{
		// The rightmost address that is NOT a trusted proxy. Everything to its
		// right is infrastructure the operator vouched for.
		{"stops at the first untrusted hop", "203.0.113.1, 198.51.100.9, 10.0.0.2", "198.51.100.9"},
		{"a single untrusted hop", "198.51.100.9, 10.0.0.2", "198.51.100.9"},
		// A REPEATED address cannot move the answer: every element to the
		// right of the pick is trusted, so a trusted element can never hold
		// the untrusted address.
		{"a repeated address", "198.51.100.9, 10.0.0.5, 198.51.100.9, 10.0.0.2", "198.51.100.9"},
		// Trusted end to end names no client. Reporting the leftmost proxy
		// would report infrastructure as a person.
		{"an all-trusted chain", "10.0.0.9, 10.0.0.2", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
				r.RemoteAddr = "10.0.0.3:4327"
				r.Header.Set("X-Forwarded-For", test.header)
			})
			assert.Equal(t, test.clientIP, observed.clientIP)
		})
	}
}

// An OBFUSCATED node stops the walk with no client, rather than letting the
// search continue leftward.
//
// The node hides the address the hub would report, so the chain names no
// client. Continuing left would let whichever proxy wrote the obfuscated entry
// nominate any address to its left as the client, which is the substitution
// the trust test exists to prevent.
func TestMiddleware_ObfuscatedNodeStopsTheWalk(t *testing.T) {
	t.Parallel()
	for _, node := range []string{"unknown", "_hidden"} {
		t.Run(node, func(t *testing.T) {
			t.Parallel()
			observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
				r.RemoteAddr = "10.0.0.3:4327"
				r.Header.Set("Forwarded", "for=198.51.100.7, for="+node+", for=10.0.0.2")
			})
			assert.Empty(t, observed.clientIP,
				"an obfuscated node hides the client; the chain names none")
		})
	}
}

// A quoted-pair escape inside a `for` node is legal RFC 7230 syntax, and the
// parser has always unescaped it. The selection now reads that parsed address
// directly, so the escaped form and the plain form name the same client.
func TestMiddleware_AcceptsAQuotedPairEscapeInAForNode(t *testing.T) {
	t.Parallel()
	observed := observeRequest(t, mustTrusted(t, "10.0.0.0/8"), func(r *http.Request) {
		r.RemoteAddr = "10.0.0.3:4327"
		r.Header.Set("Forwarded", `for="\1\9\8.51.100.7", for=10.0.0.2`)
	})
	assert.Equal(t, "198.51.100.7", observed.clientIP)
}

// The verified protocol reaches the CONTEXT, which is where the cookie policy
// and the base-URL builder read it. It used to be written to `URL.Scheme`,
// where nothing read it at all.
func TestMiddleware_RecordsTheVerifiedProtocolOnTheContext(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		trusted   []string
		configure func(*http.Request)
		wantHTTPS bool
	}{
		{
			name:    "a trusted proxy that verified TLS",
			trusted: []string{"10.0.0.0/8"},
			configure: func(r *http.Request) {
				r.RemoteAddr = "10.0.0.3:4327"
				r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.2")
				r.Header.Set("X-Forwarded-Proto", "https")
			},
			wantHTTPS: true,
		},
		{
			name:    "a trusted proxy that reports plain HTTP",
			trusted: []string{"10.0.0.0/8"},
			configure: func(r *http.Request) {
				r.RemoteAddr = "10.0.0.3:4327"
				r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.2")
				r.Header.Set("X-Forwarded-Proto", "http")
			},
		},
		{
			// The header is caller-controlled here, so it must not be read.
			// This is the case that would hand any caller a Secure cookie.
			name:    "an UNTRUSTED peer claiming https",
			trusted: nil,
			configure: func(r *http.Request) {
				r.RemoteAddr = "198.51.100.7:4327"
				r.Header.Set("X-Forwarded-Proto", "https")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var https bool
			trusted := requestsource.TrustedRanges{}
			if len(test.trusted) > 0 {
				trusted = mustTrusted(t, test.trusted...)
			}
			request := httptest.NewRequest(http.MethodGet, "http://hub.example.test/", nil)
			test.configure(request)
			requestsource.Middleware(
				func(context.Context) requestsource.TrustedRanges { return trusted },
				http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					https = peer.IsHTTPS(r.Context())
				}),
			).ServeHTTP(httptest.NewRecorder(), request)
			assert.Equal(t, test.wantHTTPS, https)
		})
	}
}
