package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// creationRecoveryInputs retains the admitted grant with private launch intent.
// Recovery never constructs a grant broader than the one that admitted creation.
type creationRecoveryInputs struct {
	Runtime  json.RawMessage
	Scopes   []leapmuxv1.Scope
	Unscoped bool
}

func encodeCreationRecoveryInputs(runtime creationRuntime, scopes authscope.ScopeSet) ([]byte, error) {
	payload, err := json.Marshal(runtime)
	if err != nil {
		return nil, fmt.Errorf("encode tab creation recovery runtime: %w", err)
	}
	if len(payload) == 0 || payload[0] != '{' {
		return nil, errors.New("tab creation recovery runtime must be a non-null JSON object")
	}
	return json.Marshal(creationRecoveryInputs{Runtime: payload, Scopes: scopes.Scopes(), Unscoped: scopes.IsUnscoped()})
}

func decodeCreationRecoveryInputs(payload []byte) (creationRecoveryInputs, error) {
	var inputs creationRecoveryInputs
	if err := json.Unmarshal(payload, &inputs); err != nil {
		return inputs, fmt.Errorf("decode tab creation recovery inputs: %w", err)
	}
	if runtime := bytes.TrimSpace(inputs.Runtime); len(runtime) == 0 || runtime[0] != '{' {
		return inputs, errors.New("tab creation recovery runtime must be a non-null JSON object")
	}
	return inputs, nil
}

