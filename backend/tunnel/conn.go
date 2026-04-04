package tunnel

import (
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/protobuf/proto"
)

// Conn implements net.Conn over an E2EE tunnel channel connection.
// It bridges the tunnel inner RPC protocol (OpenTunnelConn, SendTunnelData,
// TunnelConnEvent) with Go's standard net.Conn interface, allowing it to be
// used with libraries that expect a net.Conn (e.g., SOCKS5 proxy).
type Conn struct {
	ch     *Channel
	connID string
	reqID  uint32 // correlation ID for stream events

	readBuf  chan []byte // buffered data from TunnelConnEvent
	readPart []byte      // partially consumed buffer from a previous read
	readErr  chan error  // EOF or error from stream

	closeOnce sync.Once
	closed    chan struct{}

	localAddr  net.Addr
	remoteAddr net.Addr
}

// DialTunnel opens a tunnel connection to the target address via the E2EE
// channel and returns a net.Conn that forwards traffic through the tunnel.
func DialTunnel(ch *Channel, targetAddr string, targetPort uint32) (*Conn, error) {
	openPayload, err := proto.Marshal(&leapmuxv1.OpenTunnelConnRequest{
		TargetAddr: targetAddr,
		TargetPort: targetPort,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	reqID, err := ch.SendRPCNoWait("OpenTunnelConn", openPayload, respCh)
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	select {
	case resp := <-respCh:
		ch.UnregisterPending(reqID)
		if resp == nil {
			return nil, fmt.Errorf("channel closed")
		}
		if resp.GetIsError() {
			return nil, fmt.Errorf("rpc error (code %d): %s", resp.GetErrorCode(), resp.GetErrorMessage())
		}
		var openResp leapmuxv1.OpenTunnelConnResponse
		if err := proto.Unmarshal(resp.GetPayload(), &openResp); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}

		tc := &Conn{
			ch:         ch,
			connID:     openResp.GetConnId(),
			reqID:      reqID,
			readBuf:    make(chan []byte, 64),
			readErr:    make(chan error, 1),
			closed:     make(chan struct{}),
			localAddr:  &net.TCPAddr{IP: net.IPv4zero, Port: 0},
			remoteAddr: &net.TCPAddr{IP: net.ParseIP(targetAddr), Port: int(targetPort)},
		}

		// Register for stream events from the Worker.
		ch.RegisterStream(reqID, tc.onStreamMessage)

		return tc, nil

	case <-time.After(30 * time.Second):
		ch.UnregisterPending(reqID)
		return nil, fmt.Errorf("open tunnel conn timeout")

	case <-ch.Context().Done():
		ch.UnregisterPending(reqID)
		return nil, ch.Context().Err()
	}
}

func (tc *Conn) onStreamMessage(msg *leapmuxv1.InnerStreamMessage) {
	if msg.GetIsError() {
		select {
		case tc.readErr <- fmt.Errorf("tunnel stream: %s", msg.GetErrorMessage()):
		default:
		}
		return
	}

	var event leapmuxv1.TunnelConnEvent
	if err := proto.Unmarshal(msg.GetPayload(), &event); err != nil {
		return
	}
	if event.GetEof() {
		select {
		case tc.readErr <- io.EOF:
		default:
		}
		return
	}
	if event.GetError() != "" {
		select {
		case tc.readErr <- fmt.Errorf("tunnel error: %s", event.GetError()):
		default:
		}
		return
	}
	if len(event.GetData()) > 0 {
		select {
		case tc.readBuf <- event.GetData():
		case <-tc.closed:
		}
	}
}

// Read implements net.Conn.
func (tc *Conn) Read(b []byte) (int, error) {
	// Drain any leftover from a previous read.
	if len(tc.readPart) > 0 {
		n := copy(b, tc.readPart)
		tc.readPart = tc.readPart[n:]
		return n, nil
	}

	select {
	case data := <-tc.readBuf:
		n := copy(b, data)
		if n < len(data) {
			tc.readPart = data[n:]
		}
		return n, nil
	case err := <-tc.readErr:
		return 0, err
	case <-tc.closed:
		return 0, net.ErrClosed
	}
}

// Write implements net.Conn.
func (tc *Conn) Write(b []byte) (int, error) {
	select {
	case <-tc.closed:
		return 0, net.ErrClosed
	default:
	}

	payload, err := proto.Marshal(&leapmuxv1.SendTunnelDataRequest{
		ConnId: tc.connID,
		Data:   b,
	})
	if err != nil {
		return 0, fmt.Errorf("marshal: %w", err)
	}
	if _, err := tc.ch.SendRPCNoWait("SendTunnelData", payload); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Close implements net.Conn.
func (tc *Conn) Close() error {
	tc.closeOnce.Do(func() {
		close(tc.closed)
		tc.ch.UnregisterStream(tc.reqID)

		payload, err := proto.Marshal(&leapmuxv1.CloseTunnelConnRequest{ConnId: tc.connID})
		if err == nil {
			_, _ = tc.ch.SendRPCNoWait("CloseTunnelConn", payload)
		}
	})
	return nil
}

// LocalAddr implements net.Conn.
func (tc *Conn) LocalAddr() net.Addr { return tc.localAddr }

// RemoteAddr implements net.Conn.
func (tc *Conn) RemoteAddr() net.Addr { return tc.remoteAddr }

// SetDeadline implements net.Conn (no-op).
func (tc *Conn) SetDeadline(_ time.Time) error { return nil }

// SetReadDeadline implements net.Conn (no-op).
func (tc *Conn) SetReadDeadline(_ time.Time) error { return nil }

// SetWriteDeadline implements net.Conn (no-op).
func (tc *Conn) SetWriteDeadline(_ time.Time) error { return nil }
