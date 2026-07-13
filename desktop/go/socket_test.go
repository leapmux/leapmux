package main

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunSocketServerAcceptedConnectionWinsIdleDeadline(t *testing.T) {
	originalTimeout := sidecarIdleTimeout
	sidecarIdleTimeout = 25 * time.Millisecond
	t.Cleanup(func() { sidecarIdleTimeout = originalTimeout })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	app := NewApp("test-hash")
	t.Cleanup(func() { assert.NoError(t, app.Shutdown()) })

	accepted := make(chan struct{})
	releaseHandoff := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runSocketServer(listener, app, nil, func() {
			close(accepted)
			<-releaseHandoff
		})
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	<-accepted

	select {
	case <-app.ctx.Done():
		t.Error("idle deadline shut down an already accepted connection")
	case <-time.After(3 * sidecarIdleTimeout):
	}

	close(releaseHandoff)
	require.NoError(t, app.Shutdown())
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("socket server did not stop")
	}
}

func TestRunSocketServerReturnsCleanlyOnExternalShutdown(t *testing.T) {
	// External shutdown (Shutdown RPC or SIGTERM) while runSocketServer is
	// blocked in acceptBeforeIdleDeadline with no accepted connection must
	// return cleanly. Regression: the shutdown case returns a nil conn, so a
	// missing nil guard dereferenced the nil net.Conn and panicked.
	originalTimeout := sidecarIdleTimeout
	sidecarIdleTimeout = 30 * time.Second // keep the idle timer from winning
	t.Cleanup(func() { sidecarIdleTimeout = originalTimeout })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	app := NewApp("test-hash")
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("runSocketServer panicked on shutdown: %v", r)
			}
		}()
		done <- runSocketServer(listener, app, nil, nil)
	}()

	require.NoError(t, app.Shutdown())
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("runSocketServer did not return after external shutdown")
	}
}

func TestRunSocketServerShutsDownAfterIdleDeadline(t *testing.T) {
	originalTimeout := sidecarIdleTimeout
	sidecarIdleTimeout = 25 * time.Millisecond
	t.Cleanup(func() { sidecarIdleTimeout = originalTimeout })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	app := NewApp("test-hash")
	t.Cleanup(func() { assert.NoError(t, app.Shutdown()) })
	done := make(chan error, 1)
	go func() { done <- runSocketServer(listener, app, nil, nil) }()

	select {
	case <-app.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle deadline did not shut down the app")
	}
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("socket server did not stop after idle shutdown")
	}
}

// A relisten that fails must be REPORTED, not panic the sidecar. runSocketServer
// rebinds `listener` on the idle-deadline path, and locallisten.Listen returns
// (nil, err) when the socket dir vanished, another process took the path, or fds
// are exhausted -- the very condition isTemporaryAcceptError exists for. Storing
// that nil made the deferred listener.Close() nil-deref, and a panic skips
// main.run's deferred app.Shutdown, orphaning the Hub's revocation runtime lease
// so the next launch is fenced for its TTL.
func TestRunSocketServerReturnsRelistenFailure(t *testing.T) {
	originalTimeout := sidecarIdleTimeout
	sidecarIdleTimeout = 25 * time.Millisecond
	t.Cleanup(func() { sidecarIdleTimeout = originalTimeout })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	app := NewApp("test-hash")
	t.Cleanup(func() { assert.NoError(t, app.Shutdown()) })

	relistenErr := errors.New("relisten /tmp/sock: address already in use")
	relisten := func() (net.Listener, error) { return nil, relistenErr }

	accepted := make(chan struct{})
	releaseHandoff := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("runSocketServer panicked after a failed relisten: %v", r)
			}
		}()
		done <- runSocketServer(listener, app, relisten, func() {
			close(accepted)
			<-releaseHandoff
		})
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	<-accepted

	// Let the idle timer close the listener while the conn is already accepted:
	// the session still runs, and the loop must rebind the listener afterwards.
	time.Sleep(3 * sidecarIdleTimeout)
	close(releaseHandoff)
	// Ending the session sends the loop into the relisten branch.
	require.NoError(t, conn.Close())

	select {
	case err := <-done:
		assert.ErrorIs(t, err, relistenErr, "the relisten failure must be returned")
	case <-time.After(5 * time.Second):
		t.Fatal("runSocketServer did not return after a failed relisten")
	}
}

// A TRANSIENT accept failure must not kill the sidecar's control socket. The
// control socket shares the process's fd table with the tunnel listeners, so
// the same EMFILE spike the tunnel accept loops ride out (a browser fanning
// out through SOCKS5 -- see isTemporaryAcceptError) can hit
// acceptBeforeIdleDeadline's Accept too. Before the retry, that error
// returned fatally through runSocketServer and exited the sidecar, tearing
// down the control channel the shell depends on -- while the tunnels that
// caused the spike survived it.
func TestRunSocketServerRetriesTransientAcceptError(t *testing.T) {
	originalTimeout := sidecarIdleTimeout
	sidecarIdleTimeout = 30 * time.Second // keep the idle timer from winning
	t.Cleanup(func() { sidecarIdleTimeout = originalTimeout })

	// Two transient failures; the exhausted script then parks Accept until
	// Close, standing in for a recovered listener with no client yet.
	ln := newScriptedListener(syscall.EMFILE, syscall.EMFILE)
	app := NewApp("test-hash")
	done := make(chan error, 1)
	go func() { done <- runSocketServer(ln, app, nil, nil) }()

	// The accept loop must get past both transient failures and keep serving.
	require.Eventually(t, func() bool { return ln.acceptCalls() > 2 },
		2*time.Second, 10*time.Millisecond,
		"a transient accept error must be retried, not fatal")
	select {
	case err := <-done:
		t.Fatalf("runSocketServer exited on a transient accept error: %v", err)
	default:
	}

	require.NoError(t, app.Shutdown())
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("runSocketServer did not return after shutdown")
	}
}

// emfileListener fails every Accept with EMFILE until closed, then returns
// net.ErrClosed -- a retry storm that ends only when the listener dies.
type emfileListener struct {
	mu     sync.Mutex
	calls  int
	closed chan struct{}
}

func (l *emfileListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.calls++
	l.mu.Unlock()
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	default:
		return nil, syscall.EMFILE
	}
}

func (l *emfileListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *emfileListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero} }

func (l *emfileListener) acceptCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// An external shutdown must end the sidecar promptly even mid-retry-storm: the
// accept goroutine may be parked in its transient-error backoff when the
// shutdown arrives, and both its backoff select's shutdown arm and the closed
// listener (net.ErrClosed is not temporary) bound how long the storm can keep
// the server alive.
func TestRunSocketServerShutsDownDuringAcceptRetryStorm(t *testing.T) {
	originalTimeout := sidecarIdleTimeout
	sidecarIdleTimeout = 30 * time.Second // keep the idle timer from winning
	t.Cleanup(func() { sidecarIdleTimeout = originalTimeout })

	ln := &emfileListener{closed: make(chan struct{})}
	app := NewApp("test-hash")
	done := make(chan error, 1)
	go func() { done <- runSocketServer(ln, app, nil, nil) }()

	// Let the retry loop absorb a few failures first, so the shutdown lands
	// while the storm is live rather than before the first Accept.
	require.Eventually(t, func() bool { return ln.acceptCalls() >= 3 },
		2*time.Second, 5*time.Millisecond,
		"the retry loop must keep re-accepting through the storm")

	require.NoError(t, app.Shutdown())
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("runSocketServer did not return after shutdown during a retry storm")
	}
}
