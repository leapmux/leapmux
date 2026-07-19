package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/util/ctxutil"
)

// ProxyResponse is the response metadata returned from ProxyHTTP.
// The response body is returned separately as raw bytes.
type ProxyResponse struct {
	Status  int
	Headers http.Header
}

// HubProxy proxies ConnectRPC requests from the frontend to the Hub.
type HubProxy struct {
	client   *http.Client // h2c client for ConnectRPC (with cookie jar)
	wsClient *http.Client // HTTP/1.1 client for WebSocket upgrade
	baseURL  string
	// base is baseURL parsed once at construction. The origin pin, the request
	// path resolution, and the cookie-jar lookup all operate on the SAME hub
	// origin, so parsing it once (rather than re-parsing the constant string on
	// every proxied RPC and every WS dial) keeps the hot path allocation-free.
	base *url.URL
}

// newLocalProxy returns a proxy that dials whichever local IPC transport
// the supplied URL names. `unix:<path>` uses the kernel's AF_UNIX listener;
// `npipe:<name>` uses a Windows named pipe via go-winio. Any other scheme
// is rejected.
func newLocalProxy(listenURL string) (*HubProxy, error) {
	dial, err := locallisten.Dialer(listenURL)
	if err != nil {
		return nil, fmt.Errorf("unsupported local proxy URL %q: %w", listenURL, err)
	}
	jar, _ := cookiejar.New(nil)
	// pinRedirectsToOrigin is the same origin pin the HTTP client applies, on the
	// WS client too: coder/websocket's Dial FOLLOWS redirect responses during the
	// upgrade (it wraps the client's CheckRedirect and follows when none is set),
	// so a wsClient without the pin would let a hub-side off-origin 3xx on
	// /ws/channel or /ws/orgevents lead the CORS-free desktop process off the hub
	// origin -- the exact redirect-escape the HTTP-path pin exists to close.
	return &HubProxy{
		client: &http.Client{
			Jar:           jar,
			Transport:     locallisten.NewLocalH2CTransport(dial),
			CheckRedirect: pinRedirectsToOrigin("http://localhost"),
		},
		// WebSocket upgrade requires HTTP/1.1, not h2c.
		wsClient: &http.Client{
			Jar:           jar,
			CheckRedirect: pinRedirectsToOrigin("http://localhost"),
			Transport: &http.Transport{
				DialContext: locallisten.HTTPDialContext(dial),
			},
		},
		baseURL: "http://localhost",
		base:    mustParseURL("http://localhost"),
	}, nil
}

// newHTTPProxy creates a proxy that connects to a remote Hub via HTTP(S).
func newHTTPProxy(hubURL string) *HubProxy {
	jar, _ := cookiejar.New(nil)

	return &HubProxy{
		client: &http.Client{
			Jar:           jar,
			CheckRedirect: pinRedirectsToOrigin(hubURL),
		},
		// The remote-hub WS upgrade needs the same HTTP/1.1 transport + origin pin
		// the local proxy's wsClient carries: without a wsClient here the WS dial
		// falls back to http.DefaultClient, which has no cookie jar and no origin
		// pin, so a hub-side off-origin 3xx on /ws/channel would be followed
		// off-origin. An explicit HTTP/1.1 transport keeps the upgrade off HTTP/2
		// (WebSocket over HTTP/2 is a separate extended-CONNECT flow coder/websocket
		// does not use), matching the local wsClient.
		wsClient: &http.Client{
			Jar:           jar,
			CheckRedirect: pinRedirectsToOrigin(hubURL),
			Transport:     &http.Transport{},
		},
		baseURL: hubURL,
		base:    mustParseURL(hubURL),
	}
}

// mustParseURL parses raw, returning nil on error. Both constructors feed a
// known-parseable origin ("http://localhost" or an already-validated hub URL),
// so this never fails in practice; nil propagates to resolvedBase, which fails
// closed exactly the way the per-call url.Parse did before the base was parsed
// once at construction.
func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	return u
}

