package service

import (
	"context"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

// registerPingHandler answers channelwire.PingMethod with an empty response. It is
// deliberately unauthenticated beyond the channel itself -- the Hub has already
// authenticated the caller and named them to this worker -- and does no work, so
// it cannot fail for any reason other than the session being unusable, which is
// exactly what it is asked to prove.
func registerPingHandler(d *channel.Dispatcher, _ *Context) {
	d.Register(channelwire.PingMethod, func(_ context.Context, _ string, _ *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		_ = sender.SendResponse(&leapmuxv1.InnerRpcResponse{})
	})
}
