package kimi

import (
	"regexp"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

var kimiBlockedHookResult = regexp.MustCompile(`(?s)\A<hook_result hook_event="[^"<>\n]+">\n.*\n</hook_result>\z`)

// ReplyText reads the canonical assistant item and its accepted rendering view.
// The browser displays a recognized blocked hook result as a notification.
func (kimiProvider) ReplyText(message agent.MessageContent) (string, bool) {
	view, err := decodeKimiConversationRecord(message.Original)
	if err != nil || view.item == nil || view.item.Type != contracts.KimiConversationItemKindAssistantMessage {
		return "", false
	}
	if message.AgentSessionID == view.record.SessionID && len(message.Supplemental) != 0 {
		fields, err := kimiConversationProviderFields(message.Supplemental)
		if raw := fields[contracts.KimiConversationSupplementRoot]; err == nil && len(raw) != 0 {
			if latest, err := decodeKimiConversationRecord(raw); err == nil && view.acceptsView(latest) {
				view = latest
			}
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
