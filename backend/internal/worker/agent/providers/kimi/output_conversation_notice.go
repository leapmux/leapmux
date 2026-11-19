package kimi

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func (a *Agent) publishConversationNotice(run *kimiRun, frame kimiConversationFrame) {
	notice := kimiConversationNotice{Level: frame.Level, Title: frame.Title, Detail: frame.Detail}
	if len(frame.Code) != 0 {
		if err := json.Unmarshal(frame.Code, &notice.Code); err != nil {
			slog.Warn("decode the Kimi notice code", "error", err)
			return
		}
	}
	data, err := json.Marshal(notice)
	if err != nil {
		slog.Warn("encode the Kimi native notice", "error", err)
		return
	}
	a.publishConversationNotification(run, contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindNotice,
		SessionID: frame.SessionID, AgentID: run.agentID, EventCreatedAt: frame.EventCreatedAt, Notice: data})
}

func (a *Agent) publishConversationFailure(run *kimiRun, frame kimiConversationFrame, raw json.RawMessage) {
	a.publishConversationNotification(run, contracts.KimiConversationRecord{Type: contracts.KimiConversationRecordKindFailure,
		SessionID: frame.SessionID, AgentID: run.agentID, EventCreatedAt: frame.EventCreatedAt, Failure: raw})
}

// Notices and subscription failures have no native turn identity and do not
// replay with conversation pages. Capture their destination before queuing.
func (a *Agent) publishConversationNotification(run *kimiRun, record contracts.KimiConversationRecord) {
	sink := a.nativeStateSink(run)
	if sink == nil {
		return
	}
	direct, valid := agent.DirectTranscript(sink).(agent.ProviderServices)
	if !valid {
		slog.Warn("capture the Kimi notification destination", "agent_id", a.AgentID(), "native_agent_id", run.agentID)
		return
	}
	raw, err := encodeKimiConversationRecord(record)
	if err != nil {
		slog.Warn("encode the Kimi conversation notification", "error", err)
		return
	}
	// The feed has no event ID or replay. Equal payloads are distinct deliveries;
	// only retries of this captured write share its Worker receipt.
	captured := agent.CaptureTranscript(direct, agent.MessageContent{Original: raw, AgentSessionID: record.SessionID, IdempotencyKey: "kimi:notice-receipt:" + id.Generate(), Completion: agent.MessageCompletionComplete}, agent.SpanInfo{NoSpan: true})
	err = a.queueProviderWrite("publish the Kimi conversation notification", func() error {
		_, writeErr := captured.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
		return writeErr
	})
	if agent.OutputRetained(err) {
		a.queueIdleStateCleanup(run)
	} else {
		slog.Warn("retain the Kimi conversation notification", "error", err)
	}
}
