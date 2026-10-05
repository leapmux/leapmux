//go:build unix

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// processChangeRig drives one change of the agent process and reads the status
// that ListAgents reports at the step boundaries. It replaces the provider with a
// process that runs until the Manager stops it, and it reads the status from
// inside the launch: at that moment the Manager has stopped the old process and
// has not registered the new one.
type processChangeRig struct {
	t        *testing.T
	svc      *Service
	d        *channel.Dispatcher
	id       string
	provider leapmuxv1.AgentProvider
	// failLaunch makes the launch return an error after it reads the status.
	failLaunch bool
	// duringLaunch is the status that each launch read, in order.
	duringLaunch []leapmuxv1.AgentStatus
	// processAtLaunch is whether the Manager held a process when a launch began.
	processAtLaunch []bool
	// watcher receives every status change that the change broadcasts.
	watcher *testResponseWriter
}

func newProcessChangeRig(t *testing.T) *processChangeRig {
	t.Helper()
	svc, d, _ := setupTestService(t)
	rig := &processChangeRig{t: t, svc: svc, d: d, id: "agent-changing", provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}
	svc.startAgentFn = rig.launch
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(rig.id) })
	rig.watcher = newTestWriter()
	registerAgentWatch(svc, "relaunch-watch", rig.id, leapmuxv1.WatchMode_WATCH_MODE_FULL, rig.watcher)
	return rig
}

// lastAnnounced is the last status that the change broadcast to the watcher, or
// UNSPECIFIED when it broadcast none. A settings-only change carries no status
// and does not count.
func (r *processChangeRig) lastAnnounced() leapmuxv1.AgentStatus {
	r.t.Helper()
	last := leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED
	for _, stream := range r.watcher.streamsSnapshot() {
		if change := decodeWatchAgentEvent(r.t, stream).GetStatusChange(); change != nil && change.GetStatus() != leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED {
			last = change.GetStatus()
		}
	}
	return last
}

// seedRunning creates the row of an agent that has run before, and registers its
// process. It is the state that a restart, a clear and a forced stop start from.
func (r *processChangeRig) seedRunning() db.Agent {
	r.t.Helper()
	seedOpenAgent(r.t, r.svc, r.id, true)
	row := requireAgentRow(r.t, r.svc, r.id)
	r.registerProcess(context.Background(), agent.Options{AgentID: r.id, AgentProvider: r.provider, WorkingDir: row.WorkingDir}, r.svc.Output.NewSink(r.id, r.provider))
	return row
}

func (r *processChangeRig) registerProcess(ctx context.Context, opts agent.Options, sink agent.ProviderServices) map[string]string {
	r.t.Helper()
	confirmed, err := r.svc.Agents.StartAgentWith(ctx, opts, sink,
		func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
			return newRunningTestAgent(), nil
		})
	require.NoError(r.t, err)
	return confirmed
}

// launch stands in for the provider start. It reads the status first, as a client
// that polls ListAgents does, and then it registers a new process.
func (r *processChangeRig) launch(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
	r.processAtLaunch = append(r.processAtLaunch, r.svc.Agents.HasAgent(opts.AgentID))
	r.duringLaunch = append(r.duringLaunch, r.listedStatusOf(opts.AgentID))
	if r.failLaunch {
		return nil, errors.New("forced launch failure")
	}
	return r.registerProcess(ctx, opts, sink), nil
}

// listedStatus is the status that ListAgents reports for the agent now.
func (r *processChangeRig) listedStatus() leapmuxv1.AgentStatus {
	r.t.Helper()
	return r.listedStatusOf(r.id)
}

