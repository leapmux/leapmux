package service

import (
	"context"
	"errors"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFailedNativeResumeRetainsTheCopiedWorkerTranscript(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI
	const sourceID = "closed-gemini-source"
	const sessionID = "native-gemini-session"
	const nativeError = "No previous sessions found for this project."
	seedTranscriptSource(t, svc, sourceID, workingDir, sessionID, provider, false)
	contents := []struct {
		source leapmuxv1.MessageSource
		bytes  string
		key    string
	}{
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_USER, `{"content":"Keep the stored Worker prompt."}`, "native-user:1"},
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, `{"sessionId":"native-gemini-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"The stored Worker answer remains."}}}`, "native-answer:1"},
		{leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, `{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn"}}`, "native-turn:1"},
	}
	for index, content := range contents {
		_, err := svc.Queries.CreateMessage(t.Context(), db.CreateMessageParams{
			ID: sourceID + content.key, AgentID: sourceID, AgentSessionID: sessionID,
			Source: content.source, Content: []byte(content.bytes), IdempotencyKey: content.key,
			ContentCompression:             leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			SupplementalContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
			AgentProvider:                  provider, CreatedAt: sqltime.NewSQLiteTime(time.Unix(100+int64(index), 0)),
		})
		require.NoError(t, err)
	}
	claimed, err := svc.Queries.ClaimAgentTurnEnd(t.Context(), db.ClaimAgentTurnEndParams{
		AgentID: sourceID, AgentSessionID: sessionID, IdempotencyKey: contents[2].key,
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, claimed)
	_, err = svc.Queries.CloseAgent(t.Context(), sourceID)
	require.NoError(t, err)
	original, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sourceID})
	require.NoError(t, err)
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, errors.New(nativeError)
	}
	targetID := openTranscriptResumeAgent(t, dispatcher, workingDir, sessionID, provider)
	drainAllInFlight(svc)
	target, err := svc.Queries.GetAgentByID(t.Context(), targetID)
	require.NoError(t, err)
	assert.Contains(t, target.StartupError, nativeError)
	assert.Empty(t, target.AgentSessionID)
	assert.Empty(t, target.PendingResumeSessionID)
	status, startupError, _ := svc.deriveAgentStatus(&target, false)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, status)
	assert.Contains(t, startupError, nativeError)
	copied, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: targetID})
	require.NoError(t, err)
	require.Len(t, copied, len(original))
	for index, source := range original {
		assert.NotEqual(t, source.ID, copied[index].ID)
		assert.Equal(t, targetID, copied[index].AgentID)
		assert.Equal(t, source.Seq, copied[index].Seq)
		assert.Equal(t, source.AgentSessionID, copied[index].AgentSessionID)
		assert.Equal(t, source.Source, copied[index].Source)
		assert.Equal(t, source.Content, copied[index].Content)
		assert.Equal(t, source.IdempotencyKey, copied[index].IdempotencyKey)
	}
	stored, err := svc.Queries.HasAgentTurnEnd(t.Context(), db.HasAgentTurnEndParams{
		AgentID: targetID, AgentSessionID: sessionID, IdempotencyKey: contents[2].key,
	})
	require.NoError(t, err)
	assert.True(t, stored)
	retained, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: sourceID})
	require.NoError(t, err)
	assert.Equal(t, original, retained)
}
