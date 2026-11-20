package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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
	sink.UpdateSessionID("s-1")
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
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		agent.MessageContent{Original: []byte(`{"type":"user"}`), AgentSessionID: "s-1"}, agent.SpanInfo{}))
	messageRetractionPersist(t, sink, "u-1")
	messageRetractionPersist(t, sink, "u-2")
	messageRetractionPersist(t, sink, "u-3")

	captured := sink.CaptureMessage(agent.MessageContent{AgentSessionID: "s-1"}, agent.SpanInfo{})
	require.NoError(t, sink.RetractMessagesByIdempotencyKey(captured, []string{"u-3", "", "u-3", "u-1", "u-unknown"}))

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
	assert.Equal(t, []int64{2, 4}, events[0], "sequences are positive and sorted")

	// A redelivery of a retracted key inserts no row at any sequence.
	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID: "m-replay", AgentID: "agent-1", AgentSessionID: "s-1", IdempotencyKey: "u-1",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: []byte(`{}`),
		ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		CreatedAt:          sqltime.NewSQLiteTime(time.Now()),
	})
	assert.ErrorIs(t, err, sql.ErrNoRows, "a retracted key cannot be reinserted at a fresh sequence")

	// A duplicate signal deletes no row and emits no event.
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

func TestChildRetractionUsesTheExactLeaseAcrossTurnSettlement(t *testing.T) {
	t.Parallel()
	svc, root, _ := messageRetractionFixture(t)
	root.UpdateSessionID("s-1")
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "child-spawn", RegistryRole: bgtask.RoleChildTask})
	require.NoError(t, err)
	old := root.AcquireChildStateSink(childID)
	require.NotNil(t, old.Sink)
	old.Sink.UpdateSessionID("s-1")
	old.Sink.SetTurnState(agent.TurnState{Active: true}, 1)
	currentTurn := old.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	old.Sink.SetTurnState(agent.TurnState{}, 2)
	messageRetractionPersist(t, old.Sink, "after-turn")
	require.NoError(t, old.Sink.RetractMessagesByIdempotencyKey(currentTurn, []string{"after-turn"}))
	afterTurn, err := old.Sink.IsMessageRetracted(currentTurn, "after-turn")
	require.NoError(t, err)
	assert.True(t, afterTurn)

	captured := old.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	replacement := root.AcquireChildStateSink(childID)
	require.NotNil(t, replacement.Sink)
	require.False(t, old.RetainTranscriptSpans(), "a superseded lease cannot retain the live tracker")
	require.True(t, replacement.RetainTranscriptSpans())
	messageRetractionPersist(t, replacement.Sink, "replacement")
	frozen := old.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	for _, retired := range []agent.MessageContent{captured, frozen} {
		require.NoError(t, old.Sink.RetractMessagesByIdempotencyKey(retired, []string{"replacement"}))
	}
	row, err := svc.Queries.GetMessageByIdempotencyKey(t.Context(), db.GetMessageByIdempotencyKeyParams{
		AgentID: childID, AgentSessionID: "s-1", IdempotencyKey: "replacement",
	})
	require.NoError(t, err, "a retired lease must leave its successor's row intact")
	assert.Positive(t, row.Seq)
}

func startClaudeRetractionFixture(t *testing.T, svc *Service, sink agent.ProviderServices) (agent.Agent, agent.ProviderServices) {
	t.Helper()
	ctx := t.Context()
	row, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	var running agent.Agent
	var ordered agent.ProviderServices
	_, err = svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: row.ID, WorkingDir: row.WorkingDir, AgentProvider: row.AgentProvider,
	}, sink, func(ctx context.Context, opts agent.Options, services agent.ProviderServices) (agent.Agent, error) {
		ordered = services
		var startErr error
		running, startErr = claudetest.StartEcho(ctx, opts, services)
		return running, startErr
	})
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAgent(row.ID) })
	queue := agent.OutputQueueFor(ordered)
	require.NotNil(t, queue)
	queue.StopRetries()
	return running, ordered
}

