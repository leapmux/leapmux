package muse

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ReplyText reads the text of an agentMessage item, as the browser's
// museExtractRow does. Only Muse's item notifications carry an item, and an
// item needs its id and its kind. Unprojected view frames marked with
// nativeViewProjectionUnavailable for the same session are hidden.
func (museProvider) ReplyText(message agent.MessageContent) (string, bool) {
	frame, ok := providerkit.RawObject(message.Original)
	if !ok {
		return "", false
	}
	params, ok := providerkit.RawObject(frame["params"])
	if !ok {
		return "", false
	}
	if isMuseViewProjectionUnavailable(params, message.Supplemental) {
		return "", false
	}
	switch providerkit.ObjectString(frame, "method") {
	case contracts.MuseMethodItemStarted, contracts.MuseMethodItemUpdated, contracts.MuseMethodItemCompleted:
	default:
		return "", false
	}
	item, ok := providerkit.RawObject(params["item"])
	if !ok {
		return "", false
	}
	if providerkit.ObjectString(item, "itemId") == "" || providerkit.ObjectString(item, "kind") != contracts.MuseItemKindAgentMessage {
		return "", false
	}
	text := providerkit.ObjectString(item, "text")
	return text, text != ""
}

// isMuseViewProjectionUnavailable mirrors frontend museViewProjectionUnavailable:
// a frame is hidden when supplemental.nativeViewProjectionUnavailable has a
// non-blank sessionId matching params.sessionId and a non-blank reason.
func isMuseViewProjectionUnavailable(params map[string]json.RawMessage, supplemental []byte) bool {
	supplement, ok := providerkit.RawObject(supplemental)
	if !ok {
		return false
	}
	failure, ok := providerkit.RawObject(supplement[contracts.MuseSupplementFieldViewProjectionUnavailable])
	if !ok {
		return false
	}
	sessionID := providerkit.ObjectString(failure, "sessionId")
	return agent.TrimJavaScriptSpace(sessionID) != "" &&
		providerkit.ObjectString(params, "sessionId") == sessionID &&
		agent.TrimJavaScriptSpace(providerkit.ObjectString(failure, "reason")) != ""
}
