package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/validate"
)

// CRDTRegistry is the subset of *crdt.Registry that
// WorkerConnectorService consumes — used by the worker tab-sync path
// to fetch a manager and submit hub-internal tombstones for tabs the
// worker no longer hosts. Modeled as an interface so tests can pass a
// nil-equivalent stub and so the wiring in hub/server.go doesn't
// require constructing the full registry before this service is
// reachable.
type CRDTRegistry interface {
	Get(ctx context.Context, orgID string) (*crdt.Manager, error)
}

type WorkerConnectorService struct {
	store        store.Store
	workerMgr    *workermgr.Manager
	channelMgr   *channelmgr.Manager
	broadcaster  *HubEventBroadcaster
	pending      *workermgr.PendingRequests
	notifier     *notifier.Notifier
	crdtRegistry CRDTRegistry
	shutdownCh   <-chan struct{}
}

// NewWorkerConnectorService creates a new WorkerConnectorService.
// `registry` may be nil in unit tests; production deployments wire in
// the org-CRDT registry so worker tab-sync can drive manager-side
// tombstones for orphaned tabs the worker no longer hosts.
func NewWorkerConnectorService(
	st store.Store,
	mgr *workermgr.Manager,
	cMgr *channelmgr.Manager,
	b *HubEventBroadcaster,
	pr *workermgr.PendingRequests,
	n *notifier.Notifier,
	registry CRDTRegistry,
	shutdownCh <-chan struct{},
) *WorkerConnectorService {
	return &WorkerConnectorService{
		store:        st,
		workerMgr:    mgr,
		channelMgr:   cMgr,
		broadcaster:  b,
		pending:      pr,
		notifier:     n,
		crdtRegistry: registry,
		shutdownCh:   shutdownCh,
	}
}

// Register handles the worker → hub registration RPC.
//
// The session-cookie auth interceptor lets this RPC through (it's in the
// public allowlist) because workers don't have a hub session — they
// authenticate by presenting a registration key as a bearer credential
// in the Authorization header. The hub atomically consumes the key and
// creates the worker row in one transaction.
func (s *WorkerConnectorService) Register(
	ctx context.Context,
	req *connect.Request[leapmuxv1.RegisterRequest],
) (*connect.Response[leapmuxv1.RegisterResponse], error) {
	regKey, ok := auth.BearerToken(req.Header().Get("Authorization"))
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("registration key required"))
	}

	if _, err := validate.ValidateProperty("version", req.Msg.GetVersion()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	publicKey := ptrconv.OrEmpty(req.Msg.GetPublicKey())
	mlkemPublicKey := ptrconv.OrEmpty(req.Msg.GetMlkemPublicKey())
	slhdsaPublicKey := ptrconv.OrEmpty(req.Msg.GetSlhdsaPublicKey())

	workerID := id.Generate()
	authToken := id.Generate()

	var registeredBy string
	err := s.store.RunInTransaction(ctx, func(tx store.Store) error {
		// Atomic consume: returns the row only if expires_at > now and
		// flips it into the soft-deleted state. Any concurrent caller
		// loses the race and sees ErrNotFound.
		row, err := tx.RegistrationKeys().Consume(ctx, regKey)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return connect.NewError(connect.CodeUnauthenticated, errors.New("registration key invalid or already consumed"))
			}
			return connect.NewError(connect.CodeInternal, fmt.Errorf("consume registration key: %w", err))
		}

		registeredBy = row.CreatedBy
		// The key's creator is the worker's registrant; a blank one would make
		// the worker owned by nobody and unreachable by its real owner.
		registrantUID, mintOK := userid.New(registeredBy)
		if !mintOK {
			return connect.NewError(connect.CodeInternal, errors.New("registration key has a blank creator"))
		}
		if err := tx.Workers().Create(ctx, store.CreateWorkerParams{
			ID:              workerID,
			AuthToken:       authToken,
			RegisteredBy:    registrantUID,
			PublicKey:       publicKey,
			MlkemPublicKey:  mlkemPublicKey,
			SlhdsaPublicKey: slhdsaPublicKey,
		}); err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("create worker: %w", err))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	slog.Info("worker registered",
		"worker_id", workerID,
		"registered_by", registeredBy,
	)
	s.broadcaster.NotifyWorkersChanged(registeredBy)

	// registered_by is deliberately NOT returned here. The worker learns its owner
	// from WorkerIdentity on every Connect instead: handing it over once at
	// registration made the worker's local copy a second source of truth, and a state
	// file that predated the field, or was hand-edited or truncated, left the worker
	// running with no owner and every machine-scoped family dead for its own user.
	return connect.NewResponse(&leapmuxv1.RegisterResponse{
		WorkerId:  workerID,
		AuthToken: authToken,
	}), nil
}

