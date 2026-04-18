//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

var testPipeCounter atomic.Uint64

func uniqueTestPipePath(t *testing.T) string {
	n := testPipeCounter.Add(1)
	return fmt.Sprintf(`\\.\pipe\leapmux-test-%d-%d-%d`, os.Getpid(), time.Now().UnixNano(), n)
}

// TestRunSocketServerErrorsOnInvalidPipePath ensures an invalid pipe path
// returns a wrapped error instead of panicking.
func TestRunSocketServerErrorsOnInvalidPipePath(t *testing.T) {
	err := RunSocketServer("", "test-hash")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen ")
}

// runServerInBackground starts RunSocketServer on a goroutine and returns a
// channel that yields the terminal error (nil on clean exit). It waits briefly
// for the listener to be ready by polling DialPipe.
func runServerInBackground(t *testing.T, pipePath, binaryHash string) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- RunSocketServer(pipePath, binaryHash)
	}()

	// Wait for the pipe to appear. The listener is up after the first
	// successful DialPipe.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		probe, err := winio.DialPipe(pipePath, nil)
		if err == nil {
			_ = probe.Close()
			return done
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("pipe %s never became reachable", pipePath)
	return done
}

// TestRunSocketServerHandshake exercises the full server-side flow: connect,
// send a GetSidecarInfo request via the real frame encoder, and verify the
// RPC session responds with the protocol version and binary hash we passed in.
func TestRunSocketServerHandshake(t *testing.T) {
	pipePath := uniqueTestPipePath(t)
	const binaryHash = "test-binary-hash-abc"

	done := runServerInBackground(t, pipePath, binaryHash)

	conn, err := winio.DialPipe(pipePath, nil)
	require.NoError(t, err, "DialPipe")
	reader := bufio.NewReader(conn)

	request := &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{
			Request: &desktoppb.Request{
				Id: 1,
				Method: &desktoppb.Request_GetSidecarInfo{
					GetSidecarInfo: &desktoppb.GetSidecarInfoRequest{},
				},
			},
		},
	}
	require.NoError(t, WriteFrame(conn, request), "WriteFrame")

	got, err := ReadFrame(reader)
	require.NoError(t, err, "ReadFrame")
	resp := got.GetResponse()
	require.NotNil(t, resp, "expected Response frame")
	assert.Equal(t, uint64(1), resp.Id)
	assert.Empty(t, resp.Error)

	info := resp.GetSidecarInfo()
	require.NotNil(t, info, "expected SidecarInfo result")
	assert.Equal(t, "1", info.ProtocolVersion)
	assert.Equal(t, binaryHash, info.BinaryHash)
	assert.Equal(t, int64(os.Getpid()), info.Pid)

	// Initiate a graceful shutdown so the server goroutine exits cleanly
	// instead of leaking until the idle timeout fires.
	shutdown := &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{
			Request: &desktoppb.Request{
				Id:     2,
				Method: &desktoppb.Request_Shutdown{Shutdown: &desktoppb.ShutdownRequest{}},
			},
		},
	}
	require.NoError(t, WriteFrame(conn, shutdown), "WriteFrame shutdown")
	// The server responds before tearing the connection down; drain it so
	// the session loop observes EOF cleanly.
	_, _ = ReadFrame(reader)
	_ = conn.Close()

	select {
	case err := <-done:
		assert.NoError(t, err, "RunSocketServer exited with error")
	case <-time.After(3 * time.Second):
		t.Fatal("server did not exit after shutdown RPC")
	}
}

// TestRunSocketServerRejectsConcurrentSessions confirms only one session runs
// at a time: while the first client is still connected, a second dial should
// block until the first session returns (winio's default pipe config allows
// exactly one instance as we configured).
func TestRunSocketServerSerializesSessions(t *testing.T) {
	pipePath := uniqueTestPipePath(t)
	done := runServerInBackground(t, pipePath, "hash")

	first, err := winio.DialPipe(pipePath, nil)
	require.NoError(t, err, "first DialPipe")

	// Second dial should fail fast or block until first closes.
	secondCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		timeout := 500 * time.Millisecond
		c, err := winio.DialPipe(pipePath, &timeout)
		if err == nil {
			_ = c.Close()
		}
		secondCh <- err
	}()

	select {
	case err := <-secondCh:
		// Some winio versions fail fast with ERROR_PIPE_BUSY / deadline exceeded
		// when the pipe has only one instance. Either behavior is acceptable
		// as long as it doesn't silently let both clients through.
		assert.Error(t, err, "second dial should not succeed while first is open")
	case <-time.After(2 * time.Second):
		t.Fatal("second dial neither succeeded nor returned an error in time")
	}
	wg.Wait()

	// Clean up: shutdown the first session.
	shutdown := &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{
			Request: &desktoppb.Request{
				Id:     1,
				Method: &desktoppb.Request_Shutdown{Shutdown: &desktoppb.ShutdownRequest{}},
			},
		},
	}
	_ = WriteFrame(first, shutdown)
	_, _ = ReadFrame(bufio.NewReader(first))
	_ = first.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not exit after first session shut down")
	}
}

// TestRunSocketServerLongSessionSurvivesIdleTimeout is a regression guard:
// while a client is connected, the idle timer must stay stopped — otherwise
// a long-lived RPC session gets killed mid-flight at idleTimeout.
func TestRunSocketServerLongSessionSurvivesIdleTimeout(t *testing.T) {
	orig := sidecarIdleTimeout
	sidecarIdleTimeout = 100 * time.Millisecond
	t.Cleanup(func() { sidecarIdleTimeout = orig })

	pipePath := uniqueTestPipePath(t)
	done := runServerInBackground(t, pipePath, "hash")

	conn, err := winio.DialPipe(pipePath, nil)
	require.NoError(t, err, "DialPipe")

	// Hold the connection idle for ~4× the timeout. If the idle timer fires
	// mid-session, the server closes the connection and exits; we'd observe
	// the server goroutine returning early.
	select {
	case err := <-done:
		t.Fatalf("server exited while client was connected: %v", err)
	case <-time.After(500 * time.Millisecond):
	}

	shutdown := &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{
			Request: &desktoppb.Request{
				Id:     1,
				Method: &desktoppb.Request_Shutdown{Shutdown: &desktoppb.ShutdownRequest{}},
			},
		},
	}
	require.NoError(t, WriteFrame(conn, shutdown))
	_, _ = ReadFrame(bufio.NewReader(conn))
	_ = conn.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not exit after shutdown RPC")
	}
}
