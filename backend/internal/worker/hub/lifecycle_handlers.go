package hub

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func (c *Client) handleDeregister(requestID string, _ *leapmuxv1.DeregisterNotification) {
	slog.Info("received deregistration notification from hub")

	// Ack on the receive goroutine must not block: TrySendOrReset keeps the
	// shared Connect receive loop free of EnqueueWait. The Hub deregisters
	// regardless of whether the ack lands; a drop resets the connection.
	if !c.TrySendOrReset(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_DeregisterAck{
			DeregisterAck: &leapmuxv1.DeregisterAck{},
		},
	}) {
		slog.Warn("dropped deregister ack: connect writer over budget; resetting connection")
	}

	// Trigger graceful shutdown.
	if c.OnDeregister != nil {
		c.OnDeregister()
	}
}

func (c *Client) handleHubShuttingDown(msg *leapmuxv1.HubShuttingDownNotification) {
	delay := msg.GetRetryDelaySeconds()
	slog.Info("hub is shutting down, will delay reconnect", "retry_delay_seconds", delay)
	c.hubRetryDelay.Store(int64(delay))
}
