package fastagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFastagentRootMessagesPreserveNativeOrderAndOwnedBytes(t *testing.T) {
	t.Parallel()
	first := `{"role":"user","content":[{"type":"text","text":"literal [ ] { } and \"quote\""}],"number":1.00}`
	second := `{"role":"assistant","tool_calls":{"second":{},"first":{}},"stop_reason":"endTurn"}`
	raw := []byte(`{"extra":{"literal":"[ ] { }"},"messages":[` + first + ",\n" + second + `]}`)
	var history struct {
		Messages fastagentRootMessages `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(raw, &history))
	require.Len(t, history.Messages, 2)
	assert.Equal(t, first, string(history.Messages[0]))
	assert.Equal(t, second, string(history.Messages[1]))
	clear(raw)
	assert.Equal(t, first, string(history.Messages[0]), "each message must retain its bytes after the input buffer changes")
	assert.Equal(t, second, string(history.Messages[1]))
}

func TestFastagentRootMessagesDistinguishMissingAndEmptyArrays(t *testing.T) {
	t.Parallel()
	var history struct {
		Messages fastagentRootMessages `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{}`), &history))
	assert.Nil(t, history.Messages, "the archive reader must refuse a missing message array")
	require.NoError(t, json.Unmarshal([]byte(`{"messages":[]}`), &history))
	assert.NotNil(t, history.Messages)
	assert.Empty(t, history.Messages)
	for _, raw := range []string{`{"messages":null}`, `{"messages":{}}`, `{"messages":"bad"}`, `{"messages":[}`, `{"messages":[]} {}`} {
		require.Error(t, json.Unmarshal([]byte(raw), &history), raw)
	}
}

func TestFastagentRootMessagesEnforceTheElementLimit(t *testing.T) {
	t.Parallel()
	for _, count := range []int{fastagentRootElementLimit, fastagentRootElementLimit + 1} {
		raw := []byte("[" + strings.Repeat("0,", count-1) + "0]")
		var messages fastagentRootMessages
		err := json.Unmarshal(raw, &messages)
		if count == fastagentRootElementLimit {
			require.NoError(t, err)
			assert.Len(t, messages, count)
		} else {
			require.ErrorContains(t, err, "element limit")
			assert.Nil(t, messages)
		}
	}
}

func TestFastagentMessageDigestMatchesCompactJSON(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		" \t\r\n{ \"array\" : [ 1, { \"value\" : true }, null ] } \n",
		`{"whitespace":" inside a string ","escaped":"\t\r\n"}`,
		`{"quote":"before \" after","backslash":"\\"}`,
		`{"odd":"\\\"","even":"\\\\","unicode":"\u0020"}`,
		`{"syntax":"[ ] { } , :","text":"한글 🧪"}`,
		`{"zero":-0,"large":9007199254740993,"fraction":1.00,"exponent":1e+00}`,
		`{"duplicate":1,"duplicate":2}`,
		`{"large":"` + strings.Repeat("x ", 4096) + `"}`,
	} {
		var compact bytes.Buffer
		require.NoError(t, json.Compact(&compact, []byte(raw)))
		expected := sha256.Sum256(compact.Bytes())
		actual, err := fastagentMessageDigest(json.RawMessage(raw))
		require.NoError(t, err)
		assert.Equal(t, expected, actual, raw)
	}
}

func TestFastagentMessageDigestPreservesNativeIdentity(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, left, right string
		equal             bool
	}{
		{name: "formatting", left: `{"x":0,"y":[1,2]}`, right: "{\n\t\"x\" : 0, \"y\" : [1, 2]\r\n}", equal: true},
		{name: "member order", left: `{"x":0,"y":1}`, right: `{"y":1,"x":0}`},
		{name: "duplicate member order", left: `{"x":1,"x":2}`, right: `{"x":2,"x":1}`},
		{name: "fraction spelling", left: `1`, right: `1.0`},
		{name: "exponent spelling", left: `1.0`, right: `1e0`},
		{name: "string escape spelling", left: `"a"`, right: `"\u0061"`},
		{name: "string whitespace", left: `"a b"`, right: `"ab"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			left, err := fastagentMessageDigest(json.RawMessage(test.left))
			require.NoError(t, err)
			right, err := fastagentMessageDigest(json.RawMessage(test.right))
			require.NoError(t, err)
			if test.equal {
				assert.Equal(t, left, right)
			} else {
				assert.NotEqual(t, left, right)
			}
		})
	}
}

func TestFastagentMessageDigestRejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", " \t\r\n", `{} {}`, `{"x":"\x"}`, `{"x":"unfinished}`, `{"x":[1,]}`, `{"x":01}`} {
		digest, err := fastagentMessageDigest(json.RawMessage(raw))
		require.Error(t, err, raw)
		assert.Equal(t, [sha256.Size]byte{}, digest)
	}
}
