package agent

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalAssembledMessageKeepsCompletionState(t *testing.T) {
	t.Parallel()

	raw, err := MarshalAssembledMessage(AssembledMessageKindReasoning, "partial text", MessageCompletionInterrupted)
	require.NoError(t, err)

	var value map[string]any
	require.NoError(t, json.Unmarshal(raw, &value))
	assert.Equal(t, "assembled_message", value["type"])
	assert.Equal(t, "reasoning", value["kind"])
	assert.Equal(t, "partial text", value["text"])
	assert.Equal(t, "interrupted", value["completion"])
}

func TestAnnotateMessageCompletionPreservesExistingMetadata(t *testing.T) {
	t.Parallel()

	raw, err := AnnotateMessageCompletion(
		[]byte(`{"sessionUpdate":"tool_call_update","_leapmux":{"trace":"kept"}}`),
		MessageCompletionError,
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"sessionUpdate":"tool_call_update",
		"_leapmux":{"trace":"kept","completion":"error"}
	}`, string(raw))
}

func TestAnnotateMessageCompletionReplacesProviderCollision(t *testing.T) {
	t.Parallel()

	raw, err := AnnotateMessageCompletion([]byte(`{"_leapmux":"provider value"}`), MessageCompletionInterrupted)
	require.NoError(t, err)
	assert.JSONEq(t, `{"_leapmux":{"completion":"interrupted"}}`, string(raw))
}

func TestMessageMetadataDerivesAssembledAndProviderCompletion(t *testing.T) {
	t.Parallel()

	kind, completion := MessageMetadata([]byte(`{"type":"assembled_message","kind":"plan","text":"x","completion":"complete"}`))
	assert.Equal(t, leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_PLAN, kind)
	assert.Equal(t, leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_COMPLETE, completion)

	kind, completion = MessageMetadata([]byte(`{"type":"tool","_leapmux":{"completion":"error"}}`))
	assert.Equal(t, leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED, kind)
	assert.Equal(t, leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_ERROR, completion)
}

func TestMessageMetadataDoesNotReuseKindAsCompletion(t *testing.T) {
	t.Parallel()

	kind, completion := MessageMetadata([]byte(`{"type":"assembled_message","kind":"error","text":"x"}`))
	assert.Equal(t, leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_UNSPECIFIED, kind)
	assert.Equal(t, leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_UNSPECIFIED, completion)
}

func TestMessageMetadataUsesTheAssembledCompletion(t *testing.T) {
	t.Parallel()

	kind, completion := MessageMetadata([]byte(`{
		"type":"assembled_message",
		"kind":"text",
		"text":"x",
		"completion":"complete",
		"_leapmux":{"completion":"error"}
	}`))
	assert.Equal(t, leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_TEXT, kind)
	assert.Equal(t, leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_COMPLETE, completion)
}

func TestAnnotateMessageCompletionAcceptsNullMetadata(t *testing.T) {
	t.Parallel()

	raw, err := AnnotateMessageCompletion([]byte(`{"_leapmux":null}`), MessageCompletionInterrupted)
	require.NoError(t, err)
	assert.JSONEq(t, `{"_leapmux":{"completion":"interrupted"}}`, string(raw))
}
