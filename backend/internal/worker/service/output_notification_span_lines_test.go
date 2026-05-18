package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestPersistNotification_StandaloneCapturesActiveSpans verifies that a
// brand-new notification (createNotificationStandalone) captures the
// currently-active spans, so a LeapMux notification arriving while a
// subagent's tool_use is open renders with passthrough vertical bars.
func TestPersistNotification_StandaloneCapturesActiveSpans(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, withWorkspaces("ws-1"))
	sink := setupAgentWithWatcher(t, svc, w, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	svc.Output.spanTracker("agent-1").OpenSpan("span-A", "")

	notif, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, notif))

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, rows[0].Source)
	assert.Equal(t, int64(0), rows[0].Depth)

	persisted := parseSpanLinesJSON(t, rows[0].SpanLines)
	require.Len(t, persisted, 1)
	require.NotNil(t, persisted[0])
	assert.Equal(t, "span-A", persisted[0].SpanID)
	assert.Equal(t, SpanLineActive, persisted[0].Type)

	assert.Equal(t, rows[0].SpanLines, broadcastSpanLinesByID(t, w, rows[0].ID), "broadcast SpanLines must match persisted SpanLines")
}

// TestPersistNotification_AppendRefreshesSpanLines verifies that when a
// later notification appends to an existing thread, the row's span_lines
// is re-snapshotted at append time. The thread's seq is bumped to the
// latest position, so its bars must reflect the spans active *now* — not
// whatever was active when the thread was first created.
func TestPersistNotification_AppendRefreshesSpanLines(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, withWorkspaces("ws-1"))
	sink := setupAgentWithWatcher(t, svc, w, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	first, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, first))

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "[]", rows[0].SpanLines, "no spans active when the first notification arrived")
	threadID := rows[0].ID

	// A subagent opens a span between the two notifications.
	svc.Output.spanTracker("agent-1").OpenSpan("span-A", "")

	second, err := json.Marshal(map[string]any{"type": "interrupted"})
	require.NoError(t, err)
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, second))

	rows, err = svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1, "the two adjacent LeapMux notifications must consolidate into one thread row")
	assert.Equal(t, threadID, rows[0].ID, "consolidation reuses the original row id")

	persisted := parseSpanLinesJSON(t, rows[0].SpanLines)
	require.Len(t, persisted, 1, "the appended thread must pick up span-A even though it was opened after the row was created")
	require.NotNil(t, persisted[0])
	assert.Equal(t, "span-A", persisted[0].SpanID)
	assert.Equal(t, SpanLineActive, persisted[0].Type)

	assert.Equal(t, rows[0].SpanLines, broadcastSpanLinesByID(t, w, threadID), "the latest broadcast for the thread must carry the refreshed SpanLines")
}

// TestPersistNotification_AppendDropsClosedSpan complements the above:
// when a span has closed by the time the next notification appends, the
// thread row's span_lines must shrink accordingly, not retain the stale
// bar.
func TestPersistNotification_AppendDropsClosedSpan(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, withWorkspaces("ws-1"))
	sink := setupAgentWithWatcher(t, svc, w, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	svc.Output.spanTracker("agent-1").OpenSpan("span-A", "")

	first, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, first))

	svc.Output.spanTracker("agent-1").CloseSpan("span-A")

	second, err := json.Marshal(map[string]any{"type": "interrupted"})
	require.NoError(t, err)
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, second))

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "[]", rows[0].SpanLines, "a closed span must not linger as stale bars on a re-appended thread row")
}

// TestPersistNotification_StandaloneEmptyTracker is the regression guard
// for notifications that arrive with no active spans. They should look
// exactly the same as before this change — no left-side bars.
func TestPersistNotification_StandaloneEmptyTracker(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, withWorkspaces("ws-1"))
	sink := setupAgentWithWatcher(t, svc, w, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	notif, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, notif))

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "[]", rows[0].SpanLines)
	assert.Equal(t, "[]", broadcastSpanLinesByID(t, w, rows[0].ID))
}
