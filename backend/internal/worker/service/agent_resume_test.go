package service

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// startRecorder captures which agents the resume sweep tried to start, and lets
// a test block inside a start to observe overlap.
type startRecorder struct {
	mu      sync.Mutex
	started []string
	// block, when non-nil, is received from inside every start.
	block chan struct{}
	// failFor names agents whose start returns an error.
	failFor map[string]bool
	// extraEnv records the ExtraEnv each start was handed, keyed by agent id.
	extraEnv map[string][]string
	// startCtx records the context each start was handed, keyed by agent id.
	// That context is the agent PROCESS's lifetime, so a test can prove the
	// sweep did not cancel it.
	startCtx map[string]context.Context
	inFlight atomic.Int32
	peak     atomic.Int32
}

func newStartRecorder() *startRecorder {
	return &startRecorder{failFor: map[string]bool{}, extraEnv: map[string][]string{}, startCtx: map[string]context.Context{}}
}

func (r *startRecorder) install(svc *Service) {
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
		now := r.inFlight.Add(1)
		defer r.inFlight.Add(-1)
		for {
			peak := r.peak.Load()
			if now <= peak || r.peak.CompareAndSwap(peak, now) {
				break
			}
		}
		r.mu.Lock()
		r.started = append(r.started, opts.AgentID)
		r.extraEnv[opts.AgentID] = opts.ExtraEnv
		r.startCtx[opts.AgentID] = ctx
		fail := r.failFor[opts.AgentID]
		block := r.block
		r.mu.Unlock()
		if block != nil {
			<-block
		}
		if fail {
			return nil, assert.AnError
		}
		return map[string]string{}, nil
	}
}

func (r *startRecorder) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.started...)
}

func (r *startRecorder) envFor(agentID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.extraEnv[agentID]
}

func (r *startRecorder) ctxFor(agentID string) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startCtx[agentID]
}

// seedOpenAgent inserts an open root agent row. used=true gives it a session id
// and marks it resumed -- the two independent signals that a process ran for
// this agent, either of which makes the sweep respawn it. Distinct from
// access_control_test.go's seedAgent, which seeds the minimum an access gate
// needs and says nothing about provider or use.
func seedOpenAgent(t *testing.T, svc *Service, agentID string, used bool) {
	t.Helper()
	resumed := int64(0)
	sessionID := ""
	if used {
		resumed = 1
		sessionID = "session-" + agentID
	}
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:            agentID,
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Resumed:       resumed,
	}))
	if sessionID != "" {
		require.NoError(t, svc.Queries.UpdateAgentSessionID(context.Background(), db.UpdateAgentSessionIDParams{
			AgentSessionID: sessionID,
			ID:             agentID,
		}))
	}
}

// runSweep starts the resumer and waits for its one sweep to finish.
func runSweep(t *testing.T, svc *Service) *AgentResumer {
	t.Helper()
	r := svc.NewAgentResumer()
	r.Start(t.Context())
	r.WaitForSweepForTest()
	return r
}

// TestAgentResume_RestoresUsedAgents is the core behaviour: after a worker
// restart, an agent the user has actually talked to comes back by itself, with
// no message sent. Without it a provider that lists the agent sessions on this
// machine cannot see -- or reach -- its siblings until a human types into each
// one.
func TestAgentResume_RestoresUsedAgents(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)

	seedOpenAgent(t, svc, "agent-resumed", true)
	runSweep(t, svc)

	assert.Equal(t, []string{"agent-resumed"}, rec.ids())
}

// TestAgentResume_RestoresAnAgentWithASessionButNoResumeFlag covers the common
// case, and the reason the predicate is what it is: an agent opened fresh
// (resumed = 0) whose provider assigned it a session id during startup. Only
// checking the resumed flag would leave every normally-opened agent cold.
//
// This is also the regression guard for the predicate this replaced.
// HasUserMessages reads as "the user has used this agent" and is not: it is
// scoped to the CURRENT session, because UpdateAgentSessionID stamps
// session_start_seq with the message high-water mark. The row below is exactly
// what that leaves behind after Claude Code issues a fresh session id on its
// first turn -- a real conversation, and no user message past
// session_start_seq -- and the sweep must still resume it.
func TestAgentResume_RestoresAnAgentWithASessionButNoResumeFlag(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)

	seedOpenAgent(t, svc, "agent-messaged", false)
	_, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "msg-1",
		AgentID:       "agent-messaged",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte("hello"),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:     sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	// The provider reports its session id after that first turn, which is what
	// moves session_start_seq past the user's message.
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "session-abc",
		ID:             "agent-messaged",
	}))
	hasMessages, err := svc.Queries.HasUserMessages(ctx, "agent-messaged")
	require.NoError(t, err)
	require.False(t, hasMessages,
		"fixture check: this row is precisely the one HasUserMessages answers false for")

	runSweep(t, svc)
	assert.Equal(t, []string{"agent-messaged"}, rec.ids())
}

