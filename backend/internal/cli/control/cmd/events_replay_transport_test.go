package cmd

import (
	"context"
	"io"
	"testing"
	"testing/synctest"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
			result <- readEncryptedSemanticWatch(ctx, fixture, nil, func(raw []byte, _ *leapmuxv1.RpcError, end bool) error {
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

func TestEventReplayTransportRetriesATargetChannelCancellation(t *testing.T) {
	t.Parallel()
	assert.True(t, eventReplayTransportError(context.Canceled).Retryable())
	assert.True(t, eventReplayTransportError(connect.NewError(connect.CodeCanceled, "target channel closed")).Retryable())
	assert.False(t, eventReplayTransportError(connect.NewError(connect.CodePermissionDenied, "grant revoked")).Retryable())
}
