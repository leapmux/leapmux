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
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/agent"
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
		Options:     marshalOptions(map[string]string{agent.OptionIDModel: "opus"}),
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
		Options:    map[string]string{agent.OptionIDModel: "opus"},
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
		Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDModel: "sonnet"}},
	}, w)

	// Verify the session ID was cleared from the DB.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Empty(t, dbAgent.AgentSessionID,
		"session ID should be cleared after failed restart")

	// Verify the model was still updated in the DB.
	assert.Equal(t, "sonnet", parseOptions(dbAgent.Options)[agent.OptionIDModel],
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
		Options:     marshalOptions(map[string]string{agent.OptionIDModel: "opus"}),
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
		Options:     marshalOptions(map[string]string{agent.OptionIDModel: "opus"}),
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
		CreatedAt:     sqltime.NewSQLiteTime(time.Now()),
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
		CreatedAt:     sqltime.NewSQLiteTime(time.Now()),
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
		Options:     marshalOptions(map[string]string{agent.OptionIDModel: "opus"}),
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
		CreatedAt:     sqltime.NewSQLiteTime(time.Now()),
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
		Options:     marshalOptions(map[string]string{agent.OptionIDModel: "opus"}),
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "old-session-id",
		ID:             "agent-1",
	}))

	// Agent is NOT running (HasAgent returns false), so the restart
	// block is skipped. Verify the DB update works correctly.
	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  "agent-1",
		Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDModel: "sonnet"}},
	}, w)

	// Verify the response was successful (no errors).
	require.Empty(t, w.errors, "expected no errors")
	require.Len(t, w.responses, 1, "expected one response")

	var resp leapmuxv1.UpdateAgentSettingsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	// Verify the model was updated.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "sonnet", parseOptions(dbAgent.Options)[agent.OptionIDModel])

	// Session ID should remain unchanged when no restart was attempted.
	assert.Equal(t, "old-session-id", dbAgent.AgentSessionID,
		"session ID should not change when agent is not running")
}

func TestUpdateAgentSettings_BroadcastsGenericExtraSettingChanges(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	// OpenCode is an ACP provider (no model-dependent groups), so a server-driven config option
	// it surfaces -- like opencode_mode -- is model-independent and lives only in the
	// cached catalog. (A model-dependent provider such as Codex has no such config options, and
	// its empty-stamp cache is rebuilt from the static catalog, so this scenario is an ACP
	// one by construction.)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		Options:       `{"opencode_mode":"safe"}`,
	}))

	// A real server-driven config option is surfaced in the agent's catalog (option_groups),
	// not just persisted in options -- a previously-run agent persists both together.
	// Prime the not-running catalog with the option group so the change validates: the
	// UpdateAgentSettings strip drops any axis absent from BOTH the provider's static
	// allowlist (KnownOptionIDs) AND the catalog, and "opencode_mode" is a catalog-only
	// generic (no static template for it). The cache is served as-is because OpenCode has
	// no model-dependent groups.
	svc.Agents.PreloadCache("agent-1", []*leapmuxv1.AvailableOptionGroup{{
		Id:           "opencode_mode",
		CurrentValue: "safe",
		Options:      []*leapmuxv1.AvailableOption{{Id: "safe"}, {Id: "fast"}},
	}})

	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-1",
		Settings: &leapmuxv1.AgentSettings{
			Options: map[string]string{"opencode_mode": "fast"},
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

// TestUpdateAgentSettings_CursorModelSwitchOmitsEffortChange reproduces the spurious
// "effort (auto)" notification: switching the model on a Cursor agent (an ACP provider
// with no effort axis) must NOT stamp effort=auto and therefore must not surface an
// effort change. Only catalog-effort providers (Claude/Codex/Pi) reset effort on a
// model switch.
func TestUpdateAgentSettings_CursorModelSwitchOmitsEffortChange(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		Options:       `{"model":"auto"}`,
	}))

	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-1",
		Settings: &leapmuxv1.AgentSettings{
			Options: map[string]string{agent.OptionIDModel: "composer-2.5"},
		},
	}, w)

	require.Empty(t, w.errors)
	require.NotEmpty(t, w.streams)

	changes := lastSettingsChangedChanges(t, w)
	_, hasModel := changes[agent.OptionIDModel]
	assert.True(t, hasModel, "the model change must be reported")
	_, hasEffort := changes[agent.OptionIDEffort]
	assert.False(t, hasEffort, "a Cursor model switch must NOT surface a spurious effort change")

	// The persisted options must also stay effort-free.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	opts := loadOptions(dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR)
	_, persistedEffort := opts[agent.OptionIDEffort]
	assert.False(t, persistedEffort, "no inert effort key should be persisted for Cursor")
}

// TestUpdateAgentSettings_ModelSwitchEffortBySupport is the regression guard for [A10]:
// on a model switch for a catalog-effort provider, an effort the NEW model doesn't offer is
// reset to auto, while an effort the new model DOES offer is kept. A CLI `agent set --model
// sonnet --effort xhigh` (xhigh exists on opus, not sonnet) would otherwise persist xhigh
// against sonnet and surface it until a relaunch clamps it.
func TestUpdateAgentSettings_ModelSwitchEffortBySupport(t *testing.T) {
	cases := []struct {
		name       string
		sentEffort string
		wantEffort string
	}{
		{"unsupported effort resets to auto", "xhigh", agent.EffortAuto},
		{"supported effort survives", "high", "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

			require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
				ID:            "agent-1",
				WorkspaceID:   "ws-1",
				WorkingDir:    t.TempDir(),
				HomeDir:       t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
				// opus[1m] offers xhigh; sonnet does not.
				Options: marshalOptions(map[string]string{agent.OptionIDModel: "opus[1m]", agent.OptionIDEffort: "xhigh"}),
			}))
			svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

			dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
				AgentId: "agent-1",
				Settings: &leapmuxv1.AgentSettings{
					Options: map[string]string{agent.OptionIDModel: "sonnet", agent.OptionIDEffort: tc.sentEffort},
				},
			}, w)

			require.Empty(t, w.errors)

			dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
			require.NoError(t, err)
			opts := loadOptions(dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			assert.Equal(t, tc.wantEffort, opts[agent.OptionIDEffort],
				"effort persisted after the model switch")
		})
	}
}

// TestUpdateAgentSettings_UnknownModelKeepsExplicitEffort guards that a CLI `agent set --model <new>
// --effort xhigh` against a STOPPED agent, where <new> is absent from the provider's static catalog
// seed (a model only the running session's live catalog would list), does NOT silently reset the
// effort to auto. The effort can't be judged against an incomplete seed, so it is left for the
// running session to validate -- mirroring ValidateLaunchOptions's deliberate non-validation of
// model/effort against the seed. (A model the seed DOES list still resets an unsupported tier; see
// TestUpdateAgentSettings_ModelSwitchEffortBySupport.)
func TestUpdateAgentSettings_UnknownModelKeepsExplicitEffort(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "sonnet", agent.OptionIDEffort: agent.EffortAuto}),
	}))
	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

	// A model the static Claude seed does not list, plus an explicit effort the live catalog would
	// offer for it. The agent is stopped, so OptionGroups serves the static fallback (no such model).
	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-1",
		Settings: &leapmuxv1.AgentSettings{
			Options: map[string]string{agent.OptionIDModel: "claude-opus-99-unreleased", agent.OptionIDEffort: "xhigh"},
		},
	}, w)

	require.Empty(t, w.errors)
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	got := loadOptions(dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	assert.Equal(t, "xhigh", got[agent.OptionIDEffort],
		"an explicit effort for a model absent from the static seed must survive, not reset to auto")
	assert.Equal(t, "claude-opus-99-unreleased", got[agent.OptionIDModel], "the requested model is persisted")
}