func claudeRetractionToolFrames(tool, keyPrefix, parent string) (string, string) {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"session_id":"s-1","parent_tool_use_id":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":%q}]}}`, keyPrefix+"-open", parent, keyPrefix, tool),
		fmt.Sprintf(`{"type":"user","uuid":%q,"session_id":"s-1","parent_tool_use_id":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}}`, keyPrefix+"-close", parent, keyPrefix)
}

func checkClaudeRetractionStoredGeometry(t *testing.T, opener, closer db.Message, closerDepth int64) {
	t.Helper()
	assert.Zero(t, opener.Depth)
	assert.Equal(t, "[]", opener.SpanLines)
	assert.Positive(t, opener.SpanColor)
	assert.Equal(t, closerDepth, closer.Depth)
	assert.Equal(t, opener.SpanColor, closer.SpanColor)
	var lines []SpanLine
	require.NoError(t, json.Unmarshal([]byte(closer.SpanLines), &lines))
	require.NotEmpty(t, lines)
	assert.Equal(t, SpanLineConnectorEnd, lines[len(lines)-1].Type)
}

func TestClaudeRetractionRollsBackAndRetriesBeforeReplacement(t *testing.T) {
	t.Parallel()
	svc, sink, watcher := messageRetractionFixture(t)
	a, ordered := startClaudeRetractionFixture(t, svc, sink)
	ctx := t.Context()
	read := func(key string) (db.Message, error) {
		return svc.Queries.GetMessageByIdempotencyKey(ctx, db.GetMessageByIdempotencyKeyParams{
			AgentID: "agent-1", AgentSessionID: "s-1", IdempotencyKey: key,
		})
	}
	oldOpen, oldClose := claudeRetractionToolFrames("AskUserQuestion", "old", "")
	for _, frame := range []string{
		`{"type":"assistant","uuid":"old-text","session_id":"s-1","message":{"content":[{"type":"text","text":"first"}]}}`,
		oldOpen, oldClose,
	} {
		a.HandleOutput([]byte(frame))
	}
	before, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1"})
	require.NoError(t, err)
	require.Len(t, before, 3)
	require.NotNil(t, before[2].MarkType, "the evicted result owns a stored mark")
	_, err = svc.DB.ExecContext(ctx, `CREATE TRIGGER refuse_second_claude_delete BEFORE DELETE ON messages
		WHEN OLD.idempotency_key = 'old-open' BEGIN SELECT RAISE(ABORT, 'the second deletion failed'); END`)
	require.NoError(t, err)
	a.HandleOutput([]byte(`{"type":"assistant","uuid":"replacement","session_id":"s-1","supersedes":["old-close","old-open","old-text"],"message":{"content":[{"type":"text","text":"replacement"}]}}`))
	open, closeTool := claudeRetractionToolFrames("Read", "next", "")
	a.HandleOutput([]byte(open))
	a.HandleOutput([]byte(closeTool))
	for _, row := range before {
		persisted, err := read(row.IdempotencyKey)
		require.NoError(t, err, "rollback must restore every prior row")
		assert.Equal(t, row.Seq, persisted.Seq)
		retracted, err := ordered.IsMessageRetracted(ordered.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{}), row.IdempotencyKey)
		require.NoError(t, err)
		assert.False(t, retracted, "rollback must also restore the receipt state")
	}
	assert.Empty(t, retractedSeqs(t, watcher))
	_, err = read("replacement")
	require.ErrorIs(t, err, sql.ErrNoRows)

	_, err = svc.DB.ExecContext(ctx, "DROP TRIGGER refuse_second_claude_delete")
	require.NoError(t, err)
	require.NoError(t, agent.FlushPendingOutput(ordered))
	for _, row := range before {
		_, err := read(row.IdempotencyKey)
		require.ErrorIs(t, err, sql.ErrNoRows)
	}
	marks, err := svc.Queries.ListMessageMarksByAgentID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Empty(t, marks, "the removed result also leaves the mark query")
	removedOpener, err := ordered.ReadToolRequest("old")
	require.NoError(t, err)
	assert.Nil(t, removedOpener)
	removedResult, err := ordered.ReadToolResult("old")
	require.NoError(t, err)
	assert.Nil(t, removedResult)

	replacement, err := read("replacement")
	require.NoError(t, err)
	assert.Greater(t, replacement.Seq, before[2].Seq)
	opener, err := read("next-open")
	require.NoError(t, err)
	closer, err := read("next-close")
	require.NoError(t, err)
	checkClaudeRetractionStoredGeometry(t, opener, closer, 0)

	var liveOrder []string
	for _, stream := range watcher.streamsSnapshot() {
		var event leapmuxv1.WatchEventsResponse
		require.NoError(t, proto.Unmarshal(stream.GetPayload(), &event))
		if event.GetAgentEvent().GetMessagesRetracted() != nil {
			liveOrder = append(liveOrder, "retraction")
		}
		switch event.GetAgentEvent().GetAgentMessage().GetSeq() {
		case replacement.Seq:
			liveOrder = append(liveOrder, "replacement")
		case opener.Seq:
			liveOrder = append(liveOrder, "opener")
		case closer.Seq:
			liveOrder = append(liveOrder, "closer")
		}
	}
	assert.Equal(t, []string{"retraction", "replacement", "opener", "closer"}, liveOrder)
	assert.Equal(t, [][]int64{{before[0].Seq, before[1].Seq, before[2].Seq}}, retractedSeqs(t, watcher))

	a.HandleOutput([]byte(oldOpen))
	assert.Empty(t, requireRootOutputSink(t, svc.Output, "agent-1").currentSpanTracker().ActiveSpans())
	survivingOpen, survivingClose := claudeRetractionToolFrames("Read", "survivor", "")
	a.HandleOutput([]byte(survivingOpen))
	a.HandleOutput([]byte(survivingClose))
	a.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))
	survivingOpener, err := read("survivor-open")
	require.NoError(t, err)
	survivingCloser, err := read("survivor-close")
	require.NoError(t, err)
	checkClaudeRetractionStoredGeometry(t, survivingOpener, survivingCloser, 0)
	divider, err := svc.Queries.GetLatestMessageByAgentID(ctx, "agent-1")
	require.NoError(t, err)
	supplement, err := msgcodec.Decompress(divider.SupplementalContent, divider.SupplementalContentCompression)
	require.NoError(t, err)
	decoded, err := agent.DecodeMessageSupplement(nil, supplement)
	require.NoError(t, err)
	var counts map[string]int
	require.NoError(t, json.Unmarshal(decoded.Metadata, &counts))
	assert.Equal(t, 3, counts[contracts.MessageMetadataFieldToolUses], "the old tool and two survivors count once each")

}

