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
	depth, lines := tracker.Snapshot("")
	assert.Equal(t, int32(0), depth)
	assert.Equal(t, "[]", lines)

	depth, _ = tracker.Snapshot("span-1")
	assert.Equal(t, int32(0), depth)

	// Open first span (subagent).
	tracker.OpenSpan("span-1", "")
	depth, lines = tracker.Snapshot("span-1")
	assert.Equal(t, int32(1), depth)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, "span-1", parsed[0].SpanID)
	assert.Equal(t, 0, parsed[0].Color)

	// Close the span.
	tracker.CloseSpan("span-1")
	depth, lines = tracker.Snapshot("span-1")
	assert.Equal(t, int32(0), depth)
	assert.Equal(t, "[]", lines)
}

func TestSpanTracker_NestedSpans(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-1", "")
	tracker.OpenSpan("span-2", "span-1")

	depth1, _ := tracker.Snapshot("span-1")
	depth2, lines := tracker.Snapshot("span-2")
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

	_, lines := tracker.Snapshot("")
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

	_, lines := tracker.Snapshot("")
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
	_, lines := tracker.Snapshot("")
	assert.Equal(t, "[]", lines)
}

func TestSpanTracker_DepthForMainScope(t *testing.T) {
	tracker := &SpanTracker{}
	tracker.OpenSpan("span-1", "")
	depth, _ := tracker.Snapshot("")
	assert.Equal(t, int32(0), depth)
}

func TestSpanTracker_ColorIncrements(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-1", "")
	tracker.CloseSpan("span-1")
	tracker.OpenSpan("span-2", "")

	_, lines := tracker.Snapshot("span-2")
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, 1, parsed[0].Color)
}

func TestSpanTracker_SpanLinesNullSlots(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")
	tracker.OpenSpan("span-C", "")
	tracker.CloseSpan("span-B")

	_, lines := tracker.Snapshot("")
	var parsed []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)

	assert.NotEqual(t, "null", string(parsed[0]))
	assert.Equal(t, "null", string(parsed[1]))
	assert.NotEqual(t, "null", string(parsed[2]))
}
