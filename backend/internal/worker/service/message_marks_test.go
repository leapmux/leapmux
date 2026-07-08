package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// seedMark persists one message with the given mark type and returns its seq.
func seedMark(t *testing.T, svc *Context, agentID, id string, mark leapmuxv1.MarkType) int64 {
	t.Helper()
	seq, err := createMessageRow(context.Background(), svc.Queries, db.CreateMessageParams{
		ID:            id,
		AgentID:       agentID,
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte("hi"),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		MarkType:      mark,
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)
	return seq
}

func listMarks(t *testing.T, d *channel.Dispatcher, agentID string) *leapmuxv1.ListMessageMarksResponse {
	t.Helper()
	w := newTestWriter()
	dispatch(d, "ListMessageMarks", &leapmuxv1.ListMessageMarksRequest{AgentId: agentID}, w)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListMessageMarksResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	return &resp
}

// TestListMessageMarks_ReturnsMarkedSeqsAndRange asserts the handler returns only
// the marked rows (unmarked ones excluded), ascending, each with its type, and the
// whole-history min/max seq -- including seqs of unmarked rows in the range.
func TestListMessageMarks_ReturnsMarkedSeqsAndRange(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	// seq 1: user message (marked), 2: unmarked, 3: control response (marked),
	// 4: unmarked. Marks are a subset; the range spans all four.
	seq1 := seedMark(t, svc, "agent-1", "m1", leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE)
	seedMark(t, svc, "agent-1", "m2", leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED)
	seq3 := seedMark(t, svc, "agent-1", "m3", leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE)
	seq4 := seedMark(t, svc, "agent-1", "m4", leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED)

	resp := listMarks(t, d, "agent-1")
	require.Len(t, resp.GetMarks(), 2)
	assert.Equal(t, seq1, resp.GetMarks()[0].GetSeq())
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, resp.GetMarks()[0].GetType())
	assert.Equal(t, seq3, resp.GetMarks()[1].GetSeq())
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, resp.GetMarks()[1].GetType())
	assert.Equal(t, seq1, resp.GetMinSeq(), "min spans the whole history, incl. unmarked rows")
	assert.Equal(t, seq4, resp.GetMaxSeq(), "max spans the whole history, incl. unmarked rows")

	// Deleting the last (unmarked) row lowers max_seq but leaves the marks unchanged.
	_, err := svc.Queries.DeleteMessageByAgentAndID(ctx, db.DeleteMessageByAgentAndIDParams{AgentID: "agent-1", ID: "m4"})
	require.NoError(t, err)
	resp = listMarks(t, d, "agent-1")
	require.Len(t, resp.GetMarks(), 2)
	assert.Equal(t, seq3, resp.GetMaxSeq(), "max_seq drops to the new highest live seq after a tail delete")

	// Deleting a marked row removes it from the marks list.
	_, err = svc.Queries.DeleteMessageByAgentAndID(ctx, db.DeleteMessageByAgentAndIDParams{AgentID: "agent-1", ID: "m3"})
	require.NoError(t, err)
	resp = listMarks(t, d, "agent-1")
	require.Len(t, resp.GetMarks(), 1)
	assert.Equal(t, seq1, resp.GetMarks()[0].GetSeq())
}

// TestListMessageMarks_MinSeqAfterLeadingDelete asserts min_seq drifts above 1
// once the leading rows are deleted -- the exact case the RPC's min_seq exists for
// (seqs are never reused, so a deleted prefix is gone permanently).
func TestListMessageMarks_MinSeqAfterLeadingDelete(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	seedMark(t, svc, "agent-1", "m1", leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE)
	seq2 := seedMark(t, svc, "agent-1", "m2", leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE)

	_, err := svc.Queries.DeleteMessageByAgentAndID(ctx, db.DeleteMessageByAgentAndIDParams{AgentID: "agent-1", ID: "m1"})
	require.NoError(t, err)

	resp := listMarks(t, d, "agent-1")
	assert.Equal(t, seq2, resp.GetMinSeq(), "min_seq follows the surviving oldest row, not 1")
	require.Len(t, resp.GetMarks(), 1)
	assert.Equal(t, seq2, resp.GetMarks()[0].GetSeq())
}

