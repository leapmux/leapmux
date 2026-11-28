package deepseekharness

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReplyText reads the text block that an assistant/message row holds, as the
// browser's deepseekHarnessAssistantBlock does: the Worker stores each block
// of an assistant message as a row of its own, and blockIndex selects it.
func (deepseekHarnessProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(message.Original, &head) != nil || head.Type != contracts.DeepseekHarnessEventAssistantMessage {
		return "", false
	}
	frame, ok := providerkit.RawObject(message.Original)
	if !ok || providerkit.ObjectString(frame, contracts.DeepseekHarnessFieldType) != contracts.DeepseekHarnessEventAssistantMessage {
		return "", false
	}
	data, ok := providerkit.RawObject(frame[contracts.DeepseekHarnessFieldData])
	if !ok {
		return "", false
	}
	assistant, ok := providerkit.RawObject(data[contracts.DeepseekHarnessFieldMessage])
	if !ok {
		return "", false
	}
	blocks, ok := providerkit.RawArray(assistant[contracts.DeepseekHarnessFieldContent])
	if !ok {
		return "", false
	}
	index, ok := providerkit.RawWholeNumber(frame[contracts.DeepseekHarnessSupplementBlockIndex])
	if !ok || index < 0 || index >= int64(len(blocks)) {
		return "", false
	}
	block, ok := providerkit.RawObject(blocks[index])
	if !ok || providerkit.ObjectString(block, contracts.DeepseekHarnessFieldType) != contracts.DeepseekHarnessContentTypeText {
		return "", false
	}
	return providerkit.ObjectString(block, "text"), true
}
