package opencode

import (
	"encoding/json"
	"errors"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Native notification classification selects the reducer for both live and recovered records.
func (run *openCodeRun) persistNativeRecord(content agent.MessageContent) error {
	if (opencodeProvider{}).Classify(content.Original).Consolidatable() {
		_, err := run.sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
		return err
	}
	return run.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{})
}

// recoverOpenCodeAssistantOutput keeps native content order across text and tools.
func recoverOpenCodeAssistantOutput(messageID string, snapshot *openCodeAssistantSnapshot, text *openCodeTextStreams, tools *openCodeToolStreams) error {
	if snapshot == nil || snapshot.Content == nil {
		return errors.New("the recovered OpenCode assistant requires a decoded content array")
	}
	ordinals := make(map[string]int)
	for _, raw := range *snapshot.Content {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return err
		}
		switch header.Type {
		case "text", "reasoning":
			ordinal := ordinals[header.Type]
			ordinals[header.Type]++
			if err := text.recoverPart(messageID, ordinal, raw, snapshot); err != nil {
				return err
			}
		case "tool":
			if err := tools.recoverPart(messageID, raw); err != nil {
				return err
			}
		}
	}
	return nil
}
