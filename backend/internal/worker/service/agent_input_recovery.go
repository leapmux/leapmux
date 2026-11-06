package service

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// Readiness never authorizes a process replacement. Native work can start
// after the Worker observes idleness, so recovery needs an explicit approval
// that identifies the physical process and its saved conversation.
func (svc *Service) ensureAgentReadyForInput(agentID string) error {
	if err := svc.ensureAgentRunning(agentID, resumeIfConversation, queuedStart); err != nil {
		return err
	}
	return svc.prepareAgentInputProcess(agentID)
}

func processRecoveryRequired(cause error) error {
	return &inputqueue.DeliveryError{
		Err:         fmt.Errorf("%w: Recover login to send queued input. Recovery may interrupt this agent and its subagents: %w", agent.ErrInputProcessRestartRequired, cause),
		Outcome:     inputqueue.DispatchNotReady,
		PauseReason: leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_PROCESS_RECOVERY,
	}
}

// Release the lifecycle lock before a native request can wait. Revalidate the
// captured registration afterward, so a retired preparation cannot authorize
// input to its replacement.
func (svc *Service) prepareAgentInputProcess(agentID string) error {
	_, err := svc.prepareAgentInputProcessTarget(agentID)
	return err
}

// Retain the prepared registration for recovery's final publication and check.
func (svc *Service) prepareAgentInputProcessTarget(agentID string) (agent.StopTarget, error) {
	provider, release := svc.Agents.LockProvider(agentID)
	if provider == nil {
		release()
		return agent.StopTarget{}, notReadyInput(fmt.Errorf("%w: %s", agent.ErrAgentNotFound, agentID))
	}
	target, err := svc.Agents.CaptureStopTarget(agentID)
	preparer, supports := provider.(agent.InputProcessPreparer)
	if !supports {
		release()
		return target, err
	}
	release()
	if err != nil {
		return target, notReadyInput(err)
	}
	err = preparer.PrepareInputProcess()
	unlock := svc.Agents.LockAgent(agentID)
	current := target.IsCurrent()
	unlock()
	if !current {
		return target, notReadyInput(errors.New("the agent process changed while its input preparation was checked; retry the input"))
	}
	if err != nil && (errors.Is(err, agent.ErrInputProcessRestartRequired) || preparer.InputProcessRecoveryRequired()) {
		if svc.inputProcessRecovery(agentID) == nil {
			return target, fmt.Errorf("agent %s has no stored native session to recover; clear the context to continue", agentID)
		}
		return target, processRecoveryRequired(err)
	}
	return target, err
}

// inputProcessRecovery reads a proposal, not an approval. It performs no
// native I/O and takes no lifecycle lock, so queue publication can call it.
// The approval operation validates every field again under lifecycle ownership.
func (svc *Service) inputProcessRecovery(agentID string) *leapmuxv1.AgentInputProcessRecovery {
	required, instanceID := svc.Agents.InputProcessRecoveryState(agentID)
	if !required || instanceID == "" {
		return nil
	}
	value, exists := svc.Output.rootSinks.Load(agentID)
	if !exists {
		return nil
	}
	source := value.(*agentOutputSink)
	sessionID := source.MessageSessionID()
	if !source.registeredCurrent() || source.outputGeneration <= 0 || sessionID == "" {
		return nil
	}
	return &leapmuxv1.AgentInputProcessRecovery{
		OutputGeneration:  source.outputGeneration,
		AgentSessionId:    sessionID,
		AgentProvider:     source.agentProvider,
		ProcessInstanceId: instanceID,
	}
}

func (svc *Service) queueSnapshotProto(snapshot inputqueue.Snapshot) *leapmuxv1.AgentInputQueueSnapshot {
	result := queueSnapshotProto(snapshot)
	result.ProcessRecovery = svc.inputProcessRecovery(snapshot.AgentID)
	if result.ProcessRecovery != nil {
		for _, item := range result.Items {
			if item.Kind != leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER {
				item.CanSteer = false
				item.CanPreempt = false
			}
		}
	}
	return result
}

