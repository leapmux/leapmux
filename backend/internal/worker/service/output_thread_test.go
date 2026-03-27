package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func decodeNotifWrapper(t *testing.T, content []byte, compression leapmuxv1.ContentCompression) notifThreadWrapper {
	t.Helper()
	raw, err := msgcodec.Decompress(content, compression)
	require.NoError(t, err)

	var wrapper notifThreadWrapper
	require.NoError(t, json.Unmarshal(raw, &wrapper))
	return wrapper
}

func TestNotificationThreading_MergesOnlyAdjacentNotifications(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	firstNotif, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)
	secondNotif, err := json.Marshal(map[string]any{"type": "interrupted"})
	require.NoError(t, err)

	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, firstNotif))
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, secondNotif))

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   20,
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)

	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, wrapper.Messages, 2)
	assert.Equal(t, []string{"context_cleared", "interrupted"}, types(t, wrapper.Messages))
}

func TestNotificationThreading_NonNotificationBreaksAdjacency(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	firstNotif, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)
	secondNotif, err := json.Marshal(map[string]any{"type": "interrupted"})
	require.NoError(t, err)
	assistantMsg, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{{"type": "text", "text": "hello"}},
		},
	})
	require.NoError(t, err)

	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, firstNotif))
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageRole_MESSAGE_ROLE_ASSISTANT, assistantMsg, agent.SpanInfo{}))
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, secondNotif))

	rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
		AgentID: "agent-1",
		Seq:     0,
		Limit:   20,
	})
	require.NoError(t, err)
	require.Len(t, rows, 3)

	firstWrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	secondWrapper := decodeNotifWrapper(t, rows[2].Content, rows[2].ContentCompression)
	require.Len(t, firstWrapper.Messages, 1)
	require.Len(t, secondWrapper.Messages, 1)
	assert.Equal(t, "context_cleared", msgType(t, firstWrapper.Messages[0]))
	assert.Equal(t, "interrupted", msgType(t, secondWrapper.Messages[0]))
}
