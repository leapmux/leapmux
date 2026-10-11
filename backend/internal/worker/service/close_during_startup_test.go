package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

type closeAgentWriteFaultDB struct{ *sql.DB }

func (d closeAgentWriteFaultDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "UPDATE agents SET closed_at") {
		return nil, errors.New("the close write failed")
	}
	return d.DB.ExecContext(ctx, query, args...)
}

func TestCloseAgentBlocksNewStartBeforeTeardown(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	recorder := newStartRecorder()
	recorder.install(svc)
	const agentID = "agent-close-admission"
	seedOpenAgent(t, svc, agentID, true)

	closeBeforeTeardown := make(chan struct{})
	releaseClose := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseClose) }) }
	defer release()
	svc.beforeAgentCloseTeardownFn = func(string) {
		close(closeBeforeTeardown)
		<-releaseClose
	}
	closed := make(chan struct{})
	go func() {
		svc.closeAgentTabCommon("", agentID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
		close(closed)
	}()
	select {
	case <-closeBeforeTeardown:
	case <-ctx.Done():
		t.Fatal("the close did not reach its held teardown")
	}

	err := svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart)
	assert.ErrorContains(t, err, "closing", "a close must refuse a new startup before teardown")
	assert.Empty(t, recorder.ids(), "no new provider start can escape the earlier cancellation")
	release()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("the close did not finish after teardown was released")
	}
}

func TestAgentResumeCloseBeforeRegistrationStopsLateProcess(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, dispatcher, _ := setupTestService(t)
	const agentID = "agent-late-resume"
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })
	seedOpenAgent(t, svc, agentID, true)
	watcher := newTestWriter()
	dispatch(dispatcher, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{
			AgentId: agentID, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST,
			Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL, ReplayId: 1,
		}},
	}, watcher)
	waitAgentWatchLive(t, svc, agentID)

	late := newNativeRestartProbeAgent(agentID)
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStart) }) }
	defer release()
	svc.startBackgroundAgentFn = func(startCtx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		close(startEntered)
		<-releaseStart
		return svc.Agents.StartAgentWith(startCtx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return late, nil
			})
	}
	resumer := svc.AgentResumer()
	resumer.Start(ctx)
	select {
	case <-startEntered:
	case <-ctx.Done():
		t.Fatal("the resume did not enter the held provider start")
	}
	svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_AGENT, "", agentID)
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	require.True(t, row.ClosedAt.Valid, "close stamps the row before the provider registers")
	release()
	resumer.WaitForSweepForTest()
	assert.True(t, late.IsStopped(), "a process that registers after close cannot survive")
	assert.False(t, svc.Agents.HasAgent(agentID))
	// The catch-up replays on the session goroutine after the watch registers,
	// so it can still run here. Wait for its end, so the assertion below covers
	// its status marker on every run and the replay does not outlive the test.
	require.Eventually(t, func() bool {
		for _, event := range watchedAgentEvents(watcher) {
			if event.GetCatchUpComplete() != nil {
				return true
			}
		}
		return false
	}, inputQueueWait, 10*time.Millisecond, "the catch-up never completed")
	for _, event := range watchedAgentEvents(watcher) {
		if status := event.GetStatusChange(); status != nil {
			assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, status.GetStatus(),
				"a closed tab cannot publish ACTIVE after its late provider start")
		}
	}
}

// stopGatedAgent is a probe agent whose Stop waits for the test. It keeps a
// late process registered in the manager for as long as the test needs it.
type stopGatedAgent struct {
	*nativeRestartProbeAgent
	stopEntered chan struct{}
	releaseStop chan struct{}
	enterOnce   sync.Once
}

func (a *stopGatedAgent) Stop() {
	a.enterOnce.Do(func() { close(a.stopEntered) })
	<-a.releaseStop
	a.nativeRestartProbeAgent.Stop()
}

// watchedAgentEvents decodes every agent event that w received.
func watchedAgentEvents(w *testResponseWriter) []*leapmuxv1.AgentEvent {
	var events []*leapmuxv1.AgentEvent
	for _, payload := range w.streamsSnapshot() {
		var event leapmuxv1.WatchEventsResponse
		if proto.Unmarshal(payload.GetPayload(), &event) != nil {
			continue
		}
		if agentEvent := event.GetAgentEvent(); agentEvent != nil {
			events = append(events, agentEvent)
		}
	}
	return events
}

