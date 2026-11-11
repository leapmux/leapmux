package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/db/dbtest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type creationReplacementHarness struct {
	svc                   *Service
	dispatcher            testRPCDispatcher
	clock                 *quartz.Mock
	ctx                   context.Context
	retryStart, retryStop *quartz.Trap
	observeRead           atomic.Bool
	read                  chan struct{}
	callers               sync.WaitGroup
}

func newCreationReplacementHarness(t *testing.T) *creationReplacementHarness {
	t.Helper()
	// The shared compression encoder retains channels across calls, so separate
	// synctest bubbles cannot safely exercise these output and replacement paths.
	h := &creationReplacementHarness{clock: testutil.NewQuartzMock(t), ctx: creationOutcomeContext(t), read: make(chan struct{}, 1)}
	h.svc, h.dispatcher, _ = setupTestService(t, withClock(h.clock), withQueryRewrite(func(query string) string {
		if strings.Contains(query, "-- name: GetTabCreation :one") && h.observeRead.Swap(false) {
			h.read <- struct{}{}
		}
		return query
	}))
	_, err := h.svc.DB.ExecContext(h.ctx, fmt.Sprintf(`CREATE TRIGGER hold_replacement_checkpoint BEFORE UPDATE OF state ON tab_creations WHEN NEW.state = %d BEGIN SELECT RAISE(FAIL, 'test holds creation completion'); END`, creationCommitted))
	require.NoError(t, err)
	h.retryStart, h.retryStop = testutil.NewTimerTraps(t, h.clock, tabCreationRetryTimerTag)
	t.Cleanup(func() {
		h.retryStart.Close()
		h.retryStop.Close()
		h.release(t)
		h.svc.creations.stop()
		h.callers.Wait()
		assert.True(t, h.svc.nativeTurnRestartsInFlight.Wait(30*time.Second, "native replacement did not finish during cleanup"))
		drainAllInFlight(h.svc)
	})
	return h
}

func (h *creationReplacementHarness) release(t *testing.T) {
	t.Helper()
	_, err := h.svc.DB.ExecContext(context.Background(), "DROP TRIGGER IF EXISTS hold_replacement_checkpoint")
	require.NoError(t, err)
}

func (h *creationReplacementHarness) awaitCheckpoint(t *testing.T) time.Duration {
	t.Helper()
	delay := testutil.WaitForTimer(t, h.ctx, h.retryStart)
	require.Equal(t, time.Second, delay)
	return delay
}

func (h *creationReplacementHarness) awaitReplacementRead(t *testing.T) {
	t.Helper()
	select {
	case <-h.read:
	case <-h.ctx.Done():
		t.Fatal("replacement did not read the unfinished creation")
	}
}

func (h *creationReplacementHarness) completeCheckpoint(t *testing.T, delay time.Duration) {
	t.Helper()
	h.release(t)
	testutil.AdvanceAndAwaitStop(t, h.ctx, h.clock, delay, h.retryStop)
}

