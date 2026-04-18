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
	// before subsequent messages arrive, but dispatch the response send
	// in a goroutine. Sending synchronously would block the receive
	// loop on the send mutex, which can deadlock when handler goroutines
	// are concurrently sending responses on the same bidi stream.
	resp := c.channelMgr.HandleOpen(req)

	go func() {
		_ = c.Send(&leapmuxv1.ConnectRequest{
			RequestId: requestID,
			Payload: &leapmuxv1.ConnectRequest_ChannelOpenResp{
				ChannelOpenResp: resp,
			},
		})
	}()
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

	// Ack synchronously so the hub-side PrepareWorkspaceAccess caller can
	// observe that the accessible set is updated before it issues the next
	// inner RPC. Without this ack the worker's hardened access checks race
	// the frontend's follow-up RPC.
	if requestID == "" {
		return
	}
	if err := c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_ChannelAccessUpdateAck{
			ChannelAccessUpdateAck: &leapmuxv1.ChannelAccessUpdateAck{},
		},
	}); err != nil {
		slog.Warn("failed to send channel access update ack",
			"channel_id", update.GetChannelId(),
			"workspace_id", update.GetWorkspaceId(),
			"error", err)
	}
}
