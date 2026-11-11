package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/util/userid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/protobuf/proto"
)

// cancelPendingCreation persists close admission before waiting for the Hub.
// A pending close also changes canonical workspace state, so it requires write.
func (svc *Service) cancelPendingCreation(ctx context.Context, caller channel.Caller, request *leapmuxv1.CloseTabRequest, sender channel.ResponseWriter) bool {
	validatedOwner, ownerOK := userid.OwnerFilter(caller.UserID)
	if !ownerOK {
		sendFailedPrecondition(sender, errCreationOwnerMissing.Error())
		return true
	}
	kind := request.GetTab().GetTabType()
	var scope leapmuxv1.Scope
	switch kind {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		scope = leapmuxv1.Scope_SCOPE_AGENT_WRITE
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		scope = leapmuxv1.Scope_SCOPE_TERMINAL_WRITE
	case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
		scope = leapmuxv1.Scope_SCOPE_FILE_READ
	default:
		return false
	}
	if !requireCloseScopes(sender, caller, scope, request.GetWorktreeAction()) {
		return true
	}
	key := creationKey{validatedOwner, request.GetTab().GetTabId()}
	c := svc.creations
	unlock := c.lock(key)
	row, err := svc.Queries.GetTabCreation(c.ctx, db.GetTabCreationParams{UserID: key.owner, WorkerID: svc.WorkerID, TabID: key.tabID})
	if errors.Is(err, sql.ErrNoRows) {
		unlock()
		return false
	}
	if err != nil {
		unlock()
		sendInternalError(sender, err.Error())
		return true
	}
	if row.State == creationCommitted || row.State == creationRejected || (row.State == creationCanceled && !row.CancelAction.Valid) {
		unlock()
		return false
	}
	if row.TabType != int64(kind) {
		unlock()
		sendInvalidArgument(sender, "the tab type does not match its creation")
		return true
	}
	if row.State == creationCanceled && row.CancelAction.Valid {
		action, actionErr := normalizedCreationCloseAction(request.GetWorktreeAction())
		if actionErr != nil {
			unlock()
			sendInvalidArgument(sender, actionErr.Error())
			return true
		}
		if int64(action) != row.CancelAction.Int64 {
			unlock()
			sendFailedPrecondition(sender, "the creation already has a different close action")
			return true
		}
		result, resultErr := recordedCreationCloseResponse(row)
		unlock()
		if resultErr != nil {
			sendInternalError(sender, resultErr.Error())
		} else {
			sendProtoResponse(sender, result)
			if row.CancelProgressPayload != nil {
				svc.TriggerTabCreationRecovery()
			}
		}
		return true
	}
	if refuseUngranted(sender, caller, leapmuxv1.Scope_SCOPE_WORKSPACE_WRITE) {
		unlock()
		return true
	}
	action, actionErr := normalizedCreationCloseAction(request.GetWorktreeAction())
	if actionErr != nil {
		unlock()
		sendInvalidArgument(sender, actionErr.Error())
		return true
	}
	if row.State == creationCancelRequested && row.CancelAction.Int64 != int64(action) {
		unlock()
		sendFailedPrecondition(sender, "the creation already has a different close action")
		return true
	}
	if row.State == creationPending || row.State == creationPublished {
		changed, err := svc.Queries.RequestTabCreationCancel(c.ctx, db.RequestTabCreationCancelParams{State: creationCancelRequested, CancelAction: sql.NullInt64{Int64: int64(action), Valid: true}, UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID, State_2: row.State})
		if err != nil || changed != 1 {
			unlock()
			sendInternalError(sender, "failed to persist tab creation cancellation")
			return true
		}
		row.State = creationCancelRequested
		row.CancelAction = sql.NullInt64{Int64: int64(action), Valid: true}
	}
	// The startup claim exists before persistence. Cancellation reaches it even
	// when no subprocess or worktree link exists yet.
	switch kind {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		svc.AgentStartup.cancelAndClear(row.TabID, keepWorktreeOnClose)
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		svc.TerminalStartup.cancelAndClear(row.TabID, keepWorktreeOnClose)
	default:
		// Payload-backed tabs have no process startup claim.
	}
	unlock()
	result, err := svc.finishPendingCreationCancellation(ctx, row)
	if err != nil {
		sendFailedPrecondition(sender, err.Error())
	} else {
		sendProtoResponse(sender, result)
	}
	return true
}

type creationCancellationFlight struct {
	done   chan struct{}
	result *leapmuxv1.CloseTabResponse
	err    error
}

func normalizedCreationCloseAction(action leapmuxv1.WorktreeAction) (leapmuxv1.WorktreeAction, error) {
	if action == leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED {
		return leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP, nil
	}
	if action != leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP && action != leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE {
		return action, errors.New("the worktree action is invalid")
	}
	return action, nil
}
func recordedCreationCloseResponse(row db.TabCreation) (*leapmuxv1.CloseTabResponse, error) {
	if !row.CloseResponsePayload.Valid {
		return nil, errors.New("the completed cancellation has no recorded result")
	}
	response := &leapmuxv1.CloseTabResponse{}
	if err := proto.Unmarshal(row.CloseResponsePayload.V, response); err != nil {
		return nil, fmt.Errorf("decode the recorded close result: %w", err)
	}
	return response, nil
}

