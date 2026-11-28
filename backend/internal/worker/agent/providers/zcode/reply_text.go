package zcode

import (
	"encoding/json"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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
	coverage := providerkit.RawFields(providerkit.RawFields(supplemental)[contracts.ZCodeSupplementResponseText])
	return providerkit.ObjectString(coverage, contracts.ZCodeResponseTextAcceptedPrefix)
}

func zcodeResolveReplyCoverage(event zcodeEventEnvelope, content agent.MessageContent) []byte {
	prefix := zcodeAcceptedReplyPrefix(content.Supplemental)
	if prefix == "" {
		return content.Original
	}
	payload := providerkit.RawFields(event.Payload)
	update := zcodeSessionUpdatedPayload{
		Type:             payload["type"],
		RequestID:        payload["requestId"],
		TaskID:           payload["taskId"],
		AgentID:          payload["agentId"],
		ParentToolCallID: payload["parentToolCallId"],
		Content:          payload["content"],
		StopReason:       payload["stopReason"],
	}
	if update.kind() != zcodeSessionUpdateModelResponse {
		return content.Original
	}
	text, ok := providerkit.RawString(payload["content"])
	if !ok {
		return content.Original
	}
	suffix, ok := strings.CutPrefix(text, prefix)
	if !ok {
		return content.Original
	}
	payload["content"] = providerkit.JSONText(suffix)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return content.Original
	}
	resolved := event.withPayload(encoded).persistBytes()
	if resolved == nil {
		return content.Original
	}
	return resolved
}
