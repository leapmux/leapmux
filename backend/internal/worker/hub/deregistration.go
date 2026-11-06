package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport"
)

// Deregister removes the worker its token identifies from the Hub. The
// empty request is deliberate: the token is the only identity, so a
// caller cannot name another worker.
//
// The caller removes its LOCAL credentials only after this succeeds. An
// uncertain outcome -- a transport failure, a cancellation, an
// authentication refusal -- returns an error and the credentials stay,
// because an arbitrary authentication failure is never proof that the
// deletion happened. A repeated call is safe: the Hub accepts one for
// the same retained deleted row, so a retry whose first half succeeded
// completes the second half instead of wedging.
func Deregister(ctx context.Context, endpoint *hubtransport.Endpoint, token string) error {
	client := leapmuxv1connect.NewWorkerConnectorServiceClient(connect.NewClient(connecthttp.NewTransport(
		endpoint.HTTP2OnlyClient(),
		endpoint.BaseURL(),
		connecthttp.WithReadMaxBytes(0),
		connecthttp.WithGRPC(),
	)))
	return deregisterWithClient(ctx, client, token)
}

func deregisterWithClient(ctx context.Context, client leapmuxv1connect.WorkerConnectorServiceClient, token string) error {
	callCtx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Authorization", "Bearer "+token)
	if _, err := client.DeregisterSelf(callCtx, &leapmuxv1.DeregisterSelfRequest{}); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("deregistration was cancelled before the hub confirmed it: %w", errors.Join(ctx.Err(), err))
		}
		return fmt.Errorf("the hub did not confirm the deregistration: %w", err)
	}
	slog.Info("the hub removed this worker")
	return nil
}
