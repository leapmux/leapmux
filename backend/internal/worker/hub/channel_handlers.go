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
	// before subsequent messages arrive. TrySendOrReset keeps the receive
	// loop free of network I/O: a drop under budget cancels the connection
	// so reconnect recovers the half-open session rather than leaving the
	// frontend hung until its own timeout.
	resp := c.channelMgr.HandleOpen(req)
	if !c.TrySendOrReset(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
			ChannelOpenResp: resp,
		},
	}) {
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

func (c *Client) handleChannelAccessUpdate(requestID string, update *leapmuxv1.ChannelAccessUpdate) {
	if c.channelMgr == nil {
		slog.Warn("channel access update received but no channel manager configured")
		return
	}

	c.channelMgr.AddAccessibleWorkspaceID(update.GetChannelId(), update.GetWorkspaceId())

	// Ack before returning so the hub-side PrepareWorkspaceAccess caller can
	// observe that the accessible set is updated before it issues the next
	// inner RPC. The mutation still precedes the enqueue, which is what the
	// hub-side ordering requires. TrySendOrReset keeps the receive loop free
	// of network I/O; a drop cancels the connection so the hub's wait fails
	// fast into reconnect rather than eating its full timeout.
	if requestID == "" {
		return
	}
	if !c.TrySendOrReset(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_ChannelAccessUpdateAck{
			ChannelAccessUpdateAck: &leapmuxv1.ChannelAccessUpdateAck{},
		},
	}) {
		slog.Warn("dropped channel access update ack: connect writer over budget; resetting connection",
			"channel_id", update.GetChannelId(),
			"workspace_id", update.GetWorkspaceId(),
		)
	}
}
