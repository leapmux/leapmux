package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type creationArchiveHarness struct {
	svc         *Service
	dispatcher  testRPCDispatcher
	clock       *quartz.Mock
	kind        leapmuxv1.TabType
	id          string
	directory   string
	state       atomic.Int32
	starts      atomic.Int32
	entered     chan int32
	permits     chan struct{}
	converged   chan struct{}
	probeMu     sync.Mutex
	probes      []*nativeRestartProbeAgent
	reconciler  *OrphanReconciler
	resumer     *AgentResumer
	ctx         context.Context
	loopStarted bool
}

func newCreationArchiveHarness(t *testing.T, kind leapmuxv1.TabType, state leapmuxv1.WorkspaceArchiveState) *creationArchiveHarness {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	svc, dispatcher, _ := setupTestService(t, withClock(clock))
	h := &creationArchiveHarness{svc: svc, dispatcher: dispatcher, clock: clock, kind: kind, id: "archive-recovery", directory: t.TempDir(), entered: make(chan int32, 8), permits: make(chan struct{}, 8), converged: make(chan struct{}, 16), ctx: creationOutcomeContext(t)}
	h.state.Store(int32(state))
	svc.ResolveTabPublication = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
		result, err := fixtureResolveTabPublication(ctx, request)
		result.ArchiveState = leapmuxv1.WorkspaceArchiveState(h.state.Load())
		return result, err
	}
	awaitPermit := func(ctx context.Context) error {
		h.entered <- h.starts.Add(1)
		select {
		case <-h.permits:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	startAgent := func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		if err := awaitPermit(ctx); err != nil {
			return nil, err
		}
		probe := newNativeRestartProbeAgent(opts.AgentID)
		probe.restartRequired = false
		h.probeMu.Lock()
		h.probes = append(h.probes, probe)
		h.probeMu.Unlock()
		result, err := svc.Agents.StartAgentWith(ctx, opts, sink, func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return probe, nil })
		if err == nil {
			sink.UpdateSessionID(fmt.Sprintf("archive-session-%d", h.starts.Load()))
		}
		return result, err
	}
	svc.startAgentFn, svc.startBackgroundAgentFn = startAgent, startAgent
	svc.startTerminalFn = func(ctx context.Context, opts terminal.Options, output terminal.OutputHandler, exit terminal.ExitHandler) error {
		if err := awaitPermit(ctx); err != nil {
			return err
		}
		return svc.startTerminalProcess(ctx, opts, output, exit)
	}
	if kind == leapmuxv1.TabType_TAB_TYPE_TERMINAL {
		testutil.RegisterTerminalCleanup(t, svc.Terminals, h.id)
	}
	h.resumer = svc.AgentResumer()
	h.reconciler = NewOrphanReconciler(svc.Queries, func(context.Context) (*leapmuxv1.ListOwnedTabsForWorkerResponse, error) {
		return &leapmuxv1.ListOwnedTabsForWorkerResponse{OwnerUserId: "user-1", TabStates: []*leapmuxv1.WorkerTabState{{Tab: &leapmuxv1.TabIdent{TabType: kind, TabId: h.id}, ArchiveState: leapmuxv1.WorkspaceArchiveState(h.state.Load())}}}, nil
	}, OrphanReconcilerOptions{Interval: time.Hour, CleanupWorktreeLinks: svc.CleanupOrphanWorktreeLinks, BeginTabReconciliation: svc.BeginTabReconciliation,
		ResumeAgents: func(ids []string) { h.resumer.Schedule(h.ctx, ids) },
		OnConverged: func() {
			svc.TriggerTabCreationRecovery()
			h.resumer.Start(h.ctx)
			select {
			case h.converged <- struct{}{}:
			default:
			}
		},
	})
	t.Cleanup(func() {
		for range 8 {
			select {
			case h.permits <- struct{}{}:
			default:
			}
		}
		svc.creations.stop()
		if h.loopStarted {
			h.reconciler.Stop(nil)
		}
		h.resumer.Stop(nil)
		svc.Shutdown()
	})
	return h
}

func (h *creationArchiveHarness) runReconciler() {
	h.loopStarted = true
	go h.reconciler.Run(h.ctx)
}

func (h *creationArchiveHarness) dispatchCreation(t *testing.T) {
	t.Helper()
	method, request := testProcessCreation(h.kind, h.id, h.directory)
	if terminalRequest, ok := request.(*leapmuxv1.OpenTerminalRequest); ok {
		terminalRequest.Shell = testutil.TestShell()
	}
	writer := newTestWriter()
	dispatch(h.dispatcher, method, request, writer)
	require.Empty(t, writer.rejections())
	require.Len(t, writer.responsesSnapshot(), 1)
}

