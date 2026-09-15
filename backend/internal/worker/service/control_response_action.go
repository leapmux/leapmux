package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

// controlResponseQueueID keeps application effects idempotent across response retries.
func controlResponseQueueID(agentID string, plan controlResponsePlan) string {
	queuesInput := plan.resolution.Feedback != "" || plan.exitPlanClearingContext() || plan.isPlanPrompt() && (plan.behavior() == agent.ControlBehaviorAllow || plan.rejectionMessage() != "")
	if !queuesInput {
		return ""
	}
	key, _ := json.Marshal([]string{agentID, plan.requestMeta.RequestID, plan.requestMeta.ClaimToken})
	digest := sha256.Sum256(key)
	return "control-" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func (svc *Service) executeControlResponse(agentID string, currentAgent db.Agent, plan controlResponsePlan) error {
	// Keep session validation and every approval effect inside the lifecycle lock.
	unlock := svc.Agents.LockAgent(agentID)
	defer unlock()
	latest, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		return fmt.Errorf("read the provider session before sending a control response: %w", err)
	}
	if latest.AgentSessionID != plan.requestMeta.AgentSessionID || latest.AgentProvider != currentAgent.AgentProvider {
		return errors.New("the control request belongs to a different provider session")
	}
	if mode := plan.settings.GetPermissionMode(); mode != "" {
		group := optionids.GroupByID(svc.optionGroupsForAgent(&latest), agent.OptionIDPermissionMode)
		if len(group.GetOptions()) == 0 || !optionValueInGroup(group, mode) {
			return errors.New("the selected permission mode is unavailable for this provider session")
		}
	}
	if plan.isPlanPrompt() {
		return svc.executePlanPromptResponse(agentID, latest, plan)
	}
	if plan.exitPlanClearingContext() {
		if err := svc.applyControlResponsePlanModeMutations(latest, plan); err != nil {
			return err
		}
		provider := agent.ProviderFor(latest.AgentProvider)
		mode := resolveTargetMode(plan.settings.GetPermissionMode(), provider.PlanModePermissionMode(agent.PlanModeControlExit))
		return svc.enqueuePlanExecution(agentID, mode, controlResponseQueueID(agentID, plan))
	}
	// processControlResponse refuses a withheld resolution before it calls this
	// function, so every plan that arrives here forwards its content.
	return svc.sendControlResponseFn(agentID, plan.resolution.Content)
}

func (svc *Service) executePlanPromptResponse(agentID string, currentAgent db.Agent, plan controlResponsePlan) error {
	inputID := controlResponseQueueID(agentID, plan)
	if plan.behavior() == agent.ControlBehaviorDeny {
		if feedback := plan.rejectionMessage(); feedback != "" {
			_, err := svc.InputQueue.Enqueue(bgCtx(), inputqueue.NewItem{
				ID: inputID, AgentID: agentID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: feedback,
			})
			return err
		}
		return nil
	}
	provider := agent.ProviderFor(currentAgent.AgentProvider)
	options := provider.PlanApprovalOptions(plan.settings.GetPermissionMode())
	updatedAgent, err := svc.applyPlanOptionsLocked(currentAgent, options)
	if err != nil {
		return err
	}
	if plan.settings.GetClearContext() {
		mode := loadOptions(updatedAgent.Options, updatedAgent.AgentProvider)[agent.OptionIDPermissionMode]
		mode = resolveTargetMode(mode, provider.PlanModePermissionMode(agent.PlanModeControlPrompt))
		return svc.enqueuePlanExecution(agentID, mode, inputID)
	}
	_, err = svc.InputQueue.Enqueue(bgCtx(), inputqueue.NewItem{
		ID: inputID, AgentID: agentID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION, Text: planExecutionPromptText,
	})
	return err
}
