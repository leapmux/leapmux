package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tunnelpkg "github.com/leapmux/leapmux/tunnel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTunnelManager_CreatePortForward_Validation(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	ctx := context.Background()

	t.Run("empty workerId", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{Type: tunnelTypePortForward, TargetAddr: "127.0.0.1", TargetPort: 80})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "workerId is required")
	})

	t.Run("nil operation context", func(t *testing.T) {
		_, err := m.CreateTunnel(nil, ctx, TunnelConfig{}) //nolint:staticcheck // Exercise the public boundary.
		require.EqualError(t, err, "operation context is required")
	})

	t.Run("nil lifetime context", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, nil, TunnelConfig{})
		require.EqualError(t, err, "lifetime context is required")
	})

	t.Run("invalid type", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{WorkerID: "w1", Type: "invalid"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "type must be")
	})

	t.Run("port_forward missing targetAddr", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetPort: 80})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "targetAddr is required")
	})

	t.Run("port_forward invalid targetPort 0", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "127.0.0.1", TargetPort: 0})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "targetPort must be")
	})

	t.Run("port_forward invalid targetPort 70000", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "127.0.0.1", TargetPort: 70000})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "targetPort must be")
	})

	t.Run("invalid bindPort", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "127.0.0.1", TargetPort: 80, BindPort: 70000})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bindPort must be")
	})
}

func TestTunnelManager_ListAndDelete(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()

	// Initially empty.
	assert.Empty(t, m.ListTunnels())

	// We can't create a real tunnel without a channel, but we can test
	// the Delete path with a manually inserted tunnel.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	m.mu.Lock()
	m.tunnels["test-t1"] = &tunnel{
		info: TunnelInfo{
			ID:         "test-t1",
			WorkerID:   "w1",
			Type:       "port_forward",
			BindAddr:   "127.0.0.1",
			BindPort:   ln.Addr().(*net.TCPAddr).Port,
			TargetAddr: "10.0.0.1",
			TargetPort: 8080,
		},
		listener: ln,
		cancel:   cancel,
	}
	m.mu.Unlock()

	// List should return the tunnel.
	tunnels := m.ListTunnels()
	require.Len(t, tunnels, 1)
	assert.Equal(t, "test-t1", tunnels[0].ID)
	assert.Equal(t, tunnelTypePortForward, tunnels[0].Type)

	// Delete should succeed.
	err = m.DeleteTunnel("test-t1")
	require.NoError(t, err)
	assert.Empty(t, m.ListTunnels())

	// Verify listener is closed.
	_, err = ln.Accept()
	assert.Error(t, err, "listener should be closed after delete")
	_ = ctx // keep linter happy
}

// scriptedListener returns a scripted sequence of Accept results, so a test can
// drive the accept loop's error policy without provoking real fd exhaustion.
//
// `results` is consumed first, one error per Accept. Once it is exhausted, any
// conns in `conns` are handed out in order -- letting a test assert that an Accept
// FOLLOWING a transient failure still delivers. After both are spent, Accept blocks
// until Close so the loop parks instead of spinning.
type scriptedListener struct {
	mu      sync.Mutex
	results []error
	conns   []net.Conn
	calls   int
	closed  chan struct{}
}

func newScriptedListener(results ...error) *scriptedListener {
	return &scriptedListener{results: results, closed: make(chan struct{})}
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	i := l.calls
	l.calls++
	var err error
	if i < len(l.results) {
		err = l.results[i]
	}
	var conn net.Conn
	if err == nil && len(l.conns) > 0 {
		conn, l.conns = l.conns[0], l.conns[1:]
	}
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if conn != nil {
		return conn, nil
	}
	// Script exhausted: block until closed so the loop parks instead of spinning.
	<-l.closed
	return nil, net.ErrClosed
}

func (l *scriptedListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}
func (l *scriptedListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero} }

func (l *scriptedListener) acceptCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// installTestTunnel registers a tunnel backed by ln so the accept loop can be
// driven directly.
func installTestTunnel(t *testing.T, m *TunnelManager, id string, ln net.Listener) (*tunnel, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tn := &tunnel{
		info:        TunnelInfo{ID: id, WorkerID: "w1", Type: tunnelTypePortForward},
		listener:    ln,
		cancel:      cancel,
		connections: make(map[net.Conn]struct{}),
	}
	m.mu.Lock()
	m.tunnels[id] = tn
	m.mu.Unlock()
	return tn, ctx
}

// A TRANSIENT accept failure must not kill the tunnel. EMFILE is fd exhaustion --
// exactly what a browser fanning out through SOCKS5 or a `git fetch` over many
// port-forward conns provokes -- and it clears on its own in milliseconds.
// Treating it as fatal left the tunnel registered and reported healthy while it
// accepted nothing ever again, so every new local connection failed with no
// visible cause.
func TestTunnelManager_AcceptRetriesTransientError(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	ln := newScriptedListener(transientAcceptErr(), transientAcceptErr())
	tn, ctx := installTestTunnel(t, m, "t-transient", ln)

	done := make(chan struct{})
	go func() { defer close(done); m.acceptPortForward(ctx, tn, nil) }()

	// The loop must get past both transient failures and still be serving.
	require.Eventually(t, func() bool { return ln.acceptCalls() > 2 },
		2*time.Second, 10*time.Millisecond,
		"a transient accept error must be retried, not fatal")
	assert.Len(t, m.ListTunnels(), 1, "the tunnel survives a transient accept error")

	tn.cancel()
	_ = ln.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("accept loop did not exit on cancel")
	}
}