func TestClaudeQueuedChildFramesKeepTheRetiredLeaseGeometry(t *testing.T) {
	t.Parallel()
	svc, root, _ := messageRetractionFixture(t)
	a, _ := startClaudeRetractionFixture(t, svc, root)
	const spawnSpanID = "spawn"
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task","tool_use_id":"spawn","task_type":"local_agent","prompt":"work"}`))
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask, SpawnSpanID: spawnSpanID, ProviderChildKey: "task",
	})
	require.NoError(t, err)
	child := root.ChildSink(childID)
	queue := agent.OutputQueueFor(child)
	require.NotNil(t, queue)
	queue.StopRetries()
	blocker := agent.NewOutputOwner()
	queue.Append(agent.OutputWrite{Owner: blocker, Label: "block child storage", Run: func() error {
		return fmt.Errorf("child storage is unavailable")
	}})
	open, closeTool := claudeRetractionToolFrames("Read", "old-child", spawnSpanID)
	a.HandleOutput([]byte(open))
	a.HandleOutput([]byte(closeTool))

	successor := root.AcquireChildStateSink(childID)
	require.NotNil(t, successor.Sink)
	successor.Sink.OpenSpan("successor", "")
	queue.ClearOwner(blocker)
	require.NoError(t, agent.FlushPendingOutput(child))
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: childID})
	require.NoError(t, err)
	require.Len(t, rows, 3, "the opening prompt and both retired frames persist")
	opener, closer := rows[1], rows[2]
	assert.Equal(t, "old-child-open", opener.IdempotencyKey)
	assert.Equal(t, "old-child-close", closer.IdempotencyKey)
	assert.Empty(t, opener.AgentSessionID, "the child keeps its blank stored session")
	assert.Empty(t, closer.AgentSessionID)
	assert.True(t, opener.TranscriptOnly)
	assert.True(t, closer.TranscriptOnly)
	checkClaudeRetractionStoredGeometry(t, opener, closer, 1)
	live := requireChildOutputSink(t, requireRootOutputSink(t, svc.Output, "agent-1"), childID).currentSpanTracker().ActiveSpans()
	require.Len(t, live, 1)
	assert.Equal(t, "successor", live[0].SpanID)
}