// TestUpdateAgentSettings_UnsupportedEffortWithoutModelSwitch is the regression guard for the
// no-model-switch effort validation: an explicitly-sent effort the CURRENT model doesn't offer
// (e.g. xhigh against sonnet) must reset to auto rather than persist an unsupported tier and
// surface a misleading effort until a relaunch clamps it. A supported effort sent without a
// model switch must survive untouched. Previously effort was only validated on a model switch.
func TestUpdateAgentSettings_UnsupportedEffortWithoutModelSwitch(t *testing.T) {
	cases := []struct {
		name       string
		sentEffort string
		wantEffort string
	}{
		{"unsupported effort resets to auto", "xhigh", agent.EffortAuto},
		{"supported effort survives", "high", "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

			require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
				ID:            "agent-1",
				WorkspaceID:   "ws-1",
				WorkingDir:    t.TempDir(),
				HomeDir:       t.TempDir(),
				AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
				// sonnet offers low/medium/high but NOT xhigh.
				Options: marshalOptions(map[string]string{agent.OptionIDModel: "sonnet", agent.OptionIDEffort: "medium"}),
			}))
			svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

			// No model in the request: this is NOT a model switch, only an effort edit.
			dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
				AgentId:  "agent-1",
				Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDEffort: tc.sentEffort}},
			}, w)

			require.Empty(t, w.errors)
			dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
			require.NoError(t, err)
			assert.Equal(t, tc.wantEffort,
				loadOptions(dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)[agent.OptionIDEffort],
				"effort validated against the current model even without a model switch")
		})
	}
}

// TestUpdateAgentSettings_InheritedUnsupportedEffortResets is the regression guard for the
// merged-effort validation: an edit that touches NEITHER model nor effort (e.g. only permission
// mode) must still reset a STORED effort the unchanged model no longer offers, because the
// validation now keys on the merged (inherited) effort rather than only the explicitly-sent one.
// Without this a stale unsupported tier would survive on the row and surface a misleading effort
// until a relaunch clamped it.
func TestUpdateAgentSettings_InheritedUnsupportedEffortResets(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		// sonnet offers low/medium/high but NOT xhigh -- a stale tier (e.g. the live catalog
		// narrowed after this was stored) the unchanged model no longer supports.
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:          "sonnet",
			agent.OptionIDEffort:         "xhigh",
			agent.OptionIDPermissionMode: agent.PermissionModeDefault,
		}),
	}))
	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

	// The edit changes ONLY permission mode -- no model, no effort -- so the stale xhigh is
	// inherited via the merge, not explicitly sent.
	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  "agent-1",
		Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDPermissionMode: agent.PermissionModePlan}},
	}, w)

	require.Empty(t, w.errors)
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	got := loadOptions(dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	assert.Equal(t, agent.EffortAuto, got[agent.OptionIDEffort],
		"an inherited effort the unchanged model no longer offers resets to auto")
	assert.Equal(t, agent.PermissionModePlan, got[agent.OptionIDPermissionMode],
		"the actual edit (permission mode) still applies")
	assert.Equal(t, "sonnet", got[agent.OptionIDModel], "the model is unchanged")
}

// TestUpdateAgentSettings_EmptyOptionValueIsNoOp guards against destructive empty-value writes:
// an edit carrying a known axis with an empty value (e.g. a stray CLI `agent set --option
// sandbox_policy=`) must be a NO-OP, not a delete -- the wire merge treats an empty value as a
// key deletion, so without the sanitize-time skip the persisted option would be silently wiped.
func TestUpdateAgentSettings_EmptyOptionValueIsNoOp(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:            "gpt-5.5",
			agent.CodexOptionSandboxPolicy: agent.CodexSandboxReadOnly,
		}),
	}))
	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  "agent-1",
		Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.CodexOptionSandboxPolicy: ""}},
	}, w)

	require.Empty(t, w.errors)
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, agent.CodexSandboxReadOnly,
		parseOptions(dbAgent.Options)[agent.CodexOptionSandboxPolicy],
		"an empty option value on the edit path is a no-op, not a destructive delete")
}

// TestCASPersistAgentOptions_PreservesConcurrentKeyOnRetry is the regression guard for the
// settings-write race [C1.1]: a delta-CAS whose `expected` snapshot is stale (a concurrent
// server-initiated refresh moved the row between our read and our write) must re-read and
// re-merge onto the LATEST row, preserving the concurrent writer's key instead of clobbering
// it with a stale full-map blob.
func TestCASPersistAgentOptions_PreservesConcurrentKeyOnRetry(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	stale := marshalOptions(map[string]string{agent.OptionIDModel: "opus[1m]"})
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Options:       stale,
	}))
	// A concurrent refresh lands a NEW key after our `stale` snapshot was taken.
	require.NoError(t, svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{agent.OptionIDModel: "opus[1m]", "reasoning_effort": "high"}),
		ID:      "agent-1",
	}))

	// Persist our edit against the STALE snapshot: the CAS must re-read and re-merge.
	settled, wrote, err := casPersistAgentOptions(ctx, svc.Queries, "agent-1", stale,
		map[string]string{agent.OptionIDModel: "sonnet"})
	require.NoError(t, err)
	assert.True(t, wrote, "the delta changed the latest row, so a write lands")

	got := parseOptions(settled)
	assert.Equal(t, "sonnet", got[agent.OptionIDModel], "our edit landed")
	assert.Equal(t, "high", got["reasoning_effort"], "the concurrent writer's key survived the retry, not clobbered")
}

// TestCASPersistAgentOptions_ReassertsOverConcurrentClear guards S3: when the refresh's merge is a
// no-op against the caller's (stale) snapshot but a concurrent writer has since CLEARED a key the
// refresh re-asserts, the no-op is decided against the LIVE row, not the stale snapshot -- so the
// agent's confirmed value is re-asserted onto the row rather than silently skipped (which would
// leave the row diverged from the running agent until the next refresh).
func TestCASPersistAgentOptions_ReassertsOverConcurrentClear(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	stale := marshalOptions(map[string]string{agent.OptionIDModel: "opus[1m]", agent.OptionIDEffort: "high"})
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       stale,
	}))
	// A concurrent writer CLEARED effort after our `stale` snapshot was taken.
	require.NoError(t, svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{agent.OptionIDModel: "opus[1m]"}),
		ID:      "agent-1",
	}))

	// Re-confirm the agent's snapshot (effort still high): a no-op against `stale`, but NOT against
	// the cleared live row, so the CAS must re-assert effort rather than short-circuit on the stale
	// snapshot.
	settled, wrote, err := casPersistAgentOptions(ctx, svc.Queries, "agent-1", stale,
		map[string]string{agent.OptionIDModel: "opus[1m]", agent.OptionIDEffort: "high"})
	require.NoError(t, err)
	assert.True(t, wrote, "the refresh re-asserts effort over the concurrent clear (no-op decided against the live row)")
	assert.Equal(t, "high", parseOptions(settled)[agent.OptionIDEffort],
		"the agent's confirmed effort is restored, not left cleared by the stale-snapshot no-op")
}

// TestCASPersistAgentOptions_TrueNoOpAgainstLiveRow is the companion to the re-assert case: when
// the refresh genuinely changes nothing against the LIVE row, the CAS short-circuits without a
// write and returns the live row as the settled value.
func TestCASPersistAgentOptions_TrueNoOpAgainstLiveRow(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	row := marshalOptions(map[string]string{agent.OptionIDModel: "opus[1m]", agent.OptionIDEffort: "high"})
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       row,
	}))

	settled, wrote, err := casPersistAgentOptions(ctx, svc.Queries, "agent-1", row,
		map[string]string{agent.OptionIDModel: "opus[1m]", agent.OptionIDEffort: "high"})
	require.NoError(t, err)
	assert.False(t, wrote, "a genuine no-op against the live row writes nothing")
	assert.Equal(t, "high", parseOptions(settled)[agent.OptionIDEffort], "the live row is returned as settled")
}

// TestUpdateAgentSettings_RespelledModelKeepsEffort guards the effort-reset comparison: a
// model re-spelled into the SAME normalized id (a CLI alias, a case difference, or the
// account-default sentinel resolving to its concrete id) is NOT a real model switch, so the
// user's effort must survive rather than resetting to auto. The comparison used a raw string
// !=, so "OPUS[1M]" against the stored "opus[1m]" wrongly reset xhigh -> auto on an edit that
// didn't change the model at all; the normalized comparison fixes it.
func TestUpdateAgentSettings_RespelledModelKeepsEffort(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "opus[1m]", agent.OptionIDEffort: "xhigh"}),
	}))
	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

	// "OPUS[1M]" normalizes to the stored "opus[1m]" -- same model, just a different spelling.
	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-1",
		Settings: &leapmuxv1.AgentSettings{
			Options: map[string]string{agent.OptionIDModel: "OPUS[1M]"},
		},
	}, w)

	require.Empty(t, w.errors)

	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	opts := loadOptions(dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	assert.Equal(t, "xhigh", opts[agent.OptionIDEffort],
		"effort must survive a model re-spell that isn't a real switch")
}

