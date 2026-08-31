package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// ZCode's request/response plumbing.
//
// zcodeAgent does NOT embed jsonrpcBase, for the same reason PiAgent does not: the
// wire carries no `jsonrpc` field, so the shared marshaller would add one the
// app-server rejects. It shares only the pending-map mechanics, through
// responseCorrelator[int64].

// zcodeRequestFrame is the envelope of a request we send.
type zcodeRequestFrame struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

// zcodeReplyFrame is the envelope of a reply we send to a SERVER request. The
// app-server matches it by id alone, and a reply carrying a `method` would be
// classified as another request, so the field is absent by construction.
type zcodeReplyFrame struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  *zcodeError     `json:"error,omitempty"`
}

// sendZCodeRequest writes a request and blocks until its reply arrives, the
// process exits, or the context/timeout fires.
//
// A timeout of 0 means "no clock": use it for a call whose duration the user's
// request limits rather than the wall clock. On an app-server error the returned
// error WRAPS the *zcodeError, so a caller can read the code with zcodeErrorCode
// (and the zcodeIs* helpers) instead of matching on message text.
func (a *zcodeAgent) sendZCodeRequest(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	id := a.nextReqID.Add(1)

	data, err := json.Marshal(zcodeRequestFrame{ID: id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", method, err)
	}
	data = append(data, '\n')

	ch, release := a.register(id)
	defer release()

	if a.IsStopped() {
		return nil, fmt.Errorf("agent is stopped")
	}

	if err := a.writeStdin(data); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	raw, err := a.awaitResponse(ch, method, timeout)
	if err != nil {
		return nil, err
	}

	var env zcodeResponseEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse %s response: %w", method, err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("%s failed: %w", method, env.Error)
	}
	return env.Result, nil
}

// sendZCodeReply answers a server request with a result. The id is echoed as the
// RAW bytes that arrived, so a numeric id round-trips exactly and a future string
// id needs no change here.
func (a *zcodeAgent) sendZCodeReply(id json.RawMessage, result any) error {
	return a.writeZCodeReply(zcodeReplyFrame{ID: id, Result: result})
}

// sendZCodeErrorReply answers a server request with an error. Used where LeapMux
// cannot satisfy the request at all -- never as a substitute for a legitimate
// "deny" decision, which is a RESULT.
func (a *zcodeAgent) sendZCodeErrorReply(id json.RawMessage, code int, message string) error {
	return a.writeZCodeReply(zcodeReplyFrame{ID: id, Error: &zcodeError{Code: code, Message: message}})
}

func (a *zcodeAgent) writeZCodeReply(frame zcodeReplyFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("marshal reply: %w", err)
	}
	data = append(data, '\n')

	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	return a.writeStdin(data)
}

// routeZCodeRPC resolves a line that belongs to the RPC layer rather than to the
// event stream: our own pending response, or a request from the app-server.
//
// It returns the server-request handler instead of running it, so the caller picks
// the concurrency. The read loop runs it on its own goroutine; a direct feed runs it
// inline. consumed is true when the line needs no further dispatch.
//
// The ORDER here is load-bearing. An id+method line is ambiguous on this wire, so
// a pending id is resolved FIRST: an app-server request whose id happens to equal
// one of ours must not steal our reply, and our reply must not be mistaken for a
// request. Registration decides, not shape.
func (a *zcodeAgent) routeZCodeRPC(line *parsedLine) (handler func(), consumed bool) {
	id, ok := line.IDInt64()
	if ok && a.deliver(id, line.Raw) {
		return nil, true
	}
	if classifyZCodeMessage(line) != zcodeMessageServerRequest {
		return nil, false
	}
	// The handler runs on its own goroutine and outlives this call, which is safe
	// without a copy: readOutput allocates a fresh buffer per line, and
	// json.RawMessage.UnmarshalJSON copies into its own allocation on top of that. So
	// nothing the scanner reuses is reachable from here.
	return func() { a.handleServerRequest(line.Method, line.ID, line.Params) }, true
}