// resolvedBase returns the hub origin parsed once at construction. It falls back
// to parsing baseURL for a HubProxy built without one (tests that construct a
// literal directly); production HubProxy values come from newLocalProxy /
// newHTTPProxy, which set base, so the fallback is not on the proxy hot path.
func (p *HubProxy) resolvedBase() (*url.URL, error) {
	if p.base != nil {
		return p.base, nil
	}
	if p.baseURL == "" {
		return nil, fmt.Errorf("hub base URL is not configured")
	}
	u, err := url.Parse(p.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse hub base URL: %w", err)
	}
	return u, nil
}

// requireWSClient fails closed when the proxy has no pinned WebSocket client,
// returning label ("channel relay" / "org events relay") in the message so the
// failing dial names itself. websocket.Dial and OpenOrgEventsWSWithHeader both
// fall back to http.DefaultClient when the client is nil, and that client carries
// neither the cookie jar nor pinRedirectsToOrigin -- so a hub-side off-origin 3xx
// on /ws/channel or /ws/orgevents would lead the CORS-free desktop process
// off-origin, the exact redirect-escape the pin closes. Both production proxy
// constructors set wsClient; a future one that forgets it must break loudly at
// the dial, not silently dial unpinned. Routing both dials through this one guard
// keeps the invariant (and its load-bearing rationale) in one site.
func (p *HubProxy) requireWSClient(label string) error {
	if p.wsClient == nil {
		return fmt.Errorf("%s: hub proxy has no pinned WebSocket client", label)
	}
	return nil
}

// effectivePort returns u's port, defaulting to the scheme's standard port when
// none is spelled out. It lets sameHubOrigin treat "hub.example" and
// "hub.example:443" (https) as the one origin they are.
func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https", "wss":
		return "443"
	case "http", "ws":
		return "80"
	default:
		return ""
	}
}

// sameHubOrigin reports whether u shares base's origin: same scheme, same
// hostname, same EFFECTIVE port. It compares Hostname()+effectivePort() rather
// than the raw Host string so a same-origin URL that names the scheme's default
// port explicitly ("https://hub.example:443/...") is not mistaken for an
// off-origin one. This does not weaken the SSRF pin: a different host or a
// genuinely different (non-default) port still fails, as does an off-scheme move.
//
// The hostname compare is case-insensitive: DNS names are case-insensitive, and
// url.Hostname() preserves whatever case the URL carried, so a hub-side Location
// that differs only in host casing ("https://Hub.example.com/...") is the SAME
// origin and must be followed rather than refused as an off-origin bounce. The
// scheme is already lowercased by url.Parse, so a plain == is correct there.
func sameHubOrigin(u, base *url.URL) bool {
	return u.Scheme == base.Scheme &&
		strings.EqualFold(u.Hostname(), base.Hostname()) &&
		effectivePort(u) == effectivePort(base)
}

// pinRedirectsToOrigin returns a CheckRedirect that refuses any redirect whose
// target leaves baseURL's origin (scheme+host+effective port).
//
// hubRequestURL pins only the FIRST request to the hub origin. Without a per-hop
// pin, a hub-side off-origin 3xx (an open redirect, an auth bounce, a trailing-slash
// 301 to another host) would be followed by this CORS-free client running as the
// user's desktop process -- re-opening the SSRF to loopback services and cloud
// metadata that the pin exists to close. Same-origin redirects are still followed,
// so a legitimate hub redirect works unchanged -- including one whose Location names
// the scheme's default port explicitly. A base URL that will not parse pins
// nothing, so refuse to follow any redirect at all rather than fail open.
func pinRedirectsToOrigin(baseURL string) func(*http.Request, []*http.Request) error {
	base, err := url.Parse(baseURL)
	if err != nil {
		return func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("hub base URL %q is unparseable; refusing redirect to %q", baseURL, req.URL.Redacted())
		}
	}
	return func(req *http.Request, _ []*http.Request) error {
		if !sameHubOrigin(req.URL, base) {
			return fmt.Errorf("refusing hub redirect that leaves the origin, to %q", req.URL.Redacted())
		}
		return nil
	}
}

