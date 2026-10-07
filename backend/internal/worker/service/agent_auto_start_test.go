package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestEnsureAgentRunning_SerializesConcurrentColdStarts is the regression guard for [C11]:
// the auto-start path's HasAgent check and startAgent call must run under the per-agent
// lifecycle lock (LockAgent), so two concurrent cold starts can't both pass the check and
// spawn duplicate subprocesses (the second overwriting and orphaning the first in the
// manager's agent map). Without the lock, both ensureAgentRunning calls enter startAgent
// concurrently; with it, the second blocks until the first finishes.
//
// The second caller is the resume sweep (backgroundStart), because it is the caller that
// reaches the lock without waiting first. A request or a queue drain waits for the startup
// in flight before the lock instead, which TestEnqueueAgentInput_DuringAnOpenStartupIsDelivered
// and TestEnsureAgentRunning_RefusesWhenTheStartupItJoinedFailed cover.
//
// The test observes the second caller WAITING on the lock through the lock's own count of
// its callers, so it needs no window sized by a sleep: a parked caller cannot reach
// startAgent while the first one holds the lock.
func TestEnsureAgentRunning_SerializesConcurrentColdStarts(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	ctx := testutil.DeadlineContext(t)
	seedOpenAgent(t, svc, "agent-1", true)

	// The mock blocks while "starting" so we can observe whether a second cold-start runs
	// concurrently. It does NOT register the agent in the manager, so HasAgent stays false --
	// which is exactly why the lifecycle lock (not the HasAgent re-check alone) is what
	// serializes the two starts here. Both seams: the sweep starts through the background one.
	entered := make(chan struct{})
	release := make(chan struct{})
	start := func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		entered <- struct{}{}
		<-release
		return map[string]string{}, nil
	}
	svc.startAgentFn = start
	svc.startBackgroundAgentFn = start

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_ = svc.ensureAgentRunning("agent-1", resumeIfConversation, interactiveStart)
	}()
	select {
	case <-entered: // the first cold-start is in flight, holding LockAgent
	case <-ctx.Done():
		require.FailNow(t, "the first cold start never reached startAgent")
	}
	require.Equal(t, 1, svc.Agents.LifecycleLockCallersForTest("agent-1"),
		"fixture check: the first cold start holds the lifecycle lock alone")

	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		_ = svc.ensureAgentRunning("agent-1", resumeStoredSession, backgroundStart)
	}()

	// The second cold-start must block on the per-agent lifecycle lock; its startAgent must
	// NOT run concurrently with the first's -- that concurrency is the duplicate-spawn race.
	var regression string
	require.Eventually(t, func() bool {
		select {
		case <-entered:
			regression = "second cold-start entered startAgent concurrently with the first -- the lifecycle lock was not held around the HasAgent check + start, so duplicate subprocesses could spawn"
			return true
		case <-secondDone:
			regression = "the second cold start returned without waiting on the lifecycle lock"
			return true
		default:
		}
		return svc.Agents.LifecycleLockCallersForTest("agent-1") == 2
	}, inputQueueWait, time.Millisecond, "the second cold start never waited on the lifecycle lock")
	require.Empty(t, regression)
	// The second caller is parked on the lock now, so this read is not a race.
	select {
	case <-entered:
		require.FailNow(t, "second cold-start entered startAgent while it waited on the lifecycle lock")
	default:
	}

	// Release both. The second now runs, but only AFTER the first releases the lock.
	close(release)
	select {
	case <-entered: // the second start runs, serialized after the first
	case <-ctx.Done():
		require.FailNow(t, "the second cold start never ran after the first released the lock")
	}
	<-firstDone
	<-secondDone
}

func TestEnsureAgentRunning_RefusesArchivedAgent(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
		ID: "agent-archived", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.Queries.SetAgentWorkspaceArchived(t.Context(), db.SetAgentWorkspaceArchivedParams{
		WorkspaceArchived: true, ID: "agent-archived",
	})
	require.NoError(t, err)
	starts := 0
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		starts++
		return map[string]string{}, nil
	}

	err = svc.ensureAgentRunning("agent-archived", resumeIfConversation, interactiveStart)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "archived workspace")
	assert.Zero(t, starts)
}

