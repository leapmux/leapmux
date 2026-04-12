package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"

	"github.com/google/uuid"
	tunnelpkg "github.com/leapmux/leapmux/tunnel"
	"golang.org/x/sync/singleflight"
)

const (
	tunnelTypePortForward = "port_forward"
	tunnelTypeSocks5      = "socks5"
)

// TunnelConfig is the configuration for creating a tunnel, received from the frontend.
type TunnelConfig struct {
	WorkerID   string `json:"workerId"`
	Type       string `json:"type"`       // "port_forward" or "socks5"
	TargetAddr string `json:"targetAddr"` // port_forward only
	TargetPort int    `json:"targetPort"` // port_forward only
	BindAddr   string `json:"bindAddr"`
	BindPort   int    `json:"bindPort"`
	HubURL     string `json:"hubURL"`
	UserID     string `json:"userId"`
}

// TunnelInfo describes an active tunnel, returned to the frontend.
type TunnelInfo struct {
	ID         string `json:"id"`
	WorkerID   string `json:"workerId"`
	Type       string `json:"type"`
	BindAddr   string `json:"bindAddr"`
	BindPort   int    `json:"bindPort"`
	TargetAddr string `json:"targetAddr"`
	TargetPort int    `json:"targetPort"`
}

// tunnel represents a single active tunnel.
type tunnel struct {
	info     TunnelInfo
	listener net.Listener
	cancel   context.CancelFunc
}

// TunnelManager manages all active tunnels for the desktop app.
type TunnelManager struct {
	mu       sync.Mutex
	tunnels  map[string]*tunnel
	channels map[string]*tunnelpkg.Channel // per-worker E2EE channel
	chanSF   singleflight.Group            // dedup concurrent channel opens per worker
	chanOpts *tunnelpkg.OpenChannelOptions // optional transport overrides (e.g. Unix socket)
}

// NewTunnelManager creates a new TunnelManager.
func NewTunnelManager() *TunnelManager {
	return &TunnelManager{
		tunnels:  make(map[string]*tunnel),
		channels: make(map[string]*tunnelpkg.Channel),
	}
}

// SetChannelOptions sets transport options for opening E2EE channels.
func (m *TunnelManager) SetChannelOptions(opts *tunnelpkg.OpenChannelOptions) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chanOpts = opts
}

// CreateTunnel creates and starts a new tunnel.
func (m *TunnelManager) CreateTunnel(parentCtx context.Context, cfg TunnelConfig) (*TunnelInfo, error) {
	if cfg.WorkerID == "" {
		return nil, fmt.Errorf("workerId is required")
	}
	if cfg.Type != tunnelTypePortForward && cfg.Type != tunnelTypeSocks5 {
		return nil, fmt.Errorf("type must be '%s' or '%s'", tunnelTypePortForward, tunnelTypeSocks5)
	}
	if cfg.Type == tunnelTypePortForward {
		if cfg.TargetAddr == "" {
			return nil, fmt.Errorf("targetAddr is required for port_forward")
		}
		if cfg.TargetPort < 1 || cfg.TargetPort > 65535 {
			return nil, fmt.Errorf("targetPort must be 1-65535")
		}
	}
	if cfg.BindAddr == "" {
		cfg.BindAddr = "127.0.0.1"
	}
	if cfg.BindPort == 0 {
		if cfg.Type == tunnelTypePortForward {
			cfg.BindPort = cfg.TargetPort
		} else {
			cfg.BindPort = 1080
		}
	}
	if cfg.BindPort < 1 || cfg.BindPort > 65535 {
		return nil, fmt.Errorf("bindPort must be 1-65535")
	}

	// Ensure we have an E2EE channel to the worker.
	ch, err := m.getOrOpenChannel(parentCtx, cfg)
	if err != nil {
		slog.Error("failed to open channel to worker", "worker_id", cfg.WorkerID, "error", err)
		return nil, fmt.Errorf("open channel to worker: %w", err)
	}

	// Create TCP listener.
	bindAddr := net.JoinHostPort(cfg.BindAddr, fmt.Sprintf("%d", cfg.BindPort))
	listener, err := net.Listen("tcp", bindAddr)
	if err != nil {
		slog.Error("failed to bind tunnel listener", "bind_addr", bindAddr, "error", err)
		return nil, fmt.Errorf("bind %s: %w", bindAddr, err)
	}

	actualAddr := listener.Addr().(*net.TCPAddr)
	tunnelID := uuid.New().String()
	info := TunnelInfo{
		ID:         tunnelID,
		WorkerID:   cfg.WorkerID,
		Type:       cfg.Type,
		BindAddr:   actualAddr.IP.String(),
		BindPort:   actualAddr.Port,
		TargetAddr: cfg.TargetAddr,
		TargetPort: cfg.TargetPort,
	}

	ctx, cancel := context.WithCancel(parentCtx)
	t := &tunnel{info: info, listener: listener, cancel: cancel}

	m.mu.Lock()
	m.tunnels[tunnelID] = t
	m.mu.Unlock()

	if cfg.Type == tunnelTypeSocks5 {
		go m.serveSocks5(ctx, t, ch)
	} else {
		go m.acceptPortForward(ctx, t, ch)
	}

	slog.Info("tunnel created",
		"tunnel_id", tunnelID, "type", cfg.Type,
		"bind", actualAddr.String(),
		"target", fmt.Sprintf("%s:%d", cfg.TargetAddr, cfg.TargetPort),
	)
	return &info, nil
}

