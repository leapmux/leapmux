package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/tabcreation"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/config"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

var errCreationOwnerMissing = errors.New("tab creation requires a user identity")

const (
	creationPending         = int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_PENDING)
	creationCommitted       = int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_COMMITTED)
	creationRejected        = int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_REJECTED)
	creationCancelRequested = int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_CANCEL_REQUESTED)
	creationCanceled        = int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_CANCELED)
	creationPublished       = int64(leapmuxv1.TabCreationState_TAB_CREATION_STATE_PUBLISHED)
)

type creationKey struct{ owner, tabID string }
type creationLock struct {
	mutex sync.Mutex
	users int
}
type creationFlight struct {
	hash     []byte
	done     chan struct{}
	response *leapmuxv1.InnerRpcResponse
	code     int32
	message  string
}

// tabCreationCoordinator owns admitted creation until a definite result or shutdown.
// Its key locks protect persistence and startup handoff, never Hub requests.
type tabCreationCoordinator struct {
	svc             *Service
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	stopped         bool
	recoveryRunning bool
	recoveryAgain   bool
	flights         map[creationKey]*creationFlight
	cancellations   map[creationKey]*creationCancellationFlight
	locks           map[creationKey]*creationLock
	workers         sync.WaitGroup
	generation      uint64
	reads           map[*tabReconciliationPass]uint64
	versions        map[ownedTabKey]uint64
}

func newTabCreationCoordinator(svc *Service) *tabCreationCoordinator {
	ctx, cancel := context.WithCancel(context.Background())
	return &tabCreationCoordinator{svc: svc, ctx: ctx, cancel: cancel, flights: make(map[creationKey]*creationFlight), cancellations: make(map[creationKey]*creationCancellationFlight), locks: make(map[creationKey]*creationLock), reads: make(map[*tabReconciliationPass]uint64), versions: make(map[ownedTabKey]uint64)}
}

func (c *tabCreationCoordinator) lock(key creationKey) func() {
	c.mu.Lock()
	entry := c.locks[key]
	if entry == nil {
		entry = &creationLock{}
		c.locks[key] = entry
	}
	entry.users++
	c.mu.Unlock()
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		c.mu.Lock()
		entry.users--
		if entry.users == 0 {
			delete(c.locks, key)
		}
		c.mu.Unlock()
	}
}

// creationResponse captures only a unary result. Publication never retains a
// disconnected transport or writes through it from its owned goroutine.
type creationResponse struct{ flight *creationFlight }

func (w creationResponse) SendResponse(r *leapmuxv1.InnerRpcResponse) error {
	w.flight.response = proto.CloneOf(r)
	return nil
}
func (w creationResponse) SendError(code int32, message string) error {
	w.flight.code = code
	w.flight.message = message
	return nil
}
func (creationResponse) SendStream(*leapmuxv1.InnerStreamMessage) error {
	return errors.New("tab creation cannot stream")
}
func (creationResponse) ChannelID() string                                  { return "" }
func (creationResponse) MaxPayloadBudget() int                              { return 0 }
func (creationResponse) BindStream(channel.StreamController) (func(), bool) { return nil, false }

type creationOperation struct {
	coordinator *tabCreationCoordinator
	caller      channel.Caller
	request     proto.Message
	publication *leapmuxv1.TabPublication
	hash        []byte
	record      *db.TabCreation
	runtime     creationRuntime
	admission   *creationStartupAdmission
	persisted   bool
}

func (op *creationOperation) key() creationKey {
	return creationKey{op.caller.UserID.String(), op.publication.GetCreation().GetTabId()}
}
func (op *creationOperation) dbKey() db.GetTabCreationParams {
	return db.GetTabCreationParams{UserID: op.key().owner, WorkerID: op.coordinator.svc.WorkerID, TabID: op.key().tabID}
}

