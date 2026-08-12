package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// streamChunkDeltas returns every stream-chunk delta broadcast on the watcher
// stream, in order.
func streamChunkDeltas(t *testing.T, w *testResponseWriter) []string {
	t.Helper()
	var deltas []string
	for _, stream := range w.streamsSnapshot() {
		chunk := decodeWatchAgentEvent(t, stream).GetStreamChunk()
		if chunk == nil {
			continue
		}
		deltas = append(deltas, string(chunk.GetDelta()))
	}
	return deltas
}

// A subagent spawn owns no span, so both of its rows persist at root depth with
// no span lines at all. This is the first shape in the design: the spawn card
// and its result sit flush against the left edge.
func TestSpawnRowsCarryNoSpanLinesWhenNothingElseIsOpen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, w := setupTestService(t)
	sink := setupAgentWithWatcher(t, svc, w, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	// The tool_use of a spawn: it carries a span id but opens no span.
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		[]byte(`{"type":"assistant"}`),
		agent.SpanInfo{SpanID: "tu-spawn", SpanType: "Agent"}))
	// Its tool_result closes a span that was never opened.
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		[]byte(`{"type":"user"}`),
		agent.SpanInfo{SpanID: "tu-spawn", SpanType: "Agent", Closing: true}))

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1", Seq: 0, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for i, row := range rows {
		assert.Equal(t, "[]", row.SpanLines, "spawn row %d draws no rail", i)
		assert.Equal(t, int64(0), row.Depth, "spawn row %d stays at root depth", i)
		assert.Equal(t, int64(0), row.SpanColor, "spawn row %d takes the neutral border", i)
		assert.Equal(t, row.SpanLines, broadcastSpanLinesByID(t, w, row.ID),
			"the broadcast must match the persisted row")
	}
}

// The second shape in the design: a spawn that starts while a Read is running
// draws the Read's rail and nothing more -- one column, not two -- and the
// Read's own result still closes its column with a connector_end.
func TestSpawnInsideAnOpenSpanDrawsExactlyOneColumn(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, w := setupTestService(t)
	sink := setupAgentWithWatcher(t, svc, w, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	// Read's tool_use persists first, then opens its span (the provider order).
	readColor := sink.ReserveSpanColor("tu-read", "")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		[]byte(`{"type":"assistant"}`),
		agent.SpanInfo{SpanID: "tu-read", SpanType: "Read", SpanColor: readColor}))
	sink.SetSpanType("tu-read", "Read")
	sink.OpenSpan("tu-read", "")

	// The spawn's two rows land inside that open span.
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		[]byte(`{"type":"assistant"}`),
		agent.SpanInfo{SpanID: "tu-spawn", SpanType: "Agent"}))
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		[]byte(`{"type":"user"}`),
		agent.SpanInfo{SpanID: "tu-spawn", SpanType: "Agent", Closing: true}))

	// Read's result closes its own span.
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		[]byte(`{"type":"user"}`),
		agent.SpanInfo{SpanID: "tu-read", SpanType: "Read", Closing: true}))
	sink.CloseSpan("tu-read")

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1", Seq: 0, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 4)

	// Row 0 (Read's tool_use) persisted before its span opened, so it has none.
	assert.Equal(t, "[]", rows[0].SpanLines)

	// Rows 1 and 2 (the spawn) each draw one plain vertical for the Read.
	for i, row := range rows[1:3] {
		lines := parseSpanLinesJSON(t, row.SpanLines)
		require.Len(t, lines, 1, "spawn row %d draws exactly one column", i)
		require.NotNil(t, lines[0])
		assert.Equal(t, "tu-read", lines[0].SpanID)
		assert.Equal(t, SpanLineActive, lines[0].Type,
			"a plain vertical: the spawn connects to nothing")
		assert.Equal(t, int64(0), row.SpanColor, "the spawn takes no rail color of its own")
	}

	// Row 3 (Read's result) closes its column.
	closing := parseSpanLinesJSON(t, rows[3].SpanLines)
	require.Len(t, closing, 1)
	require.NotNil(t, closing[0])
	assert.Equal(t, SpanLineConnectorEnd, closing[0].Type)
	assert.Equal(t, "tu-read", closing[0].SpanID)
}

// Live deltas are suppressed while any span is open. A spawn opens none, so the
// parent keeps streaming for the whole subagent run.
func TestStreamChunksFlowWhileASpawnRunsAndStopWhileASpanIsOpen(t *testing.T) {
	t.Parallel()

	svc, _, w := setupTestService(t)
	sink := setupAgentWithWatcher(t, svc, w, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	// A spawn is running: nothing is open, so the delta reaches the watcher.
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		[]byte(`{"type":"assistant"}`),
		agent.SpanInfo{SpanID: "tu-spawn", SpanType: "Agent"}))
	sink.BroadcastStreamChunk([]byte("while the subagent runs"), "", "")

	// An ordinary tool span opens: deltas are suppressed again.
	sink.OpenSpan("tu-read", "")
	sink.BroadcastStreamChunk([]byte("while a tool runs"), "", "")

	assert.Equal(t, []string{"while the subagent runs"}, streamChunkDeltas(t, w))
}
