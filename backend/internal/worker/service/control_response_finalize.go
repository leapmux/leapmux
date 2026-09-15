package service

import (
	"database/sql"
	"errors"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

var errControlResponseAlreadyFinalized = errors.New("the control response is already finalized")

func (svc *Service) resumeControlResponseFinalization(answer db.ControlResponseAnswer) error {
	switch answer.State {
	case storedStateCompleted:
		return nil
	case storedStateDelivered:
		return svc.finalizeControlResponse(answer)
	default:
		return fmt.Errorf("%w: this control response has no confirmed delivery", agent.ErrDeliveryUncertain)
	}
}

func controlResponsePlanFromAnswer(answer db.ControlResponseAnswer) (controlResponsePlan, error) {
	meta := completeControlRequestMetadata(controlResponseRequestMetadata{
		RequestID: answer.RequestID, ClaimToken: answer.ClaimToken,
		AgentSessionID: answer.AgentSessionID, Payload: answer.RequestPayload,
		SourceSeq: answer.SourceSeq,
		Exists:    len(answer.RequestPayload) > 0,
	})
	plan := resolveControlResponsePlan(agent.ProviderFor(answer.AgentProvider), meta, answer.ResponseContent, nil)
	if len(answer.PlanApprovalSettings) != 0 {
		plan.settings = &leapmuxv1.PlanApprovalSettings{}
		if err := protojson.Unmarshal(answer.PlanApprovalSettings, plan.settings); err != nil {
			return controlResponsePlan{}, fmt.Errorf("read saved plan approval settings: %w", err)
		}
		// Keep the STORED bytes rather than re-encode the message. The transcript
		// row must repeat what the claim row holds, and protojson gives a
		// different byte string for each marshal of one message.
		plan.settingsJSON = answer.PlanApprovalSettings
	}
	// Recovery must retain the actual prepared bytes, even if the provider encoder changes.
	plan.resolution.Content = answer.ResolvedContent
	plan.resolution.Feedback = answer.Feedback
	return plan, nil
}

// finalizeControlResponse commits the transcript, cancellation, and completion in one transaction.
// Provider delivery occurs before this function. No database transaction waits for provider output.
func (svc *Service) finalizeControlResponse(answer db.ControlResponseAnswer) error {
	agentID := answer.AgentID
	plan, err := controlResponsePlanFromAnswer(answer)
	if err != nil {
		return err
	}
	provider := answer.AgentProvider
	if !plan.isPlanPrompt() && !plan.exitPlanClearingContext() {
		if err := svc.recordControlResponsePlanMode(agentID, provider, plan); err != nil {
			return err
		}
	}
	var write *messageWrite
	if plan.needsTranscriptRow() {
		content, err := controlResponseMessageContent(plan)
		if err != nil {
			return err
		}
		prepared := svc.Output.prepareMessage(agentID, provider, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content,
			agent.SpanInfo{MarkType: leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE}, nil)
		write = &prepared
	}
	ctx := bgCtx()
	var seq int64
	var removed db.ControlRequest
	apply := func(tx *sql.Tx) error {
		queries := svc.Queries.WithTx(tx)
		completed, err := queries.CompleteControlResponseAnswer(ctx, db.CompleteControlResponseAnswerParams{
			State: storedStateCompleted, RequiredState: storedStateDelivered,
			AgentID: agentID, RequestID: answer.RequestID, ClaimToken: answer.ClaimToken,
		})
		if err != nil {
			return err
		}
		if completed == 0 {
			return errControlResponseAlreadyFinalized
		}
		if write != nil {
			seq, err = createMessageRow(ctx, queries, write.params)
			if err != nil {
				return err
			}
		}
		removed, err = queries.DeleteControlRequestInstance(ctx, db.DeleteControlRequestInstanceParams{
			AgentID: agentID, RequestID: answer.RequestID, ClaimToken: answer.ClaimToken,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if answer.Feedback != "" {
		_, err = svc.InputQueue.EnqueueWithMutation(ctx, inputqueue.NewItem{
			ID: answer.InputID, AgentID: agentID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK,
			Text: answer.Feedback,
		}, apply)
	} else {
		var tx *sql.Tx
		tx, err = svc.DB.BeginTx(ctx, nil)
		if err == nil {
			defer func() { _ = tx.Rollback() }()
			err = apply(tx)
			if err == nil {
				err = tx.Commit()
			}
		}
	}
	if errors.Is(err, errControlResponseAlreadyFinalized) {
		return nil
	}
	if err != nil {
		return err
	}
	if removed.RequestID != "" {
		if plan.resolution.SelfDisplayed && plan.requestMeta.ToolUseID != "" {
			if sink := svc.Output.sinkForAgent(agentID); sink != nil {
				sink.SetSpanType(plan.requestMeta.ToolUseID, plan.requestMeta.ToolName)
			}
		}
	}
	// Every subscriber must retire the response, even if its provider request disappeared earlier.
	svc.Output.broadcastControlCancel(agentID, answer.RequestID, answer.ClaimToken)
	if write != nil {
		svc.Output.publishMessageWrite(*write, seq)
	}
	if answer.InputID != "" {
		svc.InputQueue.NotifyDependencyReady(agentID)
	}
	return nil
}

func (svc *Service) recordControlResponsePlanMode(agentID string, provider leapmuxv1.AgentProvider, plan controlResponsePlan) error {
	if plan.resolution.PlanModeControl == agent.PlanModeControlNone {
		return nil
	}
	unlock := svc.Agents.LockAgent(agentID)
	defer unlock()
	current, err := svc.Queries.GetAgentByID(bgCtx(), agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.AgentProvider != provider || current.AgentSessionID != plan.requestMeta.AgentSessionID {
		return nil
	}
	return svc.applyControlResponsePlanModeMutations(current, plan)
}
