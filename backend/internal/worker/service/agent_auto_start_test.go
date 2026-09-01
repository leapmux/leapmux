package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// TestEnsureAgentRunning_SerializesConcurrentColdStarts is the regression guard for [C11]:
// the auto-start path's HasAgent check and startAgent call must run under the per-agent
// lifecycle lock (LockAgent), so two concurrent sends to a cold agent can't both pass the
// check and spawn duplicate subprocesses (the second overwriting and orphaning the first in
// the manager's agent map). Without the lock, both ensureAgentRunning calls enter startAgent
// concurrently; with it, the second blocks until the first finishes.
func TestEnsureAgentRunning_SerializesConcurrentColdStarts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// The mock blocks while "starting" so we can observe whether a second cold-start runs
	// concurrently. It does NOT register the agent in the manager, so HasAgent stays false --
	// which is exactly why the lifecycle lock (not the HasAgent re-check alone) is what
	// serializes the two starts here.
	entered := make(chan struct{})
	release := make(chan struct{})
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		entered <- struct{}{}
		<-release
		return map[string]string{}, nil
	}

	firstDone := make(chan struct{})
	go func() { defer close(firstDone); _ = svc.ensureAgentRunning("agent-1", nil, interactiveStart) }()
	<-entered // the first cold-start is in flight, holding LockAgent

	secondDone := make(chan struct{})
	go func() { defer close(secondDone); _ = svc.ensureAgentRunning("agent-1", nil, interactiveStart) }()

	// The second cold-start must block on the per-agent lifecycle lock; its startAgent must
	// NOT run concurrently with the first's -- that concurrency is the duplicate-spawn race.
	select {
	case <-entered:
		t.Fatal("second cold-start entered startAgent concurrently with the first -- the lifecycle lock was not held around the HasAgent check + start, so duplicate subprocesses could spawn")
	case <-time.After(100 * time.Millisecond):
		// Expected: the second start is blocked on LockAgent.
	}

	// Release both. The second now runs, but only AFTER the first releases the lock.
	close(release)
	<-entered // the second start runs, serialized after the first
	<-firstDone
	<-secondDone
}

// TestSendAgentMessage_AutoStartBroadcastsStartingDuringEnsureRunning verifies
// that when SendAgentMessage triggers ensureAgentRunning on an INACTIVE agent
// (e.g. after a worker/desktop restart that killed the subprocess), the
// auto-start path broadcasts a STARTING AgentStatusChange. Without this, the
// chat startup banner stays hidden during the restart window even though a
// just-typed message is queued behind the still-cold subprocess — the bubble
// pulses but no progress affordance is shown beneath it.
func TestSendAgentMessage_AutoStartBroadcastsStartingDuringEnsureRunning(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	// Mock a successful auto-start so the happy path is exercised without
	// spawning a real subprocess.
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		return map[string]string{}, nil
	}

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	registerAgentWatch(svc, w.channelID, "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "agent-1",
		Content: "hello",
	}, w)

	require.Empty(t, w.errors)

	sawStarting := false
	var startingMessage string
	for _, stream := range w.streams {
		ev := decodeWatchAgentEvent(t, stream)
		sc := ev.GetStatusChange()
		if sc == nil {
			continue
		}
		if sc.GetStatus() == leapmuxv1.AgentStatus_AGENT_STATUS_STARTING {
			sawStarting = true
			startingMessage = sc.GetStartupMessage()
			break
		}
	}

	assert.True(t, sawStarting,
		"expected STARTING status change while ensureAgentRunning auto-starts the cold subprocess, so the chat startup banner can render beneath the queued user message")
	assert.NotEmpty(t, startingMessage,
		"the STARTING broadcast must carry a phase label so the banner renders something readable (e.g. \"Starting Claude Code…\")")
}

// TestSendAgentMessage_AutoStartFailureRevertsToInactive verifies that when
// ensureAgentRunning's startAgent call fails, the broadcast sequence ends in
// INACTIVE — not STARTING (which would leave the banner spinning forever) and
// not STARTUP_FAILED (which would mark the agent permanently unusable; the
// existing design intentionally keeps it retryable on the next send by
// surfacing the failure as a per-message delivery_error instead).
func TestSendAgentMessage_AutoStartFailureRevertsToInactive(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, d, w := setupTestService(t)

	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		return nil, assert.AnError
	}

	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	registerAgentWatch(svc, w.channelID, "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	dispatch(d, "SendAgentMessage", &leapmuxv1.SendAgentMessageRequest{
		AgentId: "agent-1",
		Content: "hello",
	}, w)

	require.Empty(t, w.errors)

	startingIdx := -1
	inactiveIdx := -1
	startupFailedIdx := -1
	for i, stream := range w.streams {
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
			// This test only orders STARTING / INACTIVE / STARTUP_FAILED.
		}
	}

	require.NotEqual(t, -1, startingIdx, "expected a STARTING broadcast at the start of ensureAgentRunning")
	require.NotEqual(t, -1, inactiveIdx, "expected an INACTIVE broadcast after auto-start failure so the startup banner clears")
	assert.Less(t, startingIdx, inactiveIdx, "INACTIVE must follow STARTING so the spinner clears after the failed attempt")
	assert.Equal(t, -1, startupFailedIdx,
		"auto-start failure on the SendAgentMessage path must NOT mark the agent STARTUP_FAILED — the message gets a delivery_error and the agent stays retryable on the next send")
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
// and accepting input. handleClearContext states the same reason at its own
// ACTIVE broadcast.
func TestEnsureAgentRunning_BroadcastsActiveWhenTheSinkEmitsNone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, w := setupTestService(t)
	// A start that succeeds and whose sink emits no status at all -- the shape
	// the failing row read produces in production.
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		return map[string]string{}, nil
	}
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	registerAgentWatch(svc, w.channelID, "agent-1", leapmuxv1.WatchMode_WATCH_MODE_FULL, w)

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

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
	svc.startAgentFn = func(c context.Context, _ agent.Options, _ agent.OutputSink) (map[string]string, error) {
		startCtx = c
		close(entered)
		<-release
		return map[string]string{}, nil
	}

	done := make(chan struct{})
	go func() { defer close(done); _ = svc.ensureAgentRunning("agent-1", nil, interactiveStart) }()
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
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		close(entered)
		<-release
		return map[string]string{}, nil
	}

	done := make(chan struct{})
	go func() { defer close(done); _ = svc.ensureAgentRunning("agent-1", nil, interactiveStart) }()
	<-entered

	drained := make(chan struct{})
	go func() { defer close(drained); svc.AgentStartup.WaitForInFlight() }()
	select {
	case <-drained:
		t.Fatal("the drain returned while a cold start was still in flight; the caller then closes the database under it")
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain never returned after the cold start finished")
	}
	<-done
}
