package service

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
)

type nativeRestartProbeAgent struct {
	agenttest.IdleAgent
	id              string
	stopOnce        sync.Once
	stopped         chan struct{}
	sent            chan string
	restartRequired bool
}

// installRestartReadFault changes the service's row accessor after a set
// number of reads. The restart and cold-start paths share this accessor.
func installRestartReadFault(svc *Service, passReads int32, fault error) func() {
	previous := svc.getAgentByIDFn
	var reads atomic.Int32
	svc.getAgentByIDFn = func(ctx context.Context, agentID string) (db.Agent, error) {
		if reads.Add(1) <= passReads {
			return previous(ctx, agentID)
		}
		return db.Agent{}, fault
	}
	return func() { svc.getAgentByIDFn = previous }
}

func newNativeRestartProbeAgent(id string) *nativeRestartProbeAgent {
	return &nativeRestartProbeAgent{
		id: id, stopped: make(chan struct{}), sent: make(chan string, 2), restartRequired: true,
	}
}

func (a *nativeRestartProbeAgent) AgentID() string { return a.id }

func (a *nativeRestartProbeAgent) SendInput(content string, _ []*leapmuxv1.Attachment) error {
	a.sent <- content
	return nil
}

func (a *nativeRestartProbeAgent) SendInputForSession(_ string, content string, attachments []*leapmuxv1.Attachment) error {
	return a.SendInput(content, attachments)
}

func (a *nativeRestartProbeAgent) Stop() { a.stopOnce.Do(func() { close(a.stopped) }) }

func (a *nativeRestartProbeAgent) IsStopped() bool {
	select {
	case <-a.stopped:
		return true
	default:
		return false
	}
}

func (a *nativeRestartProbeAgent) Wait() error {
	<-a.stopped
	return nil
}

func (a *nativeRestartProbeAgent) NativeTurnRestartRequired() bool { return a.restartRequired }

func prepareNativeTurnRestart(t *testing.T) (*Service, *nativeRestartProbeAgent, string) {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	t.Cleanup(svc.InputQueue.StopAndWait)
	id := "agent-native-restart"
	workingDir := t.TempDir()
	mode := map[string]string{agent.OptionIDPermissionMode: "plan"}
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: id, WorkingDir: workingDir, HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEBUDDY,
		Options:       marshalOptions(mode), Resumed: 1,
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		ID: id, AgentSessionID: "native-session-1",
	}))
	old := newNativeRestartProbeAgent(id)
	_, err := svc.Agents.StartAgentWith(ctx, agent.Options{
		AgentID: id, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEBUDDY,
		WorkingDir: workingDir, Options: mode, ResumeSessionID: "native-session-1",
	}, svc.Output.NewSink(id, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEBUDDY),
		func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return old, nil })
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(id) })
	started, err := svc.InputQueue.TurnStarted(ctx, id, false)
	require.NoError(t, err)
	require.True(t, started.ActiveTurn)
	_, added, err := svc.InputQueue.EnqueueReportingAdded(ctx, inputqueue.NewItem{
		ID: "queued-next", AgentID: id,
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text: "Present the revised plan.",
	})
	require.NoError(t, err)
	require.True(t, added)
	return svc, old, id
}

func TestNativeTurnRestartKeepsQueuedInputForOneResumedProcess(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, old, id := prepareNativeTurnRestart(t)
	newAgent := newNativeRestartProbeAgent(id)
	started := make(chan agent.Options, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseStart := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseStart()
	var starts atomic.Int32
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		starts.Add(1)
		started <- opts
		<-release
		return svc.Agents.StartAgentWith(ctx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return newAgent, nil
			})
	}

	svc.Output.turnState(id, agent.TurnState{})
	select {
	case content := <-old.sent:
		t.Fatalf("queued input reached the old process: %q", content)
	case opts := <-started:
		assert.Equal(t, "native-session-1", opts.ResumeSessionID)
		assert.Equal(t, "plan", opts.PermissionMode())
	case <-ctx.Done():
		t.Fatal("the native turn did not start a session-preserving restart")
	}
	svc.Output.turnState(id, agent.TurnState{})
	releaseStart()
	select {
	case content := <-newAgent.sent:
		assert.Equal(t, "Present the revised plan.", content)
	case <-ctx.Done():
		t.Fatal("the resumed process did not receive the queued input")
	}
	svc.Shutdown()
	assert.Equal(t, int32(1), starts.Load(), "duplicate turn-end reports must start one replacement")
	select {
	case content := <-old.sent:
		t.Fatalf("old process received queued input after replacement: %q", content)
	default:
	}
}

