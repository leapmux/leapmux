package letta

import (
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReplyText reads the top-level text that the browser's lettaExtractRow reads
// for an assistant text: a native assistant_message record, and an assembled
// envelope that the shared reader refused as malformed, which Letta still
// draws as text.
func (lettaProvider) ReplyText(message agent.MessageContent) (string, bool) {
	frame, ok := providerkit.RawObject(message.Original)
	if !ok {
		return "", false
	}
	switch providerkit.ObjectString(frame, "type") {
	case contracts.AssembledMessageType:
	case contracts.LettaMessageTurnFinished:
		return "", false
	default:
		if providerkit.ObjectString(frame, contracts.LettaDeltaFieldMessageType) != contracts.LettaDeltaKindAssistantMessage {
			return "", false
		}
	}
	return providerkit.ObjectString(frame, "text"), true
}
