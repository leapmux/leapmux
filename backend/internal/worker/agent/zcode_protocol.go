package agent

import (
	"encoding/json"
	"errors"
)

// ZCode wire-protocol vocabulary.
//
// ZCode's app-server speaks line-delimited JSON over stdio. The shape looks like
// JSON-RPC 2.0 and is NOT: there is no `jsonrpc` field, and both sides send
// requests over the same pipe. A message is classified by which of `id` and
// `method` it carries -- see classifyZCodeMessage.
//
// Every method name, event type, streaming kind, tool-update kind, mode, and error
// code the provider depends on is a constant here, so a rename is a compile error
// and each dispatch switch has one source of truth.

// ZCode client->server methods.
const (
	// ZCodeMethodUpdateProviderRegistry pushes the model providers (with inline API
	// keys) that the session may use. Without it every turn fails with
	// `provider_not_configured`: the app-server holds no credentials of its own, the
	// desktop application supplies them, and no environment variable substitutes.
	ZCodeMethodUpdateProviderRegistry = "workspace/updateProviderRegistry"

	ZCodeMethodSessionCreate = "session/create"
	ZCodeMethodSessionResume = "session/resume"
	ZCodeMethodSessionRead   = "session/read"
	ZCodeMethodSessionSend   = "session/send"
	ZCodeMethodSessionStop   = "session/stop"

	ZCodeMethodSessionSubscribe = "session/subscribe"

	ZCodeMethodSetMode         = "session/setMode"
	ZCodeMethodSetModel        = "session/setModel"
	ZCodeMethodSetThoughtLevel = "session/setThoughtLevel"
)

// ZCode server->client methods.
//
// The runtime-preferences request BLOCKS session/create until it is answered, and
// it fires more than once per session (observed scopes `runtime-materialization`
// and `user-execution`), so the read loop answers it rather than any one call site.
const (
	ZCodeMethodRequestRuntimePreferences = "session/requestRuntimePreferences"

	ZCodeMethodRequestPermission = "interaction/requestPermission"
	ZCodeMethodRequestUserInput  = "interaction/requestUserInput"
	// ZCodeMethodRequestProviderRuntimeHeaders asks the client for freshly-minted
	// OAuth headers for a start-plan provider. LeapMux mints none -- it reads the
	// desktop configuration and forwards the API key it finds there -- so the reply
	// reports whether that inline key is present instead of leaving the request
	// unanswered (which stalls the turn).
	ZCodeMethodRequestProviderRuntimeHeaders = "interaction/requestProviderRuntimeHeaders"
	// ZCodeMethodRequestOfficialMcpAuthHeaders asks for credentials for ZCode's
	// hosted MCP servers, which LeapMux likewise does not hold.
	ZCodeMethodRequestOfficialMcpAuthHeaders = "interaction/requestOfficialMcpAuthHeaders"
)

// ZCode server->client notifications (no id).
const (
	// ZCodeNotifySessionEvent carries the session event stream: params.event is a
	// {sessionId, seq, type, payload} envelope whose `type` is one of the
	// ZCodeEvent* constants below.
	ZCodeNotifySessionEvent = "session/event"
	// ZCodeNotifyStateUpdated is a SESSION-SETTINGS patch, not a session event. It
	// arrives at the top level (never wrapped in session/event) whenever the model,
	// mode, thought level, or run status changes -- including mid-turn.
	ZCodeNotifyStateUpdated = "state.updated"
)

// ZCode session event types (the `type` field inside a session/event envelope).
//
// This is the app-server's COMPLETE enumeration, in its own order. Listing every
// member -- including the ones the provider deliberately ignores -- is what lets
// the dispatch switch have no `default` that silently absorbs a new event type.
//
// The values are generated: see contracts/zcode-protocol.json. The browser plugin
// dispatches on the same strings, so a hand copy on either side could drift by a
// character and stop rendering a row with no build error.

