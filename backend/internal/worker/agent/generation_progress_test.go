package agent_test

import (
	"fmt"
	"math"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProgressCounterTracksIndependentModelAndOutputScopes(t *testing.T) {
	t.Parallel()

	var counter agent.ProgressCounter

	snapshot, changed := counter.Apply(agent.ModelTextProgress("model-1", "abcdefgh"))
	require.True(t, changed)
	assert.Equal(t, int64(2), snapshot.ThinkingTokens)

	snapshot, changed = counter.Apply(agent.OutputDeltaProgress("tool-1", 12))
	require.True(t, changed)
	assert.Equal(t, int64(2), snapshot.ThinkingTokens)
	assert.Equal(t, int64(12), snapshot.OutputBytes)

	snapshot, changed = counter.Apply(agent.CompleteModelProgress("model-1"))
	require.True(t, changed)
	assert.Zero(t, snapshot.ThinkingTokens)
	assert.Equal(t, int64(12), snapshot.OutputBytes)

	snapshot, changed = counter.Apply(agent.CompleteOutputProgress("tool-1"))
	require.True(t, changed)
	assert.Equal(t, agent.ProgressSnapshot{}, snapshot)
}

func TestProgressCounterModelResetKeepsOutputScopes(t *testing.T) {
	t.Parallel()

	var counter agent.ProgressCounter
	counter.Apply(agent.ModelTextProgress("model", "abcdefgh"))
	counter.Apply(agent.OutputDeltaProgress("background", 512))

	snapshot, changed := counter.Apply(agent.ResetModelProgress())
	require.True(t, changed)
	assert.Zero(t, snapshot.ThinkingTokens)
	assert.Equal(t, int64(512), snapshot.OutputBytes)
}

func TestModelScopeResetPreservesOtherModelAndOutputScopes(t *testing.T) {
	t.Parallel()
	var counter agent.ProgressCounter
	counter.Apply(agent.ModelTextProgress("retained", "12345678"))
	counter.Apply(agent.ModelTextProgress("other", "123456789012"))
	counter.Apply(agent.CompleteModelProgress("retained"))
	counter.Apply(agent.ModelTextProgress("transfer", "1234567890123456"))
	counter.Apply(agent.OutputTotalProgress("output", 91, true))
	update := agent.ResetModelProgress()
	update.ScopeID = "transfer"
	snapshot, changed := counter.Apply(update)
	require.True(t, changed)
	assert.EqualValues(t, 5, snapshot.ThinkingTokens)
	assert.EqualValues(t, 91, snapshot.OutputBytes)
	assert.True(t, snapshot.OutputBytesMinimum)
	update.ScopeID = "retained"
	snapshot, _ = counter.Apply(update)
	assert.EqualValues(t, 3, snapshot.ThinkingTokens)
	assert.EqualValues(t, 91, snapshot.OutputBytes)
	for _, id := range []string{"unknown", " ", "\x00"} {
		update.ScopeID = id
		unchanged, moved := counter.Apply(update)
		assert.False(t, moved)
		assert.Equal(t, snapshot, unchanged)
	}
}

func TestModelScopeResetEmptyTargetDoesNotResetEveryModel(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", " ", "\x00"} {
		var counter agent.ProgressCounter
		before, _ := counter.Apply(agent.ModelTextProgress("unrelated", "12345678"))
		after, changed := counter.Apply(agent.ResetModelScopeProgress(id))
		assert.False(t, changed, "an invalid scoped target cannot become a global reset")
		assert.Equal(t, before, after)
	}
}

func TestModelResetPreservesActiveAndRetainedSelections(t *testing.T) {
	t.Parallel()
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprint(retained), func(t *testing.T) {
			t.Parallel()
			var counter agent.ProgressCounter
			counter.Apply(agent.ModelTextProgress("root", "12345678"))
			counter.Apply(agent.ModelTextProgress("child", "123456789012"))
			counter.Apply(agent.OutputTotalProgress("output", 41, true))
			if retained {
				counter.Apply(agent.CompleteModelProgress("child"))
			}
			value, changed := counter.Apply(agent.ResetModelProgressPreservingScopes([]string{"", "unknown", "child", "child"}))
			assert.True(t, changed)
			assert.EqualValues(t, 3, value.ThinkingTokens)
			assert.EqualValues(t, 41, value.OutputBytes)
			assert.True(t, value.OutputBytesMinimum)
		})
	}
}

