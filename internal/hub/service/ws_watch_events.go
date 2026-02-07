package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/metrics"
)

// WebSocket close codes for WatchEvents.
const (
	wsCloseUnauthorized     = 4001
	wsCloseInvalidRequest   = 4002
	wsClosePermissionDenied = 4003
)

// WSWatchEventsHandler returns an http.Handler that serves WatchEvents over WebSocket.
//
// Protocol:
//  1. Client opens WebSocket with subprotocol "leapmux.watch-events.v1".
//  2. Client sends auth token as a text frame.
//  3. Client sends WatchEventsRequest as a protobuf-encoded binary frame.
//  4. Server streams WatchEventsResponse as protobuf-encoded binary frames.
//  5. Server closes with 1000 on normal completion.
func WSWatchEventsHandler(queries *db.Queries, workspaceSvc *WorkspaceService, shutdownCh <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reject new WebSocket connections during shutdown.
		if shutdownCh != nil {
			select {
			case <-shutdownCh:
				http.Error(w, "hub is shutting down", http.StatusServiceUnavailable)
				return
			default:
			}
		}

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"leapmux.watch-events.v1"},
		})
		if err != nil {
			slog.Debug("ws/watch-events: accept failed", "error", err)
			return
		}
		defer func() { _ = conn.CloseNow() }()

		metrics.WSConnectionsActive.Inc()
		defer metrics.WSConnectionsActive.Dec()

		ctx := r.Context()

		// Handshake timeout: the client must send the auth token and request
		// within 10 seconds, otherwise the connection is closed.
		handshakeCtx, handshakeCancel := context.WithTimeout(ctx, 10*time.Second)
		defer handshakeCancel()

		// 1. Read auth token (text frame).
		typ, data, err := conn.Read(handshakeCtx)
		if err != nil {
			slog.Debug("ws/watch-events: read auth token failed", "error", err)
			return
		}
		if typ != websocket.MessageText {
			_ = conn.Close(websocket.StatusCode(wsCloseInvalidRequest), "expected text frame for auth token")
			return
		}

		token := string(data)
		user, err := auth.ValidateToken(handshakeCtx, queries, token)
		if err != nil {
			_ = conn.Close(websocket.StatusCode(wsCloseUnauthorized), "unauthorized")
			return
		}

		// 2. Read WatchEventsRequest (binary frame).
		typ, data, err = conn.Read(handshakeCtx)
		if err != nil {
			slog.Debug("ws/watch-events: read request failed", "error", err)
			return
		}
		if typ != websocket.MessageBinary {
			_ = conn.Close(websocket.StatusCode(wsCloseInvalidRequest), "expected binary frame for request")
			return
		}

		var req leapmuxv1.WatchEventsRequest
		if err := proto.Unmarshal(data, &req); err != nil {
			_ = conn.Close(websocket.StatusCode(wsCloseInvalidRequest), "invalid request")
			return
		}

		handshakeCancel()

		// 3. Stream events using the shared core logic.
		sendErr := workspaceSvc.watchEventsCore(ctx, user, &req, func(resp *leapmuxv1.WatchEventsResponse) error {
			data, err := proto.Marshal(resp)
			if err != nil {
				return fmt.Errorf("marshal response: %w", err)
			}
			if err := conn.Write(ctx, websocket.MessageBinary, data); err != nil {
				return err
			}
			metrics.WSMessagesTotal.Inc()
			return nil
		})

		if sendErr != nil {
			// Map connect errors to WebSocket close codes.
			var connectErr *connect.Error
			if errors.As(sendErr, &connectErr) {
				switch connectErr.Code() {
				case connect.CodeNotFound:
					_ = conn.Close(websocket.StatusCode(wsClosePermissionDenied), "workspace not found")
				case connect.CodePermissionDenied:
					_ = conn.Close(websocket.StatusCode(wsClosePermissionDenied), "permission denied")
				default:
					slog.Debug("ws/watch-events: stream ended with error", "error", sendErr)
					_ = conn.Close(websocket.StatusInternalError, "internal error")
				}
				return
			}
			slog.Debug("ws/watch-events: stream ended", "error", sendErr)
		}

		_ = conn.Close(websocket.StatusNormalClosure, "")
	})
}
