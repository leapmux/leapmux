package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/wskeepalive"
)

// WorkerStreamHandler authenticates and serves the worker's WebSocket stream.
type WorkerStreamHandler struct {
	connector *WorkerConnectorService
	keepalive wskeepalive.Pace
}

func NewWorkerStreamHandler(connector *WorkerConnectorService) *WorkerStreamHandler {
	if connector == nil {
		panic("service: WorkerStreamHandler requires a connector")
	}
	return &WorkerStreamHandler{connector: connector, keepalive: wskeepalive.Production()}
}

func (h *WorkerStreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	worker, err := auth.AuthenticateWorkerBearer(r.Context(), h.connector.store, h.connector.hasher, r.Header.Get("Authorization"))
	deleted := errors.Is(err, auth.ErrWorkerDeleted)
	if err != nil && !deleted {
		if auth.IsWorkerCredentialError(err) {
			http.Error(w, err.Error(), http.StatusUnauthorized)
		} else {
			slog.Error("failed to authenticate worker stream", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}
		return
	}
	subject := workerSubject("")
	if worker != nil {
		subject = workerSubject(worker.ID)
	}
	conn, ok := acceptWebSocket(w, r, endpointWorker, channelwire.WSSubprotocolWorkerStream, channelwire.WorkerStreamReadLimit, subject)
	if !ok {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	if deleted {
		// A retained deleted row proves that this Hub removed the Worker.
		// Unknown tokens receive HTTP 401 before the upgrade: a proxy or a
		// restored Hub must not make the Worker destroy its own identity.
		// A permanent close after the upgrade states the Hub's decision.
		slog.Info("refusing a deleted worker's stream", "reason", channelwire.WorkerCloseReasonDeleted)
		closeWorkerStream(conn, refuseWorkerStream(websocket.StatusPolicyViolation, channelwire.WorkerCloseReasonDeleted, err))
		return
	}
	err = h.connector.serveWorkerStream(r.Context(), worker, &wsWorkerStream{conn: conn, ctx: r.Context(), keepalive: h.keepalive, subject: subject})
	closeWorkerStream(conn, err)
}

func closeWorkerStream(conn *websocket.Conn, err error) {
	var refusal *workerStreamRefusal
	if errors.As(err, &refusal) {
		_ = conn.Close(refusal.status, refusal.reason)
	} else {
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
}

type wsWorkerStream struct {
	conn      *websocket.Conn
	ctx       context.Context
	keepalive wskeepalive.Pace
	subject   wsSubject
}

func (s *wsWorkerStream) Receive() (*leapmuxv1.ConnectRequest, error) {
	msg := &leapmuxv1.ConnectRequest{}
	if err := channelwire.ReadProtoFrame(s.ctx, s.conn, msg); err != nil {
		if errors.Is(err, channelwire.ErrMalformedFrame) {
			return nil, refuseWorkerStream(websocket.StatusProtocolError, channelwire.WorkerCloseReasonInvalidMessage, err)
		}
		return nil, err
	}
	return msg, nil
}
func (s *wsWorkerStream) Send(ctx context.Context, msg *leapmuxv1.ConnectResponse) error {
	return channelwire.WriteProtoFrame(ctx, s.conn, msg)
}
func (s *wsWorkerStream) startLiveness(ctx context.Context, cancel func(), onAnswer func()) {
	s.keepalive.StartBesideAReadLoop(ctx, wskeepalive.Probe{Conn: s.conn, Cancel: cancel, OnAnswer: onAnswer, Attrs: s.subject.logAttrs(endpointWorker)})
}