// ZCode model.streaming kinds.
//
// A tool call's INPUT arrives through these kinds BEFORE the tool.updated that
// opens it, and that tool.updated then reports `inputOmitted: true` with
// `inputRef: "model_stream"`. So the input has to be cached from the stream --
// there is no second copy to read later.
const (
	ZCodeStreamStart          = "start"
	ZCodeStreamFinish         = "finish"
	ZCodeStreamError          = "error"
	ZCodeStreamTextStart      = "text_start"
	ZCodeStreamTextDelta      = "text_delta"
	ZCodeStreamTextEnd        = "text_end"
	ZCodeStreamReasoningStart = "reasoning_start"
	ZCodeStreamReasoningDelta = "reasoning_delta"
	ZCodeStreamReasoningEnd   = "reasoning_end"
	ZCodeStreamToolInputStart = "tool_input_start"
	ZCodeStreamToolInputDelta = "tool_input_delta"
	ZCodeStreamToolInputEnd   = "tool_input_end"
	ZCodeStreamToolCall       = "tool_call"
)

// ZCode tool.updated kinds are generated: see contracts/zcode-protocol.json.

// ZCode session modes.
//
// `auto` exists in the enum and is NOT implemented in the shipped app-server:
// every tool call under it is denied with
// `permission.resolved {reason:"Auto mode is reserved but not implemented yet"}`.
// It is therefore deliberately absent from the option list -- see zcode_settings.go.
// The values are generated: see contracts/zcode-protocol.json.

// contracts.ZCodeDefaultMode is the mode LeapMux asks a fresh session for.
//
// session/create HONORS its `mode` parameter, and LeapMux always sends one. A
// create that sends none does NOT get this mode: the app-server persists the last
// mode that session/setMode applied and gives the new session that one instead, so
// a session opened with no mode inherits whatever the desktop application or an
// earlier agent left behind.
//
// The reply is easy to misread. `session.mode` reports the projection's seed and
// reads `build` whatever the session runs in. `settings.mode.current` is the live
// mode, and it is the field applySettingsSnapshotLocked reads.
// The value is generated: see contracts/zcode-protocol.json.

// ZCode subscription delivery kind. `desktop-continuous` is the streaming
// subscription the desktop application itself uses.
const ZCodeDeliveryContinuous = "desktop-continuous"

// ZCode state.updated scopes. The notification covers more than one scope, and
// only the session scope carries the settings axes: the workspace scope patches
// `modelCatalog`, and applying it as settings would read an all-absent snapshot.
const (
	ZCodeScopeSession   = "session"
	ZCodeScopeWorkspace = "workspace"
)

// ZCode tool names LeapMux reasons about by name are generated: see
// contracts/zcode-protocol.json. The renderer plugin dispatches on the same names.

// ZCode interaction schema discriminator for the plan-approval flow. An
// interaction/requestUserInput carrying it is the "approve this plan" prompt;
// every other one is an AskUserQuestion.
const ZCodeInteractionPlanApproval = "plan_approval"

// ZCodePlanApproveSentinel is the answer value the app-server reads as "the plan is
// approved". Any other non-empty answer is treated as a DENIAL that carries the text
// as reviewer feedback (`plan_approval_feedback`), which is exactly the shape
// LeapMux's reject-with-a-reason control response needs.
const ZCodePlanApproveSentinel = "approve"

// ZCodePlanApprovalQuestion is the question text of the app-server's DEFAULT plan
// approval prompt, and the first key its reader looks for in `content.answers`.
//
// A plan approval that carries a reason states that reason as the question text
// instead, so this constant is not a reliable key -- which is why the reply also
// carries the positional `answer_0` form. See zcodeUserInputResult.
const ZCodePlanApprovalQuestion = "Review this implementation plan."

// ZCode's `content` answer field names.
//
// The app-server's reader accepts three spellings, in this order:
// `content.answers[<question text>]`, `content.answer_<index>`, and
// `content.answer` (only when the request has exactly one question). A reply that
// uses none of them reads as NO ANSWER, which the plan path turns into a silent
// denial -- so LeapMux sends the keyed map AND the positional fallback.
const (
	ZCodeAnswerMapField      = "answers"
	ZCodeAnswerField         = "answer"
	ZCodeAnswerIndexedPrefix = "answer_"
)

// The two fields of a requestUserInput reply.
const (
	ZCodeReplyActionField  = "action"
	ZCodeReplyContentField = "content"
)