// TestAgentResume_LeavesTheResumedProcessContextLive is the regression guard for
// an agent that came up and died half a second later with exit status 143.
//
// The context handed to startAgent is the agent PROCESS's lifetime -- the
// provider builds its exec.CommandContext from it -- not merely its startup's.
// A `defer cancel()` around the resume therefore SIGTERMs every agent the sweep
// just restored, and the only trace is an "agent exited with error" line that
// names no cause.
func TestAgentResume_LeavesTheResumedProcessContextLive(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	runSweep(t, svc)

	ctx := rec.ctxFor("agent-1")
	require.NotNil(t, ctx)
	assert.NoError(t, ctx.Err(),
		"the sweep cancelled the resumed agent's context; the process it just started is being killed")
}

// TestAgentResume_ReleasesTheContextOfAFailedStart is the other half: when no
// process came up there is nothing to keep alive, so the context must not be
// stranded until the worker exits.
func TestAgentResume_ReleasesTheContextOfAFailedStart(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.failFor["agent-1"] = true
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	runSweep(t, svc)

	ctx := rec.ctxFor("agent-1")
	require.NotNil(t, ctx)
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

// TestAgentResume_SkipsAgentsThatNeverStarted pins the filter that keeps the
// sweep from spawning a CLI per empty tab. An agent with no session id and no
// resume flag never had a process, so there is nothing to restore and starting
// it would cost memory and answer nobody.
func TestAgentResume_SkipsAgentsThatNeverStarted(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)

	seedOpenAgent(t, svc, "agent-untouched", false)
	runSweep(t, svc)

	assert.Empty(t, rec.ids(), "an agent with no session id and no resume flag must stay cold")
}

// TestAgentResume_SkipsClosedAgents pins that a closed tab is not respawned.
// ListAllOpenRootAgentIDs is what enforces it; asserting here keeps a later
// switch to a wider query from silently resurrecting closed tabs.
func TestAgentResume_SkipsClosedAgents(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)

	seedOpenAgent(t, svc, "agent-closed", true)
	_, err := svc.Queries.CloseAgent(context.Background(), "agent-closed")
	require.NoError(t, err)

	runSweep(t, svc)
	assert.Empty(t, rec.ids())
}

// TestAgentResume_SkipsATabClosedSinceTheListing pins the snapshot race. The
// candidate list is read once; a candidate at the back of a throttled queue is
// reached much later, and a CloseAgent in between already ran that tab's whole
// teardown. A process started for it then is one nothing will ever stop -- it
// holds a CLI for the life of the worker under a tab no client can see.
//
// The row is closed through the fetch seam rather than by racing a real close,
// because the window under test is exactly "the list said open, the row says
// closed".
func TestAgentResume_SkipsATabClosedSinceTheListing(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)

	seedOpenAgent(t, svc, "agent-live", true)
	seedOpenAgent(t, svc, "agent-closing", true)

	fetch := svc.getAgentByIDFn
	svc.getAgentByIDFn = func(ctx context.Context, agentID string) (db.Agent, error) {
		row, err := fetch(ctx, agentID)
		if err == nil && agentID == "agent-closing" {
			row.ClosedAt = sqltime.SQLiteNullTimeOf(time.Now())
		}
		return row, err
	}

	runSweep(t, svc)
	assert.Equal(t, []string{"agent-live"}, rec.ids(),
		"a tab closed after the listing must not be given a process nothing will stop")
}

