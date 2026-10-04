package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveControlResponse_NoopKeepsRawResponse(t *testing.T) {
	t.Parallel()

	content := []byte(`{"id":1,"result":{"ok":true}}`)
	res := ProviderDefaults{}.ResolveControlResponse(ControlResponseContext{ResponseContent: content})

	assert.Equal(t, content, res.Content)
	assert.False(t, res.SelfDisplayed)
	assert.Equal(t, PlanModeControlNone, res.PlanModeControl)
}

func TestDecodeControlBehavior(t *testing.T) {
	t.Parallel()

	// A real deny with a typed reason: request id + behavior + message all surface, trimmed.
	id, behavior, message, ok := DecodeControlBehavior([]byte(
		`{"response":{"request_id":" req-1 ","response":{"behavior":" deny ","message":" not this way "}}}`))
	assert.True(t, ok)
	assert.Equal(t, "req-1", id)
	assert.Equal(t, ControlBehaviorDeny, behavior)
	assert.Equal(t, "not this way", message)

	// The ControlRejectedByUserMessage placeholder is collapsed to "" -- a bare rejection carries
	// no reason, so both the Codex feedback path and the Cursor transform treat it as empty.
	_, _, message, ok = DecodeControlBehavior([]byte(
		`{"response":{"request_id":"req-2","response":{"behavior":"deny","message":"Rejected by user."}}}`))
	assert.True(t, ok)
	assert.Empty(t, message, "the ControlRejectedByUserMessage placeholder collapses to \"\"")

	// An allow with no message.
	_, behavior, message, ok = DecodeControlBehavior([]byte(`{"response":{"request_id":"req-3","response":{"behavior":"allow"}}}`))
	assert.True(t, ok)
	assert.Equal(t, ControlBehaviorAllow, behavior)
	assert.Empty(t, message)

	// Malformed JSON: ok is false and every field is empty.
	id, behavior, message, ok = DecodeControlBehavior([]byte(`not json`))
	assert.False(t, ok)
	assert.Empty(t, id)
	assert.Empty(t, behavior)
	assert.Empty(t, message)
}

func TestDecodeControlChoice(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Reject and Exit", DecodeControlChoice([]byte(
		`{"response":{"request_id":"req-1","response":{"behavior":"deny","choice":" Reject and Exit "}}}`)),
		"the choice is trimmed")
	assert.Empty(t, DecodeControlChoice([]byte(`{"response":{"request_id":"req-2","response":{"behavior":"allow"}}}`)),
		"a plain decision carries no choice")
	assert.Empty(t, DecodeControlChoice([]byte(`{"response":{"request_id":"req-3","response":{"behavior":"allow","choice":"   "}}}`)))
	assert.Empty(t, DecodeControlChoice([]byte(`not json`)))
	assert.Empty(t, DecodeControlChoice(nil))
	assert.Empty(t, DecodeControlChoice([]byte(`{"response":{"response":{"choice":7}}}`)), "a choice that is not a string is no choice")
}

func TestNormalizeRejectionMessage(t *testing.T) {
	t.Parallel()

	// A typed reason surfaces trimmed.
	assert.Equal(t, "not this way", NormalizeRejectionMessage("  not this way  "))
	// The auto-filled placeholder collapses to "" (no real feedback), including when padded.
	assert.Empty(t, NormalizeRejectionMessage(ControlRejectedByUserMessage))
	assert.Empty(t, NormalizeRejectionMessage("  "+ControlRejectedByUserMessage+"  "))
	// A genuinely empty / whitespace reason is "".
	assert.Empty(t, NormalizeRejectionMessage(""))
	assert.Empty(t, NormalizeRejectionMessage("   \n "))
	// DecodeControlBehavior and NormalizeRejectionMessage apply the SAME rule -- the sentinel
	// collapse must not drift between the raw-bytes decoder and the shared helper.
	_, _, decoded, ok := DecodeControlBehavior([]byte(
		`{"response":{"request_id":"r","response":{"behavior":"deny","message":"  ` + ControlRejectedByUserMessage + `  "}}}`))
	assert.True(t, ok)
	assert.Equal(t, NormalizeRejectionMessage("  "+ControlRejectedByUserMessage+"  "), decoded)
}