// The SOCKS5 path must ride out a transient accept error too.
//
// It does not run acceptPortForward's loop: it hands its listener to go-socks5's
// Server.Serve, which returns on ANY Accept error and has no retry of its own, and
// serveSocks5 then fails the tunnel. So while the retry policy lived only in
// acceptPortForward, an EMFILE spike -- the "browser fanning out through SOCKS5"
// workload isTemporaryAcceptError explicitly cites -- was ridden out by a
// port-forward tunnel but permanently deleted a SOCKS5 one. tunnelListener.Accept
// is the shared seam both loops now retry through, so this drives it directly.
func TestTunnelListenerAcceptRetriesTransientError(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	// Two transient failures, then a real connection: Serve must never see them.
	ln := newScriptedListener(transientAcceptErr(), transientAcceptErr())
	ln.conns = []net.Conn{server}
	tn, ctx := installTestTunnel(t, m, "t-socks-transient", ln)

	accepted := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := tunnelListener{Listener: ln, tunnel: tn, ctx: ctx}.Accept()
		if err != nil {
			errCh <- err
			return
		}
		accepted <- conn
	}()

	select {
	case conn := <-accepted:
		assert.NotNil(t, conn, "the connection after the transient failures is delivered")
		assert.Greater(t, ln.acceptCalls(), 2, "both EMFILEs were retried, not surfaced")
	case err := <-errCh:
		t.Fatalf("a transient accept error must be retried, not surfaced to Serve: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Accept never returned the connection that followed the transient failures")
	}
}

// A PERMANENT accept failure must still surface through tunnelListener, so
// serveSocks5 fails the tunnel rather than spinning forever.
func TestTunnelListenerAcceptSurfacesPermanentError(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	ln := newScriptedListener(errors.New("listener is broken"))
	tn, ctx := installTestTunnel(t, m, "t-socks-fatal", ln)

	_, err := tunnelListener{Listener: ln, tunnel: tn, ctx: ctx}.Accept()
	require.Error(t, err, "a permanent accept error must reach Serve so the tunnel is failed")
	assert.Contains(t, err.Error(), "listener is broken")
}

// A PERMANENT accept failure must remove the tunnel. The accept loop is gone for
// good, so leaving it registered kept ListTunnels (and the UI) reporting a tunnel
// that could never accept again -- a silent death.
func TestTunnelManager_AcceptFailureRemovesTunnel(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	ln := newScriptedListener(errors.New("listener is broken"))
	tn, ctx := installTestTunnel(t, m, "t-fatal", ln)
	require.Len(t, m.ListTunnels(), 1)

	m.acceptPortForward(ctx, tn, nil)

	assert.Empty(t, m.ListTunnels(),
		"a tunnel whose listener died must not keep reporting healthy")
}

func TestTunnelManager_DeleteNonExistent(t *testing.T) {
	m := NewTunnelManager()
	err := m.DeleteTunnel("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tunnel not found")
}

func TestTunnelManager_CloseAll(t *testing.T) {
	m := NewTunnelManager()

	// Insert 3 fake tunnels with real listeners.
	for i := 0; i < 3; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		_, cancel := context.WithCancel(context.Background())
		id := "t-" + string(rune('a'+i))
		m.mu.Lock()
		m.tunnels[id] = &tunnel{
			info:     TunnelInfo{ID: id, WorkerID: "w1", Type: tunnelTypePortForward},
			listener: ln,
			cancel:   cancel,
		}
		m.mu.Unlock()
	}

	assert.Len(t, m.ListTunnels(), 3)

	m.CloseAll()

	assert.Empty(t, m.ListTunnels())
}

