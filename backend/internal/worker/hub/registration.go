package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
)

// RegistrationResult contains the credentials obtained after registration.
type RegistrationResult struct {
	WorkerID  string
	AuthToken string
}

// Register presents `registrationKey` as a bearer credential to the
// hub's `WorkerConnectorService.Register` RPC and receives permanent
// worker credentials (auth token + worker ID) in response. The
// registering user (`registered_by`) is no longer returned here; the
// hub delivers worker ownership via the WorkerIdentity greeting on every
// Connect, so the worker keeps no cached copy of it (see UpdateRegisteredBy).
//
// Network errors (hub unreachable) are retried with exponential backoff
// because they typically reflect transient transport issues. Application
// errors — `Unauthenticated` for an invalid/expired/consumed key,
// `InvalidArgument` for malformed input — are returned immediately so
// the worker can fail fast instead of burning a key on retries.
//
// endpoint carries the transport, so a local-IPC scheme (`unix:`, `npipe:`)
// reaches the matching socket automatically. The call takes the HTTP2Only
// client for the same reason Connect does: the gRPC protocol needs HTTP/2
// trailers, which HTTP/1.1 has no way to carry.
func Register(ctx context.Context, endpoint *hubtransport.Endpoint, registrationKey, version string, publicKey, mlkemPublicKey, slhdsaPublicKey []byte) (*RegistrationResult, error) {
	client := leapmuxv1connect.NewWorkerConnectorServiceClient(
		endpoint.HTTP2OnlyClient(),
		endpoint.BaseURL(),
		connect.WithGRPC(),
	)
	return registerWithClient(ctx, client, registrationKey, version, publicKey, mlkemPublicKey, slhdsaPublicKey, newDefaultRegisterRetry())
}

const registerRetryTimerTag = "hub-register-retry"

// registerRetry is the retry policy one registration run follows.
//
// The three travel as one value rather than as three more parameters on an
// already long list, because they only make sense together: a caller that
// shortens the backoff also wants the clock that makes the wait between
// attempts instant, and the attempt limit is the third dial of the same
// policy.
type registerRetry struct {
	backoff        backoff
	attemptTimeout time.Duration
	// clock supplies the wait between attempts. A test drives it, so a
	// registration ladder costs no wall time and its rungs are exact.
	clock quartz.Clock
}

// newDefaultRegisterRetry is the policy Register uses in production.
func newDefaultRegisterRetry() registerRetry {
	return registerRetry{
		backoff:        newDefaultBackoff(),
		attemptTimeout: registerAttemptTimeout,
		clock:          quartz.NewReal(),
	}
}

func registerWithClient(
	ctx context.Context,
	client leapmuxv1connect.WorkerConnectorServiceClient,
	registrationKey string,
	version string,
	publicKey, mlkemPublicKey, slhdsaPublicKey []byte,
	retry registerRetry,
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

		resp, err := registerOnce(ctx, client, req, retry.attemptTimeout)
		if err == nil {
			// The owner is not recorded here: the Hub delivers it on every Connect
			// (WorkerIdentity), so the worker never caches a copy that could go stale
			// or go missing.
			slog.Info("worker registered", "worker_id", resp.Msg.GetWorkerId())
			return &RegistrationResult{
				WorkerID:  resp.Msg.GetWorkerId(),
				AuthToken: resp.Msg.GetAuthToken(),
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
			default:
				// Every other code is treated as transient and falls through to
				// the backoff retry below.
			}
		}

		interval := retry.backoff.Next()
		slog.Warn("hub unavailable, retrying registration...", "error", err, "backoff", interval)
		if !waitOrCancel(ctx, retry.clock, interval, registerRetryTimerTag) {
			return nil, ctx.Err()
		}
	}
}

// registerAttemptTimeout limits ONE registration attempt.
//
// The retry loop above sits BELOW the call, so an attempt that never returns
// takes the loop with it. Register runs on the HTTP2Only lane, which carries no
// http.Client timeout by design — that lane also carries the worker's
// bidirectional Connect stream, whose body ends only when the stream does — so
// a hub that completed the TCP accept and the HTTP/2 handshake and then never
// answered hung `leapmux worker` at startup for ever, with no retry, no backoff
// and no log line. The limit belongs to this unary CALL rather than to the
// lane, which is why it is a context deadline here.
//
// It is generous: registration is one round trip, but it runs while a hub may
// still be starting, and the retry costs a fresh key nothing.
//
// This limit stays on the CONTEXT rather than on registerRetry.clock, unlike
// the wait between attempts. context.WithTimeout reads the real clock, and no
// Quartz clock can drive it, so an attempt limit a test wants to reach must be
// a short real duration.
const registerAttemptTimeout = 30 * time.Second

// registerOnce makes one attempt under timeout.
func registerOnce(
	ctx context.Context,
	client leapmuxv1connect.WorkerConnectorServiceClient,
	req *connect.Request[leapmuxv1.RegisterRequest],
	timeout time.Duration,
) (*connect.Response[leapmuxv1.RegisterResponse], error) {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return client.Register(attemptCtx, req)
}
