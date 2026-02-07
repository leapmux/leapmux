package agent

// Claude Code NDJSON message types.
// The worker does NOT parse message content — it forwards verbatim bytes.
// Only the `type` field is used for lifecycle management.

// MessageType represents the type field in an NDJSON line from Claude Code.
type MessageType string

const (
	// Input messages (written to stdin).
	MessageTypeUser MessageType = "user"

	// Output messages (read from stdout).
	MessageTypeSystem    MessageType = "system"
	MessageTypeAssistant MessageType = "assistant"
	MessageTypeResult    MessageType = "result"
)

// MessageEnvelope is used only to extract the `type` field for lifecycle
// management. The full JSON line is forwarded verbatim.
type MessageEnvelope struct {
	Type MessageType `json:"type"`
}

// UserInputMessage is the structure written to Claude Code's stdin
// when using --input-format stream-json.
type UserInputMessage struct {
	Type    MessageType      `json:"type"`
	Message UserInputContent `json:"message"`
}

// UserInputContent is the nested message content for stream-json input.
type UserInputContent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