func TestTunnelManager_CanceledLeaderDoesNotCancelChannelForFollower(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	channel, _ := newTestManagedChannel()
	started := make(chan struct{})
	release := make(chan struct{})
	openLifetime := make(chan context.Context, 1)
	var startOnce sync.Once
	m.openCh = func(
		ctx context.Context,
		_, _ string,
		opts *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		startOnce.Do(func() { close(started) })
		openLifetime <- opts.LifetimeContext
		select {
		case <-release:
			return channel, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	cfg := TunnelConfig{HubURL: "http://hub", WorkerID: "worker"}
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan error, 1)
	go func() {
		_, err := m.getOrOpenChannel(leaderCtx, m.currentRevision(), cfg)
		leaderResult <- err
	}()
	<-started
	lifetimeCtx := <-openLifetime

	followerResult := make(chan error, 1)
	go func() {
		got, err := m.getOrOpenChannel(context.Background(), m.currentRevision(), cfg)
		if err == nil {
			assert.Same(t, channel, got)
		}
		followerResult <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancelLeader()

	require.ErrorIs(t, <-leaderResult, context.Canceled)
	assert.NoError(t, lifetimeCtx.Err(), "cached channel lifetime inherited the canceled leader")
	close(release)
	require.NoError(t, <-followerResult)
}

func TestTunnelManager_ChannelOpenTimesOutOnStalledHub(t *testing.T) {
	// A hub that accepts the connection but never completes the handshake must
	// not wedge the channel open forever (the open runs under the epoch context,
	// which has no deadline, and the desktop proxy clients carry no timeout).
	// channelOpenTimeout fences it so the accepted local conn fails fast instead
	// of hanging until tunnel teardown.
	prev := channelOpenTimeout
	channelOpenTimeout = 50 * time.Millisecond
	defer func() { channelOpenTimeout = prev }()

	m := NewTunnelManager()
	defer m.CloseAll()

	openReleased := make(chan struct{}, 1)
	m.openCh = func(
		ctx context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		<-ctx.Done() // stalled hub: block until the open deadline fences ctx
		openReleased <- struct{}{}
		return nil, ctx.Err()
	}

	start := time.Now()
	_, err := m.getOrOpenChannel(context.Background(), m.currentRevision(), TunnelConfig{
		HubURL: "http://hub", WorkerID: "worker",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second, "open should fail at the deadline, not hang")

	select {
	case <-openReleased:
	case <-time.After(time.Second):
		t.Fatal("openCh was never released by the open deadline")
	}
}

func TestTunnelManager_DoesNotShareChannelsAcrossConnectionIdentity(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	firstChannel, _ := newTestManagedChannel()
	secondChannel, _ := newTestManagedChannel()
	thirdChannel, _ := newTestManagedChannel()
	channels := []*managedChannel{firstChannel, secondChannel, thirdChannel}
	openCalls := 0
	m.openCh = func(
		_ context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		ch := channels[openCalls]
		openCalls++
		return ch, nil
	}

	first, err := m.getOrOpenChannel(context.Background(), m.currentRevision(), TunnelConfig{
		HubURL: "http://first-hub", WorkerID: "worker",
	})
	require.NoError(t, err)
	second, err := m.getOrOpenChannel(context.Background(), m.currentRevision(), TunnelConfig{
		HubURL: "http://second-hub", WorkerID: "worker",
	})
	require.NoError(t, err)

	assert.Same(t, channels[0], first)
	assert.Same(t, channels[1], second)

	m.SetChannelOptions(&tunnelpkg.OpenChannelOptions{BearerToken: "new-options"})
	third, err := m.getOrOpenChannel(context.Background(), m.currentRevision(), TunnelConfig{
		HubURL: "http://second-hub", WorkerID: "worker",
	})
	require.NoError(t, err)
	assert.Same(t, channels[2], third)
	assert.Equal(t, 3, openCalls)
}

func TestTunnelManager_CloseAllFencesInflightTunnelCreation(t *testing.T) {
	m := NewTunnelManager()
	started := make(chan struct{})
	release := make(chan struct{})
	channel, channelClosed := newTestManagedChannel()
	m.openCh = func(
		_ context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		close(started)
		<-release
		return channel, nil
	}

	result := make(chan error, 1)
	go func() {
		_, err := m.CreateTunnel(context.Background(), context.Background(), TunnelConfig{
			HubURL: "http://hub", WorkerID: "worker",
			Type: tunnelTypePortForward, TargetAddr: "target", TargetPort: 80,
			BindAddr: "127.0.0.1", BindPort: 18080,
		})
		result <- err
	}()
	<-started
	m.CloseAll()
	close(release)

	require.Error(t, <-result)
	assert.True(t, channelClosed.Load(), "stale channel was not closed")
	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Empty(t, m.channels)
	assert.Empty(t, m.tunnels)
}

func TestTunnelManager_RemovesClosedCachedChannel(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	first, _ := newTestManagedChannel()
	second, _ := newTestManagedChannel()
	channels := []*managedChannel{first, second}
	openCalls := 0
	m.openCh = func(
		_ context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		ch := channels[openCalls]
		openCalls++
		return ch, nil
	}
	cfg := TunnelConfig{HubURL: "http://hub", WorkerID: "worker"}

	opened, err := m.getOrOpenChannel(context.Background(), m.currentRevision(), cfg)
	require.NoError(t, err)
	opened.close()
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.channels) == 0
	}, time.Second, time.Millisecond)

	reopened, err := m.getOrOpenChannel(context.Background(), m.currentRevision(), cfg)
	require.NoError(t, err)
	assert.Same(t, second, reopened)
	assert.Equal(t, 2, openCalls)
}

func TestTunnelManager_TeardownClosesPortForwardConnections(t *testing.T) {
	tests := []struct {
		name  string
		close func(*TunnelManager, string) error
	}{
		{
			name: "DeleteTunnel",
			close: func(m *TunnelManager, id string) error {
				return m.DeleteTunnel(id)
			},
		},
		{
			name: "CloseAll",
			close: func(m *TunnelManager, _ string) error {
				m.CloseAll()
				return nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewTunnelManager()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			tun := &tunnel{
				info: TunnelInfo{
					ID: "tunnel", WorkerID: "worker", Type: tunnelTypePortForward,
					TargetAddr: "target", TargetPort: 80,
				},
				listener: listener,
				cancel:   cancel,
				// Match CreateTunnel: handlePortForward tracks each owned conn in
				// this map (addConnection), so a live tunnel must have it non-nil.
				connections: make(map[net.Conn]struct{}),
			}
			m.tunnels[tun.info.ID] = tun
			target, targetPeer := net.Pipe()
			t.Cleanup(func() {
				_ = target.Close()
				_ = targetPeer.Close()
			})
			dialed := make(chan struct{})
			m.dial = func(_ context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
				close(dialed)
				return target, nil
			}
			go m.acceptPortForward(ctx, tun, nil)

			client, err := net.Dial("tcp", listener.Addr().String())
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })
			<-dialed
			require.NoError(t, test.close(m, tun.info.ID))

			assertConnectionClosed(t, client, "accepted client connection remained open")
			assertConnectionClosed(t, targetPeer, "remote tunnel connection remained open")
		})
	}
}

