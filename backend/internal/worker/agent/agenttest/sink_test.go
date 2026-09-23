package agenttest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The one test in this helpers file, and it belongs here: every caller of
// ChildAgentIDs asserts NotContains against a child id, so a version that
// returned the map's spawn-span keys satisfied all of them and proved nothing.
// Pinning the return shape beside the fake keeps that from coming back.
func TestTestSink_ChildAgentIDsReturnsChildIDsNotSpawnSpans(t *testing.T) {
	t.Parallel()

	s := &Sink{}
	childID, err := s.EnsureChildAgent("tu-spawn", "task-1", "explore")
	require.NoError(t, err)
	require.Equal(t, "child-of-tu-spawn", childID)

	assert.Equal(t, []string{childID}, s.ChildAgentIDs())
	assert.NotContains(t, s.ChildAgentIDs(), "tu-spawn", "the spawn span is the key, not the id")
}

// Sink delegates its span bookkeeping to the REAL engine, so the geometry a
// provider test asserts is the geometry production computes. It kept its own
// copy before, and drifted from the engine twice.
func TestTestSink_SpanStateComesFromTheRealEngine(t *testing.T) {
	t.Parallel()

	sink := &Sink{}
	// A reservation the tracker really parks: the color is a palette entry, not
	// a constant, and it is the one the matching open consumes.
	reserved := sink.ReserveSpanColor("tu-a", "")
	require.NotZero(t, reserved, "the real tracker never reserves color 0")
	sink.OpenSpan("tu-a", "")
	sink.OpenSpan("tu-b", "tu-a")

	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{}`)}, agent.SpanInfo{SpanID: "tu-c"}))
	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	// Column order, and the parentage the engine recorded -- neither of which a
	// hand-kept slice reproduced.
	assert.Equal(t, []SpanOpen{
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
