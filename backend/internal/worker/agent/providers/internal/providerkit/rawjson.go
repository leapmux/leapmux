package providerkit

import (
	"bytes"
	"encoding/json"
	"math"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// RawKind distinguishes an absent field from JSON null and other values.
type RawKind uint8

const (
	RawAbsent RawKind = iota
	RawNull
	RawValue
)

// RawKindOf ignores surrounding whitespace. It does not validate other values.
func RawKindOf(raw json.RawMessage) RawKind {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return RawAbsent
	}
	if bytes.Equal(raw, []byte("null")) {
		return RawNull
	}
	return RawValue
}

// RawPresent reports whether a field holds bytes other than whitespace or JSON null.
func RawPresent(raw json.RawMessage) bool {
	return RawKindOf(raw) == RawValue
}

// RawString reads a JSON string. Other values return false.
func RawString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

// RawInt64 reads an integer JSON number without rounding or accepting quoted numbers.
func RawInt64(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, false
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return 0, false
	}
	value, err := number.Int64()
	return value, err == nil
}

// RawBool reads a JSON boolean. Other values return false, so a malformed
// flag can never read as true.
func RawBool(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("true"))
}

// RawOptionalBool reads a JSON boolean, or nil for any other value and for
// none, as a proto optional field holds it. Unlike RawBool, it tells an
// absent or malformed flag from an explicit false.
func RawOptionalBool(raw json.RawMessage) *bool {
	var value bool
	switch string(bytes.TrimSpace(raw)) {
	case "true":
		value = true
	case "false":
	default:
		return nil
	}
	return &value
}

// StringField reads a string member of a JSON object. Other inputs return an empty string.
func StringField(content []byte, key string) string {
	return ObjectString(RawFields(content), key)
}

// RawObject reads a JSON object into its fields. Any other value, JSON null
// and an array included, reports false.
func RawObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

// RawFields reads a JSON object into its fields, or nil for any other value,
// JSON null and an array included. A reader that chains field reads uses
// it; one that must tell an empty object from no object uses RawObject.
func RawFields(raw json.RawMessage) map[string]json.RawMessage {
	fields, _ := RawObject(raw)
	return fields
}

// ReadSourceObject reads the transcript row linked to a control request as a
// JSON object map, or returns nil when no source row exists or its original
// content is not a JSON object.
func ReadSourceObject(input agent.ControlRequestInput) map[string]json.RawMessage {
	if input.Source == nil {
		return nil
	}
	row, ok := input.Source()
	if !ok {
		return nil
	}
	return RawFields(row.Original)
}

// RawArray reads a JSON array into its elements. Any other value, JSON null
// included, reports false; an empty array reports true.
func RawArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	var elements []json.RawMessage
	if json.Unmarshal(raw, &elements) != nil || elements == nil {
		return nil, false
	}
	return elements, true
}

// RawStrings reads the string elements of a JSON array, and drops every
// other element. A value that is no array reads as an empty list, never nil,
// so a caller that encodes the list writes `[]`.
func RawStrings(raw json.RawMessage) []string {
	elements, _ := RawArray(raw)
	values := make([]string, 0, len(elements))
	for _, element := range elements {
		if value, ok := RawString(element); ok {
			values = append(values, value)
		}
	}
	return values
}

// RawObjectOrEmpty returns raw when it holds a JSON object, and `{}` for any
// other value. A prompt states it as the arguments of a call, so a call
// whose arguments are no object states an empty object.
func RawObjectOrEmpty(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return json.RawMessage("{}")
	}
	return raw
}

// ObjectString reads the string member key of fields, or "" when the member
// is absent or is no string.
func ObjectString(fields map[string]json.RawMessage, key string) string {
	value, _ := RawString(fields[key])
	return value
}

// maxSafeInteger is the largest whole number that a JSON reader of double
// precision states exactly, as JavaScript's Number.MAX_SAFE_INTEGER.
const maxSafeInteger = 1<<53 - 1

// RawWholeNumber reads a JSON number whose value is a whole number that a
// double states exactly, as JavaScript's Number.isSafeInteger judges it:
// `2` and `2.0` are whole, and `1.5`, `"2"` and `null` are not.
func RawWholeNumber(raw json.RawMessage) (int64, bool) {
	if value, ok := RawInt64(raw); ok {
		if value < -maxSafeInteger || value > maxSafeInteger {
			return 0, false
		}
		return value, true
	}
	number, ok := RawFloat64(raw)
	if !ok || math.Trunc(number) != number || math.Abs(number) > maxSafeInteger {
		return 0, false
	}
	return int64(number), true
}

// RawFloat64 reads a JSON number, a fraction or an exponent included. A
// quoted number, any other value, and a number past the range of float64
// return false.
func RawFloat64(raw json.RawMessage) (float64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, false
	}
	var value float64
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}
