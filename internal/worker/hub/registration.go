package hub

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v5"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/logging"
)

// RegistrationResult contains the credentials obtained after registration approval.
type RegistrationResult struct {
	WorkerID  string
	AuthToken string
	OrgID     string
}

// Register performs the registration flow with automatic retries:
// 1. Request a registration token from the Hub (retries with exponential backoff).
// 2. Print the approval URL for the user.
// 3. Poll until the registration is approved or expires.
//
// If hubURL starts with "unix:", it creates a Unix domain socket transport automatically.
func Register(ctx context.Context, hubURL, hostname, os, arch, version, homeDir string) (*RegistrationResult, error) {
	var httpClient *http.Client
	connectURL := hubURL
	if strings.HasPrefix(hubURL, "unix:") {
		socketPath := strings.TrimPrefix(hubURL, "unix:")
		httpClient = newUnixSocketClient(socketPath)
		connectURL = "http://localhost"
	} else {
		httpClient = newH2CClient()
	}
	client := leapmuxv1connect.NewWorkerConnectorServiceClient(
		httpClient,
		connectURL,
		connect.WithGRPC(),
	)
	return registerWithClient(ctx, client, hubURL, hostname, os, arch, version, homeDir, newDefaultBackoff())
}

func registerWithClient(
	ctx context.Context,
	client leapmuxv1connect.WorkerConnectorServiceClient,
	hubURL, hostname, os, arch, version, homeDir string,
	bo backoff.BackOff,
) (*RegistrationResult, error) {
	// Step 1: Request registration with retry.
	var reqResp *connect.Response[leapmuxv1.RequestRegistrationResponse]
	for {
		var err error
		reqResp, err = client.RequestRegistration(ctx, connect.NewRequest(
			&leapmuxv1.RequestRegistrationRequest{
				Hostname: hostname,
				Os:       os,
				Arch:     arch,
				Version:  version,
				HomeDir:  homeDir,
			},
		))
		if err == nil {
			break
		}

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		interval := bo.NextBackOff()
		slog.Warn("hub unavailable, retrying registration...", "error", err, "backoff", interval)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}

	regToken := reqResp.Msg.GetRegistrationToken()
	regPath := reqResp.Msg.GetRegistrationUrl()

	if strings.HasPrefix(hubURL, "unix:") {
		// Unix socket — can't construct a browser-navigable URL.
		// Show the relative path and let the user navigate via the Hub's web UI.
		slog.Info("registration requested — approve via Hub web UI",
			"path", regPath,
			"token", regToken,
		)
		fmt.Printf("\n  Approve this worker at the Hub's web UI: %s\n\n", regPath)
	} else {
		regURL := hubURL + regPath
		slog.Info("registration requested — approve in browser",
			"url", regURL,
			"token", regToken,
		)
		fmt.Printf("\n  Approve this worker at: %s\n\n", regURL)
		logging.PrintQRCode(regURL)
	}

	// Step 2: Long-poll until approved or expired.
	// Each PollRegistration call blocks on the Hub side for up to 30s when pending.
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		pollCtx, pollCancel := context.WithTimeout(ctx, 60*time.Second)
		pollResp, err := client.PollRegistration(pollCtx, connect.NewRequest(
			&leapmuxv1.PollRegistrationRequest{RegistrationToken: regToken},
		))
		pollCancel()
		if err != nil {
			slog.Warn("poll failed", "error", err)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(1 * time.Second):
			}
			continue
		}

		switch pollResp.Msg.GetStatus() {
		case leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED:
			slog.Info("registration approved",
				"worker_id", pollResp.Msg.GetWorkerId(),
			)
			return &RegistrationResult{
				WorkerID:  pollResp.Msg.GetWorkerId(),
				AuthToken: pollResp.Msg.GetAuthToken(),
				OrgID:     pollResp.Msg.GetOrgId(),
			}, nil

		case leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED:
			return nil, fmt.Errorf("registration expired")

		case leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING:
			continue // Hub long-poll timeout expired, loop again.

		default:
			return nil, fmt.Errorf("unexpected status: %v", pollResp.Msg.GetStatus())
		}
	}
}
