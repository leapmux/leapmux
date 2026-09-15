package agent

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// controlRequestIdentity is one JSON-RPC request id in the two spellings LeapMux needs.
//
// native holds the id bytes exactly as the provider wrote them, so a reply addresses
// the request the way the provider recognizes it. key is the canonical LeapMux lookup
// id: the control-request store, the outstanding-request registry, and the withdrawal
// path all use it, and they can only agree while one constructor produces it.
type controlRequestIdentity struct {
	native json.RawMessage
	key    string
}

// newControlRequestIdentity canonicalizes both JSON-RPC id branches into one key.
//
// A string id round-trips through the JSON decoder, so a literal and its escaped
// spelling give one key. A number round-trips through json.Number, so 12, 12.0 and
// 1.2e1 give one key also. The two branches cannot collide, because the string branch keeps its
// quotation marks: "12" and 12 stay two requests. The key belongs to LeapMux only, and
// the "jsonrpc:" prefix keeps it clear of a provider's own request ids.
//
// ok is false for an absent id, a null id, and a value that is neither a string nor a
// number.
func newControlRequestIdentity(nativeID json.RawMessage) (controlRequestIdentity, bool) {
	nativeID = bytes.TrimSpace(nativeID)
	if len(nativeID) == 0 || bytes.Equal(nativeID, []byte("null")) {
		return controlRequestIdentity{}, false
	}
	if nativeID[0] == '"' {
		var value string
		if json.Unmarshal(nativeID, &value) != nil {
			return controlRequestIdentity{}, false
		}
		canonical, err := json.Marshal(value)
		if err != nil {
			return controlRequestIdentity{}, false
		}
		return controlRequestIdentity{native: nativeID, key: "jsonrpc:" + string(canonical)}, true
	}
	var number json.Number
	if json.Unmarshal(nativeID, &number) != nil {
		return controlRequestIdentity{}, false
	}
	canonical, ok := canonicalJSONNumber(number)
	if !ok {
		return controlRequestIdentity{}, false
	}
	return controlRequestIdentity{native: nativeID, key: "jsonrpc:" + canonical}, true
}

// canonicalJSONNumber gives one spelling to every JSON number of the same value.
//
// An integer keeps its exact digits, because a 64-bit integer id is larger than a
// float64 can hold without loss. Every other number goes through float64, which is
// what the JSON specification allows a reader to do. Zero takes its own branch,
// because FormatFloat spells a negative zero "-0", and -0.0 is the same request as 0.
func canonicalJSONNumber(number json.Number) (string, bool) {
	if integer, err := number.Int64(); err == nil {
		return strconv.FormatInt(integer, 10), true
	}
	value, err := number.Float64()
	if err != nil {
		return "", false
	}
	if value == 0 {
		return "0", true
	}
	return strconv.FormatFloat(value, 'g', -1, 64), true
}

// JSONRPCControlRequestID is the canonical LeapMux id of one JSON-RPC control request.
// It is the cross-package spelling of newControlRequestIdentity's key, so a caller
// outside this package addresses a stored control request the way the publisher wrote
// it. Reports false for an id the publisher would refuse.
func JSONRPCControlRequestID(nativeID json.RawMessage) (string, bool) {
	identity, ok := newControlRequestIdentity(nativeID)
	return identity.key, ok
}

// storedControlRequestID uses the persisted identity when the service supplies it.
// Direct provider calls can use the native identity without a stored request.
func storedControlRequestID(ctx ControlResponseContext, nativeID string) string {
	if ctx.RequestID != "" {
		return ctx.RequestID
	}
	return nativeID
}