// DeleteTunnel stops and removes a tunnel.
func (m *TunnelManager) DeleteTunnel(tunnelID string) error {
	m.mu.Lock()
	t, ok := m.tunnels[tunnelID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("tunnel not found: %s", tunnelID)
	}
	delete(m.tunnels, tunnelID)
	m.mu.Unlock()

	t.cancel()
	_ = t.listener.Close()
	slog.Info("tunnel deleted", "tunnel_id", tunnelID)
	return nil
}

// ListTunnels returns info about all active tunnels.
func (m *TunnelManager) ListTunnels() []TunnelInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]TunnelInfo, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		result = append(result, t.info)
	}
	return result
}

// CloseAll closes all tunnels and channels.
func (m *TunnelManager) CloseAll() {
	m.mu.Lock()
	tunnels := m.tunnels
	m.tunnels = make(map[string]*tunnel)
	channels := m.channels
	m.channels = make(map[string]*tunnelpkg.Channel)
	m.mu.Unlock()

	for _, t := range tunnels {
		t.cancel()
		_ = t.listener.Close()
	}
	for _, ch := range channels {
		ch.Close()
	}
	slog.Info("all tunnels closed")
}

func (m *TunnelManager) getOrOpenChannel(ctx context.Context, cfg TunnelConfig) (*tunnelpkg.Channel, error) {
	// Fast path: return an existing open channel.
	m.mu.Lock()
	if ch, ok := m.channels[cfg.WorkerID]; ok && !ch.Closed() {
		m.mu.Unlock()
		return ch, nil
	}
	m.mu.Unlock()

	// Deduplicate concurrent dials to the same worker.
	v, err, _ := m.chanSF.Do(cfg.WorkerID, func() (interface{}, error) {
		// Re-check under singleflight: another call may have just finished.
		m.mu.Lock()
		if ch, ok := m.channels[cfg.WorkerID]; ok && !ch.Closed() {
			m.mu.Unlock()
			return ch, nil
		}
		m.mu.Unlock()

		ch, err := tunnelpkg.OpenChannel(ctx, cfg.HubURL, cfg.UserID, cfg.WorkerID, m.chanOpts)
		if err != nil {
			return nil, err
		}

		m.mu.Lock()
		if old, exists := m.channels[cfg.WorkerID]; exists {
			old.Close()
		}
		m.channels[cfg.WorkerID] = ch
		m.mu.Unlock()

		return ch, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*tunnelpkg.Channel), nil
}

// serveSocks5 runs a SOCKS5 server on the tunnel's listener.
// Each CONNECT request is dialed through the E2EE channel via tunnel.DialTunnel.
func (m *TunnelManager) serveSocks5(_ context.Context, t *tunnel, ch *tunnelpkg.Channel) {
	server := newSocks5Server(func(ctx context.Context, _, addr string) (net.Conn, error) {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}
		p, err := strconv.ParseUint(portStr, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid port %q: %w", portStr, err)
		}
		port := uint32(p)
		return tunnelpkg.DialTunnel(ch, host, port)
	})

	if err := server.Serve(t.listener); err != nil {
		slog.Debug("socks5 server stopped", "tunnel_id", t.info.ID, "error", err)
	}
}

// acceptPortForward accepts TCP connections and forwards them to a fixed target.
func (m *TunnelManager) acceptPortForward(ctx context.Context, t *tunnel, ch *tunnelpkg.Channel) {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("tunnel accept error", "tunnel_id", t.info.ID, "error", err)
			return
		}
		go m.handlePortForward(conn, t, ch)
	}
}

// handlePortForward handles a single port-forward TCP connection.
func (m *TunnelManager) handlePortForward(conn net.Conn, t *tunnel, ch *tunnelpkg.Channel) {
	defer func() { _ = conn.Close() }()

	target, err := tunnelpkg.DialTunnel(ch, t.info.TargetAddr, uint32(t.info.TargetPort))
	if err != nil {
		slog.Error("dial tunnel failed", "tunnel_id", t.info.ID, "error", err)
		return
	}
	defer func() { _ = target.Close() }()

	// Bidirectional copy — when one direction ends, close both sides so
	// the other direction unblocks promptly.
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(target, conn)
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		_, _ = io.Copy(conn, target)
	}()

	<-done
	// One direction finished — close both to unblock the other.
	_ = conn.Close()
	_ = target.Close()
	<-done
}
