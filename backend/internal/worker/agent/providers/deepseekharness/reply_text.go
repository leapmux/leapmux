package deepseekharness

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ReplyText reads the text block that an assistant/message row holds, as the
// browser's deepseekHarnessAssistantBlock does: the Worker stores each block
// of an assistant message as a row of its own, and blockIndex selects it.
func (deepseekHarnessProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame map[string]json.RawMessage
	if json.Unmarshal(message.Original, &frame) != nil {
		return "", false
	}
	var eventType string
	if json.Unmarshal(frame[contracts.DeepseekHarnessFieldType], &eventType) != nil || eventType != contracts.DeepseekHarnessEventAssistantMessage {
		return "", false
	}
	var data map[string]json.RawMessage
	var assistant map[string]json.RawMessage
	var blocks []json.RawMessage
	if json.Unmarshal(frame[contracts.DeepseekHarnessFieldData], &data) != nil ||
		json.Unmarshal(data[contracts.DeepseekHarnessFieldMessage], &assistant) != nil ||
		json.Unmarshal(assistant[contracts.DeepseekHarnessFieldContent], &blocks) != nil {
		return "", false
	}
	var index float64
	if json.Unmarshal(frame[contracts.DeepseekHarnessSupplementBlockIndex], &index) != nil ||
		index < 0 || index != float64(int(index)) || int(index) >= len(blocks) {
		return "", false
	}
	var block struct {
		Type string          `json:"type"`
		Text json.RawMessage `json:"text"`
	}
	if json.Unmarshal(blocks[int(index)], &block) != nil || block.Type != contracts.DeepseekHarnessContentTypeText {
		return "", false
	}
	var text string
	_ = json.Unmarshal(block.Text, &text)
	return text, true
}
