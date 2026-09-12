package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// toolTranscript recovers fields that a provider omits from tool notifications.
// Results reach the transcript immediately. Later transcript boundaries can enrich them.
type toolTranscript struct {
	ProviderServices
	ctx               context.Context
	locate            func(string) toolTranscriptLocation
	readSupplements   func(context.Context, string, map[string]MessageContent, bool) (map[string][]byte, error)
	initialSupplement func(context.Context, string, []byte, SpanInfo) ([]byte, error)
	resetRecords      func()
	toolCallID        func([]byte) string
	observeMessage    func(MessageContent, SpanInfo)
	finishTurn        func()
	newChild          func(ProviderServices) *toolTranscript
	children          map[string]*toolTranscript
	providerName      string
	mu                sync.Mutex
	sessionKey        string
	sessionID         string
	pending           map[string]MessageContent
}

type toolTranscriptLocation struct {
	sessionKey string
	path       string
}

func (s *toolTranscript) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionKey = ""
	s.sessionID = ""
	s.pending = nil
	s.children = nil
	if s.resetRecords != nil {
		s.resetRecords()
	}
}

// adoptLocation preserves replay results until the first session ID arrives.
// A different session clears pending results. A new path within that session keeps them.
func (s *toolTranscript) adoptLocation() string {
	location := s.locate(s.sessionID)
	if location.sessionKey != s.sessionKey {
		if s.sessionKey != "" {
			s.pending = nil
			s.children = nil
			if s.resetRecords != nil {
				s.resetRecords()
			}
		}
		s.sessionKey = location.sessionKey
	}
	if s.pending == nil {
		s.pending = make(map[string]MessageContent)
	}
	return location.path
}

func (s *toolTranscript) UpdateSessionID(sessionID string) {
	s.ProviderServices.UpdateSessionID(sessionID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = sessionID
	s.enrichPending(s.adoptLocation(), false)
}

func (s *toolTranscript) PersistMessage(source leapmuxv1.MessageSource, content MessageContent, span SpanInfo) error {
	if source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
		return s.ProviderServices.PersistMessage(source, content, span)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.adoptLocation()
	s.enrichPending(path, false)
	if path != "" && s.initialSupplement != nil {
		ctx, cancel := context.WithTimeout(s.ctx, 100*time.Millisecond)
		extra, err := s.initialSupplement(ctx, path, content.Original, span)
		cancel()
		if err != nil {
			slog.Debug("Read initial tool supplement", "provider", s.providerName, "error", err)
		}
		if len(extra) > 0 {
			combined, err := mergeToolSupplements(content.Supplemental, extra)
			if err != nil {
				slog.Warn("Merge initial tool supplement", "provider", s.providerName, "error", err)
			} else {
				content.Supplemental = combined
			}
		}
	}
	toolCallID := ""
	if span.Closing && span.SpanID != "" && s.toolCallID != nil {
		toolCallID = s.toolCallID(content.Original)
	}
	if err := s.ProviderServices.PersistMessage(source, content, span); err != nil {
		return err
	}
	if s.observeMessage != nil {
		s.observeMessage(content, span)
	}
	if toolCallID != "" && toolCallID == span.SpanID {
		saved := content
		saved.Original = append([]byte(nil), content.Original...)
		saved.Supplemental = append([]byte(nil), content.Supplemental...)
		s.pending[toolCallID] = saved
	}
	return nil
}

func (s *toolTranscript) PersistTurnEnd(content MessageContent, span SpanInfo) error {
	s.finishPending()
	return s.ProviderServices.PersistTurnEnd(content, span)
}

func (s *toolTranscript) finishPending() {
	s.mu.Lock()
	s.enrichPending(s.adoptLocation(), true)
	clear(s.pending)
	if s.finishTurn != nil {
		s.finishTurn()
	}
	children := make([]*toolTranscript, 0, len(s.children))
	for _, child := range s.children {
		children = append(children, child)
	}
	s.mu.Unlock()
	for _, child := range children {
		child.finishPending()
	}
}

// A provider supplies the child decorator because its native session identity is provider-specific.
func (s *toolTranscript) ChildSink(childAgentID string) ProviderServices {
	if s.newChild == nil {
		return s.ProviderServices.ChildSink(childAgentID)
	}
	s.mu.Lock()
	child := s.children[childAgentID]
	s.mu.Unlock()
	if child != nil {
		return child
	}
	delegate := s.ProviderServices.ChildSink(childAgentID)
	if delegate == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if child = s.children[childAgentID]; child == nil {
		child = s.newChild(delegate)
		if s.children == nil {
			s.children = make(map[string]*toolTranscript)
		}
		s.children[childAgentID] = child
	}
	return child
}

func (s *toolTranscript) PersistChildMessage(childAgentID string, source leapmuxv1.MessageSource, content []byte, span SpanInfo) error {
	child := s.ChildSink(childAgentID)
	if child == nil {
		return fmt.Errorf("child transcript %q is unavailable", childAgentID)
	}
	return child.PersistMessage(source, MessageContent{Original: content}, span)
}

func (s *toolTranscript) PersistChildTurnEnd(childAgentID string, content MessageContent, span SpanInfo) error {
	child := s.ChildSink(childAgentID)
	if child == nil {
		return fmt.Errorf("child transcript %q is unavailable", childAgentID)
	}
	return child.PersistTurnEnd(content, span)
}

func (s *toolTranscript) enrichPending(path string, final bool) {
	if len(s.pending) == 0 || path == "" {
		return
	}
	// A provider database must not hold the transcript while it is busy or unavailable.
	readContext := s.ctx
	if final {
		// Process cancellation must not discard completed results that the provider already stored.
		readContext = context.WithoutCancel(readContext)
	}
	ctx, cancel := context.WithTimeout(readContext, 100*time.Millisecond)
	defer cancel()
	protocol := make(map[string]MessageContent, len(s.pending))
	for id, content := range s.pending {
		protocol[id] = content
	}
	records, err := s.readSupplements(ctx, path, protocol, final)
	if err != nil {
		slog.Debug("Read stored tool records", "provider", s.providerName, "error", err)
	}
	for id, enriched := range records {
		content, found := s.pending[id]
		if !found {
			continue
		}
		combined, err := mergeToolSupplements(content.Supplemental, enriched)
		if err != nil {
			slog.Warn("Merge tool supplements", "provider", s.providerName, "error", err)
			continue
		}
		if _, err := s.EnrichMessage(MessageEnrichment{SpanID: id, OriginalContent: content.Original, SupplementalContent: combined}); err != nil {
			slog.Warn("Enrich tool result", "provider", s.providerName, "error", err)
			continue
		}
		delete(s.pending, id)
	}
}

// mergeToolSupplements preserves initial data when native records arrive later.
func mergeToolSupplements(initial, later []byte) ([]byte, error) {
	if len(initial) == 0 {
		return later, nil
	}
	var fields, added map[string]json.RawMessage
	if err := json.Unmarshal(initial, &fields); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(later, &added); err != nil {
		return nil, err
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	for key, value := range added {
		fields[key] = value
	}
	return json.Marshal(fields)
}