// TestCatchUpReplayDoesNotReportALateProcessOfAClosedTab reproduces, with
// gates instead of timing, the interleaving that
// TestAgentResumeCloseBeforeRegistrationStopsLateProcess hits under load.
//
// The WatchEvents catch-up registers the watch first and replays after it, on
// the session goroutine. It reads the agent row when it resolves the watch,
// and it asks the manager whether a process runs only at its closing status
// marker. Between the two reads, the resume sweep's provider start ends after
// a close: the late process registers, and it stays registered until the
// launch validation stops it. A marker that combined the row from before the
// close with the process from after it reported ACTIVE for a closed tab.
func TestCatchUpReplayDoesNotReportALateProcessOfAClosedTab(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, dispatcher, _ := setupTestService(t)
	const agentID = "agent-late-replay"
	seedOpenAgent(t, svc, agentID, true)

	// Gate 1: the catch-up waits in its git-status batch, after it read the
	// agent row and before its status marker.
	replayHeld := make(chan struct{})
	releaseReplay := make(chan struct{})
	var heldOnce, releaseReplayOnce sync.Once
	letReplayGo := func() { releaseReplayOnce.Do(func() { close(releaseReplay) }) }
	defer letReplayGo()
	svc.batchGitStatusFn = func(_ context.Context, dirs []string) []*leapmuxv1.GitRepoStatus {
		heldOnce.Do(func() { close(replayHeld) })
		<-releaseReplay
		return make([]*leapmuxv1.GitRepoStatus, len(dirs))
	}
	watcher := newTestWriter()
	dispatch(dispatcher, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{
			AgentId: agentID, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST,
			Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL, ReplayId: 1,
		}},
	}, watcher)
	select {
	case <-replayHeld:
	case <-ctx.Done():
		require.FailNow(t, "the catch-up never reached its git-status batch")
	}

	// Gate 2: the resume sweep's provider start waits until after the close.
	// Gate 3: the late process stays registered until the test lets the launch
	// validation stop it.
	late := &stopGatedAgent{
		nativeRestartProbeAgent: newNativeRestartProbeAgent(agentID),
		stopEntered:             make(chan struct{}),
		releaseStop:             make(chan struct{}),
	}
	var releaseStopOnce sync.Once
	letStopGo := func() { releaseStopOnce.Do(func() { close(late.releaseStop) }) }
	defer letStopGo()
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	svc.startBackgroundAgentFn = func(startCtx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		close(startEntered)
		<-releaseStart
		return svc.Agents.StartAgentWith(startCtx, opts, sink,
			func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
				return late, nil
			})
	}
	resumer := svc.AgentResumer()
	resumer.Start(ctx)
	select {
	case <-startEntered:
	case <-ctx.Done():
		require.FailNow(t, "the resume never entered its provider start")
	}
	svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_AGENT, "", agentID)
	require.True(t, requireAgentRow(t, svc, agentID).ClosedAt.Valid, "fixture check: the close stamped the row")
	close(releaseStart)
	select {
	case <-late.stopEntered:
	case <-ctx.Done():
		require.FailNow(t, "the launch validation never stopped the late process")
	}
	require.True(t, svc.Agents.HasAgent(agentID), "fixture check: the late process is registered")

	// The catch-up reaches its status marker while the late process of the
	// closed tab is registered.
	letReplayGo()
	require.Eventually(t, func() bool {
		for _, event := range watchedAgentEvents(watcher) {
			if event.GetCatchUpComplete() != nil {
				return true
			}
		}
		return false
	}, inputQueueWait, 10*time.Millisecond, "the catch-up never completed")
	letStopGo()
	resumer.WaitForSweepForTest()

	for _, event := range watchedAgentEvents(watcher) {
		if status := event.GetStatusChange(); status != nil {
			assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE.String(), status.GetStatus().String(),
				"the catch-up reported ACTIVE for a closed tab, from the row it read before the close")
		}
	}
	assert.True(t, late.IsStopped(), "the late process of a closed tab must not survive")
}

// holdCloseBeforeTeardown starts a close of agentID and holds it after its
// admission, before it stops the process and stamps closed_at. The returned
// function lets the close finish and waits for it.
func holdCloseBeforeTeardown(t *testing.T, svc *Service, agentID string) func() {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	held := make(chan struct{})
	releaseClose := make(chan struct{})
	svc.beforeAgentCloseTeardownFn = func(string) {
		close(held)
		<-releaseClose
	}
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		svc.closeAgentTabCommon("", agentID, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
	}()
	select {
	case <-held:
	case <-ctx.Done():
		require.FailNow(t, "the close never reached its held teardown")
	}
	var once sync.Once
	finish := func() {
		once.Do(func() {
			close(releaseClose)
			// Not ctx: finish also runs as a cleanup, and the test context ends
			// before the cleanups run. The limit is a deadlock guard only.
			select {
			case <-closed:
			case <-time.After(inputQueueWait):
				require.FailNow(t, "the close never finished after its teardown was released")
			}
		})
	}
	t.Cleanup(finish)
	return finish
}

