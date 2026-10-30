package inputqueue

import (
	"context"
	"errors"
	"fmt"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// reconcileBeforeClaimLocked returns with c.mu held on every path. Only a
// ready native candidate releases it for RPC. False readiness must be consumed
// under this same lock, so its timer cannot overwrite a newer publication.
func (m *Manager) reconcileBeforeClaimLocked(ctx context.Context, c *coordinator, request dispatchPreparation) (dispatchReadiness, error) {
	required, err := m.requiresQueueReconciliation(ctx, request.AgentID)
	if err != nil {
		return dispatchReadiness{}, m.recordReconciliationFailureLocked(ctx, c, request.AgentID, err)
	}
	if !required {
		return dispatchReadiness{Ready: true}, nil
	}
	readiness, err := m.store.dispatchReady(ctx, request)
	if err != nil || !readiness.Ready {
		return readiness, err
	}
	c.mu.Unlock()
	_, err = m.reconcileRuntime(ctx, readiness.Candidate)
	c.mu.Lock()
	if err != nil {
		return readiness, m.recordReconciliationFailureLocked(ctx, c, request.AgentID, err)
	}
	return readiness, nil
}

func (m *Manager) requiresQueueReconciliation(ctx context.Context, agentID string) (bool, error) {
	reconciler, supported := m.dispatcher.(DispatchReconciler)
	if !supported {
		return false, nil
	}
	return reconciler.RequiresQueueReconciliation(ctx, agentID)
}

func (m *Manager) reconcileRuntime(ctx context.Context, request QueueReconciliationRequest) (bool, error) {
	reconciler, supported := m.dispatcher.(DispatchReconciler)
	if !supported {
		return false, nil
	}
	return reconciler.ReconcileBeforeDispatch(ctx, request)
}

func (m *Manager) recordReconciliationFailureLocked(ctx context.Context, c *coordinator, agentID string, failure error) error {
	reason := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED
	var notReady *DeliveryError
	if errors.As(failure, &notReady) && notReady != nil && notReady.Outcome == DispatchNotReady {
		_, known := leapmuxv1.AgentInputQueuePauseReason_name[int32(notReady.PauseReason)]
		if known && notReady.PauseReason != leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED {
			reason = notReady.PauseReason
		}
	}
	snapshot, changed, err := m.store.PauseForReconciliationFailure(context.WithoutCancel(ctx), agentID, reason)
	m.enqueueFailureLocked(c, "agent native queue synchronization failed", "agent_id", agentID, "error", failure)
	if err == nil && changed {
		m.enqueueSnapshotLocked(c, snapshot)
	}
	return errors.Join(failure, err)
}

func (m *Manager) recoveredAgentIDs() []string {
	m.lifecycleMu.Lock()
	agentIDs := make([]string, 0, len(m.recovered))
	for agentID := range m.recovered {
		agentIDs = append(agentIDs, agentID)
	}
	m.lifecycleMu.Unlock()
	slices.Sort(agentIDs)
	return agentIDs
}

// The caller holds recoveryMu. Only short queue operations take c.mu.
func (m *Manager) reconcileRecovered(ctx context.Context) error {
	if _, supported := m.dispatcher.(DispatchReconciler); !supported {
		return nil
	}
	var reconcileErr error
	for _, agentID := range m.recoveredAgentIDs() {
		c := m.coordinator(agentID)
		c.mu.Lock()
		required, err := m.requiresQueueReconciliation(ctx, agentID)
		if err != nil {
			err = m.recordReconciliationFailureLocked(ctx, c, agentID, err)
			m.unlockAndPublish(c)
			reconcileErr = errors.Join(reconcileErr, err)
			continue
		}
		if !required {
			m.unlockAndPublish(c)
			continue
		}
		readiness, err := m.store.recoveryReconciliationReady(ctx, agentID)
		m.unlockAndPublish(c)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("read native recovery candidate %s: %w", agentID, err))
			continue
		}
		if !readiness.Ready {
			continue
		}
		reconciled, err := m.reconcileRuntime(ctx, readiness.Candidate)
		c.mu.Lock()
		if err != nil {
			err = m.recordReconciliationFailureLocked(ctx, c, agentID, err)
			m.unlockAndPublish(c)
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("synchronize recovered agent %s: %w", agentID, err))
			continue
		}
		if !reconciled {
			m.unlockAndPublish(c)
			continue
		}
		snapshot, changed, err := m.store.ReleaseReconciledRecoveryPause(ctx, agentID)
		if err == nil && changed {
			m.enqueueSnapshotLocked(c, snapshot)
		}
		m.unlockAndPublish(c)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("release recovered agent %s: %w", agentID, err))
		}
	}
	return reconcileErr
}
