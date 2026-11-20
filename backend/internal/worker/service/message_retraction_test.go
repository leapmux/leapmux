package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// messageRetractionFixture wires one root sink with a live watch, so a test can
// drive the retraction methods and observe the deleted sequences.
func messageRetractionFixture(t *testing.T) (*Service, agent.ProviderServices, *testResponseWriter) {
	t.Helper()
	svc, _, w := setupTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := newTestRootOutputSink(t, svc.Output, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	registerAgentWatch(svc, w.channelID, "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)
	return svc, sink, w
}

// retractedSeqs collects every AgentMessagesRetracted payload the watch saw.
func retractedSeqs(t *testing.T, w *testResponseWriter) [][]int64 {
	t.Helper()
	var events [][]int64
	for _, stream := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		require.NoError(t, proto.Unmarshal(stream.GetPayload(), &resp))
		if removed := resp.GetAgentEvent().GetMessagesRetracted(); removed != nil {
			events = append(events, removed.GetSeqs())
		}
	}
	return events
}

func messageRetractionPersist(t *testing.T, sink agent.ProviderServices, key string) {
	t.Helper()
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{"type":"assistant"}`), IdempotencyKey: key, AgentSessionID: "s-1"},
		agent.SpanInfo{}))
}

func TestRetractMessagesByIdempotencyKey_DeletesRowsAndEmitsSeqsOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink, w := messageRetractionFixture(t)
	messageRetractionPersist(t, sink, "u-1")
	messageRetractionPersist(t, sink, "u-2")
	messageRetractionPersist(t, sink, "u-3")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		agent.MessageContent{Original: []byte(`{"type":"user"}`), AgentSessionID: "s-1"}, agent.SpanInfo{}))

	captured := sink.CaptureMessage(agent.MessageContent{AgentSessionID: "s-1"}, agent.SpanInfo{})
	require.NoError(t, sink.RetractMessagesByIdempotencyKey(captured, []string{"u-1", "", "u-1", "u-3", "u-unknown"}))

	for _, key := range []string{"u-1", "u-3", "u-unknown"} {
		present, err := svc.Queries.HasMessageRetraction(ctx, db.HasMessageRetractionParams{
			AgentID: "agent-1", AgentSessionID: "s-1", IdempotencyKey: key,
		})
		require.NoError(t, err)
		assert.True(t, present, "every named key records its receipt, which suppresses a late insertion")
	}
	for _, key := range []string{"u-1", "u-3"} {
		_, err := svc.Queries.GetMessageByIdempotencyKey(ctx, db.GetMessageByIdempotencyKeyParams{
			AgentID: "agent-1", AgentSessionID: "s-1", IdempotencyKey: key,
		})
		assert.ErrorIs(t, err, sql.ErrNoRows, "the retracted row %s is deleted", key)
	}
	survivor, err := svc.Queries.GetMessageByIdempotencyKey(ctx, db.GetMessageByIdempotencyKeyParams{
		AgentID: "agent-1", AgentSessionID: "s-1", IdempotencyKey: "u-2",
	})
	require.NoError(t, err, "the neighbor survives")
	assert.Positive(t, survivor.Seq)

	events := retractedSeqs(t, w)
	require.Len(t, events, 1, "the deletion emits its sequences exactly once")
	assert.Equal(t, []int64{1, 3}, events[0], "sequences are positive and sorted")

	// A redelivery of a retracted key inserts no row at any sequence.
	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "m-replay", AgentID: "agent-1", AgentSessionID: "s-1", IdempotencyKey: "u-1",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: []byte(`{}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		CreatedAt:          sqltime.NewSQLiteTime(time.Now()),
	})
	assert.ErrorIs(t, err, sql.ErrNoRows, "a retracted key cannot be reinserted at a fresh sequence")

	// A duplicate signal names no new row and emits no event.
	require.NoError(t, sink.RetractMessagesByIdempotencyKey(captured, []string{"u-1", "u-3"}))
	assert.Len(t, retractedSeqs(t, w), 1, "duplicate signals emit no event")

	// The next message uses a fresh sequence above every prior one.
	seq, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "m-fresh", AgentID: "agent-1", AgentSessionID: "s-1",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: []byte(`{}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		CreatedAt:          sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(5), seq, "deletion preserves the sequence high-water")
}

func TestRetractMessagesByIdempotencyKey_RevokedClaimDeletesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, sink, _ := messageRetractionFixture(t)
	messageRetractionPersist(t, sink, "u-1")

	captured := sink.CaptureMessage(agent.MessageContent{AgentSessionID: "s-1"}, agent.SpanInfo{})
	// A replacement native lifetime reuses the session string and the row key.
	// The old capture is revoked and must not delete the current row.
	sink.AdoptNativeSession("s-1")

	require.NoError(t, sink.RetractMessagesByIdempotencyKey(captured, []string{"u-1"}))

	row, err := svc.Queries.GetMessageByIdempotencyKey(ctx, db.GetMessageByIdempotencyKeyParams{
		AgentID: "agent-1", AgentSessionID: "s-1", IdempotencyKey: "u-1",
	})
	require.NoError(t, err, "a revoked claim consumes its position without deletion")
	assert.Positive(t, row.Seq)
}

func TestIsMessageRetracted_ReadsTheDurableReceipt(t *testing.T) {
	t.Parallel()

	_, sink, _ := messageRetractionFixture(t)
	messageRetractionPersist(t, sink, "u-1")
	captured := sink.CaptureMessage(agent.MessageContent{AgentSessionID: "s-1"}, agent.SpanInfo{})

	retracted, err := sink.IsMessageRetracted(captured, "u-1")
	require.NoError(t, err)
	assert.False(t, retracted)

	retracted, err = sink.IsMessageRetracted(captured, "")
	require.NoError(t, err)
	assert.False(t, retracted, "an empty key is never retracted")

	require.NoError(t, sink.RetractMessagesByIdempotencyKey(captured, []string{"u-1"}))
	retracted, err = sink.IsMessageRetracted(captured, "u-1")
	require.NoError(t, err)
	assert.True(t, retracted)

	retracted, err = sink.IsMessageRetracted(captured, "u-2")
	require.NoError(t, err)
	assert.False(t, retracted, "the lookup is exact per key")

	// A claim with no captured owner takes no transcript authority.
	var uncaptured agent.MessageContent
	assert.Error(t, sink.RetractMessagesByIdempotencyKey(uncaptured, []string{"u-1"}))
	_, err = sink.IsMessageRetracted(uncaptured, "u-1")
	assert.Error(t, err)
}
