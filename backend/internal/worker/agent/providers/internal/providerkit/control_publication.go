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
	response         *controlResponseClaim
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
	ownsFailure := err != nil && b.outstandingControls[requestID] == record && record.response == nil
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
// tell that event from the echo of an answer the reader already sent. A user
// response retains its claim through delivery. A conditional withdrawal during
// that claim cancels the card only if delivery definitely failed.
func (b *JSONRPCProcess) WithdrawOutstandingControlRequest(sink agent.ControlServices, requestID string) bool {
	return b.withdrawControlPublication(sink, requestID, nil)
}

// WithdrawPublishedControlRequest retires only the exact publication that the caller captured.
// A later request can reuse the same wire ID, so its publication must remain open.
func (b *JSONRPCProcess) WithdrawPublishedControlRequest(sink agent.ControlServices, publication PublishedControlRequest) bool {
	if publication.record == nil {
		return false
	}
	return b.withdrawControlPublication(sink, publication.RequestID, publication.record)
}

// ControlPublicationIsCurrent reports whether this publication still owns its request ID.
func (b *JSONRPCProcess) ControlPublicationIsCurrent(publication PublishedControlRequest) bool {
	b.outstandingMu.Lock()
	defer b.outstandingMu.Unlock()
	return publication.record != nil && b.outstandingControls[publication.RequestID] == publication.record
}

func (b *JSONRPCProcess) withdrawControlPublication(sink agent.ControlServices, requestID string, expected *outstandingControlRequest) bool {
	b.outstandingMu.Lock()
	record, open := b.outstandingControls[requestID]
	if !open || expected != nil && record != expected {
		b.outstandingMu.Unlock()
		return false
	}
	delete(b.outstandingControls, requestID)
	if record.response != nil && record.response.owner == controlResponseUser {
		record.response.withdrawalSink = sink
		b.outstandingMu.Unlock()
		return false
	}
	record.retired = true
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
	var outstanding []controlResponseWrite
	for requestID, record := range b.outstandingControls {
		record.retired = true
		var answer any
		if record.response == nil {
			answer = record.cancelAnswer
		}
		outstanding = append(outstanding, controlResponseWrite{requestID: requestID, record: record, answer: answer})
	}
	b.outstandingControls = nil
	b.outstandingMu.Unlock()
	for _, write := range outstanding {
		b.answerWithdrawnControl(write)
		sink.CancelControlRequestInstance(write.requestID, write.record.instanceID)
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
	var answerable []controlResponseWrite
	for requestID, record := range b.outstandingControls {
		if record.cancelAnswer == nil || record.response != nil {
			continue
		}
		answerable = append(answerable, controlResponseWrite{requestID: requestID, record: record, answer: record.cancelAnswer})
		record.retired = true
		delete(b.outstandingControls, requestID)
	}
	b.outstandingMu.Unlock()
	for _, write := range answerable {
		b.answerWithdrawnControl(write)
		sink.CancelControlRequestInstance(write.requestID, write.record.instanceID)
	}
}

// answerWithdrawnControl sends the captured cancel answer. Response ownership
// was decided under outstandingMu; it must not be read again during delivery.
func (b *JSONRPCProcess) answerWithdrawnControl(write controlResponseWrite) {
	if write.answer == nil {
		return
	}
	if err := b.SendResponse(write.record.wireID, write.answer); err != nil {
		slog.Warn("cancel outstanding control request", "agent_id", b.agentID, "request_id", write.requestID, "error", err)
	}
}

// SendRawInputToOutstandingRequest refuses a response whose registered request
// is absent or already claimed. Requests, notifications and non-JSON frames pass
// through unchanged. Registered responses share one delivery ownership path.
func (b *JSONRPCProcess) SendRawInputToOutstandingRequest(data []byte, stop agent.StopContext) error {
	return b.sendRawControlResponse(data, true)
}

// MCPElicitationCancelAnswer is the action the Model Context Protocol defines for an
// elicitation that the client withdraws without a reader decision.
func MCPElicitationCancelAnswer() any {
	return map[string]any{"action": contracts.MCPElicitationActionCancel}
}

// MCPElicitationAcceptAnswer acknowledges a URL flow that the native server completed.
func MCPElicitationAcceptAnswer() any {
	return map[string]any{"action": contracts.MCPElicitationActionAccept}
}
