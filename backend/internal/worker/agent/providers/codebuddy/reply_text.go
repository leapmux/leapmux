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
	fields, ok := providerkit.RawObject(message.Original)
	if !ok {
		return "", false
	}
	switch providerkit.ObjectString(fields, "type") {
	case contracts.CodebuddyFrameKindAssistant:
		msg, ok := providerkit.RawObject(fields["message"])
		if !ok {
			return "", false
		}
		rawBlocks, ok := providerkit.RawArray(msg["content"])
		if !ok {
			return "", false
		}
		blocks := providerkit.ReadAnthropicContent(rawBlocks)
		if blocks.HasToolUse || !blocks.HasText {
			return "", false
		}
		return strings.Join(blocks.Texts, ""), true
	case codebuddyStoredMessageType:
		if providerkit.ObjectString(fields, "role") != "assistant" {
			return "", false
		}
		rawBlocks, ok := providerkit.RawArray(fields["content"])
		if !ok {
			return "", false
		}
		return storedOutputText(rawBlocks)
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
		block, ok := providerkit.RawObject(raw)
		if !ok || providerkit.ObjectString(block, "type") != "output_text" {
			continue
		}
		if text, ok := providerkit.RawString(block["text"]); ok {
			nonEmpty = nonEmpty || text != ""
			texts = append(texts, text)
		}
	}
	if !nonEmpty {
		return "", false
	}
	return strings.Join(texts, ""), true
}