// TestSinkStatusIsNotActiveWhileACloseIsInProgress pins the guard on the
// status that a provider pushes through its sink. A close stamps closed_at only
// after it stopped the process, so the row that a late provider push reads
// still says open inside that window. The close admission must answer for it.
func TestSinkStatusIsNotActiveWhileACloseIsInProgress(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	const agentID = "agent-sink-close"
	seedOpenAgent(t, svc, agentID, true)
	svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	sink := requireRootOutputSink(t, svc.Output, agentID)

	row := requireAgentRow(t, svc, agentID)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE.String(),
		sink.buildStatusChange(row, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, "").GetStatus().String(),
		"fixture check: an open tab with no close reports ACTIVE")

	finish := holdCloseBeforeTeardown(t, svc, agentID)
	row = requireAgentRow(t, svc, agentID)
	require.False(t, row.ClosedAt.Valid, "fixture check: the held close did not stamp the row yet")
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE.String(),
		sink.buildStatusChange(row, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, "").GetStatus().String(),
		"a provider push reported ACTIVE while a close of its tab was in progress")

	finish()
	row = requireAgentRow(t, svc, agentID)
	require.True(t, row.ClosedAt.Valid)
	assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE.String(),
		sink.buildStatusChange(row, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, "").GetStatus().String(),
		"a provider push reported ACTIVE for a closed tab")
}

