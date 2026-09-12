package controlipc_test

import (
	"context"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/stretchr/testify/require"
)

func TestRouterRetainsAnUpdateBeforeControllerBinding(t *testing.T) {
	controller := &recordingStreamController{}
	dispatcher := &cancelRaceDispatcher{ctrl: controller, registered: make(chan struct{}), proceed: make(chan struct{}), bindResult: make(chan bool, 1)}
	proceed := sync.OnceFunc(func() { close(dispatcher.proceed) })
	router := &controlipc.Router{WorkerID: "A", UserID: userid.MustNew("u"), LocalDispatcher: dispatcher}
	t.Cleanup(func() { proceed(); router.CancelStream("early-update") })
	done := make(chan error, 1)
	go func() {
		done <- router.StreamInner(context.Background(), controlipc.TokenInfo{}, "worker.WatchEvents", []byte("{}"), "A", "early-update", func(*leapmuxv1.StreamInnerEnvelope) error { return nil })
	}()
	<-dispatcher.registered
	require.NoError(t, router.UpdateStream("early-update", []byte("full interest")))
	proceed()
	require.True(t, <-dispatcher.bindResult)
	controller.mu.Lock()
	frames := append([][]byte(nil), controller.payloads...)
	controller.mu.Unlock()
	require.Equal(t, [][]byte{[]byte("full interest")}, frames)
	router.CancelStream("early-update")
	<-done
}

type delayedStreamBinding struct {
	bind func(channel.StreamController) func()
}

// delayedStreamTransport exposes binding so a test can replace the stream first.
type delayedStreamTransport struct {
	calls chan delayedStreamBinding
	controlipc.CrossWorkerClient
}

func (transport *delayedStreamTransport) DispatchWith(ctx context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, writer channel.ResponseWriter) {
	transport.calls <- delayedStreamBinding{bind: func(controller channel.StreamController) func() {
		release, _ := writer.BindStream(controller)
		return release
	}}
	<-ctx.Done()
}

func (transport *delayedStreamTransport) StreamInner(ctx context.Context, _ string, _ userid.UserID, _ string, _ []byte, _ func(*leapmuxv1.InnerStreamMessage), bind func(channel.StreamController)) error {
	transport.calls <- delayedStreamBinding{bind: func(controller channel.StreamController) func() {
		bind(controller)
		return func() {}
	}}
	<-ctx.Done()
	return ctx.Err()
}

func TestRouterReplacementKeepsItsOwnController(t *testing.T) {
	for _, target := range []string{"local", "sibling"} {
		t.Run(target, func(t *testing.T) {
			transport := &delayedStreamTransport{calls: make(chan delayedStreamBinding, 1)}
			router := &controlipc.Router{WorkerID: "local", UserID: userid.MustNew("u"), LocalDispatcher: transport, CrossWorker: transport}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			start := func() (delayedStreamBinding, <-chan error) {
				done := make(chan error, 1)
				go func() {
					done <- router.StreamInner(ctx, controlipc.TokenInfo{}, "worker.WatchEvents", nil, target, "same", func(*leapmuxv1.StreamInnerEnvelope) error { return nil })
				}()
				return <-transport.calls, done
			}
			old, oldDone := start()
			current, currentDone := start()
			<-oldDone
			oldController, currentController := &recordingStreamController{}, &recordingStreamController{}
			releaseOld := old.bind(oldController)
			require.NoError(t, router.UpdateStream("same", []byte("before current binding")))
			releaseOld()
			releaseCurrent := current.bind(currentController)
			t.Cleanup(releaseCurrent)
			require.NoError(t, router.UpdateStream("same", []byte("after current binding")))
			oldController.mu.Lock()
			oldFrames := append([][]byte(nil), oldController.payloads...)
			oldController.mu.Unlock()
			currentController.mu.Lock()
			currentFrames := append([][]byte(nil), currentController.payloads...)
			currentController.mu.Unlock()
			require.Empty(t, oldFrames, "the old controller must not receive the replacement's frames")
			require.Equal(t, [][]byte{[]byte("before current binding"), []byte("after current binding")}, currentFrames)
			router.CancelStream("same")
			<-currentDone
		})
	}
}
