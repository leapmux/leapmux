package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// agentMessageCapturingWriter records every broadcast AgentChatMessage (persisted
// rows only, seq >= 0) so a test can inspect the move metadata the worker stamps.
type agentMessageCapturingWriter struct {
	channelID string
	mu        sync.Mutex
	msgs      []*leapmuxv1.AgentChatMessage
}

func (w *agentMessageCapturingWriter) SendResponse(_ *leapmuxv1.InnerRpcResponse) error { return nil }
func (w *agentMessageCapturingWriter) SendError(_ int32, _ string) error                { return nil }
func (w *agentMessageCapturingWriter) ChannelID() string                                { return w.channelID }
func (w *agentMessageCapturingWriter) SendStream(s *leapmuxv1.InnerStreamMessage) error {
	resp := &leapmuxv1.WatchEventsResponse{}
	if err := proto.Unmarshal(s.GetPayload(), resp); err != nil {
		return nil
	}
	msg := resp.GetAgentEvent().GetAgentMessage()
	if msg == nil || msg.GetSeq() < 0 {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, msg)
	return nil
}

func (w *agentMessageCapturingWriter) snapshot() []*leapmuxv1.AgentChatMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*leapmuxv1.AgentChatMessage, len(w.msgs))
	copy(out, w.msgs)
	return out
}

// TestNotificationReseqBroadcast_CarriesPreviousSeq pins the protocol contract for the
// reseq move: consolidating a second adjacent notification reseqs the thread row to a
// new tail seq, and the broadcast must mark the move with previous_seq = the OLD seq.
// A consumer (the CLI follower, the frontend store) reconciles the row by id off that
// marker instead of treating the re-emission as a brand-new message. The first
// (standalone) broadcast must carry previous_seq 0 -- it is not a move.
func TestNotificationReseqBroadcast_CarriesPreviousSeq(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	mock := &agentMessageCapturingWriter{channelID: "ch-1"}
	svc.Watchers.WatchAgent("agent-1", &EventWatcher{ChannelID: "ch-1", Sender: channel.NewSender(mock)})

	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	first, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)
	second, err := json.Marshal(map[string]any{"type": "interrupted"})
	require.NoError(t, err)

	// First notification opens a standalone thread (a NEW row, no move).
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, first)
	// Second adjacent notification consolidates into the thread, reseq'ing it to the tail.
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, second)

	msgs := mock.snapshot()
	require.Len(t, msgs, 2, "the standalone create and the consolidation reseq each broadcast once")

	assert.Equal(t, int64(0), msgs[0].GetPreviousSeq(), "a brand-new standalone notification is not a move")
	firstSeq := msgs[0].GetSeq()

	// The consolidation re-broadcasts the SAME id at a strictly-higher seq, marked as a
	// move FROM the standalone's seq.
	assert.Equal(t, msgs[0].GetId(), msgs[1].GetId(), "the consolidation moves the same row")
	assert.Greater(t, msgs[1].GetSeq(), firstSeq, "the reseq lands at a new, higher tail seq")
	assert.Equal(t, firstSeq, msgs[1].GetPreviousSeq(), "previous_seq marks the seq the row moved from")
}