// TestBroadcastAgentActiveSkipsATabThatMayNotReportActive pins the guard on the
// service's own ACTIVE broadcast. Its callers validate the row first, and a
// close or an archive can land between that check and the broadcast.
func TestBroadcastAgentActiveSkipsATabThatMayNotReportActive(t *testing.T) {
	t.Parallel()

	activeCount := func(w *testResponseWriter) int {
		count := 0
		for _, event := range watchedAgentEvents(w) {
			if event.GetStatusChange().GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE {
				count++
			}
		}
		return count
	}
	for _, tc := range []struct {
		name      string
		end       func(t *testing.T, svc *Service, agentID string)
		broadcast bool
	}{
		{name: "open tab", end: func(*testing.T, *Service, string) {}, broadcast: true},
		{name: "close in progress", end: func(t *testing.T, svc *Service, agentID string) {
			holdCloseBeforeTeardown(t, svc, agentID)
		}},
		{name: "closed tab", end: func(t *testing.T, svc *Service, agentID string) {
			holdCloseBeforeTeardown(t, svc, agentID)()
		}},
		{name: "archived workspace", end: func(t *testing.T, svc *Service, agentID string) {
			_, err := svc.Queries.SetAgentWorkspaceArchived(t.Context(), db.SetAgentWorkspaceArchivedParams{
				WorkspaceArchived: true, ID: agentID,
			})
			require.NoError(t, err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, _, w := setupTestService(t)
			const agentID = "agent-active-guard"
			seedOpenAgent(t, svc, agentID, true)
			registerAgentWatch(svc, w.channelID, agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, w)
			// The row as a caller read it before the close or the archive.
			row := requireAgentRow(t, svc, agentID)
			tc.end(t, svc, agentID)

			svc.broadcastAgentActive(&row, nil)
			if tc.broadcast {
				assert.Equal(t, 1, activeCount(w))
				return
			}
			assert.Zero(t, activeCount(w), "the tab may not report ACTIVE, and the broadcast reported it")
		})
	}
}

func TestCloseAgentReleasesAdmissionAfterDatabaseFailure(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, _, _ := setupTestService(t)
	const agentID = "agent-close-write-fault"
	seedOpenAgent(t, svc, agentID, true)
	original := svc.Queries
	svc.Queries = db.New(closeAgentWriteFaultDB{DB: svc.DB})
	result, _ := svc.closeAgentTabCommon("", agentID,
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
	svc.Queries = original
	require.Contains(t, result.GetFailureDetail(), "the close write failed")
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.False(t, row.ClosedAt.Valid, "the database failure leaves the tab open")

	recorder := newStartRecorder()
	recorder.install(svc)
	require.NoError(t, svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart))
	assert.Equal(t, []string{agentID}, recorder.ids(),
		"the failed close releases its admission guard for a later start")
}

func TestCloseAgentReleasesAdmissionAfterPanic(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	const agentID = "agent-close-panic"
	seedOpenAgent(t, svc, agentID, true)
	svc.beforeAgentCloseTeardownFn = func(string) {
		panic("close teardown probe failed")
	}
	func() {
		defer func() { assert.Equal(t, "close teardown probe failed", recover()) }()
		svc.closeAgentTabCommon("", agentID,
			leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
	}()
	svc.beforeAgentCloseTeardownFn = nil

	recorder := newStartRecorder()
	recorder.install(svc)
	require.NoError(t, svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart))
	assert.Equal(t, []string{agentID}, recorder.ids(),
		"a panic before the database close releases the admission guard")
}

// TestCloseAgent_DuringStartup_SuppressesActiveAndCleansUp pins the
// post-spawn close-detection path at agent.go:1179-1193: the user
// clicks close while the runAgentStartup goroutine is parked inside
// phase 2 (subprocess startup handshake). Contract points verified:
//
//  1. CloseAgent cancels the startup context so a startAgentFn that
//     parks on `<-ctx.Done()` unblocks — no orphan goroutine.
//  2. The post-spawn closed_at re-check suppresses ACTIVE: a client
//     must never see ACTIVE for a tab the user already asked to close.
//  3. DB row is soft-deleted; the agent manager has no subprocess
//     registered; any git-mode mutation from phase 0 is rolled back.
//
// The test drives CloseAgent *synchronously from inside startAgentFn*.
// That removes the in-production race between `cancelAndClear` and the
// DB write to `closed_at`: by the time startAgentFn returns, CloseAgent
// has completed all five steps (cancel, stop, cleanup, CloseAgent DB
// write, unregister-tab), so the goroutine's post-spawn re-read is
// guaranteed to see `closed_at=true` and follow the close-detection
// branch rather than the startup-failure branch. The close-detection
// branch is the one this test is meant to exercise — the failure
// branch is already covered by TestOpenAgent_StartupFailure* tests.
func TestCloseAgent_DuringStartup_SuppressesActiveAndCleansUp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	// Subscribe before OpenAgent so an accidental ACTIVE broadcast would
	// be captured regardless of where in the sequence it fires.
	wWatch := newTestWriter()

	var (
		closeOnce    sync.Once
		startEntered = make(chan string, 1)
	)
	svc.startAgentFn = func(sCtx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		closeOnce.Do(func() {
			startEntered <- opts.AgentID
			// Subscribe here — by this point the DB row exists, so
			// WatchEvents accepts the subscription.
			dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
				Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: opts.AgentID, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST, Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL, ReplayId: 1}},
			}, wWatch)
			// The subscription lands on the session goroutine, after dispatch
			// returns. Without this wait the CloseAgent below could broadcast to
			// nobody, and the "ACTIVE never arrived" assertion at the end would
			// pass having observed nothing.
			waitAgentWatchLive(t, svc, opts.AgentID)

			// Drive CloseAgent synchronously. dispatch returns only
			// after the full handler runs, so when control comes back
			// here the ctx is cancelled and closed_at is set in the DB.
			wClose := newTestWriter()
			dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: opts.AgentID}, wClose)
		})
		<-sCtx.Done()
		return nil, sCtx.Err()
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()
	require.NotEmpty(t, agentID)

	// Sanity-check: the mock was invoked (runAgentStartup reached phase 2).
	select {
	case got := <-startEntered:
		require.Equal(t, agentID, got)
	case <-time.After(5 * time.Second):
		t.Fatal("startAgentFn never invoked — runAgentStartup did not reach phase 2")
	}

	// Under Eventually: DB row is closed (synchronous CloseAgent write),
	// manager has no subprocess, and the startup registry has been
	// cleared. The close-detection branch ends with AgentStartup.succeed
	// which deletes the entry; it does NOT re-insert like the failure
	// branch does.
	require.Eventually(t, func() bool {
		_, _, _, registered := svc.AgentStartup.status(agentID)
		if registered {
			return false
		}
		row, err := svc.Queries.GetAgentByID(ctx, agentID)
		if err != nil || !row.ClosedAt.Valid {
			return false
		}
		return !svc.Agents.HasAgent(agentID)
	}, 5*time.Second, 20*time.Millisecond,
		"agent should be fully closed: registry empty, closed_at set, no subprocess")

	// Assert no ACTIVE broadcast ever arrived on the watcher.
	for _, s := range wWatch.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		sc := resp.GetAgentEvent().GetStatusChange()
		if sc == nil {
			continue
		}
		assert.NotEqual(t, leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE, sc.GetStatus(),
			"CloseAgent during startup must suppress ACTIVE broadcast (got status=%s)", sc.GetStatus())
	}
}

