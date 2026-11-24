package commandcode

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ReplyText reads the text of a message_end event, as the browser's
// commandCodeText does: its text blocks, joined with nothing between them,
// in their exact order and white space.
func (commandcodeProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame struct {
		Type  string `json:"type"`
		Event struct {
			Type    string            `json:"type"`
			Content []json.RawMessage `json:"content"`
		} `json:"event"`
	}
	if json.Unmarshal(message.Original, &frame) != nil || frame.Type != contracts.CommandCodeFrameKindEvent ||
		frame.Event.Type != contracts.CommandCodeEventMessageEnd {
		return "", false
	}
	var texts []string
	for _, raw := range frame.Event.Content {
		var block struct {
			Type string          `json:"type"`
			Text json.RawMessage `json:"text"`
		}
		if json.Unmarshal(raw, &block) != nil || block.Type != "text" {
			continue
		}
		var text string
		_ = json.Unmarshal(block.Text, &text)
		texts = append(texts, text)
	}
	return strings.Join(texts, ""), true
}
