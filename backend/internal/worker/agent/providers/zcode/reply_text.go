package zcode

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ReplyText reads the reply of a model-response session.updated event, as
// the browser does. ResolveProviderData removes text already stored in an
// earlier row before the reader extracts a recovered reply.
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

func zcodeAcceptedReplyPrefix(supplemental []byte) string {
	var supplement map[string]json.RawMessage
	if json.Unmarshal(supplemental, &supplement) != nil {
		return ""
	}
	var coverage map[string]json.RawMessage
	if json.Unmarshal(supplement[contracts.ZCodeSupplementResponseText], &coverage) != nil {
		return ""
	}
	var prefix string
	_ = json.Unmarshal(coverage[contracts.ZCodeResponseTextAcceptedPrefix], &prefix)
	return prefix
}