// closeAgentDuringStartup drives the shared shape of the two tests below:
// phase 0 creates a worktree and branch, phase 2 parks inside startAgentFn,
// and a CloseAgent carrying `action` lands mid-phase-2 so the post-spawn
// close-detection branch runs deterministically. Returns the agent id once
// the startup goroutine (and its trailing git work) has returned.
func closeAgentDuringStartup(t *testing.T, repoDir, branchName, worktreePath string, action leapmuxv1.WorktreeAction) (*Service, string) {
	t.Helper()

	svc, d, w := setupTestService(t)
	t.Cleanup(func() { drainAllInFlight(svc) })

	var closeOnce sync.Once
	svc.startAgentFn = func(sCtx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		closeOnce.Do(func() {
			// Worktree must exist by the time we get here — phase 0
			// ran to completion before phase 2 was entered.
			require.DirExists(t, worktreePath)
			require.True(t, localBranchExists(t, repoDir, branchName))

			wClose := newTestWriter()
			dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{
				AgentId:        opts.AgentID,
				WorktreeAction: action,
			}, wClose)
		})
		<-sCtx.Done()
		return nil, sCtx.Err()
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
		AgentProvider:  leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))

	// The worktree work happens in the goroutine after startAgentFn returns.
	// Wait for that goroutine deterministically rather than polling under
	// Eventually — Windows CI takes several seconds for `git worktree add`
	// + `git worktree remove` + `git branch -D`, so a fixed polling budget
	// flakes when the cumulative git time outruns it.
	svc.AgentStartup.WaitForInFlight()
	return svc, openResp.GetAgent().GetId()
}

// TestCloseAgent_DuringStartup_UnlinkedRemoveStillRollsBack covers the window
// the startup rollback exists for, and ONLY it: a REMOVE close that arrives
// before the worktree_tabs link is written, so closeTabCommon's
// GetWorktreeForTab finds nothing and its REMOVE degrades to KEEP. Nothing but
// the post-spawn rollback can honour the user's choice there.
//
// The link is deleted from under the close rather than waiting for the real
// (tiny, unsteerable) window between `git worktree add` and AddWorktreeTab.
// Deleting it reproduces the same observable state the close would see, and
// does so deterministically.
func TestCloseAgent_DuringStartup_UnlinkedRemoveStillRollsBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoDir := initRepo(t)
	branchName := "feature/unlinked-remove"
	worktreePath := expectedWorktreePath(t, repoDir, branchName)

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	var closeOnce sync.Once
	svc.startAgentFn = func(sCtx context.Context, opts agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		closeOnce.Do(func() {
			require.DirExists(t, worktreePath)
			// Drop the link phase 0 just wrote, so the close below is the
			// pre-link shape: it can see the tab but not its worktree.
			require.NoError(t, svc.Queries.DeleteWorktreeTabsByTabID(ctx, db.DeleteWorktreeTabsByTabIDParams{
				TabType: leapmuxv1.TabType_TAB_TYPE_AGENT,
				TabID:   opts.AgentID,
				UserID:  "",
			}))
			wClose := newTestWriter()
			dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{
				AgentId:        opts.AgentID,
				WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
			}, wClose)
		})
		<-sCtx.Done()
		return nil, sCtx.Err()
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
		AgentProvider:  leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	svc.AgentStartup.WaitForInFlight()

	_, statErr := os.Stat(worktreePath)
	assert.True(t, os.IsNotExist(statErr),
		"a REMOVE the close could not honour must still be honoured by the startup rollback (stat err=%v)", statErr)
	assert.False(t, localBranchExists(t, repoDir, branchName), "branch must be deleted")
	_, wtErr := svc.Queries.GetWorktreeByPath(ctx, worktreePath)
	assert.ErrorIs(t, wtErr, sql.ErrNoRows, "worktree DB row must be cleaned up")
}