func (svc *Service) coordinateCreation(ctx context.Context, caller channel.Caller, request proto.Message, creation *leapmuxv1.TabCreation, kind leapmuxv1.TabType, sender channel.ResponseWriter, prepare func(*creationOperation, channel.ResponseWriter)) {
	if svc.refuseIfShuttingDown(sender) {
		return
	}
	if err := tabcreation.Validate(creation, kind); err != nil {
		sendInvalidArgument(sender, err.Error())
		return
	}
	if refuseUngranted(sender, caller, leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE) {
		return
	}
	if svc.WorkerID == "" {
		sendFailedPrecondition(sender, "the Worker has no registered identity")
		return
	}
	captured := proto.Clone(request)
	hash, err := tabcreation.Fingerprint(captured)
	if err != nil {
		sendInvalidArgument(sender, err.Error())
		return
	}
	c := svc.creations
	key := creationKey{caller.UserID.String(), creation.GetTabId()}
	c.mu.Lock()
	flight := c.flights[key]
	if flight != nil && !bytes.Equal(flight.hash, hash) {
		c.mu.Unlock()
		sendFailedPrecondition(sender, "the tab ID belongs to a different creation request")
		return
	}
	if flight == nil {
		if c.stopped {
			c.mu.Unlock()
			sendFailedPrecondition(sender, "the Worker is shutting down")
			return
		}
		flight = &creationFlight{hash: hash, done: make(chan struct{})}
		c.flights[key] = flight
		c.workers.Add(1)
		op := &creationOperation{coordinator: c, caller: caller, request: captured, publication: &leapmuxv1.TabPublication{Creation: proto.CloneOf(creation), TabType: kind}, hash: hash}
		go func() {
			defer c.workers.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error("tab creation panicked", "tab_id", key.tabID, "panic", recovered)
					flight.code = int32(codes.Internal)
					flight.message = "Tab creation failed."
				}
				op.releaseStartupAdmission()
				c.mu.Lock()
				delete(c.flights, key)
				close(flight.done)
				c.mu.Unlock()
			}()
			c.runCreationFlight(op, flight, prepare)
		}()
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		sendFailedPrecondition(sender, "tab creation continues on the Worker; retry the same request to learn its result")
	case <-flight.done:
		if flight.code != 0 {
			_ = sender.SendError(flight.code, flight.message)
		} else if flight.response != nil {
			sendInnerResponse(sender, proto.CloneOf(flight.response))
		} else {
			sendInternalError(sender, "tab creation returned no result")
		}
	}
}

// persist commits the resource and pending recovery record in one transaction.
func (op *creationOperation) persist(runtime creationRuntime, write func(context.Context, *db.Queries) error) error {
	unlock := op.coordinator.lock(op.key())
	defer unlock()
	return op.persistLocked(runtime, write)
}

