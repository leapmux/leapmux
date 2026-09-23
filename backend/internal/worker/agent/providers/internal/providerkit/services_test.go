package providerkit

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OpenToolSpan holds the order every provider's tool call needs: reserve before
// the persist, persist before the open, record the type either way. Each
// provider still decides `spawns` from its own wire shape.
func TestOpenToolSpan_OrdinaryToolReservesPersistsAndOpens(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	require.NoError(t, OpenToolSpan(sink, agent.MessageContent{Original: []byte(`{"type":"tool_call"}`)}, "tc-read", "read", false))

	assert.Equal(t, []string{"tc-read"}, sink.ReservedColorSpans())
	open := sink.OpenSpans()
	require.Len(t, open, 1)
	assert.Equal(t, "tc-read", open[0].SpanID)
	assert.Empty(t, open[0].ParentSpanID, "a provider's tool calls are flat")
	assert.Equal(t, "read", sink.GetSpanType("tc-read"))

	// The row persisted BEFORE its own span opened, so it draws no rail of its
	// own. Reversing the two would give every tool call a self-connector.
	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	assert.Empty(t, msgs[0].SpansOpenAtPersist)
	assert.Equal(t, "tc-read", msgs[0].SpanID)
}

// A spawn reserves nothing and opens nothing, and still records its type.
func TestOpenToolSpan_SpawnOpensNothingButRecordsItsType(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	require.NoError(t, OpenToolSpan(sink, agent.MessageContent{Original: []byte(`{"type":"tool_call"}`)}, "tc-spawn", "Agent", true))

	assert.Empty(t, sink.ReservedColorSpans(), "a spawn blocks no color")
	assert.Empty(t, sink.OpenSpans(), "and draws no rail")
	assert.Equal(t, "Agent", sink.GetSpanType("tc-spawn"),
		"the closing message still reads the type back")

	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, "tc-spawn", msgs[0].SpanID, "the row still carries the span id")
}

// A spawn takes no column, so a tool that starts next sits where the spawn
// would have been rather than one column right of it.
func TestOpenToolSpan_ASpawnLeavesTheNextToolAtTheSameDepth(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	require.NoError(t, OpenToolSpan(sink, agent.MessageContent{Original: []byte(`{}`)}, "tc-spawn", "Agent", true))
	require.NoError(t, OpenToolSpan(sink, agent.MessageContent{Original: []byte(`{}`)}, "tc-read", "read", false))

	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "the spawn contributes no rail to draw")
}

// A failed persist is reported to the caller, which logs it -- but the span
// still opens, so a later closing message finds a column to end.
func TestOpenToolSpan_ReturnsThePersistErrorAndStillOpensTheSpan(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{PersistErr: assert.AnError}
	err := OpenToolSpan(sink, agent.MessageContent{Original: []byte(`{}`)}, "tc-read", "read", false)

	require.ErrorIs(t, err, assert.AnError, "the caller logs this")
	open := sink.OpenSpans()
	require.Len(t, open, 1, "the span opens anyway, as every call site did before")
	assert.Equal(t, "tc-read", open[0].SpanID)
}