func (s *WorkerConnectorService) Connect(
	ctx context.Context,
	stream *connect.BidiStream[leapmuxv1.ConnectRequest, leapmuxv1.ConnectResponse],
) error {
	token, ok := auth.BearerToken(stream.RequestHeader().Get("Authorization"))
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("auth_token required"))
	}

	worker, err := s.store.Workers().GetByAuthToken(ctx, token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid auth token"))
		}
		return connect.NewError(connect.CodeInternal, err)
	}

	// Register the connection. Replacement cancels this derived context to
	// terminate the superseded handler without affecting the request context of
	// the newly connected worker.
	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()
	conn := &workermgr.Conn{
		WorkerID: worker.ID,
		Stream:   stream,
		Cancel:   cancelConn,
		// Greet the worker with its own identity. Register sends this before it
		// publishes the conn, so it lands before any ChannelOpen this connection could
		// carry -- which the worker needs, because requireWorkerOwner gates every
		// machine-scoped family on the owner and a session can exist the moment the
		// conn is reachable. Handing it to Register rather than sending it here is what
		// makes that ordering impossible to get wrong.
		//
		// worker.RegisteredBy is already in hand from the GetByAuthToken above, so this
		// costs no query.
		Greeting: &leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
				WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: worker.RegisteredBy},
			},
		},
	}
	replaced, err := s.workerMgr.Register(conn)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("greet worker: %w", err))
	}
	if replaced {
		// A new worker process replaced an older connection. The old
		// connection's Unregister will return false (it's no longer the
		// current conn), so cleanupWorker won't run from its defer.
		// We must close old channels now so the Frontend detects the
		// disconnect and opens fresh channels to the new worker.
		s.cleanupWorker(worker.ID)
	}
	ctx = connCtx
	defer func() {
		// Only run cleanup if this connection is still the registered one.
		// A newer worker process may have already replaced it, in which
		// case we must not unregister the replacement or close its agents.
		if s.workerMgr.Unregister(worker.ID, conn) {
			s.cleanupWorker(worker.ID)
		}
	}()

	// Update last seen.
	if err := s.store.Workers().UpdateLastSeen(ctx, worker.ID); err != nil {
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

	// resetIdle stops + drains + re-arms the idle timer. Folded into a
	// helper so every successful receive (both branches) reuses one
	// implementation instead of repeating the drain dance.
	resetIdle := func() {
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(workerIdleTimeout)
	}

	handle := func(result receiveResult) error {
		if result.err != nil {
			return errWorkerStreamClosed
		}
		resetIdle()
		if err := s.processWorkerMessage(ctx, conn, worker.ID, result.msg); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil
	}

	for {
		select {
		case result := <-msgCh:
			if err := handle(result); err != nil {
				if errors.Is(err, errWorkerStreamClosed) {
					return nil
				}
				return err
			}

		case <-idleTimer.C:
			// A message may have arrived at the same instant the timer
			// fired. Go's select picks randomly among ready cases, so
			// drain msgCh before deciding to disconnect.
			select {
			case result := <-msgCh:
				if err := handle(result); err != nil {
					if errors.Is(err, errWorkerStreamClosed) {
						return nil
					}
					return err
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

// errWorkerStreamClosed is the sentinel `handle` returns on receive
// error — distinguishes a clean worker disconnect (return nil) from a
// process-level abort.
var errWorkerStreamClosed = errors.New("worker stream closed")

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
		if err := s.store.Workers().UpdateLastSeen(ctx, workerID); err != nil && ctx.Err() == nil {
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
			if err := s.store.Workers().UpdatePublicKey(ctx, store.UpdateWorkerPublicKeyParams{
				ID:              workerID,
				PublicKey:       pk,
				MlkemPublicKey:  mlkemPK,
				SlhdsaPublicKey: slhdsaPK,
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

	// Handle workspace tab sync from worker. The response is sent
	// back on the same bidi stream with the matching request_id so
	// the worker can correlate it with its outbound message.
	if tabSync := msg.GetWorkspaceTabsSync(); tabSync != nil {
		s.handleWorkspaceTabsSync(ctx, conn, workerID, msg.GetRequestId(), tabSync)
		return nil
	}

	// Route channel messages from worker to frontend.
	if chMsg := msg.GetChannelMessageResp(); chMsg != nil {
		if s.channelMgr != nil {
			matched, connID, err := s.channelMgr.RelayWorkerMessage(chMsg, workerID)
			if !matched {
				slog.Warn("channel relay: worker sent message for an unowned channel",
					"worker_id", workerID,
					"channel_id", chMsg.GetChannelId(),
				)
				return nil
			}
			if err != nil {
				slog.Warn("channel relay: terminal worker-to-frontend failure",
					"worker_id", workerID,
					"channel_id", chMsg.GetChannelId(),
					"correlation_id", chMsg.GetCorrelationId(),
					"error", err,
				)
				s.closeWorkerChannel(conn, workerID, connID, chMsg.GetChannelId())
				return nil
			}

			slog.Debug("relaying channel message from worker",
				"worker_id", workerID,
				"channel_id", chMsg.GetChannelId(),
				"correlation_id", chMsg.GetCorrelationId(),
			)
		}
		return nil
	}

	slog.Debug("unhandled worker message",
		"worker_id", workerID,
		"request_id", msg.GetRequestId(),
	)
	return nil
}

// closeWorkerChannel tears down a channel whose frontend delivery failed.
//
// connID is the connection the failed relay was addressed to, and the
// predicate checks it as well as the worker. The frontend relay flips its
// writer closed a few instructions before UnbindUserAndCleanup runs, so a
// tab that reconnects inside that window can rebind this channel to a
// fresh connection before this close lands -- and matching on worker
// identity alone would tear down that new binding, which is exactly what
// UnbindUserAndCleanup's own ConnID predicate exists to avoid.
//
// An empty connID means the relay never resolved a binding, so there is
// nothing newer to protect and the worker check stands alone.
func (s *WorkerConnectorService) closeWorkerChannel(conn *workermgr.Conn, workerID, connID, channelID string) {
	closed := s.channelMgr.CloseByIDIf(channelID, func(info channelmgr.ChannelInfo) bool {
		if info.WorkerID != workerID {
			return false
		}
		return connID == "" || info.ConnID == connID
	})
	if len(closed) == 0 {
		return
	}
	if err := conn.Send(newChannelCloseResponse(channelID)); err != nil {
		slog.Debug("failed to close terminal worker channel",
			"worker_id", workerID, "channel_id", channelID, "error", err)
	}
}

// handleWorkspaceTabsSync compares the worker's reported tab state
// against the CRDT-derived workspace_tab_owned view, computes the
// authoritative classification, and:
//
//   - Emits a WorkspaceTabsSyncResponse on the bidi stream listing
//     the worker tabs the CRDT no longer knows about (orphans) and
//     the tabs the CRDT moved to a different workspace
//     (reassignments). The worker uses this to drop local agent /
//     terminal / file-tab entities or update their workspace_id.
//   - For tabs the CRDT knows about that the worker doesn't,
//     submits a TombstoneTab op via SubmitInternal so the CRDT side
//     converges to the worker's authoritative view (the worker is
//     the source of truth for live agent / terminal liveness).
//
// `requestID` is the ConnectRequest envelope id; the response carries
// the same id so the worker correlates it.
func (s *WorkerConnectorService) handleWorkspaceTabsSync(
	ctx context.Context,
	conn *workermgr.Conn,
	workerID, requestID string,
	sync *leapmuxv1.WorkspaceTabsSync,
) {
	hubTabs, err := s.store.WorkspaceTabIndex().ListOwnedByWorker(ctx, workerID)
	if err != nil {
		slog.Error("failed to list hub-owned tabs for worker during sync", "worker_id", workerID, "error", err)
		return
	}
	type tabKey struct {
		tabType leapmuxv1.TabType
		tabID   string
	}
	// Build a single index keyed by (tab_type, tab_id). Matched entries
	// are removed during the worker-side scan so the post-scan
	// leftovers ARE the stale-tombstone set — no third pass over the
	// worker keys needed.
	hubByKey := make(map[tabKey]store.WorkspaceTabRow, len(hubTabs))
	for _, ht := range hubTabs {
		hubByKey[tabKey{tabType: ht.TabType, tabID: ht.TabID}] = ht
	}

	resp := &leapmuxv1.WorkspaceTabsSyncResponse{}
	for _, t := range sync.GetTabs() {
		k := tabKey{tabType: t.GetTabType(), tabID: t.GetTabId()}
		ht, ok := hubByKey[k]
		if !ok {
			// Worker hosts a tab the CRDT doesn't know about. Tell
			// the worker to drop it so its local agents/terminals
			// stop running.
			resp.OrphanTabIds = append(resp.OrphanTabIds, &leapmuxv1.TabIdent{
				TabType: t.GetTabType(),
				TabId:   t.GetTabId(),
			})
			continue
		}
		// Matched — fold the worker-side info in and drop from the
		// stale candidate set.
		delete(hubByKey, k)
		if ht.WorkspaceID != t.GetWorkspaceId() {
			// The CRDT moved the tab to a different workspace
			// while the worker was disconnected. Tell the worker
			// to update its local bookkeeping.
			resp.Reassignments = append(resp.Reassignments, &leapmuxv1.TabReassignment{
				Tab: &leapmuxv1.TabIdent{
					TabType: t.GetTabType(),
					TabId:   t.GetTabId(),
				},
				NewWorkspaceId: ht.WorkspaceID,
			})
		}
	}

	// Whatever survived hubByKey above is a CRDT row the worker doesn't
	// host anymore — tombstone via the manager so subscribers observe a
	// consistent state.
	if s.crdtRegistry != nil && len(hubByKey) > 0 {
		var staleTombstones []*leapmuxv1.OrgOp
		var staleOrgID string
		for _, ht := range hubByKey {
			if staleOrgID == "" {
				staleOrgID = ht.OrgID
			}
			staleTombstones = append(staleTombstones, &leapmuxv1.OrgOp{
				OpId: id.Generate(),
				Body: &leapmuxv1.OrgOp_TombstoneTab{
					TombstoneTab: &leapmuxv1.TombstoneTabOp{
						TabType: ht.TabType,
						TabId:   ht.TabID,
					},
				},
			})
		}
		if len(staleTombstones) > 0 {
			mgr, err := s.crdtRegistry.Get(ctx, staleOrgID)
			if err != nil {
				slog.Warn("workspace tab sync: get manager failed",
					"worker_id", workerID, "org_id", staleOrgID, "error", err)
			} else {
				batch := &leapmuxv1.OpBatch{
					BatchId: "worker-sync-" + workerID + "-" + id.Generate(),
					Ops:     staleTombstones,
				}
				if _, err := mgr.SubmitInternal(ctx, crdt.SubmitInput{
					OrgID:       staleOrgID,
					Batches:     []*leapmuxv1.OpBatch{batch},
					PrincipalID: crdt.HubReservedPrincipal,
				}); err != nil {
					slog.Warn("workspace tab sync: submit tombstones failed",
						"worker_id", workerID, "error", err)
				}
			}
		}
	}

	// Always send a response even when both lists are empty so the
	// worker can rely on the round-trip to mark its initial sync
	// complete.
	if err := conn.Send(&leapmuxv1.ConnectResponse{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectResponse_WorkspaceTabsSyncResp{
			WorkspaceTabsSyncResp: resp,
		},
	}); err != nil {
		slog.Debug("failed to send workspace tabs sync response",
			"worker_id", workerID, "error", err)
	}

	slog.Info("workspace tab sync handled",
		"worker_id", workerID,
		"worker_tabs", len(sync.GetTabs()),
		"hub_tabs", len(hubTabs),
		"orphans", len(resp.OrphanTabIds),
		"reassignments", len(resp.Reassignments),
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