func TestClaudeQueuedRootResultKeepsGeometryAcrossContextReset(t *testing.T) {
	t.Parallel()
	svc, root, _ := messageRetractionFixture(t)
	a, ordered := startClaudeRetractionFixture(t, svc, root)
	open, closeTool := claudeRetractionToolFrames("Read", "old-root", "")
	a.HandleOutput([]byte(open))
	queue := agent.OutputQueueFor(ordered)
	require.NotNil(t, queue)
	blocker := agent.NewOutputOwner()
	queue.Append(agent.OutputWrite{Owner: blocker, Label: "block root storage", Run: func() error {
		return fmt.Errorf("root storage is unavailable")
	}})
	a.HandleOutput([]byte(closeTool))

	svc.Output.ResetSpanTracker("agent-1")
	successor := newTestRootOutputSink(t, svc.Output, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	successor.OpenSpan("successor", "")
	queue.ClearOwner(blocker)
	require.NoError(t, agent.FlushPendingOutput(ordered))
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "agent-1"})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "old-root-close", rows[1].IdempotencyKey)
	assert.True(t, rows[1].TranscriptOnly)
	checkClaudeRetractionStoredGeometry(t, rows[0], rows[1], 0)
	live := requireRootOutputSink(t, svc.Output, "agent-1").currentSpanTracker().ActiveSpans()
	require.Len(t, live, 1)
	assert.Equal(t, "successor", live[0].SpanID)
}

func TestClaudePhysicalRootReplacementSeparatesTheDurableChildTrackers(t *testing.T) {
	t.Parallel()
	svc, root, _ := messageRetractionFixture(t)
	ctx := t.Context()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "spawn", ProviderChildKey: "child",
	})
	require.NoError(t, err)
	prior := root.AcquireChildStateSink(childID)
	require.True(t, prior.RetainTranscriptSpans())
	prior.Sink.OpenSpan("old-span", "")
	content := prior.CaptureMessage(agent.MessageContent{
		Original: []byte(`{"type":"user"}`), IdempotencyKey: "old-close", WriteReceipt: agent.NewTranscriptWriteReceipt(),
	}, agent.SpanInfo{})
	oldSpans := agent.TranscriptFrameSpans(content, prior.Sink)

	successor := newTestRootOutputSink(t, svc.Output, "agent-1", provider)
	next := successor.AcquireChildStateSink(childID)
	require.True(t, next.RetainTranscriptSpans())
	live := requireChildOutputSink(t, requireRootOutputSink(t, svc.Output, "agent-1"), childID).currentSpanTracker()
	assert.Empty(t, live.ActiveSpans(), "the new physical child starts with clean geometry")
	next.Sink.OpenSpan("new-span", "")
	captured, err := agent.CaptureTranscriptFrame(prior.Sink, content, agent.SpanInfo{SpanID: "old-span", Closing: true})
	require.NoError(t, err)
	require.NoError(t, captured.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	oldSpans.CloseSpan("old-span")
	rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: childID})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	var lines []SpanLine
	require.NoError(t, json.Unmarshal([]byte(rows[0].SpanLines), &lines))
	require.Len(t, lines, 1, "the old result keeps only its original connector")
	assert.Equal(t, SpanLineConnectorEnd, lines[0].Type)
	require.Len(t, live.ActiveSpans(), 1)
	assert.Equal(t, "new-span", live.ActiveSpans()[0].SpanID)
}

