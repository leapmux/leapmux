package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recoveryCrashingPublicationAgent struct {
	*recoveryAgent
	prepared atomic.Bool
	crashed  atomic.Bool
	onCrash  func()
	once     sync.Once
}

func (a *recoveryCrashingPublicationAgent) PrepareInputProcess() error {
	err := a.recoveryAgent.PrepareInputProcess()
	if err == nil {
		a.prepared.Store(true)
	}
	return err
}

func (a *recoveryCrashingPublicationAgent) PublishTurnActive() agent.TurnState {
	if a.prepared.Load() {
		a.once.Do(func() {
			a.crashed.Store(true)
			a.Stop()
			a.onCrash()
		})
	}
	return a.recoveryAgent.PublishTurnActive()
}

func (a *recoveryCrashingPublicationAgent) IsStopped() bool {
	return !a.crashed.Load() && a.recoveryAgent.IsStopped()
}

func (a *recoveryCrashingPublicationAgent) Wait() error {
	_ = a.recoveryAgent.Wait()
	if a.crashed.Load() {
		return errors.New("the replacement process crashed")
	}
	return nil
}

func TestInputRecoveryKeepsAReplacementCrashPause(t *testing.T) {
	t.Parallel()
	testCtx := testutil.DeadlineContext(t)
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	exitCleaned := make(chan struct{})
	svc.Agents.SetOnExit(func(exit agent.ProcessExit) {
		svc.HandleAgentProcessExit(exit)
		if !exit.Stopped {
			close(exitCleaned)
		}
	})
	svc.startAgentFn = func(ctx context.Context, options agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		return svc.Agents.StartAgentWith(ctx, options, sink, func(ctx context.Context, options agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
			running, err := starts.start(ctx, options, sink)
			if err != nil || starts.count() != 2 {
				return running, err
			}
			return &recoveryCrashingPublicationAgent{recoveryAgent: running.(*recoveryAgent), onCrash: func() {
				select {
				case <-exitCleaned:
				case <-testCtx.Done():
					t.Error("the replacement crash did not finish its exit cleanup")
				}
			}}, nil
		})
	}
	const id = "recovery-replacement-crash"
	seedRecoveryAgent(t, svc, id, "saved-thread")
	require.ErrorIs(t, svc.ensureAgentReadyForInput(id), agent.ErrInputProcessRestartRequired)
	approved := svc.inputProcessRecovery(id)
	require.NotNil(t, approved)
	err := svc.recoverAgentInputProcess(id, approved)
	assert.Error(t, err, "a crashed replacement cannot complete recovery")
	snapshot, err := svc.InputQueue.Snapshot(testCtx, id)
	require.NoError(t, err)
	assert.True(t, snapshot.Paused, "recovery cannot release the replacement's crash pause")
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, snapshot.PauseReason)
	assert.Equal(t, 2, starts.count())
}

type recoverySubtreeReadFault struct {
	db.DBTX
	enabled atomic.Bool
}

func (store *recoverySubtreeReadFault) QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error) {
	if store.enabled.Load() && (strings.Contains(query, "-- name: ListDescendantAgentIDs :many") || strings.Contains(query, "-- name: ListAgentTreeIDs :many")) {
		return nil, errors.New("the recovery subtree read failed")
	}
	return store.DBTX.QueryContext(ctx, query, arguments...)
}

