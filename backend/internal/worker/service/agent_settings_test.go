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
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

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
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

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
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

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
	_, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "msg-1",
		AgentID:       "agent-clear",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte(`{"content":"hello"}`),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:     time.Now(),
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
	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "msg-2",
		AgentID:       "agent-clear",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte(`{"content":"world"}`),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)

	assert.Equal(t, "session-B",
		svc.resolveResumeSessionID("agent-clear", dbAgent.AgentSessionID, dbAgent.Resumed),
		"session-B should be resumable after a user message is sent")
}

func TestResolveResumeSessionID_NotAffectedByJustPersistedMessage(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	// Create a non-resumed agent (simulates opening a fresh tab).
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-idle",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Model:       "opus",
	}))
	// Simulate Claude Code's init message storing a session ID.
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "session-idle",
		ID:             "agent-idle",
	}))

	// Resolve BEFORE the user message is persisted — this is the fix.
	// Without the fix, the caller would persist the message first, and
	// resolveResumeSessionID would see the just-created message and
	// incorrectly return the session ID.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-idle")
	require.NoError(t, err)
	result := svc.resolveResumeSessionID("agent-idle", dbAgent.AgentSessionID, dbAgent.Resumed)
	assert.Empty(t, result,
		"should NOT resume — no user messages were exchanged before this send")

	// Now persist the user message (simulating the SendAgentMessage flow).
	_, err = createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "msg-first",
		AgentID:       "agent-idle",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte(`{"content":"hello"}`),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:     time.Now(),
	})
	require.NoError(t, err)

	// After the message is persisted, resolveResumeSessionID would
	// now find it — but the caller already has the pre-resolved value,
	// so the agent starts without --resume.
	postResult := svc.resolveResumeSessionID("agent-idle", dbAgent.AgentSessionID, dbAgent.Resumed)
	assert.Equal(t, "session-idle", postResult,
		"after the message is in the DB, HasUserMessages returns true (demonstrates the ordering bug)")
}

func TestUpdateAgentSettings_DoesNotResumeSessionOnRestart(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

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
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

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

func TestPersistConfirmedAgentSettings_MergesDiscoveredPrimaryAgent(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-opencode",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		ExtraSettings: `{"primaryAgent":"build"}`,
	}))

	_, err := svc.persistConfirmedAgentSettings(
		"agent-opencode",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		"",
		"",
		"",
		loadExtraSettings(`{"primaryAgent":"build"}`, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE),
		&leapmuxv1.AgentSettings{
			Model:         "openai/gpt-5",
			ExtraSettings: map[string]string{"primaryAgent": "plan"},
		},
	)
	require.NoError(t, err)

	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-opencode")
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-5", dbAgent.Model)
	assert.Equal(t, "plan", loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider)["primaryAgent"])
}

// TestPersistConfirmedAgentSettings_PersistsDiscoveredPrimaryAgentFromEmpty
// simulates the initial OpenAgent flow: an agent is created with empty
// extra_settings, then persistConfirmedAgentSettings is called with the
// discovered primary agent from CurrentSettings(). Verifies the primary
// agent is stored in the DB so that subsequent settings_changed notifications
// include a non-empty old value.
func TestPersistConfirmedAgentSettings_PersistsDiscoveredPrimaryAgentFromEmpty(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	// Agent created with empty extra_settings (like the OpenAgent handler does
	// when no extraSettings are provided by the frontend).
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-opencode",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		ExtraSettings: "{}",
	}))

	// Simulate what the OpenAgent handler does: call persistConfirmedAgentSettings
	// with empty requested extraSettings and confirmed settings that include
	// the discovered primary agent.
	_, err := svc.persistConfirmedAgentSettings(
		"agent-opencode",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		"",  // model (empty, will be overridden by confirmed)
		"",  // effort
		"",  // permissionMode
		nil, // requested extraSettings (empty for a new tab)
		&leapmuxv1.AgentSettings{
			Model:         "openai/gpt-5",
			ExtraSettings: map[string]string{"primaryAgent": "build"},
		},
	)
	require.NoError(t, err)

	// Verify the discovered primary agent was persisted.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-opencode")
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-5", dbAgent.Model)

	persisted := loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider)
	assert.Equal(t, "build", persisted["primaryAgent"],
		"discovered primary agent should be persisted from empty initial state")

	// Now simulate the user changing the primary agent — the old value
	// should come from the DB and be non-empty.
	oldExtras := loadExtraSettings(dbAgent.ExtraSettings, dbAgent.AgentProvider)
	newExtras := mergeExtraSettings(oldExtras, map[string]string{"primaryAgent": "plan"})
	assert.Equal(t, "build", oldExtras["primaryAgent"],
		"old extra_settings should contain the previously persisted primary agent")
	assert.Equal(t, "plan", newExtras["primaryAgent"],
		"new extra_settings should reflect the requested change")
}

