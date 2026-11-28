package copilot

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ReplyText reads the content of an assistant.message session event, as the
// browser's copilotExtractRow does.
func (copilotProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame struct {
		Method string `json:"method"`
		Params struct {
			Event struct {
				Type string `json:"type"`
				Data struct {
					Content json.RawMessage `json:"content"`
				} `json:"data"`
			} `json:"event"`
		} `json:"params"`
	}
	if json.Unmarshal(message.Original, &frame) != nil || frame.Method != contracts.CopilotMethodSessionEvent ||
		frame.Params.Event.Type != contracts.CopilotEventAssistantMessage {
		return "", false
	}
	var text string
	_ = json.Unmarshal(frame.Params.Event.Data.Content, &text)
	return text, true
}
