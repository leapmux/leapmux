package claude

import "github.com/leapmux/leapmux/generated/contracts"

// Claude Code sends newline-delimited JSON (NDJSON) frames.
// The worker reads native fields and preserves the original bytes.

// MessageType represents the type field in an NDJSON line from Claude Code.
type MessageType string

const (
	// Input messages (written to stdin).
	MessageTypeUser MessageType = contracts.ClaudeFrameKindUser

	// Output messages (read from stdout).
	MessageTypeSystem    MessageType = contracts.ClaudeFrameKindSystem
	MessageTypeAssistant MessageType = contracts.ClaudeFrameKindAssistant
	MessageTypeResult    MessageType = contracts.ClaudeFrameKindResult
)

// MessageEnvelope is used only to extract the `type` field for lifecycle
// management. The full JSON line is forwarded verbatim.
type MessageEnvelope struct {
	Type MessageType `json:"type"`
}

// UserInputMessage is the structure written to Claude Code's stdin
// when using --input-format stream-json.
type UserInputMessage struct {
	Type     MessageType      `json:"type"`
	Message  UserInputContent `json:"message"`
	Priority string           `json:"priority,omitempty"`
}

// UserInputContent is the nested message content for stream-json input.
// Content is string for plain text, or []interface{} for multimodal (text + images/documents).
type UserInputContent struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}
