package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCookieHeader_SingleHeader(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	baseURL := "http://localhost"
	u, err := url.Parse(baseURL)
	require.NoError(t, err)

	jar.SetCookies(u, []*http.Cookie{
		{Name: "session", Value: "abc123"},
		{Name: "csrf", Value: "xyz789"},
	})

	proxy := &HubProxy{client: &http.Client{Jar: jar}, baseURL: baseURL}
	h := proxy.cookieHeader()
	require.NotNil(t, h)

	// Must produce exactly one Cookie header, not multiple.
	values := h.Values("Cookie")
	assert.Len(t, values, 1, "should produce a single Cookie header")
	assert.Contains(t, values[0], "session=abc123")
	assert.Contains(t, values[0], "csrf=xyz789")
	assert.Contains(t, values[0], "; ", "cookies should be semicolon-separated")
}

func TestCookieHeader_NoCookies(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	proxy := &HubProxy{client: &http.Client{Jar: jar}, baseURL: "http://localhost"}
	h := proxy.cookieHeader()
	assert.Nil(t, h)
}

func TestCookieHeader_InvalidURL(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	proxy := &HubProxy{client: &http.Client{Jar: jar}, baseURL: "://invalid"}
	h := proxy.cookieHeader()
	assert.Nil(t, h)
}

func TestProxyHTTPRejectsResponseLargerThanFrameBudget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(maxFrameSize+1))
		_, _ = w.Write(make([]byte, maxFrameSize+1))
	}))
	t.Cleanup(server.Close)
	app := NewApp("")
	installTestConnection(app, newHTTPProxy(server.URL), nil, server.URL)
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	_, _, err := app.ProxyHTTP(context.Background(), "GET", "/", nil, nil)
	require.ErrorContains(t, err, "response body exceeds")
}

func TestProxyHTTPRejectsBodyThatCannotFitResponseEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, maxFrameSize))
	}))
	t.Cleanup(server.Close)
	app := NewApp("")
	installTestConnection(app, newHTTPProxy(server.URL), nil, server.URL)
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	_, _, err := app.ProxyHTTP(context.Background(), "GET", "/", nil, nil)
	require.ErrorContains(t, err, "frame budget")
}

func TestProxyHTTPPreservesRepeatedResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", "first=one; Path=/")
		w.Header().Add("Set-Cookie", "second=two; Path=/")
		w.Header().Add("X-Repeated", "one")
		w.Header().Add("X-Repeated", "two")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	app := NewApp("")
	installTestConnection(app, newHTTPProxy(server.URL), nil, server.URL)
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	resp, _, err := app.ProxyHTTP(context.Background(), "GET", "/", nil, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"first=one; Path=/", "second=two; Path=/"}, resp.Headers.Values("Set-Cookie"))
	require.Equal(t, []string{"one", "two"}, resp.Headers.Values("X-Repeated"))
}

func TestSwitchModeCancelsBlockedProxyRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/leapmux.v1.AuthService/GetSystemInfo" {
			w.WriteHeader(http.StatusOK)
			return
		}
		close(requestStarted)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	app := NewApp("")
	require.NoError(t, app.ConnectDistributed(context.Background(), server.URL))
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })

	requestDone := make(chan error, 1)
	go func() {
		_, _, err := app.ProxyHTTP(context.Background(), "GET", "/blocked", nil, nil)
		requestDone <- err
	}()
	<-requestStarted
	switchDone := make(chan error, 1)
	go func() {
		_, err := app.SwitchMode()
		switchDone <- err
	}()
	select {
	case err := <-switchDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		app.cancel()
		<-requestDone
		<-switchDone
		t.Fatal("SwitchMode did not cancel the connection-scoped proxy request")
	}
	require.ErrorIs(t, <-requestDone, context.Canceled)
}