func TestTunnelManager_DeleteCancelsInFlightPortForwardDial(t *testing.T) {
	m := NewTunnelManager()
	ctx, cancel := context.WithCancel(context.Background())
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	tun := &tunnel{
		info: TunnelInfo{
			ID: "in-flight", WorkerID: "worker", Type: tunnelTypePortForward,
			TargetAddr: "target", TargetPort: 80,
		},
		cancel:      cancel,
		connections: map[net.Conn]struct{}{client: {}},
	}
	m.tunnels[tun.info.ID] = tun
	dialStarted := make(chan struct{})
	m.dial = func(ctx context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
		close(dialStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	handlerDone := make(chan struct{})
	go func() {
		m.handlePortForward(ctx, client, tun, nil)
		close(handlerDone)
	}()
	<-dialStarted
	require.NoError(t, m.DeleteTunnel(tun.info.ID))

	completed := false
	select {
	case <-handlerDone:
		completed = true
	case <-time.After(200 * time.Millisecond):
	}
	assert.True(t, completed, "tunnel deletion left the remote dial running")
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

type testAddrConn struct {
	net.Conn
	localAddr net.Addr
}

func (c testAddrConn) LocalAddr() net.Addr { return c.localAddr }

func TestTunnelManager_DeleteClosesSocks5Connections(t *testing.T) {
	m := NewTunnelManager()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	tun := &tunnel{
		info:     TunnelInfo{ID: "socks", WorkerID: "worker", Type: tunnelTypeSocks5},
		listener: listener,
		cancel:   cancel,
		// Match CreateTunnel: serveSocks5 owns the accepted client and dialed
		// target via addConnection, which writes this map -- it must be non-nil.
		connections: make(map[net.Conn]struct{}),
	}
	m.tunnels[tun.info.ID] = tun
	target, targetPeer := net.Pipe()
	t.Cleanup(func() {
		_ = target.Close()
		_ = targetPeer.Close()
	})
	dialed := make(chan struct{})
	m.dial = func(_ context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
		close(dialed)
		return testAddrConn{
			Conn:      target,
			localAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345},
		}, nil
	}
	go m.serveSocks5(ctx, tun, nil)

	client, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.SetDeadline(time.Now().Add(time.Second)))
	_, err = client.Write([]byte{5, 1, 0})
	require.NoError(t, err)
	greeting := make([]byte, 2)
	_, err = io.ReadFull(client, greeting)
	require.NoError(t, err)
	require.Equal(t, []byte{5, 0}, greeting)
	_, err = client.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 80})
	require.NoError(t, err)
	reply := make([]byte, 10)
	_, err = io.ReadFull(client, reply)
	require.NoError(t, err)
	require.Zero(t, reply[1], "SOCKS5 CONNECT failed")
	<-dialed
	require.NoError(t, client.SetDeadline(time.Time{}))

	require.NoError(t, m.DeleteTunnel(tun.info.ID))
	assertConnectionClosed(t, client, "SOCKS5 client connection remained open")
	assertConnectionClosed(t, targetPeer, "SOCKS5 remote tunnel connection remained open")
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func assertConnectionClosed(t *testing.T, conn net.Conn, message string) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		assert.False(t, isTimeoutError(err), message)
		return
	}
	_, err := conn.Read(make([]byte, 1))
	require.Error(t, err)
	assert.False(t, isTimeoutError(err), message)
}

func TestOwnedTunnelConnForwardsCloseWriteForSocks5(t *testing.T) {
	// go-socks5 asserts `interface { CloseWrite() error }` on the client
	// connection after the remote->client copy to signal read-EOF. Wrapping the
	// accepted *net.TCPConn in ownedTunnelConn must not hide CloseWrite from
	// that assertion (regression: tunnelListener.Accept returns the wrapper).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	server := <-accepted
	require.NotNil(t, server)
	tcpConn := server.(*net.TCPConn)
	defer func() { _ = tcpConn.Close() }()

	owned := &ownedTunnelConn{Conn: tcpConn, tunnel: &tunnel{connections: make(map[net.Conn]struct{})}}

	// Compile-time: satisfies go-socks5's closeWriter interface.
	var closeWriter interface{ CloseWrite() error } = owned

	peerSawEOF := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, client) // returns once the server half-closes
		close(peerSawEOF)
	}()

	require.NoError(t, closeWriter.CloseWrite())
	select {
	case <-peerSawEOF:
	case <-time.After(time.Second):
		t.Fatal("CloseWrite did not half-close the underlying TCP conn (peer saw no EOF)")
	}
}

// fakeChannel stands in for *tunnelpkg.Channel: the manager never opens a real one
// under test, which would need a hub, a WebSocket, and a Noise handshake. It mirrors
// the contract the manager depends on -- Close is idempotent and ends the lifetime,
// and Closed reports both an explicit close and a lifetime cancelled from elsewhere,
// exactly as tunnel.Channel does.
type fakeChannel struct {
	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool
}

func (f *fakeChannel) Closed() bool { return f.closed.Load() || f.ctx.Err() != nil }

func (f *fakeChannel) Close() {
	if f.closed.CompareAndSwap(false, true) {
		f.cancel()
	}
}

func (f *fakeChannel) Context() context.Context { return f.ctx }

// newTestManagedChannel returns a cached-channel double and the flag reporting
// whether it has been closed. Its concrete channel is nil: every test using it also
// overrides TunnelManager.dial, which is the only thing that reads it.
func newTestManagedChannel() (*managedChannel, *atomic.Bool) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeChannel{ctx: ctx, cancel: cancel}
	return &managedChannel{handle: fake}, &fake.closed
}

