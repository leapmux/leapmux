package hub

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/hub/fakehubtest"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

const receiveBudget = 30 * time.Second

func requireReceive[T any](t *testing.T, ch <-chan T, message string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(receiveBudget):
		t.Fatal(message)
		var zero T
		return zero
	}
}

func connectAndPark(t *testing.T) (client *Client, cancel context.CancelFunc, connectDone <-chan error, streamEnd <-chan error) {
	t.Helper()
	ended := make(chan error, 1)
	url := fakehubtest.Start(t, func(ctx context.Context, stream *fakehubtest.Stream) {
		if _, err := stream.Receive(ctx); err != nil {
			ended <- err
			return
		}
		if err := stream.Greet(ctx, "test-owner"); err != nil {
			ended <- err
			return
		}
		ended <- stream.ReadUntilClosed(ctx)
	})
	client = newTestClient(t, url)
	connected := make(chan struct{}, 1)
	client.OnConnectionState = func(ok bool, _ error) {
		if ok {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- client.Connect(ctx, "test-token") }()
	requireReceive(t, connected, "worker did not receive its greeting")
	return client, cancel, result, ended
}

func TestConnect_SendsTheHelloFirst(t *testing.T) {
	first := make(chan *leapmuxv1.ConnectRequest, 1)
	url := fakehubtest.Start(t, func(ctx context.Context, stream *fakehubtest.Stream) {
		message, err := stream.Receive(ctx)
		if !assert.NoError(t, err) {
			return
		}
		first <- message
		_ = stream.Greet(ctx, "test-owner")
		_ = stream.ReadUntilClosed(ctx)
	})
	client := newTestClient(t, url)
	client.PublicKeys = workerkeystest.Keys(workerkeystest.SeededWorkerFill)
	client.EncryptionMode = leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- client.Connect(ctx, "token") }()
	hello := requireReceive(t, first, "hub did not receive the hello").GetHello()
	require.NotNil(t, hello)
	assert.True(t, proto.Equal(client.PublicKeys, hello.GetKeys()))
	assert.Equal(t, client.EncryptionMode, hello.GetEncryptionMode())
	cancel()
	assert.ErrorIs(t, requireReceive(t, result, "Connect did not stop"), context.Canceled)
}

func TestConnect_CancellingTheContextEndsTheStreamForTheHub(t *testing.T) {
	_, cancel, result, ended := connectAndPark(t)
	cancel()
	assert.Equal(t, websocket.StatusNormalClosure, websocket.CloseStatus(requireReceive(t, ended, "Hub did not see the close")))
	assert.ErrorIs(t, requireReceive(t, result, "Connect did not stop"), context.Canceled)
}

func TestConnect_CancelWhileSendingIsSafe(t *testing.T) {
	client, cancel, result, ended := connectAndPark(t)
	stopSending := make(chan struct{})
	sendingDone := make(chan struct{})
	go func() {
		defer close(sendingDone)
		for {
			select {
			case <-stopSending:
				return
			default:
			}
			_ = client.Send(inventoryMsg())
		}
	}()
	cancel()
	require.ErrorIs(t, requireReceive(t, result, "Connect did not stop"), context.Canceled)
	close(stopSending)
	requireReceive(t, sendingDone, "Send stayed blocked")
	require.Error(t, requireReceive(t, ended, "Hub did not see the stream end"))
}

func TestConnect_DeliversTheFramesSentBeforeAStop(t *testing.T) {
	frames := make(chan []string, 1)
	ended := make(chan error, 1)
	url := fakehubtest.Start(t, func(ctx context.Context, stream *fakehubtest.Stream) {
		if _, err := stream.Receive(ctx); !assert.NoError(t, err) {
			return
		}
		if !assert.NoError(t, stream.Greet(ctx, "owner")) {
			return
		}
		ids := make([]string, 0, 3)
		for range 3 {
			frame, err := stream.Receive(ctx)
			if !assert.NoError(t, err) {
				return
			}
			assert.NotNil(t, frame.GetChannelMessageResp())
			ids = append(ids, frame.GetRequestId())
		}
		frames <- ids
		ended <- stream.ReadUntilClosed(ctx)
	})
	client := newTestClient(t, url)
	connected := make(chan struct{}, 1)
	client.OnConnectionState = func(ok bool, _ error) {
		if ok {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- client.Connect(ctx, "token") }()
	requireReceive(t, connected, "worker did not connect")
	for i := range 3 {
		require.NoError(t, client.Send(&leapmuxv1.ConnectRequest{RequestId: fmt.Sprint(i), Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{ChannelMessageResp: &leapmuxv1.ChannelMessage{ChannelId: "channel", Ciphertext: []byte{byte(i)}}}}))
	}
	require.NoError(t, client.FlushSends(ShutdownFlushTimeout))
	cancel()
	assert.Equal(t, []string{"0", "1", "2"}, requireReceive(t, frames, "Hub did not receive the flushed frames"))
	assert.Equal(t, websocket.StatusNormalClosure, websocket.CloseStatus(requireReceive(t, ended, "Hub did not see the close handshake")))
	assert.ErrorIs(t, requireReceive(t, result, "Connect did not stop"), context.Canceled)
}
