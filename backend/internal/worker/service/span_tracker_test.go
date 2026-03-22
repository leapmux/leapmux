package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpanTracker_OpenClose(t *testing.T) {
	tracker := &SpanTracker{}

	// Initially empty.
	depth, lines := tracker.Snapshot("", "", false)
	assert.Equal(t, int32(0), depth)
	assert.Equal(t, "[]", lines)

	depth, _ = tracker.Snapshot("span-1", "", false)
	assert.Equal(t, int32(0), depth)

	// Open first span (subagent).
	tracker.OpenSpan("span-1", "")
	depth, lines = tracker.Snapshot("span-1", "", false)
	assert.Equal(t, int32(1), depth)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, "span-1", parsed[0].SpanID)
	assert.Equal(t, 0, parsed[0].Color)

	// Close the span.
	tracker.CloseSpan("span-1")
	depth, lines = tracker.Snapshot("span-1", "", false)
	assert.Equal(t, int32(0), depth)
	assert.Equal(t, "[]", lines)
}

func TestSpanTracker_NestedSpans(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-1", "")
	tracker.OpenSpan("span-2", "span-1")

	depth1, _ := tracker.Snapshot("span-1", "", false)
	depth2, lines := tracker.Snapshot("span-2", "", false)
	assert.Equal(t, int32(1), depth1)
	assert.Equal(t, int32(2), depth2)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, "span-1", parsed[0].SpanID)
	assert.Equal(t, "span-2", parsed[1].SpanID)
	assert.Equal(t, 0, parsed[0].Color)
	assert.Equal(t, 1, parsed[1].Color)
}

func TestSpanTracker_ColumnReuse(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-1", "")
	tracker.OpenSpan("span-2", "")
	tracker.CloseSpan("span-1")
	tracker.OpenSpan("span-3", "")

	_, lines := tracker.Snapshot("", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)

	assert.Equal(t, "span-3", parsed[0].SpanID)
	assert.Equal(t, "span-2", parsed[1].SpanID)
}

func TestSpanTracker_ParallelSpans(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")
	tracker.OpenSpan("span-C", "")

	_, lines := tracker.Snapshot("", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)

	assert.Equal(t, 0, parsed[0].Color)
	assert.Equal(t, 1, parsed[1].Color)
	assert.Equal(t, 2, parsed[2].Color)
}

func TestSpanTracker_CloseNonexistent(t *testing.T) {
	tracker := &SpanTracker{}
	tracker.CloseSpan("nonexistent")
	_, lines := tracker.Snapshot("", "", false)
	assert.Equal(t, "[]", lines)
}

func TestSpanTracker_DepthForMainScope(t *testing.T) {
	tracker := &SpanTracker{}
	tracker.OpenSpan("span-1", "")
	depth, _ := tracker.Snapshot("", "", false)
	assert.Equal(t, int32(0), depth)
}

func TestSpanTracker_ColorIncrements(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-1", "")
	tracker.CloseSpan("span-1")
	tracker.OpenSpan("span-2", "")

	_, lines := tracker.Snapshot("span-2", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, 1, parsed[0].Color)
}

func TestSpanTracker_PeekNextColor(t *testing.T) {
	tracker := &SpanTracker{}

	// First peek returns 0.
	assert.Equal(t, int32(0), tracker.PeekNextColor())

	// Opening a span consumes color 0; next peek returns 1.
	tracker.OpenSpan("span-1", "")
	assert.Equal(t, int32(1), tracker.PeekNextColor())

	// Close and reopen — peek still advances.
	tracker.CloseSpan("span-1")
	assert.Equal(t, int32(1), tracker.PeekNextColor())
	tracker.OpenSpan("span-2", "")
	assert.Equal(t, int32(2), tracker.PeekNextColor())
}

