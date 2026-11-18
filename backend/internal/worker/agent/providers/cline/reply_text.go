package cline

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ReplyText reads what the browser's clineTextRow draws as an assistant text:
// the text of an assistant.finished event, or the statement of the media
// that an assistant.media event returned.
func (clineProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame map[string]json.RawMessage
	if json.Unmarshal(message.Original, &frame) != nil {
		return "", false
	}
	var event string
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(frame[contracts.ClineEventFieldEvent], &event)
	_ = json.Unmarshal(frame[contracts.ClineEventFieldPayload], &payload)
	switch event {
	case contracts.ClineEventAssistantFinished:
		var text string
		_ = json.Unmarshal(payload["text"], &text)
		return text, strings.TrimSpace(text) != ""
	case contracts.ClineEventAssistantMedia:
		var media map[string]json.RawMessage
		if json.Unmarshal(payload["media"], &media) != nil || media == nil {
			return "", false
		}
		for _, field := range []string{"mediaType", "mimeType", "type"} {
			var mediaType string
			if json.Unmarshal(media[field], &mediaType) == nil && mediaType != "" {
				return "The model returned media (" + mediaType + ").", true
			}
		}
		return "The model returned media.", true
	default:
		return "", false
	}
}