func TestTunnelManager_DefaultBindPort_PortForward(t *testing.T) {
	// Verify that CreateTunnel uses TargetPort as default BindPort.
	// We can't fully create (no channel), but we can check validation passes.
	m := NewTunnelManager()
	defer m.CloseAll()

	cfg := TunnelConfig{
		WorkerID:   "w1",
		Type:       "port_forward",
		TargetAddr: "10.0.0.1",
		TargetPort: 3000,
		BindAddr:   "127.0.0.1",
		// BindPort: 0 — should default to 3000
	}

	// This will fail at getOrOpenChannel (no Hub), but the validation should pass.
	ctx := context.Background()
	_, err := m.CreateTunnel(ctx, ctx, cfg)
	require.Error(t, err)
	// The error should be about channel, not about validation.
	assert.Contains(t, err.Error(), "open channel")
}

func TestTunnelManager_DefaultBindPort_Socks5(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()

	cfg := TunnelConfig{
		WorkerID: "w1",
		Type:     tunnelTypeSocks5,
		BindAddr: "127.0.0.1",
		// BindPort: 0 — should default to 1080
	}

	ctx := context.Background()
	_, err := m.CreateTunnel(ctx, ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open channel")
}

func TestTunnelManager_DefaultBindAddr(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()

	cfg := TunnelConfig{
		WorkerID:   "w1",
		Type:       "port_forward",
		TargetAddr: "10.0.0.1",
		TargetPort: 3000,
		// BindAddr: "" — should default to 127.0.0.1
	}

	ctx := context.Background()
	_, err := m.CreateTunnel(ctx, ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open channel")
}

// TestTunnelConfig_ValidateAndNormalize asserts the config defaulting and
// validation rules DIRECTLY, without binding a socket or opening a channel --
// the seam CreateTunnel's extracted validateAndNormalize provides. The
// CreateTunnel-level tests above can only assert defaulting indirectly (they
// infer it from reaching the "open channel" failure); here the normalized values
// and the loopback-only invariant are checked at their source.
func TestTunnelConfig_ValidateAndNormalize(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cases := []struct {
			name         string
			cfg          TunnelConfig
			wantBindAddr string
			wantBindPort int
		}{
			{
				name:         "port_forward defaults bind port to target port",
				cfg:          TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "10.0.0.1", TargetPort: 3000, BindAddr: "127.0.0.1"},
				wantBindAddr: "127.0.0.1",
				wantBindPort: 3000,
			},
			{
				name:         "socks5 defaults bind port to 1080",
				cfg:          TunnelConfig{WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "127.0.0.1"},
				wantBindAddr: "127.0.0.1",
				wantBindPort: 1080,
			},
			{
				name:         "empty bind addr defaults to loopback",
				cfg:          TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "10.0.0.1", TargetPort: 3000},
				wantBindAddr: defaultTunnelBindAddr,
				wantBindPort: 3000,
			},
			{
				name:         "explicit bind port is preserved",
				cfg:          TunnelConfig{WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "127.0.0.1", BindPort: 9999},
				wantBindAddr: "127.0.0.1",
				wantBindPort: 9999,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				cfg := tc.cfg
				require.NoError(t, cfg.validateAndNormalize())
				assert.Equal(t, tc.wantBindAddr, cfg.BindAddr)
				assert.Equal(t, tc.wantBindPort, cfg.BindPort)
			})
		}
	})

	t.Run("rejections", func(t *testing.T) {
		cases := []struct {
			name    string
			cfg     TunnelConfig
			wantErr string
		}{
			{"missing worker id", TunnelConfig{Type: tunnelTypeSocks5, BindAddr: "127.0.0.1"}, "workerId is required"},
			{"bad type", TunnelConfig{WorkerID: "w1", Type: "bogus", BindAddr: "127.0.0.1"}, "type must be"},
			{"port_forward missing target addr", TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetPort: 3000, BindAddr: "127.0.0.1"}, "targetAddr is required"},
			{"port_forward target port out of range", TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "10.0.0.1", TargetPort: 70000, BindAddr: "127.0.0.1"}, "targetPort must be 1-65535"},
			{"non-loopback bind addr", TunnelConfig{WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "0.0.0.0"}, "loopback"},
			{"bind port out of range", TunnelConfig{WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "127.0.0.1", BindPort: 70000}, "bindPort must be 1-65535"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				cfg := tc.cfg
				err := cfg.validateAndNormalize()
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			})
		}
	})
}

// A channel whose LIFETIME was cancelled without an explicit Close is dead too, and
// the manager must treat it as such.
//
// tunnel.Channel reports exactly that (see its Closed doc: a shared-transport write
// failure calls ch.cancel(), and only recvLoop's deferred Close later sets `closed`),
// so a manager that keyed only on an explicit close would keep handing tunnels a
// channel whose every dial fails instead of re-resolving a live one. The manager sees
// both facts through the same handle, which is what makes them impossible to observe
// out of sync.
func TestTunnelManager_LiveChannelReResolvesWhenLifetimeCancelled(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()

	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	dying := &managedChannel{handle: &fakeChannel{ctx: lifetimeCtx, cancel: cancelLifetime}}
	fresh, _ := newTestManagedChannel()
	channels := []*managedChannel{dying, fresh}
	openCalls := 0
	m.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		ch := channels[openCalls]
		openCalls++
		return ch, nil
	}
	const hubURL, workerID = "http://hub", "worker"

	opened, err := m.getOrOpenChannel(context.Background(), m.currentRevision(), TunnelConfig{HubURL: hubURL, WorkerID: workerID})
	require.NoError(t, err)
	require.Same(t, dying, opened)

	// The transport dies: the lifetime ends, but nothing calls Close.
	cancelLifetime()
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.channels) == 0
	}, time.Second, time.Millisecond, "a channel whose lifetime ended must be evicted from the cache")

	live, err := m.liveChannel(context.Background(), dying, hubURL, workerID)
	require.NoError(t, err)
	assert.Same(t, fresh, live, "a tunnel holding a lifetime-cancelled channel must re-resolve a live one")
	assert.Equal(t, 2, openCalls)
}

