package providerkit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsedLineHasIDRejectsWhitespaceAndNull(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", " ", "null", " \nnull\t"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			line := ParsedLine{ID: json.RawMessage(raw)}
			assert.False(t, line.HasID())
		})
	}
}

func TestRawKindOf(t *testing.T) {
	t.Parallel()
	assert.Equal(t, RawAbsent, RawKindOf(nil))
	for _, tc := range []struct {
		raw  string
		kind RawKind
	}{
		{"", RawAbsent}, {" ", RawAbsent}, {"null", RawNull}, {" null ", RawNull},
		{`""`, RawValue}, {`"x"`, RawValue}, {"0", RawValue}, {"{}", RawValue}, {"invalid", RawValue},
	} {
		assert.Equal(t, tc.kind, RawKindOf(json.RawMessage(tc.raw)), tc.raw)
		assert.Equal(t, tc.kind == RawValue, RawPresent(json.RawMessage(tc.raw)), tc.raw)
	}
}

func TestRawString(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw, value string
		ok         bool
	}{
		{`"text"`, "text", true}, {` "" `, "", true}, {`"a\nb"`, "a\nb", true},
		{"", "", false}, {"null", "", false}, {"12", "", false}, {"{}", "", false},
		{"invalid", "", false}, {`"a" "b"`, "", false},
	} {
		value, ok := RawString(json.RawMessage(tc.raw))
		assert.Equal(t, tc.ok, ok, tc.raw)
		assert.Equal(t, tc.value, value, tc.raw)
	}
}

func TestRawStringDoesNotAllocateForAnAbsentField(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, []byte(" "), []byte("null")} {
		allocations := testing.AllocsPerRun(100, func() {
			value, ok := RawString(raw)
			if ok || value != "" {
				panic("an absent or null field cannot contain a string")
			}
		})
		assert.Zero(t, allocations)
	}
}

func TestRawInt64(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw   string
		value int64
	}{
		{"12", 12}, {" 0 ", 0}, {"-12", -12}, {"9223372036854775807", 9223372036854775807},
		{"-9223372036854775808", -9223372036854775808},
	} {
		value, ok := RawInt64(json.RawMessage(tc.raw))
		assert.True(t, ok, tc.raw)
		assert.Equal(t, tc.value, value, tc.raw)
	}
	for _, raw := range []string{"", " ", "12.5", "1e2", `"12"`, "null", "9223372036854775808", "-9223372036854775809", "{}", "true", "01", "1 2", "invalid"} {
		_, ok := RawInt64(json.RawMessage(raw))
		assert.False(t, ok, raw)
	}
}

func TestStringField(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ raw, value string }{
		{`{"key":"value"}`, "value"}, {`{"key":""}`, ""}, {"{}", ""}, {`{"key":null}`, ""},
		{`{"key":12}`, ""}, {`["value"]`, ""}, {`"value"`, ""}, {"null", ""}, {"invalid", ""},
	} {
		assert.Equal(t, tc.value, StringField([]byte(tc.raw), "key"), tc.raw)
	}
}
