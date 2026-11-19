package kimi

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// kimiConversationRecordWriter keeps one immutable row per native identity.
// Ordered captures retain this decorated writer, so retries use the same record
// and destination. Later views change rendering data, not the original bytes.
type kimiConversationRecordWriter struct {
	agent.ProviderServices
	queue *agent.OutputQueue
}

func newKimiConversationRecordWriter(sink agent.ProviderServices) (*kimiConversationRecordWriter, error) {
	if sink == nil {
		return nil, errors.New("the Kimi conversation projection has no destination")
	}
	queue := agent.OutputQueueFor(sink)
	direct, valid := agent.DirectTranscript(sink).(agent.ProviderServices)
	if queue == nil || !valid {
		return nil, errors.New("the Kimi conversation projection has no ordered provider destination")
	}
	return &kimiConversationRecordWriter{ProviderServices: direct, queue: queue}, nil
}

func (writer *kimiConversationRecordWriter) OutputQueue() *agent.OutputQueue {
	return writer.queue
}

func (writer *kimiConversationRecordWriter) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	view, err := decodeKimiConversationRecord(content.Original)
	if err != nil {
		return err
	}
	if view.record.Type != contracts.KimiConversationRecordKindItem {
		return errors.New("the Kimi message projection received another record kind")
	}
	if source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT &&
		(source != leapmuxv1.MessageSource_MESSAGE_SOURCE_USER || view.item.Type != contracts.KimiConversationItemKindUserMessage ||
			view.record.Role == contracts.KimiConversationRecordRoleOrigin) {
		return errors.New("the Kimi conversation projection received another message source")
	}
	return writer.persistView(view, content, span, func() error {
		return writer.ProviderServices.PersistMessage(source, content, span)
	})
}

func (writer *kimiConversationRecordWriter) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	view, err := decodeKimiConversationRecord(content.Original)
	if err != nil {
		return err
	}
	if view.record.Type != contracts.KimiConversationRecordKindTurn {
		return errors.New("the Kimi completion projection received another record kind")
	}
	return writer.persistView(view, content, span, func() error {
		return writer.ProviderServices.PersistTurnEnd(content, span)
	})
}

func kimiConversationProviderFields(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return make(map[string]json.RawMessage), nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errors.New("the Kimi conversation rendering data is not an object")
	}
	return fields, nil
}

// persistView runs in the shared output queue. A conditional-write conflict
// returns an error so that queue retains the captured view for another drain.
func (writer *kimiConversationRecordWriter) persistView(view kimiConversationRecordView, content agent.MessageContent, span agent.SpanInfo, firstWrite func() error) error {
	if content.AgentSessionID != view.record.SessionID || content.IdempotencyKey != view.key() {
		return errors.New("the captured Kimi record has another native session or record key")
	}
	stored, err := writer.ReadMessageByIdempotencyKey(view.record.SessionID, content.IdempotencyKey)
	if err != nil {
		return err
	}
	if stored == nil {
		return firstWrite()
	}
	original, err := decodeKimiConversationRecord(stored.Content.Original)
	if err != nil || !original.acceptsView(view) || stored.Content.AgentSessionID != view.record.SessionID || stored.Content.IdempotencyKey != content.IdempotencyKey {
		return errors.New("the stored Kimi record has another native identity")
	}
	if view.record.Role == contracts.KimiConversationRecordRoleRequest || view.item != nil && view.item.Type == contracts.KimiConversationItemKindUserMessage {
		writer.noteExistingRecord(content, stored.Seq)
		return nil
	}
	fields, err := kimiConversationProviderFields(stored.Content.Supplemental)
	if err != nil {
		return err
	}
	current := original
	if latest := fields[contracts.KimiConversationSupplementRoot]; len(latest) != 0 {
		current, err = decodeKimiConversationRecord(latest)
		if err != nil || !original.acceptsView(current) {
			return errors.New("the stored Kimi rendering data has another native identity")
		}
	}
	if !current.acceptsView(view) {
		return errors.New("the Kimi rendering view removes or changes accepted native context")
	}
	// The receipt time is fixed by the immutable anchor. A later subscription's
	// timestamp alone does not change the native item or cause a rendering update.
	view.record.EventCreatedAt = original.record.EventCreatedAt
	current.record.EventCreatedAt = original.record.EventCreatedAt
	nextRecord, err := json.Marshal(view.record)
	if err != nil {
		return err
	}
	currentRecord, err := json.Marshal(current.record)
	if err != nil {
		return err
	}
	if !agent.JSONCanonicalEqual(currentRecord, nextRecord) {
		fields[contracts.KimiConversationSupplementRoot] = nextRecord
	}
	incoming, err := kimiConversationProviderFields(content.Supplemental)
	if err != nil {
		return err
	}
	for field, value := range incoming {
		if field == contracts.KimiConversationSupplementRoot {
			candidate, decodeErr := decodeKimiConversationRecord(value)
			if decodeErr != nil || !original.acceptsView(candidate) {
				return errors.New("the captured Kimi rendering data has another native identity")
			}
			continue
		}
		fields[field] = slices.Clone(value)
	}
	next, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	previous := stored.Content.Supplemental
	if len(previous) == 0 {
		previous = []byte(`{}`)
	}
	if !agent.JSONCanonicalEqual(previous, next) || content.Completion != "" && content.Completion != stored.Content.Completion {
		written, enrichErr := writer.EnrichMessage(agent.MessageEnrichment{
			Publication: content.Publication, AgentSessionID: content.AgentSessionID, Seq: stored.Seq, SpanID: span.SpanID,
			OriginalContent: stored.Content.Original, PreviousRevision: stored.Revision, SupplementalContent: next,
			Completion: content.Completion,
		})
		if enrichErr != nil {
			return enrichErr
		}
		if !written {
			return fmt.Errorf("the Kimi rendering revision changed before its captured update")
		}
	}
	writer.noteExistingRecord(content, stored.Seq)
	return nil
}

func (writer *kimiConversationRecordWriter) noteExistingRecord(content agent.MessageContent, seq int64) {
	content.WriteReceipt.RecordStoredMessage(seq)
	content.WriteReceipt.RecordStoredWrite(false)
}
