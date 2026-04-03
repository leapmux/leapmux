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

func (c *Client) handleChannelAccessUpdate(update *leapmuxv1.ChannelAccessUpdate) {
	if c.channelMgr == nil {
		slog.Warn("channel access update received but no channel manager configured")
		return
	}

	c.channelMgr.AddAccessibleWorkspaceID(update.GetChannelId(), update.GetWorkspaceId())
}
