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
	return s.h.readToolRequest(s.agentID, s.currentMessageSessionID(), spanID)
}

func (s *agentOutputSink) ReadToolResult(spanID string) (*agent.StoredMessage, error) {
	return s.h.readToolResult(s.agentID, s.currentMessageSessionID(), spanID)
}

func (s *agentOutputSink) restoreMessageSession() {
	if s.h.queries == nil {
		return
	}
	row, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("Read the provider session for the transcript", "agent_id", s.agentID, "error", err)
		}
		return
	}
	s.messageSessionID = row.AgentSessionID
}

func (s *agentOutputSink) currentMessageSessionID() string {
	s.messageSessionMu.RLock()
	defer s.messageSessionMu.RUnlock()
	return s.messageSessionID
}

func (s *agentOutputSink) scopeMessage(content agent.MessageContent) agent.MessageContent {
	if content.AgentSessionID == "" {
		content.AgentSessionID = s.currentMessageSessionID()
	}
	return content
}

func (h *OutputHandler) messageSessionID(agentID string) string {
	if sink := h.sinkForAgent(agentID); sink != nil {
		return sink.currentMessageSessionID()
	}
	return ""
}

// readToolRequest is the shared lookup for provider controls and to-do extraction.
func (h *OutputHandler) readToolRequest(agentID, sessionID, spanID string) (*agent.StoredMessage, error) {
	if spanID == "" {
		return nil, nil
	}
	row, err := h.queries.GetAgentMessageBySpanIDAndSource(bgCtx(), db.GetAgentMessageBySpanIDAndSourceParams{
		AgentID: agentID, AgentSessionID: sessionID, SpanID: spanID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
	})
	return storedSpanMessage(agentID, spanID, "request", row, err)
}

// readToolResult is the sibling lookup for the LAST row of a span -- the row that
// carries the tool's result, and the row every result supplement enriches.
func (h *OutputHandler) readToolResult(agentID, sessionID, spanID string) (*agent.StoredMessage, error) {
	if spanID == "" {
		return nil, nil
	}
	row, err := h.queries.GetLatestMessageByAgentSpanAndSource(bgCtx(), db.GetLatestMessageByAgentSpanAndSourceParams{
		AgentID: agentID, AgentSessionID: sessionID, SpanID: spanID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
	})
	return storedSpanMessage(agentID, spanID, "result", row, err)
}

// storedSpanMessage decodes one message row for the two span reads above.
//
// kind identifies the read in a log line and in an error, so a failure states WHICH of the
// two rows of a span could not be read.
func storedSpanMessage(agentID, spanID, kind string, row db.Message, err error) (*agent.StoredMessage, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read tool %s: %w", kind, err)
	}
	original, err := msgcodec.Decompress(row.Content, row.ContentCompression)
	if err != nil {
		return nil, fmt.Errorf("decode tool %s: %w", kind, err)
	}
	content := agent.MessageContent{Original: original}
	if len(row.SupplementalContent) > 0 {
		supplemental, decodeErr := msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
		if decodeErr == nil {
			content, decodeErr = agent.DecodeMessageSupplement(original, supplemental)
		}
		if decodeErr != nil {
			slog.Warn("decode tool "+kind+" supplement", "agent_id", agentID, "span_id", spanID, "error", decodeErr)
		}
	}
	content.AgentSessionID = row.AgentSessionID
	return &agent.StoredMessage{Content: content, Seq: row.Seq, Revision: row.SupplementalRevision, Provider: row.AgentProvider}, nil
}
