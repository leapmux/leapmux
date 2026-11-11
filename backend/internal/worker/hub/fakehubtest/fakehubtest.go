// Package fakehubtest serves the production worker WebSocket protocol in tests.
package fakehubtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

type Stream struct {
	conn    *websocket.Conn
	request *http.Request
}

func Start(t testing.TB, handle func(context.Context, *Stream)) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(channelwire.WSRouteWorker, func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{channelwire.WSSubprotocolWorkerStream}})
		if err != nil {
			t.Errorf("accept worker stream: %v", err)
			return
		}
		conn.SetReadLimit(channelwire.WorkerStreamReadLimit)
		stop := context.AfterFunc(t.Context(), func() { _ = conn.CloseNow() })
		defer stop()
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		handle(r.Context(), &Stream{conn: conn, request: r})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func StartRefusingUpgrade(t testing.TB, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status); _, _ = w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (s *Stream) Request() *http.Request { return s.request }
func (s *Stream) Receive(ctx context.Context) (*leapmuxv1.ConnectRequest, error) {
	msg := &leapmuxv1.ConnectRequest{}
	if err := channelwire.ReadProtoFrame(ctx, s.conn, msg); err != nil {
		return nil, err
	}
	return msg, nil
}
func (s *Stream) Send(ctx context.Context, msg *leapmuxv1.ConnectResponse) error {
	return channelwire.WriteProtoFrame(ctx, s.conn, msg)
}
func (s *Stream) Greet(ctx context.Context, owner string) error {
	return s.Send(ctx, &leapmuxv1.ConnectResponse{Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: owner}}})
}
func (s *Stream) Ping(ctx context.Context) error { return s.conn.Ping(ctx) }
func (s *Stream) Refuse(status websocket.StatusCode, reason string) error {
	return s.conn.Close(status, reason)
}
func (s *Stream) ReadUntilClosed(ctx context.Context) error {
	for {
		if _, err := s.Receive(ctx); err != nil {
			return err
		}
	}
}
