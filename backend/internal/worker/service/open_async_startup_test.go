package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// TestOpenAgent_SyncPrologueReturnsFast asserts that even when startAgent
// blocks for seconds, the OpenAgent RPC response lands in the test writer
// within ~200 ms — the whole point of the OpenAgent split.
func TestOpenAgent_SyncPrologueReturnsFast(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	released := make(chan struct{})
	svc.startAgentFn = func(ctx context.Context, _ agent.Options, _ agent.OutputSink) (map[string]string, error) {
		select {
		case <-released:
		case <-ctx.Done():
		}
		return map[string]string{}, nil
	}
	defer close(released)

	start := time.Now()
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	syncLatency := time.Since(start)

	assert.Less(t, syncLatency, 200*time.Millisecond,
		"sync prologue must return well under 200 ms even when startAgent blocks; got %v", syncLatency)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.NotNil(t, resp.GetAgent())
	// Initial response carries STARTING — runtime fields (session_id,
	// available_models) are filled in by the subsequent AgentStatusChange.
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, resp.GetAgent().GetStatus(),
		"OpenAgent should return with STARTING before subprocess startup completes")
}

// TestOpenAgent_DelayedStartupBroadcastsActive asserts the goroutine
// emits ACTIVE once startAgent eventually returns.
func TestOpenAgent_DelayedStartupBroadcastsActive(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	releaseAfter := 150 * time.Millisecond
	svc.startAgentFn = func(_ context.Context, _ agent.Options, _ agent.OutputSink) (map[string]string, error) {
		time.Sleep(releaseAfter)
		return map[string]string{}, nil
	}

	// Subscribe before opening so the broadcast is captured.
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	// Watch the agent to capture broadcasts.
	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST}},
	}, wWatch)

	require.Eventually(t, func() bool {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			ae := resp.GetAgentEvent()
			if ae == nil {
				continue
			}
			if sc := ae.GetStatusChange(); sc != nil {
				if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
					return true
				}
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected ACTIVE broadcast after delayed startup")
}

func TestOpenAgent_SettingsChangedDuringStartupSurviveActiveBroadcast(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	startCalled := make(chan agent.Options, 1)
	releaseStart := make(chan struct{})
	released := false
	release := func() {
		if !released {
			close(releaseStart)
			released = true
		}
	}
	defer release()

	svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
		startCalled <- opts
		select {
		case <-releaseStart:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return opts.Options, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	var startedOpts agent.Options
	select {
	case startedOpts = <-startCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn not invoked within 5s")
	}
	require.Equal(t, agent.CodexDefaultCollaborationMode, startedOpts.Options[agent.CodexOptionCollaborationMode])

	wUpdate := newTestWriter()
	dispatch(d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
		AgentId: agentID,
		Settings: &leapmuxv1.AgentSettings{
			Options: map[string]string{
				agent.CodexOptionCollaborationMode: agent.CodexCollaborationPlan,
			},
		},
	}, wUpdate)
	require.Empty(t, wUpdate.errors)

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, agent.CodexCollaborationPlan, loadOptions(row.Options, row.AgentProvider)[agent.CodexOptionCollaborationMode])

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST}},
	}, wWatch)

	release()

	var activeStatus *leapmuxv1.AgentStatusChange
	require.Eventually(t, func() bool {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			sc := resp.GetAgentEvent().GetStatusChange()
			if sc == nil || sc.GetStatus() != leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
				continue
			}
			activeStatus = sc
			return true
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected ACTIVE broadcast after releasing startup")

	require.NotNil(t, activeStatus)
	assert.Equal(t, agent.CodexCollaborationPlan, optionids.CurrentValue(activeStatus.GetOptionGroups(), agent.CodexOptionCollaborationMode))

	row, err = svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, agent.CodexCollaborationPlan, loadOptions(row.Options, row.AgentProvider)[agent.CodexOptionCollaborationMode])
}

func TestRelaunchForStartupSettingsChangeUsesInjectedStarter(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	const agentID = "agent-startup-relaunch"
	const provider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	workingDir := t.TempDir()
	initialOptions := map[string]string{
		agent.OptionIDModel:          "opus[1m]",
		agent.OptionIDEffort:         "high",
		agent.OptionIDPermissionMode: agent.PermissionModeDefault,
	}
	relaunchOptions := map[string]string{
		agent.OptionIDModel:          "sonnet",
		agent.OptionIDEffort:         agent.EffortAuto,
		agent.OptionIDPermissionMode: agent.PermissionModePlan,
	}
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            agentID,
		WorkspaceID:   "ws-1",
		WorkingDir:    workingDir,
		HomeDir:       t.TempDir(),
		AgentProvider: provider,
		// In production the startup handoff has already persisted the latest settings before
		// relaunchForStartupSettingsChange bounces the process to apply them.
		Options: marshalOptions(relaunchOptions),
	}))
	fallback, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)

	sink := svc.Output.NewSink(agentID, provider)
	_, err = svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID:       agentID,
		AgentProvider: provider,
		WorkingDir:    workingDir,
		Options:       initialOptions,
	}, sink)
	require.NoError(t, err)
	defer svc.Agents.StopAgent(agentID)

	restartCalls := 0
	var restarted agent.Options
	svc.startAgentFn = mockAgentStarter(t, svc, func(opts agent.Options) {
		restartCalls++
		restarted = opts
	})

	relaunchOpts := svc.baseAgentOptions(agentID, workingDir, provider)
	relaunchOpts.Options = relaunchOptions
	active := svc.relaunchForStartupSettingsChange(agentID, provider, relaunchOpts, fallback)

	require.Equal(t, 1, restartCalls, "startup-time relaunch must use the injectable starter so tests do not require a real agent binary")
	assert.Equal(t, relaunchOpts.Options, restarted.Options)

	stored, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, active.Options, stored.Options)
	got := loadOptions(stored.Options, provider)
	assert.Equal(t, "sonnet", got[agent.OptionIDModel])
	assert.Equal(t, agent.EffortAuto, got[agent.OptionIDEffort])
	assert.Equal(t, agent.PermissionModePlan, got[agent.OptionIDPermissionMode])
}

