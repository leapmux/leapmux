package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// probeHub sends a ConnectRPC JSON request to the Hub's GetSystemInfo endpoint
// to verify that the Hub is reachable and responsive.
func probeHub(hubURL string) error {
	hubURL = strings.TrimRight(hubURL, "/")
	endpoint := hubURL + "/leapmux.v1.AuthService/GetSystemInfo"

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(endpoint, "application/json", strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("probe hub: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe hub: unexpected status %d", resp.StatusCode)
	}
	return nil
}
