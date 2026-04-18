//go:build windows

package locallisten

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"strings"
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
	defer ln.Close()

	accepted := make(chan []byte, 1)
	acceptErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		defer conn.Close()
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
	defer conn.Close()
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
	defer ln.Close()
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
	name := uniqueTestPipeName(t)
	ln, err := Listen("npipe:" + name)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	acceptDone := runAcceptLoop(t, ln)

	dial, err := Dialer("npipe:" + fullPipePath(name))
	if err != nil {
		t.Fatalf("Dialer: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dial(ctx)
	if err != nil {
		t.Fatalf("dial with full NT path: %v", err)
	}
	_ = conn.Close()

	_ = ln.Close()
	<-acceptDone
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
