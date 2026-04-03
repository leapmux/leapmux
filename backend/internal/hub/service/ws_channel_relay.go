package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/channelproto"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
)

// ChannelRelayHandler handles multiplexed WebSocket connections for encrypted
// channel relay. Frontend connects after OpenChannel succeeds, and all
// subsequent traffic is opaque ciphertext relayed between Frontend and Worker.
//
// URL: /ws/channel?token={session_token}
type ChannelRelayHandler struct {
	queries    *db.Queries
	workerMgr  *workermgr.Manager
	channelMgr *channelmgr.Manager
	soloUser   *auth.UserInfo
}

// NewChannelRelayHandler creates a new WebSocket relay handler.
func NewChannelRelayHandler(
	q *db.Queries,
	wMgr *workermgr.Manager,
	cMgr *channelmgr.Manager,
	soloUser *auth.UserInfo,
) *ChannelRelayHandler {
	return &ChannelRelayHandler{
		queries:    q,
		workerMgr:  wMgr,
		channelMgr: cMgr,
		soloUser:   soloUser,
	}
}

// ServeHTTP upgrades the connection to a multiplexed WebSocket and relays
// channel messages for all channels belonging to the authenticated user.
func (h *ChannelRelayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var user *auth.UserInfo

	if h.soloUser != nil {
		// Solo mode: auto-authenticate as the solo user.
		user = h.soloUser
	} else {
		// Extract auth token from subprotocol header or query param (legacy).
		token := extractTokenFromSubprotocols(r.Header.Get("Sec-WebSocket-Protocol"))
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}

		var err error
		user, err = auth.ValidateToken(r.Context(), h.queries, token)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Upgrade to WebSocket.
	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{"channel-relay"},
	})
	if err != nil {
		slog.Error("channel relay websocket upgrade failed", "user_id", user.ID, "error", err)
		return
	}

	wsConn.SetReadLimit(channelproto.WSReadLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Each multiplexed WS gets a unique connection ID so multiple connections
	// for the same user (e.g. browser + test helper) can coexist.
	connID := id.Generate()

	// Register this connection for receiving channel messages.
	h.channelMgr.BindUser(user.ID, connID, func(msg *leapmuxv1.ChannelMessage) error {
		slog.Debug("relaying channel message to frontend",
			"channel_id", msg.GetChannelId(),
			"correlation_id", msg.GetCorrelationId(),
		)
		return channelproto.WriteChannelMessage(ctx, wsConn, msg)
	}, cancel)

	slog.Info("channel relay connected", "user_id", user.ID, "conn_id", connID)
	defer func() {
		slog.Info("channel relay disconnected", "user_id", user.ID, "conn_id", connID)
		noConns := h.channelMgr.UnbindUser(user.ID, connID)

		// Clean up channels bound to this relay connection and notify workers.
		closed := h.channelMgr.UnregisterByConn(connID)

		// If this was the user's last relay connection, also clean up channels
		// that were opened via RPC but never bound to any relay connection.
		if noConns {
			closed = append(closed, h.channelMgr.UnregisterUnboundByUser(user.ID)...)
		}

		for _, cc := range closed {
			slog.Info("channel closed (relay disconnected)",
				"channel_id", cc.ChannelID,
				"worker_id", cc.WorkerID,
				"user_id", user.ID,
			)
			if conn := h.workerMgr.Get(cc.WorkerID); conn != nil {
				_ = conn.Send(&leapmuxv1.ConnectResponse{
					Payload: &leapmuxv1.ConnectResponse_ChannelClose{
						ChannelClose: &leapmuxv1.ChannelCloseNotification{
							ChannelId: cc.ChannelID,
						},
					},
				})
			}
		}

		_ = wsConn.Close(websocket.StatusNormalClosure, "")
	}()

	// Read messages from frontend and route to the correct worker.
	for {
		msg, err := channelproto.ReadChannelMessage(ctx, wsConn)
		if err != nil {
			if ctx.Err() != nil {
				return // Context cancelled.
			}
			slog.Debug("channel relay read error", "user_id", user.ID, "error", err)
			return
		}

		channelID := msg.GetChannelId()
		if channelID == "" {
			slog.Debug("channel relay: message missing channel_id", "user_id", user.ID)
			continue
		}

		// Verify the channel belongs to this user.
		if ownerID := h.channelMgr.GetUserID(channelID); ownerID != user.ID {
			slog.Debug("channel relay: channel not owned by user",
				"channel_id", channelID, "user_id", user.ID)
			continue
		}

		// Associate this channel with our connection so responses are routed
		// back to this specific WebSocket (not broadcast to all connections).
		h.channelMgr.SetChannelConn(channelID, connID)

		workerID := h.channelMgr.GetWorkerID(channelID)
		if workerID == "" {
			slog.Debug("channel relay: channel not found", "channel_id", channelID)
			continue
		}

		slog.Debug("relaying channel message from frontend",
			"worker_id", workerID,
			"channel_id", channelID,
			"correlation_id", msg.GetCorrelationId(),
		)

		// Validate chunked message constraints before forwarding.
		if err := h.channelMgr.ChunkTracker.Track(
			channelID, "fe2w",
			msg.GetCorrelationId(),
			len(msg.GetCiphertext()),
			msg.GetFlags() == leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
		); err != nil {
			slog.Warn("channel relay: chunk validation failed",
				"channel_id", channelID,
				"correlation_id", msg.GetCorrelationId(),
				"error", err,
			)
			continue
		}

		// Forward to worker via bidi stream.
		conn := h.workerMgr.Get(workerID)
		if conn == nil {
			slog.Warn("channel relay: worker offline",
				"channel_id", channelID, "worker_id", workerID)
			// Don't close the entire WS — other channels may be healthy.
			continue
		}

		if err := conn.Send(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_ChannelMessage{
				ChannelMessage: msg,
			},
		}); err != nil {
			slog.Debug("channel relay: failed to relay to worker",
				"channel_id", channelID, "error", err)
			// Don't close the entire WS — other channels may be healthy.
			continue
		}
	}
}

// authTokenSubprotocolPrefix is the prefix for auth tokens passed via
// the Sec-WebSocket-Protocol header (e.g. "auth.token.<token>").
const authTokenSubprotocolPrefix = "auth.token."

// extractTokenFromSubprotocols parses a comma-separated Sec-WebSocket-Protocol
// header value and returns the token from an "auth.token.<token>" entry.
func extractTokenFromSubprotocols(header string) string {
	for _, proto := range strings.Split(header, ",") {
		proto = strings.TrimSpace(proto)
		if strings.HasPrefix(proto, authTokenSubprotocolPrefix) {
			return strings.TrimPrefix(proto, authTokenSubprotocolPrefix)
		}
	}
	return ""
}
