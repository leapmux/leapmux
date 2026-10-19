package providerkit

import (
	"errors"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerationPendingWritesKeepTheirBytesCompletionAndDestination(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("same", agent.AssembledMessageKindText, "interrupted", JoinVerbatim)
	var first, second []string
	fail := true
	_, err := buffer.PersistScope("same", agent.MessageCompletionInterrupted, func(raw []byte) error {
		if fail {
			return errors.New("store unavailable")
		}
		first = append(first, StringField(raw, "text")+":"+StringField(raw, "completion"))
		return nil
	})
	require.ErrorContains(t, err, "store unavailable")
	assert.Empty(t, buffer.ScopeIDs(), "a pending write is no longer an open stream scope")
	buffer.Append("same", agent.AssembledMessageKindText, "replacement", JoinVerbatim)
	buffer.DiscardMatching(func(string) bool { return true })
	buffer.Append("new", agent.AssembledMessageKindText, "answer", JoinVerbatim)
	fail = false
	require.NoError(t, buffer.PersistAll(agent.MessageCompletionComplete, func(raw []byte) error {
		assert.Equal(t, []string{"interrupted:interrupted"}, first, "the older write must finish first")
		second = append(second, StringField(raw, "text"))
		return nil
	}))
	assert.Equal(t, []string{"interrupted:interrupted"}, first)
	assert.Equal(t, []string{"answer"}, second)
}

func TestGenerationPendingBatchFreezesEverySelectedScopeBeforeTheFirstWrite(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("a", agent.AssembledMessageKindText, "first", JoinVerbatim)
	buffer.Append("b", agent.AssembledMessageKindReasoning, "second", JoinVerbatim)
	var stored []string
	fail := true
	err := buffer.PersistAll(agent.MessageCompletionInterrupted, func(raw []byte) error {
		if fail {
			buffer.Append("b", agent.AssembledMessageKindText, "later", JoinVerbatim)
			return errors.New("store unavailable")
		}
		stored = append(stored, StringField(raw, "kind")+":"+StringField(raw, "text")+":"+StringField(raw, "completion"))
		return nil
	})
	require.Error(t, err)
	buffer.DiscardKind(agent.AssembledMessageKindText)
	fail = false
	require.NoError(t, buffer.PersistAll(agent.MessageCompletionComplete, func([]byte) error {
		t.Error("a later callback must not receive the pending batch")
		return nil
	}))
	assert.Equal(t, []string{"text:first:interrupted", "reasoning:second:interrupted"}, stored)
}

func TestGenerationPendingRetryGetsAnIndependentByteSlice(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("scope", agent.AssembledMessageKindText, "original", JoinVerbatim)
	attempts := 0
	_, err := buffer.PersistScope("scope", agent.MessageCompletionError, func(raw []byte) error {
		attempts++
		assert.Equal(t, "original", StringField(raw, "text"))
		if attempts == 1 {
			clear(raw)
			return errors.New("store unavailable")
		}
		return nil
	})
	require.Error(t, err)
	require.NoError(t, buffer.PersistAll(agent.MessageCompletionComplete, func([]byte) error {
		t.Error("the pending write must use its original destination")
		return nil
	}))
	assert.Equal(t, 2, attempts)
}

func TestGenerationResetExplicitlyDropsPendingWrites(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("scope", agent.AssembledMessageKindText, "discarded session", JoinVerbatim)
	attempts := 0
	require.Error(t, buffer.PersistAll(agent.MessageCompletionComplete, func([]byte) error {
		attempts++
		return errors.New("store unavailable")
	}))
	buffer.Reset()
	require.NoError(t, buffer.PersistAll(agent.MessageCompletionComplete, func([]byte) error { return nil }))
	assert.Equal(t, 1, attempts)
}

func TestGenerationFinishWaitsForPendingWrites(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"scope", "kind", "all"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			var buffer GenerationBuffer
			buffer.Append("old", agent.AssembledMessageKindText, "old", JoinVerbatim)
			fail := true
			writes := 0
			require.Error(t, buffer.PersistAll(agent.MessageCompletionInterrupted, func(raw []byte) error {
				if fail {
					return errors.New("store unavailable")
				}
				assert.Equal(t, "interrupted", StringField(raw, "completion"))
				writes++
				return nil
			}))
			buffer.Append("new", agent.AssembledMessageKindText, "new", JoinVerbatim)
			finish := func() ([][]byte, error) {
				switch operation {
				case "scope":
					raw, _, err := buffer.Finish("new", agent.MessageCompletionComplete)
					if err != nil {
						return nil, err
					}
					return [][]byte{raw}, nil
				case "kind":
					return buffer.FinishKind(agent.AssembledMessageKindText, agent.MessageCompletionComplete)
				default:
					return buffer.FinishAll(agent.MessageCompletionComplete)
				}
			}
			_, err := finish()
			require.ErrorContains(t, err, "store unavailable")
			assert.Equal(t, []string{"new"}, buffer.ScopeIDs())
			fail = false
			rows, err := finish()
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, "new", StringField(rows[0], "text"))
			assert.Equal(t, 1, writes)
			assert.False(t, buffer.HasPendingWrites())
		})
	}
}

