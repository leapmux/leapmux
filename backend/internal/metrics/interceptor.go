package metrics

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect/v2"
)

// NewInterceptor returns a ConnectRPC interceptor that records RPC
// request count and duration per service/method/code.
//
// Connect v2 has a single ServerFunc shape for unary and streaming
// calls, so the separate v1 unary and streaming wrappers collapse into
// one. The hub registers it on the server only; client-side streaming
// is not used here.
func NewInterceptor() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			svc, method := ParseProcedure(spec.Procedure)
			start := time.Now()

			err := next(ctx, spec, stream)

			code := "ok"
			if err != nil {
				code = connect.CodeOf(err).String()
			}

			RPCRequestsTotal.WithLabelValues(svc, method, code).Inc()
			RPCRequestDuration.WithLabelValues(svc, method).Observe(time.Since(start).Seconds())

			return err
		}
	}
}

// ParseProcedure extracts the service and method names from a
// ConnectRPC procedure string like "/leapmux.v1.FooService/BarMethod".
func ParseProcedure(procedure string) (service, method string) {
	procedure = strings.TrimPrefix(procedure, "/")
	parts := strings.SplitN(procedure, "/", 2)
	if len(parts) != 2 {
		return "unknown", "unknown"
	}
	svc := parts[0]
	if idx := strings.LastIndex(svc, "."); idx >= 0 {
		svc = svc[idx+1:]
	}
	return svc, parts[1]
}
