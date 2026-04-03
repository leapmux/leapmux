package service

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"google.golang.org/protobuf/proto"
)

// tunnelConn tracks a single tunnel connection to a remote target.
type tunnelConn struct {
	conn   net.Conn
	sender *channel.Sender
	closed atomic.Bool
}

// TunnelManager tracks active tunnel connections for a worker.
type TunnelManager struct {
	conns sync.Map // conn_id -> *tunnelConn
}

// NewTunnelManager creates a new TunnelManager.
func NewTunnelManager() *TunnelManager {
	return &TunnelManager{}
}

// Get returns the tunnel connection for the given conn_id, or nil.
func (m *TunnelManager) Get(connID string) *tunnelConn {
	v, ok := m.conns.Load(connID)
	if !ok {
		return nil
	}
	return v.(*tunnelConn)
}

// Store adds a tunnel connection.
func (m *TunnelManager) Store(connID string, tc *tunnelConn) {
	m.conns.Store(connID, tc)
}

// Remove removes and returns a tunnel connection.
func (m *TunnelManager) Remove(connID string) *tunnelConn {
	v, ok := m.conns.LoadAndDelete(connID)
	if !ok {
		return nil
	}
	return v.(*tunnelConn)
}

// CloseAll closes all tunnel connections.
func (m *TunnelManager) CloseAll() {
	m.conns.Range(func(key, value any) bool {
		tc := value.(*tunnelConn)
		if tc.closed.CompareAndSwap(false, true) {
			_ = tc.conn.Close()
		}
		m.conns.Delete(key)
		return true
	})
}

// tunnelReadBufSize is the read buffer size for reading from the target connection.
const tunnelReadBufSize = 32 * 1024 // 32 KiB

// registerTunnelHandlers registers all tunnel-related inner RPC handlers.
func registerTunnelHandlers(d *channel.Dispatcher, svc *Context) {
	tunnels := NewTunnelManager()

	// OpenTunnelConn dials the target address and starts streaming data back.
	d.Register("OpenTunnelConn", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.OpenTunnelConnRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		// Only the worker owner can create tunnels.
		if userID != svc.RegisteredBy {
			sendPermissionDenied(sender, "only the worker owner can create tunnel connections")
			return
		}

		targetAddr := r.GetTargetAddr()
		targetPort := r.GetTargetPort()
		if targetAddr == "" {
			sendInvalidArgument(sender, "target_addr is required")
			return
		}
		if targetPort == 0 || targetPort > 65535 {
			sendInvalidArgument(sender, "target_port must be 1-65535")
			return
		}

		addr := net.JoinHostPort(targetAddr, fmt.Sprintf("%d", targetPort))

		conn, err := net.Dial("tcp", addr)
		if err != nil {
			slog.Error("failed to dial tunnel target", "addr", addr, "error", err)
			sendInternalError(sender, fmt.Sprintf("dial %s: %v", addr, err))
			return
		}

		connID := id.Generate()
		tc := &tunnelConn{conn: conn, sender: sender}
		tunnels.Store(connID, tc)

		slog.Info("tunnel connection opened", "conn_id", connID, "target", addr, "user_id", userID)

		// Send the response with the generated conn_id.
		sendProtoResponse(sender, &leapmuxv1.OpenTunnelConnResponse{
			ConnId: connID,
		})

		// Start reading from the target in a goroutine and stream data back.
		go tunnelReadLoop(tunnels, connID, tc)
	})

	// SendTunnelData writes data to the target connection.
	d.Register("SendTunnelData", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.SendTunnelDataRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		connID := r.GetConnId()
		if connID == "" {
			sendInvalidArgument(sender, "conn_id is required")
			return
		}

		tc := tunnels.Get(connID)
		if tc == nil {
			sendNotFoundError(sender, "tunnel connection not found: "+connID)
			return
		}

		if tc.closed.Load() {
			sendNotFoundError(sender, "tunnel connection closed: "+connID)
			return
		}

		data := r.GetData()
		if len(data) > 0 {
			if _, err := tc.conn.Write(data); err != nil {
				slog.Error("failed to write to tunnel target", "conn_id", connID, "error", err)
				sendInternalError(sender, fmt.Sprintf("write: %v", err))
				return
			}
		}

		sendProtoResponse(sender, &leapmuxv1.SendTunnelDataResponse{})
	})

	// CloseTunnelConn closes a tunnel connection.
	d.Register("CloseTunnelConn", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.CloseTunnelConnRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		connID := r.GetConnId()
		if connID == "" {
			sendInvalidArgument(sender, "conn_id is required")
			return
		}

		tc := tunnels.Remove(connID)
		if tc == nil {
			sendNotFoundError(sender, "tunnel connection not found: "+connID)
			return
		}

		if tc.closed.CompareAndSwap(false, true) {
			_ = tc.conn.Close()
		}

		slog.Info("tunnel connection closed by client", "conn_id", connID)
		sendProtoResponse(sender, &leapmuxv1.CloseTunnelConnResponse{})
	})
}

// tunnelReadLoop reads data from the target connection and sends TunnelConnEvent
// stream messages back to the caller.
func tunnelReadLoop(mgr *TunnelManager, connID string, tc *tunnelConn) {
	buf := make([]byte, tunnelReadBufSize)
	for {
		n, err := tc.conn.Read(buf)
		if n > 0 {
			sendTunnelEvent(tc.sender, &leapmuxv1.TunnelConnEvent{
				ConnId: connID,
				Data:   append([]byte(nil), buf[:n]...),
			})
		}
		if err != nil {
			if tc.closed.Load() {
				// Connection was closed by the client side; no need to send EOF.
				break
			}
			if err == io.EOF {
				sendTunnelEvent(tc.sender, &leapmuxv1.TunnelConnEvent{
					ConnId: connID,
					Eof:    true,
				})
			} else {
				slog.Error("tunnel target read error", "conn_id", connID, "error", err)
				sendTunnelEvent(tc.sender, &leapmuxv1.TunnelConnEvent{
					ConnId: connID,
					Error:  err.Error(),
				})
			}
			break
		}
	}

	// Clean up: close the connection and remove from manager.
	if tc.closed.CompareAndSwap(false, true) {
		_ = tc.conn.Close()
	}
	mgr.Remove(connID)
	slog.Info("tunnel read loop ended", "conn_id", connID)
}

// sendTunnelEvent sends a TunnelConnEvent as a stream message.
func sendTunnelEvent(sender *channel.Sender, event *leapmuxv1.TunnelConnEvent) {
	payload, err := proto.Marshal(event)
	if err != nil {
		slog.Error("failed to marshal tunnel event", "error", err)
		return
	}
	_ = sender.SendStream(&leapmuxv1.InnerStreamMessage{
		Payload: payload,
	})
}
