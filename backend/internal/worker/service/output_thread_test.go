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

// setupNotifThreadTest stands up a worker service with one agent and returns
// the sink + a row-listing helper bound to that agent. Used by the
// notification-threading tests below to remove repeated CreateAgent /
// NewSink / ListMessagesByAgentID boilerplate.
func setupNotifThreadTest(t *testing.T, provider leapmuxv1.AgentProvider) (agent.OutputSink, func() []db.Message) {
	t.Helper()
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: provider,
	}))

	sink := svc.Output.NewSink("agent-1", provider)
	listRows := func() []db.Message {
		t.Helper()
		rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
			AgentID: "agent-1",
			Seq:     0,
			Limit:   20,
		})
		require.NoError(t, err)
		return rows
	}
	return sink, listRows
}

func TestNotificationThreading_MergesOnlyAdjacentNotifications(t *testing.T) {
	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	firstNotif, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)
	secondNotif, err := json.Marshal(map[string]any{"type": "interrupted"})
	require.NoError(t, err)

	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, firstNotif))
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, secondNotif))

	rows := listRows()
	require.Len(t, rows, 1)

	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, wrapper.Messages, 2)
	assert.Equal(t, []string{"context_cleared", "interrupted"}, types(t, wrapper.Messages))
}

func TestNotificationThreading_NonNotificationBreaksAdjacency(t *testing.T) {
	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
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

	rows := listRows()
	require.Len(t, rows, 3)

	firstWrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	secondWrapper := decodeNotifWrapper(t, rows[2].Content, rows[2].ContentCompression)
	require.Len(t, firstWrapper.Messages, 1)
	require.Len(t, secondWrapper.Messages, 1)
	assert.Equal(t, "context_cleared", msgType(t, firstWrapper.Messages[0]))
	assert.Equal(t, "interrupted", msgType(t, secondWrapper.Messages[0]))
}

func TestNotificationThreading_CodexStartupStatusConsolidatesInWrapper(t *testing.T) {
	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	starting := raw(t, codexStartupStatus("codex_apps", "starting", nil))
	ready := raw(t, codexStartupStatus("codex_apps", "ready", nil))
	settingsChanged, err := json.Marshal(map[string]any{
		"type": "settings_changed",
		"changes": map[string]any{
			"permissionMode": map[string]any{"old": "on-request", "new": "never"},
		},
	})
	require.NoError(t, err)

	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, starting))
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, ready))
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_LEAPMUX, settingsChanged))

	rows := listRows()
	require.Len(t, rows, 1)

	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, wrapper.Messages, 2)
	assert.Equal(t, []string{"mcpServer/startupStatus/updated", "settings_changed"}, types(t, wrapper.Messages))

	startup := parseRaw(t, wrapper.Messages[0])
	params := startup["params"].(map[string]interface{})
	assert.Equal(t, "codex_apps", params["name"])
	assert.Equal(t, "ready", params["status"])
}

func TestNotificationThreading_CodexMetadataNotificationsPersistAsSystemWrapper(t *testing.T) {
	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	skillsChanged := raw(t, codexMethod("skills/changed", map[string]interface{}{}))
	remoteControlChanged := raw(t, codexMethod("remoteControl/status/changed", map[string]interface{}{
		"status":        "disabled",
		"environmentId": nil,
	}))

	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, skillsChanged))
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, remoteControlChanged))

	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, rows[0].Role)

	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, wrapper.Messages, 2)
	assert.Equal(t, []string{"skills/changed", "remoteControl/status/changed"}, types(t, wrapper.Messages))
}

func TestNotificationThreading_CodexMetadataNotificationsSurviveMixedThread(t *testing.T) {
	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	starting := raw(t, codexStartupStatus("codex_apps", "starting", nil))
	skillsChanged := raw(t, codexMethod("skills/changed", map[string]interface{}{}))
	remoteControlChanged := raw(t, codexMethod("remoteControl/status/changed", map[string]interface{}{
		"status":        "disabled",
		"environmentId": nil,
	}))
	ready := raw(t, codexStartupStatus("codex_apps", "ready", nil))

	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, starting))
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, skillsChanged))
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, remoteControlChanged))
	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, ready))

	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, rows[0].Role)

	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, wrapper.Messages, 3)
	assert.Equal(t, []string{
		"skills/changed",
		"remoteControl/status/changed",
		"mcpServer/startupStatus/updated",
	}, types(t, wrapper.Messages))

	startup := parseRaw(t, wrapper.Messages[2])
	params := startup["params"].(map[string]interface{})
	assert.Equal(t, "ready", params["status"])
}

// TestNotificationThreading_RepeatedIdenticalProviderScopedSkipsWrite verifies
// that appending a ProviderScoped notification whose consolidation collapses
// to byte-identical wrapper.Messages does not bump the row's seq. A flapping
// remoteControl/status/changed should not produce a DB write per arrival.
func TestNotificationThreading_RepeatedIdenticalProviderScopedSkipsWrite(t *testing.T) {
	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	payload := raw(t, codexMethod("remoteControl/status/changed", map[string]interface{}{
		"status":        "disabled",
		"environmentId": nil,
	}))

	require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, payload))
	rows := listRows()
	require.Len(t, rows, 1)
	seqAfterFirst := rows[0].Seq

	for i := 0; i < 5; i++ {
		require.NoError(t, sink.PersistNotification(leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM, payload))
	}
	rows = listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, seqAfterFirst, rows[0].Seq,
		"identical ProviderScoped notifications must not bump the row's seq")
}
