package muse

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// ReplyText reads the text of an agentMessage item, as the browser's
// museExtractRow does. Only Muse's item notifications carry an item, and an
// item needs its id and its kind.
func (museProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame struct {
		Method string `json:"method"`
		Params struct {
			Item struct {
				ItemID string          `json:"itemId"`
				Kind   string          `json:"kind"`
				Text   json.RawMessage `json:"text"`
			} `json:"item"`
		} `json:"params"`
	}
	if json.Unmarshal(message.Original, &frame) != nil {
		return "", false
	}
	switch frame.Method {
	case contracts.MuseMethodItemStarted, contracts.MuseMethodItemUpdated, contracts.MuseMethodItemCompleted:
	default:
		return "", false
	}
	item := frame.Params.Item
	if item.ItemID == "" || item.Kind != contracts.MuseItemKindAgentMessage {
		return "", false
	}
	var text string
	_ = json.Unmarshal(item.Text, &text)
	return text, true
}