// liveChannel is the tunnel self-heal path: a tunnel must keep working after its
// captured E2EE channel dies (worker/hub reconnect), re-resolving a fresh
// channel instead of rejecting every connection against the dead one.
func TestTunnelManager_LiveChannelReResolvesWhenClosed(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	first, _ := newTestManagedChannel()
	second, _ := newTestManagedChannel()
	channels := []*managedChannel{first, second}
	openCalls := 0
	m.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		ch := channels[openCalls]
		openCalls++
		return ch, nil
	}
	const hubURL, workerID = "http://hub", "worker"

	// Seed the cache with `first`.
	opened, err := m.getOrOpenChannel(context.Background(), m.currentRevision(), TunnelConfig{HubURL: hubURL, WorkerID: workerID})
	require.NoError(t, err)
	require.Same(t, first, opened)

	// While first is alive, liveChannel returns it without re-opening.
	live, err := m.liveChannel(context.Background(), first, hubURL, workerID)
	require.NoError(t, err)
	require.Same(t, first, live)
	assert.Equal(t, 1, openCalls)

	// Close first (evicts it from the cache); liveChannel must re-resolve.
	first.close()
	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.channels) == 0
	}, time.Second, time.Millisecond)
	live, err = m.liveChannel(context.Background(), first, hubURL, workerID)
	require.NoError(t, err)
	require.Same(t, second, live)
	assert.Equal(t, 2, openCalls)

	// A nil captured channel (test injection / no captured channel) is returned
	// as-is so injected dial seams that ignore the channel keep working.
	live, err = m.liveChannel(context.Background(), nil, hubURL, workerID)
	require.NoError(t, err)
	assert.Nil(t, live)
}

// TestPortForwardDeliversResponseAfterClientHalfClose is the S4 regression: the
// previous full-close-on-first-done closed the client's read side as soon as the
// client half-closed its write side (after the request), truncating the
// in-flight response. The half-close copy must forward the half-close and still
// deliver the response.
func TestPortForwardDeliversResponseAfterClientHalfClose(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	tun := &tunnel{
		info: TunnelInfo{
			ID: "pf", WorkerID: "w", Type: tunnelTypePortForward,
			TargetAddr: "target", TargetPort: 80,
			BindAddr: "127.0.0.1", BindPort: listener.Addr().(*net.TCPAddr).Port,
		},
		hubURL:      "http://hub",
		listener:    listener,
		cancel:      cancel,
		connections: make(map[net.Conn]struct{}),
	}
	m.mu.Lock()
	m.tunnels[tun.info.ID] = tun
	m.mu.Unlock()

	// Remote target: read the full request (until the client's write half-close
	// arrives), then send a response.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = targetLn.Close() })
	go func() {
		c, acceptErr := targetLn.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.ReadAll(c) // drains once the forwarded client half-close arrives
		_, _ = c.Write([]byte("response"))
	}()
	m.dial = func(_ context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
		return net.Dial("tcp", targetLn.Addr().String())
	}
	go m.acceptPortForward(ctx, tun, nil)

	client, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	_, err = client.Write([]byte("request"))
	require.NoError(t, err)
	// Half-close the write side, like an HTTP/1.0 request. The old code
	// full-closed the client's read side at this point, truncating "response".
	require.NoError(t, client.(*net.TCPConn).CloseWrite())

	require.NoError(t, client.SetReadDeadline(time.Now().Add(2*time.Second)))
	resp, err := io.ReadAll(client)
	require.NoError(t, err)
	assert.Equal(t, "response", string(resp), "response truncated after client half-close")
}

// TestPortForwardReleasesConnWhenClientAborts pins the bound on the half-close's
// abort path: a client that vanishes with an RST leaves the remote->local copy
// parked in Read with nothing to wake it (no read deadline on this path, and ctx
// is the whole tunnel's lifetime), so handlePortForward blocked on its second
// <-copyDone forever, pinning the goroutine, both fds, the tunneled Conn's
// send-window slot and its worker-side conn-map entry until tunnel teardown.
//
// The sibling TestPortForwardDeliversResponseAfterClientHalfClose is the other
// half of the invariant: a CLEAN half-close must still linger for the response.
func TestPortForwardReleasesConnWhenClientAborts(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	tun := &tunnel{
		info: TunnelInfo{
			ID: "pf-abort", WorkerID: "w", Type: tunnelTypePortForward,
			TargetAddr: "target", TargetPort: 80,
			BindAddr: "127.0.0.1", BindPort: listener.Addr().(*net.TCPAddr).Port,
		},
		hubURL:      "http://hub",
		listener:    listener,
		cancel:      cancel,
		connections: make(map[net.Conn]struct{}),
	}
	m.mu.Lock()
	m.tunnels[tun.info.ID] = tun
	m.mu.Unlock()

	// A target that accepts and then goes silent: it never reads, writes, or
	// closes, so only the client's abort can end the copy.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = targetLn.Close() })
	targetAccepted := make(chan net.Conn, 1)
	go func() {
		c, acceptErr := targetLn.Accept()
		if acceptErr != nil {
			return
		}
		targetAccepted <- c
	}()
	m.dial = func(_ context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
		return net.Dial("tcp", targetLn.Addr().String())
	}

	client, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	local, err := listener.Accept()
	require.NoError(t, err)

	handled := make(chan struct{})
	go func() {
		defer close(handled)
		m.handlePortForward(ctx, local, tun, nil)
	}()

	var targetConn net.Conn
	select {
	case targetConn = <-targetAccepted:
	case <-time.After(2 * time.Second):
		t.Fatal("port forward never dialled the target")
	}
	t.Cleanup(func() { _ = targetConn.Close() })

	_, err = client.Write([]byte("request"))
	require.NoError(t, err)
	// SetLinger(0) + Close is an RST, not a FIN: the copy ends in an error rather
	// than a clean EOF, so there is no response left to wait for.
	require.NoError(t, client.(*net.TCPConn).SetLinger(0))
	require.NoError(t, client.Close())

	select {
	case <-handled:
	case <-time.After(3 * time.Second):
		t.Fatal("handlePortForward did not release an aborted connection")
	}
	tun.mu.Lock()
	remaining := len(tun.connections)
	tun.mu.Unlock()
	assert.Zero(t, remaining, "both ends must be released back to the tunnel")
}

