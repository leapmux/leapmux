package service

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestOpenAgentCopiesThePriorWorkerTranscript(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	svc, dispatcher, writer := setupTestService(t)
	defer drainAllInFlight(svc)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, nil
	}
	workingDir := t.TempDir()
	const sourceID = "source-agent"
	const sessionID = "session-a1"
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: sourceID, WorkingDir: workingDir, HomeDir: svc.HomeDir, AgentProvider: provider,
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		ID: sourceID, AgentSessionID: sessionID,
	}))
	for _, message := range []struct {
		id      string
		source  leapmuxv1.MessageSource
		content string
	}{
		{"source-user", leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, `{"type":"user","text":"Remember HALIBUT."}`},
		{"source-answer", leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, `{"type":"assistant","text":"The answer contains HALIBUT."}`},
	} {
		_, err := svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
			ID: message.id, AgentID: sourceID, AgentSessionID: sessionID,
			Source: message.source, Content: []byte(message.content),
			ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			AgentProvider:                  provider,
			CreatedAt:                      sqltime.NewSQLiteTime(time.Now()),
		})
		require.NoError(t, err)
	}
	_, err := svc.Queries.CloseAgent(ctx, sourceID)
	require.NoError(t, err)

	dispatch(dispatcher, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir: workingDir, AgentProvider: provider, AgentSessionId: sessionID,
	}, writer)
	require.Empty(t, writer.errors)
	require.Len(t, writer.responses, 1)
	var response leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &response))
	targetID := response.GetAgent().GetId()
	require.NotEmpty(t, targetID)
	assert.NotEqual(t, sourceID, targetID)
	rows, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: targetID, Seq: 0})
	require.NoError(t, err)
	require.Len(t, rows, 2, "the reopened agent keeps its prior Worker transcript")
	assert.Equal(t, []byte(`{"type":"user","text":"Remember HALIBUT."}`), rows[0].Content)
	assert.Equal(t, []byte(`{"type":"assistant","text":"The answer contains HALIBUT."}`), rows[1].Content)
}

func transcriptResumeService(t *testing.T) (*Service, *channel.Dispatcher) {
	t.Helper()
	svc, dispatcher, _ := setupTestService(t)
	t.Cleanup(func() { drainAllInFlight(svc) })
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, nil
	}
	return svc, dispatcher
}

func seedTranscriptSource(t *testing.T, svc *Service, id, workingDir, sessionID string, provider leapmuxv1.AgentProvider, closed bool) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: id, WorkingDir: workingDir, HomeDir: svc.HomeDir, AgentProvider: provider,
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		ID: id, AgentSessionID: sessionID,
	}))
	if closed {
		_, err := svc.Queries.CloseAgent(ctx, id)
		require.NoError(t, err)
	}
}

func openTranscriptResumeAgent(t *testing.T, dispatcher *channel.Dispatcher, workingDir, sessionID string, provider leapmuxv1.AgentProvider) string {
	t.Helper()
	writer := newTestWriter()
	dispatch(dispatcher, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir: workingDir, AgentProvider: provider, AgentSessionId: sessionID,
	}, writer)
	require.Empty(t, writer.errors)
	require.Len(t, writer.responses, 1)
	var response leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(writer.responses[0].GetPayload(), &response))
	id := response.GetAgent().GetId()
	require.NotEmpty(t, id)
	return id
}

func TestOpenAgentDoesNotCopyUnmatchedWorkerRows(t *testing.T) {
	t.Parallel()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	for _, tc := range []struct {
		name           string
		sourceProvider leapmuxv1.AgentProvider
		otherDir       bool
		seedSource     bool
		noMessage      bool
		otherSession   bool
	}{
		{name: "external native session"},
		{name: "different provider", seedSource: true, sourceProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX},
		{name: "different working directory", seedSource: true, otherDir: true},
		{name: "different native session", seedSource: true, otherSession: true},
		{name: "closed source with no messages", seedSource: true, noMessage: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, dispatcher := transcriptResumeService(t)
			workingDir := t.TempDir()
			if tc.seedSource {
				sourceDir := workingDir
				if tc.otherDir {
					sourceDir = t.TempDir()
				}
				sourceProvider := provider
				if tc.sourceProvider != leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
					sourceProvider = tc.sourceProvider
				}
				sourceSessionID := "session-a1"
				if tc.otherSession {
					sourceSessionID = "session-other"
				}
				seedTranscriptSource(t, svc, "source-agent", sourceDir, sourceSessionID, sourceProvider, true)
				if !tc.noMessage {
					_, err := svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
						ID: "source-message", AgentID: "source-agent", AgentSessionID: sourceSessionID,
						Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte(`{"text":"private source row"}`),
						ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
						SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
						AgentProvider:                  sourceProvider,
						CreatedAt:                      sqltime.NewSQLiteTime(time.Now()),
					})
					require.NoError(t, err)
				}
			}
			targetID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
			rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: targetID, Seq: 0})
			require.NoError(t, err)
			assert.Empty(t, rows)
		})
	}
}

