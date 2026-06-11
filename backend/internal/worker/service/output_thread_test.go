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
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

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

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, firstNotif)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, secondNotif)

	rows := listRows()
	require.Len(t, rows, 1)

	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, wrapper.Messages, 2)
	assert.Equal(t, []string{"context_cleared", "interrupted"}, types(t, wrapper.Messages))
}

// TestNotificationThreading_CrossSourceProducesSeparateThreads verifies
// that adjacent notifications with different sources do not consolidate
// into one thread. An AGENT-source system notification followed by a
// LEAPMUX-source platform notification must produce two separate
// notification rows, each carrying a truthful per-thread source.
func TestNotificationThreading_CrossSourceProducesSeparateThreads(t *testing.T) {
	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	agentNotif, err := json.Marshal(map[string]any{"type": "system", "subtype": "status", "status": "compacting"})
	require.NoError(t, err)
	leapmuxNotif, err := json.Marshal(map[string]any{"type": "context_cleared"})
	require.NoError(t, err)

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agentNotif)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, leapmuxNotif)

	rows := listRows()
	require.Len(t, rows, 2, "cross-source adjacent notifications must not consolidate")
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[0].Source)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, rows[1].Source)

	firstWrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	secondWrapper := decodeNotifWrapper(t, rows[1].Content, rows[1].ContentCompression)
	require.Len(t, firstWrapper.Messages, 1)
	require.Len(t, secondWrapper.Messages, 1)
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

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, firstNotif)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, assistantMsg, agent.SpanInfo{}))
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, secondNotif)

	rows := listRows()
	require.Len(t, rows, 3)

	firstWrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	secondWrapper := decodeNotifWrapper(t, rows[2].Content, rows[2].ContentCompression)
	require.Len(t, firstWrapper.Messages, 1)
	require.Len(t, secondWrapper.Messages, 1)
	assert.Equal(t, "context_cleared", msgType(t, firstWrapper.Messages[0]))
	assert.Equal(t, "interrupted", msgType(t, secondWrapper.Messages[0]))
}

// TestRelaunchOnExitPreservesNotificationThread is the regression for the duplicate
// settings-change notifications (Issue 1). A model/effort switch RELAUNCHES the agent;
// the old process stopping fires the runner's onExit handler, which must drop the
// dying process's pending control_requests WITHOUT clearing the in-memory notification
// thread -- otherwise the notification persisted after the relaunch lands in a fresh
// thread and can't consolidate with (cancel) the one before it. The bug was onExit
// calling the full ClearAgentRuntimeState (which clears lastNotifThread via
// CleanupAgent); the fix routes onExit through ClearPendingControlRequests instead.
func TestRelaunchOnExitPreservesNotificationThread(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	sink := svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	listRows := func() []db.Message {
		rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Seq: 0, Limit: 20})
		require.NoError(t, err)
		return rows
	}

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, raw(t, settingsChanged("sonnet", "haiku")))
	// Exactly what the relaunch's onExit handler now does (runner.go): drop pending
	// control requests, but leave the notification thread intact.
	svc.Output.ClearPendingControlRequests("agent-1")
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, raw(t, settingsChanged("haiku", "opus[1m]")))

	rows := listRows()
	require.Len(t, rows, 1, "the relaunch's onExit must NOT split the notification thread into two rows")
	wrapper := decodeNotifWrapper(t, rows[0].Content, rows[0].ContentCompression)
	require.Len(t, wrapper.Messages, 1, "the two model changes consolidate into one settings_changed")
	var merged struct {
		Changes map[string]struct {
			Old string `json:"old"`
			New string `json:"new"`
		} `json:"changes"`
	}
	require.NoError(t, json.Unmarshal(wrapper.Messages[0], &merged))
	assert.Equal(t, "sonnet", merged.Changes["model"].Old)
	assert.Equal(t, "opus[1m]", merged.Changes["model"].New, "sonnet->haiku->opus[1m] merges to sonnet->opus[1m]")

	// Contrast: the PERMANENT-teardown cleanup DOES clear the thread, so a later
	// notification correctly starts a fresh standalone row.
	svc.Output.ClearAgentRuntimeState("agent-1")
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, raw(t, settingsChanged("opus[1m]", "haiku")))
	assert.Len(t, listRows(), 2, "ClearAgentRuntimeState clears the thread, so the next notification is a new row")
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

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, starting)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, ready)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, settingsChanged)

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

func TestNotificationThreading_CodexMetadataNotificationsPersistAsAgentWrapper(t *testing.T) {
	sink, listRows := setupNotifThreadTest(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	skillsChanged := raw(t, codexMethod("skills/changed", map[string]interface{}{}))
	remoteControlChanged := raw(t, codexMethod("remoteControl/status/changed", map[string]interface{}{
		"status":        "disabled",
		"environmentId": nil,
	}))

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, skillsChanged)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, remoteControlChanged)

	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[0].Source)

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

	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, starting)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, skillsChanged)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, remoteControlChanged)
	persistNotif(t, sink, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, ready)

	rows := listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, rows[0].Source)

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

	// The first notification opens a standalone thread and is broadcast.
	broadcast, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, payload)
	require.NoError(t, err)
	assert.True(t, broadcast, "the first notification is broadcast to the frontend")
	rows := listRows()
	require.Len(t, rows, 1)
	seqAfterFirst := rows[0].Seq

	// Each repeat collapses byte-identically into the tail: no DB write AND no
	// broadcast. The broadcast=false return is what keeps the thinking-token
	// reset decorator in lockstep with the frontend, which never clears here.
	for i := 0; i < 5; i++ {
		broadcast, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, payload)
		require.NoError(t, err)
		assert.False(t, broadcast,
			"an identical ProviderScoped notification collapses and must not report a broadcast")
	}
	rows = listRows()
	require.Len(t, rows, 1)
	assert.Equal(t, seqAfterFirst, rows[0].Seq,
		"identical ProviderScoped notifications must not bump the row's seq")
}
