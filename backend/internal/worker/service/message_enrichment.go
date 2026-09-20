package service

import (
	"bytes"
	"database/sql"
	"errors"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// EnrichMessage stores supplemental rendering data without changing the provider payload.
func (s *agentOutputSink) EnrichMessage(change agent.MessageEnrichment) (bool, error) {
	if change.SpanID == "" || change.Seq < 0 || change.PreviousRevision < 0 {
		return false, nil
	}
	ctx := bgCtx()
	var row db.Message
	var err error
	if change.Seq > 0 {
		row, err = s.h.queries.GetMessageByAgentIDAndSeq(ctx, db.GetMessageByAgentIDAndSeqParams{AgentID: s.agentID, Seq: change.Seq})
	} else {
		row, err = s.h.queries.GetLatestMessageByAgentSpanAndSource(ctx, db.GetLatestMessageByAgentSpanAndSourceParams{
			AgentID: s.agentID, AgentSessionID: s.currentMessageSessionID(), SpanID: change.SpanID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
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
	// The supplement this row already carried, which is what decides whether a
	// to-do event on the NEW supplement is a fresh one or a replay.
	var priorSupplemental []byte
	if len(row.SupplementalContent) > 0 {
		// The stored supplement carries TWO halves, and this change replaces one
		// of them. The provider payload arrives again with the next change, but
		// NOTHING rebuilds the worker's own metadata half -- duration_ms,
		// tool_uses, total_cost_usd and context_usage exist in that blob alone.
		// So a supplement that does not read is a refusal, not something to
		// overwrite: writing the new payload beside a nil metadata half destroys
		// a record the original payload cannot supply.
		var decoded agent.MessageContent
		var decodeErr error
		previous, decodeErr = msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
		if decodeErr == nil {
			decoded, decodeErr = agent.DecodeMessageSupplement(content, previous)
		}
		if decodeErr != nil {
			slog.Warn("read the stored supplement of a message to enrich",
				"agent_id", s.agentID, "span_id", change.SpanID, "seq", row.Seq, "error", decodeErr)
			return false, nil
		}
		metadata = decoded.Metadata
		priorSupplemental = decoded.Supplemental
	}
	next, err := agent.EncodeMessageSupplement(agent.MessageContent{Supplemental: change.SupplementalContent, Metadata: metadata})
	if err != nil {
		return false, err
	}
	// The same supplement, whatever order its keys arrived in. A byte compare took an
	// identical re-write for a change, raised the revision and broadcast the row again.
	if agent.JSONCanonicalEqual(previous, next) {
		return false, nil
	}
	compressed, compression := msgcodec.Compress(next)
	updated, err := s.h.queries.EnrichMessageContent(ctx, db.EnrichMessageContentParams{
		ID: row.ID, AgentID: s.agentID,
		OriginalContent: row.Content, OriginalCompression: row.ContentCompression,
		PreviousRevision:    change.PreviousRevision,
		SupplementalContent: compressed, SupplementalContentCompression: compression,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.h.broadcastMessage(s.agentID, messageToProto(&updated))
	s.applyTodoEventForEnrichment(row, content, priorSupplemental, change.SupplementalContent, metadata)
	return true, nil
}

// applyTodoEventForEnrichment feeds the to-do store a list that reached a row as a
// SUPPLEMENT rather than inside the provider's own message.
//
// Cursor is why this exists. Its `cursor/update_todos` frame states the `merge` flag,
// and nothing else on the wire does: the tool row's own `rawInput.todos` carries the
// rows that changed with no word for whether they replace the list or join it. That
// frame arrives one frame AFTER the row it describes, so it reaches the row through
// EnrichMessage. Without this call the worker would have to persist it as a row of
// its own, which would draw a second to-do card for every update.
//
// It applies an event the row did not ALREADY yield, and never one it did. The test
// is the row's PREVIOUS state -- the original content joined with the supplement the
// row already carried -- not the original alone. A supplement survives the next
// enrichment: `mergeToolSupplements` replaces only the keys that collide, so a
// Cursor `cursor/update_todos` frame written by `EnrichToolSpan` is still there when
// the turn-end store pass enriches the same row again. Testing the original alone
// found nothing both times, and the second pass re-applied the first frame's
// `merge:false` snapshot -- which deletes every row the later `merge:true` frames
// added, and reverts every status they changed.
//
// Every failure is a log line. The row is committed and broadcast by this point, and
// the transcript is what the next to-do event reconciles against.
func (s *agentOutputSink) applyTodoEventForEnrichment(row db.Message, original, priorSupplemental, supplemental, metadata []byte) {
	if row.SpanID == "" {
		return
	}
	provider := agent.ProviderFor(row.AgentProvider)
	span := agent.SpanInfo{SpanID: row.SpanID, SpanType: row.SpanType}
	paired := s.h.pairedToolUseLookup(s.agentID, span)
	// The NEW state answers first, because most enrichments state no to-do list at
	// all: an ACP tool-field update, a Cursor diff, a recovered command output. One
	// resolve and one parse then settle those, which is what the ordinary persist
	// path costs. Asking what the row had ALREADY yielded first paid for a second
	// resolve and a second parse on every one of them.
	resolved := agent.ResolveMessageContent(provider, agent.MessageContent{
		Original: original, Supplemental: supplemental, Metadata: metadata,
	})
	event, present := provider.ExtractTodoEvent(span.SpanType, resolved, paired)
	if !present {
		return
	}
	alreadyYielded := agent.ResolveMessageContent(provider, agent.MessageContent{
		Original: original, Supplemental: priorSupplemental, Metadata: metadata,
	})
	if _, present := provider.ExtractTodoEvent(span.SpanType, alreadyYielded, paired); present {
		return
	}
	// The event this function ALREADY extracted, rather than the bytes it came from.
	// applyTodoEventForMessage extracts again, behind a memo of its own, so handing
	// the bytes back costs a THIRD parse of the same row -- and a second read of the
	// paired tool_use under any extractor that asks for one.
	if err := s.h.applyExtractedTodoEvent(s.agentID, event); err != nil {
		slog.Warn("apply todo event from an enrichment",
			"agent_id", s.agentID, "span_id", row.SpanID, "span_type", row.SpanType, "error", err)
	}
}
