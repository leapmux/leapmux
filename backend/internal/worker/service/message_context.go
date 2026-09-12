package service

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func (s *agentOutputSink) ReadToolRequest(spanID string) (*agent.StoredMessage, error) {
	return s.h.readToolRequest(s.agentID, spanID)
}

// readToolRequest is the shared lookup for provider controls and to-do extraction.
func (h *OutputHandler) readToolRequest(agentID, spanID string) (*agent.StoredMessage, error) {
	if spanID == "" {
		return nil, nil
	}
	row, err := h.queries.GetAgentMessageBySpanIDAndSource(bgCtx(), db.GetAgentMessageBySpanIDAndSourceParams{
		AgentID: agentID, SpanID: spanID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tool request: %w", err)
	}
	original, err := msgcodec.Decompress(row.Content, row.ContentCompression)
	if err != nil {
		return nil, fmt.Errorf("decode tool request: %w", err)
	}
	content := agent.MessageContent{Original: original}
	if len(row.SupplementalContent) > 0 {
		supplemental, decodeErr := msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
		if decodeErr == nil {
			content, decodeErr = agent.DecodeMessageSupplement(original, supplemental)
		}
		if decodeErr != nil {
			slog.Warn("decode tool request supplement", "agent_id", agentID, "span_id", spanID, "error", decodeErr)
		}
	}
	return &agent.StoredMessage{Content: content, Seq: row.Seq, Revision: row.SupplementalRevision, Provider: row.AgentProvider}, nil
}
