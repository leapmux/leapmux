package zcode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// ZCode's request/response plumbing.
//
// Agent does NOT embed JSONRPCProcess, for the same reason pi.Agent does not: the
// wire carries no `jsonrpc` field, so the shared marshaller would add one the
// app-server rejects. It shares only the pending-map mechanics, through
// Correlator[int64].

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
func (a *Agent) sendZCodeRequest(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	return a.sendZCodeRequestContext(a.Context(), method, params, timeout)
}

// sendZCodeRequestContext is sendZCodeRequest with an explicit context. A
// recovery watcher passes its own cancellable context so a teardown cancels
// the in-flight RPC.
func (a *Agent) sendZCodeRequestContext(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	id := a.nextReqID.Add(1)

	data, err := json.Marshal(zcodeRequestFrame{ID: id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", method, err)
	}
	data = append(data, '\n')
	return a.sendZCodeFrameContext(ctx, id, method, data, timeout)
}

// sendZCodeFrame preserves caller bytes and claims the native reply ID before the write.
func (a *Agent) sendZCodeFrame(id int64, method string, data []byte, timeout time.Duration) (json.RawMessage, error) {
	return a.sendZCodeFrameContext(a.Context(), id, method, data, timeout)
}

// sendZCodeFrameContext is sendZCodeFrame with an explicit context.
func (a *Agent) sendZCodeFrameContext(ctx context.Context, id int64, method string, data []byte, timeout time.Duration) (json.RawMessage, error) {
	ch, release, err := a.Register(id)
	if err != nil {
		return nil, fmt.Errorf("register %s: %w", method, err)
	}
	defer release()

	if a.IsStopped() {
		return nil, fmt.Errorf("agent is stopped")
	}

	if err := a.WriteStdin(data); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	raw, err := providerkit.AwaitReply(&a.Process, ctx, ch, method, timeout)
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
	if len(env.Result) == 0 {
		return nil, fmt.Errorf("%s response has no result or error", method)
	}
	return env.Result, nil
}

// sendZCodeReply answers a server request with a result. The id is echoed as the
// RAW bytes that arrived, so a numeric id round-trips exactly and a future string
// id needs no change here.
func (a *Agent) sendZCodeReply(id json.RawMessage, result any) error {
	return a.writeZCodeReply(zcodeReplyFrame{ID: id, Result: result})
}

// sendZCodeErrorReply answers a server request with an error. Used where LeapMux
// cannot satisfy the request at all -- never as a substitute for a legitimate
// "deny" decision, which is a RESULT.
func (a *Agent) sendZCodeErrorReply(id json.RawMessage, code int, message string) error {
	return a.writeZCodeReply(zcodeReplyFrame{ID: id, Error: &zcodeError{Code: code, Message: message}})
}

func (a *Agent) writeZCodeReply(frame zcodeReplyFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("marshal reply: %w", err)
	}
	data = append(data, '\n')

	if a.IsStopped() {
		return fmt.Errorf("agent is stopped")
	}
	return a.WriteStdin(data)
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
func (a *Agent) routeZCodeRPC(line *providerkit.ParsedLine) (handler func(), consumed bool) {
	id, ok := line.IDInt64()
	if ok && a.Deliver(id, line.Raw) {
		return nil, true
	}
	if classifyZCodeMessage(line) != zcodeMessageServerRequest {
		return nil, false
	}
	// The handler runs on its own goroutine and outlives this call, which is safe
	// without a copy: ReadOutput allocates a fresh buffer per line, and
	// json.RawMessage.UnmarshalJSON copies into its own allocation on top of that. So
	// nothing the scanner reuses is reachable from here.
	if line.Method == MethodRequestProviderRuntimeHeaders {
		// The registration happens HERE, before the handler exists: a cancellation
		// that lands before the handler runs must already find the answer.
		// prepareZCodeRuntimeHeaderAnswer is synchronous bookkeeping, so the read
		// loop absorbs it.
		answer := a.prepareZCodeRuntimeHeaderAnswer(line.ID, line.Params)
		return func() { a.answerProviderRuntimeHeaders(line.ID, line.Params, answer) }, true
	}
	return func() { a.handleServerRequest(line) }, true
}

// interceptResponse is the ReadOutput interceptor. It returns true when it
// consumed the line.
func (a *Agent) interceptResponse(line *providerkit.ParsedLine) bool {
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
// interaction/requestProviderRuntimeHeaders is NOT routed here: routeZCodeRPC
// registers its answer before the handler exists (see
// prepareZCodeRuntimeHeaderAnswer).
//
// Every branch MUST answer, including the ones LeapMux cannot satisfy. The
// app-server blocks the flow behind an unanswered request -- the runtime-preferences
// handshake blocks session/create outright -- so silence is a hang, not a decline.
func (a *Agent) handleServerRequest(line *providerkit.ParsedLine) {
	method, id, params := line.Method, line.ID, line.Params
	switch method {
	case MethodRequestRuntimePreferences:
		a.answerRuntimePreferences(id)
	case contracts.ZCodeMethodRequestPermission:
		a.handlePermissionRequest(id, params)
	case contracts.ZCodeMethodRequestUserInput:
		a.handleUserInputRequest(id, params, line.Raw)
	case MethodRequestOfficialMcpAuthHeaders:
		a.answerOfficialMcpAuthHeaders(id)
	default:
		// LeapMux must still answer an unknown request, or the app-server's work behind
		// it never resumes. Method-not-found is the honest answer.
		slog.Warn("zcode unknown server request", "agent_id", a.AgentID(), "method", method)
		if err := a.sendZCodeErrorReply(id, ErrMethodNotFound, "leapmux does not implement "+method); err != nil {
			slog.Warn("zcode reply to unknown request failed", "agent_id", a.AgentID(), "method", method, "error", err)
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

func (a *Agent) answerRuntimePreferences(id json.RawMessage) {
	if err := a.sendZCodeReply(id, zcodeRuntimePreferences{}); err != nil {
		slog.Warn("zcode runtime preferences reply failed", "agent_id", a.AgentID(), "error", err)
	}
}

// handleOutput is the ReadOutput handler for lines the interceptor did not
// consume. Session events go through the ordered coordinator so a gap in the
// live stream triggers replay; everything else dispatches through
// handleZCodeOutput.
func (a *Agent) handleOutput(line *providerkit.ParsedLine) {
	if line.Method == NotifySessionEvent {
		event, ok := parseZCodeEvent(line.Params)
		if ok {
			a.dispatchZCodeEventOrdered(event)
			return
		}
	}
	handleZCodeOutput(a, line)
}

// HandleOutput parses a single line and dispatches it. Used by tests and
// out-of-band feed paths; the production read loop calls handleOutput directly.
//
// It runs the SAME two stages the read loop does, RPC routing before event
// dispatch. Skipping the first stage would drop every request the app-server makes
// -- a permission prompt, a plan approval, the runtime-preferences handshake that
// blocks session/create -- and each one is a hang rather than a lost message.
func (a *Agent) HandleOutput(content []byte) {
	line := providerkit.ParseLine(content)
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

// ZCode's v4 command inbox.
//
// The desktop application drives a session through `v4/command`, and the stdio
// app-server serves the same inbox. A command states a client-chosen id, which
// the session's events repeat (`inputId`, `queryIds`, `sourceCommandId`), so a
// caller can match its own command in them. The reply is an acknowledgement:
// `accepted` with a typed result, or a status that states why the inbox did not
// act. Some commands are compare-and-set: they state the session revision they
// were decided against, and a `stale` reply carries the current revision.

// zcodeCommandClientID identifies LeapMux to the command inbox.
const zcodeCommandClientID = "leapmux"

// The command types that LeapMux sends.
const (
	zcodeCommandSendText                   = "sendText"
	zcodeCommandDeleteQueueItem            = "deleteQueueItem"
	zcodeCommandRequestWorkspaceHookReview = "requestWorkspaceHookReview"
	zcodeCommandRespondWorkspaceHookReview = "respondWorkspaceHookReview"
	// zcodeCommandStop aborts one foreground execution under a guard. See
	// zcodeStopPayload and sendZCodeStopCommand.
	zcodeCommandStop = "stop"
)

// The statuses of a command acknowledgement that LeapMux reads.
const (
	zcodeCommandAccepted  = "accepted"
	zcodeCommandStale     = "stale"
	zcodeCommandDuplicate = "duplicate"
	zcodeCommandNoop      = "noop"
)

// zcodeCommandGuardStopTargetChanged is the reasonCode of the `noop` that a
// guarded `stop` earns when its expected execution no longer exists. The inbox
// refuses the stop rather than abort the execution that took its place, and
// LeapMux must honour that: no window, and no second stop that could abort the
// newer execution.
const zcodeCommandGuardStopTargetChanged = "guard.stopTargetChanged"

// zcodeCommandEnvelope is one `v4/command` request.
type zcodeCommandEnvelope struct {
	CommandID string `json:"commandId"`
	ClientID  string `json:"clientId"`
	SessionID string `json:"sessionId"`
	// BaseRevision is the session revision a compare-and-set command is
	// decided against. Other commands state none.
	BaseRevision *int64 `json:"baseRevision,omitempty"`
	Type         string `json:"type"`
	Payload      any    `json:"payload"`
	IssuedAt     int64  `json:"issuedAt"`
}

// zcodeCommandAck is the acknowledgement of one command.
type zcodeCommandAck struct {
	CommandID          string              `json:"commandId"`
	Status             string              `json:"status"`
	ReasonCode         string              `json:"reasonCode"`
	Message            string              `json:"message"`
	RevisionAtDecision int64               `json:"revisionAtDecision"`
	Result             *zcodeCommandResult `json:"result"`
}

// zcodeCommandResult is the typed result of an accepted command.
type zcodeCommandResult struct {
	Type     string `json:"type"`
	Delivery string `json:"delivery"`
	InputID  string `json:"inputId"`
}

// sendZCodeCommand sends one command to the session's inbox and returns its
// acknowledgement. Each acknowledgement advances the newest session revision
// the agent knows, which a later compare-and-set command starts from.
func (a *Agent) sendZCodeCommand(envelope zcodeCommandEnvelope) (zcodeCommandAck, error) {
	return a.sendZCodeCommandTimeout(envelope, a.APITimeout())
}

// sendZCodeCommandTimeout is sendZCodeCommand with an explicit timeout. Every
// acknowledgement goes through the same validation: it must decode, and it must
// name the command it acknowledges.
func (a *Agent) sendZCodeCommandTimeout(envelope zcodeCommandEnvelope, timeout time.Duration) (zcodeCommandAck, error) {
	envelope.ClientID = zcodeCommandClientID
	envelope.IssuedAt = a.Clock().Now().UnixMilli()
	raw, err := a.sendZCodeRequest(MethodCommand, envelope, timeout)
	if err != nil {
		return zcodeCommandAck{}, err
	}
	var ack zcodeCommandAck
	if err := json.Unmarshal(raw, &ack); err != nil {
		return zcodeCommandAck{}, fmt.Errorf("decode the %s acknowledgement: %w", envelope.Type, err)
	}
	if ack.CommandID != envelope.CommandID {
		return zcodeCommandAck{}, fmt.Errorf("the %s acknowledgement is for command %q, not %q", envelope.Type, ack.CommandID, envelope.CommandID)
	}
	a.Mu.Lock()
	a.commandRevision = max(a.commandRevision, ack.RevisionAtDecision)
	a.Mu.Unlock()
	return ack, nil
}

// zcodeStopCommandPrefix names the commands of one guarded stop.
const zcodeStopCommandPrefix = "leapmux-stop-"

// zcodeStopPayload is the `stop` command's payload. The expected execution id
// is REQUIRED: the inbox aborts whatever execution is current when the field is
// absent, and LeapMux must never abort a newer execution than the one the user
// stopped. sendZCodeStopCommand refuses an empty id, and an owner that recorded
// none takes the legacy session/stop instead.
type zcodeStopPayload struct {
	ExpectedForegroundExecutionID string `json:"expectedForegroundExecutionId"`
}

// sendZCodeStopCommand sends the guarded stop of one foreground execution and
// returns its acknowledgement. It takes the stop timeout rather than the API
// timeout: the inbox aborts the execution synchronously, so a longer wait only
// makes a user-driven interrupt look slower than it is.
func (a *Agent) sendZCodeStopCommand(sessionID, expectedForegroundExecutionID string) (zcodeCommandAck, error) {
	if expectedForegroundExecutionID == "" {
		return zcodeCommandAck{}, fmt.Errorf("a guarded ZCode stop needs the execution id it guards")
	}
	return a.sendZCodeCommandTimeout(zcodeCommandEnvelope{
		CommandID: zcodeStopCommandPrefix + id.Short(),
		SessionID: sessionID,
		Type:      zcodeCommandStop,
		Payload:   zcodeStopPayload{ExpectedForegroundExecutionID: expectedForegroundExecutionID},
	}, zcodeStopTimeout)
}

// zcodeCommandRefusal states why the inbox did not accept a command.
func zcodeCommandRefusal(ack zcodeCommandAck) string {
	reason := ack.Status
	if ack.ReasonCode != "" {
		reason += " (" + ack.ReasonCode + ")"
	}
	if ack.Message != "" {
		reason += ": " + ack.Message
	}
	if ack.Status == zcodeCommandAccepted {
		reason = "the acknowledgement states no accepted input"
	}
	return reason
}

// zcodeCompareAndSetAttempts limits how often a compare-and-set command is
// sent again after a `stale` reply. Each reply states the current revision, so
// only a session that changes between two replies needs another attempt.
const zcodeCompareAndSetAttempts = 3

// sendZCodeCompareAndSetCommand sends a compare-and-set command from the newest
// session revision the agent knows, and sends it again with the revision of
// each `stale` reply. It returns the last acknowledgement.
func (a *Agent) sendZCodeCompareAndSetCommand(envelope zcodeCommandEnvelope) (zcodeCommandAck, error) {
	a.Mu.Lock()
	revision := a.commandRevision
	a.Mu.Unlock()
	commandID := envelope.CommandID
	var ack zcodeCommandAck
	for attempt := 1; attempt <= zcodeCompareAndSetAttempts; attempt++ {
		envelope.BaseRevision = &revision
		envelope.CommandID = fmt.Sprintf("%s-%d", commandID, attempt)
		var err error
		ack, err = a.sendZCodeCommand(envelope)
		if err != nil || ack.Status != zcodeCommandStale {
			return ack, err
		}
		revision = ack.RevisionAtDecision
	}
	return ack, nil
}
