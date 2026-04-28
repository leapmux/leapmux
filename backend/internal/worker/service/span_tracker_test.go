package service

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTrackerWithSeed builds a SpanTracker with a deterministic RNG so tests
// that depend on randomized branches are reproducible.
func newTrackerWithSeed(seed uint64) *SpanTracker {
	return &SpanTracker{rng: rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))}
}

// snapshotColor returns the color a given active span has by inspecting the
// tracker's serialized span lines.
func snapshotColor(t *testing.T, tracker *SpanTracker, spanID string) int {
	t.Helper()
	_, lines, _ := tracker.Snapshot("", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	for _, l := range parsed {
		if l != nil && l.SpanID == spanID {
			return l.Color
		}
	}
	t.Fatalf("span %q not found in snapshot", spanID)
	return 0
}

func TestSpanTracker_OpenClose(t *testing.T) {
	tracker := &SpanTracker{}

	// Initially empty.
	depth, lines, _ := tracker.Snapshot("", "", false)
	assert.Equal(t, int32(0), depth)
	assert.Equal(t, "[]", lines)

	depth, _, _ = tracker.Snapshot("span-1", "", false)
	assert.Equal(t, int32(0), depth)

	// Open first span (subagent).
	tracker.OpenSpan("span-1", "")
	depth, lines, _ = tracker.Snapshot("span-1", "", false)
	assert.Equal(t, int32(1), depth)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, "span-1", parsed[0].SpanID)
	assert.GreaterOrEqual(t, parsed[0].Color, 1)
	assert.LessOrEqual(t, parsed[0].Color, spanPaletteSize)

	// Close the span.
	tracker.CloseSpan("span-1")
	depth, lines, _ = tracker.Snapshot("span-1", "", false)
	assert.Equal(t, int32(0), depth)
	assert.Equal(t, "[]", lines)
}

func TestSpanTracker_NestedSpans(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-1", "")
	tracker.OpenSpan("span-2", "span-1")

	depth1, _, _ := tracker.Snapshot("span-1", "", false)
	depth2, lines, _ := tracker.Snapshot("span-2", "", false)
	assert.Equal(t, int32(1), depth1)
	assert.Equal(t, int32(2), depth2)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, "span-1", parsed[0].SpanID)
	assert.Equal(t, "span-2", parsed[1].SpanID)
	for _, l := range parsed {
		assert.GreaterOrEqual(t, l.Color, 1)
		assert.LessOrEqual(t, l.Color, spanPaletteSize)
	}
	assert.NotEqual(t, parsed[0].Color, parsed[1].Color, "parent and nested child should not share a color when palette has free slots")
}

func TestSpanTracker_ColumnReuse(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-1", "")
	tracker.OpenSpan("span-2", "")
	tracker.CloseSpan("span-1")
	tracker.OpenSpan("span-3", "")

	_, lines, _ := tracker.Snapshot("", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)

	assert.Nil(t, parsed[0])
	assert.Equal(t, "span-2", parsed[1].SpanID)
	assert.Equal(t, "span-3", parsed[2].SpanID)
}

func TestSpanTracker_RootSpanAppendsAfterRightmostActiveColumn(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("spawn-1", "")        // col 0
	tracker.OpenSpan("child-1", "spawn-1") // col 1
	tracker.OpenSpan("spawn-2", "")        // col 2
	tracker.OpenSpan("child-2", "spawn-2") // col 3
	tracker.CloseSpan("spawn-1")           // free col 0, child-1 stays active
	tracker.CloseSpan("child-1")           // free col 1
	tracker.OpenSpan("wait", "")           // should append at col 4, not reuse 0

	_, lines, _ := tracker.Snapshot("", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 5)

	assert.Nil(t, parsed[0])
	assert.Nil(t, parsed[1])
	assert.Equal(t, "spawn-2", parsed[2].SpanID)
	assert.Equal(t, "child-2", parsed[3].SpanID)
	assert.Equal(t, "wait", parsed[4].SpanID)
}

func TestSpanTracker_ParallelSpans(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")
	tracker.OpenSpan("span-C", "")

	_, lines, _ := tracker.Snapshot("", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)

	seen := make(map[int]bool, 3)
	for _, l := range parsed {
		assert.GreaterOrEqual(t, l.Color, 1)
		assert.LessOrEqual(t, l.Color, spanPaletteSize)
		seen[l.Color] = true
	}
	assert.Len(t, seen, 3, "three concurrently active spans must have three distinct colors")
}