func sameInputProcessRecovery(left, right *leapmuxv1.AgentInputProcessRecovery) bool {
	return left != nil && right != nil && left.OutputGeneration == right.OutputGeneration &&
		left.AgentSessionId == right.AgentSessionId && left.AgentProvider == right.AgentProvider &&
		left.ProcessInstanceId == right.ProcessInstanceId
}

func inputRecoveryProviderValid(provider leapmuxv1.AgentProvider) bool {
	_, exists := leapmuxv1.AgentProvider_name[int32(provider)]
	return exists && provider != leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED
}

func (svc *Service) listAgentInputRecoveries(provider leapmuxv1.AgentProvider) ([]*leapmuxv1.AgentInputRecoveryTarget, error) {
	if !inputRecoveryProviderValid(provider) {
		return nil, fmt.Errorf("%w: a valid agent provider is required", inputqueue.ErrInvalidInput)
	}
	var targets []*leapmuxv1.AgentInputRecoveryTarget
	for _, agentID := range svc.Agents.ListAgentIDs() {
		recovery := svc.inputProcessRecovery(agentID)
		if recovery == nil || recovery.AgentProvider != provider {
			continue
		}
		row, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read an agent for login recovery: %w", err)
		}
		if row.ClosedAt.Valid || row.WorkspaceArchived || row.ParentAgentID.Valid {
			continue
		}
		targets = append(targets, &leapmuxv1.AgentInputRecoveryTarget{AgentId: agentID, Title: row.Title, Recovery: recovery})
	}
	slices.SortFunc(targets, func(left, right *leapmuxv1.AgentInputRecoveryTarget) int {
		return cmp.Or(strings.Compare(left.Title, right.Title), strings.Compare(left.AgentId, right.AgentId))
	})
	return targets, nil
}

