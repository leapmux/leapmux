package agent

import (
	"math"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProgressCounterTracksIndependentModelAndOutputScopes(t *testing.T) {
	t.Parallel()

	var counter ProgressCounter

	snapshot, changed := counter.Apply(ModelTextProgress("model-1", "abcdefgh"))
	require.True(t, changed)
	assert.Equal(t, int64(2), snapshot.ThinkingTokens)

	snapshot, changed = counter.Apply(OutputDeltaProgress("tool-1", 12))
	require.True(t, changed)
	assert.Equal(t, int64(2), snapshot.ThinkingTokens)
	assert.Equal(t, int64(12), snapshot.OutputBytes)

	snapshot, changed = counter.Apply(CompleteModelProgress("model-1"))
	require.True(t, changed)
	assert.Zero(t, snapshot.ThinkingTokens)
	assert.Equal(t, int64(12), snapshot.OutputBytes)

	snapshot, changed = counter.Apply(CompleteOutputProgress("tool-1"))
	require.True(t, changed)
	assert.Equal(t, ProgressSnapshot{}, snapshot)
}

func TestProgressCounterModelResetKeepsOutputScopes(t *testing.T) {
	t.Parallel()

	var counter ProgressCounter
	counter.Apply(ModelTextProgress("model", "abcdefgh"))
	counter.Apply(OutputDeltaProgress("background", 512))

	snapshot, changed := counter.Apply(ResetModelProgress())
	require.True(t, changed)
	assert.Zero(t, snapshot.ThinkingTokens)
	assert.Equal(t, int64(512), snapshot.OutputBytes)
}

func TestProgressResetSinkKeepsOutputWhenAMessagePersists(t *testing.T) {
	t.Parallel()

	inner := &testSink{}
	sink := newModelProgressResetSink(inner)
	sink.ReportProgress(ModelTextProgress("model", "abcdefgh"))
	sink.ReportProgress(OutputDeltaProgress("background", 512))
	require.NoError(t, sink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		MessageContent{Original: []byte(`{"type":"assistant"}`)},
		SpanInfo{},
	))

	snapshot := inner.progressCount.Snapshot()
	assert.Zero(t, snapshot.ThinkingTokens)
	assert.Equal(t, int64(512), snapshot.OutputBytes)
}

func TestProgressResetSinkDecoratesChildTranscriptFacets(t *testing.T) {
	t.Parallel()

	inner := &testSink{}
	sink := newModelProgressResetSink(inner)
	child := sink.ChildSink("child")
	child.ReportProgress(ModelTextProgress("model", "abcdefgh"))
	require.NoError(t, child.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		MessageContent{Original: []byte(`{"type":"assistant"}`)},
		SpanInfo{},
	))

	childSink, ok := inner.ChildSink("child").(*testSink)
	require.True(t, ok)
	assert.Zero(t, childSink.progressCount.Snapshot().ThinkingTokens)
}

func TestProgressCounterRetainsCompletedContributionUntilOverlappingScopeEnds(t *testing.T) {
	t.Parallel()

	var counter ProgressCounter
	counter.Apply(ModelTextProgress("first", "abcdefgh"))
	counter.Apply(ModelTextProgress("second", "ijklmnop"))

	snapshot, changed := counter.Apply(CompleteModelProgress("first"))
	require.False(t, changed)
	assert.Equal(t, int64(4), snapshot.ThinkingTokens)

	snapshot, changed = counter.Apply(CompleteModelProgress("second"))
	require.True(t, changed)
	assert.Zero(t, snapshot.ThinkingTokens)
}

func TestProgressCounterUsesNativeTotalsAndProtectsOverflow(t *testing.T) {
	t.Parallel()

	var counter ProgressCounter
	counter.Apply(OutputTotalProgress("tool", 40, false))
	snapshot, changed := counter.Apply(OutputTotalProgress("tool", 32, false))
	assert.False(t, changed)
	assert.Equal(t, int64(40), snapshot.OutputBytes)

	snapshot, changed = counter.Apply(OutputDeltaProgress("tool", math.MaxInt64))
	require.True(t, changed)
	assert.Equal(t, int64(math.MaxInt64), snapshot.OutputBytes)

	snapshot, changed = counter.Apply(OutputDeltaProgress("tool", 1))
	assert.False(t, changed)
	assert.Equal(t, int64(math.MaxInt64), snapshot.OutputBytes)
}

func TestProgressCounterCountsUnicodeCodePointsWithRemainder(t *testing.T) {
	t.Parallel()

	var counter ProgressCounter
	_, changed := counter.Apply(ModelTextProgress("model", "한🙂a"))
	assert.False(t, changed)
	snapshot, changed := counter.Apply(ModelTextProgress("model", "b"))
	require.True(t, changed)
	assert.Equal(t, int64(1), snapshot.ThinkingTokens)
}

func TestProgressCounterMarksMinimumOutput(t *testing.T) {
	t.Parallel()

	var counter ProgressCounter
	snapshot, changed := counter.Apply(OutputTotalProgress("tool", 1024, true))
	require.True(t, changed)
	assert.Equal(t, int64(1024), snapshot.OutputBytes)
	assert.True(t, snapshot.OutputBytesMinimum)

	snapshot, changed = counter.Apply(OutputExactTotalProgress("tool", 2048))
	require.True(t, changed)
	assert.Equal(t, int64(2048), snapshot.OutputBytes)
	assert.False(t, snapshot.OutputBytesMinimum)
}

func TestProgressCounterAddsNativeTotalsWhenAScopeIDIsReused(t *testing.T) {
	t.Parallel()

	var counter ProgressCounter
	counter.Apply(NativeTokenProgress("reused", 8))
	counter.Apply(NativeTokenProgress("other", 4))
	counter.Apply(CompleteModelProgress("reused"))

	snapshot, changed := counter.Apply(NativeTokenProgress("reused", 2))
	require.True(t, changed)
	assert.Equal(t, int64(14), snapshot.ThinkingTokens)

	counter.Apply(OutputTotalProgress("reused-output", 100, false))
	counter.Apply(OutputTotalProgress("other-output", 50, false))
	counter.Apply(CompleteOutputProgress("reused-output"))
	snapshot, changed = counter.Apply(OutputTotalProgress("reused-output", 20, false))
	require.True(t, changed)
	assert.Equal(t, int64(170), snapshot.OutputBytes)
}