func TestGenerationPendingWritesTransferWithoutChangingTheirDestinations(t *testing.T) {
	t.Parallel()
	var source, destination GenerationBuffer
	var order []string
	fail := true
	for _, entry := range []struct {
		buffer *GenerationBuffer
		label  string
	}{{&source, "source"}, {&destination, "destination"}} {
		entry.buffer.Append("same", agent.AssembledMessageKindText, entry.label, JoinVerbatim)
		require.Error(t, entry.buffer.PersistAll(agent.MessageCompletionInterrupted, func(raw []byte) error {
			if fail {
				return errors.New("store unavailable")
			}
			assert.Equal(t, entry.label, StringField(raw, "text"))
			order = append(order, entry.label)
			return nil
		}))
	}
	source.Append("source-open", agent.AssembledMessageKindText, "source-open", JoinVerbatim)
	destination.Append("destination-open", agent.AssembledMessageKindText, "destination-open", JoinVerbatim)
	source.MoveAllTo(&destination)
	assert.Empty(t, source.ScopeIDs())
	assert.False(t, source.HasPendingWrites())
	fail = false
	require.NoError(t, destination.PersistAll(agent.MessageCompletionComplete, func(raw []byte) error {
		order = append(order, StringField(raw, "text"))
		return nil
	}))
	assert.Equal(t, []string{"destination", "source", "destination-open", "source-open"}, order)
}

func TestGenerationPendingWriteHasOneConcurrentRetry(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("scope", agent.AssembledMessageKindText, "once", JoinVerbatim)
	attempts := 0
	require.Error(t, buffer.PersistAll(agent.MessageCompletionComplete, func(raw []byte) error {
		attempts++
		if attempts == 1 {
			return errors.New("store unavailable")
		}
		assert.Equal(t, "once", StringField(raw, "text"))
		return nil
	}))
	var writers sync.WaitGroup
	for range 8 {
		writers.Go(func() {
			assert.NoError(t, buffer.PersistAll(agent.MessageCompletionComplete, nil))
		})
	}
	writers.Wait()
	assert.Equal(t, 2, attempts)
	assert.False(t, buffer.HasPendingWrites())
}

func TestGenerationPersistenceRefusesANilDestinationWithoutLosingTheScope(t *testing.T) {
	t.Parallel()
	var buffer GenerationBuffer
	buffer.Append("scope", agent.AssembledMessageKindText, "retain", JoinVerbatim)
	require.ErrorContains(t, buffer.PersistAll(agent.MessageCompletionComplete, nil), "no persistence function")
	_, text, present := buffer.ScopeSnapshot("scope")
	assert.True(t, present)
	assert.Equal(t, "retain", text)
	assert.False(t, buffer.HasPendingWrites())
}