// recoverAgentInputProcess consumes one explicit approval. A different physical
// registration or native conversation invalidates that approval.
// The caller approves interruption of parent and child work, including work
// whose native notification has not arrived. Failed turns are never replayed.
func (svc *Service) recoverAgentInputProcess(agentID string, approved *leapmuxv1.AgentInputProcessRecovery) (returnErr error) {
	if approved == nil || approved.OutputGeneration <= 0 || approved.AgentSessionId == "" || approved.ProcessInstanceId == "" || !inputRecoveryProviderValid(approved.AgentProvider) {
		return fmt.Errorf("%w: recovery must identify a process, conversation, and provider", inputqueue.ErrInvalidInput)
	}
	// Process health joins the queue snapshot without living in its store.
	// Advance the revision even after a refusal so clients discard stale targets.
	defer func() {
		if _, err := svc.InputQueue.RefreshSnapshot(bgCtx(), agentID); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("refresh input after login recovery: %w", err))
		}
	}()
	var restart *inputqueue.PlannedRestart
	var preparedTarget agent.StopTarget
	oldProcessEnded := false
	defer func() {
		if restart == nil {
			return
		}
		if oldProcessEnded {
			// Publish before releasing the restart marker. A resumed runtime
			// can already own work, and the drain must observe that work.
			provider, release := svc.Agents.LockProvider(agentID)
			release()
			if provider != nil {
				provider.PublishTurnActive()
			}
		}
		unlock := svc.Agents.LockAgent(agentID)
		defer unlock()
		if oldProcessEnded && returnErr == nil && (!preparedTarget.IsCurrent() || !svc.Agents.AgentAlive(agentID)) {
			returnErr = notReadyInput(errors.New("the replacement process exited or changed before login recovery completed"))
		}
		reason := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED
		if returnErr != nil {
			reason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED
			if required, _ := svc.Agents.InputProcessRecoveryState(agentID); required {
				reason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_PROCESS_RECOVERY
			}
		}
		if err := restart.FinishInputRecovery(bgCtx(), oldProcessEnded, reason); err != nil {
			returnErr = errors.Join(returnErr, err)
		}
		if oldProcessEnded && returnErr == nil && (!preparedTarget.IsCurrent() || !svc.Agents.AgentAlive(agentID)) {
			returnErr = notReadyInput(errors.New("the replacement process exited or changed before login recovery completed"))
		}
	}()
	err := func() error {
		unlock := svc.Agents.LockAgent(agentID)
		defer unlock()
		if !sameInputProcessRecovery(approved, svc.inputProcessRecovery(agentID)) {
			return fmt.Errorf("%w: the agent process or conversation changed; reopen Recover login", inputqueue.ErrConflict)
		}
		target, err := svc.Agents.CaptureStopTarget(agentID)
		if err != nil {
			return err
		}
		row, err := svc.readOpenAgentForLaunch(agentID)
		if err != nil {
			return err
		}
		if row.ParentAgentID.Valid || row.AgentProvider != approved.AgentProvider || storedSessionID(row) != approved.AgentSessionId || row.OutputGeneration != approved.OutputGeneration {
			return fmt.Errorf("%w: the stored recovery target changed; reopen Recover login", inputqueue.ErrConflict)
		}
		restart, err = svc.InputQueue.BeginInputRecovery(bgCtx(), agentID)
		if err != nil {
			return err
		}
		snapshot, err := svc.InputQueue.Snapshot(bgCtx(), agentID)
		if err != nil {
			return err
		}
		for _, item := range snapshot.Items {
			if item.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING {
				return fmt.Errorf("%w: an input is still being delivered; wait for it to finish and approve recovery again", inputqueue.ErrConflict)
			}
		}
		changed, err := svc.Queries.MarkAgentResumedForInputRecovery(bgCtx(), db.MarkAgentResumedForInputRecoveryParams{
			AgentID: agentID, AgentSessionID: approved.AgentSessionId, OutputGeneration: approved.OutputGeneration,
		})
		if err != nil {
			return fmt.Errorf("record the input recovery resume intent: %w", err)
		}
		if changed != 1 || !target.IsCurrent() {
			return fmt.Errorf("%w: the recovery target changed before replacement", inputqueue.ErrConflict)
		}
		svc.Agents.StopAndWaitAgent(agentID)
		oldProcessEnded = true
		return nil
	}()
	if err != nil {
		return err
	}
	if err := svc.ensureAgentRunning(agentID, resumeStoredSession, queuedStart); err != nil {
		return err
	}
	preparedTarget, returnErr = svc.prepareAgentInputProcessTarget(agentID)
	return returnErr
}

func registerAgentInputRecoveryHandlers(d ownerOnlyRegistrar, svc *Service) {
	registerOwnerGuarded(d, leapmuxv1connect.WorkerAgentInputServiceListAgentInputRecoveriesProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, request *leapmuxv1.ListAgentInputRecoveriesRequest, sender channel.ResponseWriter) {
			targets, err := svc.listAgentInputRecoveries(request.GetAgentProvider())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.ListAgentInputRecoveriesResponse{Targets: targets})
		})
	registerAgentGuardedByID(d, leapmuxv1connect.WorkerAgentInputServiceRecoverAgentInputProcessProcedure, dispatchPlain,
		func(_ context.Context, _ channel.Caller, request *leapmuxv1.RecoverAgentInputProcessRequest, sender channel.ResponseWriter) {
			if sendQueueError(sender, svc.recoverAgentInputProcess(request.GetAgentId(), request.GetRecovery())) {
				return
			}
			snapshot, err := svc.InputQueue.Snapshot(bgCtx(), request.GetAgentId())
			if sendQueueError(sender, err) {
				return
			}
			sendProtoResponse(sender, &leapmuxv1.RecoverAgentInputProcessResponse{Snapshot: svc.queueSnapshotProto(snapshot)})
		})
}
