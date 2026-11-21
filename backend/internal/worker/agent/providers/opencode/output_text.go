package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type openCodeTextIdentity struct {
	messageID string
	ordinal   int
	kind      agent.AssembledMessageKind
}
type openCodeTextSegment struct {
	scopeID  string
	kind     agent.AssembledMessageKind
	buffer   providerkit.GenerationBuffer
	order    int
	ended    bool
	finished bool
}

// openCodeTextStreams belongs to one session and runs on its ordered event dispatcher.
// A replacement connection reconciles projected text before accepting buffered live events.
type openCodeTextStreams struct {
	sessionID string
	sink      providerkit.GenerationServices
	segments  map[openCodeTextIdentity]*openCodeTextSegment
}

func newOpenCodeTextStreams(sessionID string, sink providerkit.GenerationServices) *openCodeTextStreams {
	return &openCodeTextStreams{sessionID: sessionID, sink: sink, segments: make(map[openCodeTextIdentity]*openCodeTextSegment)}
}

func (s *openCodeTextStreams) segment(messageID string, ordinal int, kind agent.AssembledMessageKind) (*openCodeTextSegment, error) {
	if messageID == "" || ordinal < 0 {
		return nil, errors.New("OpenCode text requires a message ID and nonnegative ordinal")
	}
	key := openCodeTextIdentity{messageID: messageID, ordinal: ordinal, kind: kind}
	segment := s.segments[key]
	if segment != nil {
		return segment, nil
	}
	segment = &openCodeTextSegment{scopeID: openCodeTextScopeID(messageID, kind, ordinal), kind: kind, order: len(s.segments)}
	s.segments[key] = segment
	return segment, nil
}

func openCodeTextScopeID(messageID string, kind agent.AssembledMessageKind, ordinal int) string {
	return fmt.Sprintf("opencode:%s:%s:%d", messageID, kind, ordinal)
}

func (s *openCodeTextStreams) append(segment *openCodeTextSegment, text string) {
	segment.buffer.Append(segment.scopeID, segment.kind, text, providerkit.JoinVerbatim)
	if text != "" {
		s.sink.ReportProgress(agent.ModelTextProgress(segment.scopeID, text))
	}
}

func (s *openCodeTextStreams) replace(segment *openCodeTextSegment, text string) {
	segment.buffer.Discard(segment.scopeID)
	s.sink.ReportProgress(agent.ResetModelScopeProgress(segment.scopeID))
	s.append(segment, text)
}

func (s *openCodeTextStreams) persist(segment *openCodeTextSegment, completion agent.MessageCompletion) error {
	if segment.finished {
		return segment.buffer.OutputQueue().Flush()
	}
	_, err := segment.buffer.PersistScopeTo(segment.scopeID, completion, s.sink, agent.MessageContent{
		AgentSessionID: s.sessionID, IdempotencyKey: segment.scopeID,
	}, agent.SpanInfo{})
	var pending *agent.PendingOutputError
	if err == nil || errors.As(err, &pending) {
		segment.finished = true
		s.sink.ReportProgress(agent.CompleteModelProgress(segment.scopeID))
	}
	return err
}

func (s *openCodeTextStreams) recover(message openCodeStoredMessage) error {
	snapshot, err := readOpenCodeAssistantSnapshot(message)
	if err != nil || snapshot == nil {
		return err
	}
	return s.recoverSnapshot(message.ID, snapshot)
}

func (s *openCodeTextStreams) recoverSnapshot(messageID string, snapshot *openCodeAssistantSnapshot) error {
	ordinals := make(map[string]int)
	for _, raw := range *snapshot.Content {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return err
		}
		ordinal := ordinals[header.Type]
		ordinals[header.Type]++
		if err := s.recoverPart(messageID, ordinal, raw, snapshot); err != nil {
			return err
		}
	}
	return nil
}

func (s *openCodeTextStreams) recoverPart(messageID string, ordinal int, raw json.RawMessage, snapshot *openCodeAssistantSnapshot) error {
	completion := agent.MessageCompletionComplete
	failed := providerkit.RawPresent(snapshot.Error)
	if failed {
		completion = agent.MessageCompletionError
		var nativeError struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(snapshot.Error, &nativeError); err != nil {
			return err
		}
		if nativeError.Type == "aborted" {
			completion = agent.MessageCompletionInterrupted
		}
	}
	var part struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
		Time struct {
			Completed *float64 `json:"completed"`
		} `json:"time"`
	}
	if err := json.Unmarshal(raw, &part); err != nil {
		return err
	}
	kind := agent.AssembledMessageKindText
	switch part.Type {
	case "text":
	case "reasoning":
		kind = agent.AssembledMessageKindReasoning
	default:
		return nil
	}
	if part.Text == nil {
		return errors.New("the OpenCode text part has no text value")
	}
	segment, err := s.segment(messageID, ordinal, kind)
	if err != nil {
		return err
	}
	if providerkit.RawPresent(snapshot.Retry) {
		s.discardMessage(messageID)
		return nil
	}
	if segment.finished {
		if err := segment.buffer.OutputQueue().Flush(); err != nil {
			return err
		}
		return nil
	}
	// Native text becomes nonempty only at text.ended. Reasoning also
	// records its completion time. An empty unfinished projection cannot
	// replace live deltas, and an unrelated later part cannot end this one.
	completed := snapshot.Time.Completed != nil || failed || part.Time.Completed != nil || part.Type == "text" && *part.Text != ""
	if completed || *part.Text != "" {
		s.replace(segment, *part.Text)
	}
	if completed {
		if err := s.persist(segment, completion); err != nil {
			return err
		}
	}
	return nil
}

func (s *openCodeTextStreams) discardMessage(messageID string) {
	for key, segment := range s.segments {
		if key.messageID == messageID && !segment.finished {
			segment.buffer.Discard(segment.scopeID)
			segment.finished = true
			s.sink.ReportProgress(agent.ResetModelScopeProgress(segment.scopeID))
		}
	}
}

func (s *openCodeTextStreams) flushEnded(messageID string) error {
	return s.persistMatching(agent.MessageCompletionComplete, func(key openCodeTextIdentity, segment *openCodeTextSegment) bool {
		return segment.ended && (messageID == "" || key.messageID == messageID)
	})
}

func (s *openCodeTextStreams) finish(completion agent.MessageCompletion) error {
	return s.persistMatching(completion, func(openCodeTextIdentity, *openCodeTextSegment) bool { return true })
}

func (s *openCodeTextStreams) persistMatching(completion agent.MessageCompletion, matches func(openCodeTextIdentity, *openCodeTextSegment) bool) error {
	segments := make([]*openCodeTextSegment, 0, len(s.segments))
	for key, segment := range s.segments {
		if matches(key, segment) {
			segments = append(segments, segment)
		}
	}
	slices.SortFunc(segments, func(left, right *openCodeTextSegment) int { return left.order - right.order })
	for _, segment := range segments {
		if err := s.persist(segment, completion); err != nil {
			return err
		}
	}
	return nil
}
