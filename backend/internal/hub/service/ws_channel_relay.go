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
	"github.com/leapmux/leapmux/internal/util/nilcheck"
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
	if !nilcheck.IsNilDependency(enqueuer) {
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

	// All writes to this websocket go through one goroutine, so the hub's
	// per-worker read loop never blocks on this client's socket. See
	// relayWriter for why that matters and why a lagging client is
	// dropped rather than having its frames dropped.
	//
	// No deferred close here: run owns that (`defer w.close()`), and a
	// defer registered at this point would run AFTER the disconnect block
	// below has already closed the socket -- discarding the queue strictly
	// after the close frame went out, which reads as a decision but would
	// be an accident of LIFO ordering.
	writer := newRelayWriter(ctx, wsConn, cancel, user.ID.String(), connID)
	go writer.run()

	// Register this connection for receiving channel messages.
	h.channelMgr.BindUser(user.ID.String(), connID, func(msg *leapmuxv1.ChannelMessage) error {
		slog.Debug("relaying channel message to frontend",
			"channel_id", msg.GetChannelId(),
			"correlation_id", msg.GetCorrelationId(),
		)
		return writer.enqueue(msg)
	}, cancel)

	slog.Info("channel relay connected", "user_id", user.ID, "conn_id", connID)
	defer func() {
		slog.Info("channel relay disconnected", "user_id", user.ID, "conn_id", connID)
		// Stop accepting and discard the backlog BEFORE the socket goes, so
		// no NEW frame starts once teardown has begun (pop refuses after
		// close). It does not join the drain goroutine, so a write already
		// in flight can still be running when wsConn.Close lands below --
		// which is safe rather than merely tolerated: coder/websocket
		// documents every method except Reader/Read as callable
		// concurrently, and Close unblocks goroutines inside the conn.
		// Joining instead would make this defer wait out a wedged client's
		// full write timeout before unbinding, delaying the revocation
		// promptness UnbindUserAndCleanup exists to provide.
		writer.close()
		closed := h.channelMgr.UnbindUserAndCleanup(user.ID.String(), connID)

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

	// An out-of-spec flags value is a protocol violation treated like any
	// other chunk-tracking failure, not misread as a final chunk (see
	// channelwire.ChunkContinuation).
	more, validFlags := channelwire.ChunkContinuation(msg.GetFlags())
	if !validFlags {
		slog.Warn("channel relay: out-of-spec channel message flags",
			"channel_id", channelID,
			"correlation_id", msg.GetCorrelationId(),
			"flags", msg.GetFlags(),
		)
		return errTerminalChannelRelay
	}
	if err := h.channelMgr.ChunkTracker.Track(
		channelID, "fe2w",
		msg.GetCorrelationId(),
		len(msg.GetCiphertext()),
		more,
	); err != nil {
		slog.Warn("channel relay: chunk validation failed",
			"channel_id", channelID,
			"correlation_id", msg.GetCorrelationId(),
			"error", err,
		)
		return errTerminalChannelRelay
	}

	conn := h.workerMgr.ConnForTrustedPath(workerID)
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
