package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerationBufferFinishesEachScopeOnce(t *testing.T) {
	t.Parallel()

	var buffer GenerationBuffer
	buffer.Append("message", AssembledMessageKindReasoning, "first ")
	buffer.Append("message", AssembledMessageKindReasoning, "second")
	raw, ok, err := buffer.Finish("message", MessageCompletionComplete)
	require.NoError(t, err)
	require.True(t, ok)
	var value map[string]string
	require.NoError(t, json.Unmarshal(raw, &value))
	assert.Equal(t, "first second", value["text"])
	_, ok, err = buffer.Finish("message", MessageCompletionInterrupted)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestGenerationBufferFinishesScopesInFirstSeenOrder(t *testing.T) {
	t.Parallel()

	var buffer GenerationBuffer
	buffer.Append("z-scope", AssembledMessageKindText, "first")
	buffer.Append("a-scope", AssembledMessageKindText, "second")
	rows, err := buffer.FinishAll(MessageCompletionInterrupted)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	var first, second map[string]string
	require.NoError(t, json.Unmarshal(rows[0], &first))
	require.NoError(t, json.Unmarshal(rows[1], &second))
	assert.Equal(t, "first", first["text"])
	assert.Equal(t, "second", second["text"])
}

func TestGenerationBufferDiscardsOnlyTheSelectedScope(t *testing.T) {
	t.Parallel()

	var buffer GenerationBuffer
	buffer.Append("completed", AssembledMessageKindText, "provider owns this")
	buffer.Append("retained", AssembledMessageKindReasoning, "worker owns this")
	buffer.Discard("completed")

	rows, err := buffer.FinishAll(MessageCompletionInterrupted)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0]), "worker owns this")
}