func TestSpanTracker_CloseNonexistent(t *testing.T) {
	tracker := &SpanTracker{}
	tracker.CloseSpan("nonexistent")
	_, lines, _ := tracker.Snapshot("", "", false)
	assert.Equal(t, "[]", lines)
}

func TestSpanTracker_DepthForMainScope(t *testing.T) {
	tracker := &SpanTracker{}
	tracker.OpenSpan("span-1", "")
	depth, _, _ := tracker.Snapshot("", "", false)
	assert.Equal(t, int32(0), depth)
}

func TestSpanTracker_ColorAfterClose(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-1", "")
	tracker.CloseSpan("span-1")
	tracker.OpenSpan("span-2", "")

	_, lines, _ := tracker.Snapshot("span-2", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.GreaterOrEqual(t, parsed[0].Color, 1)
	assert.LessOrEqual(t, parsed[0].Color, spanPaletteSize)
}

func TestSpanTracker_ReserveSpanColor(t *testing.T) {
	tracker := &SpanTracker{}

	// Reserving for span-1 returns a color in palette range.
	c1 := tracker.ReserveSpanColor("span-1", "")
	assert.GreaterOrEqual(t, c1, int32(1))
	assert.LessOrEqual(t, c1, int32(spanPaletteSize))

	// Reserving again with the same span ID is idempotent.
	assert.Equal(t, c1, tracker.ReserveSpanColor("span-1", ""))

	// OpenSpan consumes the reservation; the active span has the reserved color.
	tracker.OpenSpan("span-1", "")
	assert.Equal(t, int(c1), snapshotColor(t, tracker, "span-1"))

	// Reserving for span-2 while span-1 is active must avoid span-1's color.
	c2 := tracker.ReserveSpanColor("span-2", "")
	assert.GreaterOrEqual(t, c2, int32(1))
	assert.LessOrEqual(t, c2, int32(spanPaletteSize))
	assert.NotEqual(t, c1, c2, "second reservation must avoid the active span's color")
}

func TestSpanTracker_PaletteSaturation(t *testing.T) {
	tracker := &SpanTracker{}

	// Open the full palette of root-level spans simultaneously.
	for i := 0; i < spanPaletteSize; i++ {
		tracker.OpenSpan(fmt.Sprintf("s%d", i), "")
	}

	_, lines, _ := tracker.Snapshot("", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, spanPaletteSize)

	seen := make(map[int]bool, spanPaletteSize)
	for _, l := range parsed {
		assert.GreaterOrEqual(t, l.Color, 1)
		assert.LessOrEqual(t, l.Color, spanPaletteSize)
		seen[l.Color] = true
	}
	assert.Len(t, seen, spanPaletteSize, "saturating the palette must use every color exactly once")
}

func TestSpanTracker_RenderingHints(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")
	colorA := snapshotColor(t, tracker, "span-A")

	// Snapshot for a message connecting to span-A (col 0).
	// Col 0 = connector, col 1 = active_passthrough with passthrough_color = colorA.
	_, lines, _ := tracker.Snapshot("", "span-A", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineConnector, parsed[0].Type)
	assert.Equal(t, SpanLineActivePassthrough, parsed[1].Type)
	assert.Equal(t, colorA, parsed[1].PassthroughColor)

	// Snapshot for a message connecting to span-B (col 1).
	// Col 0 = active (no passthrough needed), col 1 = connector.
	_, lines, _ = tracker.Snapshot("", "span-B", false)
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineActive, parsed[0].Type)
	assert.Equal(t, SpanLineConnector, parsed[1].Type)

	// No connector span — all columns are active.
	_, lines, _ = tracker.Snapshot("", "", false)
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
	colorA := snapshotColor(t, tracker, "span-A")

	// Closing snapshot for span-A: col 0 = connector_end (└), col 1 = active_passthrough.
	_, lines, _ := tracker.Snapshot("", "span-A", true)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineConnectorEnd, parsed[0].Type)
	assert.Equal(t, SpanLineActivePassthrough, parsed[1].Type)
	assert.Equal(t, colorA, parsed[1].PassthroughColor)

	// Closing snapshot for span-B: col 0 = active, col 1 = connector_end (└).
	_, lines, _ = tracker.Snapshot("", "span-B", true)
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineActive, parsed[0].Type)
	assert.Equal(t, SpanLineConnectorEnd, parsed[1].Type)

	// Non-closing snapshot still uses connector (├).
	_, lines, _ = tracker.Snapshot("", "span-A", false)
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	assert.Equal(t, SpanLineConnector, parsed[0].Type)
}

