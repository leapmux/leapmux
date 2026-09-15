package agent

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
)

const controlPublicationFailure = "LeapMux could not store this control request."

// outstandingControlRequest records one control request the provider still waits on.
//
// wireID addresses the provider's own request, so an answer LeapMux sends reaches it.
// cancelAnswer is the result LeapMux sends when it withdraws the request without a
// reader decision. Each request kind states its own, because the protocols spell that
// answer differently. A nil cancelAnswer withdraws the browser card alone, which is
// the answer for a request the provider does not block on.
type outstandingControlRequest struct {
	wireID       json.RawMessage
	cancelAnswer any
}

// publishControlRequest registers one control request and publishes it to the reader.
//
// This is the single registrar. Every JSON-RPC provider reaches the reader through it,
// so the provider-side record and the browser-side card are created together and
// withdrawControlRequest can retire both. A storage failure registers nothing,
// publishes nothing, and answers the provider with a JSON-RPC error.
func (b *jsonrpcBase) publishControlRequest(sink ControlServices, content []byte, cancelAnswer any) {
	wireID, _, found := ExtractJSONRPCID(content)
	identity, valid := newControlRequestIdentity(wireID)
	if !found || !valid {
		slog.Warn("cannot publish control request without a valid JSON-RPC ID", "agent_id", b.agentID)
		return
	}
	b.outstandingMu.Lock()
	if b.outstandingControls == nil {
		b.outstandingControls = make(map[string]outstandingControlRequest)
	}
	b.outstandingControls[identity.key] = outstandingControlRequest{wireID: identity.native, cancelAnswer: cancelAnswer}
	b.outstandingMu.Unlock()
	if err := sink.PublishControlRequest(ControlRequest{RequestID: identity.key, Payload: content}); err != nil {
		slog.Error("publish control request", "agent_id", b.agentID, "request_id", identity.key, "error", err)
		b.outstandingMu.Lock()
		delete(b.outstandingControls, identity.key)
		b.outstandingMu.Unlock()
		if err := b.sendErrorResponse(identity.native, -32603, controlPublicationFailure); err != nil {
			slog.Warn("send control request failure", "agent_id", b.agentID, "request_id", identity.key, "error", err)
		}
		return
	}
	// A withdrawal that ran between the registration above and this publish took the
	// record and cancelled a card that did not exist yet. CancelControlRequest finds
	// no row and returns in silence, and the publish then creates the card it meant
	// to retire -- a live card with no record behind it, which no later withdrawal
	// can reach and which the reader can never dismiss. The provider already has its
	// cancel answer, so retiring the card here is all that is left.
	//
	// The registration and the publish cannot share one critical section: the publish
	// is a store write and a broadcast, and holding outstandingMu across it would
	// stall every other publisher behind it.
	b.outstandingMu.Lock()
	_, stillRegistered := b.outstandingControls[identity.key]
	b.outstandingMu.Unlock()
	if !stillRegistered {
		sink.CancelControlRequest(identity.key)
	}
}

// withdrawControlRequest retires one control request on both sides, in order.
//
// It drops the record first, so an answer that arrives afterwards forwards nothing.
// The browser card goes last.
//
// It sends the provider NOTHING, because every caller is a provider that withdrew
// its own request and therefore waits for no answer. withdrawAllControlRequests is
// the path that answers, and it exists for the opposite case: LeapMux retires a
// request the provider still blocks on.
func (b *jsonrpcBase) withdrawControlRequest(sink ControlServices, requestID string) {
	b.outstandingMu.Lock()
	delete(b.outstandingControls, requestID)
	b.outstandingMu.Unlock()
	sink.CancelControlRequest(requestID)
}

// withdrawAllControlRequests answers and retires every control request still open.
//
// It answers rather than drops: the provider blocks on the answer, so a request
// withdrawn in silence leaves the turn running with nothing left that could end it.
// A census of Goose recorded one such request, after which the agent ran for the
// rest of the session and the thinking indicator never stopped.
func (b *jsonrpcBase) withdrawAllControlRequests(sink ControlServices) {
	b.outstandingMu.Lock()
	outstanding := b.outstandingControls
	b.outstandingControls = nil
	b.outstandingMu.Unlock()
	for requestID, record := range outstanding {
		b.answerWithdrawnControl(requestID, record)
		sink.CancelControlRequest(requestID)
	}
}

// answerWithdrawnControl sends the provider the record's own cancel answer.
// A record that carries none releases nothing, so it sends nothing.
func (b *jsonrpcBase) answerWithdrawnControl(requestID string, record outstandingControlRequest) {
	if record.cancelAnswer == nil {
		return
	}
	if err := b.sendResponse(record.wireID, record.cancelAnswer); err != nil {
		slog.Warn("cancel outstanding control request", "agent_id", b.agentID, "request_id", requestID, "error", err)
	}
}

// forgetOutstandingControl drops the record that one outgoing frame answers.
//
// Every control response reaches the provider through SendRawInput, so reading the id
// there is what keeps the registry to the requests that are still open. A frame that
// answers nothing (a notification, a prompt) carries no id and changes nothing.
func (b *jsonrpcBase) forgetOutstandingControl(raw []byte) {
	wireID, _, found := ExtractJSONRPCID(raw)
	if !found {
		return
	}
	identity, valid := newControlRequestIdentity(wireID)
	if !valid {
		return
	}
	b.outstandingMu.Lock()
	delete(b.outstandingControls, identity.key)
	b.outstandingMu.Unlock()
}

// mcpElicitationCancelAnswer is the action the Model Context Protocol defines for an
// elicitation that the client withdraws without a reader decision.
func mcpElicitationCancelAnswer() any {
	return map[string]any{"action": contracts.MCPElicitationActionCancel}
}
