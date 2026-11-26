package codex

import (
	"bytes"
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codexNonReplyMethods are the methods that the browser draws as a
// notification or hides before it reads an item (CODEX_NOTIF_METHODS and
// CODEX_HIDDEN_TRANSCRIPT_METHODS in classification.ts).
var codexNonReplyMethods = map[string]bool{
	contracts.CodexMethodRawResponseItemCompleted:      true,
	contracts.CodexMethodHookCompleted:                 true,
	contracts.CodexMethodMcpServerOauthLoginCompleted:  true,
	contracts.CodexMethodMcpServerStartupStatusUpdated: true,
	contracts.CodexMethodError:                         true,
	contracts.CodexMethodWarning:                       true,
	contracts.CodexMethodConfigWarning:                 true,
	contracts.CodexMethodDeprecationNotice:             true,
	contracts.CodexMethodModelSafetyBufferingUpdated:   true,
	contracts.CodexMethodModelVerification:             true,
	contracts.CodexMethodAuthRecoveryStarted:           true,
	contracts.CodexMethodAuthRecoveryCompleted:         true,
	contracts.CodexMethodAutoApprovalReviewCompleted:   true,
	contracts.CodexMethodGuardianWarning:               true,
	contracts.CodexMethodStrictReviewRequired:          true,
}

type codexReplyItem struct {
	Type json.RawMessage `json:"type"`
	Text json.RawMessage `json:"text"`
}

// ReplyText reads the text of an agentMessage item, as the browser's
// codexExtractRow does. Codex stores an item under three envelopes: a bare
// {item}, a notification's {params: {item}}, or the item itself.
func (codexProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame struct {
		Method json.RawMessage `json:"method"`
		Turn   json.RawMessage `json:"turn"`
		Item   json.RawMessage `json:"item"`
		Params json.RawMessage `json:"params"`
		Type   json.RawMessage `json:"type"`
		Text   json.RawMessage `json:"text"`
	}
	if json.Unmarshal(message.Original, &frame) != nil {
		return "", false
	}
	if method, ok := providerkit.RawString(frame.Method); ok && codexNonReplyMethods[method] {
		return "", false
	}
	if len(frame.Turn) > 0 {
		var turn struct {
			Status json.RawMessage `json:"status"`
		}
		if json.Unmarshal(frame.Turn, &turn) == nil {
			if status, ok := providerkit.RawString(turn.Status); ok && status != "" {
				return "", false
			}
		}
	}
	item, ok := codexReplyItemOf(frame.Item, frame.Params, frame.Type, frame.Text)
	if !ok {
		return "", false
	}
	itemType, _ := providerkit.RawString(item.Type)
	if itemType != contracts.CodexItemTypeAgentMessage {
		return "", false
	}
	text, _ := providerkit.RawString(item.Text)
	return text, true
}

// codexReplyItemOf unwraps an item as the browser's extractItem does.
func codexReplyItemOf(rawItem, rawParams, rawType, rawText json.RawMessage) (codexReplyItem, bool) {
	if trimmed := bytes.TrimSpace(rawItem); len(trimmed) > 0 && trimmed[0] == '{' {
		var item codexReplyItem
		if json.Unmarshal(trimmed, &item) == nil {
			return item, true
		}
	}
	if trimmed := bytes.TrimSpace(rawParams); len(trimmed) > 0 && trimmed[0] == '{' {
		var params struct {
			Item json.RawMessage `json:"item"`
		}
		if json.Unmarshal(trimmed, &params) == nil {
			if inner := bytes.TrimSpace(params.Item); len(inner) > 0 && inner[0] == '{' {
				var item codexReplyItem
				if json.Unmarshal(inner, &item) == nil {
					return item, true
				}
			}
		}
	}
	if frameType, ok := providerkit.RawString(rawType); ok && frameType != "" {
		return codexReplyItem{Type: rawType, Text: rawText}, true
	}
	return codexReplyItem{}, false
}