func TestNativeTurnRestartFailureKeepsTheQueuedInputPaused(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, old, id := prepareNativeTurnRestart(t)
	attempted := make(chan struct{})
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		close(attempted)
		return nil, errors.New("replacement refused")
	}

	svc.Output.turnState(id, agent.TurnState{})
	select {
	case content := <-old.sent:
		t.Fatalf("queued input reached the old process after a failed restart: %q", content)
	case <-attempted:
	case <-ctx.Done():
		t.Fatal("the native turn did not attempt a replacement")
	}
	svc.Shutdown()
	row, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	assert.Empty(t, row.AgentSessionID, "a failed resume must clear its stale session ID")
	var paused bool
	require.NoError(t, svc.DB.QueryRowContext(ctx,
		"SELECT paused FROM agent_input_queue_state WHERE agent_id = ?", id).Scan(&paused))
	assert.True(t, paused, "a failed replacement keeps queued input for an explicit retry")
}

func TestNativeTurnRestartCannotReopenClosedTab(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, id := prepareNativeTurnRestart(t)
	stale, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	svc.Agents.StopAndWaitAgent(id)
	_, err = svc.Queries.CloseAgent(ctx, id)
	require.NoError(t, err)

	var starts atomic.Int32
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		starts.Add(1)
		return nil, errors.New("a closed tab reached the process launch")
	}
	_, err = svc.restartAgentPreservingSession(stale, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
	require.ErrorContains(t, err, "closed")
	assert.Zero(t, starts.Load(), "a stale row cannot start a process after tab close")
	row, readErr := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, readErr)
	assert.True(t, row.ClosedAt.Valid)
}

func TestNativeTurnRestartCannotReopenArchivedTab(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, id := prepareNativeTurnRestart(t)
	stale, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	svc.Agents.StopAndWaitAgent(id)
	_, err = svc.Queries.SetAgentWorkspaceArchived(ctx, db.SetAgentWorkspaceArchivedParams{
		ID: id, WorkspaceArchived: true,
	})
	require.NoError(t, err)
	var starts atomic.Int32
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		starts.Add(1)
		return nil, errors.New("an archived tab reached process launch")
	}
	_, err = svc.restartAgentPreservingSession(stale, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
	require.ErrorContains(t, err, "archived")
	assert.Zero(t, starts.Load(), "an archived tab cannot start a process")
	assert.False(t, svc.Agents.HasAgent(id))
}

func TestNativeTurnRestartRefusesMissingOrUnreadableRow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		fault error
		want  string
	}{
		{name: "missing", fault: sql.ErrNoRows, want: "closed"},
		{name: "read error", fault: errors.New("the restart read failed"), want: "read agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			svc, old, id := prepareNativeTurnRestart(t)
			stale, err := svc.Queries.GetAgentByID(ctx, id)
			require.NoError(t, err)
			defer installRestartReadFault(svc, 0, tc.fault)()

			var starts atomic.Int32
			svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
				starts.Add(1)
				return nil, errors.New("a missing row reached the process launch")
			}
			_, err = svc.restartAgentPreservingSession(stale, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
			require.ErrorContains(t, err, tc.want)
			assert.Zero(t, starts.Load())
			assert.False(t, old.IsStopped(), "a read failure must not stop the old process")
			queue, queueErr := svc.InputQueue.Snapshot(ctx, id)
			require.NoError(t, queueErr)
			assert.True(t, queue.Paused, "the queue stays paused when restart identity is unavailable")
		})
	}
}

func TestNativeTurnRestartStopsLaunchWhenRowClosesDuringStart(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, id := prepareNativeTurnRestart(t)
	stale, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	newAgent := newNativeRestartProbeAgent(id)
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		if _, closeErr := svc.Queries.CloseAgent(ctx, id); closeErr != nil {
			return nil, closeErr
		}
		return svc.Agents.StartAgentWith(ctx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return newAgent, nil
			})
	}

	_, err = svc.restartAgentPreservingSession(stale, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
	require.ErrorContains(t, err, "closed")
	assert.True(t, newAgent.IsStopped(), "a process started after the row closed must be stopped")
	assert.False(t, svc.Agents.HasAgent(id), "a closed tab cannot retain a process")
}

