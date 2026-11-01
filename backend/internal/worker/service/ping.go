package service

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// registerPingHandler answers WorkerSystemService.Ping with an empty
// PingResponse (an empty payload is its encoding). It is
// deliberately unauthenticated beyond the channel itself -- the Hub has already
// authenticated the caller and named them to this worker -- and does no work, so
// it cannot fail for any reason other than the session being unusable, which is
// exactly what it is asked to prove. Recorded as gateNone (ungated by design).
func registerPingHandler(r registrar, _ *Service) {
	registerUnguarded(r, leapmuxv1connect.WorkerSystemServicePingProcedure, func(_ context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		_ = sender.SendResponse(&leapmuxv1.InnerRpcResponse{})
	})
}