func TestSpanTracker_ShouldBroadcastStreamChunk_AllowsWhenNoSpansActive(t *testing.T) {
	tracker := &SpanTracker{}
	assert.True(t, tracker.ShouldBroadcastStreamChunk())
}

func TestSpanTracker_ShouldBroadcastStreamChunk_HidesWhenSpanActive(t *testing.T) {
	tracker := &SpanTracker{}
	tracker.OpenSpan("span-A", "")
	assert.False(t, tracker.ShouldBroadcastStreamChunk())
}

func TestSpanTracker_ShouldBroadcastStreamChunk_HidesWhenMultipleSpansActive(t *testing.T) {
	tracker := &SpanTracker{}
	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")
	assert.False(t, tracker.ShouldBroadcastStreamChunk())
}

func TestSpanTracker_PassthroughWithNullSlot(t *testing.T) {
	tracker := &SpanTracker{}

	// Three spans: A (col 0), B (col 1), C (col 2). Close B to create a null slot.
	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")
	tracker.OpenSpan("span-C", "")
	colorA := snapshotColor(t, tracker, "span-A")
	tracker.CloseSpan("span-B")

	// Connect to span-A: col 0 = connector, col 1 = passthrough (null slot),
	// col 2 = active_passthrough.
	_, lines, _ := tracker.Snapshot("", "span-A", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)
	assert.Equal(t, SpanLineConnector, parsed[0].Type)
	require.NotNil(t, parsed[1])
	assert.Equal(t, SpanLinePassthrough, parsed[1].Type)
	assert.Equal(t, colorA, parsed[1].PassthroughColor)
	assert.Equal(t, SpanLineActivePassthrough, parsed[2].Type)
	assert.Equal(t, colorA, parsed[2].PassthroughColor)
}

func TestSpanTracker_SpanLinesNullSlots(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")
	tracker.OpenSpan("span-C", "")
	tracker.CloseSpan("span-B")

	_, lines, _ := tracker.Snapshot("", "", false)
	var parsed []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)

	assert.NotEqual(t, "null", string(parsed[0]))
	assert.Equal(t, "null", string(parsed[1]))
	assert.NotEqual(t, "null", string(parsed[2]))
}

func TestSpanTracker_ChildSpanColumnAfterSiblingClose(t *testing.T) {
	// Regression: when a sibling span closes and frees a column to the LEFT
	// of a parent span, a new child of that parent must still be placed to
	// the RIGHT of its parent — not in the freed left-side slot.
	//
	// Scenario: main context spawns A, B, C, D (columns 0-3).
	// C closes (frees col 2). D opens tool D-1 (child of D at col 3).
	// D-1 must get col 4 (or any col > 3), NOT col 2.
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-A", "") // col 0
	tracker.OpenSpan("span-B", "") // col 1
	tracker.OpenSpan("span-C", "") // col 2
	tracker.OpenSpan("span-D", "") // col 3
	tracker.CloseSpan("span-C")    // frees col 2

	// D-1 is a tool_use inside D → child of span-D.
	tracker.OpenSpan("span-D1", "span-D") // must be col 4, not col 2

	_, lines, _ := tracker.Snapshot("span-D", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))

	// Expect 5 columns: A@0, B@1, null@2, D@3, D-1@4
	require.Len(t, parsed, 5)
	assert.Equal(t, "span-A", parsed[0].SpanID)
	assert.Equal(t, "span-B", parsed[1].SpanID)
	assert.Nil(t, parsed[2]) // freed slot from span-C
	assert.Equal(t, "span-D", parsed[3].SpanID)
	assert.Equal(t, "span-D1", parsed[4].SpanID)

	// Now close D-1 and verify the connector_end is at col 4 (to the right of D).
	_, lines, _ = tracker.Snapshot("span-D", "span-D1", true)
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 5)
	assert.Equal(t, SpanLineActive, parsed[0].Type)
	assert.Equal(t, SpanLineActive, parsed[1].Type)
	assert.Nil(t, parsed[2])
	assert.Equal(t, SpanLineActive, parsed[3].Type)
	assert.Equal(t, SpanLineConnectorEnd, parsed[4].Type)
}

