package qoder

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const (
	qoderChildBlockByteLimit    = contracts.MaxMessageSize / 4
	qoderChildPendingBlockLimit = 8
)

type qoderChildStream struct {
	blocks map[int]*qoderChildBlock
}

type qoderChildBlock struct {
	kind         string
	id           string
	name         string
	initialInput json.RawMessage
	text         strings.Builder
	input        strings.Builder
	truncated    bool
}

type qoderStreamEvent struct {
	Event struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock *struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			Thinking string          `json:"thinking"`
			ID       string          `json:"id"`
			Name     string          `json:"name"`
			Input    json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	} `json:"event"`
}

// The child stream carries deltas but no completed assistant frame. Assemble
// each block when Qoder closes it, so the normal Qoder extractor draws it once.
func (a *Agent) handleChildStreamFrame(childID, spawnID string, raw []byte) {
	var frame qoderStreamEvent
	if err := json.Unmarshal(raw, &frame); err != nil {
		slog.Warn("qoder child stream frame is unreadable", "agent_id", a.AgentID(), "error", err)
		return
	}

	var completed []*qoderChildBlock
	a.childStreamMu.Lock()
	if a.childStreams == nil {
		a.childStreams = make(map[string]*qoderChildStream)
	}
	stream := a.childStreams[spawnID]
	if stream == nil {
		stream = &qoderChildStream{blocks: make(map[int]*qoderChildBlock)}
		a.childStreams[spawnID] = stream
	}
	switch frame.Event.Type {
	case "message_start":
		completed = stream.takeBlocks()
	case "content_block_start":
		if start := frame.Event.ContentBlock; start != nil {
			if len(stream.blocks) < qoderChildPendingBlockLimit || stream.blocks[frame.Event.Index] != nil {
				block := &qoderChildBlock{kind: start.Type, id: start.ID, name: start.Name, initialInput: start.Input}
				switch start.Type {
				case "thinking":
					block.truncated = appendQoderChildDelta(&block.text, start.Thinking)
				case "text":
					block.truncated = appendQoderChildDelta(&block.text, start.Text)
				}
				stream.blocks[frame.Event.Index] = block
			} else {
				slog.Warn("qoder child has too many open content blocks", "agent_id", a.AgentID(), "spawn_tool_use_id", spawnID)
			}
		}
	case "content_block_delta":
		if block := stream.blocks[frame.Event.Index]; block != nil {
			var target *strings.Builder
			var value string
			switch {
			case block.kind == "text" && frame.Event.Delta.Type == "text_delta":
				target, value = &block.text, frame.Event.Delta.Text
			case block.kind == "thinking" && frame.Event.Delta.Type == "thinking_delta":
				target, value = &block.text, frame.Event.Delta.Thinking
			case block.kind == "tool_use" && frame.Event.Delta.Type == "input_json_delta":
				target, value = &block.input, frame.Event.Delta.PartialJSON
			}
			if target != nil && appendQoderChildDelta(target, value) {
				block.truncated = true
			}
		}
	case "content_block_stop":
		if block := stream.blocks[frame.Event.Index]; block != nil {
			completed = append(completed, block)
			delete(stream.blocks, frame.Event.Index)
		}
	case "message_stop":
		completed = stream.takeBlocks()
		delete(a.childStreams, spawnID)
	}
	a.childStreamMu.Unlock()

	for _, block := range completed {
		a.persistQoderChildBlock(childID, spawnID, block)
	}
}

// takeBlocks retains native block order when a message ends without stop frames.
func (stream *qoderChildStream) takeBlocks() []*qoderChildBlock {
	indices := make([]int, 0, len(stream.blocks))
	for index := range stream.blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	blocks := make([]*qoderChildBlock, 0, len(indices))
	for _, index := range indices {
		blocks = append(blocks, stream.blocks[index])
		delete(stream.blocks, index)
	}
	return blocks
}

func appendQoderChildDelta(builder *strings.Builder, value string) bool {
	remaining := qoderChildBlockByteLimit - builder.Len()
	if len(value) <= remaining {
		builder.WriteString(value)
		return false
	}
	if remaining > 0 {
		end := remaining
		for end > 0 && !utf8.RuneStart(value[end]) {
			end--
		}
		builder.WriteString(value[:end])
	}
	return true
}

func (a *Agent) persistQoderChildBlock(childID, spawnID string, block *qoderChildBlock) {
	var content map[string]any
	switch block.kind {
	case "text", "thinking":
		value := block.text.String()
		if block.truncated {
			value += "\n[Child output truncated]"
		}
		if value == "" {
			return
		}
		content = map[string]any{"type": block.kind, block.kind: value}
	case "tool_use":
		if block.id == "" || block.name == "" {
			return
		}
		if block.truncated {
			content = map[string]any{"type": "text", "text": "Child tool input exceeded the transcript limit."}
			break
		}
		input := block.initialInput
		if block.input.Len() > 0 {
			input = []byte(block.input.String())
		}
		var args map[string]any
		if len(input) > 0 && json.Unmarshal(input, &args) != nil {
			content = map[string]any{"type": "text", "text": "Child tool input could not be decoded."}
			break
		}
		if args == nil {
			args = make(map[string]any)
		}
		content = map[string]any{"type": "tool_use", "id": block.id, "name": block.name, "input": args}
	default:
		return
	}
	envelope := map[string]any{
		"type":               "assistant",
		"message":            map[string]any{"role": "assistant", "content": []any{content}},
		"parent_tool_use_id": spawnID,
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		slog.Error("qoder encode child block", "agent_id", a.AgentID(), "error", err)
		return
	}
	if a.persistNativeToolFrame(a.sink.ChildSink(childID), raw) {
		return
	}
	if err := a.sink.PersistChildMessage(childID, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, raw, agent.SpanInfo{}); err != nil {
		slog.Error("qoder persist child block", "agent_id", a.AgentID(), "error", err)
	}
}

func (a *Agent) clearChildStream(spawnID string) {
	a.childStreamMu.Lock()
	delete(a.childStreams, spawnID)
	a.childStreamMu.Unlock()
}
