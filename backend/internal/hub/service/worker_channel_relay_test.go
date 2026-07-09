package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

func TestProcessWorkerMessage_RoutingFailureClosesChannelAndChunkState(t *testing.T) {
	channels := channelmgr.New()
	channels.RegisterWithAuthInfo("channel", "worker", "user", channelmgr.AuthInfo{}, nil)
	var sent []*leapmuxv1.ConnectResponse
	conn := &workermgr.Conn{WorkerID: "worker", SendFn: func(msg *leapmuxv1.ConnectResponse) error {
		sent = append(sent, msg)
		return nil
	}}
	svc := &WorkerConnectorService{channelMgr: channels}

	err := svc.processWorkerMessage(context.Background(), conn, "worker", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{
			ChannelMessageResp: &leapmuxv1.ChannelMessage{
				ChannelId:     "channel",
				CorrelationId: 1,
				Ciphertext:    []byte("chunk"),
				Flags:         leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
			},
		},
	})

	require.NoError(t, err)
	assert.False(t, channels.Exists("channel"))
	require.Len(t, sent, 1)
	assert.Equal(t, "channel", sent[0].GetChannelClose().GetChannelId())
	require.NoError(t, channels.ChunkTracker.Track("channel", "w2fe", 2, 32, true),
		"terminal close must remove the previous in-flight chunk sequence")
}

func TestProcessWorkerMessage_RejectsChannelOwnedByAnotherWorker(t *testing.T) {
	channels := channelmgr.New()
	channels.RegisterWithAuthInfo("channel", "owner", "user", channelmgr.AuthInfo{}, nil)
	conn := &workermgr.Conn{WorkerID: "attacker", SendFn: func(*leapmuxv1.ConnectResponse) error {
		t.Fatal("attacking worker must not receive a close for another worker's channel")
		return nil
	}}
	svc := &WorkerConnectorService{channelMgr: channels}

	err := svc.processWorkerMessage(context.Background(), conn, "attacker", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{
			ChannelMessageResp: &leapmuxv1.ChannelMessage{ChannelId: "channel", Ciphertext: []byte("injected")},
		},
	})

	require.NoError(t, err)
	assert.True(t, channels.Exists("channel"))
}
