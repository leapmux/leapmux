package codebuddy

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codebuddyStoredMessageType is the type of the response record that
// CodeBuddy stores for an assistant message: {type: "message", role:
// "assistant", content: [{type: "output_text", text}]}.
const codebuddyStoredMessageType = "message"

// ReplyText reads the text of an assistant frame, as the browser's
// codebuddyExtractRow does. A live assistant frame holds an Anthropic
// content array, whose text blocks join with nothing between them; a frame
// with a tool_use block is a tool call. A stored response record holds
// output_text blocks instead.
func (codebuddyProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame struct {
		providerkit.AnthropicMessage
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message.Original, &frame) != nil {
		return "", false
	}
	switch frame.Type {
	case contracts.CodebuddyFrameKindAssistant:
		blocks := providerkit.ReadAnthropicContent(frame.Message.Content)
		if blocks.HasToolUse || !blocks.HasText {
			return "", false
		}
		return strings.Join(blocks.Texts, ""), true
	case codebuddyStoredMessageType:
		if frame.Role != "assistant" {
			return "", false
		}
		return storedOutputText(frame.Content)
	default:
		return "", false
	}
}

// storedOutputText joins the output_text blocks of a stored response record.
// The browser shows the record as a reply only when one block has a
// non-empty text.
func storedOutputText(blocks []json.RawMessage) (string, bool) {
	var texts []string
	nonEmpty := false
	for _, raw := range blocks {
		var block struct {
			Type string          `json:"type"`
			Text json.RawMessage `json:"text"`
		}
		if json.Unmarshal(raw, &block) != nil || block.Type != "output_text" {
			continue
		}
		var text string
		if json.Unmarshal(block.Text, &text) == nil {
			nonEmpty = nonEmpty || text != ""
			texts = append(texts, text)
		}
	}
	if !nonEmpty {
		return "", false
	}
	return strings.Join(texts, ""), true
}
