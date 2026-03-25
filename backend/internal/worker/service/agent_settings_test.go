package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestUpdateAgentSettings_ClearsSessionIDOnRestartFailure(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents)

	workDir := t.TempDir()

	// Create an agent in the DB with a session ID already set
	// (simulates a previously running agent that established a session).
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  workDir,
		HomeDir:     t.TempDir(),
		Model:       "opus",
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "old-session-id",
		ID:             "agent-1",
	}))

	// Register a mock agent so HasAgent returns true and the restart
	// path is entered. The subsequent StartAgent call will fail because
	// the mock agent doesn't implement the Claude Code initialization
	// protocol (sendControlAndWait will time out or fail).
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		Model:      "opus",
		WorkingDir: workDir,
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	// Switch model — StopAndWaitAgent will kill the mock agent, then
	// StartAgent will try to start a real claude process which will fail
	// during the initialization handshake (startup timeout).
	// Use a very short startup timeout to speed up the test.
	svc.AgentStartupTimeout = 1 * time.Nanosecond // forces immediate timeout

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  "agent-1",
		Settings: &leapmuxv1.AgentSettings{Model: "sonnet"},
	}, w)

	// Verify the session ID was cleared from the DB.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Empty(t, dbAgent.AgentSessionID,
		"session ID should be cleared after failed restart")

	// Verify the model was still updated in the DB.
	assert.Equal(t, "sonnet", dbAgent.Model,
		"model should be updated even when restart fails")
}

func TestResolveResumeSessionID_ResumedAgentPreservesSession(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")

	// Create an agent with resumed=1 (simulates a resumed session).
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-resumed",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Model:       "opus",
		Resumed:     1,
	}))
	// Set a session ID (simulates the init message storing it).
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "session-123",
		ID:             "agent-resumed",
	}))

	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-resumed")
	require.NoError(t, err)

	// No user messages have been inserted for this agent in leapmux's DB,
	// but because the agent was resumed, the session ID should be preserved.
	result := svc.resolveResumeSessionID("agent-resumed", dbAgent.AgentSessionID, dbAgent.Resumed)
	assert.Equal(t, "session-123", result,
		"resumed agent should preserve session ID even without local messages")
}

func TestResolveResumeSessionID_IgnoresPreClearMessages(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, "ws-1")

	// Create an agent (non-resumed).
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-clear",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Model:       "opus",
	}))

	// Simulate agent startup: set initial session ID.
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "session-A",
		ID:             "agent-clear",
	}))

	// User sends a message in session-A.
	_, err := svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:        "msg-1",
		AgentID:   "agent-clear",
		Role:      leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:   []byte(`{"content":"hello"}`),
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	// Verify session-A is resumable (has user messages).
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-clear")
	require.NoError(t, err)
	assert.Equal(t, "session-A",
		svc.resolveResumeSessionID("agent-clear", dbAgent.AgentSessionID, dbAgent.Resumed),
		"session-A should be resumable because a user message exists")

	// Simulate /clear: agent restarts fresh, gets a new session ID.
	// UpdateAgentSessionID atomically records session_start_seq.
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "session-B",
		ID:             "agent-clear",
	}))

	// No messages exchanged in session-B yet. Even though old messages
	// from session-A exist, resolveResumeSessionID should return "".
	dbAgent, err = svc.Queries.GetAgentByID(ctx, "agent-clear")
	require.NoError(t, err)
	assert.Empty(t,
		svc.resolveResumeSessionID("agent-clear", dbAgent.AgentSessionID, dbAgent.Resumed),
		"session-B should NOT be resumable — no messages exchanged yet")

	// After the user sends a message in session-B, it should become resumable.
	_, err = svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID:        "msg-2",
		AgentID:   "agent-clear",
		Role:      leapmuxv1.MessageRole_MESSAGE_ROLE_USER,
		Content:   []byte(`{"content":"world"}`),
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)

	assert.Equal(t, "session-B",
		svc.resolveResumeSessionID("agent-clear", dbAgent.AgentSessionID, dbAgent.Resumed),
		"session-B should be resumable after a user message is sent")
}

func TestUpdateAgentSettings_DoesNotResumeSessionOnRestart(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")

	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents)

	// Create an agent with a session ID.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Model:       "opus",
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "old-session-id",
		ID:             "agent-1",
	}))

	// Agent is NOT running (HasAgent returns false), so the restart
	// block is skipped. Verify the DB update works correctly.
	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  "agent-1",
		Settings: &leapmuxv1.AgentSettings{Model: "sonnet"},
	}, w)

	// Verify the response was successful (no errors).
	require.Empty(t, w.errors, "expected no errors")
	require.Len(t, w.responses, 1, "expected one response")

	var resp leapmuxv1.UpdateAgentSettingsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	// Verify the model was updated.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "sonnet", dbAgent.Model)

	// Session ID should remain unchanged when no restart was attempted.
	assert.Equal(t, "old-session-id", dbAgent.AgentSessionID,
		"session ID should not change when agent is not running")
}

func TestUpdateAgentSettings_BroadcastsGenericExtraSettingChanges(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		ExtraSettings: `{"opencode_mode":"safe"}`,
	}))

	sender := channel.NewSender(w)
	svc.Watchers.WatchAgent("agent-1", &EventWatcher{
		ChannelID: w.channelID,
		Sender:    sender,
	})

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-1",
		Settings: &leapmuxv1.AgentSettings{
			ExtraSettings: map[string]string{"opencode_mode": "fast"},
		},
	}, w)

	require.Empty(t, w.errors)
	require.NotEmpty(t, w.streams)

	var resp leapmuxv1.WatchEventsResponse
	require.NoError(t, proto.Unmarshal(w.streams[len(w.streams)-1].GetPayload(), &resp))
	msg := resp.GetAgentEvent().GetAgentMessage()
	require.NotNil(t, msg)

	raw, err := msgcodec.Decompress(msg.Content, msg.ContentCompression)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))

	if payload["type"] != "settings_changed" {
		messages, ok := payload["messages"].([]any)
		require.True(t, ok)
		for _, entry := range messages {
			candidate, ok := entry.(map[string]any)
			if ok && candidate["type"] == "settings_changed" {
				payload = candidate
				break
			}
		}
	}

	assert.Equal(t, "settings_changed", payload["type"])

	changes, ok := payload["changes"].(map[string]any)
	require.True(t, ok)
	change, ok := changes["opencode_mode"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "safe", change["old"])
	assert.Equal(t, "fast", change["new"])
	assert.Equal(t, "opencode_mode", change["label"])
	assert.Equal(t, "safe", change["oldLabel"])
	assert.Equal(t, "fast", change["newLabel"])
}