// ResolvePendingTabCreations runs before inventory uploads on usable identity.
// The coordinator owns admitted publications after this caller disconnects.
func (svc *Service) ResolvePendingTabCreations(ctx context.Context) error {
	validatedOwner, ownerOK := userid.OwnerFilter(svc.RegisteredBy())
	if !ownerOK {
		return errCreationOwnerMissing
	}
	rows, err := svc.Queries.ListPendingTabCreations(ctx, db.ListPendingTabCreationsParams{UserID: validatedOwner, WorkerID: svc.WorkerID, PendingState: creationPending, CancelRequestedState: creationCancelRequested, PublishedState: creationPublished, CanceledState: creationCanceled})
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := svc.resumeTabCreation(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

func (svc *Service) resumeTabCreation(ctx context.Context, row db.TabCreation) error {
	owner, ok := userid.New(row.UserID)
	if !ok || !svc.RegisteredBy().Matches(row.UserID) {
		return errors.New("pending tab creation has no matching Worker owner")
	}
	if row.State == creationCancelRequested {
		if _, err := svc.finishPendingCreationCancellation(ctx, row); err != nil {
			return err
		}
		return nil
	}
	if row.State == creationCanceled {
		if err := svc.cleanupCompletedCreationCancellation(ctx, row); err != nil {
			slog.Warn("clean up completed tab cancellation failed", "tab_id", row.TabID, "error", err)
		}
		return ctx.Err()
	}
	inputs, err := decodeCreationRecoveryInputs(row.RuntimePayload)
	if err != nil {
		return err
	}
	var scopes authscope.ScopeSet
	if inputs.Unscoped {
		scopes = authscope.UnscopedGrant()
	} else {
		var ok bool
		scopes, ok = authscope.ScopesFromWire(inputs.Scopes)
		if !ok {
			return errors.New("pending tab creation has an invalid grant")
		}
	}
	caller := channel.NewCaller(owner, scopes)
	// These are the same resource permissions checked by the dispatcher. Recovery
	// bypasses transport decoding, so it must check them before entering a handler.
	var request proto.Message
	switch leapmuxv1.TabType(row.TabType) {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		if !caller.Allows(leapmuxv1.Scope_SCOPE_AGENT_WRITE) {
			return errors.New("pending agent creation has no creation grant")
		}
		request = &leapmuxv1.OpenAgentRequest{}
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		if !caller.Allows(leapmuxv1.Scope_SCOPE_TERMINAL_WRITE) {
			return errors.New("pending terminal creation has no creation grant")
		}
		request = &leapmuxv1.OpenTerminalRequest{}
	case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
		if !callerReadsTabType(caller, leapmuxv1.TabType(row.TabType)) {
			return errors.New("pending payload creation has no creation grant")
		}
		request = &leapmuxv1.RegisterTabPayloadRequest{}
	default:
		return errors.New("pending tab creation has an invalid resource type")
	}
	if err := proto.Unmarshal(row.RequestPayload, request); err != nil {
		return fmt.Errorf("decode pending tab creation request: %w", err)
	}
	flight := &creationFlight{}
	writer := creationResponse{flight: flight}
	switch request := request.(type) {
	case *leapmuxv1.OpenAgentRequest:
		svc.handleOpenAgent(ctx, caller, request, writer)
	case *leapmuxv1.OpenTerminalRequest:
		svc.handleOpenTerminal(ctx, caller, request, writer)
	case *leapmuxv1.RegisterTabPayloadRequest:
		svc.handleRegisterTabPayload(ctx, caller, request, writer)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if flight.code == int32(codes.Aborted) {
		return nil
	}
	if flight.code != 0 {
		current, err := svc.Queries.GetTabCreation(ctx, db.GetTabCreationParams{UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID})
		if err != nil {
			return err
		}
		if current.State == creationRejected || current.State == creationCanceled {
			return nil
		}
		return fmt.Errorf("resume pending tab creation: %s", flight.message)
	}
	return nil
}

// TriggerTabCreationRecovery coalesces reconciliation and unarchive notifications.
func (svc *Service) TriggerTabCreationRecovery() {
	c := svc.creations
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	if c.recoveryRunning {
		c.recoveryAgain = true
		c.mu.Unlock()
		return
	}
	c.recoveryRunning = true
	c.workers.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.workers.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				c.mu.Lock()
				c.recoveryRunning = false
				c.mu.Unlock()
				slog.Error("tab creation recovery panicked", "panic", recovered)
			}
		}()
		for {
			if err := svc.ResolvePendingTabCreations(c.ctx); err != nil {
				if err := c.waitRetry(); err != nil {
					c.mu.Lock()
					c.recoveryRunning = false
					c.mu.Unlock()
					return
				}
				continue
			}
			c.mu.Lock()
			again := c.recoveryAgain
			c.recoveryAgain = false
			if !again {
				c.recoveryRunning = false
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
		}
	}()
}

// awaitCreationStartup keeps other process starters behind the captured plan.
func (svc *Service) awaitCreationStartup(ctx context.Context, kind leapmuxv1.TabType, tabID string) error {
	validatedOwner, ownerOK := userid.OwnerFilter(svc.RegisteredBy())
	if !ownerOK {
		return errCreationOwnerMissing
	}
	c := svc.creations
	key := creationKey{validatedOwner, tabID}
	for {
		c.mu.Lock()
		flight := c.flights[key]
		c.mu.Unlock()
		if flight != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-flight.done:
			}
			if flight.code != 0 {
				return fmt.Errorf("tab creation failed: %s", flight.message)
			}
			continue
		}
		row, err := svc.Queries.GetTabCreation(ctx, db.GetTabCreationParams{UserID: key.owner, WorkerID: svc.WorkerID, TabID: tabID})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.TabType != int64(kind) {
			return errors.New("the resource type does not match its creation")
		}
		if row.State != creationPending && row.State != creationPublished && row.State != creationCommitted {
			return errors.New("tab creation was rejected or canceled")
		}
		var checkpoint <-chan struct{}
		switch kind {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			resource, err := svc.Queries.GetAgentByID(ctx, tabID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && resource.WorkspaceArchived {
				return errAgentArchivedDuringLaunch
			}
			checkpoint = svc.AgentStartup.creationCheckpointDone(tabID)
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			resource, err := svc.Queries.GetTerminal(ctx, tabID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && resource.WorkspaceArchived {
				return errors.New("the terminal belongs to an archived workspace")
			}
			checkpoint = svc.TerminalStartup.creationCheckpointDone(tabID)
		default:
			return errors.New("this tab kind has no process startup")
		}
		if checkpoint != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-checkpoint:
			}
			continue
		}
		// The durable write precedes finishEntry. Public replacements also join
		// that tail so their admission cannot collide with the original claim.
		if row.State == creationCommitted {
			return nil
		}
		if err := svc.resumeTabCreation(ctx, row); err != nil {
			return err
		}
	}
}

func (svc *Service) awaitAgentCreationCheckpoint(agentID string) error {
	ctx, cancel := context.WithTimeout(svc.creations.ctx, svc.agentStartupTimeout())
	defer cancel()
	return svc.awaitCreationStartup(ctx, leapmuxv1.TabType_TAB_TYPE_AGENT, agentID)
}