func TestSpanTracker_ChildSpanAfterDescendantClose(t *testing.T) {
	// Regression: when a child span (D) of parent (C) closes but D's own
	// child (E) remains active, a new child of C (F) must be placed to the
	// RIGHT of E — not in D's freed column.
	//
	// Scenario:
	//   Open A(col0) → B(col1) → C(col2) → D(col3) → E(col4)
	//   Close D (frees col 3, but E at col 4 is still active)
	//   Open F (child of C) → must get col 5, NOT col 3
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-A", "")       // col 0
	tracker.OpenSpan("span-B", "span-A") // col 1
	tracker.OpenSpan("span-C", "span-B") // col 2
	tracker.OpenSpan("span-D", "span-C") // col 3
	tracker.OpenSpan("span-E", "span-D") // col 4
	tracker.CloseSpan("span-D")          // frees col 3; E still active at col 4

	// F is a new child of C — must go to col 5, not col 3.
	tracker.OpenSpan("span-F", "span-C")

	_, lines, _ := tracker.Snapshot("span-C", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))

	// Expect 6 columns: A@0, B@1, C@2, null@3, E@4, F@5
	require.Len(t, parsed, 6, "expected 6 columns: A@0, B@1, C@2, null@3, E@4, F@5")
	assert.Equal(t, "span-A", parsed[0].SpanID)
	assert.Equal(t, "span-B", parsed[1].SpanID)
	assert.Equal(t, "span-C", parsed[2].SpanID)
	assert.Nil(t, parsed[3]) // freed slot from span-D
	assert.Equal(t, "span-E", parsed[4].SpanID)
	assert.Equal(t, "span-F", parsed[5].SpanID)

	// Verify F's connector_end is at col 5 (not col 3).
	_, lines, _ = tracker.Snapshot("span-C", "span-F", true)
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 6)
	assert.Equal(t, SpanLineConnectorEnd, parsed[5].Type)
}

func TestSpanTracker_ChildSpanSkipsNonDescendantGap(t *testing.T) {
	// Regression: when a span closes and frees a column between the parent's
	// subtree and a non-descendant span, a new child must be placed AFTER
	// the non-descendant span — not in the freed gap.
	//
	// Scenario (mirrors the real bug):
	//   Agent@1 has child s2@2 (descendant). Unrelated s4@4 is NOT a
	//   descendant. A previously closed span freed col 3.
	//   Opening Bash (child of Agent) must get col 5, not col 3.
	tracker := &SpanTracker{}

	tracker.OpenSpan("root", "")      // col 0
	tracker.OpenSpan("agent", "root") // col 1
	tracker.OpenSpan("s2", "agent")   // col 2
	tracker.OpenSpan("old", "agent")  // col 3
	tracker.OpenSpan("s4", "root")    // col 4
	tracker.CloseSpan("old")          // frees col 3

	// Bash is a child of agent — must go to col 5, not col 3.
	tracker.OpenSpan("bash", "agent")

	_, lines, _ := tracker.Snapshot("agent", "", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))

	// Expect 6 columns: root@0, agent@1, s2@2, null@3, s4@4, bash@5
	require.Len(t, parsed, 6, "expected 6 columns")
	assert.Equal(t, "root", parsed[0].SpanID)
	assert.Equal(t, "agent", parsed[1].SpanID)
	assert.Equal(t, "s2", parsed[2].SpanID)
	assert.Nil(t, parsed[3])
	assert.Equal(t, "s4", parsed[4].SpanID)
	assert.Equal(t, "bash", parsed[5].SpanID)
}

func TestSpanTracker_DeepNestingDepth(t *testing.T) {
	tracker := &SpanTracker{}

	// A → B → C → D (depths 1, 2, 3, 4).
	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "span-A")
	tracker.OpenSpan("span-C", "span-B")
	tracker.OpenSpan("span-D", "span-C")

	depth, _, _ := tracker.Snapshot("span-A", "", false)
	assert.Equal(t, int32(1), depth)
	depth, _, _ = tracker.Snapshot("span-B", "", false)
	assert.Equal(t, int32(2), depth)
	depth, _, _ = tracker.Snapshot("span-C", "", false)
	assert.Equal(t, int32(3), depth)
	depth, _, _ = tracker.Snapshot("span-D", "", false)
	assert.Equal(t, int32(4), depth)
}

func TestSpanTracker_DepthAfterIntermediateClose(t *testing.T) {
	// A → B → C. Close B. C's depth should still be 3 (not affected
	// by the intermediate parent closing).
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "span-A")
	tracker.OpenSpan("span-C", "span-B")

	// Verify C is depth 3 before closing B.
	depth, _, _ := tracker.Snapshot("span-C", "", false)
	assert.Equal(t, int32(3), depth)

	// Close B — C's depth must remain 3.
	tracker.CloseSpan("span-B")
	depth, _, _ = tracker.Snapshot("span-C", "", false)
	assert.Equal(t, int32(3), depth)

	// A's depth must remain 1.
	depth, _, _ = tracker.Snapshot("span-A", "", false)
	assert.Equal(t, int32(1), depth)
}