func TestClaudeDelayedRestartRoutesTheRecipientAfterItsDeliveredPrompt(t *testing.T) {
	t.Parallel()
	svc, sink, _ := messageRetractionFixture(t)
	a, ordered := startClaudeRetractionFixture(t, svc, sink)
	for _, frame := range []string{
		`{"type":"system","subtype":"task_started","task_id":"target","tool_use_id":"spawn","task_type":"local_agent","prompt":"original"}`,
		`{"type":"assistant","uuid":"prior","session_id":"s-1","parent_tool_use_id":"spawn","message":{"content":[{"type":"tool_use","id":"prior-call","name":"Read"}]}}`,
		`{"type":"system","subtype":"task_notification","task_id":"target","status":"completed"}`,
	} {
		a.HandleOutput([]byte(frame))
	}
	queue := agent.OutputQueueFor(ordered)
	blocker := agent.NewOutputOwner()
	queue.Append(agent.OutputWrite{Owner: blocker, Label: "block sender", Run: func() error {
		return fmt.Errorf("the sender store is unavailable")
	}})
	for _, frame := range []string{
		`{"type":"assistant","uuid":"send","session_id":"s-1","message":{"content":[{"type":"tool_use","id":"send-call","name":"SendMessage","input":{"to":"target","message":"continue"}}]}}`,
		`{"type":"system","subtype":"task_started","task_id":"target","tool_use_id":"send-call","task_type":"local_agent","prompt":"continue"}`,
		`{"type":"assistant","uuid":"after-start","session_id":"s-1","parent_tool_use_id":"spawn","message":{"content":[{"type":"text","text":"continued"}]}}`,
	} {
		a.HandleOutput([]byte(frame))
	}
	queue.ClearOwner(blocker)
	require.NoError(t, agent.FlushPendingOutput(ordered))
	childID, _, found, err := sink.LookupBackgroundTask("target")
	require.NoError(t, err)
	require.True(t, found)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: childID})
	require.NoError(t, err)
	require.Len(t, rows, 4)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[2].Source)
	assert.Equal(t, "after-start", rows[3].IdempotencyKey)
	assert.False(t, rows[3].TranscriptOnly)
	assert.Equal(t, "[]", rows[3].SpanLines, "the restarted run uses its own tracker")
}

func TestClaudeFallbackNoticeWaitsForTheChildRetractionToCommit(t *testing.T) {
	t.Parallel()
	svc, root, watcher := messageRetractionFixture(t)
	a, _ := startClaudeRetractionFixture(t, svc, root)
	ctx := t.Context()
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task","tool_use_id":"spawn","task_type":"local_agent","prompt":"Inspect."}`))
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "spawn", ProviderChildKey: "task",
	})
	require.NoError(t, err)
	registerAgentWatch(svc, watcher.channelID, childID, leapmuxv1.WatchMode_WATCH_MODE_FULL, watcher)
	a.HandleOutput([]byte(`{"type":"assistant","uuid":"target","session_id":"s-1","parent_tool_use_id":"spawn","message":{"content":[{"type":"text","text":"partial child answer"}]}}`))
	child := root.ChildSink(childID)
	queue := agent.OutputQueueFor(child)
	require.NotNil(t, queue)
	queue.StopRetries()
	blocker := agent.NewOutputOwner()
	queue.Append(agent.OutputWrite{Owner: blocker, Label: "block child storage", Run: func() error {
		return fmt.Errorf("child storage is unavailable")
	}})
	_, err = svc.DB.ExecContext(ctx, `CREATE TRIGGER refuse_claude_child_delete BEFORE DELETE ON messages
		WHEN OLD.idempotency_key = 'target' BEGIN SELECT RAISE(ABORT, 'child deletion failed'); END`)
	require.NoError(t, err)

	readRoot := func() []db.Message {
		t.Helper()
		rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1"})
		require.NoError(t, err)
		return rows
	}
	before := len(readRoot())

	a.HandleOutput([]byte(`{"type":"system","subtype":"model_refusal_fallback","scope":"local","uuid":"fallback","session_id":"s-1","retracted_message_uuids":["target"]}`))
	assert.Len(t, readRoot(), before, "a child storage failure keeps the root notice waiting")

	queue.ClearOwner(blocker)
	assert.ErrorContains(t, agent.FlushPendingOutput(child), "child deletion failed")
	assert.Len(t, readRoot(), before, "a failed deletion still keeps the root notice waiting")

	_, err = svc.DB.ExecContext(ctx, "DROP TRIGGER refuse_claude_child_delete")
	require.NoError(t, err)
	require.NoError(t, agent.FlushPendingOutput(child))
	require.Len(t, retractedSeqs(t, watcher), 1)
	rows := readRoot()
	require.Len(t, rows, before+1)
	last := rows[len(rows)-1]
	notice, err := msgcodec.Decompress(last.Content, last.ContentCompression)
	require.NoError(t, err)
	assert.Contains(t, string(notice), "model_refusal_fallback")
}