// TestListMessageMarks_EmptyAgent asserts an agent with no messages yields no marks
// and a 0/0 range (the "genuinely empty" sentinel, distinct from -1 indeterminate).
func TestListMessageMarks_EmptyAgent(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))

	resp := listMarks(t, d, "agent-1")
	assert.Empty(t, resp.GetMarks())
	require.NotNil(t, resp.MinSeq, "an empty (non-error) agent reports a PRESENT 0 range, not unset")
	require.NotNil(t, resp.MaxSeq, "an empty (non-error) agent reports a PRESENT 0 range, not unset")
	assert.Equal(t, int64(0), resp.GetMinSeq())
	assert.Equal(t, int64(0), resp.GetMaxSeq())
}

// TestListMessageMarks_ClosedAgent_ReturnsEmptyWithPresentRange mirrors ListAgentMessages
// (which serves a closed agent no history): the response carries no marks. Critically the
// seq range is PRESENT and 0 -- NOT left unset -- so the client can tell a closed agent
// ("no rail") apart from a DB-error indeterminate range ("retry"). An unset range would
// drive ~5 retry RPCs per closed-tab view; a present-0 range seeds the rail loaded (and it
// stays hidden over the empty window) and ends the retry chain.
func TestListMessageMarks_ClosedAgent_ReturnsEmptyWithPresentRange(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: "/tmp", HomeDir: "/tmp",
	}))
	seedMark(t, svc, "agent-1", "m1", leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE)
	require.NoError(t, svc.Queries.CloseAgent(ctx, "agent-1"))

	resp := listMarks(t, d, "agent-1")
	assert.Empty(t, resp.GetMarks())
	require.NotNil(t, resp.MinSeq, "closed agent must report a PRESENT range, not the DB-error indeterminate (unset) signal")
	require.NotNil(t, resp.MaxSeq, "closed agent must report a PRESENT range, not the DB-error indeterminate (unset) signal")
	assert.Equal(t, int64(0), resp.GetMinSeq())
	assert.Equal(t, int64(0), resp.GetMaxSeq())
}

// TestListMessageMarks_UnknownAgent_Errors asserts an inaccessible/unknown agent id
// produces an error, not a success response, via requireAccessibleAgent.
func TestListMessageMarks_UnknownAgent_Errors(t *testing.T) {
	_, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	w := newTestWriter()
	dispatch(d, "ListMessageMarks", &leapmuxv1.ListMessageMarksRequest{AgentId: "nope"}, w)
	assert.Empty(t, w.responses, "an unknown agent must not produce a success response")
	require.NotEmpty(t, w.errors, "an unknown agent must produce an error")
}

// TestSendAgentMessage_PersistsUserMessageMark asserts a genuine user send is
// persisted with mark_type=USER_MESSAGE (so the rail dots it), and that the mark
// surfaces through ListMessageMarks. The synthetic-prompt path stays unmarked.
func TestSendAgentMessage_PersistsUserMessageMark(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{AgentId: "agent-1", Content: "hello"}, w)
	require.Empty(t, w.errors)

	msgs, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, msgs[0].MarkType, "a user send must be marked")

	resp := listMarks(t, d, "agent-1")
	require.Len(t, resp.GetMarks(), 1)
	assert.Equal(t, msgs[0].Seq, resp.GetMarks()[0].GetSeq())
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, resp.GetMarks()[0].GetType())

	// A synthetic prompt (auto-continue / plan execution) is byte-identical on the
	// wire but is NOT a human input, so it must stay unmarked.
	svc.sendSyntheticUserMessage("agent-1", "please continue", leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED)
	msgs, err = svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED, msgs[1].MarkType, "a synthetic prompt must not be marked")

	resp = listMarks(t, d, "agent-1")
	assert.Len(t, resp.GetMarks(), 1, "the synthetic prompt adds no rail dot")
}

// TestPersistAndBroadcast_ThreadsMarkType covers the shared persist path used by
// the Claude transcript ingestion AND the structured control-response row: a SpanInfo
// MarkType must land in both the persisted row and the live broadcast. This is the
// coverage for the control-response CONTROL_RESPONSE mark, whose write site routes
// through sink.PersistMessage -> persistAndBroadcast.
func TestPersistAndBroadcast_ThreadsMarkType(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, withWorkspaces("ws-1"))
	sink := setupAgentWithWatcher(t, svc, w, "agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	require.NoError(t, sink.PersistMessage(
		leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		[]byte(`{"isSynthetic":true,"controlResponse":{"provider":"CLAUDE_CODE","requestId":"r","response":{"behavior":"allow"}}}`),
		agent.SpanInfo{MarkType: leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE},
	))

	rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType, "persisted row must carry the mark")

	// The broadcast must carry the same mark so live watchers dot it without a refetch.
	var broadcastMark leapmuxv1.MarkType
	found := false
	for _, s := range w.streamsSnapshot() {
		ev := decodeWatchAgentEvent(t, s)
		if msg := ev.GetAgentMessage(); msg != nil && msg.GetId() == rows[0].ID {
			broadcastMark = msg.GetMarkType()
			found = true
		}
	}
	require.True(t, found, "the persisted row must be broadcast")
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, broadcastMark, "broadcast must carry the mark")
}

