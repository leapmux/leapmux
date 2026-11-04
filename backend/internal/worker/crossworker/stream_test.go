package crossworker

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/tunnel"
)

// scriptedStreamChannel stands in for the dedicated channel of one cross-worker
// stream. SendRPCNoWait answers the request the way a sibling worker would: it
// hands frames to the stream handler and response to the response handler, on
// a goroutine of its own, as tunnel.Channel's dispatcher does.
type scriptedStreamChannel struct {
	ctx      context.Context
	frames   []*leapmuxv1.InnerStreamMessage
	response *leapmuxv1.InnerRpcResponse

	mu                  sync.Mutex
	unregisteredStream  bool
	unregisteredPending bool
}

func (c *scriptedStreamChannel) Context() context.Context { return c.ctx }

func (*scriptedStreamChannel) SendStreamRequest(context.Context, uint64, []byte) error {
	return nil
}

func (*scriptedStreamChannel) CancelStream(context.Context, uint64) error { return nil }

func (c *scriptedStreamChannel) SendRPCNoWait(_ context.Context, _ string, _ []byte, handlers tunnel.RPCHandlers) (uint64, error) {
	go func() {
		for _, frame := range c.frames {
			handlers.Stream(frame)
		}
		if c.response != nil {
			handlers.Response <- c.response
		}
	}()
	return 1, nil
}

func (c *scriptedStreamChannel) UnregisterPending(uint64) {
	c.mu.Lock()
	c.unregisteredPending = true
	c.mu.Unlock()
}

func (c *scriptedStreamChannel) UnregisterStream(uint64) {
	c.mu.Lock()
	c.unregisteredStream = true
	c.mu.Unlock()
}

// frameRecorder collects the frames that runStream forwards to onMsg.
type frameRecorder struct {
	mu     sync.Mutex
	frames []*leapmuxv1.InnerStreamMessage
}

func (r *frameRecorder) onMsg(m *leapmuxv1.InnerStreamMessage) {
	r.mu.Lock()
	r.frames = append(r.frames, m)
	r.mu.Unlock()
}

func (r *frameRecorder) recorded() []*leapmuxv1.InnerStreamMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*leapmuxv1.InnerStreamMessage(nil), r.frames...)
}

func runScriptedStream(t *testing.T, ch *scriptedStreamChannel) ([]*leapmuxv1.InnerStreamMessage, error) {
	t.Helper()
	recorder := &frameRecorder{}
	err := runStream(testutil.DeadlineContext(t), ch, leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, nil, recorder.onMsg, nil)
	ch.mu.Lock()
	assert.True(t, ch.unregisteredStream, "the stream releases its stream handler")
	assert.True(t, ch.unregisteredPending, "the stream releases its response handler")
	ch.mu.Unlock()
	return recorder.recorded(), err
}

// A sibling worker that ends the stream with an error frame must reach the
// caller as that frame, with its code, before the stream ends. The same-worker
// path forwards it too (controlipc.streamCollector), and the CLI reports the
// worker's error from the frame.
func TestRunStream_ForwardsTheErrorFrameThatEndsTheStream(t *testing.T) {
	errorFrame := channelwire.NewStreamError(5, "agent not found")
	ch := &scriptedStreamChannel{
		ctx:    t.Context(),
		frames: []*leapmuxv1.InnerStreamMessage{{Payload: []byte("first")}, errorFrame},
	}

	frames, err := runScriptedStream(t, ch)

	var rpcErr *channelwire.RPCError
	require.ErrorAs(t, err, &rpcErr, "the stream returns the sibling's error with its code")
	assert.Equal(t, int32(5), rpcErr.Code)
	assert.EqualError(t, err, "rpc error (code 5): agent not found")
	require.Len(t, frames, 2, "the error frame reaches the caller after the frames before it")
	assert.Equal(t, []byte("first"), frames[0].GetPayload())
	assert.Same(t, errorFrame, frames[1])
}

// A stream that ends cleanly forwards every frame, the End frame included, and
// returns no error.
func TestRunStream_ForwardsEveryFrameOfACleanStream(t *testing.T) {
	ch := &scriptedStreamChannel{
		ctx: t.Context(),
		frames: []*leapmuxv1.InnerStreamMessage{
			{Payload: []byte("first")},
			{Payload: []byte("last"), End: true},
		},
	}

	frames, err := runScriptedStream(t, ch)

	require.NoError(t, err)
	require.Len(t, frames, 2)
	assert.Equal(t, []byte("first"), frames[0].GetPayload())
	assert.True(t, frames[1].GetEnd())
}

// A sibling worker can refuse a stream with a unary error. runStream returns
// it as a channelwire.RPCError that keeps the code, and forwards no frame.
func TestRunStream_ReturnsAUnaryRefusalWithItsCode(t *testing.T) {
	ch := &scriptedStreamChannel{
		ctx:      t.Context(),
		response: channelwire.NewErrorResponse(12, "unknown method"),
	}

	frames, err := runScriptedStream(t, ch)

	var rpcErr *channelwire.RPCError
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, int32(12), rpcErr.Code)
	assert.Equal(t, "unknown method", rpcErr.Message)
	assert.Empty(t, frames)
}

// A caller that gives up ends the stream with its own context's error.
func TestRunStream_EndsWhenTheCallerGivesUp(t *testing.T) {
	ch := &scriptedStreamChannel{ctx: t.Context()}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := runStream(ctx, ch, leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, nil, func(*leapmuxv1.InnerStreamMessage) {
		t.Error("a stream that sent nothing forwards nothing")
	}, nil)

	require.ErrorIs(t, err, context.Canceled)
}
