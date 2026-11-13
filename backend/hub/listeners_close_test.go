package hub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/listenset"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type heldAcceptListener struct {
	net.Listener
	accepting  chan struct{}
	closed     chan struct{}
	release    chan struct{}
	acceptOnce sync.Once
	closedOnce sync.Once
}

func (l *heldAcceptListener) Accept() (net.Conn, error) {
	l.acceptOnce.Do(func() { close(l.accepting) })
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.release
	}
	return conn, err
}

func (l *heldAcceptListener) Close() error {
	err := l.Listener.Close()
	l.closedOnce.Do(func() { close(l.closed) })
	return err
}

func TestListenerOwnershipDoesNotRaceHTTPShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ln := &heldAcceptListener{Listener: raw, accepting: make(chan struct{}), closed: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(ln.release) })
	addr, err := listenset.Parse(raw.Addr().String())
	require.NoError(t, err)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	set := newListenerSet([]baseBinding{{ln: ln, addr: addr}}, make(chan error, 1))
	set.setServer(server)
	t.Cleanup(func() {
		release()
		_ = server.Close()
		_ = set.Close()
		_ = raw.Close()
	})
	httpClosed := make(chan struct{})
	server.RegisterOnShutdown(func() { close(httpClosed) })
	set.Serve()
	select {
	case <-ln.accepting:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	setDone := make(chan error, 1)
	go func() { setDone <- set.Close() }()
	select {
	case <-ln.closed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown(ctx) }()
	select {
	case <-httpClosed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	release()
	select {
	case err := <-setDone:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-shutdownDone:
		require.NoError(t, err, "both owners must share the same listener close")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

type firstCloseFailureListener struct {
	net.Listener
	calls   atomic.Int32
	failure error
}

func (l *firstCloseFailureListener) Close() error {
	l.calls.Add(1)
	_ = l.Listener.Close()
	return l.failure
}

func TestOwnedListenerCloseRetainsTheFirstFailure(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	failure := errors.New("controlled native close failure")
	inner := &firstCloseFailureListener{Listener: raw, failure: failure}
	owned := &ownedListener{Listener: inner}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- owned.Close() }()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for range 2 {
		select {
		case err := <-results:
			require.ErrorIs(t, err, failure)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	assert.EqualValues(t, 1, inner.calls.Load(), "both owners must receive the first failure without closing twice")
}
