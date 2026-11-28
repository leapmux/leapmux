package cline

import (
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReplyText reads what the browser's clineTextRow draws as an assistant text:
// the text of an assistant.finished event, or the statement of the media
// that an assistant.media event returned.
func (clineProvider) ReplyText(message agent.MessageContent) (string, bool) {
	frame, ok := providerkit.RawObject(message.Original)
	if !ok {
		return "", false
	}
	payload, _ := providerkit.RawObject(frame[contracts.ClineEventFieldPayload])
	switch providerkit.ObjectString(frame, contracts.ClineEventFieldEvent) {
	case contracts.ClineEventAssistantFinished:
		text := providerkit.ObjectString(payload, "text")
		return text, agent.TrimJavaScriptSpace(text) != ""
	case contracts.ClineEventAssistantMedia:
		media, ok := providerkit.RawObject(payload["media"])
		if !ok {
			return "", false
		}
		for _, field := range []string{"mediaType", "mimeType", "type"} {
			if mediaType := providerkit.ObjectString(media, field); mediaType != "" {
				return "The model returned media (" + mediaType + ").", true
			}
		}
		return "The model returned media.", true
	default:
		return "", false
	}
}
