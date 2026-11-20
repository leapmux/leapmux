package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

var (
	errControlResponseAlreadyFinalized = errors.New("the control response is already finalized")
	errRetiredControlFollowUp          = errors.New("the closed agent cannot receive follow-up input")
)

func (svc *Service) resumeControlResponseFinalization(ctx context.Context, answer db.ControlResponseAnswer) error {
	return resumeControlDelivery(answer.State, func() error { return svc.finalizeControlResponse(ctx, answer) })
}

func controlResponsePlanFromAnswer(registry *agent.Registry, answer db.ControlResponseAnswer) (controlResponsePlan, error) {
	meta := completeControlRequestMetadata(controlResponseRequestMetadata{
		RequestID: answer.RequestID, ClaimToken: answer.ClaimToken,
		AgentSessionID: answer.AgentSessionID, Payload: answer.RequestPayload,
		SourceSeq: answer.SourceSeq, NonBlocking: answer.NonBlocking,
		Exists: len(answer.RequestPayload) > 0,
	})
	plan := resolveControlResponsePlan(registry.Plugin(answer.AgentProvider), meta, answer.ResponseContent, nil)
	if len(answer.PlanApprovalSettings) != 0 {
		plan.settings = &leapmuxv1.PlanApprovalSettings{}
		if err := protojson.Unmarshal(answer.PlanApprovalSettings, plan.settings); err != nil {
			return controlResponsePlan{}, fmt.Errorf("read saved plan approval settings: %w", err)
		}
		// Keep the stored bytes so the transcript row repeats the claim row exactly.
		// Protobuf JSON output can differ across builds, even when the message is unchanged.
		plan.settingsJSON = answer.PlanApprovalSettings
	}
	// Recovery must retain the actual prepared bytes, even if the provider encoder changes.
	plan.resolution.Content = answer.ResolvedContent
	plan.resolution.FollowUp = agent.ControlFollowUp{Kind: answer.FollowUpKind, Text: answer.FollowUp}
	plan.controlDecision = storedControlDecision(answer)
	return plan, nil
}

// storedControlDecision decodes the decision that an answer row stores, or
// returns nil for a row that stores none.
//
// A stored decision that does not decode also gives nil, with a warning,
// rather than an error. The decision only words the answer's transcript
// row, and the agent already received the answer. An error here would stop
// the Worker from recording that answer at all, and every later retry would
// stop at the same bytes.
func storedControlDecision(answer db.ControlResponseAnswer) *leapmuxv1.ControlDecision {
	if len(answer.Decision) == 0 {
		return nil
	}
	decision := &leapmuxv1.ControlDecision{}
	if err := proto.Unmarshal(answer.Decision, decision); err != nil {
		slog.Warn("could not decode the stored control decision", "agent_id", answer.AgentID, "request_id", answer.RequestID, "error", err)
		return nil
	}
	if decision.GetKind() == nil {
		slog.Warn("the stored control decision states no kind", "agent_id", answer.AgentID, "request_id", answer.RequestID)
		return nil
	}
	return decision
}

