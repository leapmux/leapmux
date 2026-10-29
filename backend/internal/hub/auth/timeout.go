package auth

import (
	"context"
	"time"

	"connectrpc.com/connect/v2"
)

// NewTimeoutInterceptor creates a ConnectRPC interceptor that applies
// a default timeout to unary RPCs that have no client-supplied deadline.
// The provided function is called on each request to get the current timeout.
//
// Streaming RPCs are not affected: a stream is long-lived by design, so a
// fixed default deadline would cut it off mid-flight. Connect v2 gives one
// interceptor every RPC, so the unary/streaming split is a StreamType check.
func NewTimeoutInterceptor(defaultTimeout func() time.Duration) connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			if spec.StreamType != connect.StreamTypeUnary {
				return next(ctx, spec, stream)
			}
			if _, ok := ctx.Deadline(); ok {
				return next(ctx, spec, stream)
			}
			ctx, cancel := context.WithTimeout(ctx, defaultTimeout())
			defer cancel()
			return next(ctx, spec, stream)
		}
	}
}