// hubRequestURL resolves a caller-supplied request path against the Hub base
// URL and verifies the result still points AT the Hub. base is the constructor's
// once-parsed origin (HubProxy.base).
//
// The path arrives from the webview, so it is attacker-controlled whenever any
// hostile content renders there -- and this app renders agent output. Building the
// target by concatenation (baseURL + path) let the path escape the origin
// entirely: a leading "@" turns the base into URL *userinfo*, so
// "https://hub.example" + "@evil.example/x" resolves to host "evil.example", and
// "@169.254.169.254/latest/meta-data/" reaches cloud metadata. That handed the page
// an arbitrary, CORS-free HTTP client running as the user's desktop process, able
// to read loopback-only services and LAN hosts a browser never could.
//
// Resolving with URL semantics and then re-checking the origin is what makes the
// pin hold: ResolveReference applies the same rules a browser would (including
// "//host/x" protocol-relative escapes), and comparing scheme+host afterwards
// rejects anything that moved. Cookies were never at risk -- the jar is
// domain-scoped -- but the SSRF was real.
func hubRequestURL(base *url.URL, path string) (string, error) {
	if base == nil {
		return "", fmt.Errorf("hub base URL is not parseable")
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("parse request path: %w", err)
	}
	// A request path may not carry its own origin or credentials; only the Hub's.
	if ref.IsAbs() || ref.Host != "" || ref.User != nil {
		return "", fmt.Errorf("request path must be relative to the hub, got %q", path)
	}
	resolved := base.ResolveReference(ref)
	if !sameHubOrigin(resolved, base) {
		return "", fmt.Errorf("request path must not leave the hub origin, got %q", path)
	}
	return resolved.String(), nil
}

// ProxyHTTP forwards a frontend HTTP request to the hub.
//
// It owns only the lifecycle concerns -- admission, the shutdown check, snapshotting
// the connection under the read lock, and binding the request to both the caller's
// context and the connection's -- then delegates the request itself to the HubProxy
// it snapshotted. The proxying rules (origin pinning, the frame budget) live on
// HubProxy, beside the client and base URL they operate on.
func (a *App) ProxyHTTP(ctx context.Context, method, path string, headers map[string]string, body []byte) (*ProxyResponse, []byte, error) {
	done, err := a.beginOperation()
	if err != nil {
		return nil, nil, err
	}
	defer done()
	unlock, err := a.acquireLifecycleRLock()
	if err != nil {
		return nil, nil, err
	}
	connection := a.connection
	if connection == nil {
		unlock()
		return nil, nil, fmt.Errorf("not connected")
	}
	proxy := connection.proxy
	unlock()
	requestCtx, cancelRequest := ctxutil.WithLinkedCancel(connection.ctx, ctx)
	defer cancelRequest()

	return proxy.Do(requestCtx, method, path, headers, body)
}