func (h *creationArchiveHarness) awaitConvergence(t *testing.T) {
	t.Helper()
	select {
	case <-h.converged:
	case <-h.ctx.Done():
		t.Fatal("reconciliation did not converge")
	}
}

// awaitRecoveryIdle waits for the recovery scheduled by convergence to finish.
// Changing the Hub state before recovery finishes can invalidate its observation
// and require a retry that the mock clock must drive explicitly.
func (h *creationArchiveHarness) awaitRecoveryIdle(t *testing.T) {
	t.Helper()
	testutil.RequireEventually(t, func() bool {
		h.svc.creations.mu.Lock()
		defer h.svc.creations.mu.Unlock()
		return !h.svc.creations.recoveryRunning
	}, "archived creation recovery did not become idle")
}

func (h *creationArchiveHarness) awaitStart(t *testing.T, expected int32) {
	t.Helper()
	select {
	case count := <-h.entered:
		require.Equal(t, expected, count)
	case <-h.ctx.Done():
		t.Fatal("creation recovery did not enter startup")
	}
}

func (h *creationArchiveHarness) driveResize(t *testing.T, start, stop *quartz.Trap) {
	t.Helper()
	if h.kind != leapmuxv1.TabType_TAB_TYPE_TERMINAL {
		return
	}
	delay := testutil.WaitForTimer(t, h.ctx, start)
	require.Equal(t, pendingResizeWaitCap, delay)
	testutil.AdvanceAndAwaitStop(t, h.ctx, h.clock, delay, stop)
}

func (h *creationArchiveHarness) creation(t *testing.T) db.TabCreation {
	t.Helper()
	row, err := h.svc.Queries.GetTabCreation(h.ctx, db.GetTabCreationParams{UserID: "user-1", WorkerID: h.svc.WorkerID, TabID: h.id})
	require.NoError(t, err)
	return row
}

func (h *creationArchiveHarness) archived(t *testing.T) bool {
	t.Helper()
	if h.kind == leapmuxv1.TabType_TAB_TYPE_AGENT {
		row, err := h.svc.Queries.GetAgentByID(h.ctx, h.id)
		require.NoError(t, err)
		return row.WorkspaceArchived
	}
	row, err := h.svc.Queries.GetTerminal(h.ctx, h.id)
	require.NoError(t, err)
	return row.WorkspaceArchived
}

func (h *creationArchiveHarness) join(t *testing.T) {
	t.Helper()
	h.reconciler.Stop(nil)
	h.resumer.Stop(nil)
	h.svc.creations.workers.Wait()
	h.svc.AgentStartup.WaitForInFlight()
	h.svc.TerminalStartup.WaitForInFlight()
}

func (h *creationArchiveHarness) assertRunning(t *testing.T, starts int32) {
	t.Helper()
	assert.Equal(t, starts, h.starts.Load())
	row := h.creation(t)
	assert.Equal(t, creationCommitted, row.State)
	assert.Zero(t, len(row.RequestPayload))
	assert.Zero(t, len(row.RuntimePayload))
	assert.False(t, h.archived(t))
	if h.kind == leapmuxv1.TabType_TAB_TYPE_AGENT {
		resource, err := h.svc.Queries.GetAgentByID(h.ctx, h.id)
		require.NoError(t, err)
		assert.Empty(t, resource.StartupError)
		assert.True(t, h.svc.Agents.HasAgent(h.id))
	} else {
		resource, err := h.svc.Queries.GetTerminal(h.ctx, h.id)
		require.NoError(t, err)
		assert.Empty(t, resource.StartupError)
		assert.True(t, h.svc.Terminals.IsRunning(h.id))
	}
}

func TestCreationArchive_RecoversDormantPublicationThroughReconciliation(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			h := newCreationArchiveHarness(t, kind, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED)
			h.dispatchCreation(t)
			row := h.creation(t)
			require.Equal(t, creationPublished, row.State)
			assert.NotEmpty(t, row.RuntimePayload)
			assert.Zero(t, h.starts.Load())
			require.True(t, h.archived(t))
			h.runReconciler()
			h.awaitConvergence(t)
			h.awaitRecoveryIdle(t)
			resizeStart, resizeStop := testutil.NewTimerTraps(t, h.clock, startupPendingResizeTimerTag)
			h.state.Store(int32(leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE))
			h.reconciler.Trigger()
			h.awaitConvergence(t)
			h.driveResize(t, resizeStart, resizeStop)
			h.awaitStart(t, 1)
			for range 2 {
				h.reconciler.Trigger()
				h.awaitConvergence(t)
				h.svc.TriggerTabCreationRecovery()
			}
			assert.Equal(t, int32(1), h.starts.Load())
			h.permits <- struct{}{}
			h.join(t)
			h.assertRunning(t, 1)
		})
	}
}