// TestProxyResponseBodyBudgetBoundsAndShrinksWithHeaders checks the cheap
// envelope-overhead estimate that replaced building a throwaway full proto
// Frame: the budget never exceeds maxFrameSize, is below it once headers are
// present, and a body within the budget is accepted (the final frame validation
// remains the source of truth).
func TestProxyResponseBodyBudgetBoundsAndShrinksWithHeaders(t *testing.T) {
	// No headers reserve only the small fixed wrapper overhead, so the budget
	// sits just under maxFrameSize (and never above it).
	noHeaders := proxyResponseBodyBudget(http.Header{})
	require.LessOrEqual(t, noHeaders, maxFrameSize)
	require.Greater(t, noHeaders, maxFrameSize-128)

	headers := http.Header{}
	headers.Add("X-Many", strings.Repeat("v", 4_000))
	headers.Add("X-Other", "value")
	withHeaders := proxyResponseBodyBudget(headers)
	require.Less(t, withHeaders, noHeaders, "headers must consume some of the budget")
	require.Greater(t, withHeaders, maxFrameSize-8_000, "header reserve must stay in the right ballpark")

	// A nil-safe call must not panic and must stay bounded.
	require.LessOrEqual(t, proxyResponseBodyBudget(nil), maxFrameSize)
}

// Headers that alone overflow the frame must yield a NEGATIVE budget, not a
// clamped zero.
//
// Zero reads as "only an empty body fits", but at that point not even an empty one
// does: `len(respBody) > 0` is false for an empty body, so it would be ADMITTED and
// its headers copied verbatim into a frame breaching maxFrameSize -- landing in
// exactly the validateFrameSize error-substitution branch the budget exists to keep
// unreachable, and reporting a generic frame-size failure instead of naming the
// cause. A negative budget makes the same comparison refuse it.
func TestProxyResponseBodyBudgetIsNegativeWhenHeadersAloneOverflow(t *testing.T) {
	headers := http.Header{}
	// Overflow the frame with headers alone -- the shape of a runaway Set-Cookie
	// fan-out or a WAF that echoes a large request back in its response headers.
	for i := 0; i < 24; i++ {
		headers.Add(fmt.Sprintf("X-Big-%02d", i), strings.Repeat("v", 1<<20))
	}

	budget := proxyResponseBodyBudget(headers)
	require.Negative(t, budget,
		"headers that alone exceed the frame must not leave a zero budget that admits an empty body")

	// The guard the caller applies: an EMPTY body must fail this comparison, which
	// a clamped-to-zero budget would let through.
	require.Greater(t, len([]byte{}), budget,
		"an empty body must be refused when the headers alone do not fit")
}

// The budget must be a GUARANTEED upper bound on the envelope overhead: a body
// sized to exactly the returned budget, wrapped with the same headers in the
// real Frame, must always fit within maxFrameSize. Otherwise ProxyHTTP admits a
// body that validateFrameSize then rejects, substituting an error for a response
// it could have delivered. The headers use 300-byte values so their length
// varints are multi-byte -- the case the previous +4-per-field estimate
// under-counted.
func TestProxyResponseBodyBudgetIsAGuaranteedUpperBound(t *testing.T) {
	headers := http.Header{}
	for i := 0; i < 40; i++ {
		headers.Add(fmt.Sprintf("X-Header-%02d", i), strings.Repeat("v", 300))
	}
	headers.Add("Set-Cookie", strings.Repeat("c", 300))
	headers.Add("Set-Cookie", strings.Repeat("d", 300))

	budget := proxyResponseBodyBudget(headers)
	frame := &desktoppb.Frame{
		Message: &desktoppb.Frame_Response{Response: &desktoppb.Response{
			Id: ^uint64(0), // worst-case max-varint id, reserved by wrapperReserve
			Result: &desktoppb.Response_ProxyHttp{ProxyHttp: &desktoppb.ProxyHttpResponse{
				Status:  200,
				Headers: headerValuesToProto(headers),
				Body:    make([]byte, budget),
			}},
		}},
	}
	require.NoError(t, validateFrameSize(frame),
		"a budget-sized body plus its headers must fit within maxFrameSize")
}

