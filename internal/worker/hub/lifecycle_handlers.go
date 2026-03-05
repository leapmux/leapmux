package hub

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func (c *Client) handleDeregister(requestID string, _ *leapmuxv1.DeregisterNotification) {
	slog.Info("received deregistration notification from hub")

	// Stop all agents and terminals.
	c.agents.StopAll()
	c.terminals.StopAll()

	// Send ack.
	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_DeregisterAck{
			DeregisterAck: &leapmuxv1.DeregisterAck{},
		},
	})

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