// TestEnqueueAgentInput_AutoStartBroadcastsStartingDuringEnsureRunning verifies
// that a queued input triggers ensureAgentRunning on an INACTIVE agent
// (e.g. after a worker/desktop restart that killed the subprocess), the
// auto-start path broadcasts a STARTING AgentStatusChange. Without this, the
// chat startup banner stays hidden during the restart window even though a
// queued input waits behind the cold subprocess.
func TestEnqueueAgentInput_AutoStartBroadcastsStartingDuringEnsureRunning(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	// Mock a successful auto-start so the happy path is exercised without
	// spawning a real subprocess.
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return map[string]string{}, nil
	}

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	registerAgentWatch(svc, w.channelID, "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	dispatch(d, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: "agent-1",
		Text:    "hello",
	}, w)

	require.Empty(t, w.errors)

	sawStarting := false
	var startingMessage string
	require.Eventually(t, func() bool {
		for _, stream := range w.streamsSnapshot() {
			ev := decodeWatchAgentEvent(t, stream)
			sc := ev.GetStatusChange()
			if sc != nil && sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTING {
				sawStarting = true
				startingMessage = sc.GetStartupMessage()
				return true
			}
		}
		return false
	}, inputQueueWait, 10*time.Millisecond)

	assert.True(t, sawStarting,
		"expected STARTING status change while ensureAgentRunning auto-starts the cold subprocess, so the chat startup banner can render beneath the queued user message")
	assert.NotEmpty(t, startingMessage,
		"the STARTING broadcast must carry a phase label so the banner renders something readable (e.g. \"Starting Claude Code…\")")
}

// TestEnqueueAgentInput_AutoStartFailureRevertsToInactive verifies that when
// ensureAgentRunning's startAgent call fails, the broadcast sequence ends in
// INACTIVE — not STARTING (which would leave the banner spinning forever) and
// not STARTUP_FAILED (which would mark the agent permanently unusable; the
// queue keeps the failed input available for retry).
func TestEnqueueAgentInput_AutoStartFailureRevertsToInactive(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return nil, assert.AnError
	}

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	registerAgentWatch(svc, w.channelID, "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	dispatch(d, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: "agent-1",
		Text:    "hello",
	}, w)

	require.Empty(t, w.errors)

	startingIdx := -1
	inactiveIdx := -1
	startupFailedIdx := -1
	require.Eventually(t, func() bool {
		for i, stream := range w.streamsSnapshot() {
			ev := decodeWatchAgentEvent(t, stream)
			sc := ev.GetStatusChange()
			if sc == nil {
				continue
			}
			switch sc.GetStatus() {
			case leapmuxv1.AgentStatus_AGENT_STATUS_STARTING:
				if startingIdx == -1 {
					startingIdx = i
				}
			case leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE:
				if inactiveIdx == -1 {
					inactiveIdx = i
				}
			case leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED:
				if startupFailedIdx == -1 {
					startupFailedIdx = i
				}
			default:
			}
		}
		return startingIdx >= 0 && inactiveIdx >= 0
	}, inputQueueWait, 10*time.Millisecond)

	require.NotEqual(t, -1, startingIdx, "expected a STARTING broadcast at the start of ensureAgentRunning")
	require.NotEqual(t, -1, inactiveIdx, "expected an INACTIVE broadcast after auto-start failure so the startup banner clears")
	assert.Less(t, startingIdx, inactiveIdx, "INACTIVE must follow STARTING so the spinner clears after the failed attempt")
	assert.Equal(t, -1, startupFailedIdx,
		"an auto-start delivery failure must not mark the agent permanently failed")
}

