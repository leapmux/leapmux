package service

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func (s *agentOutputSink) ReadMessageByIdempotencyKey(sessionID, idempotencyKey string) (*agent.StoredMessage, error) {
	if idempotencyKey == "" {
		return nil, nil
	}
	row, err := s.h.queries.GetMessageByIdempotencyKey(bgCtx(), db.GetMessageByIdempotencyKeyParams{
		AgentID: s.agentID, AgentSessionID: sessionID, IdempotencyKey: idempotencyKey,
	})
	return storedMessage("provider record", row, err)
}

func (s *agentOutputSink) ReadToolRequest(spanID string) (*agent.StoredMessage, error) {
	return s.h.readToolRequest(s.agentID, s.currentMessageSessionID(), spanID)
}

func (s *agentOutputSink) ReadToolResult(spanID string) (*agent.StoredMessage, error) {
	return s.ReadToolResultForSession(spanID, s.currentMessageSessionID())
}

func (s *agentOutputSink) ReadToolResultForSession(spanID, sessionID string) (*agent.StoredMessage, error) {
	return s.h.readToolResult(s.agentID, sessionID, spanID)
}

func (s *agentOutputSink) ReadToolResultBySeq(seq int64) (*agent.StoredMessage, error) {
	if seq <= 0 {
		return nil, nil
	}
	row, err := s.h.queries.GetMessageByAgentIDAndSeq(bgCtx(), db.GetMessageByAgentIDAndSeqParams{AgentID: s.agentID, Seq: seq})
	if err == nil && (row.Source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT || row.SpanID == "") {
		return nil, nil
	}
	return storedMessage("tool result", row, err)
}

// restoreMessageSession reports read failures to the constructor, which logs after unlocking.
func (s *agentOutputSink) restoreMessageSession() error {
	if s.h.queries == nil {
		return nil
	}
	row, err := s.h.queries.GetAgentByID(bgCtx(), s.agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s.messageSession = &nativeSessionFact{id: row.AgentSessionID}
	return nil
}

func (s *agentOutputSink) currentMessageSessionID() string {
	return s.currentMessageSessionFact().id
}

func (s *agentOutputSink) currentMessageSessionFact() *nativeSessionFact {
	s.messageSessionMu.Lock()
	defer s.messageSessionMu.Unlock()
	if s.messageSession == nil {
		s.messageSession = &nativeSessionFact{}
	}
	return s.messageSession
}

func (s *agentOutputSink) scopeMessage(content agent.MessageContent) agent.MessageContent {
	if content.AgentSessionID == "" && content.Publication == nil {
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
	return storedMessage("tool request", row, err)
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
	return storedMessage("tool result", row, err)
}

// storedMessage decodes one stored row for a provider record or span read.
//
// kind identifies the read in its log and error messages.
func storedMessage(kind string, row db.Message, err error) (*agent.StoredMessage, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", kind, err)
	}
	content, err := decodeStoredRow(row, kind)
	if err != nil {
		return nil, err
	}
	return &agent.StoredMessage{Content: content, Seq: row.Seq, Revision: row.SupplementalRevision}, nil
}

// decodeStoredRow decodes the content and the supplement of one message row.
// A supplement that does not decode leaves the row with its content alone,
// and a warning that identifies the row. what identifies the read in the
// warning and the error.
func decodeStoredRow(row db.Message, what string) (agent.MessageContent, error) {
	original, err := msgcodec.Decompress(row.Content, row.ContentCompression)
	if err != nil {
		return agent.MessageContent{}, fmt.Errorf("decode %s: %w", what, err)
	}
	content := agent.MessageContent{Original: original}
	if len(row.SupplementalContent) > 0 {
		supplemental, decodeErr := msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
		if decodeErr == nil {
			content, decodeErr = agent.DecodeMessageSupplement(original, supplemental)
		}
		if decodeErr != nil {
			slog.Warn("decode "+what+" supplement", "agent_id", row.AgentID, "seq", row.Seq, "span_id", row.SpanID, "error", decodeErr)
		}
	}
	content.AgentSessionID = row.AgentSessionID
	content.IdempotencyKey = row.IdempotencyKey
	content.Completion = agent.MessageCompletionFromProto(workerdb.StorageEnumValue(row.Completion))
	return content, nil
}