func TestOpenAgent_RawPermissionModeChangedDuringStartupSurvivesActiveBroadcast(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	startCalled := make(chan agent.Options, 1)
	releaseStart := make(chan struct{})
	released := false
	release := func() {
		if !released {
			close(releaseStart)
			released = true
		}
	}
	defer release()

	svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
		startCalled <- opts
		select {
		case <-releaseStart:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		confirmed := opts.Options
		confirmed[agent.OptionIDPermissionMode] = agent.PermissionModeDefault
		return confirmed, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	select {
	case <-startCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn not invoked within 5s")
	}

	wRaw := newTestWriter()
	dispatch(d, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: agentID,
		Content: `{"type":"control_request","request_id":"test-set-mode","request":{"subtype":"set_permission_mode","mode":"plan"}}`,
	}, wRaw)
	require.Empty(t, wRaw.errors)

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	require.Equal(t, agent.PermissionModePlan, loadOptions(row.Options, row.AgentProvider)[agent.OptionIDPermissionMode])

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST}},
	}, wWatch)

	release()

	var activeStatus *leapmuxv1.AgentStatusChange
	require.Eventually(t, func() bool {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			sc := resp.GetAgentEvent().GetStatusChange()
			if sc == nil || sc.GetStatus() != leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
				continue
			}
			activeStatus = sc
			return true
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected ACTIVE broadcast after releasing startup")

	require.NotNil(t, activeStatus)
	assert.Equal(t, agent.PermissionModePlan, optionids.CurrentValue(activeStatus.GetOptionGroups(), agent.OptionIDPermissionMode))

	row, err = svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, agent.PermissionModePlan, loadOptions(row.Options, row.AgentProvider)[agent.OptionIDPermissionMode])
}

func TestPersistConfirmedAgentSettingsPreservesLatePermissionModeChange(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	agentID := "agent-late-mode"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          agentID,
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Title:       "late mode",
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:          "opus",
			agent.OptionIDEffort:         "high",
			agent.OptionIDPermissionMode: agent.PermissionModeDefault,
		}),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Resumed:       0,
	}))

	startedOpts := agent.Options{
		AgentID: agentID,
		Options: map[string]string{
			agent.OptionIDModel:          "opus",
			agent.OptionIDEffort:         "high",
			agent.OptionIDPermissionMode: agent.PermissionModeDefault,
		},
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}
	latestOpts := startedOpts

	// The snapshot the handoff captured (matching latestOpts), read BEFORE the late change. The real
	// path passes dbAgent.Options from the same re-read that produced latestOpts, so the CAS guard is
	// this pre-change column form -- not the post-change one.
	snapRow, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)

	// A late permission-mode change (plan) lands AFTER the handoff captured its snapshot -- the
	// active-persist window the CAS guards. The live column moves past the snapshot.
	require.NoError(t, svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:          "opus",
			agent.OptionIDEffort:         "high",
			agent.OptionIDPermissionMode: agent.PermissionModePlan,
		}),
		ID: agentID,
	}))

	activeRow, err := svc.persistConfirmedAgentSettingsPreservingStartedSettings(
		agentID,
		snapRow.Options,
		latestOpts,
		map[string]string{agent.OptionIDPermissionMode: agent.PermissionModeDefault},
		snapRow.OptionGroups,
	)
	require.NoError(t, err)
	assert.Equal(t, agent.PermissionModePlan, loadOptions(activeRow.Options, activeRow.AgentProvider)[agent.OptionIDPermissionMode])

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, agent.PermissionModePlan, loadOptions(row.Options, row.AgentProvider)[agent.OptionIDPermissionMode])
}

