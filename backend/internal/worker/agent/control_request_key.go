package agent

import (
	"encoding/json"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The key of a stored control request.
//
// The Worker stores each control request under the key that its provider
// publishes it with (ControlRequest.RequestID), and every provider derives that
// key from the request itself: from the payload, and for a question that
// LeapMux asks for one session, from that session. So a stored payload that
// gives another key, or none, means a corrupt store or a fault in the key's
// derivation. The Worker then refuses every answer to the request with
// RefusalUnreadableRequest, before a provider reads the payload to answer it
// (StoredControlRequestAgrees and Provider.ControlRequestKey).

// envelopeRequestIDField is the member of the control_request envelope that
// states the request's key.
const envelopeRequestIDField = "request_id"

// EnvelopeControlRequestKey returns the key of a request in the
// control_request envelope that Claude Code's control protocol defined: its
// top-level request_id, exactly as the payload states it. ok is false for a
// payload that is no JSON object, and for a request_id that is absent, empty,
// or not a string.
func EnvelopeControlRequestKey(payload []byte) (string, bool) {
	fields, ok := jsonObjectFields(payload)
	if !ok {
		return "", false
	}
	return nonEmptyJSONString(fields[envelopeRequestIDField])
}

// JSONRPCControlRequestIdentity returns the identity of a JSON-RPC request
// from its id (NewControlRequestIdentity). ok is false for a payload that
// states no id, a null id, or an id that is neither a string nor a number.
//
// The publication of a JSON-RPC request keys it by this identity, and
// ProviderDefaults.ControlRequestKey reads the key back with it, so the two
// cannot derive different keys.
func JSONRPCControlRequestIdentity(payload []byte) (ControlRequestIdentity, bool) {
	wireID, _, found := ExtractJSONRPCID(payload)
	if !found {
		return ControlRequestIdentity{}, false
	}
	return NewControlRequestIdentity(wireID)
}

// ControlRequestKey defaults to the two shapes that several providers
// publish: the control_request envelope, keyed by its request_id
// (EnvelopeControlRequestKey), and a JSON-RPC request, keyed by its id
// (JSONRPCControlRequestIdentity). A payload that states a request_id is an
// envelope, so a JSON-RPC id beside it states no key: ZCode stores the wire
// id of its request beside the envelope's key.
func (ProviderDefaults) ControlRequestKey(payload []byte, _ string) (string, bool) {
	fields, ok := jsonObjectFields(payload)
	if !ok {
		return "", false
	}
	if _, envelope := fields[envelopeRequestIDField]; envelope {
		return nonEmptyJSONString(fields[envelopeRequestIDField])
	}
	identity, ok := JSONRPCControlRequestIdentity(payload)
	return identity.Key, ok
}

// StoredControlRequestAgrees reports whether the payload of a stored control
// request agrees with the key that stores it: whether plugin derives requestID
// from the payload and the request's provider session
// (Provider.ControlRequestKey). A payload that is no JSON object states no
// key, and it reaches no provider, as it reaches no prompt reader
// (UnreadableControlPrompt).
func StoredControlRequestAgrees(plugin Provider, requestID string, payload []byte, agentSessionID string) bool {
	if requestID == "" || controlPayloadFault(payload) != leapmuxv1.ControlPayloadFault_CONTROL_PAYLOAD_FAULT_UNSPECIFIED {
		return false
	}
	key, ok := plugin.ControlRequestKey(payload, agentSessionID)
	return ok && key == requestID
}

// jsonObjectFields returns the members of payload, and false for a payload
// that is no JSON object.
func jsonObjectFields(payload []byte) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

// nonEmptyJSONString returns the text of a JSON string, and false for an
// absent value, a value that is not a string, and the empty string.
func nonEmptyJSONString(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) != nil || text == "" {
		return "", false
	}
	return text, true
}
