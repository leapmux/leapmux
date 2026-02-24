package auth

import (
	"context"
	"time"

	"connectrpc.com/connect"
)

// timeoutInterceptor enforces a default deadline on unary RPCs when
// the client does not send a timeout header (Connect-Timeout-Ms or
// grpc-timeout). Streaming RPCs are not affected.
type timeoutInterceptor struct {
	defaultTimeout func() time.Duration
}

// NewTimeoutInterceptor creates a ConnectRPC interceptor that applies
// a default timeout to unary RPCs that have no client-supplied deadline.
// The provided function is called on each request to get the current timeout.
func NewTimeoutInterceptor(defaultTimeout func() time.Duration) connect.Interceptor {
	return &timeoutInterceptor{defaultTimeout: defaultTimeout}
}

func (t *timeoutInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, t.defaultTimeout())
			defer cancel()
		}
		return next(ctx, req)
	}
}

func (t *timeoutInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (t *timeoutInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next // No timeout on streaming RPCs.
}
