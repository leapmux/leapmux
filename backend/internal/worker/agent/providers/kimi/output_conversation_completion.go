package kimi

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

type kimiConversationCompletionCorrection struct {
	sessionID, agentID, turnID, itemID string
	stepIndex                          int64
	completion                         agent.MessageCompletion
	publication                        agent.TranscriptPublication
}

func (a *Agent) correctConversationStepCompletion(publication *kimiConversationTurnOutput, frame kimiConversationFrame, turnID string, step kimiConversationStep) {
	if _, generated := publication.executionSteps[step.Index]; generated {
		return
	}
	writer := publication.writer
	for _, itemID := range slices.Sorted(maps.Keys(publication.textRecords[step.Index])) {
		correction := kimiConversationCompletionCorrection{sessionID: frame.SessionID, agentID: frame.AgentID,
			turnID: turnID, itemID: itemID, stepIndex: step.Index,
			completion: kimiConversationCompletion(step.Outcome), publication: publication.progress}
		err := a.queueProviderWrite("correct the Kimi text completion", func() error { return writer.correctTextCompletion(correction) })
		if !agent.OutputRetained(err) {
			slog.Warn("retain the Kimi text completion", "error", err)
		}
	}
}

// The queue retains the exact destination, item identity and observed step
// outcome. Looking up the row here preserves payload release after admission.
func (writer *kimiConversationRecordWriter) correctTextCompletion(correction kimiConversationCompletionCorrection) error {
	if correction.publication == nil || correction.stepIndex <= 0 || correction.sessionID == "" || correction.agentID == "" || correction.turnID == "" || correction.itemID == "" {
		return fmt.Errorf("the Kimi text completion correction has no complete native owner")
	}
	identity := kimiConversationRecordView{record: contracts.KimiConversationRecord{
		Type: contracts.KimiConversationRecordKindItem, SessionID: correction.sessionID, AgentID: correction.agentID,
		TurnID: correction.turnID, Role: contracts.KimiConversationRecordRoleItem}, item: &kimiConversationItem{ItemID: correction.itemID}}
	stored, err := writer.ReadMessageByIdempotencyKey(correction.sessionID, identity.key())
	if err != nil {
		return err
	}
	if stored == nil {
		return fmt.Errorf("the retained Kimi text record is missing")
	}
	if stored.Content.AgentSessionID != correction.sessionID || stored.Content.IdempotencyKey != identity.key() {
		return fmt.Errorf("the retained Kimi text record has another native session or key")
	}
	view, err := decodeKimiConversationRecord(stored.Content.Original)
	if err != nil {
		return err
	}
	fields, err := kimiConversationProviderFields(stored.Content.Supplemental)
	if err != nil {
		return err
	}
	current := view
	if latest := fields[contracts.KimiConversationSupplementRoot]; len(latest) != 0 {
		current, err = decodeKimiConversationRecord(latest)
		if err != nil || !view.acceptsView(current) {
			return fmt.Errorf("the Kimi text rendering data has another identity")
		}
	}
	if current.record.SessionID != correction.sessionID || current.record.AgentID != correction.agentID ||
		current.record.TurnID != correction.turnID || current.record.Role != contracts.KimiConversationRecordRoleItem ||
		current.item == nil || current.item.ItemID != correction.itemID ||
		current.item.Type != contracts.KimiConversationItemKindAssistantMessage && current.item.Type != contracts.KimiConversationItemKindReasoning ||
		current.item.StepIndex != nil && *current.item.StepIndex != correction.stepIndex {
		return fmt.Errorf("the retained Kimi text record belongs to another native item or step")
	}
	if stored.Content.Completion == correction.completion {
		return nil
	}
	written, err := writer.EnrichMessage(agent.MessageEnrichment{Publication: correction.publication,
		AgentSessionID: correction.sessionID, Seq: stored.Seq, OriginalContent: stored.Content.Original,
		PreviousRevision: stored.Revision, SupplementalContent: stored.Content.Supplemental, Completion: correction.completion})
	if err != nil {
		return err
	}
	if !written {
		return fmt.Errorf("the retained Kimi text revision changed before its completion correction")
	}
	return nil
}
