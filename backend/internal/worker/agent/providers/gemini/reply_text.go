package gemini

import (
	"encoding/json"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// geminiReplyRecordTypes lists the types of a session file's message record
// that the browser draws as the agent's text: every type that
// geminiNativeMessage accepts, except the user's.
var geminiReplyRecordTypes = []string{"gemini", "info", "warning", "error"}

// ReplyText reads a message record of a subagent's session file, as the
// browser's geminiNativeRow does. The Worker stores one row for each part of
// a record (geminiChildWrites), and the row's supplement selects the part:
// the content, or one thought. Only the content is a reply. Every other row
// is an ACP frame, whose text reaches the transcript only in an assembled
// envelope.
func (geminiProvider) ReplyText(message agent.MessageContent) (string, bool) {
	if _, isTool := decodeFinishedGeminiTool(message.Original); isTool {
		return "", false
	}
	var record map[string]json.RawMessage
	if json.Unmarshal(message.Original, &record) != nil {
		return "", false
	}
	id, _ := providerkit.RawString(record["id"])
	recordType, _ := providerkit.RawString(record["type"])
	if id == "" || !slices.Contains(geminiReplyRecordTypes, recordType) {
		return "", false
	}
	var supplement map[string]json.RawMessage
	_ = json.Unmarshal(message.Supplemental, &supplement)
	if part, _ := providerkit.RawString(supplement[contracts.GeminiSupplementMessagePart]); part != "" && part != contracts.GeminiMessagePartContent {
		return "", false
	}
	return geminiContentText(record["content"])
}

// geminiContentText reads a record's content as the browser's
// geminiNativeText does: a string as it is, or the text of each part that is
// not a thought, joined with nothing between them. ok is false for content of
// any other shape.
func geminiContentText(content json.RawMessage) (string, bool) {
	if text, ok := providerkit.RawString(content); ok {
		return text, true
	}
	var parts []json.RawMessage
	if providerkit.RawKindOf(content) != providerkit.RawValue || json.Unmarshal(content, &parts) != nil {
		return "", false
	}
	var text string
	for _, raw := range parts {
		var part map[string]json.RawMessage
		if json.Unmarshal(raw, &part) != nil || providerkit.RawBool(part["thought"]) {
			continue
		}
		if value, ok := providerkit.RawString(part["text"]); ok {
			text += value
		}
	}
	return text, true
}