func TestSpanTracker_MultipleChildrenDepth(t *testing.T) {
	// Parent P with three children C1, C2, C3 — all should have same depth.
	tracker := &SpanTracker{}

	tracker.OpenSpan("P", "")
	tracker.OpenSpan("C1", "P")
	tracker.OpenSpan("C2", "P")
	tracker.OpenSpan("C3", "P")

	for _, spanID := range []string{"C1", "C2", "C3"} {
		depth, _, _ := tracker.Snapshot(spanID, "", false)
		assert.Equal(t, int32(2), depth, "depth for %s", spanID)
	}
}

func TestSpanTracker_SnapshotConnectorColor(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "")
	colorA := snapshotColor(t, tracker, "span-A")
	colorB := snapshotColor(t, tracker, "span-B")

	// Connector to A should return A's color.
	_, _, connColor := tracker.Snapshot("", "span-A", false)
	assert.Equal(t, int32(colorA), connColor)

	// Connector to B should return B's color.
	_, _, connColor = tracker.Snapshot("", "span-B", false)
	assert.Equal(t, int32(colorB), connColor)

	// No connector returns 0.
	_, _, connColor = tracker.Snapshot("", "", false)
	assert.Equal(t, int32(0), connColor)

	// Connector to nonexistent span returns 0.
	_, _, connColor = tracker.Snapshot("", "nonexistent", false)
	assert.Equal(t, int32(0), connColor)
}

func TestSpanTracker_ConnectorOnNestedChild(t *testing.T) {
	tracker := &SpanTracker{}

	// A (col 0) → B (col 1). Connect to B while A is active.
	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "span-A")
	colorB := snapshotColor(t, tracker, "span-B")

	// Connector to B: A = active, B = connector.
	_, lines, connColor := tracker.Snapshot("span-A", "span-B", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineActive, parsed[0].Type)
	assert.Equal(t, SpanLineConnector, parsed[1].Type)
	assert.Equal(t, int32(colorB), connColor)

	// Closing connector to B: A = active, B = connector_end.
	_, lines, _ = tracker.Snapshot("span-A", "span-B", true)
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineActive, parsed[0].Type)
	assert.Equal(t, SpanLineConnectorEnd, parsed[1].Type)
}

func TestSpanTracker_ConnectorWithDepthAndPassthrough(t *testing.T) {
	tracker := &SpanTracker{}

	// A (col 0) → B (col 1) → C (col 2). Connect to A while B, C active.
	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "span-A")
	tracker.OpenSpan("span-C", "span-B")
	colorA := snapshotColor(t, tracker, "span-A")
	colorB := snapshotColor(t, tracker, "span-B")

	_, lines, _ := tracker.Snapshot("", "span-A", false)
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)
	assert.Equal(t, SpanLineConnector, parsed[0].Type)
	assert.Equal(t, SpanLineActivePassthrough, parsed[1].Type)
	assert.Equal(t, colorA, parsed[1].PassthroughColor)
	assert.Equal(t, SpanLineActivePassthrough, parsed[2].Type)
	assert.Equal(t, colorA, parsed[2].PassthroughColor)

	// Connect to B: A = active, B = connector, C = active_passthrough.
	_, lines, _ = tracker.Snapshot("span-A", "span-B", false)
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)
	assert.Equal(t, SpanLineActive, parsed[0].Type)
	assert.Equal(t, SpanLineConnector, parsed[1].Type)
	assert.Equal(t, SpanLineActivePassthrough, parsed[2].Type)
	assert.Equal(t, colorB, parsed[2].PassthroughColor)
}

func TestSpanTracker_SpanType(t *testing.T) {
	tracker := &SpanTracker{}

	// GetSpanType returns "" for unknown spans.
	assert.Equal(t, "", tracker.GetSpanType("unknown"))
	assert.Equal(t, "", tracker.GetSpanType(""))

	// SetSpanType stores the type.
	tracker.SetSpanType("span-1", "Grep")
	assert.Equal(t, "Grep", tracker.GetSpanType("span-1"))

	// SetSpanType with empty spanID or spanType is a no-op.
	tracker.SetSpanType("", "Edit")
	tracker.SetSpanType("span-2", "")
	assert.Equal(t, "", tracker.GetSpanType("span-2"))

	// CloseSpan removes the span type.
	tracker.SetSpanType("span-3", "Read")
	assert.Equal(t, "Read", tracker.GetSpanType("span-3"))
	tracker.CloseSpan("span-3")
	assert.Equal(t, "", tracker.GetSpanType("span-3"))
}

