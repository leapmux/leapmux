package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// DeleteAgentMessage broadcasts the deleted row's seq so a windowed client can
// lower its recorded live-tail seq when the deleted row was at/beyond that tail.
// A second delete of the same id is an idempotent no-op: no error, no broadcast.
func TestDeleteAgentMessage_BroadcastsDeletedSeq(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "msg-1",
		AgentID:       "agent-1",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte(`{"type":"text","text":"hi"}`),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)
	require.Positive(t, seq)
	// DeleteAgentMessage only deletes FAILED user messages, so flag it failed.
	require.NoError(t, svc.Queries.SetMessageDeliveryError(ctx, db.SetMessageDeliveryErrorParams{
		DeliveryError: "delivery failed", ID: "msg-1", AgentID: "agent-1",
	}))

	svc.Watchers.WatchAgent("agent-1", &EventWatcher{
		ChannelID: w.channelID,
		Sender:    channel.NewSender(w),
	})

	dispatch(d, "DeleteAgentMessage", &leapmuxv1.DeleteAgentMessageRequest{
		AgentId:   "agent-1",
		MessageId: "msg-1",
	}, w)
	require.Empty(t, w.errors)

	// Exactly one MessageDeleted broadcast, carrying the deleted row's seq.
	deletes := collectMessageDeleted(t, w.streamsSnapshot())
	require.Len(t, deletes, 1)
	assert.Equal(t, "agent-1", deletes[0].GetAgentId())
	assert.Equal(t, "msg-1", deletes[0].GetMessageId())
	assert.Equal(t, seq, deletes[0].GetSeq())
	// The agent has no rows left, so the authoritative new tail is 0.
	assert.Equal(t, int64(0), deletes[0].GetNewLatestSeq())

	// The row is gone from the DB.
	_, err = svc.Queries.GetMessageByAgentAndID(ctx, db.GetMessageByAgentAndIDParams{ID: "msg-1", AgentID: "agent-1"})
	assert.Error(t, err)

	// A second delete of the same id is an idempotent no-op: success, no error,
	// and NO additional MessageDeleted broadcast (the first one already told
	// every watcher the real seq).
	dispatch(d, "DeleteAgentMessage", &leapmuxv1.DeleteAgentMessageRequest{
		AgentId:   "agent-1",
		MessageId: "msg-1",
	}, w)
	require.Empty(t, w.errors)
	assert.Len(t, collectMessageDeleted(t, w.streamsSnapshot()), 1, "double-delete must not re-broadcast")
}

// DeleteAgentMessage reports the authoritative new live-tail seq (MAX after the
// delete) so a windowed client sets its recorded tail exactly: deleting the tail
// row drops it to the prior row's seq; deleting a non-tail row leaves it at the
// unchanged tail.
func TestDeleteAgentMessage_ReportsNewLatestSeq(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	mk := func(id string) int64 {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID:            id,
			AgentID:       "agent-1",
			Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
			Content:       []byte(`{"type":"text","text":"hi"}`),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			CreatedAt:     time.Now(),
		})
		require.NoError(t, err)
		require.NoError(t, svc.Queries.SetMessageDeliveryError(ctx, db.SetMessageDeliveryErrorParams{
			DeliveryError: "delivery failed", ID: id, AgentID: "agent-1",
		}))
		return seq
	}
	seq1 := mk("msg-1")
	seq2 := mk("msg-2")
	seq3 := mk("msg-3")
	require.Less(t, seq1, seq2)
	require.Less(t, seq2, seq3)

	svc.Watchers.WatchAgent("agent-1", &EventWatcher{
		ChannelID: w.channelID,
		Sender:    channel.NewSender(w),
	})

	// Delete a NON-tail row (msg-1): the live tail is unchanged (still seq3).
	dispatch(d, "DeleteAgentMessage", &leapmuxv1.DeleteAgentMessageRequest{AgentId: "agent-1", MessageId: "msg-1"}, w)
	require.Empty(t, w.errors)
	deletes := collectMessageDeleted(t, w.streamsSnapshot())
	require.Len(t, deletes, 1)
	assert.Equal(t, seq1, deletes[0].GetSeq())
	assert.Equal(t, seq3, deletes[0].GetNewLatestSeq(), "deleting a non-tail row leaves the tail unchanged")

	// Delete the TAIL row (msg-3): the new authoritative tail drops to seq2.
	dispatch(d, "DeleteAgentMessage", &leapmuxv1.DeleteAgentMessageRequest{AgentId: "agent-1", MessageId: "msg-3"}, w)
	require.Empty(t, w.errors)
	deletes = collectMessageDeleted(t, w.streamsSnapshot())
	require.Len(t, deletes, 2)
	assert.Equal(t, seq3, deletes[1].GetSeq())
	assert.Equal(t, seq2, deletes[1].GetNewLatestSeq(), "deleting the tail drops it to the prior row's seq")
}

// DeleteAgentMessage refuses to delete anything but a FAILED user message: a
// DELIVERED user message (no delivery_error) and an AGENT message are both
// rejected with an error and no MessageDeleted broadcast, and the rows survive.
// Deleting an arbitrary delivered/agent row would corrupt the windowing/span
// invariants the windowed client relies on.
func TestDeleteAgentMessage_RejectsNonFailedUserMessage(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// A DELIVERED user message (no delivery_error) and an AGENT message: neither deletable.
	_, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "user-ok",
		AgentID:       "agent-1",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte(`{"type":"text","text":"hi"}`),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)
	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "agent-msg",
		AgentID:       "agent-1",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		Content:       []byte(`{"type":"result"}`),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)

	svc.Watchers.WatchAgent("agent-1", &EventWatcher{
		ChannelID: w.channelID,
		Sender:    channel.NewSender(w),
	})

	for _, id := range []string{"user-ok", "agent-msg"} {
		dispatch(d, "DeleteAgentMessage", &leapmuxv1.DeleteAgentMessageRequest{
			AgentId:   "agent-1",
			MessageId: id,
		}, w)
	}

	// Both rejected: an error per attempt, no MessageDeleted broadcast, rows intact.
	assert.Len(t, w.errors, 2)
	assert.Empty(t, collectMessageDeleted(t, w.streamsSnapshot()))
	_, err = svc.Queries.GetMessageByAgentAndID(ctx, db.GetMessageByAgentAndIDParams{ID: "user-ok", AgentID: "agent-1"})
	assert.NoError(t, err, "a delivered user message must survive")
	_, err = svc.Queries.GetMessageByAgentAndID(ctx, db.GetMessageByAgentAndIDParams{ID: "agent-msg", AgentID: "agent-1"})
	assert.NoError(t, err, "an agent message must survive")
}

// collectMessageDeleted extracts every AgentMessageDeleted payload from a slice
// of stream messages, ignoring any other agent-event kinds.
func collectMessageDeleted(t *testing.T, streams []*leapmuxv1.InnerStreamMessage) []*leapmuxv1.AgentMessageDeleted {
	t.Helper()
	var out []*leapmuxv1.AgentMessageDeleted
	for _, stream := range streams {
		if md := decodeWatchAgentEvent(t, stream).GetMessageDeleted(); md != nil {
			out = append(out, md)
		}
	}
	return out
}
