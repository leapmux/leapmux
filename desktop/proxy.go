package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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
	client  *http.Client
	baseURL string
}

// newUnixSocketProxy creates a proxy that connects to the Hub via Unix socket.
func newUnixSocketProxy(socketPath string) *HubProxy {
	return &HubProxy{
		client: &http.Client{
			Transport: &http2.Transport{
				AllowHTTP: true,
				DialTLSContext: func(ctx context.Context, _, _ string, _ *tls.Config) (net.Conn, error) {
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
	return &HubProxy{
		client:  &http.Client{},
		baseURL: hubURL,
	}
}

// ProxyHTTP is a Wails-bound method. The frontend calls it via a custom
// fetch implementation to proxy ConnectRPC requests to the Hub.
func (a *App) ProxyHTTP(method, path, headersJSON, bodyBase64 string) (*ProxyResponse, error) {
	if a.proxy == nil {
		return nil, fmt.Errorf("not connected")
	}

	// Build the target URL.
	url := a.proxy.baseURL + path

	// Decode body.
	var bodyReader io.Reader
	if bodyBase64 != "" {
		bodyBytes, err := base64.StdEncoding.DecodeString(bodyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode body: %w", err)
		}
		bodyReader = strings.NewReader(string(bodyBytes))
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	// Parse and apply headers.
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

	respHeaders := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	return &ProxyResponse{
		Status:  resp.StatusCode,
		Headers: respHeaders,
		Body:    base64.StdEncoding.EncodeToString(respBody),
	}, nil
}
