package jsonfield

import (
	"testing"

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
