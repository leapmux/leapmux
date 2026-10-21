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
	fail := true
	var persisted [][]byte
	persist := func(raw []byte) error {
		if fail {
			return wantErr
		}
		persisted = append(persisted, raw)
		return nil
	}
	assert.ErrorIs(t, buffer.PersistAll(agent.MessageCompletionInterrupted, persist), wantErr)
	assert.True(t, buffer.HasPendingWrites())
	fail = false
	require.NoError(t, buffer.PersistAll(agent.MessageCompletionComplete, persist))
	require.Len(t, persisted, 1)
	assert.Equal(t, "interrupted", StringField(persisted[0], "completion"))
	assert.False(t, buffer.HasPendingWrites())
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

func TestGenerationBufferScopeIDsListsTheRetainedScopesInFirstSeenOrder(t *testing.T) {
	t.Parallel()

	var buffer GenerationBuffer
	assert.Empty(t, buffer.ScopeIDs(), "an empty buffer holds no scope")

	buffer.Append("z-scope", agent.AssembledMessageKindText, "first", JoinVerbatim)
	buffer.Append("a-scope", agent.AssembledMessageKindReasoning, "second", JoinVerbatim)
	buffer.Append("m-scope", agent.AssembledMessageKindText, "third", JoinVerbatim)
	// A later delta of the first scope keeps that scope's first place.
	buffer.Append("z-scope", agent.AssembledMessageKindText, " again", JoinVerbatim)
	buffer.Discard("a-scope")

	assert.Equal(t, []string{"z-scope", "m-scope"}, buffer.ScopeIDs())
	assert.Equal(t, 11, buffer.RetainedTextLenForTest("z-scope"), "the read changes no segment")
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

func TestGenerationBufferPersistenceReleasesTheBufferLock(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"scope", "kind", "all"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			var buffer GenerationBuffer
			buffer.Append("scope", agent.AssembledMessageKindText, "answer", JoinVerbatim)
			persist := func([]byte) error {
				if !buffer.mu.TryLock() {
					t.Error("the persistence callback holds the buffer lock")
					return nil
				}
				buffer.mu.Unlock()
				assert.Empty(t, buffer.ScopeIDs())
				assert.True(t, buffer.HasPendingWrites())
				buffer.Append("scope", agent.AssembledMessageKindText, " later", JoinVerbatim)
				return nil
			}
			switch operation {
			case "scope":
				_, err := buffer.PersistScope("scope", agent.MessageCompletionComplete, persist)
				require.NoError(t, err)
			case "kind":
				require.NoError(t, buffer.PersistKind(agent.AssembledMessageKindText, agent.MessageCompletionComplete, persist))
			case "all":
				require.NoError(t, buffer.PersistAll(agent.MessageCompletionComplete, persist))
			}
			_, text, present := buffer.ScopeSnapshot("scope")
			assert.True(t, present)
			assert.Equal(t, " later", text)
		})
	}
}

func TestGenerationBufferDiscardMatchingReturnsScopesInFirstSeenOrder(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	for _, scope := range []string{"z", "keep", "a"} {
		buffer.Append(scope, agent.AssembledMessageKindText, scope, JoinVerbatim)
	}
	assert.Equal(t, []string{"z", "a"}, buffer.DiscardMatching(func(scope string) bool { return scope != "keep" }))
	assert.Equal(t, []string{"keep"}, buffer.ScopeIDs())
	assert.Empty(t, buffer.DiscardMatching(func(string) bool { return false }))
}

func TestGenerationBufferFailedWriteKeepsConcurrentAppends(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("scope", agent.AssembledMessageKindText, "first", JoinVerbatim)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	wantErr := errors.New("write failed")
	var texts []string
	fail := true
	go func() {
		_, err := buffer.PersistScope("scope", agent.MessageCompletionInterrupted, func(raw []byte) error {
			assert.Equal(t, "first", StringField(raw, "text"))
			assert.Equal(t, "interrupted", StringField(raw, "completion"))
			if fail {
				close(entered)
				<-release
				return wantErr
			}
			texts = append(texts, StringField(raw, "text"))
			return nil
		})
		done <- err
	}()
	<-entered
	buffer.Append("scope", agent.AssembledMessageKindText, " second", JoinVerbatim)
	buffer.Append("later", agent.AssembledMessageKindText, "third", JoinVerbatim)
	close(release)
	require.ErrorIs(t, <-done, wantErr)
	fail = false
	require.NoError(t, buffer.PersistAll(agent.MessageCompletionComplete, func(raw []byte) error {
		texts = append(texts, StringField(raw, "text"))
		return nil
	}))
	assert.Equal(t, []string{"first", " second", "third"}, texts)
}

func TestGenerationBufferConcurrentPersistenceWritesEachPrefixOnce(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("scope", agent.AssembledMessageKindText, "first", JoinVerbatim)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 2)
	var texts []string
	go func() {
		done <- buffer.PersistAll(agent.MessageCompletionComplete, func(raw []byte) error {
			texts = append(texts, StringField(raw, "text"))
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	buffer.Append("scope", agent.AssembledMessageKindText, " second", JoinVerbatim)
	go func() {
		done <- buffer.PersistAll(agent.MessageCompletionComplete, func(raw []byte) error {
			texts = append(texts, StringField(raw, "text"))
			return nil
		})
	}()
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-done)
	assert.Equal(t, []string{"first", " second"}, texts)
	assert.Empty(t, buffer.ScopeIDs())
}

func TestGenerationBufferPersistenceKeepsAReplacementScope(t *testing.T) {
	t.Parallel()
	for _, reset := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			var buffer GenerationBuffer
			buffer.Append("scope", agent.AssembledMessageKindText, "old", JoinVerbatim)
			_, err := buffer.PersistScope("scope", agent.MessageCompletionComplete, func([]byte) error {
				if reset {
					buffer.Reset()
				} else {
					buffer.Discard("scope")
				}
				buffer.Append("scope", agent.AssembledMessageKindText, "replacement", JoinVerbatim)
				if fail {
					return errors.New("write failed")
				}
				return nil
			})
			assert.Equal(t, fail, err != nil)
			_, text, present := buffer.ScopeSnapshot("scope")
			assert.True(t, present)
			assert.Equal(t, "replacement", text)
		}
	}
}
