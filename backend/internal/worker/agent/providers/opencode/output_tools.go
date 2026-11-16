package opencode

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/leapmux/leapmux/generated/contracts"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

type openCodeToolIdentity struct {
	messageID string
	callID    string
}

// Encode both components so a native delimiter cannot identify another call.
func openCodeToolSpanID(messageID, callID string) string {
	return "opencode:tool:" + base64.RawURLEncoding.EncodeToString([]byte(messageID)) + "." + base64.RawURLEncoding.EncodeToString([]byte(callID))
}

type openCodeToolState struct {
	key       openCodeToolIdentity
	order     int
	name      string
	nameKnown bool
	spanID    string
	input     json.RawMessage
	start     json.RawMessage
	called    json.RawMessage
	progress  json.RawMessage
	latest    json.RawMessage
	opened    bool
	closed    bool
	spawns    bool
}

type openCodeToolStreams struct {
	sessionID string
	sink      agent.ProviderServices
	tools     map[openCodeToolIdentity]*openCodeToolState
	count     int
	metadata  func(*openCodeToolState, json.RawMessage) error
	closed    func(*openCodeToolState)
}

func newOpenCodeToolStreams(sessionID string, sink agent.ProviderServices) *openCodeToolStreams {
	return &openCodeToolStreams{sessionID: sessionID, sink: agent.NewOrderedProviderServices(agent.OutputQueueFor(sink), sink), tools: make(map[openCodeToolIdentity]*openCodeToolState)}
}
func (s *openCodeToolStreams) tool(messageID string, callID *string) (*openCodeToolState, error) {
	if messageID == "" || callID == nil {
		return nil, errors.New("the OpenCode tool requires a message ID and call ID")
	}
	key := openCodeToolIdentity{messageID: messageID, callID: *callID}
	tool := s.tools[key]
	if tool == nil {
		tool = &openCodeToolState{key: key, order: len(s.tools), spanID: openCodeToolSpanID(messageID, *callID)}
		s.tools[key] = tool
	}
	return tool, nil
}
func (s *openCodeToolStreams) supplement(tool *openCodeToolState) ([]byte, error) {
	records := make([]json.RawMessage, 0, 2)
	if len(tool.start) > 0 {
		records = append(records, tool.start)
	}
	if len(tool.called) > 0 {
		records = append(records, tool.called)
	}
	if len(tool.progress) > 0 {
		records = append(records, tool.progress)
	}
	return json.Marshal(records)
}
func (s *openCodeToolStreams) content(tool *openCodeToolState, raw []byte, phase string, completion agent.MessageCompletion) (agent.MessageContent, error) {
	supplement, err := s.supplement(tool)
	if err != nil {
		return agent.MessageContent{}, err
	}
	return agent.MessageContent{Original: raw, Supplemental: supplement, AgentSessionID: s.sessionID, IdempotencyKey: tool.spanID + ":" + phase, Completion: completion}, nil
}
func (s *openCodeToolStreams) open(tool *openCodeToolState, raw []byte) error {
	if tool.opened {
		return s.enrichOpening(tool)
	}
	if !tool.nameKnown {
		return errors.New("the OpenCode tool call has no input-start name")
	}
	var target struct {
		SessionID string `json:"sessionID"`
	}
	if len(tool.input) > 0 && string(tool.input) != "null" {
		if err := json.Unmarshal(tool.input, &target); err != nil {
			return err
		}
	}
	tool.spawns = tool.name == contracts.OpenCodeNativeToolSubagent && target.SessionID == ""
	content, err := s.content(tool, raw, "request", "")
	if err != nil {
		return err
	}
	err = providerkit.OpenToolSpan(s.sink, content, tool.spanID, tool.name, tool.spawns)
	if agent.OutputRetained(err) {
		tool.opened = true
		s.sink.ReportProgress(agent.CompleteModelProgress(tool.spanID + ":input"))
	}
	return err
}

func (s *openCodeToolStreams) enrichOpening(tool *openCodeToolState) error {
	if err := s.sink.OutputQueue().Flush(); err != nil {
		return err
	}
	if !tool.opened || tool.closed || len(tool.progress) == 0 {
		return nil
	}
	captured := s.sink.CaptureMessage(agent.MessageContent{AgentSessionID: s.sessionID}, agent.SpanInfo{SpanID: tool.spanID})
	stored, err := s.sink.ReadToolRequest(tool.spanID)
	if err != nil {
		return err
	}
	if stored == nil {
		return agent.ErrOutputInFlight
	}
	supplement, err := s.supplement(tool)
	if err != nil {
		return err
	}
	if agent.JSONCanonicalEqual(stored.Content.Supplemental, supplement) {
		return nil
	}
	written, err := s.sink.EnrichMessage(agent.MessageEnrichment{
		Publication: captured.Publication, AgentSessionID: captured.AgentSessionID,
		Seq: stored.Seq, SpanID: tool.spanID, OriginalContent: stored.Content.Original,
		PreviousRevision: stored.Revision, SupplementalContent: supplement,
	})
	if err != nil {
		return err
	}
	if !written {
		return agent.ErrOutputInFlight
	}
	return nil
}
func (s *openCodeToolStreams) closeTool(tool *openCodeToolState, raw []byte, completion agent.MessageCompletion) error {
	if tool.closed {
		return s.sink.OutputQueue().Flush()
	}
	if !tool.opened {
		return errors.New("the OpenCode tool result has no retained opening")
	}
	content, err := s.content(tool, raw, "result", completion)
	if err != nil {
		return err
	}
	err = s.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{SpanID: tool.spanID, SpanType: tool.name, Closing: true, NoSpan: tool.spawns})
	if agent.OutputRetained(err) {
		tool.closed = true
		if s.closed != nil {
			s.closed(tool)
		}
		s.count++
		s.sink.CloseSpan(tool.spanID)
		s.sink.ReportProgress(agent.CompleteOutputProgress(tool.spanID))
		tool.start = nil
		tool.called = nil
		tool.progress = nil
		tool.latest = nil
		tool.input = nil
	}
	return err
}