func TestSpanTracker_ColorFor(t *testing.T) {
	tracker := &SpanTracker{}

	// Non-existent span returns -1.
	assert.Equal(t, int32(-1), tracker.ColorFor("span-1"))

	// Active span returns its color.
	tracker.OpenSpan("span-1", "")
	assert.Equal(t, int32(0), tracker.ColorFor("span-1"))

	tracker.OpenSpan("span-2", "span-1")
	assert.Equal(t, int32(0), tracker.ColorFor("span-1"))
	assert.Equal(t, int32(1), tracker.ColorFor("span-2"))

	// Closed span returns -1.
	tracker.CloseSpan("span-1")
	assert.Equal(t, int32(-1), tracker.ColorFor("span-1"))
	assert.Equal(t, int32(1), tracker.ColorFor("span-2"))
}

func TestSpanTracker_RenderingHints(t *testing.T) {
	tracker := &SpanTracker{}

	// Two parallel spans: A (col 0, color 0) and B (col 1, color 1).
	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")

	// Snapshot for a message connecting to span-A (col 0).
	// Col 0 = connector, col 1 = active_passthrough with passthrough_color = 0.
	_, lines := tracker.Snapshot("", "span-A", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineConnector, parsed[0].Type)
	assert.Equal(t, SpanLineActivePassthrough, parsed[1].Type)
	assert.Equal(t, 0, parsed[1].PassthroughColor)

	// Snapshot for a message connecting to span-B (col 1).
	// Col 0 = active (no passthrough needed), col 1 = connector.
	_, lines = tracker.Snapshot("", "span-B", false)
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineActive, parsed[0].Type)
	assert.Equal(t, SpanLineConnector, parsed[1].Type)

	// No connector span — all columns are active.
	_, lines = tracker.Snapshot("", "", false)
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineActive, parsed[0].Type)
	assert.Equal(t, SpanLineActive, parsed[1].Type)
}

func TestSpanTracker_ConnectorEnd(t *testing.T) {
	tracker := &SpanTracker{}

	// Two parallel spans: A (col 0) and B (col 1).
	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")

	// Closing snapshot for span-A: col 0 = connector_end (└), col 1 = active_passthrough.
	_, lines := tracker.Snapshot("", "span-A", true)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineConnectorEnd, parsed[0].Type)
	assert.Equal(t, SpanLineActivePassthrough, parsed[1].Type)

	// Closing snapshot for span-B: col 0 = active, col 1 = connector_end (└).
	_, lines = tracker.Snapshot("", "span-B", true)
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineActive, parsed[0].Type)
	assert.Equal(t, SpanLineConnectorEnd, parsed[1].Type)

	// Non-closing snapshot still uses connector (├).
	_, lines = tracker.Snapshot("", "span-A", false)
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	assert.Equal(t, SpanLineConnector, parsed[0].Type)
}

func TestSpanTracker_PassthroughWithNullSlot(t *testing.T) {
	tracker := &SpanTracker{}

	// Three spans: A (col 0), B (col 1), C (col 2). Close B to create a null slot.
	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")
	tracker.OpenSpan("span-C", "")
	tracker.CloseSpan("span-B")

	// Connect to span-A: col 0 = connector, col 1 = passthrough (null slot),
	// col 2 = active_passthrough.
	_, lines := tracker.Snapshot("", "span-A", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)
	assert.Equal(t, SpanLineConnector, parsed[0].Type)
	require.NotNil(t, parsed[1])
	assert.Equal(t, SpanLinePassthrough, parsed[1].Type)
	assert.Equal(t, 0, parsed[1].PassthroughColor)
	assert.Equal(t, SpanLineActivePassthrough, parsed[2].Type)
	assert.Equal(t, 0, parsed[2].PassthroughColor)
}

func TestSpanTracker_SpanLinesNullSlots(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")
	tracker.OpenSpan("span-C", "")
	tracker.CloseSpan("span-B")

	_, lines := tracker.Snapshot("", "", false)
	var parsed []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)

	assert.NotEqual(t, "null", string(parsed[0]))
	assert.Equal(t, "null", string(parsed[1]))
	assert.NotEqual(t, "null", string(parsed[2]))
}
