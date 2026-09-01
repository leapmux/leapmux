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
//
// It takes the endpoint the CALLER holds rather than building its own, and the
// proxy that follows is built on the same one. An endpoint owns the h2c verdict
// and the connection pools, so a second endpoint for the same URL probes the
// hub a second time, pools separately, and leaves the first one's idle
// connections with no owner that can close them.
func probeHub(ctx context.Context, endpoint *hubtransport.Endpoint) error {
	baseURL := strings.TrimRight(endpoint.BaseURL(), "/")
	target := baseURL + "/leapmux.v1.AuthService/GetSystemInfo"

	// Pin redirects to the hub origin, the same as every other hub-directed
	// client (see HubProxy in proxy.go). The hub URL arrives from the webview,
	// so a hub-side or MITM'd off-origin 3xx on the probe would otherwise be
	// followed by this CORS-free desktop process, re-opening the SSRF to
	// loopback services and cloud metadata the pin exists to close.
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