func TestOpenAgentRejectsAnOpenNativeSession(t *testing.T) {
	t.Parallel()
	for _, differentDir := range []bool{false, true} {
		label := "same directory"
		if differentDir {
			label = "different directory"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			svc, dispatcher := transcriptResumeService(t)
			provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
			workingDir := t.TempDir()
			seedTranscriptSource(t, svc, "open-agent", workingDir, "session-a1", provider, false)
			requestedDir := workingDir
			if differentDir {
				requestedDir = t.TempDir()
			}
			writer := newTestWriter()
			dispatch(dispatcher, "OpenAgent", &leapmuxv1.OpenAgentRequest{
				WorkingDir: requestedDir, AgentProvider: provider, AgentSessionId: "session-a1",
			}, writer)
			if len(writer.responses) != 0 {
				t.Fatalf("OpenAgent returned %d responses for an open native session", len(writer.responses))
			}
			require.Len(t, writer.errors, 1)
			assert.Equal(t, int32(codes.FailedPrecondition), writer.errors[0].code)
			assert.Contains(t, writer.errors[0].message, "already open")
			assert.Equal(t, 1, countAgentRows(t, svc), "a refused handle creates no second agent")
		})
	}
}

func TestConcurrentManualResumesClaimOneNativeSession(t *testing.T) {
	t.Parallel()
	svc, dispatcher, _ := setupTestService(t)
	defer drainAllInFlight(svc)
	releaseStartup := make(chan struct{})
	defer close(releaseStartup)
	started := make(chan struct{}, 2)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		started <- struct{}{}
		<-releaseStartup
		return nil, nil
	}
	workingDir := t.TempDir()
	request := &leapmuxv1.OpenAgentRequest{
		WorkingDir:     workingDir,
		AgentProvider:  leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC,
		AgentSessionId: "session-a1",
	}
	first := newTestWriter()
	dispatch(dispatcher, "OpenAgent", request, first)
	require.Empty(t, first.errors)
	require.Len(t, first.responses, 1)
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("the first native startup never reached the held start function")
	}

	second := newTestWriter()
	dispatch(dispatcher, "OpenAgent", request, second)
	if len(second.responses) != 0 {
		t.Fatalf("a second OpenAgent returned %d responses for the held native session", len(second.responses))
	}
	require.Len(t, second.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), second.errors[0].code)
	assert.Equal(t, 1, countAgentRows(t, svc))
	select {
	case <-started:
		t.Fatal("the second provider process started for a claimed native session")
	default:
	}
}

func TestPendingResumeClaimClearsOnNativeConfirmation(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	id := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	row, err := svc.Queries.GetAgentByID(t.Context(), id)
	require.NoError(t, err)
	assert.Empty(t, row.AgentSessionID, "the requested ID is not confirmed history")
	assert.Equal(t, "session-a1", row.PendingResumeSessionID)

	require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
		ID: id, AgentSessionID: "session-a1",
	}))
	row, err = svc.Queries.GetAgentByID(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, "session-a1", row.AgentSessionID)
	assert.Empty(t, row.PendingResumeSessionID)
}

func TestFailedNativeResumeReleasesPendingClaim(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, errors.New("native resume refused")
	}
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	firstID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	drainAllInFlight(svc)
	first, err := svc.Queries.GetAgentByID(t.Context(), firstID)
	require.NoError(t, err)
	assert.Empty(t, first.AgentSessionID)
	assert.Empty(t, first.PendingResumeSessionID)
	assert.Contains(t, first.StartupError, "native resume refused")

	secondID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	assert.NotEqual(t, firstID, secondID)
}