func TestPersistConfirmedAgentSettingsPreservesPreStartPermissionModeChange(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	agentID := "agent-pre-start-mode"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          agentID,
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Title:       "pre-start mode",
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:          "opus",
			agent.OptionIDEffort:         "high",
			agent.OptionIDPermissionMode: agent.PermissionModePlan,
		}),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Resumed:       0,
	}))

	initialOpts := agent.Options{
		AgentID: agentID,
		Options: map[string]string{
			agent.OptionIDModel:          "opus",
			agent.OptionIDEffort:         "high",
			agent.OptionIDPermissionMode: agent.PermissionModeDefault,
		},
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}
	startedOpts := initialOpts
	// Give startedOpts its own option map so flipping permission mode here does
	// not mutate initialOpts (the two share the same map after the struct copy).
	startedOpts.Options = map[string]string{
		agent.OptionIDModel:          "opus",
		agent.OptionIDEffort:         "high",
		agent.OptionIDPermissionMode: agent.PermissionModePlan,
	}
	latestOpts := startedOpts

	confirmed := confirmedSettingsPreservingStartupChanges(
		map[string]string{agent.OptionIDPermissionMode: agent.PermissionModeDefault},
		initialOpts,
		latestOpts,
	)
	preRow, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	activeRow, err := svc.persistConfirmedAgentSettingsPreservingStartedSettings(
		agentID,
		preRow.Options,
		latestOpts,
		confirmed,
		preRow.OptionGroups,
	)
	require.NoError(t, err)
	assert.Equal(t, agent.PermissionModePlan, loadOptions(activeRow.Options, activeRow.AgentProvider)[agent.OptionIDPermissionMode])

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, agent.PermissionModePlan, loadOptions(row.Options, row.AgentProvider)[agent.OptionIDPermissionMode])
}

// TestPersistConfirmedAgentSettingsAppliesConfirmedModelDespiteOtherAxisChange
// guards the compare-and-swap regression: when the user changes ONE axis (effort)
// mid-startup, the provider's confirmed resolution of an UNCHANGED axis (the model
// sentinel -> a concrete model) must still be persisted. A whole-blob compare
// against the launch options would discard the entire confirmed blob here, leaving
// the row stuck on the unresolved "default" sentinel.
func TestPersistConfirmedAgentSettingsAppliesConfirmedModelDespiteOtherAxisChange(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	agentID := "agent-effort-change-mid-startup"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          agentID,
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Title:       "effort change",
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:          agent.DefaultModelSentinel,
			agent.OptionIDEffort:         "high",
			agent.OptionIDPermissionMode: agent.PermissionModeDefault,
		}),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Resumed:       0,
	}))

	// The subprocess was launched with the sentinel model and effort=high.
	initialOpts := agent.Options{
		AgentID: agentID,
		Options: map[string]string{
			agent.OptionIDModel:          agent.DefaultModelSentinel,
			agent.OptionIDEffort:         "high",
			agent.OptionIDPermissionMode: agent.PermissionModeDefault,
		},
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}

	// The user lowers effort while startup is finishing; the DB row diverges from
	// the launch options on the effort axis only.
	require.NoError(t, svc.Queries.SetAgentOptions(ctx, db.SetAgentOptionsParams{
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:          agent.DefaultModelSentinel,
			agent.OptionIDEffort:         "low",
			agent.OptionIDPermissionMode: agent.PermissionModeDefault,
		}),
		ID: agentID,
	}))
	latestRow, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	latestOpts := applyDBSettingsToAgentOptions(initialOpts, &latestRow)

	// The provider confirms the launch settings: the sentinel resolved to a
	// concrete model, and effort=high (what it launched with).
	confirmed := confirmedSettingsPreservingStartupChanges(
		map[string]string{
			agent.OptionIDModel:  "claude-opus",
			agent.OptionIDEffort: "high",
		},
		initialOpts,
		latestOpts,
	)
	// The changed effort axis is dropped from the confirmed blob; the unchanged
	// model axis is kept.
	assert.Equal(t, "claude-opus", confirmed[agent.OptionIDModel])
	assert.NotContains(t, confirmed, agent.OptionIDEffort)

	activeRow, err := svc.persistConfirmedAgentSettingsPreservingStartedSettings(
		agentID,
		latestRow.Options,
		latestOpts,
		confirmed,
		latestRow.OptionGroups,
	)
	require.NoError(t, err)
	persisted := loadOptions(activeRow.Options, activeRow.AgentProvider)
	assert.Equal(t, "claude-opus", persisted[agent.OptionIDModel], "confirmed model resolution must survive")
	assert.Equal(t, "low", persisted[agent.OptionIDEffort], "user's mid-startup effort change must be preserved")

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	stored := loadOptions(row.Options, row.AgentProvider)
	assert.Equal(t, "claude-opus", stored[agent.OptionIDModel])
	assert.Equal(t, "low", stored[agent.OptionIDEffort])
}

// TestPersistConfirmedAgentSettings_AppliesConfirmedModelWhenColumnLacksDefaultAxis guards the
// CAS-guard regression: the options CAS expectation must be the row's OWN serialized form, not a
// recomputed resolveProviderDefaults(latest). When a mid-startup refresh CLEARS a default-valued
// axis (here effort is absent from the column), resolveProviderDefaults re-fills it (effort=auto),
// so a recomputed expectation would never match the column -- the options CASE would silently take
// ELSE and the entire confirmed blob (including the sentinel->concrete model resolution) would be
// discarded. Guarding on the column's canonical form makes the model resolution land.
func TestPersistConfirmedAgentSettings_AppliesConfirmedModelWhenColumnLacksDefaultAxis(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	agentID := "agent-cleared-default-axis"
	// The stored column is MISSING the effort axis (a refresh cleared it). It is NOT a fixed point
	// of resolveProviderDefaults, which would re-fill effort=auto for a Claude agent.
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		Title: "cleared default axis",
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:          agent.DefaultModelSentinel,
			agent.OptionIDPermissionMode: agent.PermissionModeDefault,
		}),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	require.NotContains(t, parseOptions(row.Options), agent.OptionIDEffort,
		"precondition: the stored column lacks the effort axis")
	// latest is the column re-loaded with provider defaults (so effort=auto reappears here), exactly
	// as the startup handoff builds it -- but the column itself still lacks effort.
	latest := applyDBSettingsToAgentOptions(agent.Options{AgentID: agentID}, &row)
	require.Equal(t, agent.EffortAuto, latest.Options[agent.OptionIDEffort],
		"precondition: resolveProviderDefaults re-fills effort on load, so it diverges from the column")

	// The provider confirms the sentinel resolved to a concrete model.
	activeRow, err := svc.persistConfirmedAgentSettingsPreservingStartedSettings(
		agentID,
		row.Options,
		latest,
		map[string]string{agent.OptionIDModel: "claude-opus"},
		row.OptionGroups,
	)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus", loadOptions(activeRow.Options, activeRow.AgentProvider)[agent.OptionIDModel],
		"the confirmed model resolution must persist despite the column lacking a default-valued axis")

	stored, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "claude-opus", loadOptions(stored.Options, stored.AgentProvider)[agent.OptionIDModel])
}

