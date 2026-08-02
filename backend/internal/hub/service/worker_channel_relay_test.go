package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
)

func TestProcessWorkerMessage_RoutingFailureClosesChannelAndChunkState(t *testing.T) {
	t.Parallel()

	channels := channelmgr.New(0)
	channels.RegisterWithAuthInfo("channel", "worker", "user", channelmgr.AuthInfo{}, nil)
	var mu sync.Mutex
	var sent []*leapmuxv1.ConnectResponse
	conn := workermgrtest.NewConnWithWrite(t, "worker", func(msg *leapmuxv1.ConnectResponse) error {
		mu.Lock()
		sent = append(sent, msg)
		mu.Unlock()
		return nil
	})
	svc := &WorkerConnectorService{channelMgr: channels}

	err := svc.processWorkerMessage(context.Background(), conn, "worker", "user", &leapmuxv1.ConnectRequest{
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
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(sent) == 1
	}, time.Second, 5*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "channel", sent[0].GetChannelClose().GetChannelId())
	require.NoError(t, channels.ChunkTracker.Track("channel", "w2fe", 2, 32, true),
		"terminal close must remove the previous in-flight chunk sequence")
}

func TestProcessWorkerMessage_RejectsChannelOwnedByAnotherWorker(t *testing.T) {
	t.Parallel()

	channels := channelmgr.New(0)
	channels.RegisterWithAuthInfo("channel", "owner", "user", channelmgr.AuthInfo{}, nil)
	conn := workermgrtest.NewConnWithWrite(t, "attacker", func(*leapmuxv1.ConnectResponse) error {
		t.Fatal("attacking worker must not receive a close for another worker's channel")
		return nil
	})
	svc := &WorkerConnectorService{channelMgr: channels}

	err := svc.processWorkerMessage(context.Background(), conn, "attacker", "user", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{
			ChannelMessageResp: &leapmuxv1.ChannelMessage{ChannelId: "channel", Ciphertext: []byte("injected")},
		},
	})

	require.NoError(t, err)
	assert.True(t, channels.Exists("channel"))
}
