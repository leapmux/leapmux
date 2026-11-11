package hub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

const workerStreamHandshakeTimeout = 10 * time.Second

// WorkerStream carries the worker's framed protobuf messages over a WebSocket.
type WorkerStream struct {
	conn  *websocket.Conn
	pings chan struct{}
}

// UpgradeRefusedError reports an HTTP response that refused the upgrade.
// The response body is limited by coder/websocket to its first 1,024 bytes.
type UpgradeRefusedError struct {
	StatusCode int
	Body       string
	Err        error
}

func (e *UpgradeRefusedError) Error() string {
	result := fmt.Sprintf("the hub refused the worker stream: HTTP %d", e.StatusCode)
	if statusText := http.StatusText(e.StatusCode); statusText != "" {
		result += " " + statusText
	}
	firstLine, _, _ := strings.Cut(e.Body, "\n")
	if firstLine = strings.TrimSpace(firstLine); firstLine != "" {
		result += ": " + firstLine
	}
	return result
}

func (e *UpgradeRefusedError) Unwrap() error { return e.Err }

// DialWorkerStream opens the worker stream through the endpoint's WebSocket client.
func DialWorkerStream(ctx context.Context, httpClient *http.Client, baseURL, authToken string) (*WorkerStream, error) {
	return dialWorkerStream(ctx, httpClient, baseURL, authToken, workerStreamHandshakeTimeout)
}

func dialWorkerStream(ctx context.Context, httpClient *http.Client, baseURL, authToken string, timeout time.Duration) (*WorkerStream, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pings := make(chan struct{}, 1)
	headers := make(http.Header)
	if authToken != "" {
		headers.Set("Authorization", "Bearer "+authToken)
	}
	conn, resp, err := websocket.Dial(ctx, channelwire.WorkerStreamURL(baseURL), &websocket.DialOptions{
		HTTPClient:   httpClient,
		HTTPHeader:   headers,
		Subprotocols: []string{channelwire.WSSubprotocolWorkerStream},
		OnPingReceived: func(context.Context, []byte) bool {
			select {
			case pings <- struct{}{}:
			default:
			}
			return true
		},
	})
	if err != nil {
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			var body []byte
			if resp.Body != nil {
				body, _ = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
			return nil, &UpgradeRefusedError{StatusCode: resp.StatusCode, Body: string(body), Err: err}
		}
		return nil, fmt.Errorf("dial the hub's worker stream: %w", err)
	}
	conn.SetReadLimit(channelwire.WorkerStreamReadLimit)
	return &WorkerStream{conn: conn, pings: pings}, nil
}

func (s *WorkerStream) Send(ctx context.Context, msg *leapmuxv1.ConnectRequest) error {
	return channelwire.WriteProtoFrame(ctx, s.conn, msg)
}
func (s *WorkerStream) Receive(ctx context.Context) (*leapmuxv1.ConnectResponse, error) {
	msg := &leapmuxv1.ConnectResponse{}
	if err := channelwire.ReadProtoFrame(ctx, s.conn, msg); err != nil {
		return nil, err
	}
	return msg, nil
}
func (s *WorkerStream) Close() error                  { return s.conn.Close(websocket.StatusNormalClosure, "") }
func (s *WorkerStream) CloseNow() error               { return s.conn.CloseNow() }
func (s *WorkerStream) PingReceived() <-chan struct{} { return s.pings }