// TestEnsureAgentRunning_BroadcastsActiveWhenTheSinkEmitsNone pins that the
// cold-start path leaves the STARTING it entered.
//
// It is the only one of the three STARTING-entering paths that used to return
// without an ACTIVE of its own, on the theory that the provider's own handshake
// emits one. That is not guaranteed: persistCatalogAndBuildStatus returns
// WITHOUT broadcasting when its row read fails. The resume sweep is the one
// caller with no follow-up traffic to correct the banner, so an already
// connected watcher sits on "Starting" for ever, for an agent that is running
// and accepting input. prepareClearContext states the same reason at its own
// ACTIVE broadcast.
func TestEnsureAgentRunning_BroadcastsActiveWhenTheSinkEmitsNone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, w := setupTestService(t)
	// A start that succeeds and whose sink emits no status at all -- the shape
	// the failing row read produces in production.
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		return map[string]string{}, nil
	}
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	registerAgentWatch(svc, w.channelID, "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	require.NoError(t, svc.ensureAgentRunning("agent-1", resumeIfConversation, interactiveStart))

	starting, active := -1, -1
	for i, stream := range w.streams {
		sc := decodeWatchAgentEvent(t, stream).GetStatusChange()
		if sc == nil {
			continue
		}
		if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTING && starting == -1 {
			starting = i
		}
		if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_ACTIVE && active == -1 {
			active = i
		}
	}
	require.NotEqual(t, -1, starting, "the cold start must report STARTING")
	require.NotEqual(t, -1, active,
		"the cold start reported STARTING and never left it; the banner spins for an agent that is running")
	assert.Less(t, starting, active, "ACTIVE must follow STARTING, not precede it")
}

// TestEnsureAgentRunning_ACloseCancelsAMessageDrivenColdStart is the payoff for
// moving the AgentStartup protocol into ensureAgentRunning.
//
// The three message-driven cold starts used to register nothing, so a CloseAgent
// could not reach one: the tab's teardown ran while the CLI was still in its
// handshake, and the process it produced was one nothing would ever stop. Only
// the boot-time resume sweep had this, because it hand-rolled the protocol at
// its own call site.
func TestEnsureAgentRunning_ACloseCancelsAMessageDrivenColdStart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	entered := make(chan struct{})
	release := make(chan struct{})
	var startCtx context.Context
	svc.startAgentFn = func(c context.Context, _ agent.Options, _ agent.ProviderServices) (map[string]string, error) {
		startCtx = c
		close(entered)
		<-release
		return map[string]string{}, nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.ensureAgentRunning("agent-1", resumeIfConversation, interactiveStart)
	}()
	<-entered

	// The close a user makes while the CLI is still handshaking.
	svc.AgentStartup.cancelAndClear("agent-1", keepWorktreeOnClose)
	assert.ErrorIs(t, startCtx.Err(), context.Canceled,
		"a close during a message-driven cold start must reach it; otherwise the CLI runs for the life of the worker")

	close(release)
	<-done
}

// TestEnsureAgentRunning_ShutdownDrainsAMessageDrivenColdStart pins the other
// half of that registration: Shutdown's WaitForInFlight must wait for a cold
// start the same way it waits for an open. A start it does not count keeps
// writing to a database the caller is about to close.
func TestEnsureAgentRunning_ShutdownDrainsAMessageDrivenColdStart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	entered := make(chan struct{})
	release := make(chan struct{})
	svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
		close(entered)
		<-release
		return map[string]string{}, nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.ensureAgentRunning("agent-1", resumeIfConversation, interactiveStart)
	}()
	<-entered

	// The cold start is blocked in its launch, so its claim must still count
	// toward the drain. The claim is what WaitForInFlight waits for (see
	// countedByDrain), so this proves without a timed window that the drain
	// cannot return yet.
	require.True(t, countedByDrain(&svc.AgentStartup.startupCore, "agent-1"),
		"the drain does not count a cold start that is still in flight; the caller then closes the database under it")

	drained := make(chan struct{})
	go func() { defer close(drained); svc.AgentStartup.WaitForInFlight() }()
	close(release)
	select {
	case <-drained:
	case <-time.After(inputQueueWait):
		t.Fatal("the drain never returned after the cold start finished")
	}
	assert.False(t, countedByDrain(&svc.AgentStartup.startupCore, "agent-1"),
		"the drain returned while the cold start still held its claim")
	<-done
}

// countedByDrain reports whether a startup goroutine for id holds an in-flight
// claim. claim adds that goroutine to the WaitGroup of WaitForInFlight in the
// same critical section that records the claim, and finishEntry removes the
// claim before it releases the WaitGroup. A true answer therefore proves that
// WaitForInFlight cannot return yet. No public signal can prove that: a
// goroutine parked in WaitGroup.Wait looks the same as one that is about to
// return, which is why the drain is read through its claim.
func countedByDrain(core *startupCore, id string) bool {
	core.mu.Lock()
	defer core.mu.Unlock()
	_, held := core.inflight[id]
	return held
}

