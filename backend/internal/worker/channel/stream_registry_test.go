package channel

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type recordingController struct {
	frames  atomic.Int32
	cancels atomic.Int32
	last    []byte
	mu      sync.Mutex
}

func (c *recordingController) OnClientFrame(payload []byte) {
	c.mu.Lock()
	c.last = append([]byte(nil), payload...)
	c.mu.Unlock()
	c.frames.Add(1)
}

func (c *recordingController) OnCancel() {
	c.cancels.Add(1)
}

// bindStream reserves a correlation id and binds its controller, which is the
// two-step sequence that session.go runs. The registry exposes no one-step
// helper, because no production caller reserves and binds on one goroutine.
func bindStream(t *testing.T, r *streamRegistry, id uint64, controller StreamController) func() {
	t.Helper()
	inbox, ok := r.reserve(id)
	require.True(t, ok)
	release, ok := r.bindReserved(id, inbox, controller)
	require.True(t, ok)
	return release
}

func TestStreamRegistry_BindDeliver(t *testing.T) {
	var r streamRegistry
	ctrl := &recordingController{}
	release := bindStream(t, &r, 7, ctrl)
	defer release()

	assertStreamDelivery(t, &r, 7, revisionFrame([]byte("hello")))
	assert.Equal(t, int32(1), ctrl.frames.Load())
	ctrl.mu.Lock()
	assert.Equal(t, []byte("hello"), ctrl.last)
	ctrl.mu.Unlock()
	assert.Equal(t, int32(0), ctrl.cancels.Load())
}

func TestStreamRegistry_DeliverCancel(t *testing.T) {
	var r streamRegistry
	ctrl := &recordingController{}
	_ = bindStream(t, &r, 3, ctrl)

	assertStreamDelivery(t, &r, 3, cancelFrame())
	assert.Equal(t, int32(1), ctrl.cancels.Load())
	assert.Equal(t, int32(0), ctrl.frames.Load())

	// Second cancel is a no-op (entry already removed).
	assertStreamDelivery(t, &r, 3, cancelFrame())
	assert.Equal(t, int32(1), ctrl.cancels.Load())
}

// A frame that selects no action fails a live stream: deliver returns the
// error, and the cleanup that the caller runs after it sends the error frame
// removes the stream and cancels its controller.
func TestStreamRegistry_DeliverWithNoActionFailsTheStream(t *testing.T) {
	var r streamRegistry
	ctrl := &recordingController{}
	_ = bindStream(t, &r, 4, ctrl)

	cleanup, err := r.deliver(4, &leapmuxv1.InnerStreamRequest{})
	require.ErrorIs(t, err, errStreamFrameNoAction)
	require.NotNil(t, cleanup)
	assert.Equal(t, int32(0), ctrl.cancels.Load(), "the stream stays live until the error frame is sent")

	cleanup()
	assert.Equal(t, int32(1), ctrl.cancels.Load())
	assert.Equal(t, int32(0), ctrl.frames.Load(), "the frame does not reach the controller as a revision")
	assertStreamDelivery(t, &r, 4, revisionFrame([]byte("late")))
	assert.Equal(t, int32(0), ctrl.frames.Load(), "the failed stream is gone")
}

// An empty payload is a revision: a WatchEventsRequest that watches nothing
// encodes to zero bytes, and it must still reach the controller.
func TestStreamRegistry_DeliversAnEmptyRevision(t *testing.T) {
	var r streamRegistry
	ctrl := &recordingController{}
	release := bindStream(t, &r, 6, ctrl)
	defer release()

	assertStreamDelivery(t, &r, 6, revisionFrame(nil))
	assert.Equal(t, int32(1), ctrl.frames.Load())
	assert.Equal(t, int32(0), ctrl.cancels.Load())
}

func TestStreamFrameErrorCode(t *testing.T) {
	assert.Equal(t, codes.InvalidArgument, streamFrameErrorCode(errStreamFrameNoAction))
	assert.Equal(t, codes.InvalidArgument, streamFrameErrorCode(fmt.Errorf("frame: %w", errStreamFrameNoAction)))
	assert.Equal(t, codes.ResourceExhausted, streamFrameErrorCode(ErrStreamInboxFull))
}

func TestStreamRegistry_UnboundDeliver(t *testing.T) {
	var r streamRegistry
	// Must neither panic nor call anything.
	assertStreamDelivery(t, &r, 99, revisionFrame([]byte("x")))
	assertStreamDelivery(t, &r, 99, cancelFrame())
	assertStreamDelivery(t, &r, 99, &leapmuxv1.InnerStreamRequest{})
}

func TestStreamRegistry_ReleaseAll(t *testing.T) {
	var r streamRegistry
	a := &recordingController{}
	b := &recordingController{}
	_ = bindStream(t, &r, 1, a)
	_ = bindStream(t, &r, 2, b)

	r.releaseAll()
	assert.Equal(t, int32(1), a.cancels.Load())
	assert.Equal(t, int32(1), b.cancels.Load())

	// Map emptied — further deliver is a no-op.
	assertStreamDelivery(t, &r, 1, cancelFrame())
	assert.Equal(t, int32(1), a.cancels.Load())
}

func TestStreamRegistry_ReleaseWithoutCancel(t *testing.T) {
	var r streamRegistry
	ctrl := &recordingController{}
	release := bindStream(t, &r, 5, ctrl)
	release()
	assert.Equal(t, int32(0), ctrl.cancels.Load())

	assertStreamDelivery(t, &r, 5, revisionFrame([]byte("x")))
	assert.Equal(t, int32(0), ctrl.frames.Load())
}

func TestStreamRegistry_ConcurrentDeliverReleaseAll(t *testing.T) {
	var r streamRegistry
	const n = 32
	ctrls := make([]*recordingController, n)
	for i := range n {
		ctrls[i] = &recordingController{}
		_ = bindStream(t, &r, uint64(i+1), ctrls[i])
	}

	var wg sync.WaitGroup
	wg.Add(n + 1)
	for i := range n {
		go func(id uint64) {
			defer wg.Done()
			assertStreamDelivery(t, &r, id, revisionFrame([]byte("x")))
		}(uint64(i + 1))
	}
	go func() {
		defer wg.Done()
		r.releaseAll()
	}()
	wg.Wait()

	// Every controller was cancelled exactly once by releaseAll (or possibly
	// also received a frame). Cancels must be exactly 1.
	for i, c := range ctrls {
		require.Equal(t, int32(1), c.cancels.Load(), "controller %d", i)
	}
}

// revisionFrame is the client frame that revises a stream with payload.
func revisionFrame(payload []byte) *leapmuxv1.InnerStreamRequest {
	return &leapmuxv1.InnerStreamRequest{Action: &leapmuxv1.InnerStreamRequest_Payload{Payload: payload}}
}

// cancelFrame is the client frame that retires a stream.
func cancelFrame() *leapmuxv1.InnerStreamRequest {
	return &leapmuxv1.InnerStreamRequest{Action: &leapmuxv1.InnerStreamRequest_Cancel{Cancel: &leapmuxv1.StreamCancel{}}}
}

func assertStreamDelivery(t *testing.T, registry *streamRegistry, id uint64, frame *leapmuxv1.InnerStreamRequest) {
	t.Helper()
	cleanup, err := registry.deliver(id, frame)
	assert.NoError(t, err)
	assert.Nil(t, cleanup)
	if cleanup != nil {
		cleanup()
	}
}
