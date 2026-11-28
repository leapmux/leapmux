package kimi

import (
	"encoding/json"
	"regexp"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

var kimiBlockedHookResult = regexp.MustCompile(`(?s)\A<hook_result hook_event="[^"<>\n]+">\n.*\n</hook_result>\z`)

// ReplyText reads the canonical assistant item and its accepted rendering view.
// The browser displays a recognized blocked hook result as a notification.
func (kimiProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var head struct {
		Type string `json:"type"`
		Item struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if json.Unmarshal(message.Original, &head) != nil ||
		head.Type != contracts.KimiConversationRecordKindItem ||
		head.Item.Type != contracts.KimiConversationItemKindAssistantMessage {
		return "", false
	}
	view, err := decodeKimiConversationRecord(message.Original)
	if err != nil || view.item == nil || view.item.Type != contracts.KimiConversationItemKindAssistantMessage {
		return "", false
	}
	if message.AgentSessionID == view.record.SessionID && len(message.Supplemental) != 0 {
		if latest, _, err := view.resolveLatestView(message.Supplemental); err == nil {
			view = latest
		}
	}
	text := *view.item.Text
	turn := view.turnContext
	if turn != nil && turn.Kind == contracts.KimiConversationTurnKindAgent && turn.Status == contracts.KimiConversationStatusEnded &&
		turn.Outcome == contracts.KimiConversationOutcomeFailed && turn.EndReason == contracts.KimiConversationEndReasonBlocked &&
		kimiBlockedHookResult.MatchString(text) {
		return "", false
	}
	return text, true
}
