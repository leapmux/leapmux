package service

import "context"

// privateEventsController retires a WatchWorkerPrivateEvents subscription
// when the client sends InnerStreamRequest{cancel: true} (or the channel
// tears down). OnClientFrame is a no-op -- this stream takes no updates.
type privateEventsController struct {
	cancel context.CancelFunc
}

func (c *privateEventsController) OnClientFrame([]byte) {}

func (c *privateEventsController) OnCancel() {
	if c.cancel != nil {
		c.cancel()
	}
}