// TestPersistConfirmedAgentSettings_DoesNotClobberConcurrentCatalog is the regression guard for
// the option_groups CAS: a richer catalog a running provider discovers AFTER the startup handoff
// read the row (persisted via SetAgentOptionGroups on a separate, unsynchronized path) must NOT
// be overwritten by the handoff's narrower startup catalog. The handoff CAS-guards the
// option_groups write against the snapshot it read, so when the column has since moved on the
// catalog write is skipped and the discovered catalog survives.
func TestPersistConfirmedAgentSettings_DoesNotClobberConcurrentCatalog(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	agentID := "agent-catalog-cas"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkspaceID: "ws-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		Title: "catalog cas",
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:  "opus",
			agent.OptionIDEffort: "high",
		}),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// The snapshot the handoff captured when it read the row, BEFORE any concurrent discovery.
	preRow, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	staleExpected := preRow.OptionGroups

	// A running provider discovers a richer catalog AFTER that read and persists it on the
	// separate SetAgentOptionGroups path (no shared lock with the handoff).
	richCatalog := mustMarshalOptionGroups(t, []*leapmuxv1.AvailableOptionGroup{
		{Id: agent.OptionIDModel, Label: "Model", Options: []*leapmuxv1.AvailableOption{{Id: "opus"}, {Id: "discovered-model"}}},
	})
	require.NotEqual(t, staleExpected, richCatalog, "precondition: the discovered catalog differs from the handoff's snapshot")
	require.NoError(t, svc.Queries.SetAgentOptionGroups(ctx, db.SetAgentOptionGroupsParams{
		OptionGroups: richCatalog, ID: agentID,
	}))

	latest := agent.Options{
		AgentID:       agentID,
		Options:       map[string]string{agent.OptionIDModel: "opus", agent.OptionIDEffort: "high"},
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}
	// The handoff lands LATE, carrying its stale snapshot as the CAS expectation.
	_, err = svc.persistConfirmedAgentSettingsPreservingStartedSettings(agentID, preRow.Options, latest, map[string]string{}, staleExpected)
	require.NoError(t, err)

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, richCatalog, row.OptionGroups,
		"the concurrently-discovered catalog must survive: the handoff CAS skips its write when the column moved on")
}

func TestOpenAgent_CodexUsesProviderDefaultPermissionMode(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	startCalled := make(chan agent.Options, 1)
	svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
		startCalled <- opts
		return opts.Options, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	var startedOpts agent.Options
	select {
	case startedOpts = <-startCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn not invoked within 5s")
	}
	assert.Equal(t, agent.CodexDefaultApprovalPolicy, startedOpts.PermissionMode())

	require.Eventually(t, func() bool {
		row, err := svc.Queries.GetAgentByID(ctx, agentID)
		return err == nil && loadOptions(row.Options, row.AgentProvider)[agent.OptionIDPermissionMode] == agent.CodexDefaultApprovalPolicy
	}, 5*time.Second, 20*time.Millisecond, "expected Codex permission mode to be stored as provider default")
}

// TestOpenAgent_ResponseHasNilGitStatus asserts that the immediate
// OpenAgent response carries no gitStatus — it's deliberately moved
// off the sync path and emitted via a subsequent STARTING broadcast so
// the RPC returns without forking `git status`.
func TestOpenAgent_ResponseHasNilGitStatus(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	blocked := make(chan struct{})
	svc.startAgentFn = func(ctx context.Context, _ agent.Options, _ agent.OutputSink) (map[string]string, error) {
		select {
		case <-blocked:
		case <-ctx.Done():
		}
		return map[string]string{}, nil
	}
	defer close(blocked)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    initRepo(t),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)

	var resp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Nil(t, resp.GetAgent().GetGitStatus(),
		"OpenAgent response must not carry gitStatus — it's deferred to the async startup broadcast")
}

