package metrics

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
)

// interceptor implements connect.Interceptor and records metrics for
// both unary and streaming RPCs.
type interceptor struct{}

// NewInterceptor returns a ConnectRPC interceptor that records RPC
// request count and duration per service/method/code.
func NewInterceptor() connect.Interceptor {
	return &interceptor{}
}

func (i *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		svc, method := ParseProcedure(req.Spec().Procedure)
		start := time.Now()

		resp, err := next(ctx, req)

		code := "ok"
		if err != nil {
			code = connect.CodeOf(err).String()
		}

		RPCRequestsTotal.WithLabelValues(svc, method, code).Inc()
		RPCRequestDuration.WithLabelValues(svc, method).Observe(time.Since(start).Seconds())

		return resp, err
	}
}

func (i *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next // Client-side streaming is not used by the hub.
}

func (i *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		svc, method := ParseProcedure(conn.Spec().Procedure)
		start := time.Now()

		err := next(ctx, conn)

		code := "ok"
		if err != nil {
			code = connect.CodeOf(err).String()
		}

		RPCRequestsTotal.WithLabelValues(svc, method, code).Inc()
		RPCRequestDuration.WithLabelValues(svc, method).Observe(time.Since(start).Seconds())

		return err
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
