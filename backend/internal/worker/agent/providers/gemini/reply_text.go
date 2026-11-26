package gemini

import (
	"encoding/json"
	"slices"
	"strings"

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
	record, ok := providerkit.RawObject(message.Original)
	if !ok || isFinishedGeminiToolRecord(record) {
		return "", false
	}
	id, _ := providerkit.RawString(record["id"])
	recordType, _ := providerkit.RawString(record["type"])
	if id == "" || !slices.Contains(geminiReplyRecordTypes, recordType) {
		return "", false
	}
	supplement := providerkit.RawFields(message.Supplemental)
	if part, _ := providerkit.RawString(supplement[contracts.GeminiSupplementMessagePart]); part != "" && part != contracts.GeminiMessagePartContent {
		return "", false
	}
	return geminiContentText(record["content"])
}

func isFinishedGeminiToolRecord(record map[string]json.RawMessage) bool {
	name, _ := providerkit.RawString(record["name"])
	if name == "" {
		return false
	}
	id, _ := providerkit.RawString(record["id"])
	status, _ := providerkit.RawString(record["status"])
	return strings.HasPrefix(id, name+"__") && len(id) > len(name)+2 &&
		(status == "success" || status == "error")
}

// geminiContentText reads a record's content as the browser's
// geminiNativeText does: a string as it is, or the text of each part that is
// not a thought, joined with nothing between them. ok is false for content of
// any other shape.
func geminiContentText(content json.RawMessage) (string, bool) {
	if text, ok := providerkit.RawString(content); ok {
		return text, true
	}
	parts, ok := providerkit.RawArray(content)
	if !ok {
		return "", false
	}
	var text strings.Builder
	for _, raw := range parts {
		part, ok := providerkit.RawObject(raw)
		if !ok || providerkit.RawBool(part["thought"]) {
			continue
		}
		if value, ok := providerkit.RawString(part["text"]); ok {
			text.WriteString(value)
		}
	}
	return text.String(), true
}