// A caller-supplied request path must never escape the Hub origin.
//
// The path comes from the webview, so it is attacker-controlled the moment any
// hostile content renders there -- and this app renders agent output. Building the
// target by concatenation (baseURL + path) let the path escape entirely: a leading
// "@" turned the base into URL userinfo, so "https://hub.example.com" +
// "@evil.example/x" resolved to host "evil.example". That handed the page an
// arbitrary, CORS-free HTTP client running as the desktop process.
//
// The two defences differ, and both matter: a path that merely LOOKS like an
// origin is neutralized by resolving it as a path (it lands on the Hub), while a
// path that genuinely carries an origin is rejected outright.
func TestHubRequestURL_NeutralizesUserinfoEscape(t *testing.T) {
	base := mustParseURL("https://hub.example.com")

	// These are the concatenation attack. Resolved with URL semantics they are
	// ordinary relative paths, so they land harmlessly ON the Hub.
	for _, tc := range []struct{ path, want string }{
		{"@evil.example/steal", "https://hub.example.com/@evil.example/steal"},
		{"@169.254.169.254/latest/meta-data/", "https://hub.example.com/@169.254.169.254/latest/meta-data/"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			got, err := hubRequestURL(base, tc.path)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)

			// The property that actually matters: the request still goes to the Hub.
			u, parseErr := url.Parse(got)
			require.NoError(t, parseErr)
			assert.Equal(t, "hub.example.com", u.Host, "the request must never leave the hub host")
		})
	}
}

func TestHubRequestURL_RejectsOriginEscape(t *testing.T) {
	base := mustParseURL("https://hub.example.com")

	for _, path := range []string{
		"//evil.example/steal",              // protocol-relative escape
		"https://evil.example/steal",        // outright absolute URL
		"http://hub.example.com/x",          // scheme downgrade
		"https://user:pw@hub.example.com/x", // credentials smuggled in
	} {
		t.Run(path, func(t *testing.T) {
			got, err := hubRequestURL(base, path)
			require.Error(t, err, "a path carrying its own origin must be refused")
			assert.Empty(t, got)
		})
	}
}

// Ordinary Hub paths must still resolve, including query strings and dot segments
// that stay within the origin.
func TestHubRequestURL_AllowsHubPaths(t *testing.T) {
	base := mustParseURL("https://hub.example.com")

	for _, tc := range []struct{ path, want string }{
		{"/api/v1/workspaces", "https://hub.example.com/api/v1/workspaces"},
		{"/api/v1/list?limit=10&q=a%20b", "https://hub.example.com/api/v1/list?limit=10&q=a%20b"},
		{"/a/../b", "https://hub.example.com/b"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			got, err := hubRequestURL(base, tc.path)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A traversal path must not climb above the Hub root into another origin; URL
// resolution clamps it at the root, which keeps it on the Hub.
func TestHubRequestURL_TraversalStaysOnHub(t *testing.T) {
	got, err := hubRequestURL(mustParseURL("https://hub.example.com"), "/../../etc/passwd")
	require.NoError(t, err)
	assert.Equal(t, "https://hub.example.com/etc/passwd", got, "traversal clamps at the origin root")
}

// HubProxy.Do performs a hub request on its own, with no App, no lifecycle locks and
// no operation gate.
//
// That it can be tested this way IS the point of the split: the origin pin and the
// body budget are properties of proxying to a hub, and they used to be reachable
// only through App.ProxyHTTP, which needs a connected app to exercise at all.
func TestHubProxyDoPerformsRequest(t *testing.T) {
	var gotMethod, gotPath, gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotHeader = r.Method, r.URL.Path, r.Header.Get("X-Probe")
		w.Header().Set("X-Reply", "pong")
		w.WriteHeader(201)
		_, _ = w.Write([]byte("hello"))
	}))
	t.Cleanup(server.Close)

	proxy := &HubProxy{client: server.Client(), baseURL: server.URL}
	resp, body, err := proxy.Do(context.Background(), "POST", "/api/thing",
		map[string]string{"X-Probe": "ping"}, []byte("payload"))

	require.NoError(t, err)
	assert.Equal(t, "POST", gotMethod)
	assert.Equal(t, "/api/thing", gotPath)
	assert.Equal(t, "ping", gotHeader, "request headers must be forwarded")
	assert.Equal(t, 201, resp.Status)
	assert.Equal(t, "pong", resp.Headers.Get("X-Reply"), "response headers must be returned")
	assert.Equal(t, "hello", string(body))
}

