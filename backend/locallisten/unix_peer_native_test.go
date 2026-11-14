//go:build linux || darwin

package locallisten

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type peerAcceptResult struct {
	connection net.Conn
	err        error
}

func waitPeerAccept(t *testing.T, results <-chan peerAcceptResult) peerAcceptResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("Unix accept did not finish")
		return peerAcceptResult{}
	}
}
func dialPeerClient(t *testing.T, path string) *net.UnixConn {
	t.Helper()
	connection, err := unixDialer(path)(testutil.DeadlineContext(t))
	require.NoError(t, err)
	client, ok := connection.(*net.UnixConn)
	require.True(t, ok)
	deadline, ok := testutil.DeadlineContext(t).Deadline()
	require.True(t, ok)
	require.NoError(t, client.SetDeadline(deadline))
	t.Cleanup(func() { _ = client.Close() })
	return client
}
func verifiedPeerListener(t *testing.T) (*unixListener, string) {
	t.Helper()
	path := uniqueUnixPathShort(t)
	listener, err := Listen("unix:" + path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	verified, ok := listener.(*unixListener)
	require.True(t, ok, "the actual Unix listener factory must enforce peer credentials")
	_, unchecked := listener.(interface{ AcceptUnix() (*net.UnixConn, error) })
	require.False(t, unchecked, "a promoted AcceptUnix method would bypass the policy")
	return verified, path
}

func TestUnixPeerUIDReadsTheNativeAccountAndRefusesAClosedDescriptor(t *testing.T) {
	path := uniqueUnixPathShort(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	client := dialPeerClient(t, path)
	accepted, err := listener.AcceptUnix()
	require.NoError(t, err)
	t.Cleanup(func() { _ = accepted.Close() })
	for _, connection := range []*net.UnixConn{accepted, client} {
		uid, err := peerUID(connection)
		require.NoError(t, err)
		assert.Equal(t, uint32(os.Geteuid()), uid)
	}
	require.NoError(t, accepted.Close())
	uid, err := peerUID(accepted)
	require.Error(t, err)
	assert.Zero(t, uid, "a failed credential query supplies no usable identity")
}

func TestUnixPeerNativeCredentialSyscallRejectsInvalidDescriptor(t *testing.T) {
	uid, err := socketPeerUID(^uintptr(0))
	require.Error(t, err)
	assert.Zero(t, uid)
}

func TestUnixPeerAcceptRefusesForeignThenAcceptsCurrentAccount(t *testing.T) {
	listener, path := verifiedPeerListener(t)
	var queries atomic.Int64
	listener.readPeerUID = func(connection *net.UnixConn) (uint32, error) {
		uid, err := peerUID(connection)
		if err != nil {
			return 0, err
		}
		if queries.Add(1) == 1 {
			return uid ^ 1, nil
		}
		return uid, nil
	}
	results := make(chan peerAcceptResult, 1)
	// Queue the foreign bytes before Accept can reject and close the connection.
	foreign := dialPeerClient(t, path)
	_, err := foreign.Write([]byte("F"))
	require.NoError(t, err)
	go func() { connection, err := listener.Accept(); results <- peerAcceptResult{connection, err} }()
	current := dialPeerClient(t, path)
	_, err = current.Write([]byte("C"))
	require.NoError(t, err)
	result := waitPeerAccept(t, results)
	require.NoError(t, result.err)
	require.NotNil(t, result.connection)
	t.Cleanup(func() { _ = result.connection.Close() })
	deadline, ok := testutil.DeadlineContext(t).Deadline()
	require.True(t, ok)
	require.NoError(t, result.connection.SetReadDeadline(deadline))
	var got [1]byte
	_, err = io.ReadFull(result.connection, got[:])
	require.NoError(t, err)
	require.Equal(t, byte('C'), got[0], "foreign bytes must never reach the caller of Accept")
	assert.EqualValues(t, 2, queries.Load())
	_, err = foreign.Read(got[:])
	require.Error(t, err)
	if timeout, ok := err.(net.Error); ok {
		assert.False(t, timeout.Timeout(), "the rejected connection must close, rather than wait for a deadline")
	}
}

func TestUnixPeerCredentialFailureClosesTheConnectionAndReturnsTheCause(t *testing.T) {
	listener, path := verifiedPeerListener(t)
	fault := errors.New("native credential lookup fault")
	listener.readPeerUID = func(*net.UnixConn) (uint32, error) { return uint32(os.Geteuid()), fault }
	results := make(chan peerAcceptResult, 1)
	go func() { connection, err := listener.Accept(); results <- peerAcceptResult{connection, err} }()
	client := dialPeerClient(t, path)
	result := waitPeerAccept(t, results)
	if result.connection != nil {
		t.Cleanup(func() { _ = result.connection.Close() })
	}
	require.Nil(t, result.connection)
	require.ErrorIs(t, result.err, fault)
	assert.ErrorContains(t, result.err, "read peer credentials")
	var data [1]byte
	_, err := client.Read(data[:])
	require.Error(t, err)
	if timeout, ok := err.(net.Error); ok {
		assert.False(t, timeout.Timeout())
	}
	// The caller receives an operational error. A caller that deliberately retries
	// still reaches the native listener, rather than a detached replacement.
	listener.readPeerUID = peerUID
	go func() { connection, err := listener.Accept(); results <- peerAcceptResult{connection, err} }()
	dialPeerClient(t, path)
	result = waitPeerAccept(t, results)
	require.NoError(t, result.err)
	require.NotNil(t, result.connection)
	require.NoError(t, result.connection.Close())
}

func TestUnixPeerListenerCloseUnblocksAccept(t *testing.T) {
	listener, _ := verifiedPeerListener(t)
	results := make(chan peerAcceptResult, 1)
	go func() { connection, err := listener.Accept(); results <- peerAcceptResult{connection, err} }()
	require.NoError(t, listener.Close())
	result := waitPeerAccept(t, results)
	require.Nil(t, result.connection)
	require.ErrorIs(t, result.err, net.ErrClosed)
}

func TestUnixPeerListenerCloseAfterRefusalUnblocksAccept(t *testing.T) {
	listener, path := verifiedPeerListener(t)
	checked := make(chan struct{})
	listener.readPeerUID = func(connection *net.UnixConn) (uint32, error) {
		uid, err := peerUID(connection)
		close(checked)
		return uid ^ 1, err
	}
	results := make(chan peerAcceptResult, 1)
	go func() { connection, err := listener.Accept(); results <- peerAcceptResult{connection, err} }()
	dialPeerClient(t, path)
	select {
	case <-checked:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the foreign-peer policy was not reached")
	}
	require.NoError(t, listener.Close())
	result := waitPeerAccept(t, results)
	require.Nil(t, result.connection)
	require.ErrorIs(t, result.err, net.ErrClosed)
}

func TestUnixPeerDialHonorsCancellation(t *testing.T) {
	_, path := verifiedPeerListener(t)
	dial, err := Dialer("unix:" + path)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	connection, err := dial(ctx)
	if connection != nil {
		_ = connection.Close()
	}
	require.ErrorIs(t, err, context.Canceled)
}

func TestUnixPeerQueuedBeforeChmodRequiresCurrentUID(t *testing.T) {
	if os.Getenv("LEAPMUX_UNIX_PEER_UMASK_CHILD") != "1" {
		command := exec.CommandContext(testutil.DeadlineContext(t), os.Args[0], "-test.run=^TestUnixPeerQueuedBeforeChmodRequiresCurrentUID$")
		command.Env = append(os.Environ(), "LEAPMUX_UNIX_PEER_UMASK_CHILD=1")
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
		return
	}
	// Umask is process-global, so only this isolated child changes it.
	previous := syscall.Umask(0)
	defer syscall.Umask(previous)
	path := uniqueUnixPathShort(t)
	var queued, queuedCurrent *net.UnixConn
	listener, err := listenUnixWithPermissions(path, func(socket string, mode os.FileMode) error {
		initial, err := os.Stat(socket)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o777), initial.Mode().Perm())
		queued = dialPeerClient(t, socket)
		_, err = io.WriteString(queued, "GET /foreign-admin HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
		require.NoError(t, err)
		queuedCurrent = dialPeerClient(t, socket)
		_, err = io.WriteString(queuedCurrent, "GET /queued-admin HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
		require.NoError(t, err)
		return os.Chmod(socket, mode)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), after.Mode().Perm())
	verified, ok := listener.(*unixListener)
	require.True(t, ok)
	var queries, admissions, foreignAdmissions atomic.Int64
	verified.readPeerUID = func(connection *net.UnixConn) (uint32, error) {
		uid, err := peerUID(connection)
		if err != nil {
			return 0, err
		}
		if queries.Add(1) == 1 {
			return uid ^ 1, nil
		}
		return uid, nil
	}
	server := &http.Server{
		BaseContext: func(net.Listener) context.Context { return peer.WithLocalIPC(t.Context()) },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !peer.IsLocalIPC(r.Context()) {
				http.Error(w, "not local", http.StatusForbidden)
				return
			}
			if r.URL.Path == "/foreign-admin" {
				foreignAdmissions.Add(1)
			}
			admissions.Add(1)
			_, _ = io.WriteString(w, "local administrator")
		}),
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() { require.NoError(t, server.Close()); require.ErrorIs(t, <-served, http.ErrServerClosed) })
	// The injected foreign identity supplements a real pre-chmod queued socket.
	// It is not a claim that this test process ran under another account.
	response, err := http.ReadResponse(bufio.NewReader(queued), &http.Request{Method: http.MethodGet})
	if response != nil {
		_ = response.Body.Close()
	}
	require.Error(t, err, "a queued foreign peer must close before it receives an HTTP response")
	assert.Zero(t, foreignAdmissions.Load())
	// A genuine current-account request queued before chmod still reaches HTTP.
	response, err = http.ReadResponse(bufio.NewReader(queuedCurrent), &http.Request{Method: http.MethodGet})
	require.NoError(t, err)
	queuedBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "local administrator", string(queuedBody))
	transport := &http.Transport{DialContext: HTTPDialContext(unixDialer(path)), DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://localhost/admin", nil)
	require.NoError(t, err)
	response, err = client.Do(request)
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "local administrator", string(body))
	assert.Zero(t, foreignAdmissions.Load())
	assert.EqualValues(t, 2, admissions.Load())
	assert.EqualValues(t, 3, queries.Load())
}

func TestUnixPeerListenerChmodFailureClosesItsNativeSocket(t *testing.T) {
	path := uniqueUnixPathShort(t)
	fault := errors.New("chmod fault")
	listener, err := listenUnixWithPermissions(path, func(string, os.FileMode) error { return fault })
	require.Nil(t, listener)
	require.ErrorIs(t, err, fault)
	_, err = os.Lstat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	// The failed publication cannot retain ownership of a replacement pathname.
	replacement, err := Listen("unix:" + path)
	require.NoError(t, err)
	require.NoError(t, replacement.Close())
}

func TestUnixPeerListenerReportsItsNativeAddress(t *testing.T) {
	listener, path := verifiedPeerListener(t)
	assert.Equal(t, "unix", listener.Addr().Network())
	assert.Equal(t, path, listener.Addr().String())
	assert.NoError(t, unixPeerCredentialsAvailable())
}
