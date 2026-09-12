package channel

import (
	"context"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func TestStreamStartupRetainsEarlyFramesAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "update", true: "cancel"}[cancel], func(t *testing.T) {
			manager, key, _ := setupTestManager(t)
			const channelID = "stream-start"
			t.Cleanup(func() { manager.HandleClose(channelID) })
			controller := &streamRequestRecordingController{}
			entered, proceed := make(chan struct{}), make(chan struct{})
			continueBinding := sync.OnceFunc(func() { close(proceed) })
			t.Cleanup(continueBinding)
			bound := make(chan bool, 1)
			dispatcher := NewDispatcher()
			dispatcher.RegisterStream("stream", func(ctx context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, writer ResponseWriter) {
				close(entered)
				<-proceed
				release, ok := writer.BindStream(controller)
				bound <- ok
				if !ok {
					return
				}
				defer release()
				<-ctx.Done()
			})
			manager.SetDispatcher(dispatcher)
			session := performHandshake(t, manager, key, channelID, "user")
			opening := sendRequest(t, session, channelID, &leapmuxv1.InnerRpcRequest{Method: "stream"})
			manager.HandleMessage(&leapmuxv1.ChannelMessage{ProtocolVersion: 1, ChannelId: channelID, CorrelationId: 7, Ciphertext: opening})
			<-entered
			frame, err := encryptInner(t, session, &leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_StreamRequest{
				StreamRequest: &leapmuxv1.InnerStreamRequest{Payload: []byte("latest interest"), Cancel: cancel},
			}})
			require.NoError(t, err)
			manager.HandleMessage(&leapmuxv1.ChannelMessage{ProtocolVersion: 1, ChannelId: channelID, CorrelationId: 7, Ciphertext: frame})
			continueBinding()
			if cancel {
				require.False(t, <-bound, "an early cancellation must prevent a later binding")
			} else {
				require.True(t, <-bound)
				require.Eventually(t, func() bool {
					controller.mu.Lock()
					defer controller.mu.Unlock()
					return len(controller.payloads) == 1 && string(controller.payloads[0]) == "latest interest"
				}, time.Second, time.Millisecond)
			}
		})
	}
}

type heldStreamController struct {
	entered  chan struct{}
	proceed  <-chan struct{}
	onCancel func()
}

func (controller *heldStreamController) OnClientFrame([]byte) {
	close(controller.entered)
	<-controller.proceed
}

func (controller *heldStreamController) OnCancel() { controller.onCancel() }

func TestStreamOverflowReportsAnErrorBeforeCancellationEndsTheStream(t *testing.T) {
	manager, key, sender := setupTestManager(t)
	const channelID = "stream-overflow"
	proceed := make(chan struct{})
	continueDelivery := sync.OnceFunc(func() { close(proceed) })
	t.Cleanup(func() { continueDelivery(); manager.HandleClose(channelID) })
	entered := make(chan struct{})
	controller := &heldStreamController{entered: entered, proceed: proceed}
	dispatcher := NewDispatcher()
	ready, bind := make(chan struct{}), make(chan struct{})
	continueBinding := sync.OnceFunc(func() { close(bind) })
	t.Cleanup(continueBinding)
	dispatcher.RegisterStream("stream", func(ctx context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, writer ResponseWriter) {
		controller.onCancel = func() { _ = writer.SendStream(&leapmuxv1.InnerStreamMessage{End: true}) }
		close(ready)
		<-bind
		release, ok := writer.BindStream(controller)
		if ok {
			defer release()
			<-ctx.Done()
		}
	})
	manager.SetDispatcher(dispatcher)
	session := performHandshake(t, manager, key, channelID, "user")
	opening := sendRequest(t, session, channelID, &leapmuxv1.InnerRpcRequest{Method: "stream"})
	manager.HandleMessage(&leapmuxv1.ChannelMessage{ProtocolVersion: 1, ChannelId: channelID, CorrelationId: 7, Ciphertext: opening})
	<-ready
	deliver := func() {
		frame, err := encryptInner(t, session, &leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_StreamRequest{
			StreamRequest: &leapmuxv1.InnerStreamRequest{Payload: []byte("revision")},
		}})
		require.NoError(t, err)
		manager.HandleMessage(&leapmuxv1.ChannelMessage{ProtocolVersion: 1, ChannelId: channelID, CorrelationId: 7, Ciphertext: frame})
	}
	deliver()
	continueBinding()
	<-entered
	for range maxPendingStreamFrames + 1 {
		deliver()
	}
	require.Eventually(t, func() bool { return len(sender.messages()) >= 2 }, time.Second, time.Millisecond)
	message := sender.messages()[0].GetChannelMessageResp()
	require.NotNil(t, message)
	plaintext, err := session.Decrypt(message.GetCiphertext())
	require.NoError(t, err)
	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(plaintext, &envelope))
	if response := envelope.GetResponse(); response != nil {
		require.True(t, response.GetIsError())
		require.Equal(t, int32(codes.ResourceExhausted), response.GetErrorCode())
	} else {
		require.True(t, envelope.GetStream().GetIsError(), "a clean End must not hide the overflow error")
		require.Equal(t, int32(codes.ResourceExhausted), envelope.GetStream().GetErrorCode())
	}
}