func TestNativeTurnRestartStopsLaunchWhenWorkspaceArchivesDuringStart(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, id := prepareNativeTurnRestart(t)
	stale, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	newAgent := newNativeRestartProbeAgent(id)
	svc.startAgentFn = func(startCtx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		if _, archiveErr := svc.Queries.SetAgentWorkspaceArchived(startCtx, db.SetAgentWorkspaceArchivedParams{
			ID: id, WorkspaceArchived: true,
		}); archiveErr != nil {
			return nil, archiveErr
		}
		return svc.Agents.StartAgentWith(startCtx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return newAgent, nil
			})
	}
	_, err = svc.restartAgentPreservingSession(stale, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
	require.ErrorContains(t, err, "archived")
	assert.True(t, newAgent.IsStopped(), "an archived tab cannot keep a late process")
	assert.False(t, svc.Agents.HasAgent(id))
}

func TestNativeTurnRestartStopsLaunchAfterPostlaunchReadFault(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, old, id := prepareNativeTurnRestart(t)
	stale, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	defer installRestartReadFault(svc, 1, errors.New("the postlaunch read failed"))()
	newAgent := newNativeRestartProbeAgent(id)
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		return svc.Agents.StartAgentWith(ctx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return newAgent, nil
			})
	}

	_, err = svc.restartAgentPreservingSession(stale, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
	require.ErrorContains(t, err, "read agent")
	assert.True(t, old.IsStopped(), "the restart already stopped the old process")
	assert.True(t, newAgent.IsStopped(), "a process with unknown row state cannot keep running")
	assert.False(t, svc.Agents.HasAgent(id))
	stored, readErr := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, readErr)
	assert.Equal(t, "native-session-1", stored.AgentSessionID,
		"a read fault cannot prove that the native session is stale")
	queue, queueErr := svc.InputQueue.Snapshot(ctx, id)
	require.NoError(t, queueErr)
	assert.True(t, queue.Paused)
	assert.Equal(t,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT,
		queue.PauseReason)
}

func TestNativeTurnRestartMintFailureKeepsTheLiveSessionID(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, old, id := prepareNativeTurnRestart(t)
	row, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	svc.ControlIPC = &noIdentityRemoteIPC{failFrom: 1}

	_, err = svc.restartAgentPreservingSession(row, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
	require.ErrorIs(t, err, ErrMissingIdentity)
	assert.False(t, old.IsStopped(), "a failed mint did not replace the live process")
	stored, readErr := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, readErr)
	assert.Equal(t, "native-session-1", stored.AgentSessionID,
		"a failed mint cannot erase the live process's resume identity")
	queue, queueErr := svc.InputQueue.Snapshot(ctx, id)
	require.NoError(t, queueErr)
	assert.True(t, queue.Paused, "the failed native restart cannot release input to the old mode")
	assert.False(t, queue.ActiveTurn, "the native callback already observed the old turn end")
	assert.Equal(t,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_FAILED,
		queue.PauseReason)
	require.Len(t, queue.Items, 1, "the queued input stays available for an explicit retry")
	assert.Equal(t, "queued-next", queue.Items[0].ID)
	select {
	case sent := <-old.sent:
		t.Fatalf("the old mode received queued input after the restart failed: %q", sent)
	default:
	}
}

func TestSettingsRestartMintFailureKeepsTheOldTurn(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, old, id := prepareNativeTurnRestart(t)
	row, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	svc.ControlIPC = &noIdentityRemoteIPC{failFrom: 1}
	_, err = svc.restartAgentPreservingSession(row, storedRestartOptions, settingsRestartMessages, restartTurnEndPending, nil)
	require.ErrorIs(t, err, ErrMissingIdentity)
	assert.False(t, old.IsStopped())
	queue, queueErr := svc.InputQueue.Snapshot(ctx, id)
	require.NoError(t, queueErr)
	assert.False(t, queue.Paused, "the old process can continue after a settings mint failure")
	assert.True(t, queue.ActiveTurn, "its running turn keeps the queue busy")
	assert.Len(t, queue.Items, 1)
}

