package agent

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
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
// It reads the digits EXACTLY, as an arbitrary-precision rational, so 12, 12.0 and
// 1.2e1 all give "12" whatever their magnitude, and two ids that differ give two
// keys whatever their magnitude.
//
// float64 cannot do this job, and the failure is not at the edges. A parse that
// routed anything but an int64 through float64 split one value into two keys from
// 1e6 upward, because FormatFloat's shortest form turns to exponent notation at an
// exponent of 6: 1000000 spelled "1000000" and 1000000.0 spelled "1e+06". A
// timestamp-shaped id, which is what several runtimes use, sits far above that.
// Past 2^53 the same parse also COLLIDED two distinct integer ids onto one key,
// because float64 has no digits left to tell them apart. Either way a cancel that
// spelled its id differently from the request matched no record, so the request's
// browser card stayed on screen with nothing able to retire it.
//
// A fractional id keeps the reduced fraction that big.Rat produces. No runtime
// sends one; what matters is that the spelling is deterministic and that two equal
// values cannot reach two keys. A negative zero needs no special case, because
// big.Rat holds no sign for zero.
//
// The magnitude check is what big.Rat does NOT supply, and it must run BEFORE the
// parse. `Rat.SetString` refuses a base-5 exponent above 1e6 and nothing else, so it
// accepts the eight-byte literal 1e999999 and expands it to a million-digit integer:
// it computes 5^999999, shifts left 999999 bits, and renders a one-megabyte decimal
// that then becomes a map key in outstandingControls, the request_id of a stored row,
// and the id of a browser card. That is tens of milliseconds and megabytes of
// allocation on the goroutine that drains the provider's stdout, for one frame the
// child can repeat. 1e-999999 costs the same through RatString.
func canonicalJSONNumber(number json.Number) (string, bool) {
	text := number.String()
	if !jsonNumberWithinIDLimit(text) {
		return "", false
	}
	rational, ok := new(big.Rat).SetString(text)
	if !ok {
		return "", false
	}
	if rational.IsInt() {
		return rational.Num().String(), true
	}
	return rational.RatString(), true
}

// canonicalJSONNumberDigitLimit caps the significant digits of a numeric JSON-RPC id,
// and its exponent in either direction.
//
// A JSON-RPC id is a counter, a timestamp, or a random integer, so 64 digits is far
// above every real id and far below a magnitude that costs anything to render. A
// runtime that needs more can send a string id, which takes no canonical form.
const canonicalJSONNumberDigitLimit = 64

// jsonNumberWithinIDLimit reports whether a JSON number is small enough to canonicalize.
//
// It reads the LITERAL, because the expansion is the cost this refuses to pay. The
// literal is already valid JSON: encoding/json checked the syntax when it decoded the
// frame, so this only measures it.
func jsonNumberWithinIDLimit(text string) bool {
	mantissa := text
	if marker := strings.IndexAny(text, "eE"); marker >= 0 {
		mantissa = text[:marker]
		exponent, err := strconv.Atoi(text[marker+1:])
		if err != nil || exponent > canonicalJSONNumberDigitLimit || exponent < -canonicalJSONNumberDigitLimit {
			return false
		}
	}
	return len(mantissa) <= canonicalJSONNumberDigitLimit
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
