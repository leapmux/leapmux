package ohmypi

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReplyText reads the text of an assistant message_end, as the browser's
// ohMyPiExtractRow does: its text blocks, joined into paragraphs. The Worker
// stores the thinking as a row of its own, and a custom message is a notice
// for the model, never the reply.
//
// The browser also draws an image block as Markdown. A reply that a terminal
// prints leaves the image out, because its Markdown is a data URL.
func (ompProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame struct {
		Type    string `json:"type"`
		Message struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(message.Original, &frame) != nil || frame.Type != contracts.OhMyPiEventMessageEnd || frame.Message.Role != contracts.OhMyPiRoleAssistant {
		return "", false
	}
	return providerkit.JoinParagraphs(providerkit.ReadAnthropicContent(frame.Message.Content).Texts), true
}
