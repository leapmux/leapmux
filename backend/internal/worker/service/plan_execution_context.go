package service

import (
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// prepareApprovedPlanContext records an intentional replacement before later settings or input can fail.
// A retry uses that recorded session and cannot clear its context again.
func (svc *Service) prepareApprovedPlanContext(item inputqueue.DispatchItem, input *controlInput) error {
	provider, release := svc.Agents.LockProvider(item.AgentID)
	defer release()
	current, err := svc.queueAgentRow(item.AgentID)
	if err != nil {
		return err
	}
	if err := input.validateSession(current); err != nil {
		return err
	}
	if input.source == nil {
		return errors.New("the context replacement has no recorded approval")
	}
	if input.source.ExecutionSessionID != "" {
		return nil
	}
	var sessionID string
	if provider == nil {
		err = agent.ErrAgentNotFound
	} else {
		sessionID, err = provider.ClearContext()
	}
	restarted := errors.Is(err, agent.ErrContextClearUnsupported) || errors.Is(err, agent.ErrAgentNotFound)
	if restarted {
		sessionID, err = svc.restartPlanContextLocked(item.AgentID, item.TargetMode, current)
	}
	if sessionID == "" {
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: the provider did not report the prepared session", agent.ErrDeliveryUncertain)
	}
	if updateErr := svc.Queries.UpdateAgentSessionID(bgCtx(), db.UpdateAgentSessionIDParams{ID: item.AgentID, AgentSessionID: sessionID}); updateErr != nil {
		return fmt.Errorf("%w: record the prepared provider session: %w", agent.ErrDeliveryUncertain, errors.Join(err, updateErr))
	}
	changed, recordErr := svc.Queries.RecordControlResponseExecutionSession(bgCtx(), db.RecordControlResponseExecutionSessionParams{
		State:   storedStateCompleted,
		AgentID: item.AgentID, InputID: item.ID, AgentSessionID: input.source.AgentSessionID,
		AgentProvider: input.source.AgentProvider, ExecutionSessionID: sessionID,
	})
	if recordErr != nil {
		return fmt.Errorf("%w: record the plan execution session: %w", agent.ErrDeliveryUncertain, errors.Join(err, recordErr))
	}
	if changed != 1 {
		return fmt.Errorf("%w: the plan execution session did not match one recorded approval", agent.ErrDeliveryUncertain)
	}
	input.source.ExecutionSessionID = sessionID
	if !restarted {
		svc.Output.ResetSpanTracker(item.AgentID)
		svc.Output.PersistLeapMuxNotification(item.AgentID, current.AgentProvider, map[string]interface{}{
			contracts.NotificationFieldType: contracts.NotificationTypeContextCleared,
		})
		svc.Output.PersistLeapMuxNotification(item.AgentID, current.AgentProvider, map[string]interface{}{
			contracts.NotificationFieldType: contracts.NotificationTypePlanExecution, contracts.NotificationFieldPlanFilePath: current.PlanFilePath,
		})
	}
	return err
}

func (svc *Service) confirmPreparedPlanSettings(item inputqueue.DispatchItem, input controlInput) error {
	_, release := svc.Agents.LockProvider(item.AgentID)
	defer release()
	current, err := svc.queueAgentRow(item.AgentID)
	if err != nil {
		return err
	}
	if err := input.validateSession(current); err != nil {
		return err
	}
	if item.TargetMode == "" {
		return nil
	}
	_, err = svc.applyPlanOptionsLocked(current, OptionMap{agent.OptionIDPermissionMode: item.TargetMode})
	return err
}
