//go:build unix

package lifecycle

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/locallisten"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pausedCloseListener pauses after releasing its old pathname. Every Close
// still reaches the original listener, including the server's direct fallback.
type pausedCloseListener struct {
	net.Listener
	once    sync.Once
	closed  chan struct{}
	release chan struct{}
}

func (l *pausedCloseListener) Close() error {
	err := l.Listener.Close()
	l.once.Do(func() { close(l.closed); <-l.release })
	return err
}

func TestControlClose_PreservesListenerThatReusesReleasedPath(t *testing.T) {
	path := filepath.Join(runtimeEnv(t), "replacement.sock")
	listener, err := locallisten.Listen("unix:" + path)
	require.NoError(t, err)
	adapter := &pausedCloseListener{Listener: listener, closed: make(chan struct{}), release: make(chan struct{})}
	server := &controlServer{reporter: &stopCountingReporter{}, token: "test-token", ln: adapter}
	mux := http.NewServeMux()
	mux.HandleFunc(controlStatusPath, server.handleStatus)
	server.httpSrv = &http.Server{Handler: mux, ReadHeaderTimeout: controlReadHeaderTimeout}
	serveFinished := make(chan struct{})
	go func() { defer close(serveFinished); _ = server.httpSrv.Serve(adapter) }()
	var release sync.Once
	closeFinished := make(chan struct{})
	closeStarted := false
	t.Cleanup(func() {
		release.Do(func() { close(adapter.release) })
		_ = server.Close()
		if closeStarted {
			select {
			case <-closeFinished:
			case <-time.After(30 * time.Second):
				t.Error("control close did not finish")
			}
		}
		select {
		case <-serveFinished:
		case <-time.After(30 * time.Second):
			t.Error("control serve did not finish")
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client, err := newControlClient("unix:"+path, "test-token")
	require.NoError(t, err)
	_, err = client.Status(ctx)
	require.NoError(t, err, "the authenticated request proves the server registered its listener")
	var closeErr error
	closeStarted = true
	go func() { defer close(closeFinished); closeErr = server.Close() }()
	select {
	case <-adapter.closed:
	case <-ctx.Done():
		t.Fatal("the original listener did not close")
	}
	replacement, err := locallisten.Listen("unix:" + path)
	require.NoError(t, err)
	defer func() { _ = replacement.Close() }()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	release.Do(func() { close(adapter.release) })
	select {
	case <-closeFinished:
	case <-ctx.Done():
		t.Fatal("control close did not return after release")
	}
	require.NoError(t, closeErr)
	for range 2 {
		require.NoError(t, server.Close())
		_, err := os.Lstat(path)
		require.NoError(t, err, "repeated close must preserve the replacement pathname")
		connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
		require.NoError(t, err)
		defer func() { _ = connection.Close() }()
		peer, err := replacement.Accept()
		require.NoError(t, err)
		defer func() { _ = peer.Close() }()
		require.NoError(t, connection.SetDeadline(time.Now().Add(30*time.Second)))
		require.NoError(t, peer.SetDeadline(time.Now().Add(30*time.Second)))
		_, writeErr := connection.Write([]byte("live"))
		require.NoError(t, writeErr)
		var payload [4]byte
		_, readErr := io.ReadFull(peer, payload[:])
		require.NoError(t, readErr)
		assert.Equal(t, "live", string(payload[:]))
		require.NoError(t, connection.Close())
		require.NoError(t, peer.Close())
	}
}
