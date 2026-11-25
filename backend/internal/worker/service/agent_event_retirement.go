package service

import (
	"context"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// retireAgentEventHistory joins the physical root before sealing its retired
// tabs. Each virtual child has its own history and shares the root's process.
func (svc *Service) retireAgentEventHistory(rootID string, retiredIDs []string) {
	if svc.retireSemanticHistory(leapmuxv1.TabType_TAB_TYPE_AGENT, retiredIDs, func(ctx context.Context) bool {
		if svc.AgentStartup.waitForTabStartup(ctx, rootID) != nil ||
			svc.semanticEvents.agentMutations.wait(ctx, rootID) != nil ||
			svc.Agents.WaitForAgentExit(ctx, rootID) != nil {
			return false
		}
		complete := true
		for _, id := range retiredIDs {
			if svc.InputQueue.WaitForRetiredDispatch(ctx, id) != nil {
				return false
			}
			if !svc.recordRetiredAgentResponses(ctx, id) {
				complete = false
			}
			if !svc.Output.waitForAgentOutput(ctx, id) {
				complete = false
			}
		}
		for _, id := range retiredIDs {
			if err := svc.Output.clearAgentControlRequests(ctx, id); err != nil {
				complete = false
			}
		}
		return complete && svc.Output.activityByRoot.wait(ctx, rootID) == nil
	}) {
		return
	}
	// When no active owned journal will run the seal callback, still finalize
	// delivered control answers and clear retired control requests so closed
	// agents do not strand unrecorded answers in DELIVERED.
	ctx, cancel := context.WithTimeout(bgCtx(), semanticJournalDeadline)
	defer cancel()
	for _, id := range retiredIDs {
		svc.recordRetiredAgentResponses(ctx, id)
		_ = svc.Output.clearAgentControlRequests(ctx, id)
	}
}

// recordRetiredAgentResponses recovers only answers whose provider delivery
// already succeeded. An uncertain or pending delivery cannot prove coverage.
func (svc *Service) recordRetiredAgentResponses(ctx context.Context, id string) bool {
	complete := true
	for _, state := range []leapmuxv1.ControlDeliveryState{storedStateDelivered, storedStatePending, storedStateUncertain} {
		answers, err := svc.Queries.ListControlResponsesAwaitingRecording(ctx, db.ListControlResponsesAwaitingRecordingParams{AgentID: id, State: state})
		if err != nil {
			return false
		}
		if state != storedStateDelivered {
			complete = complete && len(answers) == 0
			continue
		}
		for _, answer := range answers {
			if ctx.Err() != nil {
				return false
			}
			if err := svc.resumeControlResponseFinalization(ctx, answer); err != nil {
				slog.Warn("record a delivered response while retiring an agent", "agent_id", id, "error", err)
				complete = false
			}
		}
	}
	return complete
}
