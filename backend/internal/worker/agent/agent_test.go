package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// openToolSpan holds the order every provider's tool call needs: reserve before
// the persist, persist before the open, record the type either way. Each
// provider still decides `spawns` from its own wire shape.
func TestOpenToolSpan_OrdinaryToolReservesPersistsAndOpens(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	require.NoError(t, openToolSpan(sink, []byte(`{"type":"tool_call"}`), "tc-read", "read", false))

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

	sink := &testSink{}
	require.NoError(t, openToolSpan(sink, []byte(`{"type":"tool_call"}`), "tc-spawn", "Agent", true))

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

	sink := &testSink{}
	require.NoError(t, openToolSpan(sink, []byte(`{}`), "tc-spawn", "Agent", true))
	require.NoError(t, openToolSpan(sink, []byte(`{}`), "tc-read", "read", false))

	msgs := sink.Messages()
	require.Len(t, msgs, 2)
	assert.Empty(t, msgs[1].SpansOpenAtPersist, "the spawn contributes no rail to draw")
}

// testSink delegates its span bookkeeping to the REAL engine, so the geometry a
// provider test asserts is the geometry production computes. It kept its own
// copy before, and drifted from the engine twice.
func TestTestSink_SpanStateComesFromTheRealEngine(t *testing.T) {
	t.Parallel()

	sink := &testSink{}
	// A reservation the tracker really parks: the color is a palette entry, not
	// a constant, and it is the one the matching open consumes.
	reserved := sink.ReserveSpanColor("tu-a", "")
	require.NotZero(t, reserved, "the real tracker never reserves color 0")
	sink.OpenSpan("tu-a", "")
	sink.OpenSpan("tu-b", "tu-a")

	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		[]byte(`{}`), SpanInfo{SpanID: "tu-c"}))
	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	// Column order, and the parentage the engine recorded -- neither of which a
	// hand-kept slice reproduced.
	assert.Equal(t, []testSinkSpanOpen{
		{SpanID: "tu-a", ParentSpanID: ""},
		{SpanID: "tu-b", ParentSpanID: "tu-a"},
	}, msgs[0].SpansOpenAtPersist)

	// The engine's type lifetime, not the double's: a close keeps the type and
	// only a reset clears it.
	sink.SetSpanType("tu-a", "Read")
	sink.CloseSpan("tu-a")
	assert.Equal(t, "Read", sink.GetSpanType("tu-a"))
	sink.ResetSpans()
	assert.Empty(t, sink.GetSpanType("tu-a"))
	assert.Empty(t, sink.liveSpansLocked(), "a reset empties the active set")
}

// A failed persist is reported to the caller, which logs it -- but the span
// still opens, so a later closing message finds a column to end.
func TestOpenToolSpan_ReturnsThePersistErrorAndStillOpensTheSpan(t *testing.T) {
	t.Parallel()

	sink := &testSink{persistErr: assert.AnError}
	err := openToolSpan(sink, []byte(`{}`), "tc-read", "read", false)

	require.ErrorIs(t, err, assert.AnError, "the caller logs this")
	open := sink.OpenSpans()
	require.Len(t, open, 1, "the span opens anyway, as every call site did before")
	assert.Equal(t, "tc-read", open[0].SpanID)
}

// acpStatusIsFinal decides three things: whether a tool_call is persisted as an
// immediate closer, whether a late spawn may give its span back, and whether a
// provider hook produces a closing observation. An unknown status is NOT final,
// so an unrecognized state leaves the call open rather than closing it early.
func TestACPStatusIsFinal(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"completed", "failed", "cancelled"} {
		assert.True(t, acpStatusIsFinal(s), "%q ends the call", s)
	}
	for _, s := range []string{"", "pending", "in_progress", "Completed", "completed "} {
		assert.False(t, acpStatusIsFinal(s), "%q does not end the call", s)
	}
}