// TestUpdateAgentSettings_AliasedModelKeepsExplicitEffort is the regression guard for [C1]:
// an explicit effort sent alongside a re-spelled model alias must be validated against the
// model's effort tiers by NORMALIZED id. The effort-validity check matched the catalog's
// canonical model ids against the RAW incoming model, so a fully-qualified
// "claude-opus-4-8[1m]" (catalog id "opus[1m]") missed the match and reset a perfectly valid
// effort to auto. Unlike RespelledModelKeepsEffort above, this sends an explicit effort, so it
// exercises the second clause (EffortSupportedByModel) rather than the model-switch clause.
func TestUpdateAgentSettings_AliasedModelKeepsExplicitEffort(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "opus[1m]", agent.OptionIDEffort: "xhigh"}),
	}))
	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

	// Fully-qualified alias for the SAME model, plus an explicit "max" -- a tier opus[1m]
	// supports. The alias normalizes to the stored "opus[1m]", so this is not a switch, and
	// "max" is valid for the model, so it must persist rather than reset to auto.
	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-1",
		Settings: &leapmuxv1.AgentSettings{
			Options: map[string]string{agent.OptionIDModel: "claude-opus-4-8[1m]", agent.OptionIDEffort: "max"},
		},
	}, w)
	require.Empty(t, w.errors)

	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	opts := loadOptions(dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	assert.Equal(t, "max", opts[agent.OptionIDEffort],
		"an explicit effort the model supports must survive an aliased-model edit, not reset to auto")
}

// TestUpdateAgentSettings_OfflineEffortLabelUsesNewModelCatalog is the regression guard for
// [C2]: an offline edit resolves the settings_changed labels against the SETTLED model's
// catalog, not the provider default. With currentModel="" the static fallback enumerates only
// the default model (Claude's "default", which has no effort tiers), so the effort label would
// leak the raw id; passing the new model rebuilds the effort group and resolves the name.
func TestUpdateAgentSettings_OfflineEffortLabelUsesNewModelCatalog(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "opus[1m]", agent.OptionIDEffort: "medium"}),
	}))
	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

	// Switch to sonnet and to a tier sonnet offers (high), so effort actually changes and the
	// label must resolve against sonnet's catalog.
	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-1",
		Settings: &leapmuxv1.AgentSettings{
			Options: map[string]string{agent.OptionIDModel: "sonnet", agent.OptionIDEffort: "high"},
		},
	}, w)
	require.Empty(t, w.errors)

	changes := lastSettingsChangedChanges(t, w)
	effortChange, ok := changes[agent.OptionIDEffort].(map[string]any)
	require.True(t, ok, "the effort change must be reported")
	assert.Equal(t, "High", effortChange["newLabel"],
		"the effort label resolves against the new model's catalog, not a leaked raw id")
}

// TestUpdateAgentSettings_DropsForeignSecondaryAxis verifies a mis-targeted secondary
// axis is stripped rather than persisted as a phantom key: a CLI `agent set
// --permission-mode X` against OpenCode (a primary-agent provider with no permission-mode
// axis) must not land a permissionMode key in the row or the RPC reply, nor emit a
// settings_changed notification for a change the agent never applies.
func TestUpdateAgentSettings_DropsForeignSecondaryAxis(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "anthropic/claude-x"}),
	}))

	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  "agent-1",
		Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDPermissionMode: "plan"}},
	}, w)

	require.Empty(t, w.errors)

	// No phantom permissionMode key in the persisted options.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	opts := loadOptions(dbAgent.Options, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE)
	_, persisted := opts[agent.OptionIDPermissionMode]
	assert.False(t, persisted, "a foreign permissionMode must not be persisted on a primary-agent provider")

	// And the RPC reply doesn't advertise it either.
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.UpdateAgentSettingsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	_, inReply := resp.ConfirmedOptions[agent.OptionIDPermissionMode]
	assert.False(t, inReply, "the reply must not advertise a dropped foreign axis")
}

// TestUpdateAgentSettings_DropsForeignNonSecondaryAxes verifies the generalized strip:
// any axis a provider doesn't expose -- not just the secondary permission-mode/
// primary-agent axis -- is dropped. A `--effort` against Cursor (which bakes effort
// into the model id and has no effort axis) and a `--set allow_all=on` against Claude
// (a Copilot-only config option) must both be stripped rather than persisting a phantom and
// emitting a misleading settings_changed notification.
func TestUpdateAgentSettings_DropsForeignNonSecondaryAxes(t *testing.T) {
	cases := []struct {
		name     string
		provider leapmuxv1.AgentProvider
		foreign  string
	}{
		{"effort against Cursor", leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, agent.OptionIDEffort},
		{"allow_all against Claude", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, agent.CopilotConfigAllowAll},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

			require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
				ID:            "agent-1",
				WorkspaceID:   "ws-1",
				WorkingDir:    t.TempDir(),
				HomeDir:       t.TempDir(),
				AgentProvider: tc.provider,
				Options:       marshalOptions(map[string]string{agent.OptionIDModel: "auto"}),
			}))
			svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

			dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
				AgentId:  "agent-1",
				Settings: &leapmuxv1.AgentSettings{Options: map[string]string{tc.foreign: "high"}},
			}, w)

			require.Empty(t, w.errors)
			dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
			require.NoError(t, err)
			_, persisted := parseOptions(dbAgent.Options)[tc.foreign]
			assert.Falsef(t, persisted, "a foreign %q must not be persisted on %s", tc.foreign, tc.provider)
		})
	}
}

// TestUpdateAgentSettings_KeepsKnownProviderExtra verifies the strip does NOT drop a
// provider-private axis the provider genuinely exposes: a Codex sandbox-policy change
// (a declared extra in KnownOptionIDs, not a static well-known axis) is persisted.
func TestUpdateAgentSettings_KeepsKnownProviderExtra(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "gpt-5.5"}),
	}))
	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-1",
		Settings: &leapmuxv1.AgentSettings{
			Options: map[string]string{agent.CodexOptionSandboxPolicy: agent.CodexSandboxReadOnly},
		},
	}, w)

	require.Empty(t, w.errors)
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, agent.CodexSandboxReadOnly,
		parseOptions(dbAgent.Options)[agent.CodexOptionSandboxPolicy],
		"a known Codex provider extra (sandbox_policy) must be persisted, not stripped")
}

// TestUpdateAgentSettings_KeepsKnownWellKnownAxis is the positive counterpart to the
// foreign-axis strips: a well-known axis the provider DOES own (an offline effort edit
// on Claude) must survive the generalized validation and persist. This guards against an
// over-aggressive KnownOptionIDs silently dropping a legitimate setting.
func TestUpdateAgentSettings_KeepsKnownWellKnownAxis(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "sonnet"}),
	}))
	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-1"}, w)

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  "agent-1",
		Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDEffort: agent.EffortHigh}},
	}, w)

	require.Empty(t, w.errors)
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, agent.EffortHigh, parseOptions(dbAgent.Options)[agent.OptionIDEffort],
		"a well-known axis Claude owns (effort) must persist, not be stripped")
}

// TestUpdateAgentSettings_ResponseCarriesConfirmedOptions verifies the RPC reply returns
// the settled options the client reconciles its optimistic state against -- here the
// switched model -- so the reconcile doesn't depend on a separately-broadcast catalog.
// For Cursor (no effort axis) the reply carries no effort key.
func TestUpdateAgentSettings_ResponseCarriesConfirmedOptions(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "auto"}),
	}))

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId:  "agent-1",
		Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDModel: "composer-2.5"}},
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.UpdateAgentSettingsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	assert.Equal(t, "composer-2.5", resp.ConfirmedOptions[agent.OptionIDModel],
		"the reply carries the settled model the client reconciles against")
	_, hasEffort := resp.ConfirmedOptions[agent.OptionIDEffort]
	assert.False(t, hasEffort, "Cursor's reply carries no effort (it has no effort axis)")
}