// gatedTunnelListener lets a test place an accepted conn into the serve loop at a
// chosen instant: the first Accept parks until the gate opens (or the serve ctx
// dies), later ones park until the serve ctx dies. Only the Serve goroutine calls
// Accept, so handedOff needs no lock.
type gatedTunnelListener struct {
	net.Listener
	gate      <-chan struct{}
	conn      net.Conn
	done      <-chan struct{}
	handedOff bool
}

func (l *gatedTunnelListener) Accept() (net.Conn, error) {
	if !l.handedOff {
		l.handedOff = true
		select {
		case <-l.gate:
			return l.conn, nil
		case <-l.done:
			return nil, net.ErrClosed
		}
	}
	<-l.done
	return nil, net.ErrClosed
}

// t.close() must cancel the tunnel context BEFORE it marks the tunnel closed.
// The two orders are indistinguishable to close()'s own callers, but not to the
// serve loops: `closed` is what makes ownConnection reject an accepted conn, and
// ctx.Err() is what tells serveSocks5 that rejection was a teardown rather than a
// dead listener. Cancelling last leaves a window where the first is true and the
// second is not.
func TestTunnelCloseCancelsBeforeMarkingClosed(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	tun := &tunnel{connections: make(map[net.Conn]struct{})}
	closedAtCancel := true
	tun.cancel = func() {
		tun.mu.Lock()
		closedAtCancel = tun.closed
		tun.mu.Unlock()
		cancel()
	}

	tun.close()
	assert.False(t, closedAtCancel,
		"close() flipped closed before cancelling: the serve loop can see a rejected conn while ctx.Err() is still nil")
}

// The consequence of that ordering, end to end: an ordinary user-initiated
// DeleteTunnel must not be reported as a listener failure. In the old order a conn
// accepted in the window failed ownConnection, go-socks5's Serve returned
// net.ErrClosed, and serveSocks5 -- reading a ctx that was not cancelled yet --
// logged ERROR "tunnel listener failed; removing the tunnel", sending operators
// hunting a failure that never happened.
func TestServeSocks5DoesNotFailTunnelOnClose(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)
	ctx, cancelServe := context.WithCancel(context.Background())
	t.Cleanup(cancelServe)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	accepted, err := listener.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = accepted.Close() })

	gate := make(chan struct{})
	gated := &gatedTunnelListener{Listener: listener, gate: gate, conn: accepted, done: ctx.Done()}
	tun := &tunnel{
		info:        TunnelInfo{ID: "socks-close", WorkerID: "w", Type: tunnelTypeSocks5},
		listener:    gated,
		connections: make(map[net.Conn]struct{}),
	}
	// Hold close() inside cancel so the accepted conn lands in the exact window the
	// ordering governs: whatever close() has already done by the time it calls
	// cancel is what the serve loop observes.
	//
	// Guarded by a Once because close() cancels on every call, including the
	// repeat from the cleanup CloseAll -- harmless for a real context.CancelFunc,
	// which is idempotent, but not for this instrumented stand-in.
	cancelEntered := make(chan struct{})
	releaseCancel := make(chan struct{})
	var cancelOnce sync.Once
	tun.cancel = func() {
		cancelOnce.Do(func() {
			close(cancelEntered)
			<-releaseCancel
			cancelServe()
		})
	}
	m.mu.Lock()
	m.tunnels[tun.info.ID] = tun
	m.mu.Unlock()

	go m.serveSocks5(ctx, tun, nil)

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		tun.close()
	}()
	<-cancelEntered
	close(gate) // deliver the accepted conn into the window

	// failTunnel evicts the tunnel from the manager, so a tunnel still registered
	// is proof no spurious failure was reported.
	require.Never(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.tunnels[tun.info.ID]
		return !ok
	}, 300*time.Millisecond, 5*time.Millisecond,
		"an ordinary tunnel close was reported as a listener failure")

	close(releaseCancel)
	<-closed
}

// bindAddrConformanceFixture is the shared cross-language fixture described in
// testdata/tunnel_bind_addr_conformance.json -- read that file's _readme first.
//
// The frontend hand-mirrors net.ParseIP + IsLoopback (frontend/src/lib/ipAddress.ts)
// so the Add-Tunnel dialog can say WHY an address is refused. This side of the
// fixture pins the mirror's ground truth: change what the sidecar accepts without
// changing ipAddress.ts, and the TS suite reading the same file goes red (and vice
// versa). Fields mirror the JSON exactly; `Why` is documentation, not asserted on.
type bindAddrConformanceFixture struct {
	Cases []struct {
		Input      string `json:"input"`
		Normalized string `json:"normalized"`
		Loopback   bool   `json:"loopback"`
		Why        string `json:"why"`
	} `json:"cases"`
}

