package codewhale

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codewhaleFinalItemEvents are the events that end an item (ITEM_FINAL_EVENTS
// in the browser's toolCommon.ts). An item.started event is still open.
var codewhaleFinalItemEvents = map[string]bool{
	contracts.CodewhaleEventItemCompleted:   true,
	contracts.CodewhaleEventItemFailed:      true,
	contracts.CodewhaleEventItemInterrupted: true,
	contracts.CodewhaleEventItemCanceled:    true,
}

// codewhaleReplyHead reads only the top-level discriminator fields so non-reply
// rows exit without allocating a map. contract_tags_test.go pins its tags.
type codewhaleReplyHead struct {
	Event json.RawMessage `json:"event"`
	Kind  json.RawMessage `json:"kind"`
}

// ReplyText reads the reply that the browser's codewhaleMessageText reads: the
// detail of an agent_message item that a final item event carries, or the
// text block of an assistant message in a subagent's own transcript.
func (codewhaleProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var head codewhaleReplyHead
	if json.Unmarshal(message.Original, &head) != nil {
		return "", false
	}
	headEvent, _ := providerkit.RawString(head.Event)
	headKind, _ := providerkit.RawString(head.Kind)
	if !codewhaleFinalItemEvents[headEvent] && headKind != contracts.CodewhaleTranscriptKindMessage {
		return "", false
	}
	frame, ok := providerkit.RawObject(message.Original)
	if !ok {
		return "", false
	}
	if event := providerkit.ObjectString(frame, contracts.CodewhaleEnvelopeFieldEvent); event != "" {
		return codewhaleItemReply(event, frame[contracts.CodewhaleEnvelopeFieldPayload])
	}
	return codewhaleChildReply(frame)
}

// codewhaleItemReply reads the detail of an agent_message item.
func codewhaleItemReply(event string, payload json.RawMessage) (string, bool) {
	if !codewhaleFinalItemEvents[event] {
		return "", false
	}
	body, ok := providerkit.RawObject(payload)
	if !ok {
		return "", false
	}
	item, ok := providerkit.RawObject(body[contracts.CodewhaleItemFieldItem])
	if !ok {
		return "", false
	}
	if providerkit.ObjectString(item, contracts.CodewhaleItemFieldKind) != contracts.CodewhaleItemKindAgentMessage {
		return "", false
	}
	return providerkit.ObjectString(item, contracts.CodewhaleItemFieldDetail), true
}

// codewhaleChildReply reads the text block of an assistant message in a
// subagent's transcript: a message record with a finite numeric index and
// exactly one block.
func codewhaleChildReply(frame map[string]json.RawMessage) (string, bool) {
	if providerkit.ObjectString(frame, contracts.CodewhaleTranscriptFieldKind) != contracts.CodewhaleTranscriptKindMessage {
		return "", false
	}
	if _, ok := providerkit.RawFloat64(frame[contracts.CodewhaleTranscriptFieldIndex]); !ok {
		return "", false
	}
	message, ok := providerkit.RawObject(frame[contracts.CodewhaleTranscriptFieldMessage])
	if !ok {
		return "", false
	}
	if providerkit.ObjectString(message, contracts.CodewhaleTranscriptFieldRole) != contracts.CodewhaleTranscriptRoleAssistant {
		return "", false
	}
	blocks, ok := providerkit.RawArray(message[contracts.CodewhaleTranscriptFieldContent])
	if !ok || len(blocks) != 1 {
		return "", false
	}
	block, ok := providerkit.RawObject(blocks[0])
	if !ok {
		return "", false
	}
	if providerkit.ObjectString(block, contracts.CodewhaleBlockFieldType) != contracts.CodewhaleBlockTypeText {
		return "", false
	}
	return providerkit.ObjectString(block, contracts.CodewhaleBlockFieldText), true
}
