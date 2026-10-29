package auth

import (
	"context"

	"connectrpc.com/connect/v2"
)

// NewShutdownInterceptor creates a ConnectRPC interceptor that rejects all
// unary and streaming RPCs with CodeUnavailable once shutdownCh is closed.
// It should be the first interceptor in the chain so requests are rejected
// before auth or timeout processing.
func NewShutdownInterceptor(shutdownCh <-chan struct{}) connect.ServerInterceptor {
	isShuttingDown := func() bool {
		select {
		case <-shutdownCh:
			return true
		default:
			return false
		}
	}
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			if isShuttingDown() {
				return connect.Errorf(connect.CodeUnavailable, "hub is shutting down")
			}
			return next(ctx, spec, stream)
		}
	}
}
