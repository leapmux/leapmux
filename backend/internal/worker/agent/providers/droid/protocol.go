package droid

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Factory Droid's stream-jsonrpc vocabulary that only the worker reads.
//
// The words both sides spell -- notification types, tool names, permission
// option ids, working states -- live in contracts/droid-protocol.json. What is
// here is the transport: the method names the worker SENDS, the envelope
// stamps, and the shapes of the requests the worker writes. No browser code
// reads any of them.

// droidBinaryName is the program the provider launches.
const droidBinaryName = "droid"

// factoryApiVersion is stamped on every stream-jsonrpc message the worker
// sends. The CLI rejects a message without it with -32700.
const factoryApiVersion = "1.0.0"

// factoryProtocolVersion is the protocol revision the worker speaks. The SDK
// drops events from a newer peer, so this pins the dialect and the driver
// tolerates unknown notification types rather than failing.
const factoryProtocolVersion = "1.222.0"

// droidExecArgs start one long-lived stream-jsonrpc session worker.
//
//   - `--input-format stream-jsonrpc` / `--output-format stream-jsonrpc` select
//     Factory's daemon-compatible JSON-RPC protocol. `stream-json` is a
//     deprecated subset.
//   - `--settings <path>` points the process at the runtime settings file
//     LeapMux generates, which is the isolation seam for the BYOK model.
//   - `--skip-permissions-unsafe` is added by the caller for E2E/bypass runs.
//     It cannot combine with `--auto`.
var droidBaseArgs = []string{
	"exec",
	"--input-format", "stream-jsonrpc",
	"--output-format", "stream-jsonrpc",
}

// Stream-jsonrpc method names the worker sends (client -> server).
const (
	droidMethodInitializeSession      = "droid.initialize_session"
	droidMethodLoadSession            = "droid.load_session"
	droidMethodCloseSession           = "droid.close_session"
	droidMethodAddUserMessage         = "droid.add_user_message"
	droidMethodUpdateSessionSettings  = "droid.update_session_settings"
	droidMethodListModels             = "droid.list_models"
	droidMethodInterruptSession       = "droid.interrupt_session"
	droidMethodCompactSession         = "droid.compact_session"
	droidMethodResolveQueuedUserMsg   = "droid.resolve_queued_user_message"
	droidMethodGetContextStats        = "droid.get_context_stats"
	droidMethodListSkills             = "droid.list_skills"
	droidMethodForkSession            = "droid.fork_session"
	droidMethodRenameSession          = "droid.rename_session"
	droidMethodKillWorkerSession      = "droid.kill_worker_session"
	droidMethodChangeWorkingDirectory = "droid.change_working_directory"
)

// Stream-jsonrpc method names the server sends (server -> client). The worker
// answers both.
const (
	droidMethodRequestPermission = "droid.request_permission"
	droidMethodAskUser           = "droid.ask_user"
	droidMethodSessionNotif      = "droid.session_notification"
)

// Envelope type words.
const (
	droidTypeRequest      = "request"
	droidTypeResponse     = "response"
	droidTypeNotification = "notification"
	droidJSONRPCVersion   = "2.0"
	droidEnvelopeAPIKey   = "factoryApiVersion"
	droidEnvelopeProtoKey = "factoryProtocolVersion"
)

// Session file layout. A session is `<factory home>/sessions/<sanitized-cwd>/<uuid>.jsonl`
// plus its settings sidecars.
const (
	droidSessionsDirName = "sessions"
	droidSessionSuffix   = ".jsonl"
)

// droidSanitizeCwd reproduces Factory's cwd key: the absolute path with every
// slash replaced by a hyphen.
func droidSanitizeCwd(cwd string) string {
	return strings.ReplaceAll(cwd, "/", "-")
}

// droidInitRequestID is the id the worker gives initialize_session or
// load_session. The reply settles startup before the worker accepts input.
const droidInitRequestID = "leapmux-init"

// droidEnvelope is the stream-jsonrpc message envelope. Every message the
// worker writes carries both Factory version stamps; a request missing them is
// rejected with -32700.
type droidEnvelope struct {
	JSONRPC            string          `json:"jsonrpc"`
	Type               string          `json:"type"`
	FactoryAPIVersion  string          `json:"factoryApiVersion"`
	FactoryProtocolVer string          `json:"factoryProtocolVersion"`
	ID                 string          `json:"id,omitempty"`
	Method             string          `json:"method,omitempty"`
	Params             json.RawMessage `json:"params,omitempty"`
	Result             json.RawMessage `json:"result,omitempty"`
	Error              *droidRPCError  `json:"error,omitempty"`
}

// droidRPCError is the JSON-RPC error object.
type droidRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *droidRPCError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("droid rpc error %d: %s", e.Code, e.Message)
}

// newDroidEnvelope stamps the Factory versions on a message.
func newDroidEnvelope(typ string) droidEnvelope {
	return droidEnvelope{
		JSONRPC:            droidJSONRPCVersion,
		Type:               typ,
		FactoryAPIVersion:  factoryApiVersion,
		FactoryProtocolVer: factoryProtocolVersion,
	}
}

// Marshal encodes an envelope as one NDJSON line (without the trailing
// newline; the transport adds it).
func (e droidEnvelope) Marshal() ([]byte, error) {
	return json.Marshal(e)
}

// initializeParams are the droid.initialize_session params. machineId is
// REQUIRED by the CLI's schema.
type initializeParams struct {
	Cwd             string `json:"cwd"`
	MachineID       string `json:"machineId"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	AutonomyMode    string `json:"autonomyMode,omitempty"`
}

// loadSessionParams are the droid.load_session params. A resume reads the
// native transcript instead of creating a new session with the saved id.
type loadSessionParams struct {
	SessionID string `json:"sessionId"`
}

// addUserMessageParams are the droid.add_user_message params. The prompt
// argument is forbidden with a streaming --input-format, so every turn goes
// over this method. `text` is a plain string; a probe proved a `content` array
// is refused with `params.text: Required`. Images ride the separate `images`
// field, which carries each supported image as base64 data.
type addUserMessageParams struct {
	SessionID      string            `json:"sessionId"`
	Text           string            `json:"text"`
	Images         []droidInputImage `json:"images,omitempty"`
	QueuePlacement string            `json:"queuePlacement,omitempty"`
}

// droidInputImage is one of Droid's base64 input image sources.
type droidInputImage struct {
	Type      string `json:"type"`
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
}

// interruptParams are the droid.interrupt_session params.
type interruptParams struct {
	SessionID string `json:"sessionId"`
}

// updateSettingsParams are the top-level droid.update_session_settings params.
type updateSettingsParams struct {
	ModelID         string `json:"modelId,omitempty"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	InteractionMode string `json:"interactionMode,omitempty"`
	AutonomyLevel   string `json:"autonomyLevel,omitempty"`
}

// Droid processes end_of_turn inside an active loop and starts a turn when idle.
const droidQueueEndOfTurn = "end_of_turn"

// autonomyMode values on the wire. The CLI's `--auto low|medium|high` maps to
// the auto-* spellings; the default `normal` is read-only.
const (
	droidAutonomyNormal     = "normal"
	droidAutonomySpec       = "spec"
	droidAutonomyAutoLow    = "auto-low"
	droidAutonomyAutoMedium = "auto-medium"
	droidAutonomyAutoHigh   = "auto-high"
)
