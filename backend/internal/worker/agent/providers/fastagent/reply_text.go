package fastagent

import (
	"encoding/json"
	"regexp"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// fastagentArchiveHistoryFile is the name of a history file that an archive
// row may state, as the browser's nativeHistoryFile accepts it.
var fastagentArchiveHistoryFile = regexp.MustCompile(`^history_[^/\\\x00\r\n]+\.json$`)

// ReplyText reads a row of a subagent's history archive, as the browser's
// fastAgentExtractRow does. The Worker stores each element of the archive as
// a row of its own (makeFastagentArchiveRow), and the row's supplement states
// its provenance. Two kinds of element are the agent's text: a text block of
// a message's content, and a text block of the error channel. Every other row
// is an ACP frame, whose text reaches the transcript only in an assembled
// envelope.
func (fastagentProvider) ReplyText(message agent.MessageContent) (string, bool) {
	supplement, ok := providerkit.RawObject(message.Supplemental)
	if !ok {
		return "", false
	}
	meta, ok := providerkit.RawObject(supplement[contracts.FastagentArchiveNamespace])
	if !ok || !validFastagentArchiveIdentity(meta, message.AgentSessionID) {
		return "", false
	}
	field, _ := providerkit.RawString(meta[contracts.FastagentArchiveField])
	switch field {
	case contracts.FastagentArchiveContent:
	case contracts.FastagentArchiveChannels:
		if channel, _ := providerkit.RawString(meta[contracts.FastagentArchiveChannel]); channel != contracts.FastagentArchiveErrorChannel {
			return "", false
		}
	default:
		return "", false
	}
	if !fastagentArchiveIndex(meta[contracts.FastagentArchiveIndex]) {
		return "", false
	}
	block, ok := providerkit.RawObject(message.Original)
	if !ok {
		return "", false
	}
	blockType, _ := providerkit.RawString(block["type"])
	text, isString := providerkit.RawString(block["text"])
	if blockType != "text" || !isString {
		return "", false
	}
	return text, true
}

// validFastagentArchiveIdentity checks the provenance of an archive row, as
// the browser's fastAgentArchiveSource does. The row must belong to the
// session that the row states, so a row of another session reads as no
// archive row.
func validFastagentArchiveIdentity(meta map[string]json.RawMessage, agentSessionID string) bool {
	nonempty := func(key string) (string, bool) {
		value, ok := providerkit.RawString(meta[key])
		return value, ok && value != ""
	}
	if format, _ := providerkit.RawString(meta[contracts.FastagentArchiveFormatKey]); format != contracts.FastagentArchiveFormat {
		return false
	}
	sessionID, ok := nonempty(contracts.FastagentArchiveSessionID)
	if !ok || sessionID != agentSessionID {
		return false
	}
	if _, ok := nonempty(contracts.FastagentArchiveCaptureID); !ok {
		return false
	}
	if _, ok := nonempty(contracts.FastagentArchiveActor); !ok {
		return false
	}
	if historyFile, _ := providerkit.RawString(meta[contracts.FastagentArchiveHistoryFile]); !fastagentArchiveHistoryFile.MatchString(historyFile) {
		return false
	}
	if !fastagentArchiveIndex(meta[contracts.FastagentArchiveMessageIndex]) {
		return false
	}
	if role, _ := providerkit.RawString(meta[contracts.FastagentArchiveRole]); role != contracts.FastagentArchiveAssistantRole && role != contracts.FastagentArchiveUserRole {
		return false
	}
	// A timestamp is optional, but a present one must be a string; JSON null
	// is present.
	if timestamp, present := meta[contracts.FastagentArchiveTimestamp]; present {
		if _, ok := providerkit.RawString(timestamp); !ok {
			return false
		}
	}
	return true
}

// fastagentArchiveIndex reports an index that the browser's nativeIndex
// accepts: a JSON number that is a non-negative safe integer of JavaScript,
// whatever its spelling (2, 2.0 or 2e0).
func fastagentArchiveIndex(raw json.RawMessage) bool {
	value, ok := providerkit.RawWholeNumber(raw)
	return ok && value >= 0
}
