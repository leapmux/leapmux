package amp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONCToJSON(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain JSON stays as it is",
			in:   `{"a":[1,2],"b":{"c":"d"}}`,
			want: `{"a":[1,2],"b":{"c":"d"}}`,
		},
		{
			name: "line comments",
			in:   "{\n  // the mode\n  \"a\": 1 // trailing\n}",
			want: `{"a":1}`,
		},
		{
			name: "a line comment at the end of the file",
			in:   "{\"a\": 1}\n// done",
			want: `{"a":1}`,
		},
		{
			name: "block comments",
			in:   "{/* one */\"a\": /* two\nlines */ 1}",
			want: `{"a":1}`,
		},
		{
			name: "a block comment separates two tokens",
			in:   `{"a":/**/1}`,
			want: `{"a":1}`,
		},
		{
			name: "comment markers inside a string",
			in:   `{"url":"https://ampcode.com/a", "glob":"/*.go", "end":"*/"}`,
			want: `{"url":"https://ampcode.com/a","glob":"/*.go","end":"*/"}`,
		},
		{
			name: "escaped quotes inside a string",
			in:   `{"a":"say \"// not a comment\"", "b": 2}`,
			want: `{"a":"say \"// not a comment\"","b":2}`,
		},
		{
			name: "an escaped backslash ends the string",
			in:   `{"path":"C:\\", "b": 2, }`,
			want: `{"path":"C:\\","b":2}`,
		},
		{
			name: "trailing commas in objects and arrays",
			in:   "{\"a\": [1, 2, ], \"b\": {\"c\": 3,\n},\n}",
			want: `{"a":[1,2],"b":{"c":3}}`,
		},
		{
			name: "a trailing comma before a comment",
			in:   "{\"a\": 1, // last\n}",
			want: `{"a":1}`,
		},
		{
			name: "a comma inside a string stays",
			in:   `{"a": ",]", "b": ",}"}`,
			want: `{"a":",]","b":",}"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := jsoncToJSON([]byte(tc.in))
			require.NoError(t, err)
			require.Truef(t, json.Valid(out), "the output is valid JSON: %s", out)
			assert.JSONEq(t, tc.want, string(out))
		})
	}
}

func TestJSONCToJSONRefusesAnUnclosedBlockComment(t *testing.T) {
	t.Parallel()
	_, err := jsoncToJSON([]byte(`{"a": 1 /* never closes }`))
	assert.ErrorContains(t, err, "does not close")

	// The `*` that opens the comment cannot also close it.
	_, err = jsoncToJSON([]byte(`{"a": 1 /*/ }`))
	assert.ErrorContains(t, err, "does not close")
}

// The converter changes only comments and trailing commas. A text that is not
// JSON stays as broken as it was, so the JSON reader that follows refuses it
// with its own reason.
func TestJSONCToJSONLeavesOtherErrorsToTheJSONReader(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]string{
		"a slash that opens no comment":                    `{"a": 1} /`,
		"a string that never closes hides comment markers": `{"a": "open // not a comment /* nor this`,
		"a comma with nothing before the close":            `{,}`,
		"a comma with nothing before the close of a list":  `[,]`,
		"a comma after a comment that follows the open":    `{ /* note */ , }`,
		"a comma in a nested list with no value":           `{"a": [ , ]}`,
		"a comma after a key with no value":                `{"a": ,}`,
		"two commas before the close":                      `[1,,]`,
	} {
		out, err := jsoncToJSON([]byte(in))
		require.NoErrorf(t, err, "%s converts", name)
		assert.Falsef(t, json.Valid(out), "%s stays invalid JSON: %s", name, out)
	}

	out, err := jsoncToJSON([]byte(`{"a": "open // not a comment /* nor this`))
	require.NoError(t, err)
	assert.Equal(t, `{"a": "open // not a comment /* nor this`, string(out), "nothing inside a string is a comment")
}

func TestJSONCToJSONKeepsEmptyInputEmpty(t *testing.T) {
	t.Parallel()
	out, err := jsoncToJSON(nil)
	require.NoError(t, err)
	assert.Empty(t, out)
}

// Each block comment costs time in proportion to its own length, not to the
// rest of the file. The reader copied the rest of the file for each comment,
// which made the time quadratic in the count of comments.
func TestJSONCToJSONDoesNotCopyTheRestOfTheFileForEachComment(t *testing.T) {
	const comments = 2000
	text := []byte("{" + strings.Repeat("/**/", comments) + `"a": 1}`)
	allocs := testing.AllocsPerRun(3, func() {
		out, err := jsoncToJSON(text)
		if err != nil || !json.Valid(out) {
			t.Fatalf("jsoncToJSON: %v", err)
		}
	})
	assert.Less(t, allocs, float64(comments/10), "the allocations do not grow with the count of comments")
}