// persistLocked requires the creation-key lock through the caller's post-commit work.
func (op *creationOperation) persistLocked(runtime creationRuntime, write func(context.Context, *db.Queries) error) error {
	c := op.coordinator
	svc := c.svc
	if err := c.ctx.Err(); err != nil {
		return err
	}
	runtimePayload, err := json.Marshal(runtime)
	if err != nil {
		return err
	}
	envelope, err := json.Marshal(creationRecoveryInputs{Runtime: runtimePayload, Scopes: op.caller.Scopes.Scopes(), Unscoped: op.caller.Scopes.IsUnscoped()})
	if err != nil {
		return err
	}
	request, err := proto.Marshal(op.request)
	if err != nil {
		return err
	}
	publication, err := proto.Marshal(op.publication)
	if err != nil {
		return err
	}
	tx, err := svc.DB.BeginTx(c.ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := svc.Queries.WithTx(tx)
	if err := write(c.ctx, q); err != nil {
		return err
	}
	if err := q.InsertTabCreation(c.ctx, db.InsertTabCreationParams{UserID: op.key().owner, WorkerID: svc.WorkerID, TabID: op.key().tabID, TabType: int64(op.publication.GetTabType()), RequestHash: op.hash, RequestPayload: request, PublicationPayload: publication, RuntimePayload: envelope, PublicationResult: []byte{}, State: creationPending}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	op.persisted = true
	op.runtime = runtime
	return nil
}

func (op *creationOperation) publish() (*leapmuxv1.PublishedTab, error) {
	validatedOwner, ownerOK := userid.OwnerFilter(op.caller.UserID)
	if !ownerOK {
		return nil, errCreationOwnerMissing
	}
	queryKey := op.dbKey()
	queryKey.UserID = validatedOwner
	c := op.coordinator
	svc := c.svc
	for {
		row, err := svc.Queries.GetTabCreation(c.ctx, queryKey)
		if err != nil {
			if err := c.waitRetry(); err != nil {
				return nil, err
			}
			continue
		}
		if row.State == creationPublished || row.State == creationCommitted {
			result := &leapmuxv1.PublishTabResponse{}
			if err := proto.Unmarshal(row.PublicationResult, result); err != nil {
				return nil, err
			}
			if result.GetPublished() == nil {
				return nil, errors.New("the saved tab publication is invalid")
			}
			return proto.CloneOf(result.GetPublished()), nil
		}
		if row.State == creationRejected {
			return nil, &creationAbortedError{message: row.FailureMessage}
		}
		if row.State == creationCancelRequested || row.State == creationCanceled {
			return nil, &creationAbortedError{message: "tab creation was canceled"}
		}
		if row.State != creationPending {
			return nil, errors.New("the saved tab creation state is invalid")
		}
		timeout := svc.creationAttemptTimeout()
		attempt, cancel := context.WithTimeout(c.ctx, timeout)
		response, publishErr := svc.PublishTab(attempt, op.publication)
		cancel()
		if publishErr == nil && response.GetOutcome() != nil {
			unlock := c.lock(op.key())
			if rejected := response.GetRejected(); rejected != nil {
				changed, rejectErr := svc.Queries.RejectTabCreation(c.ctx, db.RejectTabCreationParams{State: creationRejected, FailureMessage: rejected.GetMessage(), UserID: validatedOwner, WorkerID: row.WorkerID, TabID: row.TabID, State_2: creationPending})
				if rejectErr == nil && changed == 1 {
					svc.closeTabForConvergence(op.publication.GetTabType(), row.UserID, row.TabID, dropWorktreeLink)
				}
				unlock()
				if rejectErr == nil {
					if changed == 0 {
						return nil, &creationAbortedError{message: "tab creation was canceled"}
					}
					return nil, &creationAbortedError{message: fmt.Sprintf("the Hub rejected tab creation: %s", rejected.GetMessage())}
				}
			} else if published := response.GetPublished(); published != nil {
				if !op.matchesPublication(published) {
					unlock()
					return nil, errors.New("the Hub returned an invalid tab publication")
				}
				payload, marshalErr := proto.Marshal(response)
				if marshalErr != nil {
					unlock()
					return nil, marshalErr
				}
				var changed int64
				if op.publication.GetTabType() == leapmuxv1.TabType_TAB_TYPE_AGENT || op.publication.GetTabType() == leapmuxv1.TabType_TAB_TYPE_TERMINAL {
					changed, err = svc.Queries.PublishTabCreation(c.ctx, db.PublishTabCreationParams{State: creationPublished, PublicationResult: payload, UserID: validatedOwner, WorkerID: row.WorkerID, TabID: row.TabID, State_2: creationPending})
				} else {
					changed, err = svc.Queries.CommitTabCreation(c.ctx, db.CommitTabCreationParams{State: creationCommitted, PublicationResult: payload, UserID: validatedOwner, WorkerID: row.WorkerID, TabID: row.TabID, State_2: creationPending})
				}
				if err == nil && changed != 0 {
					c.mu.Lock()
					c.generation++
					c.versions[newOwnedTabKey(op.publication.GetTabType(), row.TabID, row.UserID)] = c.generation
					c.pruneVersionsLocked()
					c.mu.Unlock()
				}
				unlock()
				if err == nil {
					if changed == 0 {
						return nil, &creationAbortedError{message: "tab creation was canceled"}
					}
					return proto.CloneOf(published), nil
				}
			} else {
				unlock()
			}
		}
		if err := c.waitRetry(); err != nil {
			return nil, err
		}
	}
}

const tabCreationRetryTimerTag = "tab-creation-retry"

func (c *tabCreationCoordinator) waitRetry() error {
	timer := c.svc.Clock.NewTimer(time.Second, tabCreationRetryTimerTag)
	defer timer.Stop(tabCreationRetryTimerTag)
	select {
	case <-c.ctx.Done():
		return c.ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *tabCreationCoordinator) stop() {
	c.mu.Lock()
	c.stopped = true
	c.cancel()
	c.mu.Unlock()
	done := make(chan struct{})
	go func() { c.workers.Wait(); close(done) }()
	c.svc.ShutdownDeadline.Wait(done, "shutdown deadline passed while tab creation still ran; abandoning it")
}

// handoff serializes process startup admission against close and reconciliation.
func (op *creationOperation) handoff(handle *startupEntry, start func()) error {
	validatedOwner, ownerOK := userid.OwnerFilter(op.caller.UserID)
	if !ownerOK {
		return errCreationOwnerMissing
	}
	queryKey := op.dbKey()
	queryKey.UserID = validatedOwner
	unlock := op.coordinator.lock(op.key())
	defer unlock()
	row, err := op.coordinator.svc.Queries.GetTabCreation(op.coordinator.ctx, queryKey)
	if err != nil {
		return err
	}
	if row.State != creationPublished {
		return errors.New("tab creation was canceled")
	}
	live, liveErr := op.resourceLive(op.coordinator.ctx, op.coordinator.svc.Queries)
	if liveErr != nil {
		return liveErr
	}
	if !live {
		return errors.New("the tab closed or was archived before startup")
	}
	if err := op.coordinator.ctx.Err(); err != nil {
		return err
	}
	if op.admission == nil || op.admission.entry != handle {
		return errors.New("the creation has no matching startup admission")
	}
	if err := op.admission.registry.admitCreationProcess(handle, op); err != nil {
		return err
	}
	start()
	return nil
}

// replyCommittedCreation checks present ownership without repeating lifecycle work.
// A negative replay observation does not close a resource or interrupt startup;
// the existing reconciliation and explicit-close paths own those transitions.
func (svc *Service) replyCommittedCreation(op *creationOperation, sender channel.ResponseWriter) {
	result := &leapmuxv1.PublishTabResponse{}
	if err := proto.Unmarshal(op.record.PublicationResult, result); err != nil || result.GetPublished() == nil {
		sendInternalError(sender, "the saved tab publication is invalid")
		return
	}
	observed, err := op.readPublicationLifecycle()
	if err != nil {
		sendCreationError(sender, err)
		return
	}
	defer observed.archive.done()
	if observed.state.GetReceipt().GetRejected() != nil || observed.state.GetPresence() != leapmuxv1.TabPublicationPresence_TAB_PUBLICATION_PRESENCE_HOSTED {
		sendCreationError(sender, &creationAbortedError{message: "the Hub no longer hosts this tab on this Worker"})
		return
	}
	switch op.publication.GetTabType() {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		row, err := svc.Queries.GetAgentByID(op.coordinator.ctx, op.key().tabID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && row.ClosedAt.Valid {
			sendCreationError(sender, &creationAbortedError{message: "the created agent was closed"})
			return
		}
		if err != nil {
			sendInternalError(sender, err.Error())
			return
		}
		sendProtoResponse(sender, &leapmuxv1.OpenAgentResponse{Agent: svc.agentToProto(&row, svc.agentLivenessOf(&row), nil), Publication: result.GetPublished()})
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		row, err := svc.Queries.GetTerminal(op.coordinator.ctx, op.key().tabID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && row.ClosedAt.Valid {
			sendCreationError(sender, &creationAbortedError{message: "the created terminal was closed"})
			return
		}
		if err != nil {
			sendInternalError(sender, err.Error())
			return
		}
		sendProtoResponse(sender, &leapmuxv1.OpenTerminalResponse{TerminalId: row.ID, Title: row.Title, Publication: result.GetPublished()})
	case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
		if _, err := svc.TabPayloads.Get(op.coordinator.ctx, op.key().owner, op.key().tabID); err != nil {
			if errors.Is(err, ErrTabPayloadNotFound) {
				sendCreationError(sender, &creationAbortedError{message: "the created payload was closed"})
			} else {
				sendInternalError(sender, err.Error())
			}
			return
		}
		sendProtoResponse(sender, &leapmuxv1.RegisterTabPayloadResponse{Publication: result.GetPublished()})
	default:
		sendInternalError(sender, "the saved tab type is invalid")
	}
}

// runCreationFlight retains ownership through transient reads after persistence.
func (c *tabCreationCoordinator) runCreationFlight(op *creationOperation, flight *creationFlight, prepare func(*creationOperation, channel.ResponseWriter)) {
	validatedOwner, ownerOK := userid.OwnerFilter(op.caller.UserID)
	if !ownerOK {
		sendFailedPrecondition(creationResponse{flight}, errCreationOwnerMissing.Error())
		return
	}
	queryKey := op.dbKey()
	queryKey.UserID = validatedOwner
	svc := c.svc
	for {
		writer := creationResponse{flight}
		if err := c.ctx.Err(); err != nil {
			sendFailedPrecondition(writer, "the Worker stopped before creation finished")
			return
		}
		flight.response = nil
		flight.code = 0
		flight.message = ""
		unlock := c.lock(op.key())
		row, err := svc.Queries.GetTabCreation(c.ctx, queryKey)
		unlock()
		switch {
		case errors.Is(err, sql.ErrNoRows) && !op.persisted:
			prepare(op, writer)
			if !op.persisted || flight.response != nil || flight.code == int32(codes.Aborted) {
				return
			}
		case err != nil:
			// A transient read cannot abandon an admitted durable creation.
		default:
			if !bytes.Equal(row.RequestHash, op.hash) || row.TabType != int64(op.publication.GetTabType()) {
				sendFailedPrecondition(writer, "the tab ID belongs to a different creation request")
				return
			}
			op.record = &row
			op.persisted = true
			if row.State == creationCommitted || (row.State == creationPublished && svc.creationStartupOwned(op.publication.GetTabType(), row.TabID)) {
				svc.replyCommittedCreation(op, writer)
				return
			}
			if row.State == creationRejected || row.State == creationCancelRequested || row.State == creationCanceled {
				message := row.FailureMessage
				if message == "" {
					message = "tab creation was rejected or canceled"
				}
				sendCreationError(writer, &creationAbortedError{message: message})
				return
			}
			if row.State != creationPending && row.State != creationPublished {
				sendInternalError(writer, "the saved tab creation state is invalid")
				return
			}
			inputs, decodeErr := decodeCreationRecoveryInputs(row.RuntimePayload)
			if decodeErr != nil {
				sendInternalError(writer, decodeErr.Error())
			} else {
				op.record.RuntimePayload = inputs.Runtime
				prepare(op, writer)
				if flight.response != nil || flight.code == int32(codes.Aborted) {
					return
				}
			}
		}
		if err := c.waitRetry(); err != nil {
			sendFailedPrecondition(writer, "the Worker stopped before creation finished")
			return
		}
	}
}

func (svc *Service) creationStartupOwned(kind leapmuxv1.TabType, id string) bool {
	switch kind {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return svc.AgentStartup.hasProcessStartup(id) || svc.Agents.HasAgent(id)
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return svc.TerminalStartup.hasProcessStartup(id) || svc.Terminals.IsRunning(id)
	default:
		return false
	}
}

func (svc *Service) creationAttemptTimeout() time.Duration {
	if svc.APITimeout > 0 {
		return svc.APITimeout
	}
	return config.DefaultAPITimeout
}

func (op *creationOperation) matchesPublication(published *leapmuxv1.PublishedTab) bool {
	return published != nil && proto.Equal(published.GetTab(), &leapmuxv1.TabIdent{TabType: op.publication.GetTabType(), TabId: op.key().tabID}) && published.GetWorkspaceId() == op.publication.GetCreation().GetPlacement().GetWorkspaceId()
}
