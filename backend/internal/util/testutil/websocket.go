package testutil

import (
	"crypto/sha1"
	"encoding/base64"
	"net"
	"net/http"
	"sync"
	"time"
)

// DelayedWebSocketTransport supplies a valid HTTP upgrade without network I/O.
// Each write holds the real WebSocket frame mutex for Delay, until Close ends it.
// Use inside testing/synctest to exercise control deadlines deterministically.
type DelayedWebSocketTransport struct {
	Delay        time.Duration
	WriteStarted chan struct{}
	Incoming     chan []byte
	closed       chan struct{}
	pending      []byte
	closeOnce    sync.Once
	startOnce    sync.Once
}

func NewDelayedWebSocketTransport(delay time.Duration) *DelayedWebSocketTransport {
	return &DelayedWebSocketTransport{Delay: delay, WriteStarted: make(chan struct{}), Incoming: make(chan []byte), closed: make(chan struct{})}
}

func (b *DelayedWebSocketTransport) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(b.pending) == 0 {
		select {
		case b.pending = <-b.Incoming:
		case <-b.closed:
			return 0, net.ErrClosed
		}
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}
func (b *DelayedWebSocketTransport) Write(p []byte) (int, error) {
	b.startOnce.Do(func() { close(b.WriteStarted) })
	timer := time.NewTimer(b.Delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return len(p), nil
	case <-b.closed:
		return 0, net.ErrClosed
	}
}
func (b *DelayedWebSocketTransport) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func (b *DelayedWebSocketTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	headers := make(http.Header)
	headers.Set("Upgrade", "websocket")
	headers.Set("Connection", "Upgrade")
	// The WebSocket protocol requires SHA-1 for this handshake digest.
	digest := sha1.Sum([]byte(req.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	headers.Set("Sec-WebSocket-Accept", base64.StdEncoding.EncodeToString(digest[:]))
	if protocol := req.Header.Get("Sec-WebSocket-Protocol"); protocol != "" {
		headers.Set("Sec-WebSocket-Protocol", protocol)
	}
	return &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: headers, Body: b}, nil
}