func (svc *Service) finishPendingCreationCancellation(ctx context.Context, row db.TabCreation) (*leapmuxv1.CloseTabResponse, error) {
	ownerID, ownerOK := userid.New(row.UserID)
	if !ownerOK {
		return nil, errCreationOwnerMissing
	}
	c := svc.creations
	key := creationKey{ownerID.String(), row.TabID}
	c.mu.Lock()
	flight := c.cancellations[key]
	if flight == nil {
		if c.stopped {
			c.mu.Unlock()
			return nil, errors.New("the Worker stopped before cancellation finished")
		}
		flight = &creationCancellationFlight{done: make(chan struct{})}
		c.cancellations[key] = flight
		c.workers.Add(1)
		go func() {
			defer c.workers.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.Error("tab cancellation panicked", "tab_id", row.TabID, "panic", recovered)
					flight.err = errors.New("tab cancellation failed")
				}
				c.mu.Lock()
				delete(c.cancellations, key)
				close(flight.done)
				c.mu.Unlock()
			}()
			for {
				flight.result, flight.err = svc.runCreationCancellation(c.ctx, row)
				if flight.err == nil {
					return
				}
				if err := c.waitRetry(); err != nil {
					flight.err = err
					return
				}
			}
		}()
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return proto.CloneOf(flight.result), flight.err
	}
}

func (svc *Service) runCreationCancellation(ctx context.Context, row db.TabCreation) (*leapmuxv1.CloseTabResponse, error) {
	ownerID, ownerOK := userid.New(row.UserID)
	if !ownerOK {
		return nil, errCreationOwnerMissing
	}
	key := db.GetTabCreationParams{UserID: ownerID.String(), WorkerID: row.WorkerID, TabID: row.TabID}
	current, err := svc.Queries.GetTabCreation(ctx, key)
	if err != nil {
		return nil, err
	}
	if current.State == creationCanceled {
		if err := svc.cleanupCompletedCreationCancellation(ctx, current); err != nil {
			slog.Warn("clean up completed tab cancellation failed", "tab_id", row.TabID, "error", err)
		}
		return recordedCreationCloseResponse(current)
	}
	if current.State != creationCancelRequested {
		return nil, errors.New("tab cancellation has no admitted close")
	}
	var registry *startupCore
	switch leapmuxv1.TabType(current.TabType) {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		registry = &svc.AgentStartup.startupCore
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		registry = &svc.TerminalStartup.startupCore
	default:
	}
	if registry != nil {
		release := registry.holdCloseAdmission(row.TabID)
		defer release()
		entry := registry.cancelStartup(row.TabID, false, func(entry *startupEntry) { entry.closeRaced = true; entry.closeDisposition = keepWorktreeOnClose })
		if err := registry.waitForFinishedContext(ctx, entry); err != nil {
			return nil, err
		}
	}
	request := &leapmuxv1.TabPublication{}
	if err := proto.Unmarshal(current.PublicationPayload, request); err != nil {
		return nil, fmt.Errorf("decode pending cancellation: %w", err)
	}
	action := leapmuxv1.WorktreeAction(current.CancelAction.Int64)
	if !current.CancelAction.Valid {
		return nil, errors.New("tab cancellation has no worktree action")
	}
	if _, err := normalizedCreationCloseAction(action); err != nil {
		return nil, err
	}
	attempt, cancel := context.WithTimeout(ctx, svc.creationAttemptTimeout())
	err = svc.CancelTabPublication(attempt, request)
	cancel()
	if err != nil {
		return nil, err
	}
	unlock := svc.creations.lock(creationKey{ownerID.String(), row.TabID})
	defer unlock()
	current, err = svc.Queries.GetTabCreation(ctx, key)
	if err != nil {
		return nil, err
	}
	if current.State == creationCanceled {
		return recordedCreationCloseResponse(current)
	}
	if current.State != creationCancelRequested {
		return nil, errors.New("tab cancellation changed before cleanup")
	}
	journal, err := loadCreationCancelJournal(svc, current)
	if err != nil {
		return nil, err
	}
	if err := journal.prepare(ctx, current); err != nil {
		return nil, err
	}
	if err := journal.retire(ctx); err != nil {
		return nil, err
	}
	snapshot := journal.progress.Snapshot
	retirement := *journal.progress.Retirement
	svc.applyTabCloseEffects(ctx, newOwnedTabKey(snapshot.TabType, snapshot.TabID, snapshot.UserID), retirement)
	result, err := svc.continueTabClose(ctx, snapshot, retirement.RetiredLiveRow, journal)
	if err != nil {
		return nil, err
	}
	result.DescendantAgentIds = retirement.Descendants

	payload, err := proto.Marshal(result)
	if err != nil {
		return nil, err
	}
	if payload == nil {
		payload = []byte{}
	}
	changed, err := svc.Queries.FinishTabCreationCancel(ctx, db.FinishTabCreationCancelParams{State: creationCanceled, CloseResponsePayload: sql.Null[[]byte]{V: payload, Valid: true}, UserID: ownerID.String(), WorkerID: row.WorkerID, TabID: row.TabID, State_2: creationCancelRequested})
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errors.New("tab cancellation state changed during teardown")
	}
	if err := journal.cleanupAfterResult(ctx, releaseWorktreeRemovalIdentity); err != nil {
		slog.Warn("clean up completed tab cancellation failed", "tab_id", row.TabID, "error", err)
	}
	return result, nil
}