// TestEnqueueAgentInput_DuringAnOpenStartupIsDelivered is the handler-level form
// of the defect a user reported as a first message that vanished.
//
// The open path holds no manager entry until its final handoff, so HasAgent is
// false for the whole of its startup and a message that lands in that window
// takes the auto-start branch. That branch used to find the tab's id claimed by
// the open's own startup and record "agent is not running" on the row: the CLI
// came up a second later and nothing ever handed it what the user typed. The
// send must join that startup instead, and deliver.
func TestEnqueueAgentInput_DuringAnOpenStartupIsDelivered(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// Quartz controls the timer, so the assertion that the handler waits cannot
	// lose to real time. A regression that refuses again fails immediately
	// rather than after the whole startup budget.
	clock := testutil.NewQuartzMock(t)
	svc, d, w := setupTestService(t, withClock(clock))
	testCtx := testutil.DeadlineContext(t)
	newTimer := clock.Trap().NewTimer(startupAwaitTimerTag)
	defer newTimer.Close()
	stopTimer := clock.Trap().TimerStop(startupAwaitTimerTag)
	defer stopTimer.Close()

	// A start that REGISTERS in the manager, not a stub that returns success and
	// leaves it empty: the delivery this test is about is the SendInput that
	// follows, and it needs a process to reach.
	svc.startAgentFn = startWith(svc.Agents, claudetest.StartEcho)
	t.Cleanup(func() { svc.Agents.StopAgent("agent-1") })
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// Stand in for the open path's in-flight startup.
	openHandle := svc.AgentStartup.begin("agent-1", func() {})
	require.NotNil(t, openHandle)

	sent := make(chan struct{})
	go func() {
		defer close(sent)
		dispatch(d, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
			InputId: newTestAgentInputID(),
			Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
			AgentId: "agent-1",
			Text:    "hello",
		}, w)
	}()

	call := newTimer.MustWait(testCtx)
	assert.Equal(t, svc.agentStartupTimeout(), call.Duration)
	call.MustRelease(testCtx)
	// The open path finishes only after this select, so an enqueue that waited
	// for the startup never returns here. The deadline only ends that case.
	select {
	case <-sent:
	case <-testCtx.Done():
		require.FailNow(t, "enqueue did not return while the tab startup was still running")
	}
	// The open path finishes and hands over its process.
	svc.AgentStartup.succeed("agent-1", openHandle)
	svc.AgentStartup.finishEntry(openHandle)
	stopTimer.MustWait(testCtx).MustRelease(testCtx)

	require.Empty(t, w.errors)
	// The delivery forks a real process, so the deadline is the generous one.
	require.Eventually(t, func() bool {
		rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Limit: 10})
		return err == nil && len(rows) == 1
	}, inputQueueWait, 10*time.Millisecond)

	t.Run("waits past the API timeout", testEnqueueAgentInputWaitsPastTheAPITimeoutForAnOpenStartup)
}

