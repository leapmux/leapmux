package agent

// Pi wire-protocol vocabulary. The Pi RPC stream is a JSONL feed where
// every envelope carries a top-level `type` field naming an event, and
// extension UI requests carry a `method` field naming a dialog kind.
// Tool dispatch keys off lowercase tool names. Centralizing the strings
// turns rename mistakes into compile errors and gives the dispatch
// switches a single source of truth.

// Pi event types — the top-level "type" field on every JSONL envelope
// emitted by the Pi process. The set mirrors the dispatch switch in
// handlePiOutput.
const (
	PiEventAgentStart          = "agent_start"
	PiEventAgentEnd            = "agent_end"
	PiEventTurnStart           = "turn_start"
	PiEventTurnEnd             = "turn_end"
	PiEventMessageStart        = "message_start"
	PiEventMessageUpdate       = "message_update"
	PiEventMessageEnd          = "message_end"
	PiEventToolExecutionStart  = "tool_execution_start"
	PiEventToolExecutionEnd    = "tool_execution_end"
	PiEventToolExecutionUpdate = "tool_execution_update"
	PiEventExtensionUIRequest  = "extension_ui_request"
	PiEventExtensionError      = "extension_error"
	PiEventCompactionStart     = "compaction_start"
	PiEventCompactionEnd       = "compaction_end"
	PiEventAutoRetryStart      = "auto_retry_start"
	PiEventAutoRetryEnd        = "auto_retry_end"
	PiEventQueueUpdate         = "queue_update"
	PiEventResponse            = "response"
)

// Pi message_update assistantMessageEvent sub-types — carried inside
// `message_update` envelopes. text_delta and thinking_delta are the
// streaming deltas; the others (start/stop/done/error) bracket the
// stream and are no-ops for UI rendering today.
const (
	PiAssistantEventTextDelta     = "text_delta"
	PiAssistantEventThinkingDelta = "thinking_delta"
)

// Pi extension_ui_request methods.
//
// Dialog methods (select / confirm / input / editor) block waiting for
// an extension_ui_response and surface as control requests. The
// fire-and-forget methods drive session-info or notification updates.
const (
	PiDialogMethodSelect  = "select"
	PiDialogMethodConfirm = "confirm"
	PiDialogMethodInput   = "input"
	PiDialogMethodEditor  = "editor"

	PiExtensionMethodNotify        = "notify"
	PiExtensionMethodSetStatus     = "setStatus"
	PiExtensionMethodSetWidget     = "setWidget"
	PiExtensionMethodSetTitle      = "setTitle"
	PiExtensionMethodSetEditorText = "set_editor_text"
)

// Pi tool names — the canonical lowercase identifiers Pi uses on
// `tool_execution_start` / `tool_execution_end` envelopes. The frontend
// dispatches result renderers off these identifiers, so they must
// match the wire format exactly.
const (
	PiToolBash  = "bash"
	PiToolRead  = "read"
	PiToolEdit  = "edit"
	PiToolWrite = "write"
)

// Pi RPC command methods — the "type" field on JSONL commands the
// worker writes to Pi's stdin. Pi replies with a matching {type:
// "response", id} envelope.
const (
	PiCommandPrompt             = "prompt"
	PiCommandAbort              = "abort"
	PiCommandSetModel           = "set_model"
	PiCommandSetThinkingLevel   = "set_thinking_level"
	PiCommandGetSessionStats    = "get_session_stats"
	PiCommandGetState           = "get_state"
	PiCommandGetAvailableModels = "get_available_models"
	PiCommandNewSession         = "new_session"
	PiCommandSwitchSession      = "switch_session"
)

// Pi `prompt` command's `streamingBehavior` — how Pi should treat a new
// prompt that arrives while a turn is already streaming. "steer" injects
// the new message into the in-flight turn; the default (omit) starts a
// fresh turn.
const PiStreamingBehaviorSteer = "steer"

// Pi tool-result content-block types — `tool_execution_*` partialResult
// and result envelopes carry an array of typed content blocks; the
// streaming delta walker concatenates only "text" blocks.
const PiContentBlockText = "text"
