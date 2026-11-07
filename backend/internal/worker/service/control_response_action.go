package service

import (
	"crypto/sha256"
	"database/sql"
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
	queuesInput := plan.resolution.FollowUp.Text != "" || plan.exitPlanClearingContext() || plan.isPlanPrompt() && (plan.behavior() == agent.ControlBehaviorAllow || plan.rejectionMessage() != "")
	if !queuesInput {
		return ""
	}
	key, _ := json.Marshal([]string{agentID, plan.requestMeta.RequestID, plan.requestMeta.ClaimToken})
	digest := sha256.Sum256(key)
	return "control-" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func (svc *Service) executeControlResponse(agentID string, currentAgent db.Agent, plan controlResponsePlan) error {
	// A skip of a question that LeapMux owns sends nothing, so the session does
	// not matter: the user chose not to answer, and Codex keeps no pending
	// question state.
	if plan.resolution.LeapMuxOwned && plan.resolution.FollowUp == (agent.ControlFollowUp{}) && !plan.isPlanPrompt() {
		return nil
	}
	// A virtual child has no process of its own. Its root provider owns every
	// native child session, so the answer must use the root's process route.
	ownerID, err := svc.Queries.GetRootAgentID(bgCtx(), agentID)
	if err != nil {
		return fmt.Errorf("resolve the root provider process for a control response: %w", err)
	}
	// Lock the recipient before the child. Keep both session validation and
	// approval effects inside the recipient's lifecycle operation.
	unlockOwner := svc.Agents.LockAgent(ownerID)
	defer unlockOwner()
	if ownerID != agentID {
		unlockChild := svc.Agents.LockAgent(agentID)
		defer unlockChild()
	}
	latest, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if err != nil {
		return fmt.Errorf("read the provider session before sending a control response: %w", err)
	}
	if latest.AgentSessionID != plan.requestMeta.AgentSessionID || latest.AgentProvider != currentAgent.AgentProvider {
		return errors.New("the control request belongs to a different provider session")
	}
	currentOwnerID, err := svc.Queries.GetRootAgentID(bgCtx(), agentID)
	if err != nil {
		return fmt.Errorf("recheck the provider process for a control response: %w", err)
	}
	if currentOwnerID != ownerID {
		return errors.New("the control request belongs to a different provider process")
	}
	if ownerID != agentID {
		owner, err := svc.Queries.GetAgentByID(bgCtx(), ownerID)
		if err != nil {
			return fmt.Errorf("read the recipient provider before sending a control response: %w", err)
		}
		if owner.AgentProvider != latest.AgentProvider {
			return errors.New("the control request belongs to a different provider process")
		}
	}
	pending, err := svc.Queries.GetControlRequest(bgCtx(), db.GetControlRequestParams{AgentID: agentID, RequestID: plan.requestMeta.RequestID})
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("the control request is no longer pending")
	}
	if err != nil {
		return fmt.Errorf("recheck the pending control request before delivery: %w", err)
	}
	if pending.ClaimToken != plan.requestMeta.ClaimToken || pending.AgentSessionID != plan.requestMeta.AgentSessionID {
		return errors.New("the control request is no longer pending")
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
		provider := svc.Agents.Registry().Plugin(latest.AgentProvider)
		mode := resolveTargetMode(plan.settings.GetPermissionMode(), provider.PlanModePermissionMode(agent.PlanModeControlExit))
		return svc.enqueuePlanExecution(agentID, mode, controlResponseQueueID(agentID, plan))
	}
	// A question that LeapMux owns has no provider request waiting for an answer.
	// Its follow-up input carries the answer at dispatch.
	if plan.resolution.LeapMuxOwned {
		return nil
	}
	// processControlResponse refuses a withheld resolution before it calls this
	// function, so every plan that arrives here forwards its content.
	return svc.sendControlResponseFn(ownerID, plan.resolution.Content)
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
	provider := svc.Agents.Registry().Plugin(currentAgent.AgentProvider)
	options := provider.PlanApprovalOptions(plan.settings.GetPermissionMode())
	updatedAgent, err := svc.applyPlanOptionsLocked(currentAgent, options)
	if err != nil {
		return err
	}
	if plan.settings.GetClearContext() {
		mode := loadOptions(svc.Agents.Registry(), updatedAgent.Options, updatedAgent.AgentProvider)[agent.OptionIDPermissionMode]
		mode = resolveTargetMode(mode, provider.PlanModePermissionMode(agent.PlanModeControlPrompt))
		return svc.enqueuePlanExecution(agentID, mode, inputID)
	}
	_, err = svc.InputQueue.Enqueue(bgCtx(), inputqueue.NewItem{
		ID: inputID, AgentID: agentID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION, Text: planExecutionPromptText,
	})
	return err
}

// answersARetiredInstance reports that this answer identifies a request instance the
// worker no longer holds, because the stored claim token differs from the one the
// browser sent.
//
// Its caller refuses the answer in SILENCE. See the gate in processControlResponse
// for why that is the correct outcome rather than an error.
func (plan controlResponsePlan) answersARetiredInstance(claimToken string) bool {
	return plan.requestMeta.ClaimToken != claimToken
}