// Do performs one hub request and reads the response within the frame budget.
//
// It lives on HubProxy rather than on App because every line of it operates on this
// type's own client and baseURL: the origin pin (a path must not escape the hub) and
// the budgeted read are properties of proxying to THIS hub, not of the app's
// lifecycle. Keeping them here means they can be exercised against an httptest
// server without an App, its four locks, and its operation gate.
//
// ctx must already be bound to both the caller and the connection; Do does not reach
// for either.
func (p *HubProxy) Do(ctx context.Context, method, path string, headers map[string]string, body []byte) (*ProxyResponse, []byte, error) {
	base, err := p.resolvedBase()
	if err != nil {
		return nil, nil, err
	}
	targetURL, err := hubRequestURL(base, path)
	if err != nil {
		return nil, nil, err
	}

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("proxy request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// resp.Header is immutable once Do returns and the body is read to EOF
	// (trailers land in resp.Trailer, not here), and it is only read below and
	// copied out by headerValuesToProto in the same handler call -- so use it
	// directly rather than deep-copying the whole header map on the proxy hot
	// path.
	bodyBudget := proxyResponseBodyBudget(resp.Header)
	if bodyBudget < 0 {
		// The headers alone do not fit, so NO body does -- not even an empty one.
		// Refusing here is what keeps the budget a true upper bound: clamping it to
		// zero instead would admit an empty-bodied response (0 > 0 is false) whose
		// headers are then copied verbatim into a frame that breaches maxFrameSize,
		// landing in the validateFrameSize error-substitution branch this budget
		// exists to keep unreachable -- and reporting a generic frame-size failure
		// rather than naming the actual cause.
		return nil, nil, fmt.Errorf(
			"proxy response headers alone exceed the %d-byte frame limit", maxFrameSize)
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(bodyBudget)+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}
	if len(respBody) > bodyBudget {
		return nil, nil, fmt.Errorf("proxy response body exceeds %d-byte frame budget", bodyBudget)
	}

	return &ProxyResponse{
		Status:  resp.StatusCode,
		Headers: resp.Header,
	}, respBody, nil
}

func headerValuesToProto(headers http.Header) map[string]*desktoppb.HeaderValues {
	result := make(map[string]*desktoppb.HeaderValues, len(headers))
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		result[name] = &desktoppb.HeaderValues{Values: append([]string(nil), values...)}
	}
	return result
}

// proxyResponseBodyBudget returns the largest response body (in bytes) that
// still fits under maxFrameSize once wrapped in its Frame/Response/ProxyHttp
// envelope with these headers. It uses a cheap GUARANTEED UPPER BOUND on the
// envelope's non-body encoded size rather than building (and proto.Size-ing) a
// throwaway full Frame, so the proxy hot path avoids a per-response
// HeaderValues map allocation and a full proto-tree size walk. Because it is a
// true upper bound, an admitted body always yields a frame within maxFrameSize,
// so the final validateFrameSize on the write path is a pure backstop that this
// never forces into its error-substitution branch; over-estimating the overhead
// only shrinks the body budget by a handful of bytes on a 20 MiB frame.
//
// The result is NEGATIVE when the headers alone overflow the frame, and the caller
// must refuse such a response outright. It is deliberately not clamped to zero: a
// zero budget reads as "only an empty body fits", but at that point not even an
// empty one does, and admitting it is what would breach the upper bound the
// paragraph above promises.
func proxyResponseBodyBudget(headers http.Header) int {
	// The headers map is map<string, HeaderValues>. Each entry encodes as a
	// length-delimited map-entry message (outer field tag + entry length), whose
	// body is the string key (tag + length) and the HeaderValues submessage (tag
	// + length); the submessage then holds each value string (tag + length). Bound
	// every length with a full 5-byte varint and every tag with a byte so the sum
	// can never under-count the real proto framing.
	const (
		wrapperReserve   = 48                    // Frame/Response/ProxyHttpResponse tags + status + max-varint Id + body length prefix
		perHeaderFraming = 2 + 5 + 1 + 5 + 1 + 5 // map-entry tag+len, key tag+len, HeaderValues tag+len
		perValueFraming  = 1 + 5                 // value string tag + max-varint len
	)
	overhead := wrapperReserve
	for name, values := range headers {
		if len(values) == 0 {
			continue
		}
		overhead += len(name) + perHeaderFraming
		for _, v := range values {
			overhead += len(v) + perValueFraming
		}
	}
	return maxFrameSize - overhead
}

// cookieHeader returns a single Cookie header from the jar for the hub base URL.
func (p *HubProxy) cookieHeader() http.Header {
	base, err := p.resolvedBase()
	if err != nil {
		return nil
	}
	cookies := p.client.Jar.Cookies(base)
	if len(cookies) == 0 {
		return nil
	}
	parts := make([]string, len(cookies))
	for i, c := range cookies {
		parts[i] = c.String()
	}
	h := make(http.Header)
	h.Set("Cookie", strings.Join(parts, "; "))
	return h
}