func TestSpanTracker_ToolUseConnectorInSubagent(t *testing.T) {
	// When a tool_use message is emitted inside a subagent, the tool_use's
	// own span hasn't been opened yet (it opens after persist). The parent
	// subagent span IS active. The span line for the subagent should render
	// as "connector" (├), not "active" (│).
	tracker := &SpanTracker{}
	tracker.OpenSpan("subagent", "")

	// Simulate persistAndBroadcast for a tool_use inside the subagent:
	//   span.SpanID       = "tool-1"  (not yet open)
	//   span.ParentSpanID = "subagent" (already open)
	//   span.Closing      = false
	connectorSpanID := resolveConnectorSpanID("tool-1", "", "subagent", false)
	_, lines, _ := tracker.Snapshot("subagent", connectorSpanID, false)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, SpanLineConnector, parsed[0].Type,
		"tool_use inside subagent should show connector to parent span")
}

func TestSpanTracker_ToolResultConnectorInSubagent(t *testing.T) {
	// A tool_result (closing) message should still connect to the tool's
	// own span, not the parent — the span is open at this point.
	tracker := &SpanTracker{}
	tracker.OpenSpan("subagent", "")
	tracker.OpenSpan("tool-1", "subagent")

	connectorSpanID := resolveConnectorSpanID("tool-1", "", "subagent", true)
	_, lines, _ := tracker.Snapshot("subagent", connectorSpanID, true)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineActive, parsed[0].Type)
	assert.Equal(t, SpanLineConnectorEnd, parsed[1].Type,
		"tool_result should show connector_end on its own span")
}

func TestSpanTracker_TopLevelToolUseNoConnector(t *testing.T) {
	// A top-level tool_use (no parent span) should have no connector.
	tracker := &SpanTracker{}

	connectorSpanID := resolveConnectorSpanID("tool-1", "", "", false)
	_, lines, _ := tracker.Snapshot("", connectorSpanID, false)
	assert.Equal(t, "[]", lines)
}

func TestSpanTracker_ExplicitClosingConnectorUsesOverride(t *testing.T) {
	tracker := &SpanTracker{}
	tracker.OpenSpan("subagent", "")

	connectorSpanID := resolveConnectorSpanID("wait-1", "subagent", "", true)
	_, lines, _ := tracker.Snapshot("", connectorSpanID, true)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, SpanLineConnectorEnd, parsed[0].Type,
		"explicit connector override should let a spanless message close its parent span")
}

func TestSpanTracker_ParentMapClearedOnAllClose(t *testing.T) {
	tracker := &SpanTracker{}

	// Open nested spans A → B → C and verify depth works.
	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "span-A")
	tracker.OpenSpan("span-C", "span-B")

	depth, _, _ := tracker.Snapshot("span-C", "", false)
	assert.Equal(t, int32(3), depth)

	// Close all spans in reverse order.
	tracker.CloseSpan("span-C")
	assert.NotEmpty(t, tracker.parentMap, "parentMap should still have entries while spans are active")
	tracker.CloseSpan("span-B")
	assert.NotEmpty(t, tracker.parentMap)
	tracker.CloseSpan("span-A")

	// parentMap should be cleared when all spans are closed.
	assert.Empty(t, tracker.parentMap, "parentMap should be cleared when all spans close")

	// Open new spans and verify depth/ancestry still works after the map was cleared.
	tracker.OpenSpan("span-X", "")
	tracker.OpenSpan("span-Y", "span-X")
	tracker.OpenSpan("span-Z", "span-Y")

	depth, _, _ = tracker.Snapshot("span-Z", "", false)
	assert.Equal(t, int32(3), depth)
	depth, _, _ = tracker.Snapshot("span-Y", "", false)
	assert.Equal(t, int32(2), depth)
	depth, _, _ = tracker.Snapshot("span-X", "", false)
	assert.Equal(t, int32(1), depth)
}