// TestCloseAgent_DuringStartup_RollsBackCreatedWorktree extends the
// close-detection test to the git-mode path: phase 0 created a worktree
// and branch before phase 2 parked; a REMOVE CloseAgent lands mid-phase-2.
//
// Here phase 0 already wrote the worktree_tabs link, so closeTabCommon itself
// resolves the worktree and removes it; the assertions below are on the
// end state that close must reach. The narrower window where the link does not
// exist yet -- the one the startup rollback is the only remedy for -- is
// covered by TestCloseAgent_DuringStartup_UnlinkedRemoveStillRollsBack.
func TestCloseAgent_DuringStartup_RollsBackCreatedWorktree(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoDir := initRepo(t)
	branchName := "feature/close-during-startup"
	worktreePath := expectedWorktreePath(t, repoDir, branchName)

	svc, agentID := closeAgentDuringStartup(t, repoDir, branchName, worktreePath,
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE)

	_, statErr := os.Stat(worktreePath)
	assert.True(t, os.IsNotExist(statErr), "worktree directory must be removed (stat err=%v)", statErr)
	assert.False(t, localBranchExists(t, repoDir, branchName), "branch must be deleted")
	_, wtErr := svc.Queries.GetWorktreeByPath(ctx, worktreePath)
	assert.ErrorIs(t, wtErr, sql.ErrNoRows, "worktree DB row must be cleaned up")
	_, _, _, registered := svc.AgentStartup.status(agentID)
	assert.False(t, registered, "AgentStartup registry entry must be cleared")

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid)
}

// TestCloseAgent_DuringStartup_KeepPreservesCreatedWorktree is the other half,
// and the regression this pair exists for: closing a tab must have the SAME
// effect on the worktree whether or not the close raced startup.
//
// A KEEP close is what "Close anyway" in the last-tab dialog sends, what an
// ordinary close of a non-last tab sends, and what the unreachable-worker path
// pins. The close-detection branch used to roll the worktree back regardless,
// so a user who was shown the dialog and explicitly chose to keep the
// directory lost it — along with any uncommitted work in it — purely because
// the agent was still starting. `git worktree remove --force` there is silent
// on success, so nothing in the log said where it went.
func TestCloseAgent_DuringStartup_KeepPreservesCreatedWorktree(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoDir := initRepo(t)
	branchName := "feature/keep-during-startup"
	worktreePath := expectedWorktreePath(t, repoDir, branchName)

	svc, agentID := closeAgentDuringStartup(t, repoDir, branchName, worktreePath,
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP)

	assert.DirExists(t, worktreePath, "a KEEP close must leave the worktree directory on disk")
	assert.True(t, localBranchExists(t, repoDir, branchName), "a KEEP close must leave the branch")

	// The row survives too, and with zero links -- the same shape an online
	// KEEP close of a fully-started tab leaves, which
	// ListOrphanCandidateWorktrees deliberately excludes so nothing reclaims
	// it behind the user's back.
	wt, wtErr := svc.Queries.GetWorktreeByPath(ctx, worktreePath)
	require.NoError(t, wtErr, "the worktree row must survive a KEEP close")
	links, err := svc.Queries.CountWorktreeTabs(ctx, wt.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), links, "no strand link may be left behind")
	orphans, err := svc.Queries.ListOrphanCandidateWorktrees(ctx)
	require.NoError(t, err)
	for _, o := range orphans {
		assert.NotEqual(t, wt.ID, o.ID, "a KEEP-closed worktree must never become a GC candidate")
	}

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "the tab still closes")
}

