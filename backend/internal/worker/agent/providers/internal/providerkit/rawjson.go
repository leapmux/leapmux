package providerkit

import (
	"bytes"
	"encoding/json"
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

// StringField reads a string member of a JSON object. Other inputs return an empty string.
func StringField(content []byte, key string) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(content, &fields) != nil {
		return ""
	}
	value, _ := RawString(fields[key])
	return value
}