// TestApplySettingsViaRestartBroadcastsConfirmedCatalog guards the restart-apply path: the
// RPC response carries only confirmed option values, so the freshly registered provider's
// option-group catalog must be broadcast separately after the restart handoff persists it.
func TestApplySettingsViaRestartBroadcastsConfirmedCatalog(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, withWorkspaces("ws-1"))
	const agentID = "agent-1"

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "opus[1m]", agent.OptionIDEffort: "high"}),
	}))
	sink := svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:       agentID,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		WorkingDir:    t.TempDir(),
		Options:       map[string]string{agent.OptionIDModel: "opus[1m]", agent.OptionIDEffort: "high"},
	}, sink)
	require.NoError(t, err)
	defer svc.Agents.StopAgent(agentID)

	restartCalls := 0
	svc.startAgentFn = mockAgentStarter(t, svc, func(agent.Options) { restartCalls++ })

	svc.Watchers.SetAgentWatches(w.channelID, []string{agentID}, w)

	dbAgent, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	settled := svc.applySettingsViaRestart(dbAgent, map[string]string{
		agent.OptionIDModel:  "opus[1m]",
		agent.OptionIDEffort: agent.EffortAuto,
	})
	assert.NotEmpty(t, settled[agent.OptionIDEffort])
	assert.Equal(t, 1, restartCalls, "settings restart must use the injectable starter so unit tests do not require a real agent binary")

	var sawCatalog bool
	for _, stream := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if proto.Unmarshal(stream.GetPayload(), &resp) != nil {
			continue
		}
		sc := resp.GetAgentEvent().GetStatusChange()
		if sc != nil && len(sc.GetOptionGroups()) > 0 {
			sawCatalog = true
			break
		}
	}
	assert.True(t, sawCatalog, "restart-applied settings must broadcast the confirmed option-group catalog")
}

func mockAgentStarter(t *testing.T, svc *Service, onStart func(agent.Options)) func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
	t.Helper()
	return func(ctx context.Context, opts agent.Options, sink agent.OutputSink) (map[string]string, error) {
		if onStart != nil {
			onStart(opts)
		}
		confirmed, err := svc.Agents.MockStartAgent(ctx, opts, sink)
		if err != nil {
			return nil, err
		}
		confirmed[agent.OptionIDModel] = agent.NormalizeModelID(opts.AgentProvider, opts.Model())
		confirmed[agent.OptionIDEffort] = opts.Effort()
		confirmed[agent.OptionIDPermissionMode] = agent.PermissionModeOrDefault(opts.AgentProvider, opts.PermissionMode())
		return confirmed, nil
	}
}

// mustMarshalOptionGroups marshals a catalog for a test fixture, failing the test on error. Test
// fixtures are always valid (no invalid-UTF-8 labels), so an error means a fixture bug rather than
// the runtime truncation marshalOptionGroups now guards against.
func mustMarshalOptionGroups(t *testing.T, groups []*leapmuxv1.AvailableOptionGroup) string {
	t.Helper()
	s, err := marshalOptionGroups(groups)
	require.NoError(t, err)
	return s
}

// lastSettingsChangedChanges decodes the most recent broadcast and returns the
// `changes` map of its settings_changed notification (handling the case where the
// notification is wrapped in a messages array).
func lastSettingsChangedChanges(t *testing.T, w *testResponseWriter) map[string]any {
	t.Helper()
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
			if candidate, ok := entry.(map[string]any); ok && candidate["type"] == "settings_changed" {
				payload = candidate
				break
			}
		}
	}
	require.Equal(t, "settings_changed", payload["type"])
	changes, ok := payload["changes"].(map[string]any)
	require.True(t, ok)
	return changes
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
		Options:       `{"primaryAgent":"build"}`,
	}))

	_, err := svc.persistConfirmedAgentSettings(
		"agent-opencode",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		loadOptions(`{"primaryAgent":"build"}`, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE), // stored (current row)
		confirmedOptions(
			leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
			loadOptions(`{"primaryAgent":"build"}`, leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE), // requested
			map[string]string{
				agent.OptionIDModel:        "openai/gpt-5",
				agent.OptionIDPrimaryAgent: "plan",
			},
		), // final = requested overlaid with the confirmed values + provider defaults
	)
	require.NoError(t, err)

	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-opencode")
	require.NoError(t, err)
	persisted := loadOptions(dbAgent.Options, dbAgent.AgentProvider)
	assert.Equal(t, "openai/gpt-5", persisted[agent.OptionIDModel])
	assert.Equal(t, "plan", persisted[agent.OptionIDPrimaryAgent])
}

// TestPersistConfirmedAgentSettings_PersistsDiscoveredPrimaryAgentFromEmpty
// simulates the initial OpenAgent flow: an agent is created with empty
// options, then persistConfirmedAgentSettings is called with the discovered
// primary agent from the provider's confirmed option values. Verifies the
// primary agent is stored in the DB so that subsequent settings_changed
// notifications include a non-empty old value.
func TestPersistConfirmedAgentSettings_PersistsDiscoveredPrimaryAgentFromEmpty(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	// Agent created with empty options (like the OpenAgent handler does
	// when no extraSettings are provided by the frontend).
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-opencode",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		Options:       "{}",
	}))

	// Simulate what the OpenAgent handler does: call persistConfirmedAgentSettings
	// with empty requested options and confirmed options that include
	// the discovered primary agent.
	_, err := svc.persistConfirmedAgentSettings(
		"agent-opencode",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		nil, // stored (empty row for a new tab)
		confirmedOptions(
			leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
			nil, // requested options (empty for a new tab)
			map[string]string{
				agent.OptionIDModel:        "openai/gpt-5",
				agent.OptionIDPrimaryAgent: "build",
			},
		), // final = discovered confirmed values + provider defaults
	)
	require.NoError(t, err)

	// Verify the discovered primary agent was persisted.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-opencode")
	require.NoError(t, err)
	persisted := loadOptions(dbAgent.Options, dbAgent.AgentProvider)
	assert.Equal(t, "openai/gpt-5", persisted[agent.OptionIDModel])
	assert.Equal(t, "build", persisted[agent.OptionIDPrimaryAgent],
		"discovered primary agent should be persisted from empty initial state")

	// Now simulate the user changing the primary agent — the old value
	// should come from the DB and be non-empty.
	oldOptions := loadOptions(dbAgent.Options, dbAgent.AgentProvider)
	newOptions := mergeOptions(oldOptions, map[string]string{agent.OptionIDPrimaryAgent: "plan"})
	assert.Equal(t, "build", oldOptions[agent.OptionIDPrimaryAgent],
		"old options should contain the previously persisted primary agent")
	assert.Equal(t, "plan", newOptions[agent.OptionIDPrimaryAgent],
		"new options should reflect the requested change")
}

// TestPersistConfirmedAgentSettings_PreservesConcurrentlyMergedKey guards S1: the confirmed-
// settings persist writes the options column via compare-and-swap on its own delta, not a blind
// full-map write, so a key a server-initiated PersistSettingsRefresh merged onto the row after
// the restart snapshot was taken (the relaunched reader goroutine holds no lifecycle lock) is
// preserved rather than clobbered.
func TestPersistConfirmedAgentSettings_PreservesConcurrentlyMergedKey(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-cp",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "openai/gpt-5"}),
	}))

	// Simulate the race: a server-initiated refresh merged a NEW key (reasoning_effort) onto the
	// row AFTER the restart captured its stored snapshot (which still holds only the model).
	require.NoError(t, svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{agent.OptionIDModel: "openai/gpt-5", "reasoning_effort": "high"}),
		ID:      "agent-cp",
	}))

	// The restart persist knows only the pre-refresh snapshot (model) and confirms a clamped
	// model, forcing a non-empty delta -- so the CAS actually writes (and must merge, not clobber).
	_, err := svc.persistConfirmedAgentSettings(
		"agent-cp",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		map[string]string{agent.OptionIDModel: "openai/gpt-5"}, // stored (pre-refresh snapshot)
		confirmedOptions(
			leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
			map[string]string{agent.OptionIDModel: "openai/gpt-5"},       // requested
			map[string]string{agent.OptionIDModel: "openai/gpt-5-turbo"}, // confirmed (provider clamped the model)
		), // final
	)
	require.NoError(t, err)

	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-cp")
	require.NoError(t, err)
	persisted := loadOptions(dbAgent.Options, dbAgent.AgentProvider)
	assert.Equal(t, "openai/gpt-5-turbo", persisted[agent.OptionIDModel], "the confirmed clamp is applied")
	assert.Equal(t, "high", persisted["reasoning_effort"],
		"the concurrently-merged key survives the confirmed-settings persist (CAS merge, not blind clobber)")
}

