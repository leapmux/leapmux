package providerkit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestRawObject(t *testing.T) {
	fields, ok := RawObject(json.RawMessage(`{"a":1,"b":"x"}`))
	require.True(t, ok)
	assert.JSONEq(t, `1`, string(fields["a"]))
	empty, ok := RawObject(json.RawMessage(`{}`))
	assert.True(t, ok)
	assert.NotNil(t, empty, "an empty object reads as an empty map")
	for _, raw := range []string{``, `null`, `[]`, `[{"a":1}]`, `"a"`, `7`, `true`, `{`} {
		_, ok := RawObject(json.RawMessage(raw))
		assert.False(t, ok, raw)
	}
}

func TestRawObjectOrEmpty(t *testing.T) {
	assert.Equal(t, `{"a":1}`, string(RawObjectOrEmpty(json.RawMessage(`{"a":1}`))))
	for _, raw := range []string{``, `null`, `[]`, `"a"`, `7`} {
		assert.Equal(t, `{}`, string(RawObjectOrEmpty(json.RawMessage(raw))), raw)
	}
}

func TestObjectString(t *testing.T) {
	fields := map[string]json.RawMessage{"text": json.RawMessage(`"a"`), "number": json.RawMessage(`7`), "null": json.RawMessage(`null`)}
	assert.Equal(t, "a", ObjectString(fields, "text"))
	assert.Empty(t, ObjectString(fields, "number"), "a value of the wrong type reads as absent")
	assert.Empty(t, ObjectString(fields, "null"))
	assert.Empty(t, ObjectString(fields, "absent"))
	assert.Empty(t, ObjectString(nil, "text"))
}

func TestRawWholeNumber(t *testing.T) {
	for raw, want := range map[string]int64{`0`: 0, `-0`: 0, `7`: 7, `-3`: -3, `2.0`: 2, `1e2`: 100, `9007199254740991`: 1<<53 - 1, `-9007199254740991`: -(1<<53 - 1)} {
		got, ok := RawWholeNumber(json.RawMessage(raw))
		assert.True(t, ok, raw)
		assert.Equal(t, want, got, raw)
	}
	for _, raw := range []string{``, `null`, `1.5`, `"2"`, `true`, `[]`, `9007199254740992`, `1e300`, `-9007199254740992`} {
		_, ok := RawWholeNumber(json.RawMessage(raw))
		assert.False(t, ok, raw)
	}
}

func TestRawFloat64(t *testing.T) {
	for raw, want := range map[string]float64{`1`: 1, `1.0`: 1, `-2.5`: -2.5, `1e3`: 1000, ` 7 `: 7} {
		got, ok := RawFloat64(json.RawMessage(raw))
		require.True(t, ok, raw)
		assert.InDelta(t, want, got, 0, raw)
	}
	for _, raw := range []string{``, `null`, `"1"`, `true`, `[]`, `{}`, `1e400`, `1x`} {
		_, ok := RawFloat64(json.RawMessage(raw))
		assert.False(t, ok, raw)
	}
}

func TestRawFields(t *testing.T) {
	fields := RawFields(json.RawMessage(`{"a":1}`))
	assert.JSONEq(t, `1`, string(fields["a"]))
	assert.NotNil(t, RawFields(json.RawMessage(`{}`)), "an empty object reads as an empty map")
	for _, raw := range []string{``, `null`, `[]`, `"a"`, `7`, `{`} {
		assert.Nil(t, RawFields(json.RawMessage(raw)), raw)
	}
	assert.Empty(t, RawFields(json.RawMessage(`[1]`))["a"], "a read of a missing object chains to nothing")
}

func TestRawArray(t *testing.T) {
	elements, ok := RawArray(json.RawMessage(`[1,"a",null]`))
	require.True(t, ok)
	assert.Len(t, elements, 3)
	elements, ok = RawArray(json.RawMessage(`[]`))
	assert.True(t, ok, "an empty array is an array")
	assert.Empty(t, elements)
	for _, raw := range []string{``, `null`, `{}`, `"a"`, `7`, `[`} {
		_, ok := RawArray(json.RawMessage(raw))
		assert.False(t, ok, raw)
	}
}

func TestRawOptionalBool(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]*bool{
		`true`:     new(true),
		` false `:  new(false),
		``:         nil,
		`null`:     nil,
		`"true"`:   nil,
		`1`:        nil,
		`0`:        nil,
		`{}`:       nil,
		`truthy`:   nil,
		"\ttrue\n": new(true),
	} {
		assert.Equal(t, want, RawOptionalBool(json.RawMessage(raw)), "%q", raw)
	}
}

func TestRawStrings(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"a", "", "b"}, RawStrings(json.RawMessage(`["a", 1, "", null, {"x": "y"}, "b"]`)), "an element that is no string is dropped")
	for _, raw := range []string{``, `null`, `"a"`, `{}`, `[`} {
		got := RawStrings(json.RawMessage(raw))
		assert.NotNil(t, got, "%q reads as an empty list, never nil", raw)
		assert.Empty(t, got, "%q", raw)
	}
}
