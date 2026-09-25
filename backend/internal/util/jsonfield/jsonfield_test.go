package jsonfield

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetPreservesUnrelatedBytesAndLastKeySemantics(t *testing.T) {
	for _, tc := range []struct {
		input, value, expected string
		path                   []string
	}{
		{` { "id":1, "id" : "old", "x":1,"x":2,"n":999999999999999999999 } `, `0`, ` { "id":1, "id" : 0, "x":1,"x":2,"n":999999999999999999999 } `, []string{"id"}},
		{`{"\u0069d":false}`, `""`, `{"\u0069d":""}`, []string{"id"}},
		{` { "response" : { "value": [ 1, 2 ] } } `, `false`, ` { "response" : { "value": false } } `, []string{"response", "value"}},
		{` { } `, `0`, ` { "x":0} `, []string{"x"}},
		{`{"a":0  }`, `false`, `{"a":0  ,"x":false}`, []string{"x"}},
	} {
		original := []byte(tc.input)
		result, err := Set(original, []byte(tc.value), tc.path...)
		require.NoError(t, err)
		require.Equal(t, tc.expected, string(result))
		require.Equal(t, tc.input, string(original))
		value, err := Get(result, tc.path...)
		require.NoError(t, err)
		require.Equal(t, tc.value, string(value))
		value[0] = 'x'
		require.Equal(t, tc.expected, string(result), "Get must return owned bytes")
	}
}

func TestAppendPreservesArrayBytes(t *testing.T) {
	for _, tc := range []struct{ input, expected string }{
		{` [ ] `, ` [ false] `},
		{` [999999999999999999999, {"x":1,"x":2} ] `, ` [999999999999999999999, {"x":1,"x":2} ,false] `},
	} {
		result, err := Append([]byte(tc.input), []byte(`false`))
		require.NoError(t, err)
		require.Equal(t, tc.expected, string(result))
	}
}

func TestInvalidContainersAndValuesRemainErrors(t *testing.T) {
	for _, input := range []string{"", "{broken", "{} {}", "[]", "null", "0", `"text"`} {
		_, err := Set([]byte(input), []byte(`0`), "field")
		require.Error(t, err, input)
	}
	_, err := Set([]byte(`{}`), []byte(`0`), "absent", "child")
	require.ErrorIs(t, err, ErrMissing)
	_, err = Set([]byte(`{}`), []byte(`broken`), "field")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Set([]byte(`{}`), []byte(`0`))
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Get([]byte(`{}`), "absent")
	require.ErrorIs(t, err, ErrMissing)
	_, err = Get([]byte(`{}`))
	require.ErrorIs(t, err, ErrInvalid)
	for _, input := range []string{"", "{}", "null", "0", "[broken"} {
		_, err := Append([]byte(input), []byte(`0`))
		require.Error(t, err, input)
	}
}

func TestEqualComparesTheCompactForm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		left, right string
		want        bool
	}{
		{name: "the same bytes", left: `{"a":1}`, right: `{"a":1}`, want: true},
		{name: "spacing alone", left: "{ \"a\" : [1, 2] }\n", right: `{"a":[1,2]}`, want: true},
		{name: "two empty values", left: ``, right: ``, want: true},
		{name: "an empty value and a value", left: ``, right: `null`, want: false},
		{name: "a value and an empty value", left: `{}`, right: ``, want: false},
		{name: "a different value", left: `{"a":1}`, right: `{"a":2}`, want: false},
		// Compact keeps key order, so a reorder is a different encoding. The caller
		// compares a value with the copy it wrote, which keeps the order.
		{name: "a reordered object", left: `{"a":1,"b":2}`, right: `{"b":2,"a":1}`, want: false},
		{name: "invalid JSON on the left", left: `{`, right: `{`, want: false},
		{name: "invalid JSON on the right", left: `{}`, right: `{]`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Equal(json.RawMessage(tc.left), json.RawMessage(tc.right)))
		})
	}
}