func TestPersistConfirmedAgentSettings_PersistsAvailableOptionGroups(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-goose",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "auto"}),
	}))

	// Preload the manager cache with option groups (the model catalog is now
	// projected into the "model" group); simulates what startAgentWith would do
	// after the agent starts.
	groups := []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{
			{Id: "claude-sonnet-4", Name: "Claude Sonnet 4"},
			{Id: "gpt-5.4", Name: "GPT-5.4"},
		}},
		{Id: "thinkingBudget", Label: "Thinking Budget", Options: []*leapmuxv1.AvailableOption{
			{Id: "low", Name: "Low"},
			{Id: "high", Name: "High"},
		}},
	}
	svc.Agents.PreloadCache("agent-goose", groups)

	// Persist confirmed settings — should also persist the option-group catalog.
	_, err := svc.persistConfirmedAgentSettings(
		"agent-goose",
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		map[string]string{agent.OptionIDModel: "auto"}, // stored (current row)
		confirmedOptions(
			leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
			map[string]string{agent.OptionIDModel: "auto"}, // requested
			map[string]string{agent.OptionIDModel: "auto"}, // confirmed
		), // final
	)
	require.NoError(t, err)

	// Verify the option groups were persisted to DB.
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-goose")
	require.NoError(t, err)
	assert.NotEqual(t, "[]", dbAgent.OptionGroups, "option_groups should be populated")

	// Verify round-trip: unmarshal back and check values.
	parsedGroups := parseOptionGroups(dbAgent.OptionGroups)
	require.Len(t, parsedGroups, 2)
	modelGroup := optionids.GroupByID(parsedGroups, agent.OptionIDModel)
	require.NotNil(t, modelGroup)
	require.Len(t, modelGroup.GetOptions(), 2)
	assert.Equal(t, "claude-sonnet-4", modelGroup.GetOptions()[0].GetId())
	assert.Equal(t, "gpt-5.4", modelGroup.GetOptions()[1].GetId())

	thinkingGroup := optionids.GroupByID(parsedGroups, "thinkingBudget")
	require.NotNil(t, thinkingGroup)
	assert.Len(t, thinkingGroup.GetOptions(), 2)
}

// TestSettleConfirmedOptions_DropsReconciledAwayProviderDefault guards S1: settleConfirmedOptions
// runs reconcileOrphanedOptions AFTER confirmedOptions, so a provider-default axis the running
// session does NOT surface is dropped rather than resurrected by resolveProviderDefaults. Codex
// declares a sandbox_policy default; given a confirmed snapshot that surfaces only the model (a
// hypothetical session that dropped the sandbox axis), confirmedOptions re-stamps the default while
// settleConfirmedOptions drops it. The always-live model axis survives either way.
func TestSettleConfirmedOptions_DropsReconciledAwayProviderDefault(t *testing.T) {
	codex := leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	requested := OptionMap{agent.OptionIDModel: "gpt-5"} // no sandbox axis requested
	confirmed := OptionMap{agent.OptionIDModel: "gpt-5"} // the session surfaces only the model

	// confirmedOptions alone re-fills the sandbox provider default for the unsurfaced axis...
	reStamped := confirmedOptions(codex, requested, confirmed)
	assert.Equal(t, agent.CodexDefaultSandboxPolicy, reStamped[agent.CodexOptionSandboxPolicy],
		"confirmedOptions re-stamps the provider default for the unsurfaced sandbox axis")

	// ...while settleConfirmedOptions reconciles it away because the session doesn't surface it.
	settled := settleConfirmedOptions(codex, requested, surfacedOptions(confirmed))
	_, hasSandbox := settled[agent.CodexOptionSandboxPolicy]
	assert.False(t, hasSandbox, "an unsurfaced provider-default axis is dropped, not resurrected")
	assert.Equal(t, "gpt-5", settled[agent.OptionIDModel], "the always-live model axis is kept")
}

// TestBuildSettingsChanges_OrphanedAxisNotAnnounced guards against a spurious "Label (old -> )"
// settings_changed entry for an axis a model switch DROPPED. reconcileOrphanedOptions removes an
// axis the relaunched session no longer surfaces (effort, after switching to a model without an
// effort axis), so the settled value is empty while the prior value was concrete. buildSettingsChanges
// must omit such an axis -- there is no new value to name, and emitting it renders a dangling arrow
// with a blank target -- while still announcing the genuine model change that caused the drop.
func TestBuildSettingsChanges_OrphanedAxisNotAnnounced(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	dbAgent := &db.Agent{ID: "agent-1", AgentProvider: claude, OptionGroups: "[]"}

	// Isolated orphan: model unchanged, effort dropped. Nothing should be announced.
	old := OptionMap{agent.OptionIDModel: "opus", agent.OptionIDEffort: "high"}
	settledSameModel := OptionMap{agent.OptionIDModel: "opus"}
	changes := svc.buildSettingsChanges(dbAgent, old, settledSameModel, sortedOptionKeys(old, settledSameModel), true)
	_, hasEffort := changes[agent.OptionIDEffort]
	assert.False(t, hasEffort, "an orphaned axis (settled value empty) must not be announced")
	assert.Empty(t, changes, "no spurious change for an axis that simply disappeared")

	// Model switch that drops effort: the model change IS announced, the orphaned effort is NOT.
	settledNewModel := OptionMap{agent.OptionIDModel: "sonnet"}
	changes = svc.buildSettingsChanges(dbAgent, old, settledNewModel, sortedOptionKeys(old, settledNewModel), true)
	_, hasEffort = changes[agent.OptionIDEffort]
	assert.False(t, hasEffort, "the orphaned effort is still suppressed on a real model switch")
	assert.Contains(t, changes, agent.OptionIDModel, "the genuine model change is still announced")
}

// TestSetAgentOptionGroupsIfUnchanged_KeepsConcurrentCatalog guards the synchronous catalog CAS:
// a (re)start handoff's option_groups write is a no-op when the column moved on (a richer catalog
// a running provider discovered concurrently landed in between), so the newer catalog is kept --
// the synchronous mirror of UpdateAgentConfirmedSettingsPreservingStartedSettings's
// expected_option_groups guard, which persistConfirmedAgentSettings now uses.
func TestSetAgentOptionGroupsIfUnchanged_KeepsConcurrentCatalog(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-cas", WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "auto"}),
	}))

	// A running provider discovered a richer catalog (two models) and persisted it.
	richer := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "m1"}, {Id: "m2"}}},
	})
	require.NoError(t, svc.Queries.SetAgentOptionGroups(ctx, db.SetAgentOptionGroupsParams{OptionGroups: richer, ID: "agent-cas"}))

	// A handoff that read a STALE expected (the empty default the row no longer holds) writes a
	// narrower catalog: the CAS does not match, so it is a no-op and the richer catalog stays.
	narrower := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "m1"}}},
	})
	rows, err := svc.Queries.SetAgentOptionGroupsIfUnchanged(ctx, db.SetAgentOptionGroupsIfUnchangedParams{
		OptionGroups: narrower, ExpectedOptionGroups: "[]", ID: "agent-cas",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), rows, "a mismatched expected catalog is a no-op")
	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-cas")
	require.NoError(t, err)
	assert.Equal(t, richer, dbAgent.OptionGroups, "the concurrently-discovered richer catalog is kept, not clobbered")

	// When the expected DOES match the row, the write lands.
	rows, err = svc.Queries.SetAgentOptionGroupsIfUnchanged(ctx, db.SetAgentOptionGroupsIfUnchangedParams{
		OptionGroups: narrower, ExpectedOptionGroups: richer, ID: "agent-cas",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows, "a matching expected catalog writes")
	dbAgent, err = svc.Queries.GetAgentByID(ctx, "agent-cas")
	require.NoError(t, err)
	assert.Equal(t, narrower, dbAgent.OptionGroups, "a matching CAS replaces the catalog")
}

// seedConfirmedSettingsAgent creates a Claude agent with the given options and catalog for the
// casPersistConfirmedSettings tests, returning its just-read row snapshot.
func seedConfirmedSettingsAgent(t *testing.T, svc *Service, id string, options map[string]string, catalog string) db.Agent {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: id, WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       marshalOptions(options),
	}))
	require.NoError(t, svc.Queries.SetAgentOptionGroups(ctx, db.SetAgentOptionGroupsParams{OptionGroups: catalog, ID: id}))
	row, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	return row
}

