package providerkit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONStringify(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		value any
		want  string
	}{
		"HTML characters stay as they are": {value: "a && b <c>", want: `"a && b <c>"`},
		"line and paragraph separators stay as they are": {
			value: "a\u2028b\u2029c",
			want:  "\"a\u2028b\u2029c\"",
		},
		"an escaped backslash before u2028 stays escaped": {value: `\u2028`, want: `"\\u2028"`},
		"other escapes stay escaped":                      {value: "tab\t\"quote\"\nline", want: `"tab\t\"quote\"\nline"`},
		"a list keeps its order":                          {value: []any{"tool", "item", 3}, want: `["tool","item",3]`},
		"a map sorts its keys":                            {value: map[string]string{"b": "2", "a": "1"}, want: `{"a":"1","b":"2"}`},
		"no trailing newline":                             {value: true, want: `true`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := JSONStringify(tc.value)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
			assert.True(t, json.Valid(got), "the result is JSON")
		})
	}
}

func TestJSONStringifyReturnsTheEncodingError(t *testing.T) {
	t.Parallel()
	_, err := JSONStringify(make(chan int))
	require.Error(t, err)
}

func TestJSONTextPanicsOnAnEncodingError(t *testing.T) {
	t.Parallel()
	assert.Equal(t, `["a","b"]`, string(JSONText([]string{"a", "b"})))
	assert.Panics(t, func() { JSONText(make(chan int)) })
}