func TestNativeTurnRestartUsesTheCurrentStoredOptions(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, id := prepareNativeTurnRestart(t)
	stale, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	newOptions := map[string]string{agent.OptionIDPermissionMode: "default"}
	_, err = svc.DB.ExecContext(ctx, "UPDATE agents SET options = ? WHERE id = ?", marshalOptions(newOptions), id)
	require.NoError(t, err)

	var started agent.Options
	svc.startAgentFn = func(_ context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		started = opts
		return nil, errors.New("stop after observing restart options")
	}
	_, _ = svc.restartAgentPreservingSession(stale, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
	assert.Equal(t, "default", started.PermissionMode(), "native restart reads the settled mode after a concurrent settings write")
}

func TestNativeTurnRestartCloseAfterRegistrationStopsReplacement(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, id := prepareNativeTurnRestart(t)
	stale, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	newAgent := newNativeRestartProbeAgent(id)
	launchEntered := make(chan error, 1)
	releaseLaunch := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLaunch) }) }
	defer release()
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		settings, err := svc.Agents.StartAgentWith(ctx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return newAgent, nil
			})
		launchEntered <- err
		<-releaseLaunch
		return settings, err
	}
	result := make(chan error, 1)
	go func() {
		_, restartErr := svc.restartAgentPreservingSession(stale, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
		result <- restartErr
	}()
	select {
	case startErr := <-launchEntered:
		require.NoError(t, startErr)
	case <-ctx.Done():
		t.Fatal("the replacement did not reach its held launch")
	}
	svc.closeAgentTabCommon("", id, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
	release()
	select {
	case restartErr := <-result:
		require.ErrorIs(t, restartErr, errAgentClosedDuringLaunch)
	case <-ctx.Done():
		t.Fatal("the restart did not stop after close")
	}
	assert.True(t, newAgent.IsStopped(), "the close stops the process that the restart launched")
	row, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid)
}

func TestNativeTurnRestartCloseBeforeRegistrationStopsReplacement(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, id := prepareNativeTurnRestart(t)
	stale, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	newAgent := newNativeRestartProbeAgent(id)
	beforeRegistration := make(chan struct{})
	releaseLaunch := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLaunch) }) }
	defer release()
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		close(beforeRegistration)
		<-releaseLaunch
		return svc.Agents.StartAgentWith(ctx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return newAgent, nil
			})
	}
	result := make(chan error, 1)
	go func() {
		_, restartErr := svc.restartAgentPreservingSession(stale, storedRestartOptions, nativeTurnRestartMessages, restartTurnEndObserved, nil)
		result <- restartErr
	}()
	select {
	case <-beforeRegistration:
	case <-ctx.Done():
		t.Fatal("the restart did not reach the held registration")
	}
	svc.closeAgentTabCommon("", id, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
	release()
	select {
	case restartErr := <-result:
		require.ErrorIs(t, restartErr, errAgentClosedDuringLaunch)
	case <-ctx.Done():
		t.Fatal("the restart did not stop its late process")
	}
	assert.True(t, newAgent.IsStopped())
	assert.False(t, svc.Agents.HasAgent(id))
}

func TestClearContextStopsProcessRegisteredAfterClose(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, id := prepareNativeTurnRestart(t)
	late := newNativeRestartProbeAgent(id)
	svc.startAgentFn = func(startCtx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		svc.closeAgentTabCommon("", id, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
		return svc.Agents.StartAgentWith(startCtx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return late, nil
			})
	}
	finish, err := svc.prepareClearContext(id)
	require.ErrorIs(t, err, errAgentClosedDuringLaunch)
	assert.Nil(t, finish, "a closed tab cannot publish a clear boundary")
	assert.True(t, late.IsStopped())
	assert.False(t, svc.Agents.HasAgent(id))
	row, readErr := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, readErr)
	assert.True(t, row.ClosedAt.Valid)
}

func TestClearContextFinishSkipsClosedTab(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, id := prepareNativeTurnRestart(t)
	newAgent := newNativeRestartProbeAgent(id)
	svc.startAgentFn = func(startCtx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		return svc.Agents.StartAgentWith(startCtx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return newAgent, nil
			})
	}
	finish, err := svc.prepareClearContext(id)
	require.NoError(t, err)
	require.NotNil(t, finish)
	svc.closeAgentTabCommon("", id, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
	var before, after int
	require.NoError(t, svc.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE agent_id = ?", id).Scan(&before))
	finish()
	require.NoError(t, svc.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE agent_id = ?", id).Scan(&after))
	assert.Equal(t, before, after, "a committed clear cannot publish a new row after its tab closes")
	assert.True(t, newAgent.IsStopped())
}

func TestPlanExecutionRestartStopsProcessRegisteredAfterClose(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, id := prepareNativeTurnRestart(t)
	row, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	late := newNativeRestartProbeAgent(id)
	svc.startAgentFn = func(startCtx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		svc.closeAgentTabCommon("", id, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
		return svc.Agents.StartAgentWith(startCtx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return late, nil
			})
	}
	_, err = svc.restartPlanContextLocked(id, "acceptEdits", row)
	require.ErrorIs(t, err, errAgentClosedDuringLaunch)
	assert.True(t, late.IsStopped())
	assert.False(t, svc.Agents.HasAgent(id))
}