// TestCasPersistConfirmedSettings_AtomicWritePreservesConcurrentKeyAndWritesCatalog guards [S7]: the
// atomic two-column confirmed-settings write merges the options DELTA onto a row a concurrent writer
// moved (preserving its unrelated key) AND writes the catalog in the SAME statement, so the two
// columns move together rather than via two separately-observable writes.
func TestCasPersistConfirmedSettings_AtomicWritePreservesConcurrentKeyAndWritesCatalog(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	priorCatalog := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}}},
	})
	snap := seedConfirmedSettingsAgent(t, svc, "agent-cps", map[string]string{agent.OptionIDModel: "opus"}, priorCatalog)

	// A concurrent writer adds an unrelated key (effort) the delta won't touch, moving the options row.
	require.NoError(t, svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{agent.OptionIDModel: "opus", agent.OptionIDEffort: "high"}), ID: "agent-cps",
	}))

	newCatalog := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "sonnet"}}},
	})
	row, err := casPersistConfirmedSettings(ctx, svc.Queries, "agent-cps", snap.Options,
		map[string]string{agent.OptionIDModel: "sonnet"}, snap.OptionGroups, newCatalog)
	require.NoError(t, err)

	got := parseOptions(row.Options)
	assert.Equal(t, "sonnet", got[agent.OptionIDModel], "the delta is applied")
	assert.Equal(t, "high", got[agent.OptionIDEffort], "the concurrent writer's key is preserved by the merge")
	assert.Equal(t, newCatalog, row.OptionGroups, "the catalog rode the same atomic write")
}

// TestCasPersistConfirmedSettings_KeepsConcurrentlyDiscoveredRicherCatalog guards [S7]: when a
// running provider discovered a richer catalog after the handoff snapshot, the atomic write applies
// the options but the gated catalog CAS no-ops, keeping the richer catalog rather than clobbering it.
func TestCasPersistConfirmedSettings_KeepsConcurrentlyDiscoveredRicherCatalog(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	priorCatalog := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}}},
	})
	snap := seedConfirmedSettingsAgent(t, svc, "agent-cps2", map[string]string{agent.OptionIDModel: "opus"}, priorCatalog)

	// A running provider discovers a richer catalog AFTER the snapshot and persists it concurrently.
	richer := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}, {Id: "sonnet"}}},
	})
	require.NoError(t, svc.Queries.SetAgentOptionGroups(ctx, db.SetAgentOptionGroupsParams{OptionGroups: richer, ID: "agent-cps2"}))

	narrowCatalog := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "sonnet"}}},
	})
	row, err := casPersistConfirmedSettings(ctx, svc.Queries, "agent-cps2", snap.Options,
		map[string]string{agent.OptionIDModel: "sonnet"}, snap.OptionGroups, narrowCatalog)
	require.NoError(t, err)

	assert.Equal(t, "sonnet", parseOptions(row.Options)[agent.OptionIDModel], "the options still apply")
	assert.Equal(t, richer, row.OptionGroups, "the concurrently-discovered richer catalog is kept, not clobbered")
}

// TestCasPersistConfirmedSettings_EmptyCatalogParamsLeaveCatalogUntouched guards the [S3] catalog-
// marshal-failure path: persistConfirmedAgentSettings passes "" / "" so the options still persist
// while the stored catalog is left intact rather than overwritten with a truncated one.
func TestCasPersistConfirmedSettings_EmptyCatalogParamsLeaveCatalogUntouched(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	priorCatalog := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}}},
	})
	snap := seedConfirmedSettingsAgent(t, svc, "agent-cps3", map[string]string{agent.OptionIDModel: "opus"}, priorCatalog)

	row, err := casPersistConfirmedSettings(ctx, svc.Queries, "agent-cps3", snap.Options,
		map[string]string{agent.OptionIDModel: "sonnet"}, "", "")
	require.NoError(t, err)
	assert.Equal(t, "sonnet", parseOptions(row.Options)[agent.OptionIDModel], "the options still apply")
	assert.Equal(t, priorCatalog, row.OptionGroups, "empty catalog params leave the stored catalog untouched")
}

// TestCasPersistConfirmedSettings_AssertsCatalogWhenIdenticalBlobLandedConcurrently guards against
// dropping a discovered catalog when an options-only writer (casPersistAgentOptions via
// PersistSettingsRefresh/applyOptionChanges) lands the IDENTICAL options blob this handoff would
// write, BEFORE the handoff's CAS runs. That writer never touches option_groups, so the atomic
// statement's options CASE takes ELSE (options != base) and the gated option_groups CASE takes ELSE
// too. Without the standalone re-assertion the early-return would then skip the catalog, leaving the
// row with the staler narrower one -- which an exiting agent (no follow-up BroadcastStatusActive)
// never converges. The handoff's richer catalog must still land.
func TestCasPersistConfirmedSettings_AssertsCatalogWhenIdenticalBlobLandedConcurrently(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	narrowerCatalog := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "sonnet"}}},
	})
	// The row begins on opus with a narrow catalog; the handoff snapshot captures this catalog.
	snap := seedConfirmedSettingsAgent(t, svc, "agent-cps-blob", map[string]string{agent.OptionIDModel: "opus"}, narrowerCatalog)

	// A concurrent options-only writer (casPersistAgentOptions' SetAgentOptions path) lands EXACTLY
	// the blob the handoff is about to write (model=sonnet), and never touches option_groups.
	identicalBlob := marshalOptions(map[string]string{agent.OptionIDModel: "sonnet"})
	require.NoError(t, svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{Options: identicalBlob, ID: "agent-cps-blob"}))

	// The handoff discovered a RICHER catalog (model gained sonnet alongside the original opus).
	richerCatalog := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}, {Id: "sonnet"}}},
	})
	row, err := casPersistConfirmedSettings(ctx, svc.Queries, "agent-cps-blob", snap.Options,
		map[string]string{agent.OptionIDModel: "sonnet"}, snap.OptionGroups, richerCatalog)
	require.NoError(t, err)

	assert.Equal(t, "sonnet", parseOptions(row.Options)[agent.OptionIDModel], "options already settled by the concurrent writer")
	assert.Equal(t, richerCatalog, row.OptionGroups,
		"the discovered richer catalog is re-asserted, not dropped for the staler narrower one")
}

// TestCasPersistConfirmedSettings_KeepsRicherCatalogWhenIdenticalBlobAndCatalogGrewConcurrently
// guards the safety of the re-assertion: when BOTH an options-only writer landed the identical blob
// AND a running provider grew the catalog past expectedCatalog, the standalone catalog CAS must
// no-op (its expected_option_groups no longer matches) and keep the richer concurrently-discovered
// catalog rather than clobbering it with this handoff's own catalog.
func TestCasPersistConfirmedSettings_KeepsRicherCatalogWhenIdenticalBlobAndCatalogGrewConcurrently(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	priorCatalog := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}}},
	})
	snap := seedConfirmedSettingsAgent(t, svc, "agent-cps-grew", map[string]string{agent.OptionIDModel: "opus"}, priorCatalog)

	// Options-only writer lands the identical blob (model=sonnet), never touching option_groups.
	require.NoError(t, svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{agent.OptionIDModel: "sonnet"}), ID: "agent-cps-grew",
	}))
	// A running provider concurrently discovered an even richer catalog and persisted it.
	concurrentRicher := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}, {Id: "sonnet"}, {Id: "haiku"}}},
	})
	require.NoError(t, svc.Queries.SetAgentOptionGroups(ctx, db.SetAgentOptionGroupsParams{OptionGroups: concurrentRicher, ID: "agent-cps-grew"}))

	// The handoff carries its own (narrower-than-concurrent) catalog.
	handoffCatalog := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}, {Id: "sonnet"}}},
	})
	row, err := casPersistConfirmedSettings(ctx, svc.Queries, "agent-cps-grew", snap.Options,
		map[string]string{agent.OptionIDModel: "sonnet"}, snap.OptionGroups, handoffCatalog)
	require.NoError(t, err)

	assert.Equal(t, "sonnet", parseOptions(row.Options)[agent.OptionIDModel], "options already settled by the concurrent writer")
	assert.Equal(t, concurrentRicher, row.OptionGroups,
		"the concurrently-discovered richer catalog is kept; the standalone re-assertion no-ops")
}

