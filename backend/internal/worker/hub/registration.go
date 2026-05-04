package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/cenkalti/backoff/v5"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// RegistrationResult contains the credentials obtained after registration.
type RegistrationResult struct {
	WorkerID     string
	AuthToken    string
	RegisteredBy string
}

// Register presents `registrationKey` as a bearer credential to the
// hub's `WorkerConnectorService.Register` RPC and receives permanent
// worker credentials (auth token + worker ID + registered_by) in
// response.
//
// Network errors (hub unreachable) are retried with exponential backoff
// because they typically reflect transient transport issues. Application
// errors — `Unauthenticated` for an invalid/expired/consumed key,
// `InvalidArgument` for malformed input — are returned immediately so
// the worker can fail fast instead of burning a key on retries.
//
// A hubURL with a local-IPC scheme (locallisten.SchemeUnix or SchemeNpipe)
// is dispatched to the matching transport automatically.
func Register(ctx context.Context, hubURL, registrationKey, version string, publicKey, mlkemPublicKey, slhdsaPublicKey []byte) (*RegistrationResult, error) {
	httpClient, connectURL := clientForHubURL(hubURL)
	client := leapmuxv1connect.NewWorkerConnectorServiceClient(
		httpClient,
		connectURL,
		connect.WithGRPC(),
	)
	return registerWithClient(ctx, client, registrationKey, version, publicKey, mlkemPublicKey, slhdsaPublicKey, newDefaultBackoff())
}

func registerWithClient(
	ctx context.Context,
	client leapmuxv1connect.WorkerConnectorServiceClient,
	registrationKey string,
	version string,
	publicKey, mlkemPublicKey, slhdsaPublicKey []byte,
	bo backoff.BackOff,
) (*RegistrationResult, error) {
	if registrationKey == "" {
		return nil, errors.New("registration key is required")
	}

	for {
		req := connect.NewRequest(&leapmuxv1.RegisterRequest{
			Version:         version,
			PublicKey:       publicKey,
			MlkemPublicKey:  mlkemPublicKey,
			SlhdsaPublicKey: slhdsaPublicKey,
		})
		// The handler authenticates by reading the bearer key from the
		// Authorization header — this is *not* the long-lived auth_token
		// flow, that one is bound to a different RPC.
		req.Header().Set("Authorization", "Bearer "+registrationKey)

		resp, err := client.Register(ctx, req)
		if err == nil {
			slog.Info("worker registered",
				"worker_id", resp.Msg.GetWorkerId(),
				"registered_by", resp.Msg.GetRegisteredBy(),
			)
			return &RegistrationResult{
				WorkerID:     resp.Msg.GetWorkerId(),
				AuthToken:    resp.Msg.GetAuthToken(),
				RegisteredBy: resp.Msg.GetRegisteredBy(),
			}, nil
		}

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Don't retry permanent errors. An invalid or already-consumed
		// key surfaces as Unauthenticated; bad inputs surface as
		// InvalidArgument. Either way, retrying just wastes the user's
		// time and risks burning a fresh key in a race.
		if connectErr := new(connect.Error); errors.As(err, &connectErr) {
			switch connectErr.Code() {
			case connect.CodeUnauthenticated, connect.CodeInvalidArgument, connect.CodePermissionDenied:
				return nil, fmt.Errorf("registration rejected: %w", err)
			}
		}

		interval := bo.NextBackOff()
		slog.Warn("hub unavailable, retrying registration...", "error", err, "backoff", interval)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}
