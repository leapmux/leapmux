package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr/workermgrtest"
	"github.com/leapmux/leapmux/internal/util/testutil"
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
	svc := &WorkerConnectorService{
		hasher:     hubtestutil.TestSecretHasher(),
		channelMgr: channels}

	err := svc.processWorkerMessage(context.Background(), conn, "worker", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{
			ChannelMessageResp: &leapmuxv1.ChannelMessage{
				ChannelId:     "channel",
				CorrelationId: 1,
				Ciphertext:    []byte("chunk"),
				Flags:         leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
			},
		},
	}, nil)

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

func TestProcessWorkerMessage_WorkerCloseRetiresTheBoundChannel(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)

	channels := channelmgr.New(0)
	channels.RegisterWithAuthInfo("channel", "worker", "user", channelmgr.AuthInfo{}, nil)
	channels.RegisterWithAuthInfo("other", "worker", "user", channelmgr.AuthInfo{}, nil)
	var sent []*leapmuxv1.ChannelMessage
	channels.BindUser("user", "relay", func(msg *leapmuxv1.ChannelMessage) error {
		sent = append(sent, msg)
		return nil
	}, nil)
	_, bound, err := channels.UseAuthorizedChannel("channel", "relay", func(channelmgr.ChannelInfo) bool {
		return true
	}, func(channelmgr.ChannelInfo) error { return nil })
	require.NoError(t, err)
	require.True(t, bound)

	svc := &WorkerConnectorService{hasher: hubtestutil.TestSecretHasher(), channelMgr: channels}
	var workerReplies []*leapmuxv1.ConnectResponse
	conn := workermgrtest.NewConnWithWrite(t, "worker", func(msg *leapmuxv1.ConnectResponse) error {
		workerReplies = append(workerReplies, msg)
		return nil
	})
	closeRequest := &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{ChannelMessageResp: channelwire.CloseNotification("channel")},
	}
	otherConn := workermgrtest.NewConnWithWrite(t, "other-worker", func(*leapmuxv1.ConnectResponse) error { return nil })
	require.NoError(t, svc.processWorkerMessage(t.Context(), otherConn, "other-worker", closeRequest, nil))
	assert.True(t, channels.Exists("channel"))
	assert.Empty(t, sent)

	require.NoError(t, svc.processWorkerMessage(t.Context(), conn, "worker", closeRequest, nil))
	flushCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, conn.Flush(flushCtx))
	assert.Empty(t, workerReplies)

	assert.False(t, channels.Exists("channel"))
	assert.True(t, channels.Exists("other"))
	assert.NotContains(t, logs.String(), "terminal worker-to-frontend failure")
	require.Len(t, sent, 1)
	assert.Equal(t, "channel", sent[0].GetChannelId())
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE, sent[0].GetFlags())
}

func TestProcessWorkerMessage_RejectsChannelOwnedByAnotherWorker(t *testing.T) {
	t.Parallel()

	channels := channelmgr.New(0)
	channels.RegisterWithAuthInfo("channel", "owner", "user", channelmgr.AuthInfo{}, nil)
	conn := workermgrtest.NewConnWithWrite(t, "attacker", func(*leapmuxv1.ConnectResponse) error {
		t.Fatal("attacking worker must not receive a close for another worker's channel")
		return nil
	})
	svc := &WorkerConnectorService{
		hasher:     hubtestutil.TestSecretHasher(),
		channelMgr: channels}

	err := svc.processWorkerMessage(context.Background(), conn, "attacker", &leapmuxv1.ConnectRequest{
		Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{
			ChannelMessageResp: &leapmuxv1.ChannelMessage{ChannelId: "channel", Ciphertext: []byte("injected")},
		},
	}, nil)

	require.NoError(t, err)
	assert.True(t, channels.Exists("channel"))
}
