package codewhale

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// codewhaleFinalItemEvents are the events that end an item (ITEM_FINAL_EVENTS
// in the browser's toolCommon.ts). An item.started event is still open.
var codewhaleFinalItemEvents = map[string]bool{
	contracts.CodewhaleEventItemCompleted:   true,
	contracts.CodewhaleEventItemFailed:      true,
	contracts.CodewhaleEventItemInterrupted: true,
	contracts.CodewhaleEventItemCanceled:    true,
}

// ReplyText reads the reply that the browser's codewhaleMessageText reads: the
// detail of an agent_message item that a final item event carries, or the
// text block of an assistant message in a subagent's own transcript.
func (codewhaleProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame map[string]json.RawMessage
	if json.Unmarshal(message.Original, &frame) != nil {
		return "", false
	}
	var event string
	if json.Unmarshal(frame[contracts.CodewhaleEnvelopeFieldEvent], &event) == nil && event != "" {
		return codewhaleItemReply(event, frame[contracts.CodewhaleEnvelopeFieldPayload])
	}
	return codewhaleChildReply(frame)
}

// codewhaleItemReply reads the detail of an agent_message item.
func codewhaleItemReply(event string, payload json.RawMessage) (string, bool) {
	if !codewhaleFinalItemEvents[event] {
		return "", false
	}
	var body map[string]json.RawMessage
	var item map[string]json.RawMessage
	if json.Unmarshal(payload, &body) != nil || json.Unmarshal(body[contracts.CodewhaleItemFieldItem], &item) != nil || item == nil {
		return "", false
	}
	var kind, detail string
	_ = json.Unmarshal(item[contracts.CodewhaleItemFieldKind], &kind)
	if kind != contracts.CodewhaleItemKindAgentMessage {
		return "", false
	}
	_ = json.Unmarshal(item[contracts.CodewhaleItemFieldDetail], &detail)
	return detail, true
}

// codewhaleChildReply reads the text block of an assistant message in a
// subagent's transcript: a message record with an index and exactly one
// block.
func codewhaleChildReply(frame map[string]json.RawMessage) (string, bool) {
	var kind string
	var index float64
	if json.Unmarshal(frame[contracts.CodewhaleTranscriptFieldKind], &kind) != nil || kind != contracts.CodewhaleTranscriptKindMessage ||
		json.Unmarshal(frame[contracts.CodewhaleTranscriptFieldIndex], &index) != nil {
		return "", false
	}
	var message map[string]json.RawMessage
	var blocks []json.RawMessage
	if json.Unmarshal(frame[contracts.CodewhaleTranscriptFieldMessage], &message) != nil ||
		json.Unmarshal(message[contracts.CodewhaleTranscriptFieldContent], &blocks) != nil || len(blocks) != 1 {
		return "", false
	}
	var role string
	_ = json.Unmarshal(message[contracts.CodewhaleTranscriptFieldRole], &role)
	var block map[string]json.RawMessage
	if role != contracts.CodewhaleTranscriptRoleAssistant || json.Unmarshal(blocks[0], &block) != nil {
		return "", false
	}
	var blockType, text string
	_ = json.Unmarshal(block[contracts.CodewhaleBlockFieldType], &blockType)
	if blockType != contracts.CodewhaleBlockTypeText {
		return "", false
	}
	_ = json.Unmarshal(block[contracts.CodewhaleBlockFieldText], &text)
	return text, true
}
