package letta

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ReplyText reads the top-level text that the browser's lettaExtractRow reads
// for an assistant text: a native assistant_message record, and an assembled
// envelope that the shared reader refused as malformed, which Letta still
// draws as text.
func (lettaProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame struct {
		Type        string          `json:"type"`
		MessageType string          `json:"message_type"`
		Text        json.RawMessage `json:"text"`
	}
	if json.Unmarshal(message.Original, &frame) != nil {
		return "", false
	}
	switch {
	case frame.Type == contracts.AssembledMessageType:
	case frame.Type == contracts.LettaMessageTurnFinished:
		return "", false
	case frame.MessageType == contracts.LettaDeltaKindAssistantMessage:
	default:
		return "", false
	}
	var text string
	_ = json.Unmarshal(frame.Text, &text)
	return text, true
}
