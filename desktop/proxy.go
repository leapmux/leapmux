package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"

	"golang.org/x/net/http2"
)

// ProxyResponse is the response returned from ProxyHTTP to the frontend.
type ProxyResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"` // base64-encoded
}

// HubProxy proxies ConnectRPC requests from the frontend to the Hub.
type HubProxy struct {
	client   *http.Client // h2c client for ConnectRPC (with cookie jar)
	wsClient *http.Client // HTTP/1.1 client for WebSocket upgrade
	jar      http.CookieJar
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
		jar:     jar,
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
		jar:     jar,
		baseURL: hubURL,
	}
}

// ProxyHTTP is a Wails-bound method. The frontend calls it via a custom
// fetch implementation to proxy ConnectRPC requests to the Hub.
// The cookie jar manages session cookies automatically.
func (a *App) ProxyHTTP(method, path, headersJSON, bodyBase64 string) (*ProxyResponse, error) {
	if a.proxy == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Build the target URL.
	targetURL := a.proxy.baseURL + path

	// Decode body.
	var bodyReader io.Reader
	if bodyBase64 != "" {
		bodyBytes, err := base64.StdEncoding.DecodeString(bodyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequest(method, targetURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Parse and apply headers from the frontend.
	if headersJSON != "" {
		var headers map[string]string
		if err := json.Unmarshal([]byte(headersJSON), &headers); err != nil {
			return nil, fmt.Errorf("parse headers: %w", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	}

	resp, err := a.proxy.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("proxy request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
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
		Body:    base64.StdEncoding.EncodeToString(respBody),
	}, nil
}

// cookieHeader returns the Cookie header value from the jar for the hub base URL.
func (p *HubProxy) cookieHeader() http.Header {
	u, err := url.Parse(p.baseURL)
	if err != nil {
		return nil
	}
	cookies := p.jar.Cookies(u)
	if len(cookies) == 0 {
		return nil
	}
	h := make(http.Header)
	for _, c := range cookies {
		h.Add("Cookie", c.String())
	}
	return h
}