// TestOpenTerminal_CatchUpReplaySurfacesStartupMessage regression-tests
// the bug where a newly-opened terminal tab showed an option "Starting
// terminal…" label instead of the backend-provided "Starting <shell>…".
//
// The client subscribes to WatchEvents only AFTER receiving the
// OpenTerminal response, so the sync-path STARTING broadcast lands with
// no watchers registered. The fix stores the phase label in the
// TerminalStartup registry so deriveTerminalStatus → the WatchEvents
// catch-up replay can surface it to the just-arriving subscriber.
func TestOpenTerminal_CatchUpReplaySurfacesStartupMessage(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)
	// Block the PTY spawn indefinitely so the terminal stays in STARTING
	// long enough for the WatchEvents catch-up replay to read the
	// registry.
	blocked := make(chan struct{})
	svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		<-blocked
		return nil
	}
	defer close(blocked)

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  t.TempDir(),
		Shell:       "/bin/zsh",
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	terminalID := openResp.GetTerminalId()
	require.NotEmpty(t, terminalID)

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: terminalID}},
	}, wWatch)

	// Catch-up replay fires synchronously during the WatchEvents handler,
	// so the streams slice is already populated when dispatch returns.
	var sawStartingWithMsg bool
	for _, s := range wWatch.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		sc := resp.GetTerminalEvent().GetStatusChange()
		if sc == nil {
			continue
		}
		if sc.GetStatus() == leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING {
			assert.Equal(t, "Starting zsh…", sc.GetStartupMessage(),
				"catch-up replay must surface the phase label the OpenTerminal sync prologue registered")
			sawStartingWithMsg = true
		}
	}
	assert.True(t, sawStartingWithMsg,
		"expected a STARTING statusChange in the catch-up replay")
}

// TestOpenTerminal_ResolvesDefaultShellForStartupMessage covers the
// frontend path: handleOpenTerminal sends shell="" and expects the
// backend to pick the default. The startup-panel label must therefore
// name the *resolved* binary ("Starting zsh…"), not fall back to a
// generic "Starting terminal…". Regression test for the bug where the
// label was computed from r.GetShell() before resolution.
func TestOpenTerminal_ResolvesDefaultShellForStartupMessage(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)
	blocked := make(chan struct{})
	svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		<-blocked
		return nil
	}
	defer close(blocked)

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  t.TempDir(),
		Shell:       "", // frontend default: let the backend resolve.
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	terminalID := openResp.GetTerminalId()

	_, _, msg, ok := svc.TerminalStartup.status(terminalID)
	require.True(t, ok, "terminal should still be in the STARTING registry (PTY spawn is blocked)")
	assert.True(t, strings.HasPrefix(msg, "Starting "),
		"startup message should start with 'Starting ' — got %q", msg)
	assert.NotEqual(t, "Starting terminal…", msg,
		"startup message must name the resolved shell, not the option fallback (got %q)", msg)
}

// TestListTerminals_SurfacesRegistryStartupMessage verifies that the
// ListTerminals handler includes startup_message on the TerminalInfo
// for terminals that are currently STARTING in the registry. Without
// this, a client refreshing mid-startup (e.g. hard reload during PTY
// spawn) falls back to the option "Starting terminal…" label.
func TestListTerminals_SurfacesRegistryStartupMessage(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)
	blocked := make(chan struct{})
	svc.startTerminalFn = func(context.Context, terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		<-blocked
		return nil
	}
	defer close(blocked)

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  t.TempDir(),
		Shell:       "/usr/bin/fish",
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	terminalID := openResp.GetTerminalId()

	wList := newTestWriter()
	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{
		TabIds: []string{terminalID},
	}, wList)
	require.Empty(t, wList.errors)
	require.Len(t, wList.responses, 1)

	var listResp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(wList.responses[0].GetPayload(), &listResp))
	require.Len(t, listResp.GetTerminals(), 1)
	ti := listResp.GetTerminals()[0]
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, ti.GetStatus())
	assert.Equal(t, "Starting fish…", ti.GetStartupMessage())
}

func TestOpenTerminal_TitlePersistedBeforePTYRegistrationHydratesManagerMeta(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	releaseStart := make(chan struct{})
	defer func() {
		select {
		case <-releaseStart:
		default:
			close(releaseStart)
		}
	}()
	svc.startTerminalFn = func(ctx context.Context, opts terminal.Options, outFn terminal.OutputHandler, exitFn terminal.ExitHandler) error {
		<-releaseStart
		return svc.Terminals.StartTerminal(ctx, opts, outFn, exitFn)
	}

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  t.TempDir(),
		Shell:       testutil.TestShell(),
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	terminalID := openResp.GetTerminalId()
	require.NotEmpty(t, terminalID)

	const title = "Terminal Ada"
	wTitle := newTestWriter()
	dispatch(d, "UpdateTerminalTitle", &leapmuxv1.UpdateTerminalTitleRequest{
		WorkspaceId: "ws-1",
		TerminalId:  terminalID,
		Title:       title,
	}, wTitle)
	require.Empty(t, wTitle.errors)
	rowBeforeStart, err := svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	require.Equal(t, title, rowBeforeStart.Title)

	wResize := newTestWriter()
	dispatch(d, "ResizeTerminal", &leapmuxv1.ResizeTerminalRequest{
		WorkspaceId: "ws-1",
		TerminalId:  terminalID,
		Cols:        100,
		Rows:        40,
	}, wResize)
	require.Empty(t, wResize.errors)

	close(releaseStart)

	require.Eventually(t, func() bool {
		meta, ok := svc.Terminals.GetMeta(terminalID)
		return ok && meta.Title == title
	}, 5*time.Second, 20*time.Millisecond,
		"terminal manager metadata should pick up the DB title written before PTY registration")

	svc.Shutdown()

	row, err := svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	assert.Equal(t, title, row.Title,
		"shutdown snapshot must not overwrite the pre-start title with empty manager metadata")

	testutil.RegisterTerminalCleanup(t, svc.Terminals, terminalID)
}