// The origin pin is a property of the proxy, and must refuse any path carrying its
// own origin -- the SSRF guard, now exercisable directly with no App around it.
//
// Every form of "somewhere else" is caught by the same arm (ref.Host / ref.IsAbs /
// ref.User), which is why the resolved-origin compare below it is defence in depth
// rather than the live guard.
func TestHubProxyDoRefusesOffOriginPath(t *testing.T) {
	proxy := &HubProxy{client: http.DefaultClient, baseURL: "http://hub.invalid"}

	for name, path := range map[string]string{
		"absolute URL":      "http://evil.example/steal",
		"protocol-relative": "//evil.example/steal",
		"embedded creds":    "http://user:pass@evil.example/steal",
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := proxy.Do(context.Background(), "GET", path, nil, nil)
			require.Error(t, err, "a path carrying its own origin must not be proxied")
			assert.Contains(t, err.Error(), "must be relative to the hub")
		})
	}
}

// A cancelled context must abort the request rather than hang: Do binds the caller's
// and the connection's lifetimes through the ctx App hands it.
func TestHubProxyDoHonoursContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	t.Cleanup(server.Close)

	proxy := &HubProxy{client: server.Client(), baseURL: server.URL}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := proxy.Do(ctx, "GET", "/slow", nil, nil)
	require.Error(t, err, "a cancelled context must abort the proxied request")
}

// The origin pin covers redirect hops, not just the first request: a hub-side 3xx
// pointing off-origin is refused, so an open redirect on the trusted hub cannot turn
// this CORS-free client into an SSRF client reaching loopback/metadata as the user's
// process. Without the CheckRedirect pin the default policy would follow it.
func TestHubProxyDoRefusesOffOriginRedirect(t *testing.T) {
	var reachedTarget bool
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reachedTarget = true
		_, _ = w.Write([]byte("secret"))
	}))
	t.Cleanup(internal.Close)

	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bounce" {
			http.Redirect(w, r, internal.URL+"/latest/meta-data/", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hub.Close)

	client := hub.Client()
	client.CheckRedirect = pinRedirectsToOrigin(hub.URL)
	proxy := &HubProxy{client: client, baseURL: hub.URL}

	_, _, err := proxy.Do(context.Background(), "GET", "/bounce", nil, nil)
	require.Error(t, err, "a redirect leaving the hub origin must not be followed")
	assert.Contains(t, err.Error(), "leaves the origin")
	assert.False(t, reachedTarget, "the off-origin target must never be reached")
}

// A same-origin hub redirect is still followed, so the pin does not break a
// legitimate hub 3xx (an auth bounce, a trailing-slash 301).
func TestHubProxyDoFollowsSameOriginRedirect(t *testing.T) {
	var finalHit bool
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			finalHit = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}
	}))
	t.Cleanup(hub.Close)

	client := hub.Client()
	client.CheckRedirect = pinRedirectsToOrigin(hub.URL)
	proxy := &HubProxy{client: client, baseURL: hub.URL}

	resp, body, err := proxy.Do(context.Background(), "GET", "/start", nil, nil)
	require.NoError(t, err, "a same-origin redirect must be followed")
	assert.True(t, finalHit, "the same-origin target must be reached")
	assert.Equal(t, http.StatusOK, resp.Status)
	assert.Equal(t, "ok", string(body))
}

