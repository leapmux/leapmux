package channel

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
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
			dispatcher.Handle("stream", Registration{Streaming: true}, func(ctx context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, writer ResponseWriter) {
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
			manager.HandleMessage(&leapmuxv1.ChannelMessage{ChannelId: channelID, CorrelationId: 7, Ciphertext: opening})
			<-entered
			frame, err := encryptInner(t, session, &leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_StreamRequest{
				StreamRequest: map[bool]*leapmuxv1.InnerStreamRequest{false: revisionFrame([]byte("latest interest")), true: cancelFrame()}[cancel],
			}})
			require.NoError(t, err)
			manager.HandleMessage(&leapmuxv1.ChannelMessage{ChannelId: channelID, CorrelationId: 7, Ciphertext: frame})
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
	dispatcher.Handle("stream", Registration{Streaming: true}, func(ctx context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, writer ResponseWriter) {
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
	manager.HandleMessage(&leapmuxv1.ChannelMessage{ChannelId: channelID, CorrelationId: 7, Ciphertext: opening})
	<-ready
	deliver := func() {
		frame, err := encryptInner(t, session, &leapmuxv1.InnerMessage{Kind: &leapmuxv1.InnerMessage_StreamRequest{
			StreamRequest: revisionFrame([]byte("revision")),
		}})
		require.NoError(t, err)
		manager.HandleMessage(&leapmuxv1.ChannelMessage{ChannelId: channelID, CorrelationId: 7, Ciphertext: frame})
	}
	deliver()
	continueBinding()
	<-entered
	for range maxPendingStreamFrames + 1 {
		deliver()
	}
	messages := sender.waitForMessages(2)
	// The client holds correlation id 7 as a stream, so the overflow error must
	// be a stream error frame: a unary error on a stream id carries no End frame.
	envelope := decryptInnerMessage(t, session, messages[0])
	require.Nil(t, envelope.GetResponse(), "the overflow error must not be a unary reply on a stream id")
	frame := envelope.GetStream()
	require.NotNil(t, frame.GetError(), "a clean End must not hide the overflow error")
	require.Equal(t, int32(codes.ResourceExhausted), frame.GetError().GetCode())
	require.True(t, frame.GetEnd(), "the overflow error frame ends the stream")
}

// decryptInnerMessage decrypts one reply the worker sent and returns its
// envelope, whichever kind it is.
func decryptInnerMessage(t *testing.T, session *noiseutil.Session, message *leapmuxv1.ConnectRequest) *leapmuxv1.InnerMessage {
	t.Helper()
	plaintext, _ := decryptChannelMessage(t, session, message)
	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(plaintext, &envelope))
	return &envelope
}

// A server-streaming request that arrives after the session closed its stream
// registry, but before the session ends, cannot prepare its stream. The client
// holds the request's correlation id as a stream, so the refusal must be a
// stream error frame: a unary error on that id carries no End frame and leaves
// a Go tunnel client waiting for its own timeout.
func TestDispatchAsync_RefusesAStreamItCannotPrepareWithAStreamErrorFrame(t *testing.T) {
	manager, key, sender := setupTestManager(t)
	const channelID = "stream-prepare-refused"
	t.Cleanup(func() { manager.HandleClose(channelID) })
	invoked := make(chan struct{}, 1)
	dispatcher := NewDispatcher()
	dispatcher.Handle("stream", Registration{Streaming: true}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
		invoked <- struct{}{}
	})
	manager.SetDispatcher(dispatcher)
	session := performHandshake(t, manager, key, channelID, "user")

	// HandleClose closes the registry before it cancels the session, so the
	// drainer still runs while a request decrypted in between finds the
	// registry closed. Close it alone to hold the session in that state.
	live, ok := manager.getSession(channelID)
	require.True(t, ok)
	live.streams.releaseAll()

	opening := sendRequest(t, session, channelID, &leapmuxv1.InnerRpcRequest{Method: "stream"})
	manager.HandleMessage(&leapmuxv1.ChannelMessage{ChannelId: channelID, CorrelationId: 7, Ciphertext: opening})

	messages := sender.waitForMessages(1)
	require.Len(t, messages, 1)
	assert.Equal(t, uint64(7), messages[0].GetChannelMessageResp().GetCorrelationId())
	envelope := decryptInnerMessage(t, session, messages[0])
	require.Nil(t, envelope.GetResponse(), "a unary error on a stream id carries no End frame")
	frame := envelope.GetStream()
	require.NotNil(t, frame, "the refusal must be a stream frame, got %T", envelope.GetKind())
	require.NotNil(t, frame.GetError())
	assert.Equal(t, int32(codes.FailedPrecondition), frame.GetError().GetCode())
	assert.True(t, frame.GetEnd(), "the error frame ends the stream")
	assert.Empty(t, invoked, "a stream that cannot be prepared never reaches its handler")
}