// interceptResponse is the readOutput interceptor. It returns true when it
// consumed the line.
func (a *zcodeAgent) interceptResponse(line *parsedLine) bool {
	handler, consumed := a.routeZCodeRPC(line)
	if handler != nil {
		// Every reply to a server request is a stdin WRITE, which blocks while the
		// app-server is not reading. Answering on its own goroutine keeps the read loop
		// draining -- and the app-server only drains its own stdin between reads, so a
		// reply written from this goroutine could deadlock against it.
		go handler()
	}
	return consumed
}

// handleServerRequest routes one app-server request to its handler.
//
// Every branch MUST answer, including the ones LeapMux cannot satisfy. The
// app-server blocks the flow behind an unanswered request -- the runtime-preferences
// handshake blocks session/create outright -- so silence is a hang, not a decline.
func (a *zcodeAgent) handleServerRequest(method string, id, params json.RawMessage) {
	switch method {
	case ZCodeMethodRequestRuntimePreferences:
		a.answerRuntimePreferences(id)
	case ZCodeMethodRequestPermission:
		a.handlePermissionRequest(id, params)
	case ZCodeMethodRequestUserInput:
		a.handleUserInputRequest(id, params)
	case ZCodeMethodRequestProviderRuntimeHeaders:
		a.answerProviderRuntimeHeaders(id, params)
	case ZCodeMethodRequestOfficialMcpAuthHeaders:
		a.answerOfficialMcpAuthHeaders(id)
	default:
		// LeapMux must still answer an unknown request, or the app-server's work behind
		// it never resumes. Method-not-found is the honest answer.
		slog.Warn("zcode unknown server request", "agent_id", a.agentID, "method", method)
		if err := a.sendZCodeErrorReply(id, ZCodeErrMethodNotFound, "leapmux does not implement "+method); err != nil {
			slog.Warn("zcode reply to unknown request failed", "agent_id", a.agentID, "method", method, "error", err)
		}
	}
}

// zcodeRuntimePreferences is the reply to session/requestRuntimePreferences.
//
// Every field is refused deliberately. Native search enhancements and memory are
// desktop features whose state LeapMux does not own, and auto-resolution would let
// the app-server answer an AskUserQuestion by itself -- which would silently
// bypass the user's control request.
type zcodeRuntimePreferences struct {
	NativeSearchEnhancementsEnabled bool `json:"nativeSearchEnhancementsEnabled"`
	MemoryEnabled                   bool `json:"memoryEnabled"`
	AskUserQuestionAutoResolution   bool `json:"askUserQuestionAutoResolutionEnabled"`
}

func (a *zcodeAgent) answerRuntimePreferences(id json.RawMessage) {
	if err := a.sendZCodeReply(id, zcodeRuntimePreferences{}); err != nil {
		slog.Warn("zcode runtime preferences reply failed", "agent_id", a.agentID, "error", err)
	}
}

// handleOutput is the readOutput handler for lines the interceptor did not consume.
func (a *zcodeAgent) handleOutput(line *parsedLine) {
	handleZCodeOutput(a, line)
}

// HandleOutput parses a single line and dispatches it. Used by tests and
// out-of-band feed paths; the production read loop calls handleOutput directly.
//
// It runs the SAME two stages the read loop does, RPC routing before event
// dispatch. Skipping the first stage would drop every request the app-server makes
// -- a permission prompt, a plan approval, the runtime-preferences handshake that
// blocks session/create -- and each one is a hang rather than a lost message.
func (a *zcodeAgent) HandleOutput(content []byte) {
	line := parseLine(content)
	handler, consumed := a.routeZCodeRPC(line)
	if handler != nil {
		handler()
		return
	}
	if consumed {
		return
	}
	handleZCodeOutput(a, line)
}
