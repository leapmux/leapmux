package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
)

type scriptedListener struct {
	accept    func() (net.Conn, error)
	closed    atomic.Bool
	closeOnce sync.Once
	closedCh  chan struct{}
}

func (l *scriptedListener) Accept() (net.Conn, error) { return l.accept() }
func (l *scriptedListener) Close() error {
	l.closed.Store(true)
	if l.closedCh != nil {
		l.closeOnce.Do(func() { close(l.closedCh) })
	}
	return nil
}
func (l *scriptedListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}

func TestServeTunnelForward_AcceptErrorCancelsActiveConnections(t *testing.T) {
	localClient, localServer := net.Pipe()
	remoteClient, remoteServer := net.Pipe()
	t.Cleanup(func() {
		_ = localClient.Close()
		_ = localServer.Close()
		_ = remoteClient.Close()
		_ = remoteServer.Close()
	})

	acceptErr := errors.New("listener accept failed")
	allowFailAccept := make(chan struct{})
	var acceptCalls atomic.Int32
	ln := &scriptedListener{
		accept: func() (net.Conn, error) {
			if acceptCalls.Add(1) == 1 {
				return localServer, nil
			}
			<-allowFailAccept
			return nil, acceptErr
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- serveTunnelForward(t.Context(), ln, "127.0.0.1", 9000, func(_ context.Context, _ string, _ uint16) (net.Conn, error) {
			return remoteClient, nil
		}, nil)
	}()

	// Prove the active connection is running inside pipeBidirectional before
	// forcing Accept to fail.
	_, err := localClient.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(remoteServer, buf)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(buf))

	close(allowFailAccept)

	select {
	case err := <-done:
		require.ErrorIs(t, err, acceptErr)
	case <-time.After(10 * time.Second):
		t.Fatal("serveTunnelForward deadlocked after Accept returned an error while a connection was active")
	}

	// Active connection ends must be closed by cancel().
	_, err = localClient.Read(make([]byte, 1))
	assert.Error(t, err)
	_, err = remoteServer.Read(make([]byte, 1))
	assert.Error(t, err)
}

func TestServeTunnelForward_ConcurrentDialFailuresSynchronizeNotice(t *testing.T) {
	const numConns = 16

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var acceptCalls atomic.Int32
	closedCh := make(chan struct{})
	ln := &scriptedListener{
		closedCh: closedCh,
		accept: func() (net.Conn, error) {
			if acceptCalls.Add(1) <= numConns {
				c1, c2 := net.Pipe()
				t.Cleanup(func() { _ = c1.Close() })
				return c2, nil
			}
			<-closedCh
			return nil, net.ErrClosed
		},
	}

	var dialing atomic.Int32
	allDialing := make(chan struct{})
	var closeAllDialing sync.Once
	dial := func(dialCtx context.Context, _ string, _ uint16) (net.Conn, error) {
		if dialing.Add(1) == numConns {
			closeAllDialing.Do(func() { close(allDialing) })
		}
		select {
		case <-allDialing:
		case <-dialCtx.Done():
			return nil, dialCtx.Err()
		}
		return nil, errors.New("connection refused")
	}

	// Keep both printer.Err and noticed unsynchronized on purpose so -race
	// fails immediately if serveTunnelForward calls notice concurrently.
	var errBuf bytes.Buffer
	printer := &cli.Printer{Out: io.Discard, Err: &errBuf, Format: cli.FormatText}
	noticed := 0
	allNoticed := make(chan struct{})
	notice := func(format string, args ...any) {
		_ = printer.Notice(format, args...)
		noticed++
		if noticed == numConns {
			close(allNoticed)
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- serveTunnelForward(ctx, ln, "127.0.0.1", 9000, dial, notice)
	}()

	select {
	case <-allNoticed:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for dial failure notices")
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for serveTunnelForward to stop")
	}

	assert.Equal(t, numConns, noticed)
	assert.Equal(t, numConns, strings.Count(errBuf.String(), "tunnel dial 127.0.0.1:9000 failed: connection refused"))
}

func TestOpenTunnelChannel_PreflightAndErrorClassification(t *testing.T) {
	t.Run("unknown worker fails preflight with not_found before opening channel", func(t *testing.T) {
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		harness := startHubChannelHarness(t, &recordingHub{workers: workerRows(fullTestID("worker-A"))}, disp)

		ghost := fullTestID("worker-ghost")
		f := runControl(t, "tunnel", "connect", "127.0.0.1:22", "--worker", ghost, "--hub", harness.url).
			requireFailure(t, cli.CodeNotFound)
		assert.Equal(t, "no such worker: "+ghost, f.Message)
		assert.Equal(t, 0, harness.channelOpens(), "preflight refusal must not open a channel")
	})

	t.Run("offline worker classifies channel open failure as worker_offline", func(t *testing.T) {
		workerID := fullTestID("worker-off")
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		harness := startHubChannelHarness(t, &recordingHub{
			workers:           workerRows(workerID),
			refuseChannelOpen: connect.NewError(connect.CodeUnavailable, "worker is offline"),
		}, disp)

		f := runControl(t, "tunnel", "forward", "0:127.0.0.1:22", "--worker", workerID, "--hub", harness.url).
			requireFailure(t, cli.CodeWorkerOffline)
		assert.Contains(t, f.Message, "worker is offline")
	})

	t.Run("unimplemented channel service returns version_mismatch with CLI and Hub build hints", func(t *testing.T) {
		workerID := fullTestID("worker-old")
		disp := &systemInfoDispatcher{info: sampleWorkerSystemInfo()}
		harness := startHubChannelHarness(t, &recordingHub{
			workers:           workerRows(workerID),
			refuseChannelOpen: connect.NewError(connect.CodeUnimplemented, "channel service missing"),
		}, disp)

		f := runControl(t, "tunnel", "connect", "127.0.0.1:22", "--worker", workerID, "--hub", harness.url).
			requireFailure(t, cli.CodeVersionMismatch)
		joinedHints := strings.Join(f.Hints, "\n")
		assert.Contains(t, joinedHints, "CLI build ")
		assert.Contains(t, joinedHints, "Hub build ")
	})

	t.Run("tab local IPC socket fails with usage error", func(t *testing.T) {
		startSpawnIPC(t, &recordingHub{workers: workerRows("worker-A")}, nil)

		f := runControl(t, "tunnel", "connect", "127.0.0.1:22", "--worker", "worker-A").
			requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "direct Hub connection")
	})
}