func TestClosingAResumedTabReleasesPendingClaim(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	firstID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	drainAllInFlight(svc)
	_, err := svc.Queries.CloseAgent(t.Context(), firstID)
	require.NoError(t, err)
	first, err := svc.Queries.GetAgentByID(t.Context(), firstID)
	require.NoError(t, err)
	assert.Empty(t, first.PendingResumeSessionID)
	secondID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	assert.NotEqual(t, firstID, secondID)
}

func TestConfirmedNativeSessionRejectsAnotherOpenClaim(t *testing.T) {
	t.Parallel()
	svc, _ := transcriptResumeService(t)
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	seedTranscriptSource(t, svc, "confirmed-owner", t.TempDir(), "session-a1", provider, false)
	err := svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
		ID: "second-owner", WorkingDir: t.TempDir(), HomeDir: svc.HomeDir,
		AgentProvider: provider, PendingResumeSessionID: "session-a1", Resumed: 1,
	})
	require.Error(t, err, "the unique index keeps a confirmed owner exclusive")
}

func TestCopiedWorkerRowsKeepOrderMetadataAndHighWater(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	seedTranscriptSource(t, svc, "source-agent", workingDir, "session-a1", provider, false)
	createdAt := sqltime.NewSQLiteTime(time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC))
	for _, row := range []struct {
		id, text, span, key, sessionID string
		source                         leapmuxv1.MessageSource
		mark                           leapmuxv1.MarkType
	}{
		{"source-1", "first", "span-1", "native-key-1", "session-before-clear", leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE},
		{"source-2", "second", "span-2", "native-key-2", "session-a1", leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED},
	} {
		_, err := svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
			ID: row.id, AgentID: "source-agent", AgentSessionID: row.sessionID,
			Source: row.source, Content: []byte(row.text),
			ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			SupplementalContent:            []byte("supplement-" + row.text),
			SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			IdempotencyKey:                 row.key,
			SpanID:                         row.span,
			ParentSpanID:                   "parent-" + row.span,
			SpanType:                       "Read",
			SpanLines:                      `["line"]`,
			SpanColor:                      23,
			Depth:                          2,
			AgentProvider:                  provider,
			MarkType:                       row.mark,
			CreatedAt:                      createdAt,
		})
		require.NoError(t, err)
	}
	_, err := svc.DB.ExecContext(t.Context(), "UPDATE messages SET supplemental_revision = 7 WHERE id = ?", "source-1")
	require.NoError(t, err)
	_, err = svc.Queries.CloseAgent(t.Context(), "source-agent")
	require.NoError(t, err)

	targetID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: targetID, Seq: 0})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	for i, row := range rows {
		assert.Equal(t, int64(i+1), row.Seq)
		assert.Equal(t, targetID+":"+strconv.Itoa(i+1), row.ID)
		wantSessionID := "session-a1"
		if i == 0 {
			wantSessionID = "session-before-clear"
		}
		assert.Equal(t, wantSessionID, row.AgentSessionID)
		assert.Equal(t, provider, row.AgentProvider)
		assert.Equal(t, createdAt, row.CreatedAt)
		assert.Equal(t, int64(2), row.Depth)
		assert.Equal(t, "parent-span-"+strconv.Itoa(i+1), row.ParentSpanID)
		assert.Equal(t, "Read", row.SpanType)
		assert.Equal(t, `["line"]`, row.SpanLines)
		assert.Equal(t, int64(23), row.SpanColor)
	}
	assert.Equal(t, []byte("first"), rows[0].Content)
	assert.Equal(t, []byte("supplement-first"), rows[0].SupplementalContent)
	assert.Equal(t, "native-key-1", rows[0].IdempotencyKey)
	assert.Equal(t, int64(7), rows[0].SupplementalRevision)
	assert.Equal(t, "span-1", rows[0].SpanID)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, rows[0].MarkType)
	assert.Equal(t, []byte("second"), rows[1].Content)
	assert.Equal(t, "native-key-2", rows[1].IdempotencyKey)

	seq, err := svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
		ID: "new-turn", AgentID: targetID, AgentSessionID: "session-a1",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte("next"),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		AgentProvider:                  provider,
		CreatedAt:                      createdAt,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), seq, "the next message follows the copied tail")

	_, err = svc.Queries.DeleteClosedAgentsBefore(t.Context(), sqltime.SQLiteNullTimeOf(time.Now().Add(time.Hour)))
	require.NoError(t, err)
	_, err = svc.Queries.GetAgentByID(t.Context(), "source-agent")
	assert.ErrorIs(t, err, sql.ErrNoRows)
	retained, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: targetID, Seq: 0})
	require.NoError(t, err)
	assert.Len(t, retained, 3, "deleting the old tab does not delete the new transcript")
}