// TestCloseTerminal_DuringStartup_SuppressesReadyAndCleansUp is the
// terminal-side analog of the close-during-startup test. It pins the
// post-spawn closed_at re-check in runTerminalStartup: when
// CloseTerminal lands while startTerminalFn is still in flight, the
// goroutine must stop the PTY it just spawned, skip the READY
// broadcast, roll back any phase-0 git mutation, and leave DB state
// consistent (closed_at set, worktree DB row cleaned).
//
// As with the agent test, CloseTerminal is driven synchronously from
// inside startTerminalFn so the post-spawn re-read deterministically
// sees closed_at=true — otherwise the goroutine would race with the
// CloseTerminal DB write and land in failTerminalStartup, which is
// already covered by TestOpenTerminal_* tests.
//
// The close carries REMOVE because that is the only disposition the rollback
// acts on; the KEEP half of the contract is pinned on the agent side by
// TestCloseAgent_DuringStartup_KeepPreservesCreatedWorktree, and both paths
// share registerTabForWorktreeAfterClose / rollbackGitModeAfterClose.
func TestCloseTerminal_DuringStartup_SuppressesReadyAndCleansUp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	repoDir := initRepo(t)
	branchName := "feature/close-term-during-startup"
	worktreePath := expectedWorktreePath(t, repoDir, branchName)

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	wWatch := newTestWriter()

	var closeOnce sync.Once
	svc.startTerminalFn = func(sCtx context.Context, opts terminal.Options, _ terminal.OutputHandler, _ terminal.ExitHandler) error {
		closeOnce.Do(func() {
			// Worktree and branch were created in phase 0 before we got here.
			require.DirExists(t, worktreePath)
			require.True(t, localBranchExists(t, repoDir, branchName))

			dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
				Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: opts.ID}},
			}, wWatch)
			// See the agent-side test: the subscription is asynchronous, and a
			// "READY never arrived" assertion over an unregistered watcher is
			// vacuous.
			waitTerminalWatchLive(t, svc, opts.ID)

			wClose := newTestWriter()
			dispatch(d, "CloseTerminal", &leapmuxv1.CloseTerminalRequest{
				TerminalId:     opts.ID,
				WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
			}, wClose)
		})
		// Return sCtx.Err() to simulate "spawn aborted" — exercises the
		// close-detected branch with startErr != nil. The branch must
		// still suppress READY and roll back the worktree.
		return sCtx.Err()
	}

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
		Shell:          "/bin/zsh",
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	terminalID := openResp.GetTerminalId()
	require.NotEmpty(t, terminalID)

	// Rollback + cleanup happen in the goroutine after startTerminalFn
	// returns. Wait for that goroutine deterministically rather than
	// polling — see the agent-side test above for why the fixed polling
	// budget flakes on Windows CI.
	svc.TerminalStartup.WaitForInFlight()

	_, statErr := os.Stat(worktreePath)
	assert.True(t, os.IsNotExist(statErr), "worktree directory must be removed (stat err=%v)", statErr)
	assert.False(t, localBranchExists(t, repoDir, branchName), "branch must be deleted")
	_, wtErr := svc.Queries.GetWorktreeByPath(ctx, worktreePath)
	assert.ErrorIs(t, wtErr, sql.ErrNoRows, "worktree DB row must be cleaned up")
	row, err := svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "terminal DB row must have closed_at set")
	_, _, _, registered := svc.TerminalStartup.status(terminalID)
	assert.False(t, registered, "TerminalStartup registry entry must be cleared")
	assert.False(t, svc.Terminals.HasTerminal(terminalID), "PTY must be dropped from the manager")

	// READY must never have been broadcast — the post-spawn closed_at
	// re-check in runTerminalStartup has to short-circuit that path.
	for _, s := range wWatch.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		sc := resp.GetTerminalEvent().GetStatusChange()
		if sc == nil {
			continue
		}
		assert.NotEqual(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, sc.GetStatus(),
			"CloseTerminal during startup must suppress READY broadcast (got status=%s)", sc.GetStatus())
	}
}

// TestFailStartup_KeepCloseLeavesWorktreeAlone pins the startup-FAILURE half of
// the close-disposition contract, which is the half the close-detected branch
// cannot cover.
//
// closeTabCommon runs stopProcess -- and so cancelAndClear, which records the
// disposition -- BEFORE closeDB writes closed_at. A cancelled startup therefore
// usually surfaces as an error out of phase 0 or startAgent while closed_at is
// still unreadable, which routes it to failStartup rather than to the
// close-detected branch. failStartup used to call rollbackGitMode
// unconditionally, so "Close anyway" (= KEEP my worktree) on the last-tab
// dialog destroyed the worktree and its branch whenever it landed in that
// window -- silently, since that path only logs on failure.
func TestFailStartup_KeepCloseLeavesWorktreeAlone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		branch      string
		disposition closeWorktreeDisposition
		wantRemoved bool
	}{
		{"keep close leaves it", "feat/fail-keep", keepWorktreeOnClose, false},
		{"strand close leaves it for the reconciler", "feat/fail-strand", strandWorktreeOnClose, false},
		{"remove close still rolls back", "feat/fail-remove", removeWorktreeOnClose, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			svc, _, _ := setupTestService(t)
			defer drainAllInFlight(svc)

			repoDir := testutil.NewGitRepo(t)
			branchName := tc.branch
			gm := createWorktreeForTest(t, svc, repoDir, branchName)
			require.DirExists(t, gm.Rollback.CreatedWorktree.WorktreePath)

			dbAgent := createAgentRowForTest(t, svc, gm.WorkingDir)

			// Record the close the way a real CloseAgent does: cancelAndClear
			// runs while the startup is still in flight, before closed_at.
			h := svc.AgentStartup.begin(dbAgent.ID, func() {})
			svc.AgentStartup.cancelAndClear(dbAgent.ID, tc.disposition)

			svc.failAgentStartup(&dbAgent, gm, context.Canceled, nil, h)

			_, statErr := os.Stat(gm.Rollback.CreatedWorktree.WorktreePath)
			if tc.wantRemoved {
				assert.True(t, os.IsNotExist(statErr), "worktree must be removed (stat err=%v)", statErr)
				assert.False(t, localBranchExists(t, repoDir, branchName), "branch must be deleted")
			} else {
				assert.NoError(t, statErr, "worktree the user asked to keep must survive a failed startup")
				assert.True(t, localBranchExists(t, repoDir, branchName), "its branch must survive too")
				_, wtErr := svc.Queries.GetWorktreeByPath(ctx, gm.Rollback.CreatedWorktree.WorktreePath)
				assert.NoError(t, wtErr, "the tracked worktree row must survive")
			}
			svc.AgentStartup.finishEntry(h)
		})
	}
}

