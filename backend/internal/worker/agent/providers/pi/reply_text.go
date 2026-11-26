package pi

import (
	"encoding/json"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// piPlanReadyNotice is the line that the browser draws for the end of a
// plan_mode_complete call whose result repeats the plan (plan.ts).
const piPlanReadyNotice = "Plan ready for review."

// ReplyText reads the text that the browser's piExtractRow draws as an
// assistant text:
//
//   - The message_end of an assistant message: its text blocks, joined into
//     paragraphs. The Worker stores the thinking as a row of its own.
//   - The message_end of a custom message that the extension displays, other
//     than a subagent notification, and an appended custom_message entry that
//     it displays: the content string, or its text blocks.
//   - A plan_mode_complete call that states a text rather than a plan.
//
// The browser also draws an image block as Markdown. A reply that a terminal
// prints leaves the image out, because its Markdown is a data URL.
func (piProvider) ReplyText(message agent.MessageContent) (string, bool) {
	var frame struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
		Entry   json.RawMessage `json:"entry"`
	}
	if json.Unmarshal(message.Original, &frame) != nil {
		return "", false
	}
	switch frame.Type {
	case contracts.PiEventEntryAppended:
		var entry piReplyMessage
		if json.Unmarshal(frame.Entry, &entry) != nil || entry.Type != contracts.PiEntryTypeCustomMessage || !entry.displayed(false) {
			return "", false
		}
		return entry.text(true)
	case contracts.PiEventMessageEnd:
		var message piReplyMessage
		if json.Unmarshal(frame.Message, &message) != nil {
			return "", false
		}
		switch message.Role {
		case contracts.PiRoleAssistant:
			return message.text(false)
		case contracts.PiRoleCustom:
			if !message.displayed(true) || message.isSubagentNotification() {
				return "", false
			}
			return message.text(true)
		default:
			return "", false
		}
	case contracts.PiEventToolExecutionStart, contracts.PiEventToolExecutionEnd:
		return piPlanStatementText(message.Original)
	default:
		return "", false
	}
}

// piReplyMessage is a Pi message or session entry that can carry a reply.
type piReplyMessage struct {
	Type       string          `json:"type"`
	Role       string          `json:"role"`
	CustomType string          `json:"customType"`
	Display    *bool           `json:"display"`
	Content    json.RawMessage `json:"content"`
	Details    json.RawMessage `json:"details"`
}

// displayed reports the display flag. A custom message hides only with an
// explicit false; an appended entry shows only with an explicit true.
func (m piReplyMessage) displayed(absentShows bool) bool {
	if m.Display == nil {
		return absentShows
	}
	return *m.Display
}

// text reads the content as piContentText does: a custom message may carry
// one plain string, and every other content is a block array whose text
// blocks join into paragraphs.
func (m piReplyMessage) text(custom bool) (string, bool) {
	if custom {
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			return text, true
		}
	}
	var blocks []json.RawMessage
	if json.Unmarshal(m.Content, &blocks) != nil {
		return "", false
	}
	return providerkit.JoinParagraphs(providerkit.ReadAnthropicContent(blocks).Texts), true
}

// isSubagentNotification reports a subagent notification that lists at least
// one child with its status, which the browser draws as an agent result
// rather than as text (piSubagentNotifications).
func (m piReplyMessage) isSubagentNotification() bool {
	if m.CustomType != contracts.PiCustomTypeSubagentNotification {
		return false
	}
	var details map[string]json.RawMessage
	if json.Unmarshal(m.Details, &details) != nil {
		return false
	}
	entries := []json.RawMessage{m.Details}
	var others []json.RawMessage
	if json.Unmarshal(details["others"], &others) == nil {
		entries = append(entries, others...)
	}
	for _, raw := range entries {
		var entry struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if json.Unmarshal(raw, &entry) == nil && entry.ID != "" && entry.Status != "" {
			return true
		}
	}
	return false
}

// piPlanStatementText reads what a plan_mode_complete row states when it
// states a text rather than a plan, as the browser's piPlanStatement does:
// the reason of a failed call, the notice for a result that repeats the plan,
// or the result text. The start event, which carries the plan, is a plan.
func piPlanStatementText(content []byte) (string, bool) {
	var call struct {
		ToolCallID    string          `json:"toolCallId"`
		ToolName      string          `json:"toolName"`
		Args          json.RawMessage `json:"args"`
		Result        json.RawMessage `json:"result"`
		PartialResult json.RawMessage `json:"partialResult"`
		IsError       bool            `json:"isError"`
	}
	if json.Unmarshal(content, &call) != nil || call.ToolCallID == "" || call.ToolName != contracts.PiToolPlanComplete {
		return "", false
	}
	var args struct {
		Plan string `json:"plan"`
	}
	if json.Unmarshal(call.Args, &args) == nil && agent.TrimJavaScriptSpace(args.Plan) != "" {
		return "", false
	}
	result, hasResult := piToolResultOf(call.Result)
	partial, hasPartial := piToolResultOf(call.PartialResult)
	resultText := ""
	switch {
	case hasResult:
		resultText = agent.TrimJavaScriptSpace(result.text)
	case hasPartial:
		resultText = agent.TrimJavaScriptSpace(partial.text)
	}
	if !call.IsError && hasResult && result.statesPlan {
		return piPlanReadyNotice, true
	}
	return resultText, resultText != ""
}

// piToolResult is the reading of a Pi tool result body: the paragraphs of its
// text blocks, and whether its details carry a plan.
type piToolResult struct {
	text       string
	statesPlan bool
}

func piToolResultOf(raw json.RawMessage) (piToolResult, bool) {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return piToolResult{}, false
	}
	// A field of another shape reads as empty, as the browser reads it.
	var blocks []json.RawMessage
	_ = json.Unmarshal(body["content"], &blocks)
	var details map[string]json.RawMessage
	_ = json.Unmarshal(body["details"], &details)
	var plan string
	_ = json.Unmarshal(details["plan"], &plan)
	return piToolResult{
		text:       providerkit.JoinParagraphs(providerkit.ReadAnthropicContent(blocks).Texts),
		statesPlan: agent.TrimJavaScriptSpace(plan) != "",
	}, true
}
