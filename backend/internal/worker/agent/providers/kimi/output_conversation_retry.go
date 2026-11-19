package kimi

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// Settle the retry after the captured turn record and before releasing its
// activity scope. The captured writer refuses a superseded scope.
func (a *Agent) settleConversationServerErrorRetry(writer agent.AutoRetryWriter, turn kimiConversationTurnHeader, payload []byte) {
	retry := turn.Outcome == contracts.KimiConversationOutcomeFailed && turn.EndReason != contracts.KimiConversationEndReasonBlocked &&
		turn.Error != nil && turn.Error.Retryable != nil && *turn.Error.Retryable
	err := a.queueProviderWrite("settle the Kimi server-error retry", func() error {
		providerkit.ScheduleOrCancelServerErrorAutoRetry(writer, retry, payload)
		return nil
	})
	if !agent.OutputRetained(err) {
		slog.Warn("retain the Kimi server-error retry decision", "error", err)
	}
}

type kimiConversationRetryOutput struct {
	nextAttempt int64
	key         string
	pending     bool
}

// Native step indices identify requests within one exact native turn. Attempts
// of that request reuse its index, including the completed retry's step summary.
func (a *Agent) observeConversationRetry(run *kimiRun, turn *kimiConversationTurnState, publication *kimiConversationTurnOutput, retryState *kimiConversationRetry, eventCreatedAt string) {
	if retryState == nil {
		return
	}
	var index int64
	for candidate := range turn.steps {
		index = max(index, candidate)
	}
	if index == 0 || turn.steps[index].Outcome != contracts.KimiConversationOutcomeFailed {
		return
	}
	if publication.retries == nil {
		publication.retries = make(map[int64]*kimiConversationRetryOutput)
	}
	previous := publication.retries[index]
	if previous != nil && previous.nextAttempt >= retryState.NextAttempt {
		return
	}
	retry, err := json.Marshal(retryState)
	if err != nil {
		slog.Warn("encode the Kimi retry state", "error", err)
		return
	}
	record := contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindRetry, SessionID: run.conversation.document.sessionID, AgentID: run.agentID, TurnID: turn.header.TurnID, StepIndex: index, Retry: retry, EventCreatedAt: eventCreatedAt}
	raw, err := encodeKimiConversationRecord(record)
	if err != nil {
		slog.Warn("encode the Kimi retry record", "error", err)
		return
	}
	view := kimiConversationRecordView{record: record, retry: retryState}
	key := view.key()
	captured, err := publication.scope.CaptureTranscript(agent.MessageContent{Original: raw, AgentSessionID: run.conversation.document.sessionID, IdempotencyKey: key, Completion: agent.MessageCompletionComplete}, agent.SpanInfo{NoSpan: true})
	if err != nil {
		slog.Warn("capture the Kimi retry notification", "error", err)
		return
	}
	err = a.queueProviderWrite("publish the native Kimi retry", func() error {
		_, err := captured.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
		return err
	})
	if agent.OutputRetained(err) {
		publication.retries[index] = &kimiConversationRetryOutput{nextAttempt: retryState.NextAttempt, key: key, pending: true}
	} else {
		slog.Warn("retain the Kimi retry notification", "error", err)
	}
}

func (a *Agent) resolveConversationRetry(publication *kimiConversationTurnOutput, index int64) {
	retry := publication.retries[index]
	if retry == nil || !retry.pending {
		return
	}
	raw, err := json.Marshal(map[string]string{contracts.NotificationFieldType: contracts.NotificationTypeApiRetryResolved})
	if err != nil {
		slog.Warn("encode the Kimi retry resolution", "error", err)
		return
	}
	captured, err := publication.scope.CaptureTranscript(agent.MessageContent{Original: raw, IdempotencyKey: retry.key + "/resolved", Completion: agent.MessageCompletionComplete}, agent.SpanInfo{NoSpan: true})
	if err != nil {
		slog.Warn("capture the Kimi retry resolution", "error", err)
		return
	}
	err = a.queueProviderWrite("resolve the native Kimi retry", func() error { _, err := captured.PersistWorkerNotification(); return err })
	if agent.OutputRetained(err) {
		retry.pending = false
	} else {
		slog.Warn("retain the Kimi retry resolution", "error", err)
	}
}

// The native feed publishes items before the completed step summary. Resolve
// before that output ends the notification sequence containing the retry.
func (a *Agent) resolveConversationRetryFromOutput(publication *kimiConversationTurnOutput, turn *kimiConversationTurnState, item *kimiConversationItemState, frame kimiConversationFrame) {
	if turn.header.Retry != nil || item.data == nil || item.identity.stepIndex == nil {
		return
	}
	switch item.identity.kind {
	case contracts.KimiConversationItemKindAssistantMessage, contracts.KimiConversationItemKindReasoning:
		if item.text == nil || item.text.Len() == 0 {
			return
		}
	case contracts.KimiConversationItemKindToolCall:
		liveInput := frame.Type == contracts.KimiFrameItemDelta && frame.Field == "input_text" &&
			frame.Delta != nil && *frame.Delta != ""
		if !liveInput && (item.data.Name == nil || *item.data.Name == "") {
			return
		}
	default:
		return
	}
	a.resolveConversationRetry(publication, *item.identity.stepIndex)
}