func TestInputRecoveryRetainsInputWhenControlCleanupFails(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	const root = "recovery-cleanup-root"
	const child = "recovery-cleanup-child"
	seedRecoveryAgent(t, svc, root, "saved-thread")
	enqueueRecoveryInput(t, svc, root, "held-input")
	recoveryQueuedItem(t, svc, root, "held-input")
	require.NoError(t, svc.Queries.CreateChildAgent(t.Context(), db.CreateChildAgentParams{
		ID: child, ParentAgentID: sql.NullString{String: root, Valid: true}, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	for _, owner := range []string{root, child} {
		for _, nonblocking := range []bool{false, true} {
			requestID := "old-blocking"
			if nonblocking {
				requestID = "conversation-question"
			}
			createTestControlRequest(t, t.Context(), svc.Output, testControlRequestParams{
				AgentID: owner, RequestID: requestID, Payload: []byte(`{}`), NonBlocking: nonblocking,
			})
		}
	}
	svc.Output.WaitActivityRefreshes()
	originalQueries := svc.Queries
	fault := &recoverySubtreeReadFault{DBTX: svc.DB}
	fault.enabled.Store(true)
	svc.Queries = db.New(fault)
	t.Cleanup(func() {
		fault.enabled.Store(false)
		svc.Queries = originalQueries
	})
	err := svc.recoverAgentInputProcess(root, svc.inputProcessRecovery(root))
	require.Error(t, err, "a cleanup fault cannot report recovery success")
	assert.Equal(t, 1, starts.count(), "no replacement can start with old process controls")
	snapshot, err := svc.InputQueue.Snapshot(t.Context(), root)
	require.NoError(t, err)
	assert.True(t, snapshot.Paused)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
	fault.enabled.Store(false)
	svc.Queries = originalQueries
	workingStart := svc.startAgentFn
	svc.startAgentFn = func(ctx context.Context, options agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		for _, owner := range []string{root, child} {
			controls, readErr := svc.Output.ListControlRequests(ctx, owner)
			require.NoError(t, readErr)
			require.Len(t, controls, 1, "old blocking controls must retire before startup")
			assert.True(t, controls[0].NonBlocking)
		}
		confirmed, startErr := workingStart(ctx, options, sink)
		if startErr == nil {
			createTestControlRequest(t, ctx, svc.Output, testControlRequestParams{AgentID: root, RequestID: "fresh-blocking", Payload: []byte(`{}`)})
		}
		return confirmed, startErr
	}
	if approved := svc.inputProcessRecovery(root); approved != nil {
		require.NoError(t, svc.recoverAgentInputProcess(root, approved))
	} else {
		_, err = svc.InputQueue.SetPaused(t.Context(), root, false)
		require.NoError(t, err)
	}
	waitForMessageCount(t, svc, root, 1)
	requireLookupControlRequest(t, t.Context(), svc.Output, root, "fresh-blocking")
}

func TestInputRecoveryCleanupFailureCannotBeBypassedByASettingsRestart(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	const id = "recovery-settings-cleanup"
	seedRecoveryAgent(t, svc, id, "saved-thread")
	enqueueRecoveryInput(t, svc, id, "held-input")
	recoveryQueuedItem(t, svc, id, "held-input")
	createTestControlRequest(t, t.Context(), svc.Output, testControlRequestParams{
		AgentID: id, RequestID: "old-blocking", Payload: []byte(`{}`),
	})
	svc.Output.WaitActivityRefreshes()
	originalQueries := svc.Queries
	fault := &recoverySubtreeReadFault{DBTX: svc.DB}
	fault.enabled.Store(true)
	svc.Queries = db.New(fault)
	t.Cleanup(func() {
		fault.enabled.Store(false)
		svc.Queries = originalQueries
	})
	require.Error(t, svc.recoverAgentInputProcess(id, svc.inputProcessRecovery(id)))
	require.True(t, starts.fake(0).wasStopped())
	require.Equal(t, 1, starts.count())
	row, err := originalQueries.GetAgentByID(t.Context(), id)
	require.NoError(t, err)
	// A settings request can pass its running-process check before
	// recovery stops that process, then reach this restart afterward.
	_, err = svc.restartAgentPreservingSession(row, storedRestartOptions, settingsRestartMessages, restartTurnEndPending, nil)
	assert.Error(t, err, "a direct restart must enforce the same control cleanup")
	assert.Equal(t, 1, starts.count(), "cleanup failure must prevent every replacement launch")
	after, err := originalQueries.GetAgentByID(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, "saved-thread", after.AgentSessionID, "a pre-start cleanup refusal cannot invalidate the saved conversation")
	assert.EqualValues(t, 1, after.Resumed)
}

func TestInputRecoveryControlCleanupRefusesARegisteredProcess(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	const id = "recovery-live-control-owner"
	seedRecoveryAgent(t, svc, id, "saved-thread")
	require.ErrorIs(t, svc.ensureAgentReadyForInput(id), agent.ErrInputProcessRestartRequired)
	createTestControlRequest(t, t.Context(), svc.Output, testControlRequestParams{
		AgentID: id, RequestID: "live-blocking", Payload: []byte(`{}`),
	})
	err := svc.retireStoppedProcessControls(id)
	require.ErrorIs(t, err, inputqueue.ErrConflict)
	requireLookupControlRequest(t, t.Context(), svc.Output, id, "live-blocking")
	assert.False(t, starts.fake(0).wasStopped())
	assert.Equal(t, 1, starts.count())
}

func TestInputRecoveryApprovalCannotTransferThroughAPremintedOutputSource(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, func(int) func(int) error {
		return func(int) error { return fmtRestartRequired() }
	})
	const id = "recovery-preminted-source"
	row := seedRecoveryAgent(t, svc, id, "saved-thread")
	require.ErrorIs(t, svc.ensureAgentReadyForInput(id), agent.ErrInputProcessRestartRequired)
	oldProcess := starts.fake(0)
	// Settings restarts allocate their replacement's output source before
	// their launcher stops the current physical process.
	preminted := newTestRootOutputSink(t, svc.Output, id, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
	approved := svc.inputProcessRecovery(id)
	require.NotNil(t, approved)
	assert.False(t, oldProcess.wasStopped(), "the proposal was captured for the first physical process")
	options := svc.baseAgentOptions(id, row.WorkingDir, row.AgentProvider)
	options.Session = agent.ResumeSession("saved-thread")
	unlock := svc.Agents.LockAgent(id)
	svc.Agents.StopAndWaitAgent(id)
	_, err := svc.startAgent(t.Context(), options, preminted)
	unlock()
	require.NoError(t, err)
	require.ErrorIs(t, svc.prepareAgentInputProcess(id), agent.ErrInputProcessRestartRequired)
	current := svc.inputProcessRecovery(id)
	require.NotNil(t, current)
	assert.Equal(t, approved.OutputGeneration, current.OutputGeneration)
	assert.Equal(t, approved.AgentSessionId, current.AgentSessionId)
	assert.Equal(t, approved.AgentProvider, current.AgentProvider)
	assert.NotEmpty(t, approved.ProcessInstanceId)
	assert.NotEqual(t, approved.ProcessInstanceId, current.ProcessInstanceId, "physical registration identity must differ despite reuse of the output source")
	err = svc.recoverAgentInputProcess(id, approved)
	require.ErrorIs(t, err, inputqueue.ErrConflict, "an output generation cannot transfer approval between physical processes")
	assert.False(t, starts.fake(1).wasStopped(), "the second physical process needs a new approval")
	assert.Equal(t, 2, starts.count())
}

func TestInputRecoveryRejectsAnIncorrectPhysicalProcessIdentity(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	const id = "recovery-incorrect-physical-process"
	seedRecoveryAgent(t, svc, id, "saved-thread")
	require.ErrorIs(t, svc.ensureAgentReadyForInput(id), agent.ErrInputProcessRestartRequired)
	approved := svc.inputProcessRecovery(id)
	require.NotNil(t, approved)
	approved.ProcessInstanceId = "a-different-physical-process"
	require.ErrorIs(t, svc.recoverAgentInputProcess(id, approved), inputqueue.ErrConflict)
	assert.False(t, starts.fake(0).wasStopped())
	assert.Equal(t, 1, starts.count())
}
