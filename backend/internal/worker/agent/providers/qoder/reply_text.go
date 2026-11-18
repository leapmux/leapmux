package qoder

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReplyText reads the text of an assistant frame, as the browser's
// qoderExtractRow does: the text blocks of message.content, joined with
// nothing between them. A frame with a tool_use block is a tool call, and
// one with no text block is a thinking or empty row; neither is a reply.
func (qoderProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame providerkit.AnthropicMessage
	if json.Unmarshal(message.Original, &frame) != nil || frame.Type != contracts.QoderFrameKindAssistant {
		return "", false
	}
	blocks := providerkit.ReadAnthropicContent(frame.Message.Content)
	if blocks.HasToolUse || !blocks.HasText {
		return "", false
	}
	return strings.Join(blocks.Texts, ""), true
}
