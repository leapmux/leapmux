package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/validate"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
)

// WorkerConnectorService implements the Hub-side service called by Worker
// instances for registration and bidirectional streaming.
type WorkerConnectorService struct {
	queries    *db.Queries
	workerMgr  *workermgr.Manager
	channelMgr *channelmgr.Manager
	pending    *workermgr.PendingRequests
	notifier   *notifier.Notifier
	shutdownCh <-chan struct{}
	soloMode   bool
}

// NewWorkerConnectorService creates a new WorkerConnectorService.
func NewWorkerConnectorService(q *db.Queries, mgr *workermgr.Manager, soloMode bool) *WorkerConnectorService {
	return &WorkerConnectorService{queries: q, workerMgr: mgr, soloMode: soloMode}
}

// SetChannelMgr sets the channel manager for routing encrypted channel traffic.
func (s *WorkerConnectorService) SetChannelMgr(cm *channelmgr.Manager) {
	s.channelMgr = cm
}

// SetPendingRequests sets the pending requests tracker for file operations.
func (s *WorkerConnectorService) SetPendingRequests(pr *workermgr.PendingRequests) {
	s.pending = pr
}

// SetNotifier sets the notifier for processing pending notifications on connect.
func (s *WorkerConnectorService) SetNotifier(n *notifier.Notifier) {
	s.notifier = n
}

// SetShutdownCh sets the channel used to signal hub shutdown.
// When closed, cleanupWorker skips DB operations and broadcasts.
func (s *WorkerConnectorService) SetShutdownCh(ch <-chan struct{}) {
	s.shutdownCh = ch
}

func (s *WorkerConnectorService) RequestRegistration(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RequestRegistrationRequest],
) (*connect.Response[leapmuxv1.RequestRegistrationResponse], error) {
	if s.soloMode {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker registration is not available in solo mode"))
	}
	// Expire old pending registrations first.
	if err := s.queries.ExpireRegistrations(ctx); err != nil {
		slog.Debug("failed to expire registrations", "error", err)
	}

	regID := id.Generate()
	expiresAt := time.Now().Add(10 * time.Minute).UTC()

	version, err := validate.ValidateProperty("version", req.Msg.GetVersion())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	publicKey := req.Msg.GetPublicKey()
	if publicKey == nil {
		publicKey = []byte{}
	}
	mlkemPublicKey := req.Msg.GetMlkemPublicKey()
	if mlkemPublicKey == nil {
		mlkemPublicKey = []byte{}
	}
	slhdsaPublicKey := req.Msg.GetSlhdsaPublicKey()
	if slhdsaPublicKey == nil {
		slhdsaPublicKey = []byte{}
	}

	if err := s.queries.CreateRegistration(ctx, db.CreateRegistrationParams{
		ID:              regID,
		Version:         version,
		PublicKey:       publicKey,
		MlkemPublicKey:  mlkemPublicKey,
		SlhdsaPublicKey: slhdsaPublicKey,
		ExpiresAt:       expiresAt,
	}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create registration: %w", err))
	}

	return connect.NewResponse(&leapmuxv1.RequestRegistrationResponse{
		RegistrationToken: regID,
		RegistrationUrl:   "/register/" + regID,
	}), nil
}

