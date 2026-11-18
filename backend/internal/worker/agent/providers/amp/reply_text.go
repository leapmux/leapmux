package amp

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReplyText reads the text of an assistant row, as the browser's
// ampBlockText does: the text blocks of message.content that hold a
// character other than white space, joined into paragraphs by a blank line.
// A row with a tool_use block is a tool call, never a reply.
func (ampProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame providerkit.AnthropicMessage
	if json.Unmarshal(message.Original, &frame) != nil || frame.Type != contracts.AmpLineTypeAssistant {
		return "", false
	}
	blocks := providerkit.ReadAnthropicContent(frame.Message.Content)
	if blocks.HasToolUse {
		return "", false
	}
	var texts []string
	for _, text := range blocks.Texts {
		if strings.TrimSpace(text) != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return "", false
	}
	return strings.Join(texts, "\n\n"), true
}
