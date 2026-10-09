package providerkit

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerationBufferKeepsRowsUntilPersistenceSucceeds(t *testing.T) {
	t.Parallel()

	var buffer GenerationBuffer
	buffer.Append("answer", agent.AssembledMessageKindText, "kept", JoinVerbatim)
	wantErr := errors.New("database unavailable")
	err := buffer.PersistAll(agent.MessageCompletionInterrupted, func([]byte) error { return wantErr })
	assert.ErrorIs(t, err, wantErr)

	var persisted [][]byte
	require.NoError(t, buffer.PersistAll(agent.MessageCompletionInterrupted, func(raw []byte) error {
		persisted = append(persisted, raw)
		return nil
	}))
	require.Len(t, persisted, 1)
}

func TestGenerationBufferFinishesEachScopeOnce(t *testing.T) {
	t.Parallel()

	var buffer GenerationBuffer
	buffer.Append("message", agent.AssembledMessageKindReasoning, "first ", JoinVerbatim)
	buffer.Append("message", agent.AssembledMessageKindReasoning, "second", JoinVerbatim)
	raw, ok, err := buffer.Finish("message", agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.True(t, ok)
	var value map[string]string
	require.NoError(t, json.Unmarshal(raw, &value))
	assert.Equal(t, "first second", value["text"])
	_, ok, err = buffer.Finish("message", agent.MessageCompletionInterrupted)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestGenerationBufferFinishesScopesInFirstSeenOrder(t *testing.T) {
	t.Parallel()

	var buffer GenerationBuffer
	buffer.Append("z-scope", agent.AssembledMessageKindText, "first", JoinVerbatim)
	buffer.Append("a-scope", agent.AssembledMessageKindText, "second", JoinVerbatim)
	rows, err := buffer.FinishAll(agent.MessageCompletionInterrupted)
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
	buffer.Append("completed", agent.AssembledMessageKindText, "provider owns this", JoinVerbatim)
	buffer.Append("retained", agent.AssembledMessageKindReasoning, "worker owns this", JoinVerbatim)
	buffer.Discard("completed")

	rows, err := buffer.FinishAll(agent.MessageCompletionInterrupted)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Contains(t, string(rows[0]), "worker owns this")
}

func TestGenerationBufferKeepsReasoningDeltasVerbatim(t *testing.T) {
	t.Parallel()

	var buffer GenerationBuffer
	buffer.Append("reasoning", agent.AssembledMessageKindReasoning, "**Verifying terminal release synchronization", JoinVerbatim)
	buffer.Append("reasoning", agent.AssembledMessageKindReasoning, "Analyzing lock acquisition order and concurrency**", JoinVerbatim)

	raw, ok, err := buffer.Finish("reasoning", agent.MessageCompletionInterrupted)
	require.NoError(t, err)
	require.True(t, ok)
	assert.JSONEq(t, `{
		"type":"assembled_message",
		"kind":"reasoning",
		"text":"**Verifying terminal release synchronizationAnalyzing lock acquisition order and concurrency**",
		"completion":"interrupted"
	}`, string(raw))

	buffer.Append("existing-breaks", agent.AssembledMessageKindReasoning, "first\n\n\n\n", JoinParagraph)
	buffer.Append("existing-breaks", agent.AssembledMessageKindReasoning, "second", JoinParagraph)
	raw, ok, err = buffer.Finish("existing-breaks", agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Contains(t, string(raw), `"text":"first\n\n\n\nsecond"`)
}

func TestGenerationBufferScopeSnapshotKeepsIndependentScopes(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	for _, target := range []string{"", "missing"} {
		kind, text, present := buffer.ScopeSnapshot(target)
		assert.False(t, present)
		assert.Empty(t, kind)
		assert.Empty(t, text)
	}
	buffer.Append("first", agent.AssembledMessageKindText, "first", JoinVerbatim)
	buffer.Append("second", agent.AssembledMessageKindReasoning, "second", JoinVerbatim)
	kind, snapshot, present := buffer.ScopeSnapshot("first")
	require.True(t, present)
	assert.Equal(t, agent.AssembledMessageKindText, kind)
	assert.Equal(t, "first", snapshot)
	buffer.Append("first", agent.AssembledMessageKindText, " appended", JoinVerbatim)
	_, current, present := buffer.ScopeSnapshot("first")
	require.True(t, present)
	assert.Equal(t, "first appended", current)
	assert.Equal(t, "first", snapshot)
	_, second, present := buffer.ScopeSnapshot("second")
	require.True(t, present)
	assert.Equal(t, "second", second)
	_, present, err := buffer.Finish("first", agent.MessageCompletionComplete)
	require.NoError(t, err)
	require.True(t, present)
	_, _, present = buffer.ScopeSnapshot("first")
	assert.False(t, present)
	assert.Equal(t, "first appended", current)
	buffer.Discard("second")
	_, _, present = buffer.ScopeSnapshot("second")
	assert.False(t, present)
	assert.Equal(t, "second", second)
	buffer.Append("third", agent.AssembledMessageKindText, "third", JoinVerbatim)
	_, third, present := buffer.ScopeSnapshot("third")
	require.True(t, present)
	buffer.Reset()
	_, _, present = buffer.ScopeSnapshot("third")
	assert.False(t, present)
	assert.Equal(t, "third", third)
}

func TestGenerationBufferScopeSnapshotReadsAnEmptyStoredSegment(t *testing.T) {
	t.Parallel()
	buffer := GenerationBuffer{segments: map[string]*generationSegment{
		"empty": {kind: agent.AssembledMessageKindReasoning},
	}}
	kind, text, present := buffer.ScopeSnapshot("empty")
	assert.True(t, present)
	assert.Equal(t, agent.AssembledMessageKindReasoning, kind)
	assert.Empty(t, text)
	_, present, err := buffer.Finish("empty", agent.MessageCompletionComplete)
	require.NoError(t, err)
	assert.False(t, present)
}

func TestGenerationBufferScopeSnapshotDuringConcurrentAppends(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("scope", agent.AssembledMessageKindText, "prefix", JoinVerbatim)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		for range 1000 {
			buffer.Append("scope", agent.AssembledMessageKindText, ".", JoinVerbatim)
		}
	}()
	go func() {
		defer group.Done()
		for range 1000 {
			kind, text, present := buffer.ScopeSnapshot("scope")
			assert.True(t, present)
			assert.Equal(t, agent.AssembledMessageKindText, kind)
			assert.True(t, strings.HasPrefix(text, "prefix"))
		}
	}()
	group.Wait()
	_, text, present := buffer.ScopeSnapshot("scope")
	require.True(t, present)
	assert.Len(t, text, 1006)
}