func TestResumeAfterWorkerRetentionStillOpens(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	seedTranscriptSource(t, svc, "source-agent", workingDir, "session-a1", provider, true)
	_, err := svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
		ID: "source-message", AgentID: "source-agent", AgentSessionID: "session-a1",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte("saved before retention"),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		AgentProvider:                  provider,
		CreatedAt:                      sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	_, err = svc.Queries.DeleteClosedAgentsBefore(t.Context(), sqltime.SQLiteNullTimeOf(time.Now().Add(time.Hour)))
	require.NoError(t, err)
	_, err = svc.Queries.GetAgentByID(t.Context(), "source-agent")
	require.ErrorIs(t, err, sql.ErrNoRows)

	targetID := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: targetID, Seq: 0})
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestRepeatedResumeCopiesTheNewestClosedTranscript(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	seedTranscriptSource(t, svc, "original", workingDir, "session-a1", provider, false)
	_, err := svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
		ID: "original-message", AgentID: "original", AgentSessionID: "session-a1",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte("first"),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		AgentProvider:                  provider,
		CreatedAt:                      sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	_, err = svc.Queries.CloseAgent(t.Context(), "original")
	require.NoError(t, err)

	firstReopen := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{
		ID: firstReopen, AgentSessionID: "session-a1",
	}))
	_, err = svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
		ID: "second-message", AgentID: firstReopen, AgentSessionID: "session-a1",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, Content: []byte("second"),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		AgentProvider:                  provider,
		CreatedAt:                      sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	_, err = svc.Queries.CloseAgent(t.Context(), firstReopen)
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), "UPDATE agents SET closed_at = ? WHERE id = ?", "2026-01-01T00:00:00.000Z", "original")
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), "UPDATE agents SET closed_at = ? WHERE id = ?", "2026-01-02T00:00:00.000Z", firstReopen)
	require.NoError(t, err)
	seedTranscriptSource(t, svc, "empty-newer", workingDir, "session-a1", provider, true)
	_, err = svc.DB.ExecContext(t.Context(), "UPDATE agents SET closed_at = ? WHERE id = ?", "2026-01-03T00:00:00.000Z", "empty-newer")
	require.NoError(t, err)

	secondReopen := openTranscriptResumeAgent(t, dispatcher, workingDir, "session-a1", provider)
	rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: secondReopen, Seq: 0})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, secondReopen+":1", rows[0].ID)
	assert.Equal(t, secondReopen+":2", rows[1].ID)
	assert.Equal(t, []byte("first"), rows[0].Content)
	assert.Equal(t, []byte("second"), rows[1].Content)
}

func TestResumeCopyFailureRollsBackTheNewAgent(t *testing.T) {
	t.Parallel()
	svc, _ := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_DIRAC
	seedTranscriptSource(t, svc, "source-agent", workingDir, "session-a1", provider, false)
	_, err := svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
		ID: "source-message", AgentID: "source-agent", AgentSessionID: "session-a1",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte("first"),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		AgentProvider:                  provider,
		CreatedAt:                      sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	_, err = svc.Queries.CloseAgent(t.Context(), "source-agent")
	require.NoError(t, err)
	seedTranscriptSource(t, svc, "collision-owner", workingDir, "other-session", provider, false)
	_, err = svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
		ID: "target:1", AgentID: "collision-owner", AgentSessionID: "other-session",
		Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, Content: []byte("collision"),
		ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		AgentProvider:                  provider,
		CreatedAt:                      sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)

	err = svc.createAgentRecordWithTranscript(t.Context(), db.CreateAgentParams{
		ID: "target", WorkingDir: workingDir, HomeDir: svc.HomeDir,
		AgentProvider: provider, Resumed: 1,
	}, "session-a1")
	require.Error(t, err)
	assert.ErrorContains(t, err, "copy the prior Worker transcript")
	_, err = svc.Queries.GetAgentByID(t.Context(), "target")
	assert.ErrorIs(t, err, sql.ErrNoRows, "a failed copy removes the uncommitted target")
	oldRows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: "source-agent", Seq: 0})
	require.NoError(t, err)
	assert.Len(t, oldRows, 1, "a failed copy leaves the source intact")
}
