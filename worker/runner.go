// Package worker provides an exported entry point for running the
// LeapMux worker as a library (e.g. from the standalone binary).
package worker

import (
	"context"
	"net/http"

	"github.com/leapmux/leapmux/internal/worker/hub"
)

// RunConfig holds configuration for running the worker as a library.
type RunConfig struct {
	HubURL     string       // Hub server URL (e.g. "http://localhost:4327")
	DataDir    string       // Directory for persistent state
	AuthToken  string       // Pre-provisioned auth token (skip registration)
	HTTPClient *http.Client // Custom HTTP client (e.g. for Unix socket transport)
}

// Run starts the worker and blocks until ctx is cancelled.
// If AuthToken is set, registration is skipped and the worker connects directly.
// If HTTPClient is set, it is used for ConnectRPC communication instead of the default.
func Run(ctx context.Context, cfg RunConfig) error {
	var client *hub.Client
	if cfg.HTTPClient != nil {
		client = hub.NewWithHTTPClient(cfg.HTTPClient, cfg.HubURL, cfg.DataDir)
	} else {
		client = hub.New(cfg.HubURL, cfg.DataDir)
	}
	defer client.Stop()

	client.ConnectWithReconnect(ctx, cfg.AuthToken)
	return nil
}
