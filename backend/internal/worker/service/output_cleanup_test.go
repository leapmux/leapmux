package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestClearAgentRuntimeState_DeletesPendingAndBroadcastsCancels covers the
// crash / stop cleanup contract: every pending control_request row for the
// agent must be deleted from the DB, and a controlCancel must be broadcast
// for each one so any still-connected tabs drop the prompt without waiting
// for a reconnect-and-replay round trip.
func TestClearAgentRuntimeState_DeletesPendingAndBroadcastsCancels(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, "ws-1")

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Two pending requests so we can verify both get cancelled.
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "req-1", Payload: []byte(`{"a":1}`),
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: "agent-1", RequestID: "req-2", Payload: []byte(`{"b":2}`),
	}))

	// Register a watcher so broadcasts have somewhere to go.
	watcher := &EventWatcher{ChannelID: "test-ch", Sender: channel.NewSender(w)}
	svc.Watchers.WatchAgent("agent-1", watcher)

	svc.Output.ClearAgentRuntimeState("agent-1")

	// DB rows are gone.
	remaining, err := svc.Queries.ListControlRequestsByAgentID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Empty(t, remaining, "expected all control_requests rows to be deleted")

	// One controlCancel broadcast per previously pending row.
	cancels := collectBroadcastCancelIDs(t, w)
	assert.ElementsMatch(t, []string{"req-1", "req-2"}, cancels)
}

// TestClearAgentRuntimeState_NoPendingIsNoOp ensures the helper is safe to
// call on agents with nothing to clean up — the common case for stop paths
// that fire on every CloseAgent / restart regardless of whether a prompt
// was open.
func TestClearAgentRuntimeState_NoPendingIsNoOp(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, "ws-1")

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-empty",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	watcher := &EventWatcher{ChannelID: "test-ch", Sender: channel.NewSender(w)}
	svc.Watchers.WatchAgent("agent-empty", watcher)

	svc.Output.ClearAgentRuntimeState("agent-empty")

	cancels := collectBroadcastCancelIDs(t, w)
	assert.Empty(t, cancels, "expected no broadcasts when nothing was pending")
}

// collectBroadcastCancelIDs decodes every stream payload captured by the
// test writer and returns the request IDs of any AgentControlCancel events
// that were broadcast.
func collectBroadcastCancelIDs(t *testing.T, w *testResponseWriter) []string {
	t.Helper()
	var ids []string
	for _, msg := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(msg.GetPayload(), &resp); err != nil {
			continue
		}
		ae, ok := resp.GetEvent().(*leapmuxv1.WatchEventsResponse_AgentEvent)
		if !ok {
			continue
		}
		cc, ok := ae.AgentEvent.GetEvent().(*leapmuxv1.AgentEvent_ControlCancel)
		if !ok {
			continue
		}
		ids = append(ids, cc.ControlCancel.GetRequestId())
	}
	return ids
}