// TestOpenAgent_CatchUpReplaySurfacesStartupMessage mirrors the
// terminal regression test for agents: a WatchEvents subscriber that
// attaches after the initial STARTING broadcast should see the current
// phase label via catch-up replay, not an empty string.
func TestOpenAgent_CatchUpReplaySurfacesStartupMessage(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)
	// Block startAgent so the goroutine settles after setting phase 2
	// ("Starting Claude Code…") and waits there — the registry entry
	// then holds that phase label for replay.
	blocked := make(chan struct{})
	svc.startAgentFn = func(ctx context.Context, _ agent.Options, _ agent.OutputSink) (map[string]string, error) {
		select {
		case <-blocked:
		case <-ctx.Done():
		}
		return map[string]string{}, nil
	}
	defer close(blocked)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    initRepo(t),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()

	// Wait until the goroutine has progressed past phase 2 so the
	// registry holds "Starting Claude Code…" (the last registered
	// message before startAgent blocks).
	require.Eventually(t, func() bool {
		_, _, msg, ok := svc.AgentStartup.status(agentID)
		return ok && msg == "Starting Claude Code…"
	}, 5*time.Second, 20*time.Millisecond,
		"expected runAgentStartup to reach phase 2 before startAgent blocks")

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST}},
	}, wWatch)

	var sawStartingWithMsg bool
	for _, s := range wWatch.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		sc := resp.GetAgentEvent().GetStatusChange()
		if sc == nil || sc.GetStatus() != leapmuxv1.AgentStatus_AGENT_STATUS_STARTING {
			continue
		}
		assert.Equal(t, "Starting Claude Code…", sc.GetStartupMessage(),
			"catch-up replay must surface the phase label stored in the registry")
		sawStartingWithMsg = true
	}
	assert.True(t, sawStartingWithMsg,
		"expected a STARTING statusChange in the catch-up replay")
}

// TestOpenAgent_ActiveBroadcastCarriesGitStatus asserts that the final
// ACTIVE broadcast emitted by runAgentStartup carries the gitStatus
// computed in the pre-startAgent phase. Phase-ordering of the
// intermediate STARTING broadcasts is not asserted here (they may land
// before the test's WatchEvents subscription registers, since the
// runAgentStartup goroutine fires concurrently with the RPC response);
// TestBuildAgentStatusChange verifies the phase/error/gitStatus field
// mapping directly, race-free.
func TestOpenAgent_ActiveBroadcastCarriesGitStatus(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	// Block startAgent briefly so the test can subscribe before ACTIVE lands.
	svc.startAgentFn = func(_ context.Context, _ agent.Options, _ agent.OutputSink) (map[string]string, error) {
		time.Sleep(100 * time.Millisecond)
		return map[string]string{}, nil
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    initRepo(t),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST}},
	}, wWatch)

	require.Eventually(t, func() bool {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			sc := resp.GetAgentEvent().GetStatusChange()
			if sc == nil || sc.GetStatus() != leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
				continue
			}
			assert.NotNil(t, sc.GetGitStatus(), "ACTIVE broadcast must carry gitStatus")
			return true
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected ACTIVE broadcast with gitStatus")
}

// TestBuildAgentStatusChange covers the per-status AgentStatusChange
// constructors. Race-free companion to TestOpenAgent_ActiveBroadcastCarriesGitStatus:
// locks in the field mapping without routing through the broadcast fan-out.
func TestBuildAgentStatusChange(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	dbAgent := &db.Agent{
		ID:            "agent-bac",
		WorkspaceID:   "ws-1",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options: marshalOptions(map[string]string{
			agent.OptionIDModel:  "opus[1m]",
			agent.OptionIDEffort: "xhigh",
		}),
		WorkingDir: t.TempDir(),
	}

	t.Run("STARTING carries startupMessage and gitStatus, no option groups", func(t *testing.T) {
		gs := &leapmuxv1.AgentGitStatus{Branch: "main", OriginUrl: "https://example.com/repo.git"}
		sc := buildAgentStartingStatus(dbAgent, "Checking Git status…", gs)
		assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, sc.GetStatus())
		assert.Equal(t, "Checking Git status…", sc.GetStartupMessage())
		assert.Empty(t, sc.GetStartupError())
		assert.Same(t, gs, sc.GetGitStatus(), "gitStatus must flow through without a copy")
		// OptionGroups are deliberately skipped for non-ACTIVE so a STARTING
		// broadcast doesn't overwrite the frontend's last-known catalog with an
		// empty slice.
		assert.Empty(t, sc.GetOptionGroups())
	})

	t.Run("STARTUP_FAILED carries startupError and gitStatus", func(t *testing.T) {
		gs := &leapmuxv1.AgentGitStatus{Branch: "main"}
		sc := buildAgentFailedStatus(dbAgent, "exec: claude: not found", gs)
		assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, sc.GetStatus())
		assert.Equal(t, "exec: claude: not found", sc.GetStartupError())
		assert.Empty(t, sc.GetStartupMessage())
		assert.Same(t, gs, sc.GetGitStatus())
	})

	t.Run("STARTING with empty message and nil gitStatus", func(t *testing.T) {
		sc := buildAgentStartingStatus(dbAgent, "", nil)
		assert.Nil(t, sc.GetGitStatus(), "nil gitStatus must flow through unchanged (no auto-fetch)")
		assert.Empty(t, sc.GetStartupMessage())
		assert.Empty(t, sc.GetStartupError())
	})

	t.Run("ACTIVE attaches available models and option groups", func(t *testing.T) {
		sc := svc.buildAgentActiveStatus(dbAgent, nil)
		assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, sc.GetStatus())
		// Catalogs are populated from the Agent Manager, which is empty in
		// this test — but the important invariant is that ACTIVE is the
		// only status that even looks at the Manager for catalogs.
		assert.Empty(t, sc.GetStartupError())
		assert.Empty(t, sc.GetStartupMessage())
	})
}

