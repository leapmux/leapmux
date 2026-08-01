package channel

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestStreamRegistry_BindDeliver(t *testing.T) {
	var r streamRegistry
	ctrl := &recordingController{}
	release := r.bind(7, ctrl)
	defer release()

	r.deliver(7, &leapmuxv1.InnerStreamRequest{Payload: []byte("hello")})
	assert.Equal(t, int32(1), ctrl.frames.Load())
	ctrl.mu.Lock()
	assert.Equal(t, []byte("hello"), ctrl.last)
	ctrl.mu.Unlock()
	assert.Equal(t, int32(0), ctrl.cancels.Load())
}

func TestStreamRegistry_DeliverCancel(t *testing.T) {
	var r streamRegistry
	ctrl := &recordingController{}
	_ = r.bind(3, ctrl)

	r.deliver(3, &leapmuxv1.InnerStreamRequest{Cancel: true})
	assert.Equal(t, int32(1), ctrl.cancels.Load())
	assert.Equal(t, int32(0), ctrl.frames.Load())

	// Second cancel is a no-op (entry already removed).
	r.deliver(3, &leapmuxv1.InnerStreamRequest{Cancel: true})
	assert.Equal(t, int32(1), ctrl.cancels.Load())
}

func TestStreamRegistry_UnboundDeliver(t *testing.T) {
	var r streamRegistry
	// Must neither panic nor call anything.
	r.deliver(99, &leapmuxv1.InnerStreamRequest{Payload: []byte("x")})
	r.deliver(99, &leapmuxv1.InnerStreamRequest{Cancel: true})
}

func TestStreamRegistry_ReleaseAll(t *testing.T) {
	var r streamRegistry
	a := &recordingController{}
	b := &recordingController{}
	_ = r.bind(1, a)
	_ = r.bind(2, b)

	r.releaseAll()
	assert.Equal(t, int32(1), a.cancels.Load())
	assert.Equal(t, int32(1), b.cancels.Load())

	// Map emptied — further deliver is a no-op.
	r.deliver(1, &leapmuxv1.InnerStreamRequest{Cancel: true})
	assert.Equal(t, int32(1), a.cancels.Load())
}

func TestStreamRegistry_ReleaseWithoutCancel(t *testing.T) {
	var r streamRegistry
	ctrl := &recordingController{}
	release := r.bind(5, ctrl)
	release()
	assert.Equal(t, int32(0), ctrl.cancels.Load())

	r.deliver(5, &leapmuxv1.InnerStreamRequest{Payload: []byte("x")})
	assert.Equal(t, int32(0), ctrl.frames.Load())
}

func TestStreamRegistry_ConcurrentDeliverReleaseAll(t *testing.T) {
	var r streamRegistry
	const n = 32
	ctrls := make([]*recordingController, n)
	for i := range n {
		ctrls[i] = &recordingController{}
		_ = r.bind(uint64(i+1), ctrls[i])
	}

	var wg sync.WaitGroup
	wg.Add(n + 1)
	for i := range n {
		go func(id uint64) {
			defer wg.Done()
			r.deliver(id, &leapmuxv1.InnerStreamRequest{Payload: []byte("x")})
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
