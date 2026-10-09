package service

import (
	"bytes"
	"database/sql"
	"errors"
	"log/slog"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
)

// EnrichMessage stores supplemental rendering data without changing the provider payload.
func (s *agentOutputSink) EnrichMessage(change agent.MessageEnrichment) (bool, error) {
	if change.SpanID == "" || change.Seq < 0 || change.PreviousRevision < 0 {
		return false, nil
	}
	change.OriginalContent = slices.Clone(change.OriginalContent)
	change.SupplementalContent = slices.Clone(change.SupplementalContent)
	if change.Publication == nil {
		captured := s.CaptureMessage(agent.MessageContent{Original: change.OriginalContent, AgentSessionID: change.AgentSessionID}, agent.SpanInfo{SpanID: change.SpanID})
		change.Publication = captured.Publication
		change.AgentSessionID = captured.AgentSessionID
	}
	var owner *transcriptOwner
	if change.Publication != nil {
		var err error
		owner, err = s.capturedOwner(agent.MessageContent{Publication: change.Publication, AgentSessionID: change.AgentSessionID})
		if err != nil {
			return false, err
		}
		if owner.span.SpanID != "" && owner.span.SpanID != change.SpanID {
			return false, nil
		}
	}
	sessionID := s.currentMessageSessionID()
	if owner != nil {
		sessionID = change.AgentSessionID
	}
	ctx := bgCtx()
	var row db.Message
	var err error
	if change.Seq > 0 {
		row, err = s.h.queries.GetMessageByAgentIDAndSeq(ctx, db.GetMessageByAgentIDAndSeqParams{AgentID: s.agentID, Seq: change.Seq})
	} else {
		row, err = s.h.queries.GetLatestMessageByAgentSpanAndSource(ctx, db.GetLatestMessageByAgentSpanAndSourceParams{
			AgentID: s.agentID, AgentSessionID: sessionID, SpanID: change.SpanID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		})
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.SpanID != change.SpanID || row.Source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
		return false, nil
	}
	if owner != nil && row.AgentSessionID != change.AgentSessionID {
		return false, nil
	}
	if row.SupplementalRevision != change.PreviousRevision {
		return false, nil
	}
	content, err := msgcodec.Decompress(row.Content, row.ContentCompression)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(content, change.OriginalContent) {
		return false, nil
	}
	if len(row.SupplementalContent) == 0 && len(change.SupplementalContent) == 0 {
		return false, nil
	}
	var previous []byte
	var metadata []byte
	var parsed *agent.ParsedMessageSupplement
	// The prior supplement identifies a to-do event that the row already supplied.
	var priorSupplemental []byte
	if len(row.SupplementalContent) > 0 {
		// Replace the provider data and preserve the Worker metadata.
		// The original payload cannot restore these metadata fields:
		//   - duration_ms.
		//   - tool_uses.
		//   - total_cost_usd.
		//   - context_usage.
		// Refuse an unreadable supplement. Replacing it would destroy those recorded values.
		var decodeErr error
		previous, decodeErr = msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
		if decodeErr == nil {
			parsed, decodeErr = agent.ParseStoredMessageSupplement(previous)
		}
		if decodeErr != nil {
			if errors.Is(decodeErr, agent.ErrInvalidNotificationStorage) {
				return false, decodeErr
			}
			slog.Warn("read the stored supplement of a message to enrich",
				"agent_id", s.agentID, "span_id", change.SpanID, "seq", row.Seq, "error", decodeErr)
			return false, nil
		}
		decoded := parsed.MessageContent(content)
		metadata = decoded.Metadata
		priorSupplemental = decoded.Supplemental
	}
	if parsed == nil {
		parsed, err = agent.ParseStoredMessageSupplement(previous)
		if err != nil {
			return false, err
		}
	}
	messageCount := 0
	if parsed.HasNotificationReduction() {
		wrapper, err := unwrapNotifContent(content)
		if err != nil || wrapper.Type != notifThreadWrapperType {
			return false, errors.New("the stored notification reduction has no matching thread")
		}
		messageCount = len(wrapper.Messages)
	}
	next, err := parsed.ReplaceProvider(change.SupplementalContent, messageCount)
	if err != nil {
		return false, err
	}
	// Canonical comparison ignores key order and prevents another revision for equal JSON.
	if agent.JSONCanonicalEqual(previous, next) {
		return false, nil
	}
	var todoEvent *todoevents.Event
	if !row.TranscriptOnly && (owner == nil || owner.IsCurrent()) {
		todoEvent = s.prepareTodoEventForEnrichment(row, content, priorSupplemental, change.SupplementalContent, metadata)
	}
	compressed, compression := msgcodec.Compress(next)
	prospective := row
	prospective.SupplementalContent, prospective.SupplementalContentCompression = compressed, compression
	message, err := messageToProto(&prospective)
	if err != nil {
		return false, err
	}
	mutation := s.h.transcriptMutationMutex(s.rootAgentID)
	mutation.RLock()
	var todoApplyError error
	defer func() {
		mutation.RUnlock()
		if todoApplyError != nil {
			slog.Warn("apply todo event from an enrichment",
				"agent_id", s.agentID, "span_id", row.SpanID, "span_type", row.SpanType, "error", todoApplyError)
		}
		s.h.watcher.DrainAgentEvents(s.agentID)
	}()
	updated, err := s.h.queries.EnrichMessageContent(ctx, db.EnrichMessageContentParams{
		ID: row.ID, AgentID: s.agentID,
		OriginalContent: row.Content, OriginalCompression: row.ContentCompression,
		PreviousRevision:    change.PreviousRevision,
		SupplementalContent: compressed, SupplementalContentCompression: compression,
		PreviousSupplementalContent:            row.SupplementalContent,
		PreviousSupplementalContentCompression: row.SupplementalContentCompression,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	message.SupplementalRevision = updated.SupplementalRevision
	change.WriteReceipt.RecordStoredEnrichment(change.PreviousRevision, updated.SupplementalRevision, change.SupplementalContent)
	if owner != nil {
		owner.enqueueMessage(message)
	} else {
		s.h.enqueueMessage(s.agentID, message)
	}
	if !row.TranscriptOnly && (owner == nil || owner.IsCurrent()) {
		todoApplyError = s.applyTodoEventForEnrichment(row, todoEvent)
	}
	return true, nil
}

// prepareTodoEventForEnrichment derives a to-do event from new rendering data.
// Cursor's cursor/update_todos frame carries the merge flag after the tool row arrives.
// Its rawInput.todos field lacks that flag, so the later frame enriches the original row.
// A separate transcript row would draw a second to-do card for the same update.
//
// Compare the new resolved content with the prior resolved content before the CAS.
// The prior content includes the stored supplement, which later enrichment preserves.
// Comparing only the original would apply a prior merge:false snapshot again.
// That repeated snapshot would delete items and revert statuses from later merge:true updates.
func (s *agentOutputSink) prepareTodoEventForEnrichment(row db.Message, original, priorSupplemental, supplemental, metadata []byte) *todoevents.Event {
	if row.SpanID == "" {
		return nil
	}
	provider := s.h.agents.Registry().Plugin(row.AgentProvider)
	span := agent.SpanInfo{SpanID: row.SpanID, SpanType: row.SpanType}
	paired := s.h.pairedToolUseLookup(s.agentID, row.AgentSessionID, span)
	// Read the new state first. Most enrichments contain no to-do event.
	// Only an extracted event requires another resolve and parse of the prior state.
	resolved := agent.ResolveMessageContent(provider, agent.MessageContent{
		Original: original, Supplemental: supplemental, Metadata: metadata,
	}.Clone())
	event, present := provider.ExtractTodoEvent(span.SpanType, resolved, paired)
	if !present {
		return nil
	}
	alreadyYielded := agent.ResolveMessageContent(provider, agent.MessageContent{
		Original: original, Supplemental: priorSupplemental, Metadata: metadata,
	}.Clone())
	if _, present := provider.ExtractTodoEvent(span.SpanType, alreadyYielded, paired); present {
		return nil
	}
	frozen := cloneTodoEvent(event)
	return &frozen
}

// applyTodoEventForEnrichment applies the frozen result under current-owner admission.
// The caller reports a failure after it releases the mutation lease.
func (s *agentOutputSink) applyTodoEventForEnrichment(row db.Message, event *todoevents.Event) error {
	if event == nil {
		return nil
	}
	// Apply the prepared event. Extracting it again would repeat parsing and the paired tool request read.
	items, changed, err := s.h.applyTodoEvent(s.agentID, *event)
	if err != nil {
		return err
	}
	if changed {
		s.h.enqueueTodoChange(s.agentID, items)
	}
	return nil
}