// TestMarshalOptionGroups_ErrorsRatherThanTruncating guards [S3]: a group that can't be marshaled
// (an invalid-UTF-8 label) makes the whole call error instead of silently dropping the group and
// persisting a truncated catalog that would churn the column on every push.
func TestMarshalOptionGroups_ErrorsRatherThanTruncating(t *testing.T) {
	good := &leapmuxv1.AvailableOptionGroup{Id: agent.OptionIDModel, Label: "Model"}
	bad := &leapmuxv1.AvailableOptionGroup{Id: agent.OptionIDEffort, Label: "\xff\xfe"} // invalid UTF-8

	_, err := marshalOptionGroups([]*leapmuxv1.AvailableOptionGroup{good, bad})
	require.Error(t, err, "a group that can't be marshaled must error, not be silently dropped")

	s, err := marshalOptionGroups([]*leapmuxv1.AvailableOptionGroup{good})
	require.NoError(t, err, "a valid catalog still marshals")
	assert.Contains(t, s, agent.OptionIDModel)
}

func TestUpdateAgentSettings_BroadcastsGoosePermissionModeLabels(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-goose",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:          "auto",
			agent.OptionIDPermissionMode: "auto",
		}),
	}))

	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-goose"}, w)

	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: "agent-goose",
		Settings: &leapmuxv1.AgentSettings{
			Options: map[string]string{agent.OptionIDPermissionMode: "approve"},
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
	assert.Equal(t, "auto", change["old"])
	assert.Equal(t, "approve", change["new"])
	assert.Equal(t, "Auto", change["oldLabel"])
	assert.Equal(t, "Approve", change["newLabel"])
}

// TestNotifyPermissionModeChanged_ResolvesLabels verifies the SERVER-initiated mode
// notification (an ACP config_option_update routed through the sink, not the RPC path)
// resolves display labels at the emit site -- so it doesn't depend on the frontend
// settings-label cache being primed and never renders a raw mode id.
func TestNotifyPermissionModeChanged_ResolvesLabels(t *testing.T) {
	ctx := context.Background()
	svc, _, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-goose",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		Options:       marshalOptions(map[string]string{agent.OptionIDModel: "auto", agent.OptionIDPermissionMode: "auto"}),
	}))

	svc.Watchers.SetAgentWatches(w.channelID, []string{"agent-goose"}, w)

	sink := svc.Output.NewSink("agent-goose", leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE)
	sink.NotifyPermissionModeChanged("auto", "approve")

	require.Empty(t, w.errors)
	require.NotEmpty(t, w.streams)

	changes := lastSettingsChangedChanges(t, w)
	change, ok := changes["permissionMode"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "auto", change["old"])
	assert.Equal(t, "approve", change["new"])
	assert.Equal(t, "Auto", change["oldLabel"], "labels are resolved at the emit site, not left to the frontend cache")
	assert.Equal(t, "Approve", change["newLabel"])
	assert.Equal(t, "Mode", change["label"])
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
	require.NoError(t, svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{agent.OptionIDPermissionMode: "default"}),
		ID:      "agent-1",
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
	assert.Equal(t, "bypassPermissions", parseOptions(dbAgent.Options)[agent.OptionIDPermissionMode],
		"permission mode should be persisted to DB when set_permission_mode is sent while agent is running")
}

// TestSendAgentRawMessage_SetPermissionModeIgnoredForNonClaudeProvider pins the intended
// narrowing after moving the parse behind the Provider interface: only Claude speaks
// set_permission_mode, so a Claude-shaped frame sent to a non-Claude (Codex) agent extracts no mode
// and does NOT eagerly write the DB -- it falls through to the generic raw-forward path.
func TestSendAgentRawMessage_SetPermissionModeIgnoredForNonClaudeProvider(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkspaceID:   "ws-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{agent.OptionIDPermissionMode: "untrusted"}),
		ID:      "agent-1",
	}))

	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:    "agent-1",
		WorkingDir: t.TempDir(),
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX))
	require.NoError(t, err)
	defer svc.Agents.StopAgent("agent-1")

	dispatch(d, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: "agent-1",
		Content: `{"type":"control_request","request_id":"r1","request":{"subtype":"set_permission_mode","mode":"bypassPermissions"}}`,
	}, w)

	require.Empty(t, w.errors)

	dbAgent, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, "untrusted", parseOptions(dbAgent.Options)[agent.OptionIDPermissionMode],
		"a Claude-shaped set_permission_mode frame must not eagerly rewrite a Codex agent's permission mode")
}

// TestConfirmedOptions verifies the settled-options overlay that the settings_changed
// notification, the corrective DB write, and the RPC reply all share: confirmed
// (non-empty) values override the requested base on EVERY axis -- not just model/effort
// -- while empty/absent confirmed values keep the request. A nil confirmed map (offline
// edit / failed restart) yields the base unchanged.
func TestConfirmedOptions(t *testing.T) {
	const provider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR

	// nil confirmed -> base unchanged (offline edit / failed restart).
	got := confirmedOptions(provider, map[string]string{agent.OptionIDModel: "auto"}, nil)
	assert.Equal(t, "auto", got[agent.OptionIDModel])

	// Confirmed values override the request on every axis, including a clamped extra:
	// the sentinel resolved to a concrete model, effort clamped from ultracode, and a
	// server-downgraded reasoning_effort all flow through.
	got = confirmedOptions(provider,
		map[string]string{agent.OptionIDModel: "default", agent.OptionIDEffort: "ultracode", "reasoning_effort": "xhigh"},
		map[string]string{agent.OptionIDModel: "sonnet", agent.OptionIDEffort: "xhigh", "reasoning_effort": "high"})
	assert.Equal(t, "sonnet", got[agent.OptionIDModel], "confirmed model (sentinel resolved) overrides the request")
	assert.Equal(t, "xhigh", got[agent.OptionIDEffort], "confirmed effort (clamped from ultracode) overrides")
	assert.Equal(t, "high", got["reasoning_effort"], "a clamped extra axis is carried through, not just model/effort")

	// Empty/absent confirmed fields don't clobber the requested values.
	got = confirmedOptions(provider,
		map[string]string{agent.OptionIDModel: "opus[1m]"},
		map[string]string{agent.OptionIDEffort: "max"})
	assert.Equal(t, "opus[1m]", got[agent.OptionIDModel], "absent confirmed model keeps the requested model")
	assert.Equal(t, "max", got[agent.OptionIDEffort])
}

