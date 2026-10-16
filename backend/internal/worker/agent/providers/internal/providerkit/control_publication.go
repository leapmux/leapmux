package providerkit

import (
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const ControlPublicationFailure = "LeapMux could not store this control request."

// ErrControlRequestNotPending reports a control response whose request the provider no
// longer waits on. A reader answer, a withdrawal, or a standing approval already retired
// the request, so a second answer would reach a request that is closed.
var ErrControlRequestNotPending = errors.New("the control request is no longer pending")

// outstandingControlRequest records one control request the provider still waits on.
//
// wireID addresses the provider's own request, so an answer LeapMux sends reaches it.
// cancelAnswer is the result LeapMux sends when it withdraws the request without a
// reader decision. Each request kind states its own, because the protocols spell that
// answer differently. A nil cancelAnswer withdraws the browser card alone, which is
// the answer for a request the provider does not block on.
//
// approveAnswer is the result LeapMux sends when a standing approval answers the request
// without a reader decision: Codex answers a permission request this way while the
// session holds its bypass preset. A nil approveAnswer means that no standing approval
// can answer the request, as for a question that only the reader can answer.
type outstandingControlRequest struct {
	instanceID       string
	wireID           json.RawMessage
	cancelAnswer     any
	approveAnswer    any
	pending          bool // A standing approval owns the response until delivery finishes.
	retired          bool // A withdrawal or standing approval retired this instance.
	published        bool // The sink accepted this publication.
	publicationErr   error
	payload          []byte
	sessionID        string
	publishOnFailure bool
}

// PublishedControlRequest identifies one publication without exposing its mutable state.
type PublishedControlRequest struct {
	RequestID string
	record    *outstandingControlRequest
}

// PublishControlRequest registers one control request and publishes it to the reader,
// as PublishControlRequestInSession does, for a request that belongs to the provider
// session that the sink holds now.
func (b *JSONRPCProcess) PublishControlRequest(sink agent.ControlServices, content []byte, cancelAnswer any) {
	b.PublishControlRequestInSession(sink, "", content, cancelAnswer)
}

// PublishControlRequestWithApproval registers one control request with the answer that a
// standing approval sends for it, and publishes it to the reader, as
// PublishControlRequestInSession does. It returns the publication handle, or an empty handle on failure.
//
// The caller reads its standing approval again AFTER this call returns, and approves the
// request through ApproveOutstandingControlRequest when the approval now applies. A
// settings change that ran between the caller's first check and this registration then
// still reaches the request.
func (b *JSONRPCProcess) PublishControlRequestWithApproval(sink agent.ControlServices, content []byte, cancelAnswer, approveAnswer any) PublishedControlRequest {
	return b.PublishSessionControlRequestWithApproval(sink, "", content, cancelAnswer, approveAnswer)
}

// PublishSessionControlRequestWithApproval keeps the session identity with the approval record.
func (b *JSONRPCProcess) PublishSessionControlRequestWithApproval(sink agent.ControlServices, sessionID string, content []byte, cancelAnswer, approveAnswer any) PublishedControlRequest {
	return b.publishControlRequest(sink, sessionID, content, cancelAnswer, approveAnswer)
}

// PublishControlRequestInSession registers one control request and publishes it to the
// reader.
//
// This is the single registrar for an inbound JSON-RPC control REQUEST: one the
// provider raised with a JSON-RPC id, which LeapMux answers with a JSON-RPC response.
// Every such request reaches the reader through it, so the provider-side record and
// the browser-side card are created together and WithdrawControlRequest can retire
// both. A storage failure sends a JSON-RPC error unless a concurrent approval owns the response.
//
// agentSessionID is the provider session that the request belongs to. The sink stores
// it with the request, and the worker accepts an answer only while the agent is in
// that session. An empty agentSessionID makes the sink store the session that it holds
// now. A caller states the session when the request can arrive before the sink learns
// it, as a request that an agent raises while it opens its session does.
//
// A provider whose control requests are NOT JSON-RPC requests keeps its own registry
// and its own withdrawal path, because there is no id here to key them by and no
// JSON-RPC response shape to answer them with. Claude sends a `control_request`
// envelope, Pi an extension_ui_response, ZCode re-announces with a fresh wire id per
// announcement, Copilot raises native events keyed by session and request id, and the
// Codex plan prompt is LeapMux's own and carries no id at all.
func (b *JSONRPCProcess) PublishControlRequestInSession(sink agent.ControlServices, agentSessionID string, content []byte, cancelAnswer any) {
	b.publishControlRequest(sink, agentSessionID, content, cancelAnswer, nil)
}

// publishControlRequest is the one registrar behind the exported publishers. It returns
// the exact publication handle, or an empty handle when it registered nothing.
func (b *JSONRPCProcess) publishControlRequest(sink agent.ControlServices, agentSessionID string, content []byte, cancelAnswer, approveAnswer any) PublishedControlRequest {
	wireID, _, found := agent.ExtractJSONRPCID(content)
	identity, valid := agent.NewControlRequestIdentity(wireID)
	if !found || !valid {
		slog.Warn("cannot publish control request without a valid JSON-RPC ID", "agent_id", b.agentID)
		return PublishedControlRequest{}
	}
	record := b.registerControl(identity, &outstandingControlRequest{payload: content, sessionID: agentSessionID, cancelAnswer: cancelAnswer, approveAnswer: approveAnswer})
	if record == nil {
		slog.Warn("duplicate live control request ID", "agent_id", b.agentID, "request_id", identity.Key)
		return PublishedControlRequest{}
	}
	if !b.publishRegisteredControl(sink, identity.Key, record) {
		return PublishedControlRequest{}
	}
	return PublishedControlRequest{RequestID: identity.Key, record: record}
}

// registerControl gives each publication its own identity, even when a wire ID is reused.
func (b *JSONRPCProcess) registerControl(identity agent.ControlRequestIdentity, record *outstandingControlRequest) *outstandingControlRequest {
	b.outstandingMu.Lock()
	defer b.outstandingMu.Unlock()
	if b.outstandingControls == nil {
		b.outstandingControls = make(map[string]*outstandingControlRequest)
	}
	if b.outstandingControls[identity.Key] != nil {
		return nil
	}
	record.instanceID = id.Generate()
	record.wireID = append(json.RawMessage(nil), identity.Native...)
	record.payload = append([]byte(nil), record.payload...)
	b.outstandingControls[identity.Key] = record
	return record
}

// publishRegisteredControl keeps publication and delivery ownership on the same record.
func (b *JSONRPCProcess) publishRegisteredControl(sink agent.ControlServices, requestID string, record *outstandingControlRequest) bool {
	err := sink.PublishControlRequest(agent.ControlRequest{AgentSessionID: record.sessionID, RequestID: requestID, InstanceID: record.instanceID, Payload: record.payload})
	b.outstandingMu.Lock()
	// Only an unpublished request needs its original bytes for recovery.
	record.payload = nil
	record.publicationErr = err
	record.published = err == nil
	ownsFailure := err != nil && b.outstandingControls[requestID] == record && !record.pending
	if ownsFailure {
		delete(b.outstandingControls, requestID)
	}
	retired := record.retired
	b.outstandingMu.Unlock()
	if err != nil {
		slog.Error("publish control request", "agent_id", b.agentID, "request_id", requestID, "error", err)
		if ownsFailure {
			b.SendErrorResponseDetached(record.wireID, -32603, ControlPublicationFailure, "control publication failure")
		}
		return false
	}
	if retired {
		sink.CancelControlRequestInstance(requestID, record.instanceID)
	}
	return true
}

// WithdrawControlRequest retires one control request on both sides, in order.
//
// It drops the record first, so an answer that arrives afterwards forwards nothing.
// The browser card goes last.
//
// It sends the provider NOTHING, because every caller is a provider that withdrew
// its own request and therefore waits for no answer. WithdrawAllControlRequests is
// the path that answers, and it exists for the opposite case: LeapMux retires a
// request the provider still blocks on.
func (b *JSONRPCProcess) WithdrawControlRequest(sink agent.ControlServices, requestID string) {
	b.outstandingMu.Lock()
	record := b.outstandingControls[requestID]
	if record != nil {
		record.retired = true
	}
	delete(b.outstandingControls, requestID)
	b.withdrawGeneration++
	b.outstandingMu.Unlock()
	if record != nil {
		sink.CancelControlRequestInstance(requestID, record.instanceID)
	} else {
		// An explicit native withdrawal also removes a stored request that this registry does not hold.
		sink.CancelControlRequest(requestID)
	}
}

// WithdrawOutstandingControlRequest retires one control request that the provider
// withdrew by an event of its own, and reports whether the request was still open.
//
// It differs from WithdrawControlRequest in one precondition: the request must
// still be REGISTERED. A provider that learns of a withdrawal from a broad event --
// Grok Build states that an interaction resolved, whoever resolved it -- cannot
// tell that event from the echo of an answer the reader already sent. That answer
// removed the record in SendRawInput, so a withdrawal keyed on the record alone
// never cancels a card the reader just decided.
func (b *JSONRPCProcess) WithdrawOutstandingControlRequest(sink agent.ControlServices, requestID string) bool {
	b.outstandingMu.Lock()
	record, open := b.outstandingControls[requestID]
	if !open {
		b.outstandingMu.Unlock()
		return false
	}
	record.retired = true
	delete(b.outstandingControls, requestID)
	b.withdrawGeneration++
	b.outstandingMu.Unlock()
	sink.CancelControlRequestInstance(requestID, record.instanceID)
	return true
}

// WithdrawAllControlRequests answers and retires every control request still open.
//
// It answers rather than drops: the provider blocks on the answer, so a request
// withdrawn in silence leaves the turn running with nothing left that could end it.
// A census of Goose recorded one such request, after which the agent ran for the
// rest of the session and the thinking indicator never stopped.
func (b *JSONRPCProcess) WithdrawAllControlRequests(sink agent.ControlServices) {
	b.outstandingMu.Lock()
	outstanding := b.outstandingControls
	for _, record := range outstanding {
		record.retired = true
	}
	b.outstandingControls = nil
	b.withdrawGeneration++
	b.outstandingMu.Unlock()
	for requestID, record := range outstanding {
		b.answerWithdrawnControl(requestID, record)
		sink.CancelControlRequestInstance(requestID, record.instanceID)
	}
}

// AnswerOutstandingControlRequests answers and retires every control request that
// carries a cancel answer, and LEAVES the rest registered.
//
// It exists for a caller whose next step can fail. A request that carries a cancel
// answer blocks the runtime, so it must be released before anything else is tried; a
// request that carries none releases nothing, so retiring it early buys nothing and
// costs the reader their only control if that next step then fails.
func (b *JSONRPCProcess) AnswerOutstandingControlRequests(sink agent.ControlServices) {
	b.outstandingMu.Lock()
	answerable := make(map[string]*outstandingControlRequest)
	for requestID, record := range b.outstandingControls {
		if record.cancelAnswer == nil || record.pending {
			continue
		}
		answerable[requestID] = record
		record.retired = true
		delete(b.outstandingControls, requestID)
	}
	if len(answerable) > 0 {
		b.withdrawGeneration++
	}
	b.outstandingMu.Unlock()
	for requestID, record := range answerable {
		b.answerWithdrawnControl(requestID, record)
		sink.CancelControlRequestInstance(requestID, record.instanceID)
	}
}

// answerWithdrawnControl sends the provider the record's own cancel answer.
// A record that carries none releases nothing, so it sends nothing.
func (b *JSONRPCProcess) answerWithdrawnControl(requestID string, record *outstandingControlRequest) {
	if record.cancelAnswer == nil || record.pending {
		return
	}
	if err := b.SendResponse(record.wireID, record.cancelAnswer); err != nil {
		slog.Warn("cancel outstanding control request", "agent_id", b.agentID, "request_id", requestID, "error", err)
	}
}

// SendRawInputToOutstandingRequest is SendRawInput for a provider whose EVERY JSON-RPC
// control request registers through the publishers above. It refuses a JSON-RPC
// response whose request the registry no longer holds, with ErrControlRequestNotPending,
// and writes nothing. Every other frame passes to SendRawInput unchanged.
//
// SendRawInput writes such a response anyway, so a reader answer that arrives after a
// withdrawal or a standing approval reached the provider as a second answer to a closed
// request. A provider that publishes through a registry of its own (CodeWhale, Copilot,
// ZCode, Pi) cannot use this method, because its open requests are not in this registry.
func (b *JSONRPCProcess) SendRawInputToOutstandingRequest(data []byte, stop agent.StopContext) error {
	// A response carries an id and no method. A request, a notification, and a frame
	// that is not JSON at all go to SendRawInput, which decides about them as before.
	var frame struct {
		ID     json.RawMessage `json:"id"`
		Method *string         `json:"method"`
	}
	if json.Unmarshal(data, &frame) != nil || frame.Method != nil || isJSONNull(frame.ID) {
		return b.SendRawInput(data, stop)
	}
	restore, open := b.takeOutstandingControl(data)
	if !open {
		return ErrControlRequestNotPending
	}
	err := b.writeRawFrame(data)
	if err != nil && !errors.Is(err, agent.ErrDeliveryUncertain) {
		restore()
	}
	return err
}

// takeOutstandingControl removes the record that one outgoing frame answers, and
// returns the function that puts it back. open reports whether the frame answered a
// request that the registry held.
//
// Every control response reaches the provider through SendRawInput, so reading the id
// there is what keeps the registry to the requests that are still open. A frame that
// answers nothing (a notification, a prompt) carries no id and changes nothing.
//
// It runs BEFORE the write, because a withdrawal can run during the write -- a
// provider that reports an interaction resolved, whoever resolved it -- and a record
// still present then cancels the card that the reader just answered.
//
// restore puts the record back for a write that certainly delivered nothing, so a
// later withdrawal still reaches the request. It does nothing when any withdrawal
// ran in between: a withdrawal of every request (Stop) must not see a request that it
// retired come back.
func (b *JSONRPCProcess) takeOutstandingControl(raw []byte) (restore func(), open bool) {
	nothing := func() {}
	wireID, _, found := agent.ExtractJSONRPCID(raw)
	if !found {
		return nothing, false
	}
	identity, valid := agent.NewControlRequestIdentity(wireID)
	if !valid {
		return nothing, false
	}
	b.outstandingMu.Lock()
	record, open := b.outstandingControls[identity.Key]
	if !open || record.pending {
		b.outstandingMu.Unlock()
		return nothing, false
	}
	delete(b.outstandingControls, identity.Key)
	generation := b.withdrawGeneration
	b.outstandingMu.Unlock()
	return func() {
		b.outstandingMu.Lock()
		defer b.outstandingMu.Unlock()
		if b.withdrawGeneration != generation || b.outstandingControls[identity.Key] != nil {
			return
		}
		if b.outstandingControls == nil {
			b.outstandingControls = make(map[string]*outstandingControlRequest)
		}
		b.outstandingControls[identity.Key] = record
	}, true
}

// MCPElicitationCancelAnswer is the action the Model Context Protocol defines for an
// elicitation that the client withdraws without a reader decision.
func MCPElicitationCancelAnswer() any {
	return map[string]any{"action": contracts.MCPElicitationActionCancel}
}
