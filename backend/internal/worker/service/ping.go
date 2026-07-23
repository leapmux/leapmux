package service

import (
	"context"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// registerPingHandler answers channelwire.PingMethod with an empty response. It is
// deliberately unauthenticated beyond the channel itself -- the Hub has already
// authenticated the caller and named them to this worker -- and does no work, so
// it cannot fail for any reason other than the session being unusable, which is
// exactly what it is asked to prove. Recorded as gateNone (ungated by design).
func registerPingHandler(r registrar, _ *Service) {
	registerUngated(r, channelwire.PingMethod, func(_ context.Context, _ userid.UserID, _ *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		_ = sender.SendResponse(&leapmuxv1.InnerRpcResponse{})
	})
}
