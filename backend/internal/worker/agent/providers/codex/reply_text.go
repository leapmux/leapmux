package codex

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
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

// ReplyText reads the text of an agentMessage item, as the browser's
// codexExtractRow does. Codex stores an item under three envelopes: a bare
// {item}, a notification's {params: {item}}, or the item itself.
func (codexProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame map[string]json.RawMessage
	if json.Unmarshal(message.Original, &frame) != nil {
		return "", false
	}
	var method string
	_ = json.Unmarshal(frame["method"], &method)
	if codexNonReplyMethods[method] {
		return "", false
	}
	var turn struct {
		Status json.RawMessage `json:"status"`
	}
	var status string
	if json.Unmarshal(frame["turn"], &turn) == nil && json.Unmarshal(turn.Status, &status) == nil && status != "" {
		return "", false
	}
	item := codexItemOf(frame)
	var itemType, text string
	_ = json.Unmarshal(item[contracts.CodexItemType], &itemType)
	if itemType != contracts.CodexItemTypeAgentMessage {
		return "", false
	}
	_ = json.Unmarshal(item["text"], &text)
	return text, true
}

// codexItemOf unwraps an item as the browser's extractItem does.
func codexItemOf(frame map[string]json.RawMessage) map[string]json.RawMessage {
	var item map[string]json.RawMessage
	if json.Unmarshal(frame[contracts.CodexItemEnvelope], &item) == nil && item != nil {
		return item
	}
	var params struct {
		Item map[string]json.RawMessage `json:"item"`
	}
	if json.Unmarshal(frame["params"], &params) == nil && params.Item != nil {
		return params.Item
	}
	var frameType string
	if json.Unmarshal(frame["type"], &frameType) == nil && frameType != "" {
		return frame
	}
	return nil
}
