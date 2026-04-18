//go:build unix

package locallisten

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// uniqueUnixPathShort returns a per-test socket path whose full length stays
// well under the AF_UNIX sun_path limit. t.TempDir handles cleanup.
func uniqueUnixPathShort(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "s.sock")
}

func TestListen_UnixBindsAcceptsAndRoundTrips(t *testing.T) {
	path := uniqueUnixPathShort(t)
	ln, err := Listen("unix:" + path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %o, want 0600", info.Mode().Perm())
	}

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

	conn, err := net.Dial("unix", path)
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

func TestListen_UnixClosesUnlinks(t *testing.T) {
	path := uniqueUnixPathShort(t)
	ln, err := Listen("unix:" + path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket still present after Close: err=%v", err)
	}
}

func TestListen_UnixRemovesStaleSocket(t *testing.T) {
	path := uniqueUnixPathShort(t)

	// Create a stale socket: bind and close, leaving the file on disk
	// (net.Listener.Close on unix does not unlink).
	pre, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("pre-listen: %v", err)
	}
	if err := pre.Close(); err != nil {
		t.Fatalf("pre-close: %v", err)
	}

	ln, err := Listen("unix:" + path)
	if err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	_ = ln.Close()
}

func TestListen_UnixRejectsNonSocketFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-socket")
	if err := os.WriteFile(path, []byte("hi"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_, err := Listen("unix:" + path)
	if err == nil {
		t.Fatal("expected error when target exists but is not a socket")
	}
}

func TestListen_NpipeRejectedOnUnix(t *testing.T) {
	_, err := Listen("npipe:should-not-work")
	if !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("got %v, want ErrUnsupportedScheme", err)
	}
}

func TestWaitReady_UnixSucceedsOnceListening(t *testing.T) {
	path := uniqueUnixPathShort(t)
	ln, err := Listen("unix:" + path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := WaitReady(ctx, "unix:"+path); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestWaitReady_UnixTimesOut(t *testing.T) {
	path := uniqueUnixPathShort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := WaitReady(ctx, "unix:"+path)
	if err == nil {
		t.Fatal("expected error when no listener exists")
	}
}

func TestWaitReady_UnixSucceedsAfterDelayedListen(t *testing.T) {
	path := uniqueUnixPathShort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	readyCh := make(chan error, 1)
	go func() {
		readyCh <- WaitReady(ctx, "unix:"+path)
	}()

	time.Sleep(50 * time.Millisecond)
	ln, err := Listen("unix:" + path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	select {
	case err := <-readyCh:
		if err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitReady did not return after Listen succeeded")
	}
}

func TestDialer_UnixReachesListener(t *testing.T) {
	path := uniqueUnixPathShort(t)
	ln, err := Listen("unix:" + path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
			accepted <- struct{}{}
		}
	}()

	dial, err := Dialer("unix:" + path)
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

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("unix listener never accepted a connection")
	}
}

func TestDialer_NpipeFailsWithSchemeError(t *testing.T) {
	dial, err := Dialer("npipe:some-pipe")
	if err != nil {
		t.Fatalf("Dialer: %v", err)
	}
	_, err = dial(context.Background())
	if err == nil {
		t.Fatal("expected dial error on non-Windows platforms")
	}
	if !errors.Is(err, ErrUnsupportedScheme) {
		t.Errorf("error %v does not wrap ErrUnsupportedScheme", err)
	}
	if !strings.Contains(err.Error(), "npipe") {
		t.Errorf("error %v does not mention npipe", err)
	}
}
