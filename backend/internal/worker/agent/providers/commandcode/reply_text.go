package commandcode

import (
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReplyText reads the text of a message_end event, as the browser's
// commandCodeText does: its text blocks, joined with nothing between them,
// in their exact order and white space.
func (commandcodeProvider) ReplyText(message agent.MessageContent) (string, bool) {
	frame, ok := providerkit.RawObject(message.Original)
	if !ok || providerkit.ObjectString(frame, "type") != contracts.CommandCodeFrameKindEvent {
		return "", false
	}
	event, ok := providerkit.RawObject(frame["event"])
	if !ok || providerkit.ObjectString(event, "type") != contracts.CommandCodeEventMessageEnd {
		return "", false
	}
	return nativeText(event["content"]), true
}