func TestCreationReplacement_AgentEntryPointsWaitForTheOriginalCheckpoint(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"clear", "native restart", "approved plan"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			workingDir := t.TempDir()
			h := newCreationReplacementHarness(t)
			svc := h.svc
			const id = "replacement-agent"
			var starts atomic.Int32
			svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
				if starts.Add(1) > 1 {
					row, err := svc.Queries.GetTabCreation(ctx, db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: id})
					assert.NoError(t, err)
					assert.Equal(t, creationCommitted, row.State, "replacement started before the original outcome was durable")
					return nil, errors.New("replacement provider failed")
				}
				probe := newNativeRestartProbeAgent(id)
				probe.restartRequired = action == "native restart"
				result, err := svc.Agents.StartAgentWith(ctx, opts, sink, func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return probe, nil })
				sink.UpdateSessionID("original-session")
				return result, err
			}
			method, request := testProcessCreation(leapmuxv1.TabType_TAB_TYPE_AGENT, id, workingDir)
			writer := newTestWriter()
			dispatch(h.dispatcher, method, request, writer)
			require.Empty(t, writer.rejections())
			delay := h.awaitCheckpoint(t)
			done := make(chan error, 1)
			h.observeRead.Store(true)
			if action == "native restart" {
				requireRootOutputSink(t, svc.Output, id).SetTurnState(agent.TurnState{}, 1)
				h.callers.Go(func() {
					if !svc.nativeTurnRestartsInFlight.Wait(30*time.Second, "native replacement did not finish") {
						done <- errors.New("native replacement did not finish")
						return
					}
					done <- nil
				})
			} else {
				item := inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{ItemMetadata: inputqueue.ItemMetadata{OrderingMetadata: inputqueue.OrderingMetadata{ID: "replacement-input", AgentID: id, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT}}, Text: "/clear"}}
				if action == "approved plan" {
					item.Kind = leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION
					item.PrepareContext = true
					item.TargetMode = "acceptEdits"
					require.NoError(t, dbtest.StoreControlResponse(h.ctx, svc.DB, db.ControlResponseAnswer{AgentID: id, RequestID: "approval", ClaimToken: "claim", State: storedStateCompleted, InputID: item.ID, AgentSessionID: "original-session", PlanApprovalSettings: []byte(`{"permissionMode":"acceptEdits","clearContext":true}`)}))
				}
				h.callers.Go(func() { _, err := (&agentInputQueueAdapter{svc: svc}).Dispatch(item); done <- err })
			}
			h.awaitReplacementRead(t)
			assert.Equal(t, int32(1), starts.Load())
			select {
			case err := <-done:
				t.Fatalf("replacement returned before the checkpoint: %v", err)
			default:
			}
			h.completeCheckpoint(t, delay)
			select {
			case err := <-done:
				if action != "native restart" {
					require.ErrorContains(t, err, "replacement provider failed")
				} else {
					require.NoError(t, err)
				}
			case <-h.ctx.Done():
				t.Fatal("replacement did not finish")
			}
			svc.AgentStartup.WaitForInFlight()
			assert.Equal(t, int32(2), starts.Load())
			assert.False(t, svc.Agents.HasAgent(id))
			resource, err := svc.Queries.GetAgentByID(h.ctx, id)
			require.NoError(t, err)
			assert.Empty(t, resource.AgentSessionID, "old success must not restore the native session after replacement failure")
			if action == "clear" {
				assert.Equal(t, "replacement provider failed", resource.StartupError)
			}
			row, err := svc.Queries.GetTabCreation(h.ctx, db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: id})
			require.NoError(t, err)
			assert.Equal(t, creationCommitted, row.State)
			assert.Empty(t, row.RuntimePayload)
		})
	}
}

func TestCreationReplacement_ClearRecoversAfterTheOriginalFailureCheckpoint(t *testing.T) {
	t.Parallel()
	workingDir := t.TempDir()
	h := newCreationReplacementHarness(t)
	svc := h.svc
	const id = "recover-failed-creation"
	var starts atomic.Int32
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		if starts.Add(1) == 1 {
			return nil, errors.New("original provider failed")
		}
		probe := newNativeRestartProbeAgent(id)
		probe.restartRequired = false
		result, err := svc.Agents.StartAgentWith(ctx, opts, sink, func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) { return probe, nil })
		sink.UpdateSessionID("recovered-session")
		return result, err
	}
	method, request := testProcessCreation(leapmuxv1.TabType_TAB_TYPE_AGENT, id, workingDir)
	writer := newTestWriter()
	dispatch(h.dispatcher, method, request, writer)
	require.Empty(t, writer.rejections())
	delay := h.awaitCheckpoint(t)
	done := make(chan error, 1)
	h.observeRead.Store(true)
	h.callers.Go(func() {
		_, err := (&agentInputQueueAdapter{svc: svc}).Dispatch(inputqueue.DispatchItem{StoredItem: inputqueue.StoredItem{ItemMetadata: inputqueue.ItemMetadata{OrderingMetadata: inputqueue.OrderingMetadata{ID: "recover", AgentID: id, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT}}, Text: "/clear"}})
		done <- err
	})
	h.awaitReplacementRead(t)
	assert.Equal(t, int32(1), starts.Load())
	select {
	case err := <-done:
		t.Fatalf("clear returned before the failure checkpoint: %v", err)
	default:
	}
	h.completeCheckpoint(t, delay)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-h.ctx.Done():
		t.Fatal("clear did not finish")
	}
	svc.AgentStartup.WaitForInFlight()
	resource, err := svc.Queries.GetAgentByID(h.ctx, id)
	require.NoError(t, err)
	assert.Empty(t, resource.StartupError)
	assert.Equal(t, "recovered-session", resource.AgentSessionID)
	assert.True(t, svc.Agents.HasAgent(id))
	assert.Equal(t, int32(2), starts.Load())
}