func TestPersistConfirmedAgentSettings_PersistsAvailableModelsAndGroups(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-gemini",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		Model:         "auto",
	}))

	// Preload the manager cache with models and option groups
	// (simulates what startAgentWith would do after agent starts).
	models := []*leapmuxv1.AvailableModel{
		{Id: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro"},
		{Id: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash"},
	}
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Key: "thinkingBudget", Label: "Thinking Budget", Options: []*leapmuxv1.AvailableOption{
			{Id: "low", Name: "Low"},
			{Id: "high", Name: "High"},
		}},
	}
	svc.Agents.PreloadCache("agent-gemini", models, groups)

	// Persist confirmed settings — should also persist available models/groups.
	_, err := svc.persistConfirmedAgentSettings(
		"agent-gemini",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		"auto", "", "", nil,
		&leapmuxv1.AgentSettings{Model: "auto"},
	)
	require.NoError(t, err)

	// Verify available models/groups were persisted to DB.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-gemini")
	require.NoError(t, err)
	assert.NotEqual(t, "[]", dbAgent.AvailableModels, "available_models should be populated")
	assert.NotEqual(t, "[]", dbAgent.AvailableOptionGroups, "available_option_groups should be populated")

	// Verify round-trip: unmarshal back and check values.
	parsedModels := unmarshalAvailableModels(dbAgent.AvailableModels)
	require.Len(t, parsedModels, 2)
	assert.Equal(t, "gemini-2.5-pro", parsedModels[0].GetId())
	assert.Equal(t, "gemini-2.5-flash", parsedModels[1].GetId())

	parsedGroups := unmarshalAvailableOptionGroups(dbAgent.AvailableOptionGroups)
	require.Len(t, parsedGroups, 1)
	assert.Equal(t, "thinkingBudget", parsedGroups[0].GetKey())
	assert.Len(t, parsedGroups[0].GetOptions(), 2)
}

// TestMarshalAvailableModels_StripsDerivedDefaultBadge covers S6: the IsDefault badge is
// derived from the LEAPMUX_*_DEFAULT_MODEL override (re-applied on every read by the
// manager), so persisting it would store a value that goes stale when the override
// changes. marshalAvailableModels strips it, and must not mutate the shared catalog
// pointer it strips from.
func TestMarshalAvailableModels_StripsDerivedDefaultBadge(t *testing.T) {
	badged := &leapmuxv1.AvailableModel{Id: "opus[1m]", DisplayName: "Opus (1M)", IsDefault: true}
	models := []*leapmuxv1.AvailableModel{
		{Id: "sonnet", DisplayName: "Sonnet"},
		badged,
	}

	parsed := unmarshalAvailableModels(marshalAvailableModels(models))
	require.Len(t, parsed, 2)
	for _, m := range parsed {
		assert.False(t, m.GetIsDefault(), "persisted %q must carry no default badge", m.GetId())
	}
	assert.Equal(t, "opus[1m]", parsed[1].GetId(), "intrinsic fields survive the strip")

	// The shared, immutable catalog pointer must not be mutated in place by the strip.
	assert.True(t, badged.IsDefault, "marshaling must not clear IsDefault on the shared input entry")
}

// TestStripDefaultModelBadge_DropsNilEntries guards that a nil catalog entry is
// dropped rather than passed through to protojson (which would persist it as a
// hollow "{}"), matching the nil-as-absent convention the rest of the catalog
// plumbing uses.
func TestStripDefaultModelBadge_DropsNilEntries(t *testing.T) {
	models := []*leapmuxv1.AvailableModel{
		{Id: "sonnet", DisplayName: "Sonnet"},
		nil,
		{Id: "opus[1m]", DisplayName: "Opus (1M)", IsDefault: true},
	}

	stripped := stripDefaultModelBadge(models)
	require.Len(t, stripped, 2, "the nil entry is dropped")
	for _, m := range stripped {
		require.NotNil(t, m)
		assert.False(t, m.GetIsDefault(), "the badge is cleared on the surviving entries")
	}

	// End to end: the persisted round-trip carries no hollow entry.
	parsed := unmarshalAvailableModels(marshalAvailableModels(models))
	require.Len(t, parsed, 2)
	assert.Equal(t, []string{"sonnet", "opus[1m]"}, []string{parsed[0].GetId(), parsed[1].GetId()})
}

func TestUpdateAgentSettings_BroadcastsGeminiPermissionModeLabels(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-gemini",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		Model:         "auto",
	}))
	require.NoError(t, svc.Queries.UpdateAgentAllSettings(ctx, db.UpdateAgentAllSettingsParams{
		Model:          "auto",
		Effort:         "",
		PermissionMode: "default",
		ExtraSettings:  "{}",
		ID:             "agent-gemini",
	}))

	sender := channel.NewSender(w)
	svc.Watchers.WatchAgent("agent-gemini", &EventWatcher{
		ChannelID: w.channelID,
		Sender:    sender,
	})

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-gemini",
		Settings: &leapmuxv1.AgentSettings{
			PermissionMode: "yolo",
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

	changes, ok := payload["changes"].(map[string]any)
	require.True(t, ok)
	change, ok := changes["permissionMode"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "default", change["old"])
	assert.Equal(t, "yolo", change["new"])
	assert.Equal(t, "Default", change["oldLabel"])
	assert.Equal(t, "YOLO", change["newLabel"])
}