func TestCreationArchive_JoinsRetryingCheckpointAndRecoversOnce(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			h := newCreationArchiveHarness(t, kind, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE)
			_, err := h.svc.DB.ExecContext(h.ctx, fmt.Sprintf(`CREATE TRIGGER hold_archive_creation_completion BEFORE UPDATE OF state ON tab_creations WHEN NEW.state = %d BEGIN SELECT RAISE(FAIL, 'test holds creation completion'); END`, creationCommitted))
			require.NoError(t, err)
			retryStart, retryStop := testutil.NewTimerTraps(t, h.clock, tabCreationRetryTimerTag)
			resizeStart, resizeStop := testutil.NewTimerTraps(t, h.clock, startupPendingResizeTimerTag)
			h.permits <- struct{}{}
			h.dispatchCreation(t)
			h.driveResize(t, resizeStart, resizeStop)
			h.awaitStart(t, 1)
			delay := testutil.WaitForTimer(t, h.ctx, retryStart)
			require.Equal(t, time.Second, delay)
			require.Equal(t, creationPublished, h.creation(t).State)
			h.runReconciler()
			h.awaitConvergence(t)
			h.state.Store(int32(leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED))
			h.reconciler.Trigger()
			testutil.RequireEventually(t, func() bool { return h.archived(t) }, "archive flags did not persist before joining startup")
			testutil.AdvanceAndAwaitStop(t, h.ctx, h.clock, delay, retryStop)
			h.awaitConvergence(t)
			h.svc.AgentStartup.WaitForInFlight()
			h.svc.TerminalStartup.WaitForInFlight()
			row := h.creation(t)
			require.Equal(t, creationPublished, row.State)
			assert.NotEmpty(t, row.RuntimePayload)
			if kind == leapmuxv1.TabType_TAB_TYPE_AGENT {
				assert.False(t, h.svc.Agents.HasAgent(h.id))
			} else {
				assert.False(t, h.svc.Terminals.IsRunning(h.id))
			}
			h.awaitRecoveryIdle(t)
			_, err = h.svc.DB.ExecContext(h.ctx, `DROP TRIGGER hold_archive_creation_completion`)
			require.NoError(t, err)
			h.state.Store(int32(leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE))
			h.reconciler.Trigger()
			h.awaitConvergence(t)
			h.driveResize(t, resizeStart, resizeStop)
			h.awaitStart(t, 2)
			for range 2 {
				h.reconciler.Trigger()
				h.awaitConvergence(t)
				h.svc.TriggerTabCreationRecovery()
			}
			assert.Equal(t, int32(2), h.starts.Load())
			h.permits <- struct{}{}
			h.join(t)
			h.assertRunning(t, 2)
		})
	}
}

func TestCreationArchive_RecoveryRetriesAStaleArchiveObservation(t *testing.T) {
	t.Parallel()
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			h := newCreationArchiveHarness(t, kind, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED)
			h.dispatchCreation(t)
			h.svc.creations.workers.Wait()

			// Hold the archived Hub response until reconciliation applies ACTIVE.
			// This forces the stale-observation retry without scheduler timing.
			observed := make(chan struct{})
			release := make(chan struct{})
			releaseObservation := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseObservation)
			resolve := h.svc.ResolveTabPublication
			var held atomic.Bool
			h.svc.ResolveTabPublication = func(ctx context.Context, request *leapmuxv1.TabPublication) (*leapmuxv1.ResolveTabPublicationResponse, error) {
				result, err := resolve(ctx, request)
				if err == nil && result.GetArchiveState() == leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED && held.CompareAndSwap(false, true) {
					close(observed)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return result, err
			}
			retryStart, retryStop := testutil.NewTimerTraps(t, h.clock, tabCreationRetryTimerTag)
			resizeStart, resizeStop := testutil.NewTimerTraps(t, h.clock, startupPendingResizeTimerTag)
			h.svc.TriggerTabCreationRecovery()
			select {
			case <-observed:
			case <-h.ctx.Done():
				t.Fatal("recovery did not read the archived publication")
			}
			h.state.Store(int32(leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE))
			h.runReconciler()
			h.awaitConvergence(t)
			require.False(t, h.archived(t))
			require.Zero(t, h.starts.Load())
			releaseObservation()
			delay := testutil.WaitForTimer(t, h.ctx, retryStart)
			require.Equal(t, time.Second, delay)
			require.Zero(t, h.starts.Load())
			testutil.AdvanceAndAwaitStop(t, h.ctx, h.clock, delay, retryStop)
			h.driveResize(t, resizeStart, resizeStop)
			h.awaitStart(t, 1)
			h.permits <- struct{}{}
			h.join(t)
			h.assertRunning(t, 1)
		})
	}
}