func TestSpanTracker_Reset(t *testing.T) {
	tracker := &SpanTracker{}

	// Open nested spans and set types.
	tracker.OpenSpan("span-A", "")
	tracker.OpenSpan("span-B", "span-A")
	tracker.SetSpanType("span-A", "Agent")
	tracker.SetSpanType("span-B", "Grep")

	// Verify state exists before reset.
	depth, lines, _ := tracker.Snapshot("span-B", "", false)
	assert.Equal(t, int32(2), depth)
	assert.NotEqual(t, "[]", lines)
	assert.Equal(t, "Agent", tracker.GetSpanType("span-A"))

	// Reset clears everything.
	tracker.Reset()

	depth, lines, _ = tracker.Snapshot("", "", false)
	assert.Equal(t, int32(0), depth)
	assert.Equal(t, "[]", lines)
	assert.Equal(t, "", tracker.GetSpanType("span-A"))
	assert.Equal(t, "", tracker.GetSpanType("span-B"))

	// Reservation slot is cleared — a new reserve returns a valid color.
	c := tracker.ReserveSpanColor("span-X", "")
	assert.GreaterOrEqual(t, c, int32(1))
	assert.LessOrEqual(t, c, int32(spanPaletteSize))

	// Tracker is fully reusable after reset.
	tracker.OpenSpan("span-X", "")
	depth, lines, _ = tracker.Snapshot("span-X", "", false)
	assert.Equal(t, int32(1), depth)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, "span-X", parsed[0].SpanID)
	assert.Equal(t, int(c), parsed[0].Color, "OpenSpan must consume the reserved color")
}

func TestSpanTracker_ResetClearsPending(t *testing.T) {
	tracker := &SpanTracker{}

	tracker.ReserveSpanColor("a", "")
	require.NotNil(t, tracker.pending, "reservation populates pending")

	tracker.Reset()
	assert.Nil(t, tracker.pending, "Reset must clear pending")
}

func TestSpanTracker_ResetClearsDeck(t *testing.T) {
	tracker := newTrackerWithSeed(1)

	// Drain the deck partway by opening and closing a few spans.
	for i := 0; i < 3; i++ {
		spanID := fmt.Sprintf("s%d", i)
		tracker.OpenSpan(spanID, "")
		tracker.CloseSpan(spanID)
	}
	require.NotEmpty(t, tracker.deck, "deck should hold remaining cards mid-round")

	tracker.Reset()
	assert.Empty(t, tracker.deck, "Reset must drop the in-progress deck so the next turn starts with a fresh shuffle")
}

func TestSpanTracker_ColorAvoidsActiveColors(t *testing.T) {
	tracker := newTrackerWithSeed(1)

	// Open spanPaletteSize-1 spans so exactly one color remains free.
	for i := 0; i < spanPaletteSize-1; i++ {
		tracker.OpenSpan(fmt.Sprintf("s%d", i), "")
	}

	// Determine which color is currently free.
	used := make(map[int]bool)
	for i := 0; i < spanPaletteSize-1; i++ {
		used[snapshotColor(t, tracker, fmt.Sprintf("s%d", i))] = true
	}
	freeColor := -1
	for c := 1; c <= spanPaletteSize; c++ {
		if !used[c] {
			freeColor = c
			break
		}
	}
	require.NotEqual(t, -1, freeColor, "exactly one color should be free")

	// The next span MUST get that unique free color.
	tracker.OpenSpan("last", "")
	assert.Equal(t, freeColor, snapshotColor(t, tracker, "last"))
}

func TestSpanTracker_ColorAvoidsParentWhenAvailable(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		tracker := newTrackerWithSeed(seed)
		tracker.OpenSpan("parent", "")
		tracker.OpenSpan("child", "parent")

		parentColor := snapshotColor(t, tracker, "parent")
		childColor := snapshotColor(t, tracker, "child")
		assert.NotEqual(t, parentColor, childColor, "child must not share color with parent (seed=%d)", seed)
	}
}

func TestSpanTracker_ExhaustedPaletteAvoidsParentAndAdjacent(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		tracker := newTrackerWithSeed(seed)

		// Open 8 root-level spans; this saturates the palette.
		for i := 0; i < spanPaletteSize; i++ {
			tracker.OpenSpan(fmt.Sprintf("s%d", i), "")
		}

		// Open a 9th span as a child of s3 (col 3). Its column lands at 8
		// (rightmost), so the only column-adjacent active span is s7 at col 7.
		tracker.OpenSpan("ninth", "s3")
		ninthColor := snapshotColor(t, tracker, "ninth")
		parentColor := snapshotColor(t, tracker, "s3")
		leftColor := snapshotColor(t, tracker, "s7")

		assert.GreaterOrEqual(t, ninthColor, 1, "color in palette range (seed=%d)", seed)
		assert.LessOrEqual(t, ninthColor, spanPaletteSize, "color in palette range (seed=%d)", seed)
		assert.NotEqual(t, parentColor, ninthColor, "saturated branch must avoid parent color (seed=%d)", seed)
		assert.NotEqual(t, leftColor, ninthColor, "saturated branch must avoid left-adjacent color (seed=%d)", seed)
	}
}