func TestSendAgentRawMessage_SetPermissionModePersistsToDBWhileRunning(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.SetAgentPermissionMode(ctx, db.SetAgentPermissionModeParams{
		PermissionMode: "default",
		ID:             "agent-1",
	}))

	// Register a mock agent so HasAgent returns true.
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	// Send set_permission_mode via SendAgentRawMessage while the agent
	// is running. The DB should be updated eagerly, even if the agent
	// doesn't echo the mode back in its control_response.
	dispatch(d, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: "agent-1",
		Content: `{"type":"control_request","request_id":"r1","request":{"subtype":"set_permission_mode","mode":"bypassPermissions"}}`,
	}, w)

	require.Empty(t, w.errors)

	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "bypassPermissions", dbAgent.PermissionMode,
		"permission mode should be persisted to DB when set_permission_mode is sent while agent is running")
}

// TestConfirmedOrDefault verifies the settings_changed notification resolves the
// model/effort the session actually settled on, falling back to the requested
// values when the confirmed settings are nil or omit a field. Both the
// live-update and restart branches share this helper.
func TestConfirmedOrDefault(t *testing.T) {
	// nil confirmed settings -> requested values unchanged (offline edit / failed restart).
	model, effort := confirmedOrDefault(nil, "default", "auto")
	assert.Equal(t, "default", model)
	assert.Equal(t, "auto", effort)

	// Both fields confirmed -> both override (sentinel resolved to a concrete model;
	// effort clamped to a concrete level).
	model, effort = confirmedOrDefault(&leapmuxv1.AgentSettings{Model: "sonnet", Effort: "high"}, "default", "auto")
	assert.Equal(t, "sonnet", model)
	assert.Equal(t, "high", effort)

	// Empty fields in confirmed settings don't clobber the requested values.
	model, effort = confirmedOrDefault(&leapmuxv1.AgentSettings{Model: "", Effort: "max"}, "opus[1m]", "auto")
	assert.Equal(t, "opus[1m]", model, "empty confirmed model keeps the requested model")
	assert.Equal(t, "max", effort)

	// The common live case: the session confirms the requested model unchanged, so the
	// returned model equals the request -- which keeps the notifyModel != newModel
	// guard (gating the corrective UpdateAgentModelAndEffort write) from firing.
	model, effort = confirmedOrDefault(&leapmuxv1.AgentSettings{Model: "opus", Effort: "xhigh"}, "opus", "ultracode")
	assert.Equal(t, "opus", model, "confirmed model equal to the request is returned unchanged")
	assert.Equal(t, "xhigh", effort, "confirmed effort (clamped from ultracode) overrides the request")
}

// TestReportModelChange verifies the settings_changed notification reports a model
// change whenever the stored model and the settled model differ after normalization,
// suppressing only a model that merely re-normalizes to the same alias. The
// account-default sentinel resolving to a concrete model IS reported (S7): a stuck
// "default" finally resolving is a real, user-visible transition, and there is no
// signal distinguishing it from a new tab's first resolution.
func TestReportModelChange(t *testing.T) {
	sentinel := agent.DefaultModelSentinel
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	// S7: the sentinel resolving to a concrete model is reported -- the panel shows
	// the resolved model too, so chat and panel agree.
	assert.True(t, reportModelChange(claude, sentinel, "sonnet"),
		"sentinel resolving to a concrete model is a real transition")
	// Explicit switch away from the sentinel reports.
	assert.True(t, reportModelChange(claude, sentinel, "opus"),
		"explicit switch from default is reported")
	// Normal concrete switch reports.
	assert.True(t, reportModelChange(claude, "sonnet", "opus"))
	// An unresolved sentinel stays "default" on both sides -> no spurious change.
	assert.False(t, reportModelChange(claude, sentinel, sentinel),
		"an unresolved sentinel is not a change")
	// No change -> nothing to report.
	assert.False(t, reportModelChange(claude, "sonnet", "sonnet"))
	// A concrete model resolving to the same model (effort-only edit) -> no report.
	assert.False(t, reportModelChange(claude, "opus", "opus"))
	// A stored fully-qualified model that re-normalizes to the settled alias on an
	// effort-only edit is NOT a model change (both normalize to "opus").
	assert.False(t, reportModelChange(claude, "claude-opus-4-8", "opus"),
		"a model that only re-normalizes is not a change")
	assert.False(t, reportModelChange(claude, "claude-opus-4-8[1m]", "opus[1m]"),
		"the [1m] variant re-normalizes too")
	// A genuine switch whose spellings normalize differently still reports.
	assert.True(t, reportModelChange(claude, "claude-opus-4-8", "sonnet"),
		"a genuine switch (opus -> sonnet) still reports despite normalization")
	// A provider without an alias space (Codex) compares raw, unchanged behavior.
	codex := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	assert.True(t, reportModelChange(codex, "gpt-5-codex", "gpt-5"))
	assert.False(t, reportModelChange(codex, "gpt-5", "gpt-5"))
}
