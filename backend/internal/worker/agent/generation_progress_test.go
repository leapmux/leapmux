package agent_test

import (
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