// testEnqueueAgentInputWaitsPastTheAPITimeoutForAnOpenStartup reproduces a slow
// provider handshake. Queue dispatch runs after the enqueue RPC returns, so
// its wait uses the process startup budget. The API budget is not its limit.
func testEnqueueAgentInputWaitsPastTheAPITimeoutForAnOpenStartup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	clock := testutil.NewQuartzMock(t)
	svc, d, w := setupTestService(t, withClock(clock))
	testCtx := testutil.DeadlineContext(t)
	newTimer := clock.Trap().NewTimer(startupAwaitTimerTag)
	defer newTimer.Close()
	stopTimer := clock.Trap().TimerStop(startupAwaitTimerTag)
	defer stopTimer.Close()

	svc.startAgentFn = startWith(svc.Agents, claudetest.StartEcho)
	t.Cleanup(func() { svc.Agents.StopAgent("agent-1") })
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	openHandle := svc.AgentStartup.begin("agent-1", func() {})
	require.NotNil(t, openHandle)

	dispatch(d, "EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
		InputId: newTestAgentInputID(),
		Kind:    leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		AgentId: "agent-1",
		Text:    "hello",
	}, w)
	require.Empty(t, w.errors)

	call := newTimer.MustWait(testCtx)
	call.MustRelease(testCtx)
	advance := clock.Advance(svc.agentAPITimeout())
	advance.MustWait(testCtx)

	// The open path finishes after the API timeout but before its own startup
	// budget. The queued message must still join this process and reach it.
	svc.AgentStartup.succeed("agent-1", openHandle)
	svc.AgentStartup.finishEntry(openHandle)
	stopTimer.MustWait(testCtx).MustRelease(testCtx)

	require.Eventually(t, func() bool {
		rows, err := svc.Queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{AgentID: "agent-1", Limit: 10})
		return err == nil && len(rows) == 1
	}, inputQueueWait, 10*time.Millisecond)
	assert.Equal(t, svc.agentStartupTimeout(), call.Duration,
		"queue dispatch must use the process startup budget")
}

// TestEnsureAgentRunning_RefusesWhenTheStartupItJoinedFailed is the other end of
// TestEnqueueAgentInput_DuringAnOpenStartupIsDelivered: the open startup that the
// caller joins FAILS.
//
// The caller passed the startup check before the failure, because the open path
// was still in flight. failStartup then records STARTUP_FAILED, and that release
// wakes the caller. A caller that read only "the startup settled" started a
// second process for the failed startup. That repeats a launch that the provider
// already refused, and it replaces the failure record that refuses the user's
// next message.
func TestEnsureAgentRunning_RefusesWhenTheStartupItJoinedFailed(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		priority startPriority
	}{
		{"interactive", interactiveStart},
		{"queued", queuedStart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			clock := testutil.NewQuartzMock(t)
			svc, _, _ := setupTestService(t, withClock(clock))
			rec := newStartRecorder()
			rec.install(svc)
			seedOpenAgent(t, svc, "agent-1", true)
			ctx := testutil.DeadlineContext(t)
			joinTimer := clock.Trap().NewTimer(startupAwaitTimerTag)
			defer joinTimer.Close()
			stopTimer := clock.Trap().TimerStop(startupAwaitTimerTag)
			defer stopTimer.Close()

			// Stand in for the open path's in-flight startup.
			openHandle := svc.AgentStartup.begin("agent-1", func() {})
			require.NotNil(t, openHandle)
			row := requireAgentRow(t, svc, "agent-1")

			errCh := make(chan error, 1)
			go func() { errCh <- svc.ensureAgentRunning("agent-1", resumeIfConversation, tc.priority) }()
			joinTimer.MustWait(ctx).MustRelease(ctx)

			// The open path fails through its real failure tail: persist the
			// error, broadcast STARTUP_FAILED, record it in the registry.
			refusal := errors.New(`could not resume session "session-agent-1": refused (send /clear to start a fresh session)`)
			svc.failAgentStartup(&row, gitModeResult{}, refusal, nil, openHandle)
			svc.AgentStartup.finishEntry(openHandle)
			stopTimer.MustWait(ctx).MustRelease(ctx)

			select {
			case err := <-errCh:
				assert.ErrorIs(t, err, errAgentStartupFailed,
					"the startup it joined failed, so there is no process to report")
			case <-ctx.Done():
				require.FailNow(t, "the caller never resumed after the startup it joined failed")
			}
			assert.Empty(t, rec.ids(), "it launched the failed startup again")
			status, startupError, _, tracked := svc.AgentStartup.status("agent-1")
			assert.True(t, tracked, "the failure record must survive the refused cold start")
			assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, status)
			assert.Equal(t, refusal.Error(), startupError)
		})
	}
}

