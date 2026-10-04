package service

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResumeKeepsNativeCompletionReceiptsForTheRootAndSuppressedChildDivider(t *testing.T) {
	t.Parallel()
	svc, dispatcher := transcriptResumeService(t)
	workingDir := t.TempDir()
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	seedTranscriptSource(t, svc, "old-root", workingDir, "native-root-session", provider, false)
	root := svc.Output.NewSink("old-root", provider)
	rootContent := agent.MessageContent{Original: []byte(`{"type":"result","duration_ms":12}`), AgentSessionID: "native-root-session", IdempotencyKey: "root-turn:1"}
	require.NoError(t, root.PersistTurnEnd(rootContent, agent.SpanInfo{}))
	childID, err := root.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child", Title: "Read the source"})
	require.NoError(t, err)
	child := root.ChildSink(childID)
	child.UpdateSessionID("native-child-session")
	require.NoError(t, root.CloseBackgroundTask("native-child", bgtask.StatusCompleted))
	childContent := agent.MessageContent{Original: []byte(`{"type":"result","duration_ms":12}`), AgentSessionID: "native-child-session", IdempotencyKey: "child-turn:1"}
	require.NoError(t, child.PersistTurnEnd(childContent, agent.SpanInfo{}))
	for _, id := range []string{childID, "old-root"} {
		_, err := svc.Queries.CloseAgent(t.Context(), id)
		require.NoError(t, err)
	}
	newRootID := openTranscriptResumeAgent(t, dispatcher, workingDir, "native-root-session", provider)
	newChild, err := svc.Queries.GetChildAgentByProviderKey(t.Context(), db.GetChildAgentByProviderKeyParams{ParentAgentID: sqlString(newRootID), ProviderChildKey: "native-child"})
	require.NoError(t, err)
	for _, scope := range []struct {
		agentID string
		content agent.MessageContent
	}{
		{newRootID, rootContent}, {newChild.ID, childContent},
	} {
		stored, err := svc.Queries.HasAgentTurnEnd(t.Context(), db.HasAgentTurnEndParams{AgentID: scope.agentID, AgentSessionID: scope.content.AgentSessionID, IdempotencyKey: scope.content.IdempotencyKey})
		require.NoError(t, err)
		assert.True(t, stored, "the reopened transcript must retain its native completion identity")
	}
	writer := &testResponseWriter{channelID: "resumed-native-completion"}
	registerAgentWatch(svc, writer.channelID, newRootID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	registerAgentWatch(svc, writer.channelID, newChild.ID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	newRoot := svc.Output.NewSink(newRootID, provider)
	require.NoError(t, newRoot.PersistTurnEnd(rootContent, agent.SpanInfo{}))
	require.NoError(t, newRoot.ChildSink(newChild.ID).PersistTurnEnd(childContent, agent.SpanInfo{}))
	for _, stream := range writer.streamsSnapshot() {
		assert.Nil(t, decodeWatchAgentEvent(t, stream).GetTurnEnd(), "historical completion replay must not notify the reopened tab again")
	}
}