// loadBindAddrConformance reads the fixture shared with the frontend suite.
//
// go test runs with CWD = the package dir (desktop/go), so the repo-level testdata/
// dir -- the one home reachable from both desktop/go and frontend/src/lib -- is two
// levels up. A file the go tool ignores by name ("testdata") but both languages can
// read is exactly the point.
func loadBindAddrConformance(t *testing.T) bindAddrConformanceFixture {
	t.Helper()
	const path = "../../testdata/tunnel_bind_addr_conformance.json"
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the conformance fixture is shared with frontend/src/lib/ipAddress.test.ts")

	var fixture bindAddrConformanceFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	// A fixture that silently loads zero cases would make this test pass while
	// asserting nothing -- the one failure mode a shared fixture must not have.
	require.NotEmpty(t, fixture.Cases, "fixture %s loaded no cases", path)
	return fixture
}

// A tunnel must not be bindable beyond loopback.
//
// Both tunnel types forward into the worker's private network and NEITHER listener
// authenticates (the SOCKS5 server is constructed with no auth methods). BindAddr
// is frontend-supplied and was only defaulted, never validated, so "0.0.0.0" --
// from hostile webview content, or a user who just typed it -- turned the machine
// into an open, unauthenticated SOCKS5 gateway any LAN host could pivot through.
//
// The cases come from the fixture shared with frontend/src/lib/ipAddress.test.ts.
// This side asserts the sidecar half of the contract: the sidecar accepts the value
// the dialog would SUBMIT (`normalized`) iff the dialog predicted acceptance
// (`loopback`). The dialog's own half -- that it normalizes and predicts that way --
// is asserted by the TS suite against the same file.
func TestValidateTunnelBindAddr(t *testing.T) {
	fixture := loadBindAddrConformance(t)

	seen := make(map[string]bool, len(fixture.Cases))
	for _, c := range fixture.Cases {
		require.False(t, seen[c.Input], "duplicate fixture input %q", c.Input)
		seen[c.Input] = true

		t.Run(fmt.Sprintf("%s=>%q", accepts(c.Loopback), c.Normalized), func(t *testing.T) {
			err := validateTunnelBindAddr(c.Normalized)
			if c.Loopback {
				assert.NoError(t, err,
					"fixture says the dialog submits %q for input %q and expects it accepted (%s)",
					c.Normalized, c.Input, c.Why)
				return
			}
			assert.Error(t, err,
				"fixture says %q (from input %q) must be refused (%s); an unauthenticated listener must not be reachable off-machine",
				c.Normalized, c.Input, c.Why)
		})
	}
}

// accepts names a fixture verdict for a subtest name.
func accepts(loopback bool) string {
	if loopback {
		return "allows"
	}
	return "refuses"
}

// The error must say WHICH rule refused, since the dialog mirrors both separately.
func TestValidateTunnelBindAddr_ErrorMessages(t *testing.T) {
	assert.ErrorContains(t, validateTunnelBindAddr("localhost"), "must be an IP address",
		"a hostname is not parseable, and the message should say so rather than blame loopback")
	assert.ErrorContains(t, validateTunnelBindAddr("0.0.0.0"), "loopback",
		"a valid non-loopback IP must be refused for being off-machine")
}

// The validation must be enforced by CreateTunnel, not merely available.
func TestCreateTunnel_RejectsNonLoopbackBindAddr(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)
	ctx := context.Background()

	_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{
		Type:     tunnelTypeSocks5,
		BindAddr: "0.0.0.0",
		WorkerID: "w1",
	})
	require.Error(t, err, "CreateTunnel must refuse an off-machine bind address")
	assert.Contains(t, err.Error(), "loopback")
}

// backoffAfterTemporaryAccept is the wait+backoff step shared by the two accept
// loops (acceptWithRetry and acceptBeforeIdleDeadline). It advances the delay only
// when the wait completes, and returns cancelled the moment its cancel channel fires
// -- the property that lets a teardown abandon a retry storm promptly rather than
// sleeping out a full backoff first.
func TestBackoffAfterTemporaryAccept(t *testing.T) {
	t.Run("completing the wait advances the backoff and does not cancel", func(t *testing.T) {
		cancel := make(chan struct{}) // never fires
		next, cancelled := backoffAfterTemporaryAccept(acceptRetryDelayMin, cancel)
		assert.False(t, cancelled, "an uncancelled wait must not report cancellation")
		assert.Equal(t, nextAcceptRetryDelay(acceptRetryDelayMin), next,
			"a completed wait must advance to the next backoff step")
	})

	t.Run("caps the advanced delay at the ceiling", func(t *testing.T) {
		cancel := make(chan struct{})
		next, cancelled := backoffAfterTemporaryAccept(acceptRetryDelayMax, cancel)
		assert.False(t, cancelled)
		assert.Equal(t, acceptRetryDelayMax, next, "the backoff must not exceed its ceiling")
	})

	t.Run("a fired cancel abandons the wait promptly without advancing the delay", func(t *testing.T) {
		cancel := make(chan struct{})
		close(cancel) // already cancelled

		done := make(chan struct{})
		var next time.Duration
		var cancelled bool
		go func() {
			// A large delay proves the early return is driven by cancel, not the timer.
			next, cancelled = backoffAfterTemporaryAccept(time.Hour, cancel)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("a fired cancel must abandon the backoff wait promptly")
		}
		assert.True(t, cancelled, "a fired cancel must report cancellation")
		assert.Equal(t, time.Hour, next, "a cancelled wait must not advance the backoff")
	})
}
