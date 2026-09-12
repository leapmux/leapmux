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
// The values are generated: see contracts/pi-protocol.json. The browser plugin
// dispatches on the same strings, so a hand copy on either side could drift.

// Pi message_update assistantMessageEvent sub-types — carried inside
// `message_update` envelopes. text_delta and thinking_delta are the
// streaming deltas; the others (start/stop/done/error) bracket the
// stream and are no-ops for UI rendering today.
// The values are generated: see contracts/pi-protocol.json.

// Pi extension_ui_request methods.
//
// Dialog methods (select / confirm / input / editor) block waiting for
// an extension_ui_response and surface as control requests. The
// fire-and-forget methods drive session-info or notification updates.
// The values are generated: see contracts/pi-protocol.json.

// Pi tool names — the canonical lowercase identifiers Pi uses on
// `tool_execution_start` / `tool_execution_end` envelopes. The frontend
// dispatches result renderers off these identifiers, so they must
// match the wire format exactly.
// The values are generated: see contracts/pi-protocol.json.

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
	PiCommandGetEntries         = "get_entries"
	PiCommandGetCommands        = "get_commands"
	PiCommandGetAvailableModels = "get_available_models"
	PiCommandNewSession         = "new_session"
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

// Pi message roles — the `role` field on entries inside `agent_end.messages`
// and on `message_end.message`. Only assistant entries carry the terminal
// stop-reason for a turn.
const PiRoleAssistant = "assistant"

// Pi assistant stop reasons — the `stopReason` field on the final
// assistant entry of an `agent_end` envelope. "error" pairs with a
// non-empty `errorMessage` describing the failure.
const PiStopReasonError = "error"