func TestCreationReplacement_TerminalRPCWaitsForTheOriginalCheckpoint(t *testing.T) {
	t.Parallel()
	for _, initialFailure := range []bool{true, false} {
		label := "failed restart after successful creation"
		if initialFailure {
			label = "successful restart after failed creation"
		}
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			workingDir := t.TempDir()
			h := newCreationReplacementHarness(t)
			svc := h.svc
			const id = "restart-creation"
			testutil.RegisterTerminalCleanup(t, svc.Terminals, id)
			svc.startTerminalFn = func(ctx context.Context, opts terminal.Options, output terminal.OutputHandler, exit terminal.ExitHandler) error {
				if initialFailure {
					return errors.New("original shell failed")
				}
				if err := svc.startTerminalProcess(ctx, opts, output, exit); err != nil {
					return err
				}
				if err := svc.Terminals.SendInput(id, []byte("exit"+testutil.TestShellEnter())); err != nil {
					return err
				}
				return svc.Terminals.WaitForExitContext(ctx, id)
			}
			resizeStart, resizeStop := testutil.NewTimerTraps(t, h.clock, startupPendingResizeTimerTag)
			method, request := testProcessCreation(leapmuxv1.TabType_TAB_TYPE_TERMINAL, id, workingDir)
			request.(*leapmuxv1.OpenTerminalRequest).Shell = testutil.TestShell()
			writer := newTestWriter()
			dispatch(h.dispatcher, method, request, writer)
			require.Empty(t, writer.rejections())
			resizeDelay := testutil.WaitForTimer(t, h.ctx, resizeStart)
			testutil.AdvanceAndAwaitStop(t, h.ctx, h.clock, resizeDelay, resizeStop)
			delay := h.awaitCheckpoint(t)
			if !initialFailure {
				_, err := svc.DB.ExecContext(h.ctx, "UPDATE terminals SET shell = ? WHERE id = ?", filepath.Join(workingDir, "missing-shell"), id)
				require.NoError(t, err)
			}
			restarted, done := newTestWriter(), make(chan struct{})
			h.observeRead.Store(true)
			h.callers.Go(func() {
				defer close(done)
				dispatch(h.dispatcher, leapmuxv1connect.WorkerTerminalServiceRestartTerminalProcedure, &leapmuxv1.RestartTerminalRequest{TerminalId: id, Cols: 113, Rows: 47}, restarted)
			})
			h.awaitReplacementRead(t)
			assert.Empty(t, restarted.responsesSnapshot())
			assert.Empty(t, restarted.rejections())
			assert.False(t, svc.Terminals.IsRunning(id))
			h.completeCheckpoint(t, delay)
			select {
			case <-done:
			case <-h.ctx.Done():
				t.Fatal("terminal restart did not finish")
			}
			require.Empty(t, restarted.rejections())
			require.Len(t, restarted.responsesSnapshot(), 1)
			svc.TerminalStartup.WaitForInFlight()
			resource, err := svc.Queries.GetTerminal(h.ctx, id)
			require.NoError(t, err)
			if initialFailure {
				assert.Empty(t, resource.StartupError)
				assert.True(t, svc.Terminals.IsRunning(id))
			} else {
				assert.NotEmpty(t, resource.StartupError)
				assert.False(t, svc.Terminals.IsRunning(id))
			}
			row, err := svc.Queries.GetTabCreation(h.ctx, db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: id})
			require.NoError(t, err)
			assert.Equal(t, creationCommitted, row.State)
			assert.Empty(t, row.RuntimePayload)
		})
	}
}