// ZCode interaction decisions (interaction/requestPermission replies).
//
// `escalate` and `modify` are in the app-server's enumeration and LeapMux emits
// neither: escalate hands the decision to a second approver ZCode's desktop
// application owns, and modify rewrites the tool's input, which no LeapMux control
// surface offers. They are listed so a decision READ off the wire has a label.
// The values are generated: see contracts/zcode-protocol.json.

// ZCode turn input sources (turn.started.inputSource).
//
// An ABSENT inputSource is the user's own turn. Every value below marks a turn the
// runtime started for itself, and such a turn must not end the user's turn or
// double-report its output.
const (
	ZCodeInputSourceBackgroundTask   = "background_task"
	ZCodeInputSourceFork             = "fork"
	ZCodeInputSourceGoalStateChange  = "goal_state_change"
	ZCodeInputSourceGoalContinuation = "goal-continuation"
	ZCodeInputSourcePluginReference  = "plugin_reference"
	ZCodeInputSourceRewind           = "rewind"
	ZCodeInputSourceSideChat         = "selection_side_chat"
	ZCodeInputSourceSubagent         = "subagent"
	ZCodeInputSourceSubagentMessage  = "subagent_message"
	ZCodeInputSourceTodoReminder     = "todo_reminder"
)

// ZCodeToolSourceSubagent marks a tool.updated that belongs to a SUBAGENT rather
// than to the main conversation. Such an update carries agentId / agentType /
// childSessionId, which is how a child transcript is minted for it.
const ZCodeToolSourceSubagent = "subagent"

// ZCode background-task kinds (taskKind on a background-task snapshot).
const (
	ZCodeTaskKindBash     = "bash"
	ZCodeTaskKindSubagent = "subagent"
)

// ZCode turn results (turn.completed.resultType).
// The values are generated: see contracts/zcode-protocol.json.

// ZCode interaction actions (interaction/requestUserInput replies).
const (
	ZCodeActionAccept  = "accept"
	ZCodeActionDecline = "decline"
)

// ZCode error codes observed on the wire.
const (
	// ZCodeErrPromptRunning is returned by session/send while a turn is already
	// running. It is transient by nature -- the turn ends -- so the send is retried
	// briefly rather than reported as a delivery failure.
	ZCodeErrPromptRunning = -32010
	// ZCodeErrSessionNotActive is returned for a session the app-server dropped.
	ZCodeErrSessionNotActive = -32004
	// ZCodeErrMethodNotFound marks a method this app-server build does not implement.
	// Treated as "the capability is absent", never as a hard failure.
	ZCodeErrMethodNotFound = -32601
	// ZCodeErrPromptRunningLegacy is the code an older app-server build used for
	// "a prompt is already running". Kept beside the current one so a user on the
	// older build still gets the retry rather than a delivery error.
	ZCodeErrPromptRunningLegacy = 1308
	// ZCodeErrInternal is the code LeapMux replies with when it cannot satisfy a
	// server request at all. It is never used for a DENIAL: a denial is a decision
	// the user made, and it travels as a result.
	ZCodeErrInternal = -32603
)

// ZCodeOfficialAuthUnavailable is the status LeapMux reports for ZCode's hosted MCP
// servers, whose credentials only the desktop application holds. Declaring the
// unavailability lets the app-server fall through to the servers it can reach; an
// unanswered request would block the tool for good.
const ZCodeOfficialAuthUnavailable = "official_auth_unavailable"

// ZCodeCauseProviderNotConfigured is the `turn.failed` cause for a session whose
// model provider carries no usable credential. It is NOT retryable: retrying
// re-reads the same empty configuration.
const ZCodeCauseProviderNotConfigured = "provider_not_configured"

// zcodeMessageKind is what classifyZCodeMessage answers.
type zcodeMessageKind int

const (
	// zcodeMessageUnknown is a line carrying neither an id nor a method.
	zcodeMessageUnknown zcodeMessageKind = iota
	// zcodeMessageResponse is a reply to a request WE sent: an id, no method.
	zcodeMessageResponse
	// zcodeMessageServerRequest is a request FROM the app-server: an id AND a method.
	zcodeMessageServerRequest
	// zcodeMessageNotification is a method with no id.
	zcodeMessageNotification
)