// The remote hub proxy must actually WIRE the redirect pin onto BOTH clients --
// the HTTP/ConnectRPC client and the HTTP/1.1 WebSocket-upgrade client -- so a
// hub-side off-origin 3xx is refused on every path this CORS-free process opens
// to the hub. coder/websocket's Dial FOLLOWS redirect responses during the WS
// upgrade, so a wsClient without the pin would let an open redirect on
// /ws/channel or /ws/orgevents lead the desktop off-origin; this proves both
// clients carry it so a future edit that drops either is caught.
func TestNewHTTPProxyPinsRedirects(t *testing.T) {
	proxy := newHTTPProxy("https://hub.example.com")
	require.NotNil(t, proxy.client.CheckRedirect, "the remote hub proxy must pin redirects on its HTTP client")
	require.NotNil(t, proxy.wsClient.CheckRedirect, "the remote hub proxy must pin redirects on its WS-upgrade client")

	offOrigin, err := http.NewRequest("GET", "https://evil.example/x", nil)
	require.NoError(t, err)
	assert.Error(t, proxy.client.CheckRedirect(offOrigin, nil),
		"the HTTP client pin must refuse a redirect leaving the hub origin")
	assert.Error(t, proxy.wsClient.CheckRedirect(offOrigin, nil),
		"the WS-upgrade client pin must refuse a redirect leaving the hub origin")
}

// The origin pin compares EFFECTIVE origin (scheme + hostname + default-aware port),
// not the raw Host string, so a same-origin redirect whose Location names the
// scheme's default port explicitly is still followed -- while a genuinely off-origin
// host or a different non-default port is still refused. A raw Host compare would
// reject "https://hub.example:443" against base "https://hub.example".
func TestPinRedirectsToOriginNormalizesDefaultPort(t *testing.T) {
	newReq := func(target string) *http.Request {
		req, err := http.NewRequest("GET", target, nil)
		require.NoError(t, err)
		return req
	}

	t.Run("base without explicit port accepts explicit default port", func(t *testing.T) {
		check := pinRedirectsToOrigin("https://hub.example")
		require.NoError(t, check(newReq("https://hub.example:443/dashboard/"), nil),
			"a same-origin redirect naming the default :443 must be followed")
		require.NoError(t, check(newReq("https://hub.example/dashboard/"), nil),
			"the port-less same-origin form must still be followed")
	})

	t.Run("base with explicit default port accepts port-less form", func(t *testing.T) {
		check := pinRedirectsToOrigin("https://hub.example:443")
		require.NoError(t, check(newReq("https://hub.example/x"), nil),
			"the port-less form is the same origin as an explicit :443 base")
	})

	t.Run("genuinely different origins are still refused", func(t *testing.T) {
		check := pinRedirectsToOrigin("https://hub.example")
		assert.Error(t, check(newReq("https://evil.example/x"), nil), "different host must be refused")
		assert.Error(t, check(newReq("https://hub.example:8443/x"), nil), "different non-default port must be refused")
		assert.Error(t, check(newReq("http://hub.example/x"), nil), "downgraded scheme must be refused")
	})

	t.Run("host casing is ignored (DNS is case-insensitive)", func(t *testing.T) {
		// url.Hostname() preserves the case the URL carried, and neither the
		// configured base nor a redirect Location is lowercased, so a
		// case-sensitive compare would refuse a legitimate same-origin redirect
		// whose host differs only in case (infra behind a proxy that varies
		// casing). DNS names are case-insensitive, so these are the same origin.
		check := pinRedirectsToOrigin("https://Hub.Example.com")
		require.NoError(t, check(newReq("https://hub.example.com/dashboard"), nil),
			"a lowercased same-origin redirect against a mixed-case base must be followed")
		require.NoError(t, check(newReq("https://HUB.EXAMPLE.COM/dashboard"), nil),
			"an uppercased same-origin redirect must be followed")

		lowerBase := pinRedirectsToOrigin("https://hub.example.com")
		require.NoError(t, lowerBase(newReq("https://Hub.Example.com/x"), nil),
			"a mixed-case redirect against a lowercase base must be followed")
		// A genuinely different host is still refused regardless of casing.
		assert.Error(t, lowerBase(newReq("https://Evil.Example.com/x"), nil),
			"a different host must still be refused even when case-folded")
	})
}
