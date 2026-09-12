package service

import (
	"bytes"
	"database/sql"
	"errors"

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
			AgentID: s.agentID, SpanID: change.SpanID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
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
		// A damaged supplement can be replaced because the original payload remains intact.
		var decodeErr error
		previous, decodeErr = msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
		if decodeErr == nil {
			if decoded, err := agent.DecodeMessageSupplement(content, previous); err == nil {
				metadata = decoded.Metadata
			}
		}
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