// TestPersistSyntheticUserMessage_LeavesInterruptNoticeUnmarked asserts the synthetic-user-message
// writer -- now used ONLY for the interrupt notice, since genuine control answers persist through
// persistControlResponseRow -- writes an UNSPECIFIED-mark row so the interrupt draws no rail dot.
func TestPersistSyntheticUserMessage_LeavesInterruptNoticeUnmarked(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	svc.persistSyntheticUserMessage("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "[Request interrupted by user]")

	rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, rows[0].Source)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED, rows[0].MarkType, "the interrupt notice stays unmarked")
}

// TestPersistControlResponseAnswerRows_SingleStructuredRow pins the structural "exactly one answer
// row" guarantee. A non-self-displayed provider gets exactly one marked structured row; a
// self-displayed answer in the context-clearing ExitPlanMode case (its own tool_result is about to
// be wiped) gets exactly one marked structured row too -- never a second echo; a self-displayed
// answer NOT clearing context gets none (its ingested tool_result owns the mark).
func TestPersistControlResponseAnswerRows_SingleStructuredRow(t *testing.T) {
	ctx := context.Background()

	mk := func(selfDisplayed, clear bool) controlResponsePlan {
		var plan controlResponsePlan
		plan.requestMeta.RequestID = "req-1"
		plan.requestMeta.Loaded = true
		plan.hasDecision = true
		plan.decision.Response.Response.Behavior = agent.ControlBehaviorAllow
		plan.decision.ClearContext = clear
		plan.resolution.PlanModeControl = agent.PlanModeControlExit
		plan.resolution.SelfDisplayed = selfDisplayed
		plan.resolution.Content = []byte(`{"response":{"request_id":"req-1","response":{"behavior":"allow"}}}`)
		return plan
	}

	t.Run("non-self-displayed persists one marked structured row", func(t *testing.T) {
		svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
		svc.persistControlResponseAnswerRow("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, mk(false, false))
		rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
	})

	t.Run("self-displayed clear-context persists exactly one marked structured row", func(t *testing.T) {
		svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
		svc.persistControlResponseAnswerRow("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, mk(true, true))
		rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
		require.NoError(t, err)
		require.Len(t, rows, 1, "the wiped tool_result's mark moves to the single structured row, never a second echo")
		assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, rows[0].MarkType)
	})

	t.Run("self-displayed without clear-context persists no row", func(t *testing.T) {
		svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
		svc.persistControlResponseAnswerRow("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, mk(true, false))
		rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
		require.NoError(t, err)
		require.Empty(t, rows, "the ingested tool_result owns the mark; no synthetic row")
	})
}

func TestReplayAgentCatchUp_ReplaysControlRequestAgentProvider(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID:    "agent-1",
		RequestID:  "request-1",
		Payload:    []byte(`{"type":"permission","id":"request-1"}`),
		ClaimToken: "instance-token-1",
	}))
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	svc.replayAgentCatchUp(channel.NewSender(w), &leapmuxv1.WatchAgentEntry{AgentId: "agent-1"}, dbAgent, nil)

	var replayed *leapmuxv1.AgentControlRequest
	for _, stream := range w.streamsSnapshot() {
		ev := decodeWatchAgentEvent(t, stream)
		if cr := ev.GetControlRequest(); cr != nil {
			replayed = cr
			break
		}
	}
	require.NotNil(t, replayed, "catch-up replay must include pending control requests")
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, replayed.GetAgentProvider(),
		"replayed control requests must preserve the provider-specific UI/response policy")
	// The replay must carry the stored per-instance claim_token so a reconnecting window echoes it back
	// on its answer and the worker's idempotency claim stays instance-scoped across the reconnect.
	assert.Equal(t, "instance-token-1", replayed.GetClaimToken(),
		"replayed control requests must carry the per-instance claim token the frontend echoes back")
}
