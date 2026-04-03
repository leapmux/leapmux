package main

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTunnelManager_CreatePortForward_Validation(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	ctx := context.Background()

	t.Run("empty workerId", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, TunnelConfig{Type: "port_forward", TargetAddr: "127.0.0.1", TargetPort: 80})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "workerId is required")
	})

	t.Run("invalid type", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, TunnelConfig{WorkerID: "w1", Type: "invalid"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "type must be")
	})

	t.Run("port_forward missing targetAddr", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, TunnelConfig{WorkerID: "w1", Type: "port_forward", TargetPort: 80})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "targetAddr is required")
	})

	t.Run("port_forward invalid targetPort 0", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, TunnelConfig{WorkerID: "w1", Type: "port_forward", TargetAddr: "127.0.0.1", TargetPort: 0})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "targetPort must be")
	})

	t.Run("port_forward invalid targetPort 70000", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, TunnelConfig{WorkerID: "w1", Type: "port_forward", TargetAddr: "127.0.0.1", TargetPort: 70000})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "targetPort must be")
	})

	t.Run("invalid bindPort", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, TunnelConfig{WorkerID: "w1", Type: "port_forward", TargetAddr: "127.0.0.1", TargetPort: 80, BindPort: 70000})
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
	assert.Equal(t, "port_forward", tunnels[0].Type)

	// Delete should succeed.
	err = m.DeleteTunnel("test-t1")
	require.NoError(t, err)
	assert.Empty(t, m.ListTunnels())

	// Verify listener is closed.
	_, err = ln.Accept()
	assert.Error(t, err, "listener should be closed after delete")
	_ = ctx // keep linter happy
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
			info:     TunnelInfo{ID: id, WorkerID: "w1", Type: "port_forward"},
			listener: ln,
			cancel:   cancel,
		}
		m.mu.Unlock()
	}

	assert.Len(t, m.ListTunnels(), 3)

	m.CloseAll()

	assert.Empty(t, m.ListTunnels())
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
	_, err := m.CreateTunnel(context.Background(), cfg)
	require.Error(t, err)
	// The error should be about channel, not about validation.
	assert.Contains(t, err.Error(), "open channel")
}

func TestTunnelManager_DefaultBindPort_Socks5(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()

	cfg := TunnelConfig{
		WorkerID: "w1",
		Type:     "socks5",
		BindAddr: "127.0.0.1",
		// BindPort: 0 — should default to 1080
	}

	_, err := m.CreateTunnel(context.Background(), cfg)
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

	_, err := m.CreateTunnel(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open channel")
}
