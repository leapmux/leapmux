package providerkit

import (
	"encoding/json"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const ControlPublicationFailure = "LeapMux could not store this control request."

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

// PublishControlRequest registers one control request and publishes it to the reader,
// as PublishControlRequestInSession does, for a request that belongs to the provider
// session that the sink holds now.
func (b *JSONRPCProcess) PublishControlRequest(sink agent.ControlServices, content []byte, cancelAnswer any) {
	b.PublishControlRequestInSession(sink, "", content, cancelAnswer)
}

// PublishControlRequestInSession registers one control request and publishes it to the
// reader.
//
// This is the single registrar for an inbound JSON-RPC control REQUEST: one the
// provider raised with a JSON-RPC id, which LeapMux answers with a JSON-RPC response.
// Every such request reaches the reader through it, so the provider-side record and
// the browser-side card are created together and WithdrawControlRequest can retire
// both. A storage failure registers nothing, publishes nothing, and answers the
// provider with a JSON-RPC error.
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
	wireID, _, found := agent.ExtractJSONRPCID(content)
	identity, valid := agent.NewControlRequestIdentity(wireID)
	if !found || !valid {
		slog.Warn("cannot publish control request without a valid JSON-RPC ID", "agent_id", b.agentID)
		return
	}
	b.outstandingMu.Lock()
	if b.outstandingControls == nil {
		b.outstandingControls = make(map[string]outstandingControlRequest)
	}
	b.outstandingControls[identity.Key] = outstandingControlRequest{wireID: identity.Native, cancelAnswer: cancelAnswer}
	// Sampled in the SAME critical section that registers the record, so the check
	// below compares against the state this registration saw.
	generation := b.withdrawGeneration
	b.outstandingMu.Unlock()
	if err := sink.PublishControlRequest(agent.ControlRequest{AgentSessionID: agentSessionID, RequestID: identity.Key, Payload: content}); err != nil {
		slog.Error("publish control request", "agent_id", b.agentID, "request_id", identity.Key, "error", err)
		b.outstandingMu.Lock()
		delete(b.outstandingControls, identity.Key)
		b.outstandingMu.Unlock()
		// Queued, not waited for: PublishControlRequest runs on the goroutine that
		// drains the provider's stdout.
		b.SendErrorResponseDetached(identity.Native, -32603, ControlPublicationFailure,
			"control publication failure")
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
	//
	// BOTH terms are needed, and each one alone is wrong. The record being gone does
	// not mean a withdrawal took it: PublishControlRequest completes the browser
	// broadcast before it returns, so the reader can answer inside this window, and
	// the answer's own SendRawInput removes the record too -- on which a bare presence
	// test cancelled the card the reader had just allowed, and the browser showed it
	// vanishing as CANCELLED. The generation moving does not mean THIS record was
	// withdrawn either, because a withdrawal of any other request raises it. Only both
	// together describe the case this exists for.
	b.outstandingMu.Lock()
	_, stillRegistered := b.outstandingControls[identity.Key]
	withdrawn := b.withdrawGeneration != generation
	b.outstandingMu.Unlock()
	if !stillRegistered && withdrawn {
		sink.CancelControlRequest(identity.Key)
	}
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
	delete(b.outstandingControls, requestID)
	b.withdrawGeneration++
	b.outstandingMu.Unlock()
	sink.CancelControlRequest(requestID)
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
	if _, open := b.outstandingControls[requestID]; !open {
		b.outstandingMu.Unlock()
		return false
	}
	delete(b.outstandingControls, requestID)
	b.withdrawGeneration++
	b.outstandingMu.Unlock()
	sink.CancelControlRequest(requestID)
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
	b.outstandingControls = nil
	b.withdrawGeneration++
	b.outstandingMu.Unlock()
	for requestID, record := range outstanding {
		b.answerWithdrawnControl(requestID, record)
		sink.CancelControlRequest(requestID)
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
	answerable := make(map[string]outstandingControlRequest)
	for requestID, record := range b.outstandingControls {
		if record.cancelAnswer == nil {
			continue
		}
		answerable[requestID] = record
		delete(b.outstandingControls, requestID)
	}
	if len(answerable) > 0 {
		b.withdrawGeneration++
	}
	b.outstandingMu.Unlock()
	for requestID, record := range answerable {
		b.answerWithdrawnControl(requestID, record)
		sink.CancelControlRequest(requestID)
	}
}

// answerWithdrawnControl sends the provider the record's own cancel answer.
// A record that carries none releases nothing, so it sends nothing.
func (b *JSONRPCProcess) answerWithdrawnControl(requestID string, record outstandingControlRequest) {
	if record.cancelAnswer == nil {
		return
	}
	if err := b.SendResponse(record.wireID, record.cancelAnswer); err != nil {
		slog.Warn("cancel outstanding control request", "agent_id", b.agentID, "request_id", requestID, "error", err)
	}
}

// takeOutstandingControl removes the record that one outgoing frame answers, and
// returns the function that puts it back.
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
func (b *JSONRPCProcess) takeOutstandingControl(raw []byte) (restore func()) {
	nothing := func() {}
	wireID, _, found := agent.ExtractJSONRPCID(raw)
	if !found {
		return nothing
	}
	identity, valid := agent.NewControlRequestIdentity(wireID)
	if !valid {
		return nothing
	}
	b.outstandingMu.Lock()
	record, open := b.outstandingControls[identity.Key]
	if !open {
		b.outstandingMu.Unlock()
		return nothing
	}
	delete(b.outstandingControls, identity.Key)
	generation := b.withdrawGeneration
	b.outstandingMu.Unlock()
	return func() {
		b.outstandingMu.Lock()
		defer b.outstandingMu.Unlock()
		if b.withdrawGeneration != generation {
			return
		}
		if b.outstandingControls == nil {
			b.outstandingControls = make(map[string]outstandingControlRequest)
		}
		b.outstandingControls[identity.Key] = record
	}
}

// MCPElicitationCancelAnswer is the action the Model Context Protocol defines for an
// elicitation that the client withdraws without a reader decision.
func MCPElicitationCancelAnswer() any {
	return map[string]any{"action": contracts.MCPElicitationActionCancel}
}
