package hub

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func (c *Client) handleChannelOpen(requestID string, req *leapmuxv1.ChannelOpenRequest) {
	if c.channelMgr == nil {
		slog.Warn("channel open received but no channel manager configured")
		return
	}

	// Complete the handshake synchronously so the session is registered
	// before subsequent messages arrive. trySendControl keeps the receive
	// loop free of network I/O: a drop under budget cancels the connection
	// so reconnect recovers the half-open session rather than leaving the
	// frontend hung until its own timeout. A drop because the connection is
	// already ending needs no warning: the receive loop reports why it ended.
	resp := c.channelMgr.HandleOpen(req)
	if c.trySendControl(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
			ChannelOpenResp: resp,
		},
	}) == controlSaturated {
		slog.Warn("dropped channel open response: connect writer over budget; resetting connection",
			"channel_id", req.GetChannelId(),
			"request_id", requestID,
		)
	}
}

func (c *Client) handleChannelMessage(msg *leapmuxv1.ChannelMessage) {
	if c.channelMgr == nil {
		slog.Warn("channel message received but no channel manager configured")
		return
	}

	c.channelMgr.HandleMessage(msg)
}

func (c *Client) handleChannelClose(notification *leapmuxv1.ChannelCloseNotification) {
	if c.channelMgr == nil {
		return
	}

	c.channelMgr.HandleClose(notification.GetChannelId())
}
