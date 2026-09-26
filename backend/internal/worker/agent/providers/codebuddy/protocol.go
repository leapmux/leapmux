package codebuddy

import "encoding/json"

// CodeBuddy Code NDJSON message types.
//
// The stream is Claude Code 2.1.220-shaped, so the envelope family matches
// providers/claude. The worker does not parse message content: it forwards
// verbatim bytes and reads only the `type` field for lifecycle management.

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

// CodeBuddy's can_use_tool answer. This is the single hard incompatibility with
// Claude Code's stream: the CLI-side parser reads `allowed`, not `behavior`.
// Probe r5 proved a `behavior:"allow"` answer comes back as "Permission denied
// by SDK"; probe r6 proved `allowed:true` runs the tool.
//
// The wire shape:
//
//	{"type":"control_response","response":{"subtype":"success","request_id":"…",
//	  "response":{"allowed":true,"updatedInput":{…}}}}
//
// and for a denial:
//
//	{"type":"control_response","response":{"subtype":"success","request_id":"…",
//	  "response":{"allowed":false,"reason":"…","interrupt":false}}}
type canUseToolAnswer struct {
	Allowed      bool           `json:"allowed"`
	Reason       string         `json:"reason,omitempty"`
	Interrupt    bool           `json:"interrupt,omitempty"`
	UpdatedInput map[string]any `json:"updatedInput,omitempty"`
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

// resultMessage is the part of a `result` line the worker reads. CodeBuddy
// hardcodes total_cost_usd to 0 and adds modelUsage and _meta.
type resultMessage struct {
	SessionID    string `json:"session_id"`
	IsError      bool   `json:"is_error"`
	Subtype      string `json:"subtype"`
	NumTurns     int    `json:"num_turns"`
	NumToolUses  int32  `json:"num_tool_uses"`
	TerminalMode string `json:"terminal_reason"`
}

// assistantMessage is the part of an `assistant` line the worker reads.
type assistantMessage struct {
	ParentToolUseID *string `json:"parent_tool_use_id"`
	SessionID       string  `json:"session_id"`
}
