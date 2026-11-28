package cmd

import (
	"context"
	"io"
	"testing"
	"testing/synctest"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/tunnel"
)

type replayChannelFixture struct {
	ctx       context.Context
	callbacks chan tunnel.RPCHandlers
}

func (c *replayChannelFixture) Context() context.Context { return c.ctx }

func (c *replayChannelFixture) SendRPCNoWait(_ context.Context, _ string, _ []byte, handlers tunnel.RPCHandlers) (uint64, error) {
	c.callbacks <- handlers
	return 1, nil
}

func (*replayChannelFixture) UnregisterStream(uint64) {}

func TestEncryptedSemanticWatchPreservesABurstWhileOutputWaits(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		fixture := &replayChannelFixture{ctx: ctx, callbacks: make(chan tunnel.RPCHandlers)}
		firstReached := make(chan struct{})
		releaseFirst := make(chan struct{})
		result := make(chan error, 1)
		var received []byte
		go func() {
			result <- readEncryptedSemanticWatch(ctx, fixture, semanticWatchProcedure, nil, func(raw []byte, _ *leapmuxv1.RpcError, end bool) error {
				if end {
					return io.EOF
				}
				if len(received) == 0 {
					close(firstReached)
					<-releaseFirst
				}
				received = append(received, raw...)
				return nil
			})
		}()
		handlers := <-fixture.callbacks
		const size = 96
		writeDone := make(chan struct{})
		go func() {
			defer close(writeDone)
			for seq := range size {
				handlers.Stream(&leapmuxv1.InnerStreamMessage{Payload: []byte{byte(seq)}})
			}
			handlers.Stream(&leapmuxv1.InnerStreamMessage{End: true})
		}()
		<-firstReached
		synctest.Wait()
		close(releaseFirst)
		<-writeDone
		require.ErrorIs(t, <-result, io.EOF)
		require.Len(t, received, size)
		for seq, seen := range received {
			assert.Equal(t, byte(seq), seen)
		}
	})
}

func TestEventReplayTransportClassifiesRemoteStreamEndSeparatelyFromLocalCancel(t *testing.T) {
	t.Parallel()
	remoteEOF := &remoteStreamEndedError{cause: io.EOF}
	assert.True(t, eventReplayTransportError(remoteEOF).Retryable())
	assert.Equal(t, cli.CodeUnavailable, eventReplayTransportError(remoteEOF).Code)
	assert.ErrorIs(t, remoteEOF, io.EOF)

	remoteCancel := &remoteStreamEndedError{cause: connect.NewError(connect.CodeCanceled, "target channel closed")}
	assert.True(t, eventReplayTransportError(remoteCancel).Retryable())
	assert.Equal(t, cli.CodeUnavailable, eventReplayTransportError(remoteCancel).Code)

	assert.False(t, eventReplayTransportError(connect.NewError(connect.CodePermissionDenied, "grant revoked")).Retryable())
}

func TestEncryptedSemanticWatchDrainsBufferedFramesWhenChannelCloses(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx := t.Context()
		chCtx, chCancel := context.WithCancel(ctx)
		fixture := &replayChannelFixture{ctx: chCtx, callbacks: make(chan tunnel.RPCHandlers)}
		firstReached := make(chan struct{})
		releaseFirst := make(chan struct{})
		result := make(chan error, 1)
		var received []byte
		go func() {
			result <- readEncryptedSemanticWatch(ctx, fixture, semanticWatchProcedure, nil, func(raw []byte, rpcErr *leapmuxv1.RpcError, end bool) error {
				if err := semanticFrameError(rpcErr); err != nil {
					return err
				}
				if len(raw) != 0 {
					if len(received) == 0 {
						close(firstReached)
						<-releaseFirst
					}
					received = append(received, raw...)
				}
				if end {
					return io.EOF
				}
				return nil
			})
		}()
		handlers := <-fixture.callbacks
		// Send first frame to pause reader, then queue buffered frames (including a
		// non-empty payload on End: true) and tear down the channel immediately.
		handlers.Stream(&leapmuxv1.InnerStreamMessage{Payload: []byte{1}})
		<-firstReached
		handlers.Stream(&leapmuxv1.InnerStreamMessage{Payload: []byte{2}})
		handlers.Stream(&leapmuxv1.InnerStreamMessage{Payload: []byte{3}, End: true})
		chCancel()
		synctest.Wait()
		close(releaseFirst)
		require.ErrorIs(t, <-result, io.EOF)
		assert.Equal(t, []byte{1, 2, 3}, received)
	})
}

func TestSemanticFrameErrorMapsFailedPreconditionToConflictWithoutOverridingResourceExhausted(t *testing.T) {
	t.Parallel()
	precond := semanticFrameError(&leapmuxv1.RpcError{Code: int32(codes.FailedPrecondition), Message: "frame exceeds limit"})
	e := cli.FromError(precond)
	require.NotNil(t, e)
	assert.Equal(t, cli.CodeConflict, e.Code)
	assert.False(t, e.Retryable())
	assert.Equal(t, "frame exceeds limit", e.Message)

	exhausted := semanticFrameError(&leapmuxv1.RpcError{Code: int32(codes.ResourceExhausted), Message: "rate limited"})
	ex := cli.FromError(exhausted)
	require.NotNil(t, ex)
	assert.NotEqual(t, cli.CodeConflict, ex.Code)
}

func TestReadPrivateEventsOnChannel_StalledConsumerOverflowsWithoutBlockingCallback(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		fixture := &replayChannelFixture{ctx: ctx, callbacks: make(chan tunnel.RPCHandlers)}
		firstReached := make(chan struct{})
		releaseFirst := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			result <- readPrivateEventsOnChannel(ctx, fixture, nil, func([]byte) error {
				select {
				case <-firstReached:
				default:
					close(firstReached)
				}
				<-releaseFirst
				return nil
			})
		}()
		handlers := <-fixture.callbacks
		handlers.Stream(&leapmuxv1.InnerStreamMessage{Payload: []byte{1}})
		<-firstReached

		// Pushing more than the handoff buffer while the consumer is stalled must
		// return immediately from handlers.Stream (never wedging the shared channel)
		// and fail only this stream with errPrivateStreamFellBehind.
		for range 80 {
			handlers.Stream(&leapmuxv1.InnerStreamMessage{Payload: []byte{2}})
		}
		close(releaseFirst)
		err := <-result
		require.ErrorIs(t, err, errPrivateStreamFellBehind)
		assert.True(t, cli.FromError(err).Retryable())
	})
}
