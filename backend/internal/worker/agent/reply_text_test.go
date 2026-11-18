package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingReplyProvider answers ReplyText with a fixed reply and records
// each frame that reaches it.
type recordingReplyProvider struct {
	ProviderDefaults
	calls *[]MessageContent
	text  string
	ok    bool
}

func (p recordingReplyProvider) ReplyText(message MessageContent) (string, bool) {
	*p.calls = append(*p.calls, message)
	return p.text, p.ok
}

func newRecordingReplyProvider(text string, ok bool) (recordingReplyProvider, *[]MessageContent) {
	calls := &[]MessageContent{}
	return recordingReplyProvider{calls: calls, text: text, ok: ok}, calls
}

func TestReplyTextReadsAValidAssembledTextRowItself(t *testing.T) {
	t.Parallel()
	provider, calls := newRecordingReplyProvider("the provider's", true)
	text, ok := ReplyText(provider, MessageContent{Original: []byte(`{"type":"assembled_message","kind":"text","text":"The tests pass.","completion":"interrupted"}`)})
	assert.True(t, ok)
	assert.Equal(t, "The tests pass.", text)
	assert.Empty(t, *calls, "a valid envelope never reaches the provider")
}

func TestReplyTextRefusesRowsBeforeTheProvider(t *testing.T) {
	t.Parallel()
	for name, original := range map[string]string{
		"a valid reasoning row":          `{"type":"assembled_message","kind":"reasoning","text":"Thinking.","completion":"complete"}`,
		"a valid plan row":               `{"type":"assembled_message","kind":"plan","text":"1. Edit.","completion":"complete"}`,
		"a malformed reasoning row":      `{"type":"assembled_message","kind":"reasoning","text":"Thinking."}`,
		"a malformed plan row":           `{"type":"assembled_message","kind":"plan","text":"1. Edit."}`,
		"a notification thread":          `{"type":"notification_thread","messages":[{"type":"text","text":"Retrying."}]}`,
		"content that is not JSON":       `not json`,
		"content that is not an object":  `["The tests pass."]`,
		"a valid text row of blank text": `{"type":"assembled_message","kind":"text","text":" \n\t","completion":"complete"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			provider, calls := newRecordingReplyProvider("the provider's", true)
			text, ok := ReplyText(provider, MessageContent{Original: []byte(original)})
			assert.False(t, ok)
			assert.Empty(t, text)
			assert.Empty(t, *calls)
		})
	}
}

func TestReplyTextHandsAMalformedTextRowToTheProvider(t *testing.T) {
	t.Parallel()
	for name, original := range map[string]string{
		"no completion":      `{"type":"assembled_message","kind":"text","text":"Hello."}`,
		"unknown completion": `{"type":"assembled_message","kind":"text","text":"Hello.","completion":"paused"}`,
		"unknown kind":       `{"type":"assembled_message","kind":"summary","text":"Hello.","completion":"complete"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			provider, calls := newRecordingReplyProvider("Hello.", true)
			text, ok := ReplyText(provider, MessageContent{Original: []byte(original)})
			assert.True(t, ok)
			assert.Equal(t, "Hello.", text)
			require.Len(t, *calls, 1)
			assert.JSONEq(t, original, string((*calls)[0].Original))
		})
	}
}

func TestReplyTextHandsTheProviderTheResolvedFrameItsSupplementAndItsSession(t *testing.T) {
	t.Parallel()
	provider, calls := newRecordingReplyProvider("Done.", true)
	content, err := DecodeMessageSupplement(
		[]byte(`{"type":"native","text":"Done."}`),
		[]byte(`{"provider":{"part":"content"},"metadata":{"num_tool_uses":2}}`),
	)
	require.NoError(t, err)
	content.AgentSessionID = "session-1"
	text, ok := ReplyText(provider, content)
	assert.True(t, ok)
	assert.Equal(t, "Done.", text)
	require.Len(t, *calls, 1)
	got := (*calls)[0]
	assert.JSONEq(t, `{"type":"native","text":"Done.","num_tool_uses":2}`, string(got.Original), "the frame carries the row's metadata, as the browser's resolved frame does")
	assert.JSONEq(t, `{"part":"content"}`, string(got.Supplemental))
	assert.Equal(t, "session-1", got.AgentSessionID)
	assert.Empty(t, got.Metadata, "the metadata is already in the frame")
}

func TestReplyTextTrimsTheReplyAsTheBrowsersQuoteDoes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reply string
		want  string
		ok    bool
	}{
		{reply: "", ok: false},
		{reply: " \n\t\r\v\f", ok: false},
		{reply: "\u00a0\u3000\u2028\u2029", ok: false},
		{reply: "\ufeff", ok: false},
		{reply: "\u0085", want: "\u0085", ok: true},
		{reply: "  Hi\n", want: "Hi", ok: true},
		{reply: "\n\n# A\n\n  b\n", want: "# A\n\n  b", ok: true},
		{reply: "\u200bHi", want: "\u200bHi", ok: true},
	} {
		provider, _ := newRecordingReplyProvider(tc.reply, true)
		text, ok := ReplyText(provider, MessageContent{Original: []byte(`{"type":"native"}`)})
		assert.Equal(t, tc.ok, ok, "%q", tc.reply)
		assert.Equal(t, tc.want, text, "%q", tc.reply)
	}
	provider, _ := newRecordingReplyProvider("Hi", false)
	_, ok := ReplyText(provider, MessageContent{Original: []byte(`{"type":"native"}`)})
	assert.False(t, ok, "a provider that reads no reply keeps none, whatever text it returns")
}
