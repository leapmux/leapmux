package qoder

import "encoding/json"

// Qoder CLI NDJSON message types. The worker does not parse message content; it
// forwards verbatim bytes and reads only the `type` field for lifecycle.

// MessageType is the top-level `type` field of one NDJSON line.
type MessageType string

const (
	MessageTypeUser           MessageType = "user"
	MessageTypeControlRequest MessageType = "control_request"
	MessageTypeSystem         MessageType = "system"
	MessageTypeAssistant      MessageType = "assistant"
	MessageTypeResult         MessageType = "result"
)

// The control-frame names are the CROSS-PROVIDER neutral envelope that shared
// service code already spells. They stay out of the contract for that reason,
// and live here as the provider's own Go-side spelling.
const (
	frameTypeControlRequest  = "control_request"
	frameTypeControlResponse = "control_response"
)

// MessageEnvelope extracts the `type` field for lifecycle management.
type MessageEnvelope struct {
	Type MessageType `json:"type"`
}

// UserInputMessage is written to stdin with --input-format stream-json.
type UserInputMessage struct {
	Type    MessageType      `json:"type"`
	Message UserInputContent `json:"message"`
}

// UserInputContent is the nested message content.
type UserInputContent struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// controlResponseEnvelope routes a control_response to its pending request.
type controlResponseEnvelope struct {
	Response struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Response  json.RawMessage `json:"response"`
		Error     string          `json:"error"`
	} `json:"response"`
}

// controlRequestEnvelope is a control_request in either direction.
type controlRequestEnvelope struct {
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
}

// systemInitMessage is the part of a system/init line the worker reads.
type systemInitMessage struct {
	SessionID    string   `json:"session_id"`
	Model        string   `json:"model"`
	Permission   string   `json:"permissionMode"`
	Capabilities []string `json:"capabilities"`
}

// resultMessage is the part of a result line the worker reads.

// assistantMessage is the part of an assistant line the worker reads.
type assistantMessage struct {
	ParentToolUseID *string `json:"parent_tool_use_id"`
	SessionID       string  `json:"session_id"`
}

// canUseToolAnswer is the object the Qoder control_response carries for a
// can_use_tool decision. The browser sends the neutral behavior envelope; the
// worker translates it here.
//
// Qoder's own answer reader accepts two spellings. A decision can state an
// `outcome` (proceed_once, proceed_always, proceed_always_and_save, cancel,
// modify_with_editor) or a `behavior` with the fields that go with it. LeapMux
// states `behavior`, because that is the shape that carries the two things a
// decision needs: the user's rejection words and a modified tool input.
//
// A rejection carries the words on BOTH `message` and `reason`: the reader
// takes `message` on the `behavior` spelling and `reason` on the `allowed`
// spelling, and a deny must reach the model whichever reader runs.
//
// `updatedInput` must travel WITHOUT an `outcome`. The reader resolves an
// explicit outcome first and then takes the modified input from a `payload`
// field, so an answer that states both `outcome:"proceed_once"` and
// `updatedInput` drops the input -- which is exactly what an AskUserQuestion
// reply would lose.
type canUseToolAnswer struct {
	Behavior        string         `json:"behavior"`
	Outcome         string         `json:"outcome,omitempty"`
	Message         string         `json:"message,omitempty"`
	Reason          string         `json:"reason,omitempty"`
	UpdatedInput    map[string]any `json:"updatedInput,omitempty"`
	PermissionScope string         `json:"permissionScope,omitempty"`
}
