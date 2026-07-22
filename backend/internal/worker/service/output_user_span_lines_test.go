package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// broadcastSpanLinesByID returns the SpanLines of the most recent
// AgentMessage broadcast on the watcher stream whose Id matches msgID.
// Walking in reverse handles paths like notification-thread appends that
// produce multiple broadcasts for the same row id; reading the latest
// broadcast is what frontends would see. Fails the test if no matching
// broadcast is found.
func broadcastSpanLinesByID(t *testing.T, w *testResponseWriter, msgID string) string {
	t.Helper()
	streams := w.streamsSnapshot()
	for i := len(streams) - 1; i >= 0; i-- {
		ev := decodeWatchAgentEvent(t, streams[i])
		msg := ev.GetAgentMessage()
		if msg == nil {
			continue
		}
		if msg.Id == msgID {
			return msg.SpanLines
		}
	}
	t.Fatalf("no AgentMessage broadcast with id %q on watcher stream", msgID)
	return ""
}

func parseSpanLinesJSON(t *testing.T, raw string) []*SpanLine {
	t.Helper()
	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(raw), &parsed))
	return parsed
}

// setupAgentWithWatcher creates an agent row, starts a mock agent process,
// registers a watcher on it, and arranges for shutdown via t.Cleanup.
// Returns the sink so callers that drive the OutputHandler directly can
// reach it.
func setupAgentWithWatcher(t *testing.T, svc *Service, w *testResponseWriter, agentID string, provider leapmuxv1.AgentProvider) agent.OutputSink {
	t.Helper()
	ctx := context.Background()

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: provider,
	}))

	sink := svc.Output.NewSink(agentID, provider)
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    agentID,
		Options:    map[string]string{agent.OptionIDModel: "opus"},
		WorkingDir: t.TempDir(),
	}, sink)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAgent(agentID) })

	svc.Watchers.SetAgentWatches(w.channelID, []string{agentID}, w)
	return sink
}

// persistNotif persists a notification through the sink and asserts no error,
// discarding the broadcast flag PersistNotification returns. Keeps the many
// notification-thread tests terse now that the signature returns (bool, error).
func persistNotif(t *testing.T, sink agent.OutputSink, source leapmuxv1.MessageSource, content []byte) {
	t.Helper()
	_, err := sink.PersistNotification(source, content)
	require.NoError(t, err)
}

func TestSnapshotPassthroughSpanLines_EmptyTracker(t *testing.T) {
	h := NewOutputHandler(nil, nil, NewWatcherManager(), nil, nil)
	assert.Equal(t, "[]", h.snapshotPassthroughSpanLines("agent-1"))
}

func TestSnapshotPassthroughSpanLines_SingleOpenSpan(t *testing.T) {
	h := NewOutputHandler(nil, nil, NewWatcherManager(), nil, nil)
	h.spanTracker("agent-1").OpenSpan("span-A", "")

	parsed := parseSpanLinesJSON(t, h.snapshotPassthroughSpanLines("agent-1"))
	require.Len(t, parsed, 1)
	require.NotNil(t, parsed[0])
	assert.Equal(t, "span-A", parsed[0].SpanID)
	assert.Equal(t, SpanLineActive, parsed[0].Type, "user-message passthrough should not draw a connector")
	assert.GreaterOrEqual(t, parsed[0].Color, 1, "active span must have an assigned color")
}

func TestSnapshotPassthroughSpanLines_NestedSpans(t *testing.T) {
	h := NewOutputHandler(nil, nil, NewWatcherManager(), nil, nil)
	h.spanTracker("agent-1").OpenSpan("span-A", "")
	h.spanTracker("agent-1").OpenSpan("span-B", "span-A")

	parsed := parseSpanLinesJSON(t, h.snapshotPassthroughSpanLines("agent-1"))
	require.Len(t, parsed, 2)
	for _, line := range parsed {
		require.NotNil(t, line)
		assert.Equal(t, SpanLineActive, line.Type, "every nested span renders as a passthrough vertical bar — no connectors on a user row")
	}
	assert.Equal(t, "span-A", parsed[0].SpanID)
	assert.Equal(t, "span-B", parsed[1].SpanID)
}

func TestSnapshotPassthroughSpanLines_PerAgentIsolation(t *testing.T) {
	h := NewOutputHandler(nil, nil, NewWatcherManager(), nil, nil)
	h.spanTracker("agent-1").OpenSpan("span-A", "")

	// Other agents must see an empty snapshot — span trackers are per-agent.
	assert.Equal(t, "[]", h.snapshotPassthroughSpanLines("agent-2"))
}

// TestSendAgentMessage_PersistsSpanLinesWhileSpanIsOpen verifies that a
// human-typed user message captures the currently-active spans, so the
// frontend renders unbroken vertical bars across the user row instead of
// breaking the span column.
func TestSendAgentMessage_PersistsSpanLinesWhileSpanIsOpen(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	setupAgentWithWatcher(t, svc, w, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	// Pretend a tool_use opened a span before the user typed.
	svc.Output.spanTracker("agent-1").OpenSpan("span-A", "")

	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "agent-1",
		Content: "hello",
	}, w)
	require.Empty(t, w.errors)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.Equal(t, int64(0), rows[0].Depth, "user messages stay at root depth — span lines provide the visual indentation")

	persisted := parseSpanLinesJSON(t, rows[0].SpanLines)
	require.Len(t, persisted, 1)
	require.NotNil(t, persisted[0])
	assert.Equal(t, "span-A", persisted[0].SpanID)
	assert.Equal(t, SpanLineActive, persisted[0].Type)

	// The broadcast must carry the same SpanLines string the DB row has —
	// otherwise live watchers and reload watchers see different rendering.
	assert.Equal(t, rows[0].SpanLines, broadcastSpanLinesByID(t, w, rows[0].ID), "broadcast SpanLines must match persisted SpanLines")
}

// TestSendAgentMessage_SpanLinesEmptyWhenNoSpansActive guards against a
// regression where the helper returns something other than "[]" for an
// idle tracker. A user message at the root with no active spans should
// render exactly as before this change — no left-side bars.
func TestSendAgentMessage_SpanLinesEmptyWhenNoSpansActive(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	setupAgentWithWatcher(t, svc, w, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "agent-1",
		Content: "hello",
	}, w)
	require.Empty(t, w.errors)

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

// TestSendSyntheticUserMessage_PersistsSpanLinesWhileSpanIsOpen verifies
// that the plan-mode UI prompt path (sendSyntheticUserMessage) also
// captures active spans. This site is a separate direct-SQL path from
// the SendAgentMessage RPC, so it gets its own coverage.
func TestSendSyntheticUserMessage_PersistsSpanLinesWhileSpanIsOpen(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, withWorkspaces("ws-1"))
	setupAgentWithWatcher(t, svc, w, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	svc.Output.spanTracker("agent-1").OpenSpan("span-A", "")

	svc.sendSyntheticUserMessage("agent-1", "synthetic prompt", leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED)

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   10,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)

	persisted := parseSpanLinesJSON(t, rows[0].SpanLines)
	require.Len(t, persisted, 1)
	require.NotNil(t, persisted[0])
	assert.Equal(t, "span-A", persisted[0].SpanID)
	assert.Equal(t, SpanLineActive, persisted[0].Type)

	assert.Equal(t, rows[0].SpanLines, broadcastSpanLinesByID(t, w, rows[0].ID), "broadcast SpanLines must match persisted SpanLines")
}