// TestAgentResume_SkipsChildTranscripts pins that a subagent transcript is
// never spawned: it has no process of its own, it is fed by its parent's.
// ensureAgentRunning refuses one outright, so a sweep that offered it up would
// log a failure per child on every boot.
func TestAgentResume_SkipsChildTranscripts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)

	seedOpenAgent(t, svc, "agent-root", true)
	require.NoError(t, svc.Queries.CreateChildAgent(ctx, db.CreateChildAgentParams{
		ID:            "agent-child",
		ParentAgentID: sql.NullString{String: "agent-root", Valid: true},
		SpawnSpanID:   "span-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Title:         "child",
	}))

	runSweep(t, svc)
	assert.Equal(t, []string{"agent-root"}, rec.ids(), "only the root owns a process")
}

// TestAgentResume_SkipsAgentsThatFailedToStart pins the same permanent-failure
// gate SendAgentMessage applies. Respawning one burns a startup slot on a CLI
// that is going to fail again, and the row already tells the user to open a new
// agent.
func TestAgentResume_SkipsAgentsThatFailedToStart(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)

	seedOpenAgent(t, svc, "agent-broken", true)
	require.NoError(t, svc.Queries.SetAgentStartupError(context.Background(), db.SetAgentStartupErrorParams{
		StartupError: "claude: command not found",
		ID:           "agent-broken",
	}))

	runSweep(t, svc)
	assert.Empty(t, rec.ids())
}

// TestAgentResume_OneFailureDoesNotAbandonTheRest pins that the sweep is not an
// errgroup that cancels its siblings: one agent whose CLI is missing must not
// leave every other agent on the machine cold.
func TestAgentResume_OneFailureDoesNotAbandonTheRest(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.failFor["agent-a"] = true
	rec.install(svc)

	seedOpenAgent(t, svc, "agent-a", true)
	seedOpenAgent(t, svc, "agent-b", true)

	runSweep(t, svc)
	assert.ElementsMatch(t, []string{"agent-a", "agent-b"}, rec.ids())
}

// TestAgentResume_WaitsForAWorkerOwner pins the precondition, and pins that a
// refusal does NOT consume the one-shot latch. A resumed agent needs a control
// socket, and remintAgentControlIPC cannot mint one for an unnamed user -- so
// the sweep must wait for the Hub's WorkerIdentity and retry on a later
// converged pass instead of losing its only chance.
func TestAgentResume_WaitsForAWorkerOwner(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	// setupTestService seeds an owner; clear it to model a worker whose Hub has
	// not delivered WorkerIdentity yet.
	svc.registeredBy.Store(&userid.UserID{})
	r := svc.NewAgentResumer()
	r.Start(t.Context())
	r.WaitForSweepForTest()
	require.Empty(t, rec.ids(), "no owner means no control socket, so no resume")

	owner, ok := userid.New("user-1")
	require.True(t, ok)
	svc.SetRegisteredBy(owner)
	r.Start(t.Context())
	r.WaitForSweepForTest()
	assert.Equal(t, []string{"agent-1"}, rec.ids(),
		"the refusal must not latch -- a later converged pass is the retry")
}

// TestAgentResume_RefusesWhileShuttingDown pins that a sweep triggered as the
// worker tears down starts nothing. Shutdown stops the loops before its drains,
// and a spawn admitted after that point registers a remote-IPC cleanup nobody
// runs and writes to a database about to close.
func TestAgentResume_RefusesWhileShuttingDown(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	svc.shuttingDown.Store(true)
	runSweep(t, svc)
	assert.Empty(t, rec.ids())
}

// TestAgentResume_SweepsOnlyOnce pins the latch. The reconciler reports a
// converged pass hourly for the life of the worker, so an unlatched Start would
// re-run the whole sweep every hour.
func TestAgentResume_SweepsOnlyOnce(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	r := svc.NewAgentResumer()
	r.Start(t.Context())
	r.Start(t.Context())
	r.WaitForSweepForTest()
	r.Start(t.Context())
	r.WaitForSweepForTest()

	assert.Equal(t, []string{"agent-1"}, rec.ids())
}

// TestAgentResume_LimitsItsOwnFanOut pins that the sweep respects the
// configured concurrency. The manager's permit pool already caps startups; the
// sweep's own limit is what keeps the QUEUE in front of that pool shallow, so an
// interactive open waits behind one generation of startups rather than behind
// every open tab on the machine.
func TestAgentResume_LimitsItsOwnFanOut(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	svc.AgentStartupConcurrency = 1
	rec := newStartRecorder()
	rec.block = make(chan struct{})
	rec.install(svc)

	seedOpenAgent(t, svc, "agent-a", true)
	seedOpenAgent(t, svc, "agent-b", true)

	r := svc.NewAgentResumer()
	r.Start(t.Context())

	// Only one start may be in flight. Sample while the first is parked.
	require.Eventually(t, func() bool { return rec.inFlight.Load() == 1 }, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, int32(1), rec.inFlight.Load(),
		"a second resume ran while the first was still handshaking, under a concurrency of 1")

	close(rec.block)
	r.WaitForSweepForTest()
	assert.Equal(t, int32(1), rec.peak.Load())
	assert.Len(t, rec.ids(), 2, "the limit delays a resume, it never drops one")
}

