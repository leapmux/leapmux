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
	// before subsequent messages arrive. TrySend keeps the receive loop
	// free of network I/O: a drop is possible only when the Connect writer
	// is already past its byte budget, in which case the connection is
	// about to reset anyway.
	resp := c.channelMgr.HandleOpen(req)
	if !c.TrySend(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
			ChannelOpenResp: resp,
		},
	}) {
		slog.Warn("dropped channel open response: connect writer over budget",
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
	// hub-side ordering requires. TrySend keeps the receive loop free of
	// network I/O.
	if requestID == "" {
		return
	}
	if !c.TrySend(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_ChannelAccessUpdateAck{
			ChannelAccessUpdateAck: &leapmuxv1.ChannelAccessUpdateAck{},
		},
	}) {
		slog.Warn("dropped channel access update ack: connect writer over budget",
			"channel_id", update.GetChannelId(),
			"workspace_id", update.GetWorkspaceId(),
		)
	}
}
