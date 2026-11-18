package droid

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReplyText reads the top-level text of an assembled envelope that the shared
// reader refused as malformed, as the browser's droidExtractRow does. Only
// such an envelope of the `text` kind reaches it: the browser classifies it
// as assistant text by the kind column alone. Droid's native frames hold no
// reply; the Worker assembles the assistant's text into the envelope.
func (droidProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame map[string]json.RawMessage
	if json.Unmarshal(message.Original, &frame) != nil {
		return "", false
	}
	envelopeType, _ := providerkit.RawString(frame[contracts.AssembledMessageFieldType])
	kind, _ := providerkit.RawString(frame[contracts.AssembledMessageFieldKind])
	if envelopeType != contracts.AssembledMessageType || kind != contracts.AssembledMessageKindText {
		return "", false
	}
	// The browser reads a text that is not a string as empty.
	text, _ := providerkit.RawString(frame[contracts.AssembledMessageFieldText])
	return text, true
}