func (s *openCodeToolStreams) recover(message openCodeStoredMessage) error {
	snapshot, err := readOpenCodeAssistantSnapshot(message)
	if err != nil || snapshot == nil {
		return err
	}
	for _, raw := range *snapshot.Content {
		if err := s.recoverPart(message.ID, raw); err != nil {
			return err
		}
	}
	return nil
}
func (s *openCodeToolStreams) recoverPart(messageID string, raw json.RawMessage) error {
	var part struct {
		Type  string  `json:"type"`
		ID    *string `json:"id"`
		Name  *string `json:"name"`
		State struct {
			Status   string             `json:"status"`
			Content  *[]json.RawMessage `json:"content"`
			Error    json.RawMessage    `json:"error"`
			Input    json.RawMessage    `json:"input"`
			Metadata json.RawMessage    `json:"metadata"`
		} `json:"state"`
	}
	if err := json.Unmarshal(raw, &part); err != nil {
		return err
	}
	if part.Type != contracts.OpenCodeNativePartTypeTool {
		return nil
	}
	if part.Name == nil {
		return errors.New("the OpenCode stored tool has no name")
	}
	switch part.State.Status {
	case "streaming":
		if _, valid := providerkit.RawString(part.State.Input); !valid {
			return errors.New("the OpenCode streaming tool input requires a string")
		}
	case contracts.OpenCodeNativeToolStatusRunning, contracts.OpenCodeNativeToolStatusCompleted, contracts.OpenCodeNativeToolStatusError:
		if err := validateOpenCodeToolObject(part.State.Input, "stored input"); err != nil {
			return err
		}
		switch part.State.Status {
		case contracts.OpenCodeNativeToolStatusRunning:
			if err := validateOpenCodeToolObject(part.State.Metadata, "stored progress metadata"); err != nil {
				return err
			}
		case contracts.OpenCodeNativeToolStatusCompleted:
			if part.State.Content == nil || len(*part.State.Content) == 0 {
				return errors.New("the completed OpenCode tool requires nonempty content")
			}
		case contracts.OpenCodeNativeToolStatusError:
			if err := validateOpenCodeToolObject(part.State.Error, "stored error"); err != nil {
				return err
			}
			if part.State.Content != nil && len(*part.State.Content) == 0 {
				return errors.New("the stored OpenCode tool result content cannot be empty")
			}
		}
	default:
		return fmt.Errorf("unsupported recovered OpenCode tool state %q", part.State.Status)
	}
	tool, err := s.tool(messageID, part.ID)
	if err != nil {
		return err
	}
	if tool.closed {
		if err := s.sink.OutputQueue().Flush(); err != nil {
			return err
		}
		return nil
	}
	if tool.nameKnown && tool.name != *part.Name {
		return errors.New("the recovered OpenCode tool name changed")
	}
	tool.name, tool.nameKnown, tool.latest = *part.Name, true, raw
	if part.State.Status == "streaming" {
		return nil
	}
	tool.input = part.State.Input
	if err := s.open(tool, raw); err != nil {
		return err
	}
	if s.metadata != nil && len(part.State.Metadata) > 0 && string(part.State.Metadata) != "null" {
		if err := s.metadata(tool, part.State.Metadata); err != nil {
			return err
		}
	}
	switch part.State.Status {
	case contracts.OpenCodeNativeToolStatusRunning:
	case contracts.OpenCodeNativeToolStatusCompleted:
		if err := s.closeTool(tool, raw, agent.MessageCompletionComplete); err != nil {
			return err
		}
	case contracts.OpenCodeNativeToolStatusError:
		if err := s.closeTool(tool, raw, agent.MessageCompletionError); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported recovered OpenCode tool state %q", part.State.Status)
	}
	return nil
}

func (s *openCodeToolStreams) finish(completion agent.MessageCompletion) error {
	ordered := make([]*openCodeToolState, 0, len(s.tools))
	for _, tool := range s.tools {
		ordered = append(ordered, tool)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].order < ordered[j].order })
	for _, tool := range ordered {
		if tool.closed {
			continue
		}
		if !tool.opened {
			s.sink.ReportProgress(agent.CompleteModelProgress(tool.spanID + ":input"))
			continue
		}
		if err := s.closeTool(tool, tool.latest, completion); err != nil {
			return err
		}
	}
	return s.sink.OutputQueue().Flush()
}
