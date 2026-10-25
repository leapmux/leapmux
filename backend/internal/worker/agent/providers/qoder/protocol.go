package qoder

import "encoding/json"

// Qoder CLI uses newline-delimited JSON (NDJSON) messages.
// The worker preserves stored native frames and consumes protocol state.
// On input, it places attachments before the prompt text.

// These control envelopes use the shared neutral shape.
// The provider owns their native dispatch and cancellation.
const (
	frameTypeControlRequest       = "control_request"
	frameTypeControlResponse      = "control_response"
	frameTypeControlCancelRequest = "control_cancel_request"
)

// These system subtypes carry no transcript content.
const (
	qoderSubtypeHookStarted            = "hook_started"
	qoderSubtypeHookProgress           = "hook_progress"
	qoderSubtypePermissionDenied       = "permission_denied"
	qoderSubtypeElicitationComplete    = "elicitation_complete"
	qoderSubtypeArtifactsUpdate        = "artifacts_update"
	qoderSubtypeCommandsChanged        = "commands_changed"
	qoderSubtypeControlRequestProgress = "control_request_progress"
	qoderSubtypeTaskProgress           = "task_progress"
)

// These words of an mcp_status reply select the MCP servers that
// registerMcpServersForForms registers again. A configured scope identifies
// the source of a server: user, project and local settings, and --mcp-config
// (`flag`). Qoder marks each server of mcp_set_servers as `dynamic`.
const (
	qoderMcpScopeUser       = "user"
	qoderMcpScopeProject    = "project"
	qoderMcpScopeLocal      = "local"
	qoderMcpScopeFlag       = "flag"
	qoderMcpScopeDynamic    = "dynamic"
	qoderMcpStatusConnected = "connected"
	qoderMcpTypeSDK         = "sdk"
)

// The child option that states the kind of native work behind a child agent.
// LeapMux owns these words. Qoder's stop_task stops a child that an Agent tool
// call started (a task of the session), but not a child of a Workflow script,
// so ChildCapabilities offers an interrupt for a task child only.
const (
	qoderOptionChildKind = "child-kind"
	qoderChildKindTask   = "task"
)

// These are Qoder's built-in output styles, as `--output-style` and the
// initialize reply spell them. The output-style group offers them before a
// session reports its own list, which also holds the user's styles.
const (
	qoderOutputStyleDefault     = "default"
	qoderOutputStyleExplanatory = "Explanatory"
	qoderOutputStyleLearning    = "Learning"
)

// MessageEnvelope extracts the `type` field for lifecycle management.
type MessageEnvelope struct {
	Type string `json:"type"`
}

// UserInputMessage is written to stdin with --input-format stream-json.
type UserInputMessage struct {
	Type    string           `json:"type"`
	Message UserInputContent `json:"message"`
}

// UserInputContent is the nested message content.
type UserInputContent struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// qoderTextBlock is a text element of a user input content array.
type qoderTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// qoderImageSource is the base64 image source Qoder accepts on stream input.
type qoderImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// qoderImageBlock is an image element of a user input content array.
type qoderImageBlock struct {
	Type   string           `json:"type"`
	Source qoderImageSource `json:"source"`
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
	Permission   string   `json:"permissionMode"`
	Capabilities []string `json:"capabilities"`
}

// assistantMessage is the part of an assistant line the worker reads.
type assistantMessage struct {
	ParentToolUseID *string `json:"parent_tool_use_id"`
	SessionID       string  `json:"session_id"`
}

// canUseToolAnswer is the object the Qoder control_response carries for a
// can_use_tool decision. The browser sends the neutral behavior envelope; the
// worker translates it here.
//
// Qoder accepts an outcome or a behavior with its associated fields.
// The native outcomes are:
//   - proceed_once
//   - proceed_always
//   - proceed_always_and_save
//   - cancel
//   - modify_with_editor
//
// LeapMux uses behavior to preserve rejection text and changed tool input.
// A rejection states the text in message and reason.
// The behavior reader uses message. The allowed reader uses reason.
// Both readers must deliver the rejection text to the model.
//
// An answer with updatedInput must omit outcome.
// Qoder resolves outcome first and then reads changed input from payload.
// An outcome beside updatedInput would therefore discard the changed input, including AskUserQuestion answers.
type canUseToolAnswer struct {
	Behavior        string         `json:"behavior"`
	Outcome         string         `json:"outcome,omitempty"`
	Message         string         `json:"message,omitempty"`
	Reason          string         `json:"reason,omitempty"`
	UpdatedInput    map[string]any `json:"updatedInput,omitempty"`
	PermissionScope string         `json:"permissionScope,omitempty"`
}