// listedStatusOf is the status that ListAgents reports for agentID now.
func (r *processChangeRig) listedStatusOf(agentID string) leapmuxv1.AgentStatus {
	r.t.Helper()
	w := newTestWriter()
	dispatch(r.d, "ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: []string{agentID}}, w)
	require.Empty(r.t, w.errors)
	require.Len(r.t, w.responses, 1)
	var resp leapmuxv1.ListAgentsResponse
	require.NoError(r.t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.Len(r.t, resp.GetAgents(), 1)
	return resp.GetAgents()[0].GetStatus()
}

// A process change that takes the old process away before the new one exists must
// still read STARTING in between. A reader that finds the agent in that gap with
// no startup entry reads INACTIVE for an agent that is restarting. A client that
// polls ListAgents until the status leaves STARTING takes that reply for the
// verdict of a start that has not ended.
func TestListAgents_ReportsStartingWhileAProcessIsReplaced(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// change drives the process change that the case covers.
		change func(t *testing.T, rig *processChangeRig, row db.Agent)
		// announced is the last status that the change broadcasts to a watcher.
		// UNSPECIFIED means that the caller of the change announces the end.
		announced leapmuxv1.AgentStatus
	}{
		{"a settings change that needs a restart", func(t *testing.T, rig *processChangeRig, _ db.Agent) {
			rig.svc.updateAgentSettingsFn = func(_ string, applied OptionMap) agent.SettingsApplyResult {
				return agent.RestartRequiredSettings(applied)
			}
			w := newTestWriter()
			dispatch(rig.d, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
				AgentId:  rig.id,
				Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDModel: "sonnet"}},
			}, w)
			require.Empty(t, w.errors)
		}, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE},
		{"a forced stop", func(t *testing.T, rig *processChangeRig, row db.Agent) {
			require.NoError(t, rig.svc.forceStopAgentTurn(row))
		}, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE},
		{"a clear of the context", func(t *testing.T, rig *processChangeRig, _ db.Agent) {
			finish, err := rig.svc.prepareClearContext(rig.id)
			require.NoError(t, err)
			finish()
		}, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE},
		{"a plan execution restart", func(t *testing.T, rig *processChangeRig, row db.Agent) {
			require.NoError(t, rig.svc.initiatePlanExecutionRestart(rig.id, "acceptEdits", row))
		}, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE},
		{"a relaunch for a settings change made during startup", func(t *testing.T, rig *processChangeRig, row db.Agent) {
			opts := rig.svc.baseAgentOptions(rig.id, row.WorkingDir, row.AgentProvider)
			opts.Options = OptionMap{agent.OptionIDModel: "opus"}
			_, running := rig.svc.relaunchForStartupSettingsChange(rig.id, row.AgentProvider, opts, row)
			require.True(t, running)
		}, leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rig := newProcessChangeRig(t)
			row := rig.seedRunning()
			require.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, rig.listedStatus(), "the agent runs before the change")

			tc.change(t, rig, row)

			require.Len(t, rig.duringLaunch, 1, "the change must launch exactly one process")
			assert.False(t, rig.processAtLaunch[0], "the old process must be gone when the launch begins")
			assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, rig.duringLaunch[0],
				"ListAgents must report STARTING, never INACTIVE, between the stop and the new process")
			assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, rig.listedStatus(), "the new process runs after the change")
			assert.Equal(t, tc.announced, rig.lastAnnounced(), "a watcher that replayed STARTING during the change needs the end of it")
			requireStartupsReleased(t, &rig.svc.AgentStartup.startupCore)
		})
	}
}

// A launch that fails leaves no process. The startup entry that the change held
// must go with it, so the agent reads the state that its row states and not
// STARTING for ever.
func TestListAgents_ReportsNoStartingAfterAProcessChangeFails(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		change func(t *testing.T, rig *processChangeRig, row db.Agent)
		// announced is the last status that the change broadcasts to a watcher.
		// UNSPECIFIED means that the caller of the change announces the end.
		announced leapmuxv1.AgentStatus
	}{
		{"a forced stop", func(t *testing.T, rig *processChangeRig, row db.Agent) {
			require.Error(t, rig.svc.forceStopAgentTurn(row))
		}, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE},
		{"a clear of the context", func(t *testing.T, rig *processChangeRig, _ db.Agent) {
			_, err := rig.svc.prepareClearContext(rig.id)
			require.Error(t, err)
		}, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED},
		{"a plan execution restart", func(t *testing.T, rig *processChangeRig, row db.Agent) {
			require.Error(t, rig.svc.initiatePlanExecutionRestart(rig.id, "acceptEdits", row))
		}, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE},
		{"a relaunch for a settings change made during startup", func(t *testing.T, rig *processChangeRig, row db.Agent) {
			opts := rig.svc.baseAgentOptions(rig.id, row.WorkingDir, row.AgentProvider)
			opts.Options = OptionMap{agent.OptionIDModel: "opus"}
			_, running := rig.svc.relaunchForStartupSettingsChange(rig.id, row.AgentProvider, opts, row)
			require.False(t, running)
		}, leapmuxv1.AgentStatus_AGENT_STATUS_UNSPECIFIED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rig := newProcessChangeRig(t)
			row := rig.seedRunning()
			rig.failLaunch = true

			tc.change(t, rig, row)

			require.Len(t, rig.duringLaunch, 1)
			assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, rig.duringLaunch[0])
			_, _, _, tracked := rig.svc.AgentStartup.status(rig.id)
			assert.False(t, tracked, "a failed change must give its startup entry back")
			after := rig.listedStatus()
			assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, after, "a failed change must not read STARTING for ever")
			assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, after, "no process runs after a failed launch")
			assert.Equal(t, tc.announced, rig.lastAnnounced(), "a watcher that replayed STARTING during the change needs the end of it")
			requireStartupsReleased(t, &rig.svc.AgentStartup.startupCore)
		})
	}
}

// An open is the first change of the process, and it reads STARTING from the
// moment that its row is visible until the process runs. The launch reads the
// status at its own boundary, and the end reads ACTIVE.
func TestListAgents_ReportsStartingFromTheOpenToTheEndOfTheStartup(t *testing.T) {
	t.Parallel()

	rig := newProcessChangeRig(t)
	w := newTestWriter()

	dispatch(rig.d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: rig.provider,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var opened leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &opened))
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, opened.GetAgent().GetStatus(), "the open must reply STARTING")
	requireStartupsReleased(t, &rig.svc.AgentStartup.startupCore)

	require.Len(t, rig.duringLaunch, 1)
	assert.False(t, rig.processAtLaunch[0])
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTING, rig.duringLaunch[0], "the launch of an open must read STARTING")
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, rig.listedStatusOf(opened.GetAgent().GetId()), "the process of the open runs after the startup")
}
