package kimi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func (a *Agent) observeExactPrompt(run *kimiRun, turn *kimiConversationTurnState, publication *kimiConversationTurnOutput, submission *kimiPromptSubmission, item kimiConversationItem) {
	if !submission.tracked || run.agentID != kimiMainAgentID || submission.conflictingConsumption {
		return
	}
	if submission.turnID != "" {
		if submission.turnID != turn.header.TurnID || submission.itemID != item.ItemID {
			submission.conflictingConsumption = true
			a.retireKimiInput(submission.receipt(), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
		}
		return
	}
	submission.turnID, submission.itemID = turn.header.TurnID, item.ItemID
	submission.consumingOpener = item.Placement == "opener"
	if publication.replyInputs == nil {
		publication.replyInputs = make(map[string]*kimiPromptSubmission)
	}
	publication.replyInputs[submission.id] = submission
	if item.Placement == "opener" {
		sink, receipt, turnID := agent.DirectProviderServices(a.sink), submission.receipt(), turn.header.TurnID
		err := a.queueProviderWrite("bind the Kimi input turn", func() error {
			return sink.BindNativeInputTurn(receipt, turnID)
		})
		if !agent.OutputRetained(err) {
			slog.Warn("retain the Kimi input binding", "error", err)
		}
	}
}

func (a *Agent) retireKimiInput(receipt agent.NativeInputReceipt, outcome leapmuxv1.AgentInputTurnOutcome) {
	sink := agent.DirectProviderServices(a.sink)
	err := a.queueProviderWrite("retire the Kimi input reply", func() error {
		return sink.RetireNativeInputTurn(receipt, outcome)
	})
	if !agent.OutputRetained(err) {
		slog.Warn("retain the Kimi input outcome", "error", err)
	}
}

func (a *Agent) scheduleExactReply(run *kimiRun, turn *kimiConversationTurnState, publication *kimiConversationTurnOutput) {
	if run.agentID != kimiMainAgentID || turn.header.Kind != contracts.KimiConversationTurnKindAgent ||
		turn.header.Status != contracts.KimiConversationStatusEnded || publication.replyLookupStarted ||
		len(publication.replyInputs) == 0 || a.api == nil {
		return
	}
	publication.replyLookupStarted = true
	sessionID, header := run.conversation.document.sessionID, turn.header
	a.transcriptRecoveryWork.Run("input-reply/"+sessionID+"/"+header.TurnID, func() {
		ctx, cancel := context.WithTimeout(a.Context(), kimiSendWait)
		defer cancel()
		snapshot, err := a.readExactReplyTurn(ctx, sessionID, header)
		a.dispatchMu.Lock()
		defer a.finishDispatch()
		if !a.ownsNativeSession(sessionID) || a.IsDiscardingOutput() ||
			run.conversation == nil || run.conversation.turns[header.TurnID] != publication {
			return
		}
		if len(publication.replyInputs) == 0 {
			return
		}
		current := run.conversation.document.turns[header.TurnID]
		if current == nil || !sameKimiConversationTurnIdentity(header, current.header) {
			a.retireExactReplyInputs(publication)
			return
		}
		if run.conversationRecovery.Load() != nil {
			publication.replyLookupStarted = false
			return
		}
		if err == nil {
			err = a.publishExactReply(run, publication, sessionID, snapshot)
		}
		if err != nil {
			slog.Warn("prove the Kimi input reply", "turn_id", header.TurnID, "error", err)
			a.retireExactReplyInputs(publication)
		}
	})
}

func (a *Agent) readExactReplyTurn(ctx context.Context, sessionID string, header kimiConversationTurnHeader) (kimiConversationTurn, error) {
	if header.Ordinal == nil || *header.Ordinal == math.MaxInt64 {
		return kimiConversationTurn{}, errors.New("the Kimi turn has no usable history ordinal")
	}
	query := url.Values{"before_ordinal": {strconv.FormatInt(*header.Ordinal+1, 10)}, "page_size": {"1"}}
	var page kimiConversationPage
	if err := a.api.callQuery(ctx, "GET", kimiSessionPath(sessionID, "/agents/"+kimiMainAgentID+"/turns"), query, nil, &page); err != nil {
		return kimiConversationTurn{}, err
	}
	if err := validateKimiConversationPage(page.Turns, page.HasMore); err != nil {
		return kimiConversationTurn{}, err
	}
	if len(page.Turns) != 1 || !sameKimiConversationTurnIdentity(header, page.Turns[0].kimiConversationTurnHeader) ||
		page.Turns[0].Status != contracts.KimiConversationStatusEnded {
		return kimiConversationTurn{}, errors.New("the Kimi history did not contain the exact ended turn")
	}
	return page.Turns[0], nil
}

func (a *Agent) publishExactReply(run *kimiRun, publication *kimiConversationTurnOutput, sessionID string, snapshot kimiConversationTurn) error {
	// Project this turn independently. A narrow REST page does not replace
	// the main conversation or displace a newer live turn.
	document := newKimiConversationState(sessionID, run.agentID)
	if existing := run.conversation.document.turns[snapshot.TurnID]; existing != nil {
		document.turns[snapshot.TurnID] = existing
	}
	if err := document.replaceSnapshot([]kimiConversationTurn{snapshot}); err != nil {
		return err
	}
	turn := document.turns[snapshot.TurnID]
	frame := kimiConversationFrame{SessionID: sessionID, AgentID: run.agentID, EventCreatedAt: snapshot.EndedAt}
	a.projectConversationTurn(run, turn, frame)
	if turn.projectionPending {
		return errors.New("the Worker could not retain the authoritative Kimi transcript items")
	}
	reply, missing, err := exactReplyProjection(sessionID, snapshot, publication.replyInputs)
	if err != nil {
		return err
	}
	if len(reply.Inputs) != 0 {
		sink := agent.DirectProviderServices(a.sink)
		err := a.queueProviderWrite("publish the Kimi reply projection", func() error { return sink.PublishNativeReplyTurn(reply) })
		if !agent.OutputRetained(err) {
			return err
		}
	}
	for _, submission := range missing {
		a.retireKimiInput(submission.receipt(), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	a.forgetExactReplyInputs(publication)
	return nil
}

func exactReplyProjection(sessionID string, snapshot kimiConversationTurn, inputs map[string]*kimiPromptSubmission) (agent.NativeReplyTurn, []*kimiPromptSubmission, error) {
	reply := agent.NativeReplyTurn{SessionID: sessionID, TurnID: snapshot.TurnID}
	switch snapshot.Outcome {
	case contracts.KimiConversationOutcomeCompleted:
		reply.Outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_COMPLETED
	case contracts.KimiConversationOutcomeInterrupted:
		reply.Outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_INTERRUPTED
	case contracts.KimiConversationOutcomeFailed:
		reply.Outcome = leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_FAILED
	default:
		return reply, nil, fmt.Errorf("the Kimi turn has no verified outcome %q", snapshot.Outcome)
	}
	found := make(map[string]int64, len(inputs))
	for index, item := range snapshot.Items {
		ordinal := int64(index) + 1
		if item.Type == contracts.KimiConversationItemKindUserMessage {
			for _, promptID := range item.PromptIDs {
				if submission := inputs[promptID]; submission != nil && !submission.conflictingConsumption &&
					submission.sessionID == sessionID && submission.turnID == snapshot.TurnID {
					if submission.itemID == item.ItemID && found[promptID] >= 0 {
						found[promptID] = ordinal
					} else {
						found[promptID] = -1
					}
				}
			}
		}
		if item.Type == contracts.KimiConversationItemKindAssistantMessage {
			view := kimiConversationRecordView{
				record: contracts.KimiConversationRecord{
					Type: contracts.KimiConversationRecordKindItem, SessionID: sessionID, AgentID: kimiMainAgentID,
					TurnID: snapshot.TurnID, Role: contracts.KimiConversationRecordRoleItem,
				},
				item: &item,
			}
			reply.Items = append(reply.Items, agent.NativeReplyItem{Ordinal: ordinal, MessageKey: view.key()})
		}
	}
	var missing []*kimiPromptSubmission
	for id, submission := range inputs {
		if ordinal := found[id]; ordinal > 0 {
			reply.Inputs = append(reply.Inputs, agent.NativeReplyBoundary{InputID: id, AfterOrdinal: ordinal})
		} else {
			missing = append(missing, submission)
		}
	}
	return reply, missing, nil
}

func (a *Agent) retireExactReplyInputs(publication *kimiConversationTurnOutput) {
	for _, submission := range publication.replyInputs {
		a.retireKimiInput(submission.receipt(), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
	}
	a.forgetExactReplyInputs(publication)
}

func (a *Agent) retireKimiSessionReplies(sessionID string) {
	for id, submission := range a.promptSubmissions {
		if submission.tracked && submission.sessionID == sessionID {
			a.retireKimiInput(submission.receipt(), leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNPROVED)
			delete(a.promptSubmissions, id)
		}
	}
}

func (a *Agent) forgetExactReplyInputs(publication *kimiConversationTurnOutput) {
	for id, submission := range publication.replyInputs {
		if a.promptSubmissions[id] == submission {
			delete(a.promptSubmissions, id)
		}
	}
	publication.replyInputs = nil
}