// TestConfirmedOptions_PreservesProviderPrivateExtra guards the invariant that a
// provider-private axis persisted as an option but never surfaced as a group (Pi's
// pi_provider) survives a confirmed-settings overlay. CurrentOptions -- the source of
// the confirmed map -- omits it (it scans only groups), so it must be preserved from
// the BASE rather than dropped. See CurrentOptions' doc note.
func TestConfirmedOptions_PreservesProviderPrivateExtra(t *testing.T) {
	const pi = leapmuxv1.AgentProvider_AGENT_PROVIDER_PI
	got := confirmedOptions(pi,
		// base (the persisted row) carries pi_provider.
		map[string]string{agent.OptionIDModel: "deepseek-chat", agent.PiOptionProvider: "deepseek"},
		// confirmed (CurrentOptions(OptionGroups)) omits it -- Pi exposes no provider group.
		map[string]string{agent.OptionIDModel: "deepseek-chat"})
	assert.Equal(t, "deepseek", got[agent.PiOptionProvider],
		"a provider-private extra must survive from the base when the confirmed catalog omits it")
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

// modelOptionGroups builds a one-element catalog holding just the model group with
// the given (id, name) options. Shared by the settings-change label tests below.
func modelOptionGroups(idName ...[2]string) []*leapmuxv1.AvailableOptionGroup {
	opts := make([]*leapmuxv1.AvailableOption, 0, len(idName))
	for _, p := range idName {
		opts = append(opts, &leapmuxv1.AvailableOption{Id: p[0], Name: p[1]})
	}
	return []*leapmuxv1.AvailableOptionGroup{{Id: agent.OptionIDModel, Label: "Model", Options: opts}}
}

// TestBuildSettingsChanges_OldModelDroppedFromLiveCatalog reproduces the raw
// bracketed-id leak in the settings_changed notification. Switching away from a
// model the CLI surfaces only while it is active -- Claude Code hides the
// standard-context Opus behind "default", so the resolved "opus[1m]" is listed only
// while selected -- relaunches the agent onto a rebuilt catalog that no longer lists
// it, so resolving the OLD value's label against the live catalog leaks the raw id.
// The label must instead resolve against the catalog persisted on the agent row,
// captured while Opus was still the active model.
func TestBuildSettingsChanges_OldModelDroppedFromLiveCatalog(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE

	// Live (post-switch) catalog: relaunched onto Fable, so Opus is gone.
	liveFable := modelOptionGroups(
		[2]string{"default", "Default (recommended)"},
		[2]string{"fable[1m]", "Fable"},
		[2]string{"sonnet", "Sonnet"},
		[2]string{"haiku", "Haiku"},
	)
	// A real live/persisted catalog records the active model as its currentValue, which is
	// the stamp PreloadCache keys the cache on. buildSettingsChanges now resolves labels
	// against the SETTLED model's catalog ([C2]), so the cache stamp must match fable[1m]
	// for the cached liveFable (not the static fallback) to serve this offline read.
	liveFable[0].CurrentValue = "fable[1m]"
	// Persisted catalog from while Opus was active (ensureSettledModelListed had
	// surfaced opus[1m] into the picker for that session).
	prevOpus := modelOptionGroups(
		[2]string{"default", "Default (recommended)"},
		[2]string{"opus[1m]", "Opus (1M context)"},
		[2]string{"fable[1m]", "Fable"},
		[2]string{"sonnet", "Sonnet"},
		[2]string{"haiku", "Haiku"},
	)

	svc.Agents.PreloadCache("agent-x", liveFable)
	dbAgent := &db.Agent{ID: "agent-x", AgentProvider: claude, OptionGroups: mustMarshalOptionGroups(t, prevOpus)}

	changes := svc.buildSettingsChanges(dbAgent,
		map[string]string{agent.OptionIDModel: "opus[1m]"},
		map[string]string{agent.OptionIDModel: "fable[1m]"},
		[]string{agent.OptionIDModel},
		true,
	)

	entry, ok := changes[agent.OptionIDModel].(optionChangeEntry)
	require.True(t, ok, "a model change must be reported")
	assert.Equal(t, "opus[1m]", entry.Old)
	assert.Equal(t, "fable[1m]", entry.New)
	// The new model resolves from the live catalog; the OLD model -- absent from the
	// live catalog -- resolves from the persisted catalog instead of leaking raw.
	assert.Equal(t, "Fable", entry.NewLabel)
	assert.Equal(t, "Opus (1M context)", entry.OldLabel)
}

// TestBuildSettingsChanges_SkipsDefaultMaterializationFirstSet is the [S12] guard: a first set
// (oldVal=="") whose value is merely the axis's own DEFAULT being materialized is not a
// user-visible change and must NOT be announced. resolveProviderDefaults does not stamp
// permissionMode into oldOptions (only sanitizeIncomingOptions does, into the settled map), so the
// first settings edit on a fresh agent reads as ""->default; without this guard it would surface a
// spurious "Permission Mode (default)" the user never chose. A first set to a NON-default value IS
// a real choice and is still announced.
func TestBuildSettingsChanges_SkipsDefaultMaterializationFirstSet(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	provider := leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT // ACP: no model-dependent groups, cache served as-is

	// A catalog whose permissionMode group has DefaultValue "default".
	catalog := []*leapmuxv1.AvailableOptionGroup{
		{
			Id:           agent.OptionIDPermissionMode,
			Label:        "Permission Mode",
			DefaultValue: "default",
			CurrentValue: "default",
			Options: []*leapmuxv1.AvailableOption{
				{Id: "default", Name: "Default"},
				{Id: "plan", Name: "Plan"},
			},
		},
	}
	svc.Agents.PreloadCache("agent-pm", catalog)
	dbAgent := &db.Agent{ID: "agent-pm", AgentProvider: provider, OptionGroups: mustMarshalOptionGroups(t, catalog)}

	// First set of permissionMode to its DEFAULT -> materialization, not a user change -> skipped.
	changes := svc.buildSettingsChanges(dbAgent,
		map[string]string{}, // old lacks permissionMode (resolveProviderDefaults never stamps it)
		map[string]string{agent.OptionIDPermissionMode: "default"}, // settled stamps the default
		[]string{agent.OptionIDPermissionMode},
		true, // notifyFirstSet
	)
	_, reported := changes[agent.OptionIDPermissionMode]
	assert.False(t, reported, "a first set to the axis default (materialization) is not announced")

	// First set of permissionMode to a NON-default value -> a real user choice -> announced.
	changes = svc.buildSettingsChanges(dbAgent,
		map[string]string{},
		map[string]string{agent.OptionIDPermissionMode: "plan"},
		[]string{agent.OptionIDPermissionMode},
		true,
	)
	entry, reported := changes[agent.OptionIDPermissionMode].(optionChangeEntry)
	require.True(t, reported, "a first set to a non-default value is announced")
	assert.Equal(t, "", entry.Old)
	assert.Equal(t, "plan", entry.New)
}

// TestReconcileOrphanedOptions is the regression guard for [E12]: a relaunched session's
// COMPLETE surfaced snapshot reconciles away an axis the new model no longer exposes (a
// rejected/dropped option), while keeping the model axis and the provider's persisted-only
// extras (Pi's pi_provider, which is never surfaced as a group).
func TestReconcileOrphanedOptions(t *testing.T) {
	copilot := leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT
	pi := leapmuxv1.AgentProvider_AGENT_PROVIDER_PI

	// An option the relaunched session no longer surfaces (absent from `surfaced`) is dropped.
	got := reconcileOrphanedOptions(copilot,
		map[string]string{agent.OptionIDModel: "gpt-5.4", "reasoning_effort": "ultra"},
		surfacedOptions{agent.OptionIDModel: "gpt-5.4"})
	assert.NotContains(t, got, "reasoning_effort", "an orphaned option the new model dropped is reconciled away")
	assert.Equal(t, "gpt-5.4", got[agent.OptionIDModel])

	// A still-surfaced option is kept (with whatever the agent confirmed for it).
	got = reconcileOrphanedOptions(copilot,
		map[string]string{agent.OptionIDModel: "gpt-5.4", "reasoning_effort": "high"},
		surfacedOptions{agent.OptionIDModel: "gpt-5.4", "reasoning_effort": "high"})
	assert.Equal(t, "high", got["reasoning_effort"], "a surfaced option survives")

	// pi_provider is persisted-only (never surfaced), so its absence from `surfaced` is
	// expected -- it must be kept, not reconciled away.
	got = reconcileOrphanedOptions(pi,
		map[string]string{agent.OptionIDModel: "gpt-5.5", agent.PiOptionProvider: "openai-codex"},
		surfacedOptions{agent.OptionIDModel: "gpt-5.5"})
	assert.Equal(t, "openai-codex", got[agent.PiOptionProvider], "a persisted-only extra is preserved")

	// The model axis is always kept even if `surfaced` is empty.
	got = reconcileOrphanedOptions(copilot,
		map[string]string{agent.OptionIDModel: "gpt-5.4"},
		surfacedOptions{})
	assert.Equal(t, "gpt-5.4", got[agent.OptionIDModel])
}

// TestResolveOptionValueLabel covers the live-first / persisted-fallback / raw-id
// ladder the settings_changed notification uses to name option values.
func TestResolveOptionValueLabel(t *testing.T) {
	live := modelOptionGroups(
		[2]string{"fable[1m]", "Fable"},
		[2]string{"sonnet", "Sonnet"},
	)
	prev := modelOptionGroups(
		[2]string{"opus[1m]", "Opus (1M context)"},
		[2]string{"fable[1m]", "Fable (stale label)"},
	)

	// A value the live catalog still offers wins from the live catalog, even when
	// the persisted catalog carries a stale label for the same id.
	assert.Equal(t, "Fable", resolveOptionValueLabel(live, prev, agent.OptionIDModel, "fable[1m]"))
	// A value the live catalog dropped resolves from the persisted catalog.
	assert.Equal(t, "Opus (1M context)", resolveOptionValueLabel(live, prev, agent.OptionIDModel, "opus[1m]"))
	// A value in neither catalog falls back to the raw id rather than blanking.
	assert.Equal(t, "haiku", resolveOptionValueLabel(live, prev, agent.OptionIDModel, "haiku"))
	// A value under an unknown group key also falls back to the raw id.
	assert.Equal(t, "fable[1m]", resolveOptionValueLabel(live, prev, "effort", "fable[1m]"))
	// A nil persisted catalog is tolerated: live still resolves, missing falls to raw.
	assert.Equal(t, "Sonnet", resolveOptionValueLabel(live, nil, agent.OptionIDModel, "sonnet"))
	assert.Equal(t, "opus[1m]", resolveOptionValueLabel(live, nil, agent.OptionIDModel, "opus[1m]"))
}
