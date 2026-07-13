package service

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/coder/websocket"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
)

// ChannelRelayHandler handles multiplexed WebSocket connections for encrypted
// channel relay. Frontend connects after OpenChannel succeeds, and all
// subsequent traffic is opaque ciphertext relayed between Frontend and Worker.
//
// Authentication accepts:
//   - Solo mode: auto-authed as the solo user.
//   - Cookie: Set-Cookie session, set by /auth/login.
//   - Bearer: "Authorization: Bearer lmx_..." for the CLI / agents that
//     hold api_tokens or delegation_tokens.
//
// URL: /ws/channel
type ChannelRelayHandler struct {
	wsAuthenticator
	workerMgr       *workermgr.Manager
	channelMgr      *channelmgr.Manager
	closeDispatcher channelCloseEnqueuer
}

type channelCloseEnqueuer interface {
	enqueueChannelCloses([]channelmgr.ClosedChannel)
}

var errTerminalChannelRelay = errors.New("channel relay cannot continue")

// NewChannelRelayHandler creates a new WebSocket relay handler.
func NewChannelRelayHandler(
	st store.Store,
	wMgr *workermgr.Manager,
	cMgr *channelmgr.Manager,
	authContexts *auth.AuthContextRegistry,
	soloUser *auth.UserInfo,
	secureCookie bool,
) *ChannelRelayHandler {
	return &ChannelRelayHandler{
		wsAuthenticator: wsAuthenticator{
			store:        st,
			authLease:    newWebSocketAuthLease(authContexts),
			soloUser:     soloUser,
			secureCookie: secureCookie,
		},
		workerMgr:       wMgr,
		channelMgr:      cMgr,
		closeDispatcher: newWorkerCloseDispatcher(wMgr),
	}
}

// WithChannelCloseEnqueuer shares the service's bounded worker notification
// dispatcher with relay-disconnect teardown.
func (h *ChannelRelayHandler) WithChannelCloseEnqueuer(enqueuer channelCloseEnqueuer) *ChannelRelayHandler {
	if !isNilDependency(enqueuer) {
		h.closeDispatcher = enqueuer
	}
	return h
}

// WithTokenValidator wires Bearer-auth support into the relay handler.
// Returns the receiver for chaining at construction time.
func (h *ChannelRelayHandler) WithTokenValidator(v *auth.TokenValidator) *ChannelRelayHandler {
	h.tokenValidator = v
	return h
}

// ServeHTTP upgrades the connection to a multiplexed WebSocket and relays
// channel messages for all channels belonging to the authenticated user.
func (h *ChannelRelayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, err := h.authenticate(r)
	if err != nil {
		writeHTTPAuthError(w, "channel relay", err)
		return
	}

	// Upgrade to WebSocket.
	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{"channel-relay"},
	})
	if err != nil {
		slog.Error("channel relay websocket upgrade failed", "user_id", user.ID, "error", err)
		return
	}

	wsConn.SetReadLimit(channelwire.WSReadLimit)

	ctx, cleanupLease, current := h.authLease.bind(r.Context(), user, wsConn)
	if !current {
		return
	}
	defer cleanupLease()
	cancel := cleanupLease

	// Each multiplexed WS gets a unique connection ID so multiple connections
	// for the same user (e.g. browser + test helper) can coexist.
	connID := id.Generate()

	// Register this connection for receiving channel messages.
	h.channelMgr.BindUser(user.ID, connID, func(msg *leapmuxv1.ChannelMessage) error {
		slog.Debug("relaying channel message to frontend",
			"channel_id", msg.GetChannelId(),
			"correlation_id", msg.GetCorrelationId(),
		)
		return channelwire.WriteChannelMessage(ctx, wsConn, msg)
	}, cancel)

	slog.Info("channel relay connected", "user_id", user.ID, "conn_id", connID)
	defer func() {
		slog.Info("channel relay disconnected", "user_id", user.ID, "conn_id", connID)
		closed := h.channelMgr.UnbindUserAndCleanup(user.ID, connID)

		for _, cc := range closed {
			slog.Info("channel closed (relay disconnected)",
				"channel_id", cc.ChannelID,
				"worker_id", cc.WorkerID,
				"user_id", user.ID,
			)
		}
		h.closeDispatcher.enqueueChannelCloses(closed)

		_ = wsConn.Close(websocket.StatusNormalClosure, "")
	}()

	// Read messages from frontend and route to the correct worker.
	for {
		msg, err := channelwire.ReadChannelMessage(ctx, wsConn)
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

		// Verify the channel belongs to this user and, for delegation
		// bearers, was opened by the same scoped bearer. Otherwise a
		// delegated relay connection could attach to an unrestricted
		// same-user channel by guessing its id.
		_, ok, relayErr := h.channelMgr.UseAuthorizedChannel(
			channelID,
			connID,
			func(info channelmgr.ChannelInfo) bool {
				return userCanUseChannel(user, info.AuthInfo, info.UserID)
			},
			func(info channelmgr.ChannelInfo) error {
				return h.relayFrontendMessageToWorker(info, msg)
			},
		)
		if !ok {
			slog.Debug("channel relay: channel not authorized for user",
				"channel_id", channelID, "user_id", user.ID)
			continue
		}
		if errors.Is(relayErr, errTerminalChannelRelay) {
			closed := h.channelMgr.CloseByIDIf(channelID, func(info channelmgr.ChannelInfo) bool {
				return userCanUseChannel(user, info.AuthInfo, info.UserID)
			})
			h.closeDispatcher.enqueueChannelCloses(closed)
		}
	}
}

// relayFrontendMessageToWorker forwards one authorized frontend ciphertext frame
// to the channel's worker: it enforces chunk-reassembly limits, resolves the
// worker connection, and sends. A chunk-protocol violation, an offline worker,
// or a broken worker stream returns errTerminalChannelRelay so the read loop
// tears the channel down; a channel with no worker (already gone) is a no-op.
func (h *ChannelRelayHandler) relayFrontendMessageToWorker(info channelmgr.ChannelInfo, msg *leapmuxv1.ChannelMessage) error {
	channelID := info.ChannelID
	workerID := info.WorkerID
	if workerID == "" {
		slog.Debug("channel relay: channel not found", "channel_id", channelID)
		return nil
	}

	slog.Debug("relaying channel message from frontend",
		"worker_id", workerID,
		"channel_id", channelID,
		"correlation_id", msg.GetCorrelationId(),
		"ciphertext_len", len(msg.GetCiphertext()),
	)

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
		return errTerminalChannelRelay
	}

	conn := h.workerMgr.Get(workerID)
	if conn == nil {
		slog.Warn("channel relay: worker offline",
			"channel_id", channelID, "worker_id", workerID)
		return errTerminalChannelRelay
	}

	if err := conn.Send(&leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_ChannelMessage{
			ChannelMessage: msg,
		},
	}); err != nil {
		slog.Debug("channel relay: failed to relay to worker",
			"channel_id", channelID, "error", err)
		return errTerminalChannelRelay
	}
	return nil
}