// classifyZCodeMessage decides what an inbound line is.
//
// The id+method case is genuinely ambiguous on this wire, because the app-server
// sends requests over the same pipe our responses arrive on. It is resolved by
// registration, NOT by shape: the read loop first asks the correlator whether the
// id belongs to a pending request, and only calls this an inbound request when it
// does not. This function therefore reports the SHAPE, and the
// caller resolves the race -- see zcodeAgent.interceptResponse.
func classifyZCodeMessage(line *parsedLine) zcodeMessageKind {
	hasID := line.HasID()
	hasMethod := line.Method != ""
	switch {
	case hasID && hasMethod:
		return zcodeMessageServerRequest
	case hasID:
		return zcodeMessageResponse
	case hasMethod:
		return zcodeMessageNotification
	default:
		return zcodeMessageUnknown
	}
}

// zcodeResponseEnvelope is the result/error shape of a reply to one of our
// requests. It deliberately does NOT embed a `jsonrpc` field: the wire carries
// none, and a marshalled zero value would be rejected.
type zcodeResponseEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *zcodeError     `json:"error"`
}

// zcodeError is the app-server's error object. `data` carries a Zod validation
// report for a malformed request and is kept raw for the log.
type zcodeError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *zcodeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return "zcode error"
}

// zcodeEventEnvelope is one session event.
//
// It IS the params of a session/event notification, at the top level -- the
// app-server sends `{method:"session/event", params:{eventId, sessionId, seq, type,
// payload, ...}}`, with no wrapper object.
type zcodeEventEnvelope struct {
	EventID      string          `json:"eventId"`
	SessionID    string          `json:"sessionId"`
	TurnID       string          `json:"turnId"`
	Seq          int64           `json:"seq"`
	Timestamp    int64           `json:"timestamp"`
	DeliveryKind string          `json:"deliveryKind"`
	Type         string          `json:"type"`
	Payload      json.RawMessage `json:"payload"`
}

// zcodeEventNotification decodes both spellings of a session/event's params: the
// flat envelope the app-server sends, and a nested `event` object. The nested form
// is accepted because session/subscribe returns its replayed events under a field
// of that name, so one decoder serves both arrival paths.
type zcodeEventNotification struct {
	Event *zcodeEventEnvelope `json:"event"`
	zcodeEventEnvelope
}

// parseZCodeEvent decodes a session/event notification's params into the event
// envelope. ok is false when the line carries no event type at all, which is the
// only thing every downstream handler needs to agree on.
func parseZCodeEvent(params json.RawMessage) (zcodeEventEnvelope, bool) {
	if len(params) == 0 {
		return zcodeEventEnvelope{}, false
	}
	var notif zcodeEventNotification
	if err := json.Unmarshal(params, &notif); err != nil {
		return zcodeEventEnvelope{}, false
	}
	if notif.Type != "" {
		return notif.zcodeEventEnvelope, true
	}
	if notif.Event != nil && notif.Event.Type != "" {
		return *notif.Event, true
	}
	return zcodeEventEnvelope{}, false
}

// zcodeErrorCode extracts the app-server's error code from an error returned by
// sendZCodeRequest, or (0, false) when the error is not a wire error (a write
// failure, a timeout, a process exit).
func zcodeErrorCode(err error) (int, bool) {
	var wire *zcodeError
	if errors.As(err, &wire) {
		return wire.Code, true
	}
	return 0, false
}

// zcodeIsPromptRunning reports whether err says a turn is already running, across
// both codes the app-server uses for it.
func zcodeIsPromptRunning(err error) bool {
	code, ok := zcodeErrorCode(err)
	return ok && (code == ZCodeErrPromptRunning || code == ZCodeErrPromptRunningLegacy)
}

// zcodeIsMethodNotFound reports whether err says the app-server does not implement
// the method, so an optional startup step can be skipped rather than failed.
func zcodeIsMethodNotFound(err error) bool {
	code, ok := zcodeErrorCode(err)
	return ok && code == ZCodeErrMethodNotFound
}
