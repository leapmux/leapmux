package letta

import (
	"encoding/json"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"
)

// Letta Code's App Server WebSocket vocabulary (protocol_v2) that only the
// worker reads.
//
// The words both sides spell -- command kinds, message kinds, delta kinds, tool
// names, loop statuses -- live in contracts/letta-protocol.json. What is here
// is the transport: the ready-line pattern, the envelope fields the worker
// writes, and the launch arguments. No browser code reads any of them.

// lettaBinaryName is the program the provider launches.
const lettaBinaryName = "letta"

// lettaServerArgs start the App Server.
//
//   - `--listen ws://127.0.0.1:0` binds a free loopback port and prints two
//     lines; the second states the WebSocket endpoint.
var lettaServerArgs = []string{"server", "--listen", "ws://127.0.0.1:0"}

// lettaWebSocketLine matches the ready line that states the WebSocket endpoint:
// `WebSocket: ws://127.0.0.1:60886/ws`.
var lettaWebSocketLine = regexp.MustCompile(`^\s*WebSocket:\s*(ws://\S+)\s*$`)

// runtimeScope is the ConversationRuntimeScope every envelope carries.
type runtimeScope struct {
	AgentID        string `json:"agent_id"`
	ConversationID string `json:"conversation_id"`
}

// lettaCommand is the base of every client->server command. The discriminator
// is `type`, not `kind`, and every command carries the three correlation fields
// the RuntimeEnvelope requires.
type lettaCommand struct {
	Type           string         `json:"type"`
	Runtime        *runtimeScope  `json:"runtime,omitempty"`
	EventSeq       int            `json:"event_seq"`
	EmittedAt      string         `json:"emitted_at"`
	IdempotencyKey string         `json:"idempotency_key"`
	RequestID      string         `json:"request_id,omitempty"`
	Payload        map[string]any `json:"payload,omitempty"`
	// runtime_start puts its fields at the top level, not under payload.
	AgentID            string         `json:"agent_id,omitempty"`
	CreateAgent        map[string]any `json:"create_agent,omitempty"`
	CreateConversation map[string]any `json:"create_conversation,omitempty"`
	Mode               string         `json:"mode,omitempty"`
}

// newLettaCommand builds a command with the correlation fields the envelope
// requires. A command without them is ignored by the server.
func newLettaCommand(typ string, requestID string) lettaCommand {
	seq := lettaNextSeq()
	return lettaCommand{
		Type:           typ,
		EventSeq:       seq,
		EmittedAt:      lettaNow(),
		IdempotencyKey: typ + "-" + requestID + "-" + itoa(seq),
		RequestID:      requestID,
	}
}

// Session and store layout. A local-backend agent record is a flat JSON file
// under $LETTA_LOCAL_BACKEND_DIR/agents/, and a conversation is a directory
// under conversations/. The reader opens them read-only.
const (
	lettaBackendDirEnv    = "LETTA_LOCAL_BACKEND_DIR"
	lettaHomeEnv          = "LETTA_HOME"
	lettaAgentsDir        = "agents"
	lettaConversationsDir = "conversations"
)

// Approval payload shape. The `request_id` and `decision` sit FLAT on the
// payload, never under a `response` key: the nested form produces
// `loop_error: "Protocol violation: input.kind=approval_response requires
// payload.request_id and either payload.decision or payload.error"`.
type approvalResponsePayload struct {
	Kind      string          `json:"kind"`
	RequestID string          `json:"request_id"`
	Decision  json.RawMessage `json:"decision,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// decisionBody is the allow/deny decision of an approval_response.
//
// `message` is NEVER omitted: Letta's `isValidApprovalResponseBody` requires
// `typeof decision.message === "string"` for a deny. An `omitempty` that
// dropped an empty message turned every deny into a protocol violation, the
// tool call hung, and the turn never reached the model again.
type decisionBody struct {
	Behavior     string          `json:"behavior"`
	Message      string          `json:"message"`
	UpdatedInput json.RawMessage `json:"updated_input,omitempty"`
}

// lettaSeq is the event_seq counter every command carries. The server requires
// a strictly increasing sequence per connection.
var lettaSeq atomic.Int64

func lettaNextSeq() int {
	return int(lettaSeq.Add(1))
}

// lettaNow is the emitted_at stamp the envelope requires.
func lettaNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// itoa is a tiny helper so the envelope key needs no fmt import churn.
func itoa(v int) string {
	return strconv.Itoa(v)
}
