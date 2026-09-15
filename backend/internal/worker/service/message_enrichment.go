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
	}
	next, err := agent.EncodeMessageSupplement(agent.MessageContent{Supplemental: change.SupplementalContent, Metadata: metadata})
	if err != nil {
		return false, err
	}
	if bytes.Equal(previous, next) {
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
	return true, nil
}