// TestEnsureAgentRunning_RefusesAnAgentWhoseStartupFailed pins the refusal for
// every caller of ensureAgentRunning, and for each of the two records of a
// startup failure.
//
// A cold start must not repeat a startup that failed. Queue dispatch already
// refuses such an agent, but the control request path and the resume sweep
// reach ensureAgentRunning without that check, and queue dispatch reaches it
// after a join that can end in the failure.
func TestEnsureAgentRunning_RefusesAnAgentWhoseStartupFailed(t *testing.T) {
	t.Parallel()

	const failure = "claude: command not found"
	for _, record := range []struct {
		name string
		fail func(t *testing.T, svc *Service)
	}{
		{
			// persistAgentStartupError only logs when its write fails, so the
			// registry can be the one record of the failure.
			name: "failure held in memory",
			fail: func(t *testing.T, svc *Service) {
				handle := svc.AgentStartup.begin("agent-1", func() {})
				require.NotNil(t, handle)
				svc.AgentStartup.fail(handle, failure)
				svc.AgentStartup.finishEntry(handle)
			},
		},
		{
			// A Worker restart clears the registry, so the column is the one
			// record of the failure.
			name: "failure held in the database",
			fail: func(_ *testing.T, svc *Service) { svc.persistAgentStartupError("agent-1", failure) },
		},
	} {
		for _, caller := range []struct {
			name     string
			priority startPriority
		}{
			{"interactive", interactiveStart},
			{"queued", queuedStart},
			{"background", backgroundStart},
		} {
			t.Run(record.name+"/"+caller.name, func(t *testing.T) {
				t.Parallel()

				svc, _, _ := setupTestService(t)
				rec := newStartRecorder()
				rec.install(svc)
				seedOpenAgent(t, svc, "agent-1", true)
				record.fail(t, svc)

				err := svc.ensureAgentRunning("agent-1", resumeIfConversation, caller.priority)
				assert.ErrorIs(t, err, errAgentStartupFailed)
				assert.Empty(t, rec.ids(), "a cold start repeated a startup that failed")
				row := requireAgentRow(t, svc, "agent-1")
				status, startupError, _ := deriveAgentStatus(&row, svc.agentLivenessOf(&row))
				assert.Equal(t, leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED, status,
					"the refused cold start must keep the record of the failure")
				assert.Equal(t, failure, startupError)
			})
		}
	}
}

// TestSendAgentRawMessage_DoesNotStartAnAgentWhoseStartupFailed pins the
// refusal on the control request path. A raw control request to an agent that
// does not run is the one RPC that cold-starts the agent without the queue's
// startup check.
func TestSendAgentRawMessage_DoesNotStartAnAgentWhoseStartupFailed(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)
	svc.persistAgentStartupError("agent-1", "claude: command not found")

	w := newTestWriter()
	dispatch(d, "SendAgentRawMessage", &leapmuxv1.SendAgentRawMessageRequest{
		AgentId: "agent-1",
		Content: `{"type":"control_request","request_id":"test-mcp-status","request":{"subtype":"mcp_status"}}`,
	}, w)
	require.Len(t, w.rejections(), 1, "the error response proves that the handler finished")
	assert.Equal(t, int32(codes.FailedPrecondition), w.rejections()[0].code)
	assert.Equal(t, errAgentStartupFailed.Error(), w.rejections()[0].message)
	assert.Empty(t, rec.ids(), "a control request started an agent whose startup failed")
}

// TestEnsureAgentRunning_AFailedColdStartStaysRetryable pins the limit of the
// refusal above. A cold start that fails records no startup failure, so the
// next message starts the agent again. Only the open path and a context clear
// record a startup failure.
func TestEnsureAgentRunning_AFailedColdStartStaysRetryable(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.failFor["agent-1"] = true
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	require.Error(t, svc.ensureAgentRunning("agent-1", resumeIfConversation, queuedStart))
	_, _, _, tracked := svc.AgentStartup.status("agent-1")
	assert.False(t, tracked, "a failed cold start must leave no STARTUP_FAILED record")
	assert.Empty(t, requireAgentRow(t, svc, "agent-1").StartupError)

	rec.mu.Lock()
	rec.failFor["agent-1"] = false
	rec.mu.Unlock()
	require.NoError(t, svc.ensureAgentRunning("agent-1", resumeIfConversation, queuedStart),
		"the next message must start the agent again after a failed cold start")
	assert.Equal(t, []string{"agent-1", "agent-1"}, rec.ids())
	assert.Equal(t, "session-agent-1", rec.resumeFor("agent-1"), "the retry must resume the stored session")
}