// finalizeControlResponse commits the transcript, cancellation, and completion in one transaction.
// Provider delivery occurs before this function. No database transaction waits for provider output.
func (svc *Service) finalizeControlResponse(ctx context.Context, answer db.ControlResponseAnswer) error {
	agentID := answer.AgentID
	plan, err := controlResponsePlanFromAnswer(svc.Agents.Registry(), answer)
	if err != nil {
		return err
	}
	provider := answer.AgentProvider
	if !plan.isPlanPrompt() && !plan.exitPlanClearingContext() {
		if err := svc.recordControlResponsePlanMode(ctx, agentID, provider, plan); err != nil {
			return err
		}
	}
	var write *messageWrite
	if plan.needsTranscriptRow() {
		prompt := controlRequestPrompt(svc.Queries, svc.Agents.Registry().Plugin(provider), agentID, agent.ControlRequest{
			RequestID: plan.requestMeta.RequestID, Payload: plan.requestMeta.Payload, SourceSeq: plan.requestMeta.SourceSeq,
			AgentSessionID: plan.requestMeta.AgentSessionID, NonBlocking: plan.requestMeta.NonBlocking,
		})
		content, err := controlResponseMessageContent(plan, prompt)
		if err != nil {
			return err
		}
		prepared := svc.Output.prepareMessage(agentID, provider, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, content,
			agent.SpanInfo{MarkType: leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE}, nil)
		write = &prepared
	}
	_, _, release := svc.Output.lockControlMutation(agentID, "")
	publication := controlPublication{}
	defer func() {
		if release != nil {
			svc.Output.finishControlPublication(agentID, "", publication, release)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	var seq int64
	var removed db.ControlRequest
	apply := func(tx *sql.Tx) error {
		queries := svc.Queries.WithTx(tx)
		completed, err := completeControlDelivery(ctx, queries, answer.DeliveryID)
		if err != nil {
			return err
		}
		if !completed {
			return errControlResponseAlreadyFinalized
		}
		if write != nil {
			seq, err = createMessageRow(ctx, queries, write.params)
			if err != nil {
				return err
			}
		}
		removed, err = queries.DeleteObservedControlRequest(ctx, db.DeleteObservedControlRequestParams{
			AgentID: agentID, RequestID: answer.RequestID, ClaimToken: answer.ClaimToken,
			RequestPayload: answer.RequestPayload, AgentSessionID: answer.AgentSessionID,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	record := func() error {
		tx, err := svc.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := apply(tx); err != nil {
			return err
		}
		return tx.Commit()
	}
	if answer.FollowUpKind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED {
		err = record()
	} else {
		live, liveErr := svc.Queries.CanWatchAgentHistory(ctx, agentID)
		if liveErr != nil {
			return liveErr
		}
		if !live {
			err = record()
		} else {
			_, err = svc.InputQueue.EnqueueWithMutation(ctx, inputqueue.NewItem{
				ID: answer.InputID, AgentID: agentID, Kind: answer.FollowUpKind, Text: answer.FollowUp,
			}, func(tx *sql.Tx) error {
				live, err := svc.Queries.WithTx(tx).CanWatchAgentHistory(ctx, agentID)
				if err != nil {
					return err
				}
				if !live {
					return errRetiredControlFollowUp
				}
				return apply(tx)
			})
			if err != nil {
				live, liveErr = svc.Queries.CanWatchAgentHistory(ctx, agentID)
				if liveErr != nil {
					return errors.Join(err, liveErr)
				}
				if !live {
					err = record()
				}
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
	publication.enqueueCancellation(svc.Output, agentID, answer.RequestID, answer.ClaimToken)
	if write != nil {
		svc.Output.publishMessageWrite(*write, seq)
	}
	unlock := release
	release = nil
	svc.Output.finishControlPublication(agentID, "", publication, unlock)
	if answer.InputID != "" {
		svc.InputQueue.NotifyDependencyReady(agentID)
	}
	return nil
}

func (svc *Service) recordControlResponsePlanMode(ctx context.Context, agentID string, provider leapmuxv1.AgentProvider, plan controlResponsePlan) error {
	if plan.resolution.PlanModeControl == agent.PlanModeControlNone {
		return nil
	}
	unlock := svc.Agents.LockAgent(agentID)
	defer unlock()
	current, err := svc.Queries.GetAgentByID(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.ClosedAt.Valid || current.AgentProvider != provider || current.AgentSessionID != plan.requestMeta.AgentSessionID {
		return nil
	}
	plugin := svc.Agents.Registry().Plugin(provider)
	if plan.resolution.PlanModeControl == agent.PlanModeControlExit && plan.behavior() == contracts.ControlAnswerBehaviorAllow {
		mode := resolveTargetMode(plan.settings.GetPermissionMode(), plugin.PlanModePermissionMode(agent.PlanModeControlExit))
		if options := plugin.PlanApprovalOptions(mode); len(options) > 0 {
			_, err := svc.applyPlanOptionsLocked(current, options)
			return err
		}
		svc.handPlanExitModeToRunningProvider(agentID, mode)
	}
	return svc.applyControlResponsePlanModeMutations(current, plan)
}

// handPlanExitModeToRunningProvider gives the approved plan exit's mode to a
// running provider that defers it (see agent.PlanExitModeDeferrer). A provider
// that does not defer keeps the persist-only path. The caller holds the
// lifecycle lock, so this reads the agent without locking it again.
func (svc *Service) handPlanExitModeToRunningProvider(agentID, mode string) {
	if mode == "" {
		return
	}
	if deferrer, ok := svc.Agents.RunningAgent(agentID).(agent.PlanExitModeDeferrer); ok {
		deferrer.DeferPlanExitMode(mode)
	}
}
