package codebuddy

import "encoding/json"

// CodeBuddy Code NDJSON message types.
//
// The stream is Claude Code 2.1.220-shaped, so the envelope family matches
// providers/claude. The worker forwards native frames and reads selected
// content blocks for tool, child, and session state.

// MessageType is the top-level `type` field of one NDJSON line.
type MessageType string

const (
	// Input messages written to stdin.
	MessageTypeUser           MessageType = "user"
	MessageTypeControlRequest MessageType = "control_request"

	// Output messages read from stdout.
	MessageTypeSystem               MessageType = "system"
	MessageTypeAssistant            MessageType = "assistant"
	MessageTypeResult               MessageType = "result"
	MessageTypeControlRequestOut    MessageType = "control_request"
	MessageTypeControlResponse      MessageType = "control_response"
	MessageTypeControlCancelRequest MessageType = "control_cancel_request"
	MessageTypeToolProgress         MessageType = "tool_progress"
	MessageTypeActiveGoal           MessageType = "active_goal"
	MessageTypeConversationReset    MessageType = "conversation_reset"
	// MessageTypeControlNotification is CodeBuddy-only: a channel permission relay.
	MessageTypeControlNotification MessageType = "control_notification"
	// MessageTypeError is CodeBuddy-only: `{type:"error",error:"..."}` on stream failure.
	MessageTypeError MessageType = "error"
)

// The control-frame names are the CROSS-PROVIDER neutral envelope that shared
// service code already spells. They stay out of the contract for that reason,
// and live here as the provider's own Go-side spelling.
const (
	frameTypeControlRequest  = "control_request"
	frameTypeControlResponse = "control_response"
)

// MessageEnvelope is used only to extract the `type` field for lifecycle
// management. The full JSON line is forwarded verbatim.
type MessageEnvelope struct {
	Type MessageType `json:"type"`
}

// UserInputMessage is the structure written to stdin when using
// --input-format stream-json.
type UserInputMessage struct {
	Type    MessageType      `json:"type"`
	Message UserInputContent `json:"message"`
	// Priority is CodeBuddy's own field. Omitted for a normal prompt.
	Priority string `json:"priority,omitempty"`
}

// UserInputContent is the nested message content for stream-json input.
// Content is a string for plain text, or []interface{} for multimodal blocks.
type UserInputContent struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type codebuddyTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codebuddyMediaSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type codebuddyMediaBlock struct {
	Type   string               `json:"type"`
	Source codebuddyMediaSource `json:"source"`
}

// controlResponseEnvelope is the outer control_response frame. The inner
// Response payload differs by request; this type only carries the routing.
type controlResponseEnvelope struct {
	Response struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Response  json.RawMessage `json:"response"`
		Error     string          `json:"error"`
	} `json:"response"`
}

// The reasons that CodeBuddy gives when its steer handler does not take a steer
// into the running turn (q4 in 2.160.0). A refusal of the content carries no
// reason.
const (
	// codebuddySteerReasonIdle states that no turn runs.
	codebuddySteerReasonIdle = "idle"
	// codebuddySteerReasonStale states that the steer is for a turn that
	// already ended.
	codebuddySteerReasonStale = "stale"
)

// controlRequestEnvelope is the outer control_request frame the CLI sends the
// host, and the host sends the CLI. The Request payload differs by subtype.
type controlRequestEnvelope struct {
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
}

// canUseToolRequest is the payload of an outbound can_use_tool control request.

// systemInitMessage is the part of a `system`/`init` line the worker reads.
// CodeBuddy adds apiKeySource and output_style and drops `agents`.
type systemInitMessage struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	// PermissionMode is the mode the process started in.
	PermissionMode string `json:"permissionMode"`
}

// resultMessage is the part of a result line the worker reads.
type resultMessage struct {
	Result     json.RawMessage                `json:"result"`
	IsError    bool                           `json:"is_error"`
	Errors     []string                       `json:"errors"`
	SessionID  string                         `json:"session_id"`
	Usage      *codebuddyUsage                `json:"usage"`
	ModelUsage map[string]codebuddyModelUsage `json:"modelUsage"`
	Meta       struct {
		ContextUsed *int64 `json:"codebuddy.ai/contextUsed"`
	} `json:"_meta"`
	// AbortReason is set by CodeBuddy 2.158.0 and later on the `result` of an
	// aborted turn, and on no other `result`.
	AbortReason string `json:"terminal_reason"`
}

// The `terminal_reason` values that state a turn ended in an abort, as CodeBuddy
// spells them. CodeBuddy 2.160.0 writes the first when a stop arrives while the
// model streams, and the second when it arrives while a tool runs.
const (
	codebuddyAbortReasonAbortedStreaming = "aborted_streaming"
	codebuddyAbortReasonAbortedTools     = "aborted_tools"
)

// statesAbortedTurn reports whether a `result` states that the stop took effect, so
// the turn ended in an abort. A turn that ended some other way before the CLI read the
// stop keeps its own outcome.
//
// `terminal_reason` is the only statement. CodeBuddy ends an abort with the SUCCESS
// shape (`subtype: success`, `is_error: false`), so neither `subtype` nor `is_error`
// tells an abort from a finished turn. The Claude Code and Qoder providers read
// `is_error: false` as a finished turn. That rule is impossible here, because it
// would read each stop of CodeBuddy as a finished turn. A failure writes
// `subtype: error_during_execution` and `is_error: true`, with no reason.
//
// A CodeBuddy before 2.158.0 writes no `terminal_reason`. A stop on that CLI
// therefore ends as a finished turn. No other field states the abort, and the
// provider keeps no table of CLI versions.
func (r *resultMessage) statesAbortedTurn() bool {
	return r.AbortReason == codebuddyAbortReasonAbortedStreaming ||
		r.AbortReason == codebuddyAbortReasonAbortedTools
}

type codebuddyUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

type codebuddyModelUsage struct {
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	ContextWindow            int64 `json:"contextWindow"`
}

// assistantMessage is the part of an `assistant` line the worker reads.
type assistantMessage struct {
	ParentToolUseID *string `json:"parent_tool_use_id"`
	SessionID       string  `json:"session_id"`
	Message         struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content"`
	} `json:"message"`
}