func TestSpanTracker_ReserveMatchesOpenSpan(t *testing.T) {
	tracker := newTrackerWithSeed(42)

	cases := []struct {
		spanID, parentSpanID string
	}{
		{"a", ""},
		{"b", "a"},
		{"c", "a"},
		{"d", "b"},
	}

	for _, tc := range cases {
		reserved := tracker.ReserveSpanColor(tc.spanID, tc.parentSpanID)
		tracker.OpenSpan(tc.spanID, tc.parentSpanID)
		actual := snapshotColor(t, tracker, tc.spanID)
		assert.Equal(t, int(reserved), actual, "spanID=%q: OpenSpan must use the reserved color", tc.spanID)
	}
}

func TestSpanTracker_ReserveIsIdempotentForSameSpanID(t *testing.T) {
	tracker := newTrackerWithSeed(7)

	first := tracker.ReserveSpanColor("a", "")
	second := tracker.ReserveSpanColor("a", "")
	assert.Equal(t, first, second, "repeated reserve for same spanID must return the same color")

	// Reservation for a different ID computes independently.
	other := tracker.ReserveSpanColor("b", "")
	assert.GreaterOrEqual(t, other, int32(1))
	assert.LessOrEqual(t, other, int32(spanPaletteSize))
}

func TestSpanTracker_DistributionUsesAllColors(t *testing.T) {
	tracker := newTrackerWithSeed(99)
	seen := make(map[int]bool, spanPaletteSize)

	for i := 0; i < 200; i++ {
		spanID := fmt.Sprintf("s%d", i)
		tracker.OpenSpan(spanID, "")
		seen[snapshotColor(t, tracker, spanID)] = true
		tracker.CloseSpan(spanID)
	}

	assert.Len(t, seen, spanPaletteSize, "200 root-level openings must observe every palette color at least once")
}

func TestSpanTracker_ShuffleVisitsAllColorsInWindowOf8(t *testing.T) {
	// With shuffle-deck selection, any 8 sequential single-span openings
	// (no concurrent spans constraining the deck) must use all 8 palette
	// colors before any repeats. Repeat across many seeds and across
	// multiple back-to-back windows on the same tracker.
	for seed := uint64(1); seed <= 25; seed++ {
		tracker := newTrackerWithSeed(seed)
		for window := 0; window < 3; window++ {
			seen := make(map[int]bool, spanPaletteSize)
			for i := 0; i < spanPaletteSize; i++ {
				spanID := fmt.Sprintf("w%d-s%d", window, i)
				tracker.OpenSpan(spanID, "")
				seen[snapshotColor(t, tracker, spanID)] = true
				tracker.CloseSpan(spanID)
			}
			assert.Lenf(t, seen, spanPaletteSize,
				"seed=%d window=%d: 8 sequential opens must visit all 8 palette colors", seed, window)
		}
	}
}

func TestSpanTracker_PendingCountsAsInUseForNextReserve(t *testing.T) {
	tracker := newTrackerWithSeed(3)

	first := tracker.ReserveSpanColor("a", "")
	second := tracker.ReserveSpanColor("b", "")

	assert.NotEqual(t, first, second, "pending color from a previous reserve must count as in use")
	assert.GreaterOrEqual(t, second, int32(1))
	assert.LessOrEqual(t, second, int32(spanPaletteSize))
}

func TestSpanTracker_PendingClearedOnReserveDifferentSpan(t *testing.T) {
	tracker := newTrackerWithSeed(5)

	tracker.ReserveSpanColor("a", "")
	bColor := tracker.ReserveSpanColor("b", "")

	// pending now refers to b; opening a recomputes a fresh color.
	tracker.OpenSpan("a", "")
	aColor := snapshotColor(t, tracker, "a")

	assert.GreaterOrEqual(t, aColor, 1)
	assert.LessOrEqual(t, aColor, spanPaletteSize)
	assert.NotEqual(t, int(bColor), aColor, "a's recomputed color must avoid b's pending color (it counts as in use)")
}

func TestSpanTracker_CloseClearsPendingForSameSpan(t *testing.T) {
	tracker := newTrackerWithSeed(11)

	tracker.ReserveSpanColor("a", "")
	require.NotNil(t, tracker.pending)

	tracker.CloseSpan("a")
	assert.Nil(t, tracker.pending, "CloseSpan for the pending span must clear pending")
}
