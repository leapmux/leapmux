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

	registerAgentWatch(svc, w.channelID, agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, w)
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
	t.Parallel()

	h := NewOutputHandler(nil, nil, NewWatcherManager(), nil, nil)
	assert.Equal(t, "[]", h.snapshotPassthroughSpanLines("agent-1"))
}

func TestSnapshotPassthroughSpanLines_SingleOpenSpan(t *testing.T) {
	t.Parallel()

	h := NewOutputHandler(nil, nil, NewWatcherManager(), nil, nil)
	h.rootTracker("agent-1").OpenSpan("span-A", "")

	parsed := parseSpanLinesJSON(t, h.snapshotPassthroughSpanLines("agent-1"))
	require.Len(t, parsed, 1)
	require.NotNil(t, parsed[0])
	assert.Equal(t, "span-A", parsed[0].SpanID)
	assert.Equal(t, SpanLineActive, parsed[0].Type, "user-message passthrough should not draw a connector")
	assert.GreaterOrEqual(t, parsed[0].Color, 1, "active span must have an assigned color")
}

func TestSnapshotPassthroughSpanLines_NestedSpans(t *testing.T) {
	t.Parallel()

	h := NewOutputHandler(nil, nil, NewWatcherManager(), nil, nil)
	h.rootTracker("agent-1").OpenSpan("span-A", "")
	h.rootTracker("agent-1").OpenSpan("span-B", "span-A")

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
	t.Parallel()

	h := NewOutputHandler(nil, nil, NewWatcherManager(), nil, nil)
	h.rootTracker("agent-1").OpenSpan("span-A", "")

	// Other agents must see an empty snapshot — span trackers are per-agent.
	assert.Equal(t, "[]", h.snapshotPassthroughSpanLines("agent-2"))
}
