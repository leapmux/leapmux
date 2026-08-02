package hub

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// handlerGrace must cancel laggard handlers strictly before the drain's deadline
// expires, otherwise the forced cancellation leaves the drain no time to observe
// the handlers returning and close their connections. Pin that ordering so a
// future constant edit cannot silently invert it.
//
// httpDrainTimeout vs crdtShutdownTimeout is deliberately NOT asserted here: the
// CRDT registry is shut down only after the drain completes (Serve receives
// shutdownDone before calling crdtRegistry.Shutdown), so no in-flight handler can
// observe a shut-down registry regardless of the two durations.
func TestShutdownTimeoutsAreOrdered(t *testing.T) {
	require.Less(t, handlerGrace, httpDrainTimeout,
		"handlerGrace must cancel laggards before the drain expires")
}

// acquiredResources.close releases cancelHandlers on a construction failure, so
// a failed NewServer cannot leak the handlerCtx cancel (which would otherwise
// trip go vet's lostcancel and, more importantly, leak a goroutine-blocking
// resource). Lock that the wiring exists and that a nil field stays a no-op.
func TestAcquiredResourcesCloseReleasesCancelHandlers(t *testing.T) {
	t.Run("releases cancel when set", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		_ = acquiredResources{cancelHandlers: cancel}.close(nil)

		require.ErrorIs(t, ctx.Err(), context.Canceled,
			"close must invoke cancelHandlers so a failed NewServer leaks nothing")
	})

	t.Run("nil cancel is a no-op and still returns nil", func(t *testing.T) {
		// The whole accumulator must be nil-safe: a field NewServer never set
		// (because it failed before reaching that step) must not panic.
		require.NoError(t, acquiredResources{}.close(nil))
	})
}

// TestBaseContextCancelsInFlightHandlerOnShutdown verifies the stdlib mechanism
// the production wiring in NewServer relies on: that an http.Server whose
// BaseContext returns a cancellable context propagates that cancellation to
// r.Context() for every in-flight request, so Shutdown returns nil rather than
// waiting out its deadline on a parked handler.
//
// This exercises the BaseContext -> r.Context() cascade in isolation, NOT the
// production server literal (NewServer needs a live store, listeners, and
// keystore, so it is too heavy to stand up here). The production wiring itself
// -- handlerCtx created in NewServer and set as http.Server.BaseContext -- is
// covered by go vet's reachability check on cancelHandlers and by the
// construction-failure test above; this test pins the net/http contract that
// makes that wiring load-bearing.
func TestBaseContextCancelsInFlightHandlerOnShutdown(t *testing.T) {
	handlerCtx, cancelHandlers := context.WithCancel(context.Background())
	t.Cleanup(cancelHandlers)

	handlerCancelled := make(chan struct{})
	var served atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/park", func(w http.ResponseWriter, r *http.Request) {
		served.Store(true)
		<-r.Context().Done() // park until handlerCtx is cancelled
		close(handlerCancelled)
		w.WriteHeader(http.StatusOK)
	})

	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	srv := &http.Server{
		Handler:     mux,
		BaseContext: func(net.Listener) context.Context { return handlerCtx },
		Protocols:   protocols,
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	serverDone := make(chan error, 1)
	go func() { serverDone <- srv.Serve(ln) }()

	// A real connection keeps the request in net/http's activeConn map, which is
	// what Shutdown waits on.
	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_, err = conn.Write([]byte("GET /park HTTP/1.1\r\nHost: localhost\r\n\r\n"))
	require.NoError(t, err)

	// Wait until the handler is parked, else Shutdown could observe zero active
	// connections and the test would not exercise the path.
	require.Eventually(t, func() bool { return served.Load() },
		time.Second, 5*time.Millisecond, "handler never observed the request")

	// Tight drain so a regression (handler NOT cancelled) fails fast.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelDrain()

	cancelHandlers() // the shutdown goroutine does this handlerGrace after shutdown begins
	err = srv.Shutdown(drainCtx)
	require.NoError(t, err, "Shutdown must return nil once the handler's context is cancelled")

	select {
	case <-handlerCancelled:
	case <-time.After(time.Second):
		t.Fatal("handler was not cancelled despite base context cancellation")
	}

	select {
	case err := <-serverDone:
		require.ErrorIs(t, err, http.ErrServerClosed)
	case <-time.After(time.Second):
		t.Fatal("server did not stop serving")
	}
}
