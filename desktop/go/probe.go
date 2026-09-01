package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/leapmux/leapmux/hubtransport"
)

// probeHub sends a ConnectRPC JSON request to the Hub's GetSystemInfo endpoint
// to verify that the Hub is reachable and responsive.
func probeHub(ctx context.Context, hubURL string) error {
	// hubtransport supplies the transport, so this probe reaches a `unix:` or
	// `npipe:` hub as well, and prefers h2c on a cleartext one -- the same
	// answer the proxy that follows it will use.
	endpoint, err := hubtransport.New(hubURL)
	if err != nil {
		return fmt.Errorf("probe hub: %w", err)
	}
	baseURL := strings.TrimRight(endpoint.BaseURL(), "/")
	target := baseURL + "/leapmux.v1.AuthService/GetSystemInfo"

	// Pin redirects to the hub origin, the same as every other hub-directed
	// client (see HubProxy in proxy.go). hubURL arrives from the webview, so a
	// hub-side or MITM'd off-origin 3xx on the probe would otherwise be followed
	// by this CORS-free desktop process, re-opening the SSRF to loopback services
	// and cloud metadata the pin exists to close.
	client := endpoint.UnaryClient(10 * time.Second)
	client.CheckRedirect = pinRedirectsToOrigin(baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("probe hub: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe hub: unexpected status %d", resp.StatusCode)
	}
	return nil
}