// TestFailStartup_UncontestedFailureStillRollsBack is the other side of the
// same fork: with NO close recorded, a failed startup owns the rollback --
// nothing else will undo the mutation, and leaving it would strand a worktree
// and a branch the user never saw. It is what makes closeDisposition's `ok`
// return load-bearing rather than decorative: without it, "no close raced" and
// "a KEEP close raced" both arrive as the zero value and one of the two is
// always handled wrongly.
func TestFailStartup_UncontestedFailureStillRollsBack(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	repoDir := testutil.NewGitRepo(t)
	branchName := "feat/uncontested"
	gm := createWorktreeForTest(t, svc, repoDir, branchName)
	require.DirExists(t, gm.Rollback.CreatedWorktree.WorktreePath)

	dbAgent := createAgentRowForTest(t, svc, gm.WorkingDir)
	// No begin(): nothing raced this startup, so it owns its own rollback.
	svc.failAgentStartup(&dbAgent, gm, context.Canceled, nil, nil)

	_, statErr := os.Stat(gm.Rollback.CreatedWorktree.WorktreePath)
	assert.True(t, os.IsNotExist(statErr), "an uncontested startup failure must roll back (stat err=%v)", statErr)
	assert.False(t, localBranchExists(t, repoDir, branchName), "and delete the branch it created")
}

func TestFailStartup_ArchiveRollsBackOnlyIncompleteGitMutation(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name           string
		phase0Complete bool
		wantRemoved    bool
	}{
		{name: "incomplete", wantRemoved: true},
		{name: "complete", phase0Complete: true, wantRemoved: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			repoDir := testutil.NewGitRepo(t)
			gm := createWorktreeForTest(t, svc, repoDir, "feat/archive-"+testCase.name)
			dbAgent := createAgentRowForTest(t, svc, gm.WorkingDir)
			handle := svc.AgentStartup.begin(dbAgent.ID, func() {})
			require.NotNil(t, handle)
			if testCase.phase0Complete {
				svc.linkWorktreeAfterPhase0(&svc.AgentStartup.startupCore, handle, gm.WorktreeID,
					leapmuxv1.TabType_TAB_TYPE_AGENT, dbAgent.ID, false)
			}
			svc.AgentStartup.cancelForArchive(dbAgent.ID)

			svc.failAgentStartup(&dbAgent, gm, context.Canceled, nil, handle)
			svc.AgentStartup.finishEntry(handle)

			_, statErr := os.Stat(gm.Rollback.CreatedWorktree.WorktreePath)
			if testCase.wantRemoved {
				assert.True(t, os.IsNotExist(statErr), "an incomplete archive startup must roll back")
				return
			}
			assert.NoError(t, statErr, "a completed worktree association must survive archive")
			links, err := svc.Queries.CountWorktreeTabs(context.Background(), gm.WorktreeID)
			require.NoError(t, err)
			assert.Equal(t, int64(1), links)
		})
	}
}

// createWorktreeForTest runs the real create-worktree git mode and returns its
// result, so the rollback metadata under test is the metadata production
// builds rather than a hand-assembled struct.
func createWorktreeForTest(t *testing.T, svc *Service, repoDir, branchName string) gitModeResult {
	t.Helper()
	plan, err := svc.validateGitMode(context.Background(), repoDir, openAgentGitModeReq(&leapmuxv1.OpenAgentRequest{
		CreateWorktree: true,
		WorktreeBranch: branchName,
	}))
	require.NoError(t, err)
	gm, err := svc.executeGitMode(context.Background(), plan)
	require.NoError(t, err)
	require.NotNil(t, gm.Rollback.CreatedWorktree, "create-worktree must record rollback metadata")
	return gm
}

func createAgentRowForTest(t *testing.T, svc *Service, workingDir string) db.Agent {
	t.Helper()
	agentID := "a-" + t.Name()
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID: agentID, WorkingDir: workingDir, HomeDir: workingDir,
	}))
	row, err := svc.getAgentByID(context.Background(), agentID)
	require.NoError(t, err)
	return row
}
