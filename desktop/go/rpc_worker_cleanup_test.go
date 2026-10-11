package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/leapmux/leapmux/util/drain"
	"github.com/stretchr/testify/require"
)

type controlledWorkerCleanup struct {
	blockingSoloInstance
	finished chan struct{}
	err      error
}

func (cleanup controlledWorkerCleanup) Stop() error {
	err := cleanup.blockingSoloInstance.Stop()
	close(cleanup.finished)
	return errors.Join(err, cleanup.err)
}

type cleanupCheckedWriter struct {
	net.Conn
	finished  <-chan struct{}
	premature atomic.Bool
	closed    atomic.Bool
}

func (writer *cleanupCheckedWriter) Close() error {
	writer.closed.Store(true)
	return writer.Conn.Close()
}

func (writer *cleanupCheckedWriter) Write(data []byte) (int, error) {
	select {
	case <-writer.finished:
	default:
		writer.premature.Store(true)
	}
	return writer.Conn.Write(data)
}

func TestShutdownReplyFollowsControlledWorkerCleanup(t *testing.T) {
	assertControlledWorkerCleanupReply(t, false, nil)
}

func TestShutdownReplySurvivesTheHandlerDeadlineDuringControlledWorkerCleanup(t *testing.T) {
	assertControlledWorkerCleanupReply(t, true, errors.New("the Worker lease release failed"))
}

func assertControlledWorkerCleanupReply(t *testing.T, expireDeadline bool, cleanupErr error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	deadline, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	app := NewApp("")
	cleanup := controlledWorkerCleanup{
		blockingSoloInstance: blockingSoloInstance{entered: make(chan struct{}), release: make(chan struct{})},
		finished:             make(chan struct{}),
		err:                  cleanupErr,
	}
	connection := installTestConnection(app, nil, cleanup, "")
	client, sidecar := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = sidecar.Close() })
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(cleanup.release) }) }
	t.Cleanup(release)
	t.Cleanup(func() {
		release()
		_ = client.Close()
		if cleanupErr == nil {
			require.NoError(t, app.Shutdown())
		} else {
			require.ErrorIs(t, app.Shutdown(), cleanupErr)
		}
	})
	writer := &cleanupCheckedWriter{Conn: sidecar, finished: cleanup.finished}
	session := NewRPCSession(app, sidecar, writer, nil)
	control := newControlledHandlerDrain()
	if expireDeadline {
		session.handlerDrain = control.policy()
	}
	runDone := make(chan error, 1)
	go func() { runDone <- session.Run() }()
	require.NoError(t, WriteFrame(client, &desktoppb.Frame{Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{Id: 901, Method: &desktoppb.Request_Shutdown{Shutdown: &desktoppb.ShutdownRequest{}}}}}))
	select {
	case <-cleanup.entered:
	case <-deadline.Done():
		t.Fatal("the desktop did not start Worker cleanup")
	}
	require.ErrorIs(t, app.ctx.Err(), context.Canceled)
	require.NoError(t, connection.ctx.Err(), "the Worker context must remain live during its cleanup")
	if expireDeadline {
		first := control.next(t)
		require.Equal(t, handlerDrainTimeout, first.timeout)
		close(first.expire)
		control.nextCleanup(t)
		require.False(t, writer.closed.Load(), "the writer must remain open while Worker cleanup continues")
	}
	reply := make(chan *desktoppb.Frame, 1)
	readErr := make(chan error, 1)
	go func() {
		frame, err := ReadFrame(bufio.NewReader(client))
		if err != nil {
			readErr <- err
			return
		}
		reply <- frame
	}()
	release()
	select {
	case frame := <-reply:
		response := frame.GetResponse()
		require.NotNil(t, response)
		require.Equal(t, uint64(901), response.GetId())
		require.Empty(t, response.GetError())
		lifecycle := response.GetLifecycle()
		require.NotNil(t, lifecycle)
		require.NotNil(t, lifecycle.GetSidecarInfo())
		if cleanupErr == nil {
			require.Empty(t, lifecycle.GetCleanupErrors())
		} else {
			require.Len(t, lifecycle.GetCleanupErrors(), 1)
			require.Contains(t, lifecycle.GetCleanupErrors()[0], cleanupErr.Error())
		}
		require.False(t, writer.premature.Load(), "the lifecycle reply must follow actual Worker cleanup")
		require.ErrorIs(t, connection.ctx.Err(), context.Canceled)
	case err := <-readErr:
		t.Fatalf("the controlled shutdown lost its lifecycle reply: %v", err)
	case <-deadline.Done():
		t.Fatal("the desktop did not return its lifecycle reply")
	}
	select {
	case err := <-runDone:
		require.NoError(t, err)
	case <-deadline.Done():
		t.Fatal("the desktop transport did not finish after cleanup")
	}
	if expireDeadline {
		responseGrace := control.next(t)
		require.Equal(t, handlerInterruptGrace, responseGrace.timeout)
		require.Empty(t, responseGrace.warning)
		require.Empty(t, control.requests)
		require.Empty(t, control.cleanupRequests)
	}
}

func TestReadFramesObservesPeerDisconnectAfterAppCancellation(t *testing.T) {
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	var input bytes.Buffer
	require.NoError(t, WriteFrame(&input, &desktoppb.Frame{
		Message: &desktoppb.Frame_Request{Request: &desktoppb.Request{
			Id:     902,
			Method: &desktoppb.Request_GetSidecarInfo{GetSidecarInfo: &desktoppb.GetSidecarInfoRequest{}},
		}},
	}))
	writer := newBlockingWriteCloser()
	session := NewRPCSession(app, &input, writer, nil)
	sessionCtx, cancelSession := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancelSession(nil) })
	app.cancel()
	session.readFrames(make(chan frameReadResult), cancelSession)
	require.ErrorIs(t, context.Cause(sessionCtx), io.EOF)
	select {
	case <-session.peerDone:
	default:
		t.Fatal("the reader did not report the peer disconnect during cleanup")
	}
	select {
	case <-writer.closed:
	default:
		t.Fatal("the reader did not interrupt the disconnected peer's writer")
	}
}

func TestRejectedShutdownReleasesCleanupBeforeAResponseWrite(t *testing.T) {
	app := NewApp("")
	t.Cleanup(func() { require.NoError(t, app.Shutdown()) })
	writer := newBlockingWriteCloser()
	t.Cleanup(func() { require.NoError(t, writer.Close()) })
	session := NewRPCSession(app, bytes.NewReader(nil), writer, nil)
	var cleanup drain.Counter
	cleanup.Add()
	cleanupDone := cleanup.DoneChan()
	sessionCtx, cancel := context.WithCancel(context.Background())
	cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		session.dispatch(sessionCtx, &desktoppb.Request{
			Id:     903,
			Method: &desktoppb.Request_Shutdown{Shutdown: &desktoppb.ShutdownRequest{}},
		}, sync.OnceFunc(cleanup.Done))
	}()
	select {
	case <-writer.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the rejected shutdown did not start its response")
	}
	select {
	case <-cleanupDone:
	default:
		t.Fatal("a rejected shutdown must release its cleanup wait before response writing")
	}
	require.NoError(t, app.ctx.Err(), "a rejected shutdown must not start native cleanup")
	require.NoError(t, writer.Close())
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("the rejected shutdown handler did not finish")
	}
}