func (s *WorkerConnectorService) PollRegistration(
	ctx context.Context,
	req *connect.Request[leapmuxv1.PollRegistrationRequest],
) (*connect.Response[leapmuxv1.PollRegistrationResponse], error) {
	regID := req.Msg.GetRegistrationToken()
	if regID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("registration_token is required"))
	}

	reg, err := s.queries.GetRegistrationByID(ctx, regID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("registration not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Long-poll: if still pending, wait for notification or timeout.
	if reg.Status == leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING {
		_ = s.workerMgr.WaitForRegistrationChange(ctx, regID, 30*time.Second)

		// Re-query after waking up.
		reg, err = s.queries.GetRegistrationByID(ctx, regID)
		if err != nil {
			if err == sql.ErrNoRows {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("registration not found"))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}

	resp := &leapmuxv1.PollRegistrationResponse{}

	switch reg.Status {
	case leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING:
		resp.Status = leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_PENDING
	case leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED:
		resp.Status = leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED
		if reg.WorkerID.Valid {
			resp.WorkerId = reg.WorkerID.String
			worker, err := s.queries.GetWorkerByIDInternal(ctx, reg.WorkerID.String)
			if err == nil {
				resp.AuthToken = worker.AuthToken
				resp.OrgId = worker.OrgID
			}
		}
	case leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED:
		resp.Status = leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED
	default:
		resp.Status = leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_UNSPECIFIED
	}

	return connect.NewResponse(resp), nil
}

func (s *WorkerConnectorService) Connect(
	ctx context.Context,
	stream *connect.BidiStream[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse],
) error {
	// The worker must authenticate via auth_token in the first message or
	// via metadata. For now, extract from the request header.
	authToken := stream.RequestHeader().Get("Authorization")
	if authToken == "" {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("auth_token required"))
	}

	token := ""
	const prefix = "Bearer "
	if len(authToken) > len(prefix) {
		token = authToken[len(prefix):]
	}

	worker, err := s.queries.GetWorkerByAuthToken(ctx, token)
	if err != nil {
		if err == sql.ErrNoRows {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid auth token"))
		}
		return connect.NewError(connect.CodeInternal, err)
	}

	// Register the connection.
	conn := &workermgr.Conn{
		WorkerID: worker.ID,
		OrgID:    worker.OrgID,
		Stream:   stream,
	}
	replaced := s.workerMgr.Register(conn)
	if replaced {
		// A new worker process replaced an older connection. The old
		// connection's Unregister will return false (it's no longer the
		// current conn), so cleanupWorker won't run from its defer.
		// We must close old channels now so the Frontend detects the
		// disconnect and opens fresh channels to the new worker.
		s.cleanupWorker(worker.ID)
	}
	defer func() {
		// Only run cleanup if this connection is still the registered one.
		// A newer worker process may have already replaced it, in which
		// case we must not unregister the replacement or close its agents.
		if s.workerMgr.Unregister(worker.ID, conn) {
			s.cleanupWorker(worker.ID)
		}
	}()

	// Update last seen.
	if err := s.queries.UpdateWorkerLastSeen(ctx, worker.ID); err != nil {
		slog.Warn("failed to update worker last seen", "worker_id", worker.ID, "error", err)
	}

	slog.Info("worker connected", "worker_id", worker.ID, "status", worker.Status)
	defer slog.Info("worker disconnected", "worker_id", worker.ID)

	// Process pending notifications.
	if s.notifier != nil {
		if worker.Status == leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING {
			// Worker is being deregistered — process notifications inline, then close.
			if err := s.notifier.ProcessPendingNotifications(ctx, worker.ID); err != nil {
				slog.Error("failed to process pending notifications (deregistering)", "worker_id", worker.ID, "error", err)
			}
			return nil
		}
		// Normal worker: process pending notifications in background.
		go func() {
			if err := s.notifier.ProcessPendingNotifications(ctx, worker.ID); err != nil {
				slog.Error("failed to process pending notifications", "worker_id", worker.ID, "error", err)
			}
		}()
	}

	// Main message loop: read messages from worker and process them.
	// Run stream.Receive() in a goroutine so we can also detect idle
	// timeouts (dead workers that didn't close the TCP connection cleanly).
	type receiveResult struct {
		msg *leapmuxv1.ConnectRequest
		err error
	}
	msgCh := make(chan receiveResult, 1)
	go func() {
		for {
			msg, err := stream.Receive()
			select {
			case msgCh <- receiveResult{msg, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	const workerIdleTimeout = 10 * time.Second
	idleTimer := time.NewTimer(workerIdleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case result := <-msgCh:
			if result.err != nil {
				// Connection closed by worker.
				return nil // Connection closed.
			}

			// Reset idle timer on every received message.
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(workerIdleTimeout)

			msg := result.msg
			if err := s.processWorkerMessage(ctx, conn, worker.ID, msg); err != nil {
				return connect.NewError(connect.CodeInvalidArgument, err)
			}

		case <-idleTimer.C:
			// A message may have arrived at the same instant the timer
			// fired. Go's select picks randomly among ready cases, so
			// drain msgCh before deciding to disconnect.
			select {
			case result := <-msgCh:
				if result.err != nil {
					return nil
				}
				idleTimer.Reset(workerIdleTimeout)
				if err := s.processWorkerMessage(ctx, conn, worker.ID, result.msg); err != nil {
					return connect.NewError(connect.CodeInvalidArgument, err)
				}
				continue
			default:
			}
			slog.Warn("worker idle timeout, assuming disconnected", "worker_id", worker.ID)
			return nil

		case <-ctx.Done():
			// Hub shutting down or request canceled.
			return nil
		}
	}
}

// processWorkerMessage handles a single message from the worker stream.
// Returns a non-nil error to terminate the connection (e.g. invalid config).
func (s *WorkerConnectorService) processWorkerMessage(
	ctx context.Context,
	conn *workermgr.Conn,
	workerID string,
	msg *leapmuxv1.ConnectRequest,
) error {
	// Update last seen periodically on heartbeats.
	if hb := msg.GetHeartbeat(); hb != nil {
		if err := s.queries.UpdateWorkerLastSeen(ctx, workerID); err != nil {
			slog.Warn("failed to update worker last seen on heartbeat", "worker_id", workerID, "error", err)
		}
		// Cache encryption mode on the live connection (not persisted to DB).
		encMode := hb.GetEncryptionMode()
		if encMode == leapmuxv1.EncryptionMode_ENCRYPTION_MODE_UNSPECIFIED {
			if conn.EncryptionMode != leapmuxv1.EncryptionMode_ENCRYPTION_MODE_UNSPECIFIED {
				// The worker already declared a mode; sending UNSPECIFIED
				// afterwards is a bug — reject the connection.
				return fmt.Errorf("worker sent unspecified encryption mode after previously declaring %v", conn.EncryptionMode)
			}
			encMode = leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM
		}
		// Reject disabled encryption in non-solo mode.
		if encMode == leapmuxv1.EncryptionMode_ENCRYPTION_MODE_DISABLED && !s.soloMode {
			return fmt.Errorf("disabled encryption mode is only allowed in solo mode")
		}
		conn.EncryptionMode = encMode
		// Persist worker's public keys if provided (sent with the initial heartbeat).
		if pk := hb.GetPublicKey(); len(pk) > 0 {
			mlkemPK := hb.GetMlkemPublicKey()
			if mlkemPK == nil {
				mlkemPK = []byte{}
			}
			slhdsaPK := hb.GetSlhdsaPublicKey()
			if slhdsaPK == nil {
				slhdsaPK = []byte{}
			}
			if err := s.queries.UpdateWorkerPublicKey(ctx, db.UpdateWorkerPublicKeyParams{
				PublicKey:       pk,
				MlkemPublicKey:  mlkemPK,
				SlhdsaPublicKey: slhdsaPK,
				ID:              workerID,
			}); err != nil {
				slog.Warn("failed to update worker public key", "worker_id", workerID, "error", err)
			}
		}
		// Send heartbeat response via conn.Send() to serialize with
		// other writes (e.g. channel relay) on the same bidi stream.
		if err := conn.Send(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_Heartbeat{
				Heartbeat: &leapmuxv1.Heartbeat{},
			},
		}); err != nil {
			slog.Debug("failed to send heartbeat response", "worker_id", workerID, "error", err)
		}
		return nil
	}

	// Try to complete pending request-response pairs (file operations).
	if s.pending != nil && msg.GetRequestId() != "" {
		if s.pending.Complete(msg.GetRequestId(), msg) {
			return nil
		}
	}

	// Handle workspace tab sync from worker.
	if tabSync := msg.GetWorkspaceTabsSync(); tabSync != nil {
		s.handleWorkspaceTabsSync(ctx, workerID, tabSync)
		return nil
	}

	// Route channel messages from worker to frontend.
	if chMsg := msg.GetChannelMessageResp(); chMsg != nil {
		if s.channelMgr != nil {
			// Validate chunked message constraints before forwarding.
			if err := s.channelMgr.ChunkTracker.Track(
				chMsg.GetChannelId(), "w2fe",
				chMsg.GetCorrelationId(),
				len(chMsg.GetCiphertext()),
				chMsg.GetFlags() == leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE,
			); err != nil {
				slog.Warn("channel relay: chunk validation failed",
					"worker_id", workerID,
					"channel_id", chMsg.GetChannelId(),
					"correlation_id", chMsg.GetCorrelationId(),
					"error", err,
				)
				return nil
			}

			slog.Debug("relaying channel message from worker",
				"worker_id", workerID,
				"channel_id", chMsg.GetChannelId(),
				"correlation_id", chMsg.GetCorrelationId(),
			)
			if !s.channelMgr.SendToFrontend(chMsg) {
				slog.Debug("failed to route channel message to frontend",
					"worker_id", workerID,
					"channel_id", chMsg.GetChannelId(),
					"correlation_id", chMsg.GetCorrelationId(),
				)
			}
		}
		return nil
	}

	slog.Debug("unhandled worker message",
		"worker_id", workerID,
		"request_id", msg.GetRequestId(),
	)
	return nil
}

// handleWorkspaceTabsSync reconciles the hub's workspace_tabs for the connecting worker.
// It deletes stale hub tabs whose agents or terminals no longer exist on the worker.
// It does NOT add missing tabs because the hub is the source of truth for tab
// visibility — a tab absent from the hub may have been explicitly closed by the user.
func (s *WorkerConnectorService) handleWorkspaceTabsSync(ctx context.Context, workerID string, sync *leapmuxv1.WorkspaceTabsSync) {
	// Build worker map keyed by tab_type|tab_id → workspace_id.
	type tabKey struct {
		tabType leapmuxv1.TabType
		tabID   string
	}
	workerTabs := make(map[tabKey]string)
	for _, tab := range sync.GetTabs() {
		k := tabKey{tabType: tab.GetTabType(), tabID: tab.GetTabId()}
		workerTabs[k] = tab.GetWorkspaceId()
	}

	// Get current hub tabs for this worker.
	hubTabs, err := s.queries.ListWorkspaceTabsByWorker(ctx, workerID)
	if err != nil {
		slog.Error("failed to list hub tabs for worker during sync", "worker_id", workerID, "error", err)
		return
	}

	// Reconcile hub tabs against worker state.
	deleted := 0
	moved := 0
	for _, ht := range hubTabs {
		k := tabKey{tabType: ht.TabType, tabID: ht.TabID}
		workerWsID, exists := workerTabs[k]
		if !exists {
			// Tab no longer exists on worker — delete from hub.
			if err := s.queries.DeleteWorkspaceTab(ctx, db.DeleteWorkspaceTabParams{
				WorkspaceID: ht.WorkspaceID,
				TabType:     ht.TabType,
				TabID:       ht.TabID,
			}); err != nil {
				slog.Error("failed to delete stale tab during sync",
					"worker_id", workerID,
					"workspace_id", ht.WorkspaceID,
					"tab_id", ht.TabID,
					"error", err,
				)
			} else {
				deleted++
			}
		} else if workerWsID != ht.WorkspaceID {
			// Workspace mismatch — worker is authoritative, update hub.
			// Delete old row and upsert new row with worker's workspace_id,
			// preserving position and tile_id.
			if err := s.queries.DeleteWorkspaceTab(ctx, db.DeleteWorkspaceTabParams{
				WorkspaceID: ht.WorkspaceID,
				TabType:     ht.TabType,
				TabID:       ht.TabID,
			}); err != nil {
				slog.Error("failed to delete mismatched tab during sync",
					"worker_id", workerID,
					"old_workspace_id", ht.WorkspaceID,
					"new_workspace_id", workerWsID,
					"tab_id", ht.TabID,
					"error", err,
				)
				continue
			}
			if err := s.queries.UpsertWorkspaceTab(ctx, db.UpsertWorkspaceTabParams{
				WorkspaceID: workerWsID,
				WorkerID:    ht.WorkerID,
				TabType:     ht.TabType,
				TabID:       ht.TabID,
				Position:    ht.Position,
				TileID:      ht.TileID,
			}); err != nil {
				slog.Error("failed to upsert moved tab during sync",
					"worker_id", workerID,
					"workspace_id", workerWsID,
					"tab_id", ht.TabID,
					"error", err,
				)
			} else {
				moved++
			}
		}
	}

	slog.Info("workspace tab sync completed",
		"worker_id", workerID,
		"worker_tabs", len(sync.GetTabs()),
		"hub_tabs", len(hubTabs),
		"deleted", deleted,
		"moved", moved,
	)
}

// cleanupWorker handles resource cleanup for a disconnected worker.
func (s *WorkerConnectorService) cleanupWorker(workerID string) {
	// During hub shutdown, skip all cleanup operations.
	// The DB is about to be closed and all workers are disconnecting.
	if s.shutdownCh != nil {
		select {
		case <-s.shutdownCh:
			slog.Info("skipping worker cleanup during hub shutdown", "worker_id", workerID)
			return
		default:
		}
	}

	// Close all channels associated with this worker.
	if s.channelMgr != nil {
		removed := s.channelMgr.UnregisterByWorker(workerID)
		if len(removed) > 0 {
			slog.Info("closed channels for disconnected worker",
				"worker_id", workerID,
				"count", len(removed),
			)
		}
	}

	slog.Info("worker disconnected, cleanup complete", "worker_id", workerID)
}
