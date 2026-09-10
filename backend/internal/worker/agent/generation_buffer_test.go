package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerationBufferKeepsRowsUntilPersistenceSucceeds(t *testing.T) {
	t.Parallel()

	var buffer GenerationBuffer
	buffer.Append("answer", AssembledMessageKindText, "kept", joinVerbatim)
	wantErr := errors.New("database unavailable")
	err := buffer.PersistAll(MessageCompletionInterrupted, func([]byte) error { return wantErr })
	assert.ErrorIs(t, err, wantErr)

	var persisted [][]byte
	require.NoError(t, buffer.PersistAll(MessageCompletionInterrupted, func(raw []byte) error {
		persisted = append(persisted, raw)
		return nil
	}))
	require.Len(t, persisted, 1)
}

func TestGenerationBufferFinishesEachScopeOnce(t *testing.T) {
	t.Parallel()

	var buffer GenerationBuffer
	buffer.Append("message", AssembledMessageKindReasoning, "first ", joinVerbatim)
	buffer.Append("message", AssembledMessageKindReasoning, "second", joinVerbatim)
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
	buffer.Append("z-scope", AssembledMessageKindText, "first", joinVerbatim)
	buffer.Append("a-scope", AssembledMessageKindText, "second", joinVerbatim)
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
	buffer.Append("completed", AssembledMessageKindText, "provider owns this", joinVerbatim)
	buffer.Append("retained", AssembledMessageKindReasoning, "worker owns this", joinVerbatim)
	buffer.Discard("completed")

	rows, err := buffer.FinishAll(MessageCompletionInterrupted)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0]), "worker owns this")
}

func TestGenerationBufferKeepsReasoningDeltasVerbatim(t *testing.T) {
	t.Parallel()

	var buffer GenerationBuffer
	buffer.Append("reasoning", AssembledMessageKindReasoning, "**Verifying terminal release synchronization", joinVerbatim)
	buffer.Append("reasoning", AssembledMessageKindReasoning, "Analyzing lock acquisition order and concurrency**", joinVerbatim)

	raw, ok, err := buffer.Finish("reasoning", MessageCompletionInterrupted)
	require.NoError(t, err)
	require.True(t, ok)
	assert.JSONEq(t, `{
		"type":"assembled_message",
		"kind":"reasoning",
		"text":"**Verifying terminal release synchronizationAnalyzing lock acquisition order and concurrency**",
		"completion":"interrupted"
	}`, string(raw))

	buffer.Append("existing-breaks", AssembledMessageKindReasoning, "first\n\n\n\n", joinParagraph)
	buffer.Append("existing-breaks", AssembledMessageKindReasoning, "second", joinParagraph)
	raw, ok, err = buffer.Finish("existing-breaks", MessageCompletionComplete)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Contains(t, string(raw), `"text":"first\n\n\n\nsecond"`)
}
