package deepseekharness

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
)

type toolSource struct {
	tooltranscript.SourceDefaults
	owner     *Agent
	mu        sync.Mutex
	sessionID string
	names     map[string]string
}

func newToolTranscript(ctx context.Context, sink agent.ProviderServices, owner *Agent) *tooltranscript.Transcript {
	return tooltranscript.New(ctx, sink, &toolSource{owner: owner, names: map[string]string{}})
}

func (*toolSource) ProviderName() string { return "DeepSeek Harness" }

func (s *toolSource) Locate(sessionID string) tooltranscript.Location {
	s.mu.Lock()
	s.sessionID = sessionID
	s.mu.Unlock()
	return tooltranscript.Location{SessionKey: sessionID, Path: s.owner.imageReceipts.Receipts, Ready: sessionID != "" && s.owner.imageReceipts.Receipts != ""}
}

func (*toolSource) ToolCallID(original []byte) string { return deepseekToolCallID(original) }

func deepseekToolCallID(original []byte) string {
	var event struct {
		Type string `json:"type"`
		Data struct {
			Message struct {
				ID      string          `json:"toolCallId"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"data"`
	}
	if json.Unmarshal(original, &event) != nil || event.Type != contracts.DeepseekHarnessEventToolResult || event.Data.Message.ID == "" {
		return ""
	}
	return event.Data.Message.ID
}

func (s *toolSource) ObserveMessage(content agent.MessageContent, span agent.SpanInfo) {
	var event struct {
		Type string `json:"type"`
		Data struct {
			ID   string `json:"callId"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if json.Unmarshal(content.Original, &event) != nil || event.Type != contracts.DeepseekHarnessEventToolCall || event.Data.ID == "" || event.Data.ID != span.SpanID || event.Data.Name == "" {
		return
	}
	s.mu.Lock()
	s.names[event.Data.ID] = event.Data.Name
	s.mu.Unlock()
}

func (s *toolSource) InitialSupplement(ctx context.Context, _ string, original []byte, span agent.SpanInfo) ([]byte, error) {
	if !span.Closing {
		return nil, nil
	}
	id := s.ToolCallID(original)
	if id == "" || id != span.SpanID {
		return nil, nil
	}
	s.mu.Lock()
	sessionID, name := s.sessionID, s.names[id]
	s.mu.Unlock()
	if name == "" {
		name = span.SpanType
	}
	return s.recoverSupplement(ctx, sessionID, id, name, original)
}

func (s *toolSource) recoverSupplement(ctx context.Context, sessionID, id, name string, original []byte) ([]byte, error) {
	return recoverToolSupplement(ctx, s.owner.imageReceipts, s.owner.rpc, sessionID, id, name, original)
}

func recoverToolSupplement(ctx context.Context, files imageHookFiles, rpc remoteRPC, sessionID, id, name string, original []byte) ([]byte, error) {
	receipt, receiptErr := recoverImageReceipt(ctx, files, sessionID, id, name, original)
	if receiptErr == nil && len(receipt) > 0 {
		return receipt, nil
	}
	images, imageErr := recoverResultImages(ctx, rpc, sessionID, original)
	return images, errors.Join(receiptErr, imageErr)
}

func (s *toolSource) CaptureSupplement(location tooltranscript.Location, content agent.MessageContent, span agent.SpanInfo) tooltranscript.SupplementReader {
	id := deepseekToolCallID(content.Original)
	if id == "" {
		return nil
	}
	s.mu.Lock()
	name := s.names[id]
	s.mu.Unlock()
	if name == "" {
		name = span.SpanType
	}
	captured := content.Clone()
	if captured.AgentSessionID == "" {
		captured.AgentSessionID = location.SessionKey
	}
	files, rpc := s.owner.imageReceipts, s.owner.rpc
	pending := map[string]agent.MessageContent{id: captured}
	names := map[string]string{id: name}
	return func(ctx context.Context, _ bool) ([]byte, error) {
		records, err := readDeepseekToolSupplements(ctx, files, rpc, location.SessionKey, pending, names)
		return records[id], err
	}
}

func (s *toolSource) ReadSupplements(ctx context.Context, location tooltranscript.Location, pending map[string]agent.MessageContent, _ bool) (map[string][]byte, error) {
	names := make(map[string]string, len(pending))
	s.mu.Lock()
	for id := range pending {
		names[id] = s.names[id]
	}
	s.mu.Unlock()
	return readDeepseekToolSupplements(ctx, s.owner.imageReceipts, s.owner.rpc, location.SessionKey, pending, names)
}

func readDeepseekToolSupplements(ctx context.Context, files imageHookFiles, rpc remoteRPC, sessionID string, pending map[string]agent.MessageContent, names map[string]string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(pending))
	var failures error
	for id, content := range pending {
		if id != deepseekToolCallID(content.Original) {
			continue
		}
		rowSessionID := content.AgentSessionID
		if rowSessionID == "" {
			rowSessionID = sessionID
		}
		if sessionID != "" && rowSessionID != sessionID {
			failures = errors.Join(failures, errors.New("the DeepSeek Harness tool result belongs to another session"))
			continue
		}
		extra, err := recoverToolSupplement(ctx, files, rpc, rowSessionID, id, names[id], content.Original)
		failures = errors.Join(failures, err)
		if err == nil && len(extra) > 0 {
			result[id] = extra
		}
	}
	return result, failures
}

func (s *toolSource) FinishTurn()   { s.mu.Lock(); clear(s.names); s.mu.Unlock() }
func (s *toolSource) ResetRecords() { s.mu.Lock(); s.sessionID = ""; clear(s.names); s.mu.Unlock() }
func (s *toolSource) NewChild(_ string, _ agent.ProviderServices) tooltranscript.Source {
	return &toolSource{owner: s.owner, names: map[string]string{}}
}