func TestModelResetSelectionOwnsTheCallerSlice(t *testing.T) {
	t.Parallel()
	ids := []string{"child"}
	update := agent.ResetModelProgressPreservingScopes(ids)
	ids[0] = "root"
	assert.Equal(t, []string{"child"}, update.PreserveModelScopes)
	var counter agent.ProgressCounter
	counter.Apply(agent.ModelTextProgress("root", "12345678"))
	counter.Apply(agent.ModelTextProgress("child", "123456789012"))
	value, _ := counter.Apply(update)
	assert.EqualValues(t, 3, value.ThinkingTokens)
}

func TestModelResetSelectionKeepsExistingResetBoundaries(t *testing.T) {
	t.Parallel()
	for _, ids := range [][]string{nil, {}, {""}, {"unknown"}, {"", "unknown", "unknown"}} {
		var counter agent.ProgressCounter
		counter.Apply(agent.ModelTextProgress("model", "12345678"))
		counter.Apply(agent.OutputDeltaProgress("output", 51))
		value, _ := counter.Apply(agent.ResetModelProgressPreservingScopes(ids))
		assert.Zero(t, value.ThinkingTokens)
		assert.EqualValues(t, 51, value.OutputBytes)
	}
	var counter agent.ProgressCounter
	counter.Apply(agent.ModelTextProgress("root", "12345678"))
	counter.Apply(agent.ModelTextProgress("child", "123456789012"))
	update := agent.ResetModelProgressPreservingScopes([]string{"child"})
	update.ScopeID = "child"
	value, _ := counter.Apply(update)
	assert.EqualValues(t, 2, value.ThinkingTokens, "the exact scope selector takes priority")
}

func TestProgressResetSinkKeepsOutputWhenAMessagePersists(t *testing.T) {
	t.Parallel()

	inner := &agenttest.Sink{}
	sink := agent.NewModelProgressResetSink(agent.NewProviderServices(inner))
	sink.ReportProgress(agent.ModelTextProgress("model", "abcdefgh"))
	sink.ReportProgress(agent.OutputDeltaProgress("background", 512))
	require.NoError(t, sink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"assistant"}`)},
		agent.SpanInfo{},
	))

	snapshot := inner.ProgressSnapshot()
	assert.Zero(t, snapshot.ThinkingTokens)
	assert.Equal(t, int64(512), snapshot.OutputBytes)
}

func TestProgressResetSinkDecoratesChildTranscriptFacets(t *testing.T) {
	t.Parallel()

	inner := &agenttest.Sink{}
	sink := agent.NewModelProgressResetSink(agent.NewProviderServices(inner))
	child := sink.ChildSink("child")
	child.ReportProgress(agent.ModelTextProgress("model", "abcdefgh"))
	require.NoError(t, child.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"assistant"}`)},
		agent.SpanInfo{},
	))

	childSink := inner.Child("child")
	assert.Zero(t, childSink.ProgressSnapshot().ThinkingTokens)
}

func TestProgressCounterRetainsCompletedContributionUntilOverlappingScopeEnds(t *testing.T) {
	t.Parallel()

	var counter agent.ProgressCounter
	counter.Apply(agent.ModelTextProgress("first", "abcdefgh"))
	counter.Apply(agent.ModelTextProgress("second", "ijklmnop"))

	snapshot, changed := counter.Apply(agent.CompleteModelProgress("first"))
	require.False(t, changed)
	assert.Equal(t, int64(4), snapshot.ThinkingTokens)

	snapshot, changed = counter.Apply(agent.CompleteModelProgress("second"))
	require.True(t, changed)
	assert.Zero(t, snapshot.ThinkingTokens)
}

