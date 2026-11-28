package claude

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReplyText reads the text of an assistant envelope, as the browser's
// claudeAssistantRow does: the text blocks of message.content, joined into
// paragraphs. An envelope with a tool_use block is a tool call, and one with
// no text block is a thinking or empty row; neither is a reply.
//
// The browser also draws an image block as Markdown. A reply that a terminal
// prints leaves the image out, because its Markdown is a data URL.
func (claudeProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame providerkit.AnthropicMessage
	if json.Unmarshal(message.Original, &frame) != nil || frame.Type != contracts.ClaudeFrameKindAssistant {
		return "", false
	}
	blocks := providerkit.ReadAnthropicContent(frame.Message.Content)
	if blocks.HasToolUse || !blocks.HasText {
		return "", false
	}
	return providerkit.JoinParagraphs(blocks.Texts), true
}
