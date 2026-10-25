package codebuddy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

const (
	codebuddyStreamMessageStart = "message_start"
	codebuddyStreamMessageStop  = "message_stop"
	codebuddyStreamBlockStart   = "content_block_start"
	codebuddyStreamBlockDelta   = "content_block_delta"
	codebuddyStreamBlockStop    = "content_block_stop"
)

// Native partial events identify the root session and the optional owning child tool.
// The child's conversation request differs from its parent's request.
type codebuddyStreamEnvelope struct {
	SessionID       string               `json:"session_id"`
	ParentToolUseID string               `json:"parent_tool_use_id"`
	RequestID       string               `json:"_requestId"`
	MessageID       string               `json:"_messageId"`
	Event           codebuddyStreamEvent `json:"event"`
}

type codebuddyStreamEvent struct {
	Type    string `json:"type"`
	Index   *int   `json:"index"`
	Message struct {
		ID string `json:"id"`
	} `json:"message"`
	ContentBlock struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"content_block"`
	Delta struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

// CodeBuddy keeps one current response per parent tool ID. Keep its completion
// after the turn ends so a repeated start cannot reopen the same response.
type codebuddyModelStream struct {
	requestID string
	messageID string
	completed bool
	blocks    map[int]codebuddyStreamBlock
	// Capture the admitted sink so child cleanup can retire it. A late delta
	// must never ask the registry for a replacement sink.
	target agent.ProviderServices
}

type codebuddyStreamBlock struct {
	kind      string
	completed bool
}

func (a *Agent) handleStreamEvent(raw []byte) {
	if a.IsStopped() {
		return
	}
	var frame codebuddyStreamEnvelope
	if json.Unmarshal(raw, &frame) != nil {
		return
	}
	for _, id := range []string{frame.RequestID, frame.MessageID} {
		if strings.TrimSpace(id) == "" || strings.ContainsRune(id, '\x00') {
			return
		}
	}
	a.mu.Lock()
	sessionID := a.sessionID
	a.mu.Unlock()
	if frame.SessionID == "" || frame.SessionID != sessionID {
		return
	}
	if frame.ParentToolUseID != "" && (strings.TrimSpace(frame.ParentToolUseID) == "" || strings.ContainsRune(frame.ParentToolUseID, '\x00')) {
		return
	}
	startsMessage := frame.Event.Type == codebuddyStreamMessageStart
	switch frame.Event.Type {
	case codebuddyStreamMessageStart:
		if frame.Event.Message.ID != frame.MessageID {
			return
		}
	case codebuddyStreamMessageStop, codebuddyStreamBlockStart, codebuddyStreamBlockDelta, codebuddyStreamBlockStop:
	default:
		return
	}
	var owner string
	var target agent.ProviderServices
	if startsMessage {
		var ok bool
		owner, target, ok = a.modelStreamTarget(frame.ParentToolUseID)
		if !ok {
			return
		}
	} else if frame.ParentToolUseID != "" {
		rowKey, childID := a.tasks.child(frame.ParentToolUseID)
		if rowKey == "" || childID == "" {
			return
		}
		owner = childID
	}

	a.mu.Lock()
	if frame.SessionID != a.sessionID || owner == "" && (a.compactionLive || a.manualCompaction) {
		a.mu.Unlock()
		return
	}
	current := a.modelStreams[owner]
	var updates []agent.ProgressUpdate
	if startsMessage {
		if current != nil && current.requestID == frame.RequestID && current.messageID == frame.MessageID {
			a.mu.Unlock()
			return
		}
		if a.modelStreams == nil {
			a.modelStreams = make(map[string]*codebuddyModelStream)
		}
		a.modelStreams[owner] = &codebuddyModelStream{requestID: frame.RequestID, messageID: frame.MessageID, target: target}
		updates = []agent.ProgressUpdate{agent.ResetModelProgress()}
	} else {
		if current == nil || current.requestID != frame.RequestID || current.messageID != frame.MessageID {
			a.mu.Unlock()
			return
		}
		var toolID string
		updates, toolID = current.observe(frame.Event)
		// A permission can precede the completed assistant frame. The admitted
		// root stream establishes ownership before that permission arrives.
		if owner == "" && toolID != "" {
			if a.turnToolCalls == nil {
				a.turnToolCalls = make(map[string]struct{})
			}
			a.turnToolCalls[toolID] = struct{}{}
		}
		target = current.target
	}
	a.mu.Unlock()
	if startsMessage && owner == "" {
		a.armTurnFromOutput()
	}
	for _, update := range updates {
		target.ReportProgress(update)
	}
}

// Partial events cannot create or revive a child. Its native spawn establishes
// the registry identity before the first child model request.
func (a *Agent) modelStreamTarget(parentToolUseID string) (string, agent.ProviderServices, bool) {
	if parentToolUseID == "" {
		return "", a.sink, true
	}
	rowKey, childID := a.tasks.child(parentToolUseID)
	if rowKey == "" || childID == "" {
		return "", nil, false
	}
	registeredChild, status, exists, err := a.sink.LookupBackgroundTask(rowKey)
	if err != nil {
		slog.Warn("codebuddy: look up child model stream", "row_key", rowKey, "error", err)
		return "", nil, false
	}
	if !exists || status.IsFinished() || registeredChild != childID {
		return "", nil, false
	}
	return childID, a.sink.ChildSink(childID), true
}

func (s *codebuddyModelStream) scopeID(index int) string {
	return fmt.Sprintf("%q/%q/%d", s.requestID, s.messageID, index)
}

// observe returns progress and the ID of a newly admitted tool block.
// The caller records root tool ownership under the mutex and reports progress after unlocking.
func (s *codebuddyModelStream) observe(event codebuddyStreamEvent) ([]agent.ProgressUpdate, string) {
	if s.completed {
		return nil, ""
	}
	if event.Type == codebuddyStreamMessageStop {
		s.completed = true
		var updates []agent.ProgressUpdate
		for index, block := range s.blocks {
			if !block.completed {
				block.completed = true
				s.blocks[index] = block
				if block.kind != "tool_use" {
					updates = append(updates, agent.CompleteModelProgress(s.scopeID(index)))
				}
			}
		}
		return updates, ""
	}
	if event.Index == nil || *event.Index < 0 {
		return nil, ""
	}
	index := *event.Index
	block, exists := s.blocks[index]
	var text string
	switch event.Type {
	case codebuddyStreamBlockStart:
		if exists {
			return nil, ""
		}
		switch event.ContentBlock.Type {
		case "text":
			text = event.ContentBlock.Text
		case "thinking":
			text = event.ContentBlock.Thinking
		case "tool_use":
			for _, value := range []string{event.ContentBlock.ID, event.ContentBlock.Name} {
				if strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\x00') {
					return nil, ""
				}
			}
			if s.blocks == nil {
				s.blocks = make(map[int]codebuddyStreamBlock)
			}
			s.blocks[index] = codebuddyStreamBlock{kind: event.ContentBlock.Type}
			return nil, event.ContentBlock.ID
		default:
			return nil, ""
		}
		if s.blocks == nil {
			s.blocks = make(map[int]codebuddyStreamBlock)
		}
		s.blocks[index] = codebuddyStreamBlock{kind: event.ContentBlock.Type}
	case codebuddyStreamBlockDelta:
		if !exists || block.completed {
			return nil, ""
		}
		switch {
		case block.kind == "text" && event.Delta.Type == "text_delta":
			text = event.Delta.Text
		case block.kind == "thinking" && event.Delta.Type == "thinking_delta":
			text = event.Delta.Thinking
		default:
			return nil, ""
		}
	case codebuddyStreamBlockStop:
		if !exists || block.completed {
			return nil, ""
		}
		block.completed = true
		s.blocks[index] = block
		if block.kind == "tool_use" {
			return nil, ""
		}
		return []agent.ProgressUpdate{agent.CompleteModelProgress(s.scopeID(index))}, ""
	default:
		return nil, ""
	}
	if text == "" {
		return nil, ""
	}
	return []agent.ProgressUpdate{agent.ModelTextProgress(s.scopeID(index), text)}, ""
}

// A completed assistant frame can arrive without a separate message_stop.
// Its normal persistence resets the counter; this guard prevents late deltas.
func (a *Agent) completeModelStream(owner string, raw []byte) {
	var frame codebuddyStreamEnvelope
	if json.Unmarshal(raw, &frame) != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current := a.modelStreams[owner]
	if frame.SessionID == a.sessionID && current != nil && current.requestID == frame.RequestID && current.messageID == frame.MessageID {
		a.closeModelStreamLocked(owner)
	}
}

func (a *Agent) closeModelStreamLocked(owner string) {
	if current := a.modelStreams[owner]; current != nil {
		current.completed = true
	}
}

func (a *Agent) forgetModelStream(owner string) {
	a.mu.Lock()
	delete(a.modelStreams, owner)
	a.mu.Unlock()
}