type creationSettingsRelaunchProbe struct{ *nativeRestartProbeAgent }

func (*creationSettingsRelaunchProbe) UpdateSettings(options OptionMap) agent.SettingsApplyResult {
	return agent.RestartRequiredSettings(options)
}

func TestCreationReplacement_InternalSettingsRelaunchKeepsItsEnclosingCreation(t *testing.T) {
	t.Parallel()
	workingDir := t.TempDir()
	svc, dispatcher, _ := setupTestService(t)
	ctx := creationOutcomeContext(t)
	const id = "internal-settings-relaunch"
	firstEntered, secondEntered := make(chan agent.Options, 1), make(chan agent.Options, 1)
	releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
	finishFirst := sync.OnceFunc(func() { close(releaseFirst) })
	finishSecond := sync.OnceFunc(func() { close(releaseSecond) })
	defer func() { finishFirst(); finishSecond(); svc.creations.stop(); drainAllInFlight(svc) }()
	var starts atomic.Int32
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		count := starts.Add(1)
		permit := releaseFirst
		if count == 1 {
			firstEntered <- opts
		} else {
			secondEntered <- opts
			permit = releaseSecond
		}
		select {
		case <-permit:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		probe := newNativeRestartProbeAgent(id)
		probe.restartRequired = false
		result, err := svc.Agents.StartAgentWith(ctx, opts, sink, func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
			if count == 1 {
				return &creationSettingsRelaunchProbe{nativeRestartProbeAgent: probe}, nil
			}
			return probe, nil
		})
		sink.UpdateSessionID(fmt.Sprintf("settings-session-%d", count))
		return result, err
	}
	writer := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceOpenAgentProcedure, &leapmuxv1.OpenAgentRequest{Creation: creationIntent(id), WorkingDir: workingDir, Options: map[string]string{agent.OptionIDModel: "opus"}}, writer)
	require.Empty(t, writer.rejections())
	select {
	case opts := <-firstEntered:
		assert.Equal(t, "opus", opts.Model())
	case <-ctx.Done():
		t.Fatal("initial startup did not reach its handshake")
	}
	update := newTestWriter()
	dispatch(dispatcher, leapmuxv1connect.WorkerAgentServiceUpdateAgentSettingsProcedure, &leapmuxv1.UpdateAgentSettingsRequest{AgentId: id, Settings: &leapmuxv1.AgentSettings{Options: map[string]string{agent.OptionIDModel: "sonnet"}}}, update)
	require.Empty(t, update.rejections())
	finishFirst()
	select {
	case opts := <-secondEntered:
		assert.Equal(t, "sonnet", opts.Model())
	case <-ctx.Done():
		t.Fatal("creation waited on its own checkpoint instead of relaunching")
	}
	key := db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: id}
	row, err := svc.Queries.GetTabCreation(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, creationPublished, row.State)
	assert.NotEmpty(t, row.RuntimePayload)
	assert.NotNil(t, svc.AgentStartup.creationCheckpointDone(id))
	finishSecond()
	svc.AgentStartup.WaitForInFlight()
	resource, err := svc.Queries.GetAgentByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "sonnet", loadOptions(svc.Agents.Registry(), resource.Options, resource.AgentProvider)[agent.OptionIDModel])
	assert.Equal(t, "settings-session-2", resource.AgentSessionID)
	assert.Empty(t, resource.StartupError)
	assert.True(t, svc.Agents.HasAgent(id))
	assert.Equal(t, int32(2), starts.Load())
	row, err = svc.Queries.GetTabCreation(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, creationCommitted, row.State)
	assert.Empty(t, row.RuntimePayload)
	assert.Empty(t, row.RequestPayload)
}
