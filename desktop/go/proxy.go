package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"

	"golang.org/x/net/http2"
)

// ProxyResponse is the response metadata returned from ProxyHTTP.
// The response body is returned separately as raw bytes.
type ProxyResponse struct {
	Status  int
	Headers map[string]string
}

// HubProxy proxies ConnectRPC requests from the frontend to the Hub.
type HubProxy struct {
	client   *http.Client // h2c client for ConnectRPC (with cookie jar)
	wsClient *http.Client // HTTP/1.1 client for WebSocket upgrade
	baseURL  string
}

// newUnixSocketProxy creates a proxy that connects to the Hub via Unix socket.
func newUnixSocketProxy(socketPath string) *HubProxy {
	jar, _ := cookiejar.New(nil)

	return &HubProxy{
		client: &http.Client{
			Jar: jar,
			Transport: &http2.Transport{
				AllowHTTP: true,
				DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
		// WebSocket upgrade requires HTTP/1.1, not h2c.
		wsClient: &http.Client{
			Jar: jar,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
		baseURL: "http://localhost",
	}
}

// newHTTPProxy creates a proxy that connects to a remote Hub via HTTP(S).
func newHTTPProxy(hubURL string) *HubProxy {
	jar, _ := cookiejar.New(nil)

	return &HubProxy{
		client: &http.Client{
			Jar: jar,
		},
		baseURL: hubURL,
	}
}

// ProxyHTTP proxies an HTTP request to the Hub and returns the response
// metadata and raw body bytes. The cookie jar manages session cookies
// automatically.
func (a *App) ProxyHTTP(method, path string, headers map[string]string, body []byte) (*ProxyResponse, []byte, error) {
	if a.proxy == nil {
		return nil, nil, fmt.Errorf("not connected")
	}

	targetURL := a.proxy.baseURL + path

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, targetURL, bodyReader)
	if err != nil {
		return nil, nil, fmt.Errorf("create request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := a.proxy.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("proxy request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}

	// Build response headers. For Set-Cookie, join all values so the
	// frontend can see them (map[string]string loses multi-values).
	respHeaders := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		if strings.EqualFold(k, "Set-Cookie") {
			respHeaders[k] = strings.Join(resp.Header.Values(k), ", ")
		} else {
			respHeaders[k] = resp.Header.Get(k)
		}
	}

	return &ProxyResponse{
		Status:  resp.StatusCode,
		Headers: respHeaders,
	}, respBody, nil
}

// cookieHeader returns a single Cookie header from the jar for the hub base URL.
func (p *HubProxy) cookieHeader() http.Header {
	u, err := url.Parse(p.baseURL)
	if err != nil {
		return nil
	}
	cookies := p.client.Jar.Cookies(u)
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
