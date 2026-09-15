package crossworker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStreamControllerCancellationDoesNotWaitForTheSender(t *testing.T) {
	controller := &crossWorkerStreamCtrl{
		wake: make(chan struct{}, 1), done: make(chan struct{}), closed: true,
	}
	// The sender observed closure but still holds its final transport operation.
	finishSend := sync.OnceFunc(func() { close(controller.done) })
	t.Cleanup(finishSend)
	returned := make(chan struct{})
	go func() { controller.OnCancel(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		finishSend()
		<-returned
		t.Fatal("cancellation waited for the sender and prevented its caller from closing the channel")
	}
}

type heldStreamChannel struct {
	ctx      context.Context
	started  chan struct{}
	proceed  chan struct{}
	start    sync.Once
	mu       sync.Mutex
	frames   []string
	ids      []uint64
	writeErr error
}

func (transport *heldStreamChannel) Context() context.Context { return transport.ctx }

func (transport *heldStreamChannel) SendStreamRequest(ctx context.Context, id uint64, payload []byte, _ bool) error {
	transport.start.Do(func() { close(transport.started) })
	select {
	case <-transport.proceed:
	case <-ctx.Done():
		return ctx.Err()
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.frames = append(transport.frames, "update:"+string(payload))
	transport.ids = append(transport.ids, id)
	return transport.writeErr
}

func (transport *heldStreamChannel) CancelStream(_ context.Context, id uint64) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.frames = append(transport.frames, "cancel")
	transport.ids = append(transport.ids, id)
	return transport.writeErr
}

func TestStreamControllerSendsTheLastRevisionBeforeCancel(t *testing.T) {
	for _, failWrites := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "write failure"}[failWrites], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			transport := &heldStreamChannel{ctx: ctx, started: make(chan struct{}), proceed: make(chan struct{})}
			if failWrites {
				transport.writeErr = errors.New("send failed")
			}
			controller := newCrossWorkerStreamCtrl(transport, 7)
			controller.OnClientFrame([]byte("first"))
			<-transport.started
			controller.OnClientFrame([]byte("superseded"))
			last := []byte("last")
			controller.OnClientFrame(last)
			last[0] = 'x'
			var callers sync.WaitGroup
			for range 8 {
				callers.Go(controller.OnCancel)
			}
			returned := make(chan struct{})
			go func() { callers.Wait(); close(returned) }()
			select {
			case <-returned:
			case <-time.After(time.Second):
				cancel()
				<-returned
				t.Fatal("cancellation waited for a transport write")
			}
			controller.OnClientFrame([]byte("too late"))
			close(transport.proceed)
			select {
			case <-controller.done:
			case <-time.After(time.Second):
				t.Fatal("the stream sender did not stop after cancellation")
			}
			require.Equal(t, []string{"update:first", "update:last", "cancel"}, transport.frames)
			require.Equal(t, []uint64{7, 7, 7}, transport.ids)
		})
	}
}

func TestStreamControllerPreservesEmptyRevisionsAndStopsWithTheChannel(t *testing.T) {
	for _, closeChannel := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty revision", true: "channel closes during send"}[closeChannel], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			transport := &heldStreamChannel{ctx: ctx, started: make(chan struct{}), proceed: make(chan struct{})}
			controller := newCrossWorkerStreamCtrl(transport, 0)
			controller.OnClientFrame(nil)
			select {
			case <-transport.started:
			case <-time.After(time.Second):
				t.Fatal("the stream sender discarded an empty revision")
			}
			if closeChannel {
				cancel()
			} else {
				close(transport.proceed)
				controller.OnCancel()
			}
			select {
			case <-controller.done:
			case <-time.After(time.Second):
				t.Fatal("the stream sender did not stop")
			}
			controller.OnClientFrame([]byte("after completion"))
			controller.OnCancel()
			if closeChannel {
				require.Empty(t, transport.frames)
			} else {
				require.Equal(t, []string{"update:", "cancel"}, transport.frames)
				require.Equal(t, []uint64{0, 0}, transport.ids)
			}
		})
	}
}