// TestAgentResume_EmptyDatabaseStartsNothing covers the boundary a fresh worker
// hits on every boot.
func TestAgentResume_EmptyDatabaseStartsNothing(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)

	runSweep(t, svc)
	assert.Empty(t, rec.ids())
}

// TestAgentResume_SkipsAnAgentAlreadyRunning pins the guard against a duplicate
// spawn: a message that lands during the sweep starts the agent through
// ensureAgentRunning, and the sweep must observe that rather than repeat it.
func TestAgentResume_SkipsAnAgentAlreadyRunning(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	// Register a live process for the agent through the manager's own start
	// path, so HasAgent answers true exactly as it would in production.
	_, err := svc.Agents.MockStartAgent(t.Context(), agent.Options{
		AgentID:       "agent-1",
		WorkingDir:    t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, svc.Output.NewSink("agent-1", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent("agent-1") })
	require.True(t, svc.Agents.HasAgent("agent-1"))

	runSweep(t, svc)
	assert.Empty(t, rec.ids())
}

// TestAgentResume_StopAbandonsTheRemainingCandidates pins what Stop is for.
// Shutdown calls it before its drains, so a sweep working through a long
// candidate list must stop handing out new startups rather than keep spawning
// CLIs into a worker that is closing its database.
func TestAgentResume_StopAbandonsTheRemainingCandidates(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	svc.AgentStartupConcurrency = 1
	rec := newStartRecorder()
	rec.block = make(chan struct{})
	rec.install(svc)

	for _, id := range []string{"agent-a", "agent-b", "agent-c", "agent-d"} {
		seedOpenAgent(t, svc, id, true)
	}

	r := svc.NewAgentResumer()
	r.Start(t.Context())
	// Wait until the first resume is parked inside its start, so the launcher is
	// definitely past the loop's stop check for that candidate and blocked on the
	// fan-out limit for the next.
	require.Eventually(t, func() bool { return rec.inFlight.Load() == 1 }, 5*time.Second, 5*time.Millisecond)

	stopped := make(chan struct{})
	go func() { defer close(stopped); r.Stop() }()
	// Releasing the parked start lets the launcher return to its loop, where the
	// closed stop channel now ends the sweep.
	close(rec.block)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the in-flight resume was released")
	}

	assert.Less(t, len(rec.ids()), 4,
		"Stop must abandon the candidates the sweep has not reached; it started every one")
}

// TestAgentResume_StartAfterStopLaunchesNoSweep pins the race Shutdown creates.
// The composed stopper stops the resumer BEFORE the reconciler, so the
// reconciler goroutine can still report a converged pass -- and call Start --
// after Stop returned.
//
// It asserts on the GOROUTINE, not on the agents. A sweep launched here starts
// nothing, because the closed stop channel ends its loop at the first
// candidate; the damage is that it lists the open agents against a database the
// caller is about to close, with nothing left to wait for it. Asserting "no
// agent started" would therefore pass against the very bug it names.
func TestAgentResume_StartAfterStopLaunchesNoSweep(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	r := svc.NewAgentResumer()
	r.Stop()
	r.Start(t.Context())

	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	assert.Nil(t, done,
		"Start launched a sweep goroutine after Stop returned; nothing joins it, and it reads a closing database")
	assert.Empty(t, rec.ids())
}

// TestAgentResume_StopBeforeTheSweepIsRunNeverBlocks pins that Stop is safe on a
// resumer that never started. Shutdown always calls it, and a worker whose Hub
// never converged has no sweep goroutine to wait for.
func TestAgentResume_StopBeforeTheSweepIsRunNeverBlocks(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	r := svc.NewAgentResumer()
	done := make(chan struct{})
	go func() { defer close(done); r.Stop(); r.Stop() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop blocked on a resumer that never swept")
	}
}