func TestProgressCounterUsesNativeTotalsAndProtectsOverflow(t *testing.T) {
	t.Parallel()

	var counter agent.ProgressCounter
	counter.Apply(agent.OutputTotalProgress("tool", 40, false))
	snapshot, changed := counter.Apply(agent.OutputTotalProgress("tool", 32, false))
	assert.False(t, changed)
	assert.Equal(t, int64(40), snapshot.OutputBytes)

	snapshot, changed = counter.Apply(agent.OutputDeltaProgress("tool", math.MaxInt64))
	require.True(t, changed)
	assert.Equal(t, int64(math.MaxInt64), snapshot.OutputBytes)

	snapshot, changed = counter.Apply(agent.OutputDeltaProgress("tool", 1))
	assert.False(t, changed)
	assert.Equal(t, int64(math.MaxInt64), snapshot.OutputBytes)
}

func TestProgressCounterCountsUnicodeCodePointsWithRemainder(t *testing.T) {
	t.Parallel()

	var counter agent.ProgressCounter
	_, changed := counter.Apply(agent.ModelTextProgress("model", "한🙂a"))
	assert.False(t, changed)
	snapshot, changed := counter.Apply(agent.ModelTextProgress("model", "b"))
	require.True(t, changed)
	assert.Equal(t, int64(1), snapshot.ThinkingTokens)
}

func TestProgressCounterMarksMinimumOutput(t *testing.T) {
	t.Parallel()

	var counter agent.ProgressCounter
	snapshot, changed := counter.Apply(agent.OutputTotalProgress("tool", 1024, true))
	require.True(t, changed)
	assert.Equal(t, int64(1024), snapshot.OutputBytes)
	assert.True(t, snapshot.OutputBytesMinimum)

	snapshot, changed = counter.Apply(agent.OutputExactTotalProgress("tool", 2048))
	require.True(t, changed)
	assert.Equal(t, int64(2048), snapshot.OutputBytes)
	assert.False(t, snapshot.OutputBytesMinimum)
}

func TestProgressCounterAddsNativeTotalsWhenAScopeIDIsReused(t *testing.T) {
	t.Parallel()

	var counter agent.ProgressCounter
	counter.Apply(agent.NativeTokenProgress("reused", 8))
	counter.Apply(agent.NativeTokenProgress("other", 4))
	counter.Apply(agent.CompleteModelProgress("reused"))

	snapshot, changed := counter.Apply(agent.NativeTokenProgress("reused", 2))
	require.True(t, changed)
	assert.Equal(t, int64(14), snapshot.ThinkingTokens)

	counter.Apply(agent.OutputTotalProgress("reused-output", 100, false))
	counter.Apply(agent.OutputTotalProgress("other-output", 50, false))
	counter.Apply(agent.CompleteOutputProgress("reused-output"))
	snapshot, changed = counter.Apply(agent.OutputTotalProgress("reused-output", 20, false))
	require.True(t, changed)
	assert.Equal(t, int64(170), snapshot.OutputBytes)
}

func TestLateModelOperationsKeepAZeroByteOutputMinimum(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"single reset", "global reset", "model completion"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			var counter agent.ProgressCounter
			counter.Apply(agent.OutputTotalProgress("retained", 0, true))
			counter.Apply(agent.OutputTotalProgress("live", 1, false))
			counter.Apply(agent.CompleteOutputProgress("retained"))
			before := counter.Snapshot()
			require.EqualValues(t, 1, before.OutputBytes)
			require.True(t, before.OutputBytesMinimum)
			var update agent.ProgressUpdate
			switch operation {
			case "single reset":
				update = agent.ResetModelScopeProgress("retained")
			case "global reset":
				update = agent.ResetModelProgress()
			case "model completion":
				counter.Apply(agent.ModelTextProgress("model", "text"))
				update = agent.CompleteModelProgress("model")
			}
			after, _ := counter.Apply(update)
			assert.EqualValues(t, 1, after.OutputBytes)
			assert.True(t, after.OutputBytesMinimum)
		})
	}
}
