//go:build linux || darwin

package locallisten

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListen_UnixRefusesAnOverlongPath(t *testing.T) {
	path := uniqueUnixPathShort(t)
	limit := MaxUnixSocketPathBytes()
	path += strings.Repeat("x", limit+1-len(path))
	listener, err := Listen("unix:" + path)
	if listener != nil {
		_ = listener.Close()
	}
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSocketPathTooLong)
	assert.ErrorContains(t, err, fmt.Sprint(limit))
	_, statErr := os.Lstat(path)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

// uniqueUnixPathShort returns a per-test socket path whose full length stays
// well under the AF_UNIX sun_path limit (104 bytes on macOS).
//
// We avoid t.TempDir() because its path embeds the full test name plus a
// sequence counter; on macOS that yields paths like
// /var/folders/.../<TestName>/001/ which can push past the limit once a
// filename is appended. Instead we mint a short temp dir with prefix "lm"
// and register cleanup ourselves.
func uniqueUnixPathShort(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lm")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
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

func TestListen_UnixRepeatedClosePreservesReplacement(t *testing.T) {
	path := uniqueUnixPathShort(t)
	old, err := Listen("unix:" + path)
	require.NoError(t, err)
	require.NoError(t, old.Close())
	require.NoFileExists(t, path)
	replacement, err := Listen("unix:" + path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = replacement.Close() })
	assert.ErrorIs(t, old.Close(), net.ErrClosed)
	info, err := os.Lstat(path)
	require.NoError(t, err, "closing the old listener must preserve the replacement")
	assert.Equal(t, os.ModeSocket, info.Mode().Type())
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	var dialer net.Dialer
	client, err := dialer.DialContext(ctx, "unix", path)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
	server, err := replacement.Accept()
	require.NoError(t, err)
	defer func() { _ = server.Close() }()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.NoError(t, client.SetDeadline(deadline))
	require.NoError(t, server.SetDeadline(deadline))
	_, err = client.Write([]byte("new"))
	require.NoError(t, err)
	var payload [3]byte
	_, err = io.ReadFull(server, payload[:])
	require.NoError(t, err)
	assert.Equal(t, "new", string(payload[:]))
	require.NoError(t, replacement.Close())
	require.NoFileExists(t, path)
}

func TestListen_UnixRemovesStaleSocket(t *testing.T) {
	path := uniqueUnixPathShort(t)

	// Disable automatic unlink so closing the fixture leaves a stale socket.
	pre, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatalf("pre-listen: %v", err)
	}
	pre.SetUnlinkOnClose(false)
	if err := pre.Close(); err != nil {
		t.Fatalf("pre-close: %v", err)
	}

	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.Equal(t, os.ModeSocket, info.Mode().Type())
	ln, err := Listen("unix:" + path)
	if err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	_ = ln.Close()
}

func TestListen_UnixRefusesToReplaceActiveSocket(t *testing.T) {
	path := uniqueUnixPathShort(t)
	active, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen active socket: %v", err)
	}
	defer func() { _ = active.Close() }()

	if replacement, err := Listen("unix:" + path); err == nil {
		_ = replacement.Close()
		t.Fatal("Listen replaced an active Unix socket")
	}

	accepted := make(chan error, 1)
	go func() {
		conn, err := active.Accept()
		if err == nil {
			_ = conn.Close()
		}
		accepted <- err
	}()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("active socket no longer accepts connections: %v", err)
	}
	_ = conn.Close()
	if err := <-accepted; err != nil {
		t.Fatalf("accept active socket: %v", err)
	}
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

func TestListen_UnixAcceptsAPathAtTheLimit(t *testing.T) {
	path := uniqueUnixPathShort(t)
	path += strings.Repeat("x", MaxUnixSocketPathBytes()-len(path))
	listener, err := Listen("unix:" + path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSocket)
}
