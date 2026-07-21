package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestMessageSeq_NotReusedAfterTailDelete asserts the per-agent seq high-water
// makes a deleted tail seq permanently unavailable: a subsequent message gets a
// strictly higher seq, never the freed one. Without this, an AFTER_CURSOR reconnect
// could not tell the deleted row from the new one that took its seq.
func TestMessageSeq_NotReusedAfterTailDelete(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	mk := func(id string) int64 {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID:            id,
			AgentID:       "agent-1",
			Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
			Content:       []byte("hi"),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			CreatedAt:     sqltime.NewSQLiteTime(time.Now()),
		})
		require.NoError(t, err)
		return seq
	}

	require.Equal(t, int64(1), mk("m1"))
	require.Equal(t, int64(2), mk("m2"))

	// Delete the tail (seq 2). The old MAX(live)+1 allocation would free seq 2.
	deletedSeq, err := svc.Queries.DeleteMessageByAgentAndID(ctx, db.DeleteMessageByAgentAndIDParams{
		AgentID: "agent-1",
		ID:      "m2",
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), deletedSeq)

	// MAX(live seq) drops to 1, but the high-water remembers 2.
	maxLive, err := svc.Queries.GetMaxSeqByAgentID(ctx, "agent-1")
	require.NoError(t, err)
	require.Equal(t, int64(1), maxLive)

	// The next message must NOT reuse seq 2 -- it gets 3.
	assert.Equal(t, int64(3), mk("m3"), "a deleted tail seq must never be reused")
}

// TestMessageSeq_ReseqUsesHighWater asserts the notification reseq path
// (UpdateNotificationThread) also allocates from the high-water, so moving a row to
// the tail gives it a strictly-higher seq than any prior message -- including one
// above a since-deleted tail's freed seq.
func TestMessageSeq_ReseqUsesHighWater(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	mk := func(id string) int64 {
		seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
			ID:            id,
			AgentID:       "agent-1",
			Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX,
			Content:       []byte("{}"),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			CreatedAt:     sqltime.NewSQLiteTime(time.Now()),
		})
		require.NoError(t, err)
		return seq
	}

	mk("n1")         // seq 1
	mk("n2")         // seq 2
	last := mk("n3") // seq 3
	require.Equal(t, int64(3), last)

	// Delete the tail (seq 3); the high-water stays at 3.
	_, err := svc.Queries.DeleteMessageByAgentAndID(ctx, db.DeleteMessageByAgentAndIDParams{AgentID: "agent-1", ID: "n3"})
	require.NoError(t, err)

	// Reseq n1 to the tail: it must land at high-water+1 == 4, above the freed seq 3.
	newSeq, err := svc.Queries.UpdateNotificationThread(ctx, db.UpdateNotificationThreadParams{
		ID:                 "n1",
		AgentID:            "agent-1",
		Content:            []byte("{}"),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SpanLines:          "[]",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(4), newSeq, "reseq must allocate above the high-water, never reuse a freed seq")
}