func TestClaudeChildHandbackReportRetriesBeforeLaterChildOutput(t *testing.T) {
	t.Parallel()
	svc, root, _ := messageRetractionFixture(t)
	a, _ := startClaudeRetractionFixture(t, svc, root)
	ctx := t.Context()
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task","tool_use_id":"spawn","task_type":"local_agent","prompt":"Inspect."}`))
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "spawn", ProviderChildKey: "task",
	})
	require.NoError(t, err)
	child := root.ChildSink(childID)
	childQueue := agent.OutputQueueFor(child)
	require.NotNil(t, childQueue)
	childQueue.StopRetries()
	_, err = svc.DB.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER reject_child_report BEFORE INSERT ON messages
		WHEN NEW.agent_id <> 'agent-1' AND NEW.source = %d
		BEGIN SELECT RAISE(ABORT, 'child report write failed'); END`, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX))
	require.NoError(t, err)

	a.HandleOutput([]byte(`{"type":"assistant","uuid":"handback-frame","session_id":"s-1","parent_tool_use_id":"spawn","task_description":"Reviewer","message":{"content":[{"type":"tool_use","id":"handback","name":"SubagentHandback","input":{"message":"Findings"}}]}}`))
	a.HandleOutput([]byte(`{"type":"assistant","uuid":"later","session_id":"s-1","parent_tool_use_id":"spawn","message":{"content":[{"type":"text","text":"a later message"}]}}`))
	before, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: childID})
	require.NoError(t, err)
	assert.Len(t, before, 1, "the later assistant row waits behind the failed report")

	_, err = svc.DB.ExecContext(ctx, "DROP TRIGGER reject_child_report")
	require.NoError(t, err)
	require.NoError(t, agent.FlushPendingOutput(child))
	rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: childID})
	require.NoError(t, err)
	require.Len(t, rows, 3, "the original prompt, report, and later message each appear once")
	report, err := msgcodec.Decompress(rows[1].Content, rows[1].ContentCompression)
	require.NoError(t, err)
	assert.Contains(t, string(report), "Findings")
	assert.Empty(t, rows[1].AgentSessionID, "the admitted virtual child had no native session")
	assert.Equal(t, "later", rows[2].IdempotencyKey)
}

func TestClaudeRetiredSpanCannotClearTheSuccessorTail(t *testing.T) {
	t.Parallel()
	svc, root, _ := messageRetractionFixture(t)
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask, SpawnSpanID: "spawn", ProviderChildKey: "child",
	})
	require.NoError(t, err)
	first := root.AcquireChildStateSink(childID)
	require.True(t, first.RetainTranscriptSpans())
	first.Sink.OpenSpan("same-span", "")
	captured := first.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	retained := agent.TranscriptFrameSpans(captured, first.Sink)

	successor := root.AcquireChildStateSink(childID)
	require.True(t, successor.RetainTranscriptSpans())
	successor.Sink.OpenSpan("same-span", "")
	successor.Sink.ReportProgress(agent.OutputTailProgress("same-span", "successor output", false))
	child := requireChildOutputSink(t, requireRootOutputSink(t, svc.Output, "agent-1"), childID)
	readTail := func() string {
		child.progress.mu.Lock()
		defer child.progress.mu.Unlock()
		tail := child.progress.tails["same-span"]
		if tail == nil {
			return ""
		}
		return tail.text
	}
	require.Equal(t, "successor output", readTail())
	retained.CloseSpan("same-span")

	assert.Equal(t, "successor output", readTail())
	active := child.currentSpanTracker().ActiveSpans()
	require.Len(t, active, 1)
	assert.Equal(t, "same-span", active[0].SpanID)
}

func TestRetractionWithoutATransactionLeavesRowsAndReceiptsUnchanged(t *testing.T) {
	t.Parallel()
	svc, root, _ := messageRetractionFixture(t)
	messageRetractionPersist(t, root, "target")
	capture := root.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	ctx := t.Context()
	_, err := svc.DB.ExecContext(ctx, `CREATE TRIGGER reject_nontransactional_delete BEFORE DELETE ON messages
		WHEN OLD.idempotency_key = 'target' BEGIN SELECT RAISE(ABORT, 'deletion refused'); END`)
	require.NoError(t, err)
	store := svc.Output.db
	svc.Output.db = nil
	defer func() { svc.Output.db = store }()

	require.Error(t, root.RetractMessagesByIdempotencyKey(capture, []string{"target"}))
	present, err := root.IsMessageRetracted(capture, "target")
	require.NoError(t, err)
	assert.False(t, present, "a missing transaction must prevent any receipt write")
	row, err := svc.Queries.GetMessageByIdempotencyKey(ctx, db.GetMessageByIdempotencyKeyParams{
		AgentID: "agent-1", AgentSessionID: "s-1", IdempotencyKey: "target",
	})
	require.NoError(t, err)
	assert.Positive(t, row.Seq)
}

func TestClaudeRetiredFramePersistsWithoutLiveTurnEffects(t *testing.T) {
	t.Parallel()
	svc, root, watcher := messageRetractionFixture(t)
	ctx := t.Context()
	messageRetractionPersist(t, root, "evicted")
	claim := root.CaptureMessage(agent.MessageContent{}, agent.SpanInfo{})
	root.OpenSpan("call", "")
	admitted := root.CaptureMessage(agent.MessageContent{
		Original:     []byte(`{"type":"result","subtype":"success"}`),
		WriteReceipt: agent.NewTranscriptWriteReceipt(),
	}, agent.SpanInfo{})
	retainedSpans := agent.TranscriptFrameSpans(admitted, root)
	require.NotNil(t, retainedSpans)
	require.True(t, agent.TranscriptFrameIsCurrent(admitted))
	root.BroadcastSessionInfo(map[string]any{contracts.SessionInfoKeyCompaction: map[string]any{}})

	// NoteAgentProcessExited retires the source before it resets activity.
	// Persist at that boundary while the original activity scope still exists.
	require.Equal(t, []string{"agent-1"}, svc.Output.invalidateExitedProcessSessionInfo("agent-1", root))
	assert.False(t, agent.TranscriptFrameIsCurrent(admitted))
	result, err := agent.CaptureTranscriptFrame(root, admitted, agent.SpanInfo{SpanID: "call", Closing: true})
	require.NoError(t, err)
	require.NoError(t, result.PersistTurnEnd())
	retainedSpans.CloseSpan("call")
	require.NoError(t, root.RetractMessagesByIdempotencyKey(claim, []string{"evicted"}))

	rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1"})
	require.NoError(t, err)
	require.Len(t, rows, 1, "the retained result survives and the admitted retraction removes its target")
	assert.True(t, rows[0].TranscriptOnly)
	assert.Equal(t, "s-1", rows[0].AgentSessionID)
	var lines []SpanLine
	require.NoError(t, json.Unmarshal([]byte(rows[0].SpanLines), &lines))
	require.Len(t, lines, 1)
	assert.Equal(t, SpanLineConnectorEnd, lines[0].Type)
	assert.Empty(t, requireRootOutputSink(t, svc.Output, "agent-1").currentSpanTracker().ActiveSpans())
	assert.Equal(t, [][]int64{{1}}, retractedSeqs(t, watcher))
	for _, stream := range watcher.streamsSnapshot() {
		var event leapmuxv1.WatchEventsResponse
		require.NoError(t, proto.Unmarshal(stream.GetPayload(), &event))
		assert.Nil(t, event.GetAgentEvent().GetTurnEnd())
	}
}
