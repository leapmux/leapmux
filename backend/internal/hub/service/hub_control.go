package service

import (
	"log/slog"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
)

// notifyWorkersChanged sends a HubControlFrame with WorkersChanged to the
// specified user via the reserved "_hub" channel ID.
func notifyWorkersChanged(cMgr *channelmgr.Manager, userID string) {
	if cMgr == nil {
		return
	}
	frame := &leapmuxv1.HubControlFrame{
		Event: &leapmuxv1.HubControlFrame_WorkersChanged{
			WorkersChanged: &leapmuxv1.WorkersChanged{},
		},
	}
	data, err := proto.Marshal(frame)
	if err != nil {
		slog.Error("failed to marshal HubControlFrame", "error", err)
		return
	}
	cMgr.SendToUser(userID, &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       channelmgr.HubControlChannelID,
		Ciphertext:      data,
	})
}