// TestBuildTerminalStatusChange covers the per-status TerminalStatusChange
// constructors, mirroring TestBuildAgentStatusChange.
func TestBuildTerminalStatusChange(t *testing.T) {
	t.Run("STARTING carries startupMessage, empty error", func(t *testing.T) {
		sc := buildTerminalStartingStatus("term-1", "Starting zsh…", nil)
		assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, sc.GetStatus())
		assert.Equal(t, "Starting zsh…", sc.GetStartupMessage())
		assert.Empty(t, sc.GetStartupError())
	})

	t.Run("STARTUP_FAILED carries startupError, empty message", func(t *testing.T) {
		sc := buildTerminalFailedStatus("term-1", "fork: permission denied")
		assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, sc.GetStatus())
		assert.Equal(t, "fork: permission denied", sc.GetStartupError())
		assert.Empty(t, sc.GetStartupMessage())
	})

	t.Run("READY produces blank message and error", func(t *testing.T) {
		sc := buildTerminalReadyStatus("term-1")
		assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, sc.GetStatus())
		assert.Empty(t, sc.GetStartupError())
		assert.Empty(t, sc.GetStartupMessage())
	})
}

// TestOpenAgent_StartupFailurePhaseCarriesGitStatus asserts that when
// startAgent fails, the STARTUP_FAILED broadcast still carries the
// gitStatus computed during the pre-startAgent phase, so the frontend
// can render branch info alongside the error.
func TestOpenAgent_StartupFailurePhaseCarriesGitStatus(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)
	svc.startAgentFn = func(_ context.Context, _ agent.Options, _ agent.OutputSink) (map[string]string, error) {
		return nil, errors.New("forced startup failure")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    initRepo(t),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()

	wWatch := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: agentID, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST}},
	}, wWatch)

	require.Eventually(t, func() bool {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			sc := resp.GetAgentEvent().GetStatusChange()
			if sc == nil {
				continue
			}
			if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED {
				assert.NotEmpty(t, sc.GetStartupError(), "STARTUP_FAILED must carry the error")
				assert.NotNil(t, sc.GetGitStatus(),
					"STARTUP_FAILED must still carry gitStatus computed pre-startAgent")
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected STARTUP_FAILED broadcast")
}

// TestOpenAgent_StartupFailureBroadcastsFailureAndRollsBack asserts that
// a startAgentFn error produces a STARTUP_FAILED status with the error
// string visible to a subscribed watcher, and that the agent is closed.
func TestOpenAgent_StartupFailureBroadcastsFailureAndRollsBack(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	var startCalls sync.WaitGroup
	startCalls.Add(1)
	svc.startAgentFn = func(_ context.Context, _ agent.Options, _ agent.OutputSink) (map[string]string, error) {
		defer startCalls.Done()
		return nil, errors.New("forced startup failure: boom")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	startCalls.Wait()

	// Agent's DB row stays open after startup failure — the in-tab
	// error UI remains reachable across page refreshes; the user
	// dismisses the failed agent via the "Close tab" button which
	// calls CloseAgent.
	require.Eventually(t, func() bool {
		row, err := svc.Queries.GetAgentByID(ctx, agentID)
		return err == nil && !row.ClosedAt.Valid
	}, 5*time.Second, 20*time.Millisecond, "agent DB row should stay open after startup failure")

	// Startup registry should report STARTUP_FAILED with the error.
	require.Eventually(t, func() bool {
		status, errStr, _, ok := svc.AgentStartup.status(agentID)
		return ok && status == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED && errStr != ""
	}, 5*time.Second, 20*time.Millisecond, "expected Startup registry to retain STARTUP_FAILED")

}

// TestExecuteCreateWorktree_FailureIsRecoverable is a unit test of the
// executeGitMode mutation path: when `git worktree add` fails (here
// because the repo root is invalid), the helper reports the error without
// leaving any persistent state. validateGitMode would have caught the bad
// input in a real RPC, but this bypass proves the execute-side error path
// is clean — the caller gets err, no rollback metadata, and no DB row.
func TestExecuteCreateWorktree_FailureIsRecoverable(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	bogusRoot := t.TempDir() // not a git repo
	plan := gitModePlan{
		Mode:         gitModeCreateWorktree,
		WorkingDir:   bogusRoot,
		RepoRoot:     bogusRoot,
		BranchName:   "feature/x",
		StartPoint:   "HEAD",
		WorktreePath: filepath.Join(bogusRoot, "wt"),
	}

	result, err := svc.executeCreateWorktree(context.Background(), plan)
	require.Error(t, err)
	assert.False(t, result.Rollback.HasPartialMutation(),
		"a worktree add that never created the dir should not signal partial mutation")
}

// TestOpenAgent_BroadcastsRollbackLabelOnStartFailure asserts the
// STARTING-phase broadcast sequence when phase 0 succeeds but phase 2
// (subprocess startup) fails: the tab sees "Creating worktree …" then
// "Rolling back worktree …" then STARTUP_FAILED with the injected error.
func TestOpenAgent_BroadcastsRollbackLabelOnStartFailure(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		return nil, errors.New("forced start failure")
	}

	// Subscribe synchronously inside the OpenAgent sync prologue — before
	// runAgentStartup is spawned — so every phase broadcast lands on the
	// watcher. Catch-up replay alone is racy: it only surfaces the current
	// registry message, so if the goroutine advances to phase 1 before a
	// post-OpenAgent WatchEvents dispatch reads the registry, the phase-0
	// "Creating worktree" label is lost (observed on Windows CI).
	wWatch := newTestWriter()
	svc.createAgentRecordFn = func(ctx context.Context, params db.CreateAgentParams) error {
		if err := svc.Queries.CreateAgent(ctx, params); err != nil {
			return err
		}
		svc.Watchers.WatchAgent(params.ID, &EventWatcher{
			ChannelID: wWatch.channelID,
			Sender:    channel.NewSender(wWatch),
		})
		return nil
	}

	repoDir := initRepo(t)
	branchName := "feature/rollback-label"
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
		AgentProvider:  leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()

	// Drain STATUS_CHANGE broadcasts until we see the final STARTUP_FAILED.
	require.Eventually(t, func() bool {
		for _, s := range wWatch.streamsSnapshot() {
			var resp leapmuxv1.WatchEventsResponse
			if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
				continue
			}
			if sc := resp.GetAgentEvent().GetStatusChange(); sc != nil && sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "expected STARTUP_FAILED after start failure")

	// Walk the full sequence. Because the watcher is subscribed before
	// runAgentStartup spawns, every STARTING broadcast lands live; we
	// require the phase-0 label AND the rollback label to both appear
	// before the STARTUP_FAILED.
	var sawPhase0, sawRollback, sawFailed bool
	var failedError string
	for _, s := range wWatch.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		sc := resp.GetAgentEvent().GetStatusChange()
		if sc == nil {
			continue
		}
		switch sc.GetStatus() {
		case leapmuxv1.AgentStatus_AGENT_STATUS_STARTING:
			if strings.Contains(sc.GetStartupMessage(), "Creating worktree") && strings.Contains(sc.GetStartupMessage(), branchName) {
				sawPhase0 = true
			}
			if strings.Contains(sc.GetStartupMessage(), "Rolling back worktree") && strings.Contains(sc.GetStartupMessage(), branchName) {
				sawRollback = true
			}
		case leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED:
			sawFailed = true
			failedError = sc.GetStartupError()
		}
	}
	assert.True(t, sawPhase0, `expected STARTING "Creating worktree %q…" broadcast`, branchName)
	assert.True(t, sawRollback, `expected STARTING "Rolling back worktree %q…" broadcast`, branchName)
	assert.True(t, sawFailed, "expected final STARTUP_FAILED broadcast")
	assert.Contains(t, failedError, "forced start failure")

	// After rollback completes, the worktree dir and branch should be gone.
	waitForStartupFailure(t, svc, agentID)
	assert.NoDirExists(t, expectedWorktreePath(repoDir, branchName))
	assert.False(t, localBranchExists(t, repoDir, branchName))
}

// TestExecuteGitMode_HonorsCtxCancellation verifies executeGitMode exits
// early when the context is cancelled between shell-outs. This is the
// unit-level guarantee that CloseAgent / CloseTerminal mid-phase-0 does
// not run expensive git commands against a doomed tab.
func TestExecuteGitMode_HonorsCtxCancellation(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before call

	result, err := svc.executeGitMode(ctx, gitModePlan{Mode: gitModeUseCurrent, WorkingDir: t.TempDir()})
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, result.Rollback.HasPartialMutation())
}

// TestBuildTerminalStatusChange_CarriesGitInfo locks in the wire contract
// that replaced the dropped OpenTerminalResponse.git_branch /
// git_origin_url / git_toplevel fields: runTerminalStartup's phase-1
// STARTING broadcast populates these on the TerminalStatusChange, and the
// frontend reads them into the tab. A regression here (e.g. someone
// re-pointing the proto fields) would be caught without depending on
// goroutine timing.
func TestBuildTerminalStatusChange_CarriesGitInfo(t *testing.T) {
	sc := buildTerminalStartingStatus("term-1", "Starting zsh…", &leapmuxv1.AgentGitStatus{
		Branch:    "feature/x",
		OriginUrl: "git@example.com:org/repo.git",
		Toplevel:  "/home/u/repo",
	})
	assert.Equal(t, "term-1", sc.GetTerminalId())
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, sc.GetStatus())
	assert.Equal(t, "Starting zsh…", sc.GetStartupMessage())
	assert.Equal(t, "feature/x", sc.GetGitBranch())
	assert.Equal(t, "git@example.com:org/repo.git", sc.GetGitOriginUrl())
	assert.Equal(t, "/home/u/repo", sc.GetGitToplevel())
}
