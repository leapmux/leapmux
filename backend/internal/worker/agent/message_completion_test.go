package agent

import (
	"encoding/json"
	"testing"

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

func TestAnnotateMessageCompletionRejectsNonObjectMetadata(t *testing.T) {
	t.Parallel()

	_, err := AnnotateMessageCompletion([]byte(`{"_leapmux":"reserved"}`), MessageCompletionInterrupted)
	require.ErrorContains(t, err, "completion metadata")
}

func TestAnnotateMessageCompletionAcceptsNullMetadata(t *testing.T) {
	t.Parallel()

	raw, err := AnnotateMessageCompletion([]byte(`{"_leapmux":null}`), MessageCompletionInterrupted)
	require.NoError(t, err)
	assert.JSONEq(t, `{"_leapmux":{"completion":"interrupted"}}`, string(raw))
}
