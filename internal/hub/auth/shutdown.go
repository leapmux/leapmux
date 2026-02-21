package auth

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
)

// shutdownInterceptor rejects all RPCs once the shutdown channel is closed.
type shutdownInterceptor struct {
	shutdownCh <-chan struct{}
}

// NewShutdownInterceptor creates a ConnectRPC interceptor that rejects all
// unary and streaming RPCs with CodeUnavailable once shutdownCh is closed.
// It should be the first interceptor in the chain so requests are rejected
// before auth or timeout processing.
func NewShutdownInterceptor(shutdownCh <-chan struct{}) connect.Interceptor {
	return &shutdownInterceptor{shutdownCh: shutdownCh}
}

func (s *shutdownInterceptor) isShuttingDown() bool {
	select {
	case <-s.shutdownCh:
		return true
	default:
		return false
	}
}

func (s *shutdownInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if s.isShuttingDown() {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("hub is shutting down"))
		}
		return next(ctx, req)
	}
}

func (s *shutdownInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (s *shutdownInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if s.isShuttingDown() {
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("hub is shutting down"))
		}
		return next(ctx, conn)
	}
}
