//go:build windows

package locallisten

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/user"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

var pipeTestCounter atomic.Uint64

func uniqueTestPipeName(t *testing.T) string {
	t.Helper()
	n := pipeTestCounter.Add(1)
	return fmt.Sprintf("leapmux-locallisten-test-%d-%d-%d", os.Getpid(), time.Now().UnixNano(), n)
}

// runAcceptLoop starts a background goroutine that accepts connections on ln
// until ln.Close is called. Each accepted connection is closed immediately,
// matching what production code needs from a pipe "ready" check (the pipe is
// reachable, not that an RPC session runs). Returns a done channel the test
// can wait on to ensure the goroutine exited.
func runAcceptLoop(t *testing.T, ln net.Listener) chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return done
}

func TestListen_NpipeBindsAcceptsAndRoundTrips(t *testing.T) {
	name := uniqueTestPipeName(t)
	ln, err := Listen("npipe:" + name)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	accepted := make(chan []byte, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 5)
		if _, err := io.ReadFull(conn, buf); err != nil {
			acceptErr <- err
			return
		}
		accepted <- buf
	}()

	conn, err := winio.DialPipe(`\\.\pipe\`+name, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case got := <-accepted:
		if string(got) != "hello" {
			t.Errorf("got %q, want %q", got, "hello")
		}
	case err := <-acceptErr:
		t.Fatalf("accept: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accept")
	}
}

func TestListen_NpipeAcceptsFullNTPath(t *testing.T) {
	// Parse preserves backslashes; this test confirms Listen accepts the
	// fully-qualified NT pipe path in addition to the short name form.
	name := uniqueTestPipeName(t)
	fullPath := `\\.\pipe\` + name
	ln, err := Listen("npipe:" + fullPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	_ = ln.Close()
}

// TestUserOnlySDDL_HasCurrentUserSID unit-tests the SDDL we construct before
// handing it to winio. We deliberately avoid probing the live pipe's security
// descriptor (GetNamedSecurityInfo on a pipe path requires an active instance
// and races with the winio accept loop); validating the generated SDDL
// string against the current user's SID catches the bug class that matters —
// a malformed or empty descriptor making the pipe world-accessible.
func TestUserOnlySDDL_HasCurrentUserSID(t *testing.T) {
	sddl, err := userOnlySDDL()
	if err != nil {
		t.Fatalf("userOnlySDDL: %v", err)
	}
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	if !strings.Contains(sddl, u.Uid) {
		t.Errorf("SDDL %q missing current user SID %s", sddl, u.Uid)
	}

	// Round-trip through Windows' SDDL parser and confirm the resulting
	// owner SID matches the current user. This guarantees the string is
	// syntactically valid and semantically what we intend.
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("SecurityDescriptorFromString: %v", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatalf("Owner: %v", err)
	}
	if owner.String() != u.Uid {
		t.Errorf("SDDL owner = %s, want %s", owner.String(), u.Uid)
	}
}

func TestListen_UnixRejectedOnWindows(t *testing.T) {
	_, err := Listen("unix:C:\\ProgramData\\leapmux\\hub.sock")
	if !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("got %v, want ErrUnsupportedScheme", err)
	}
}

func TestWaitReady_NpipeSucceedsOnceAccepting(t *testing.T) {
	name := uniqueTestPipeName(t)
	ln, err := Listen("npipe:" + name)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	acceptDone := runAcceptLoop(t, ln)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := WaitReady(ctx, "npipe:"+name); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	_ = ln.Close()
	<-acceptDone
}

func TestWaitReady_NpipeTimesOut(t *testing.T) {
	name := uniqueTestPipeName(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := WaitReady(ctx, "npipe:"+name)
	if err == nil {
		t.Fatal("expected error when no listener exists")
	}
}

func TestWaitReady_NpipeSucceedsAfterDelayedListen(t *testing.T) {
	name := uniqueTestPipeName(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	readyCh := make(chan error, 1)
	go func() {
		readyCh <- WaitReady(ctx, "npipe:"+name)
	}()

	time.Sleep(50 * time.Millisecond)
	ln, err := Listen("npipe:" + name)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	acceptDone := runAcceptLoop(t, ln)

	select {
	case err := <-readyCh:
		if err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitReady did not return after Listen succeeded")
	}
	_ = ln.Close()
	<-acceptDone
}

func TestDialer_NpipeReachesListener(t *testing.T) {
	name := uniqueTestPipeName(t)
	ln, err := Listen("npipe:" + name)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	acceptDone := runAcceptLoop(t, ln)

	dial, err := Dialer("npipe:" + name)
	if err != nil {
		t.Fatalf("Dialer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	_ = ln.Close()
	<-acceptDone
}

func TestDialer_NpipeAcceptsFullNTPath(t *testing.T) {
	// A full NT path must work end to end, not only through the parser.
	//
	// This test dropped its round trip once: listen, accept loop, dial and close
	// wedged on Windows Server 2025 inside go-winio v0.6.2's close path during
	// teardown, with one goroutine parked past `closeCh <- 1` on doneCh while the
	// listener routine still ran. That is
	// https://github.com/microsoft/go-winio/issues/85. npipeListener.Close now
	// starts another close for exactly that state, so the round trip is back. Read
	// Close for the mechanism.
	name := uniqueTestPipeName(t)
	ln, err := Listen("npipe:" + fullPipePath(name))
	if err != nil {
		t.Fatalf("Listen with full NT path: %v", err)
	}
	acceptDone := runAcceptLoop(t, ln)

	dial, err := Dialer("npipe:" + fullPipePath(name))
	if err != nil {
		t.Fatalf("Dialer with full NT path: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	if err := ln.Close(); err != nil {
		t.Fatalf("listener close: %v", err)
	}
	<-acceptDone
}

// TestCloseAccepted_FreesPipeNameForRelisten reproduces the Switch-Mode bug:
// after closing a listener whose accepted server-side handle is still alive,
// a second ListenPipe on the same name fails with ERROR_ACCESS_DENIED because
// FILE_FLAG_FIRST_PIPE_INSTANCE refuses to create another instance while one
// exists. CloseAccepted releases those handles so re-listen succeeds.
func TestCloseAccepted_FreesPipeNameForRelisten(t *testing.T) {
	name := uniqueTestPipeName(t)
	ln, err := Listen("npipe:" + name)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	// Accept loop holds the accepted server-side handle open. This is the state
	// a hijacked WebSocket leaves behind: net/http drops a hijacked conn from
	// activeConn, so the http.Server lets go of it and the underlying pipe
	// handle outlives http.Server.Close.
	accepted := make(chan net.Conn, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		accepted <- c
	}()

	client, err := winio.DialPipe(`\\.\pipe\`+name, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	var serverConn net.Conn
	select {
	case serverConn = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accept")
	}

	// Close the listener but DON'T close serverConn — this is the leak we
	// reproduce. Re-listen on the same name must fail.
	if err := ln.Close(); err != nil {
		t.Fatalf("listener close: %v", err)
	}
	leaked, err := Listen("npipe:" + name)
	if err == nil {
		_ = leaked.Close()
		_ = serverConn.Close()
		t.Fatal("expected re-listen to fail while accepted handle is alive")
	}

	// CloseAccepted releases the leaked handle, so the next listen succeeds.
	CloseAccepted(ln)
	relisten, err := Listen("npipe:" + name)
	if err != nil {
		t.Fatalf("re-listen after CloseAccepted: %v", err)
	}
	_ = relisten.Close()
}

// TestNpipeListener_AcceptTranslatesClose verifies that closing the pipe
// listener surfaces an error satisfying errors.Is(net.ErrClosed) — so shared
// accept loops can check a single sentinel rather than winio.ErrPipeListenerClosed.
func TestNpipeListener_AcceptTranslatesClose(t *testing.T) {
	ln, err := Listen("npipe:" + uniqueTestPipeName(t))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	_ = ln.Close()

	_, acceptErr := ln.Accept()
	if acceptErr == nil {
		t.Fatal("expected Accept on closed listener to return an error")
	}
	if !errors.Is(acceptErr, net.ErrClosed) {
		t.Errorf("Accept error %v does not wrap net.ErrClosed", acceptErr)
	}
	if !errors.Is(acceptErr, winio.ErrPipeListenerClosed) {
		t.Errorf("Accept error %v should still wrap winio.ErrPipeListenerClosed for debugging", acceptErr)
	}
}

// stuckListener stands in for a winio listener whose close blocks. Every close
// before releaseAfter waits; the one AT releaseAfter releases them all. That is
// the go-winio state npipeListener.Close retries out of: the first close token
// reaches an accept that is in flight, leaves the listener routine running, and
// only a later token stops it. See
// https://github.com/microsoft/go-winio/issues/85.
//
// A hand-written fake rather than a real pipe, because the winio path that
// swallows the first token needs an unmapped error from an aborted connect, and
// no test can make Windows produce that on demand. The race against a real pipe
// is covered by TestNpipeListener_CloseReturnsWithAnAcceptInFlight.
type stuckListener struct {
	releaseAfter int64
	closeErr     error
	calls        atomic.Int64
	release      chan struct{}
	releaseOnce  sync.Once
}

func newStuckListener(releaseAfter int64) *stuckListener {
	return &stuckListener{releaseAfter: releaseAfter, release: make(chan struct{})}
}

func (l *stuckListener) releaseAll() { l.releaseOnce.Do(func() { close(l.release) }) }

func (l *stuckListener) Close() error {
	if l.calls.Add(1) >= l.releaseAfter {
		l.releaseAll()
		return l.closeErr
	}
	<-l.release
	return nil
}

func (l *stuckListener) Accept() (net.Conn, error) {
	return nil, errors.New("stuckListener never accepts")
}

func (l *stuckListener) Addr() net.Addr { return stuckAddr{} }

type stuckAddr struct{}

func (stuckAddr) Network() string { return "pipe" }
func (stuckAddr) String() string  { return `\\.\pipe\stuck` }

// The ordinary close sends ONE close and returns what it returns. The retry
// exists for a wedged listener; it must not add a second close to a healthy one,
// because each extra close token aborts an accept that winio has in flight.
//
// The hour-long interval is the assertion: a Close that waits for the timer at
// all cannot finish inside the deadline below.
func TestNpipeListener_CloseSendsOneCloseWhenTheListenerStops(t *testing.T) {
	fake := newStuckListener(1)
	ln := newNpipeListener(fake)
	ln.retryInterval = time.Hour

	closed := make(chan error, 1)
	go func() { closed <- ln.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close waited for the retry timer on a listener that stopped")
	}
	if got := fake.calls.Load(); got != 1 {
		t.Errorf("close attempts = %d, want 1", got)
	}
}

// The regression: a first close that never returns must not become a Close that
// never returns. Without the retry this test hangs, which is what wedged the Hub
// on Windows -- http.Server.Shutdown holds srv.mu across the listener close.
func TestNpipeListener_CloseRetriesWhenTheFirstCloseWedges(t *testing.T) {
	fake := newStuckListener(2)
	t.Cleanup(fake.releaseAll)
	ln := newNpipeListener(fake)
	ln.retryInterval = 20 * time.Millisecond

	closed := make(chan error, 1)
	go func() { closed <- ln.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return although a second close would have released it")
	}
	// At least two: the first close wedges by construction, so a Close that sent
	// only one could not have returned. A slow runner may fit more in.
	if got := fake.calls.Load(); got < 2 {
		t.Errorf("close attempts = %d, want at least 2", got)
	}
}

// A listener that answers no close at all still has to let the caller go, with
// the failure reported rather than swallowed. Holding out for the listener is
// the deadlock this whole path exists to prevent.
func TestNpipeListener_CloseReportsAListenerThatNeverStops(t *testing.T) {
	fake := newStuckListener(math.MaxInt64)
	// The attempts stay parked on the release channel until this runs.
	t.Cleanup(fake.releaseAll)
	ln := newNpipeListener(fake)
	ln.retryInterval = 5 * time.Millisecond

	closed := make(chan error, 1)
	go func() { closed <- ln.Close() }()
	select {
	case err := <-closed:
		if !errors.Is(err, errCloseStuck) {
			t.Fatalf("Close error = %v, want one wrapping errCloseStuck", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close never gave up on a listener that never stops")
	}
	// The bound is the point: Close retried, and it started no more closes than
	// it promised. The count is read after Close returned, so the last attempt's
	// goroutine may not have run yet -- an exact count would be a timing bet.
	if got := fake.calls.Load(); got < 2 || got > closeAttempts {
		t.Errorf("close attempts = %d, want between 2 and %d", got, closeAttempts)
	}
}

// The retry must not swallow what the listener reports. Close runs the winio
// close on its own goroutine now, so the error takes a channel to reach the
// caller, and hub teardown reads that error to name a listener that failed.
func TestNpipeListener_CloseReportsTheListenersOwnError(t *testing.T) {
	want := errors.New("pipe close refused")
	fake := newStuckListener(1)
	fake.closeErr = want
	ln := newNpipeListener(fake)
	ln.retryInterval = time.Hour

	closed := make(chan error, 1)
	go func() { closed <- ln.Close() }()
	select {
	case err := <-closed:
		if !errors.Is(err, want) {
			t.Fatalf("Close error = %v, want %v", err, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return")
	}
}

// The Hub closes its local listener twice: http.Server.Shutdown closes it, and
// the teardown that follows closes it again. The second close must return at
// once and report nothing, NOT spend the retry budget and report a stuck
// listener.
func TestNpipeListener_CloseTwiceReportsNothingTheSecondTime(t *testing.T) {
	ln, err := Listen("npipe:" + uniqueTestPipeName(t))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	acceptDone := runAcceptLoop(t, ln)
	if err := ln.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	<-acceptDone

	closed := make(chan error, 1)
	go func() { closed <- ln.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("second close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second close did not return")
	}
}

// net/http closes a listener from its own Serve teardown while Shutdown closes
// the same one, so two closes can overlap. Every one of them must return, and
// none may panic on the listener routine's single doneCh close.
func TestNpipeListener_ConcurrentClosesAllReturn(t *testing.T) {
	ln, err := Listen("npipe:" + uniqueTestPipeName(t))
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	acceptDone := runAcceptLoop(t, ln)

	const closers = 4
	closed := make(chan error, closers)
	for range closers {
		go func() { closed <- ln.Close() }()
	}
	for i := range closers {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatalf("concurrent close %d: %v", i, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of %d concurrent closes returned", i, closers)
		}
	}
	<-acceptDone
}

// The race against a REAL pipe, in the shape that wedged CI: a readiness probe
// connects and writes nothing, the accept loop re-arms, and the listener closes
// on top of that accept. One iteration is a coin toss, so this runs a batch;
// each close is bounded well above Close's own retry budget, so only a close
// that cannot finish at all fails the test.
func TestNpipeListener_CloseReturnsWithAnAcceptInFlight(t *testing.T) {
	for i := range 50 {
		name := uniqueTestPipeName(t)
		ln, err := Listen("npipe:" + name)
		if err != nil {
			t.Fatalf("Listen %d: %v", i, err)
		}
		acceptDone := runAcceptLoop(t, ln)

		// Connect and write nothing, exactly what locallisten.WaitReady leaves
		// behind. A connect that ends this way is what makes winio's aborted
		// connect report an error its close path does not map.
		conn, err := winio.DialPipe(pipePrefix+name, nil)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		_ = conn.Close()

		closed := make(chan error, 1)
		go func() { closed <- ln.Close() }()
		select {
		case cerr := <-closed:
			if cerr != nil {
				t.Fatalf("close %d: %v", i, cerr)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("listener close wedged on iteration %d", i)
		}
		<-acceptDone
	}
}
