package zcode

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ReplyText reads the reply of a model-response session.updated event, as
// the browser's zcodeAssistantText does. The event states the whole reply
// under `content`; a turn that only called tools states an empty one, which
// is no reply.
func (zcodeProvider) ReplyText(message agent.MessageContent) (string, bool) {
	event, ok := parseZCodeEvent(message.Original)
	if !ok || event.Type != contracts.ZCodeEventSessionUpdated {
		return "", false
	}
	var payload zcodeSessionUpdatedPayload
	if json.Unmarshal(event.Payload, &payload) != nil || payload.kind() != zcodeSessionUpdateModelResponse {
		return "", false
	}
	var text string
	_ = json.Unmarshal(payload.Content, &text)
	return text, true
}
