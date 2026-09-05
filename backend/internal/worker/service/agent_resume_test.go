package service

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/testutil"
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
	// resumeID records the ResumeSessionID each start was handed, keyed by agent
	// id. Without it a sweep that resumes nothing looks identical to one that
	// resumes the right session, and the difference is the user's conversation.
	resumeID map[string]string
	// startCtx records the context each start was handed, keyed by agent id.
	// That context is the agent PROCESS's lifetime, so a test can prove the
	// sweep did not cancel it.
	startCtx map[string]context.Context
	inFlight atomic.Int32
	peak     atomic.Int32
}

func newStartRecorder() *startRecorder {
	return &startRecorder{
		failFor:  map[string]bool{},
		extraEnv: map[string][]string{},
		resumeID: map[string]string{},
		startCtx: map[string]context.Context{},
	}
}

func (r *startRecorder) install(svc *Service) {
	// Both seams: a cold start the user triggered goes through startAgentFn, and
	// the resume sweep goes through startBackgroundAgentFn. Stubbing only one
	// would let a test believe it drove the sweep while it drove the other path.
	start := func(ctx context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
		defer testutil.TrackPeak(&r.inFlight, &r.peak)()
		r.mu.Lock()
		r.started = append(r.started, opts.AgentID)
		r.extraEnv[opts.AgentID] = opts.ExtraEnv
		r.resumeID[opts.AgentID] = opts.ResumeSessionID
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
	svc.startAgentFn = start
	svc.startBackgroundAgentFn = start
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

func (r *startRecorder) resumeFor(agentID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resumeID[agentID]
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

func TestAgentResume_SkipsArchivedAgents(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	recorder := newStartRecorder()
	recorder.install(svc)
	seedOpenAgent(t, svc, "agent-archived", true)
	_, err := svc.Queries.SetAgentWorkspaceArchived(t.Context(), db.SetAgentWorkspaceArchivedParams{
		WorkspaceArchived: 1,
		ID:                "agent-archived",
	})
	require.NoError(t, err)

	runSweep(t, svc)

	assert.Empty(t, recorder.ids())
}

// TestAgentResume_SkipsSubagentTranscripts covers the candidate the BOOT sweep
// cannot produce and the unarchive fan-out can.
//
// The sweep's own query selects roots, so a child never reaches it from there.
// Schedule takes the tab ids the Hub listed for the workspace, and a subagent
// transcript IS a tab in that layout -- so unarchiving a workspace that holds
// one hands this scheduler an agent that owns no process. ensureAgentRunning
// refuses it, which would spend a startup permit and log a failed start per
// subagent tab.
func TestAgentResume_SkipsSubagentTranscripts(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	recorder := newStartRecorder()
	recorder.install(svc)
	seedOpenAgent(t, svc, "agent-root", true)
	require.NoError(t, svc.Queries.CreateChildAgent(t.Context(), db.CreateChildAgentParams{
		ID:            "agent-child",
		ParentAgentID: sql.NullString{String: "agent-root", Valid: true},
		SpawnSpanID:   "span-1",
		Title:         "child task",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	resumer := svc.NewAgentResumer()
	t.Cleanup(resumer.Stop)
	resumer.Schedule(t.Context(), []string{"agent-child", "agent-root"})

	testutil.AssertEventually(t, func() bool { return len(recorder.ids()) == 1 }, "the root resumes")
	assert.Equal(t, []string{"agent-root"}, recorder.ids(),
		"a subagent transcript owns no process, so it must never be a resume candidate")
}

// TestAgentResume_UsesTheBackgroundStartPath pins which manager entry point the
// sweep reaches, which the shared recorder cannot see because it stubs both.
//
// The permit pool applies to background spawns alone. A sweep that took the
// interactive path would restore every tab at once, and the configured cap would
// mean nothing; a message-driven start that took the background path would queue
// behind the sweep and fail the user's send on the client timeout.
func TestAgentResume_UsesTheBackgroundStartPath(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	seedOpenAgent(t, svc, "agent-1", true)

	var interactive, background int
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		interactive++
		return map[string]string{}, nil
	}
	svc.startBackgroundAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		background++
		return map[string]string{}, nil
	}

	runSweep(t, svc)

	assert.Equal(t, 1, background, "the sweep must spawn through the background path, which is the one the permit pool caps")
	assert.Zero(t, interactive, "a resume that took the interactive path would ignore the configured cap entirely")
}

// TestEnsureAgentRunning_UsesTheInteractiveStartPath is the other side: a cold
// start a user is waiting on must never draw on the permit pool.
func TestEnsureAgentRunning_UsesTheInteractiveStartPath(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	seedOpenAgent(t, svc, "agent-1", true)

	var interactive, background int
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		interactive++
		return map[string]string{}, nil
	}
	svc.startBackgroundAgentFn = func(context.Context, agent.Options, agent.OutputSink) (map[string]string, error) {
		background++
		return map[string]string{}, nil
	}

	require.NoError(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart))

	assert.Equal(t, 1, interactive, "a start the user waits on must bypass the permit pool")
	assert.Zero(t, background, "it would otherwise queue behind the boot sweep and fail the send on the client timeout")
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
// identifies no cause.
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
		"the sweep cancelled the resumed agent's context, which kills the process it just started")
}

// TestAgentResume_RootsTheProcessContextOutsideTheSweep pins WHERE that context
// comes from, which the liveness check above cannot see.
//
// The provider builds its exec.CommandContext from this context, so it is the
// process's lifetime. Rooting it at the sweep's own context attaches every
// resumed agent to the worker's root context -- and worker.Run tears down on
// `<-ctx.Done(); Shutdown()`, so the signal would reach the CLI before
// Service.Shutdown latched its state or ran its ordered StopAll. A resumed
// agent would then die less gracefully than one the user opened, and it would
// never get the stdin close and the grace period Manager.StopAll gives every
// other agent.
func TestAgentResume_RootsTheProcessContextOutsideTheSweep(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	sweepCtx, cancelSweep := context.WithCancel(context.Background())
	r := svc.NewAgentResumer()
	r.Start(sweepCtx)
	r.WaitForSweepForTest()
	require.Equal(t, []string{"agent-1"}, rec.ids())

	// Cancelling the sweep's context is what a worker teardown does first.
	cancelSweep()
	assert.NoError(t, rec.ctxFor("agent-1").Err(),
		"the resumed agent's process context is a child of the sweep's; a worker teardown would kill it before Shutdown ran")
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

// TestAgentResume_ResumesTheSessionTheRowPointsAt is the regression guard for a
// conversation the worker forgot on its own.
//
// ensureAgentRunning's own resolveResumeSessionID asks HasUserMessages, which is
// scoped to the CURRENT session: UpdateAgentSessionID stamps session_start_seq
// with the message high-water mark, so it answers false for exactly the row the
// sweep exists to rescue -- an agent whose provider issued a fresh session id
// mid-conversation. Letting it resolve the id therefore resumes NOTHING: the CLI
// comes up blank, its startup handshake reports a new session id, and that write
// replaces the only pointer LeapMux holds to the conversation. The transcript
// survives in the worker database; the route back to the provider's side of it
// does not.
func TestAgentResume_ResumesTheSessionTheRowPointsAt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)

	// The row Claude Code leaves after its first turn: a real conversation, and
	// a session id stamped after the user's message.
	seedOpenAgent(t, svc, "agent-1", false)
	_, err := createMessageRow(ctx, svc.Queries, db.CreateMessageParams{
		ID:            "msg-1",
		AgentID:       "agent-1",
		Source:        leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content:       []byte("hello"),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt:     sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "session-abc",
		ID:             "agent-1",
	}))
	hasMessages, err := svc.Queries.HasUserMessages(ctx, "agent-1")
	require.NoError(t, err)
	require.False(t, hasMessages,
		"fixture check: this row is precisely the one HasUserMessages answers false for")

	runSweep(t, svc)

	require.Equal(t, []string{"agent-1"}, rec.ids())
	assert.Equal(t, "session-abc", rec.resumeFor("agent-1"),
		"the sweep started a BLANK session; the handshake's new session id would overwrite the only pointer to this conversation")
}

// TestAgentResume_KeepsTheRowsSessionIDSoThePickerStillExcludesIt ties the
// resume sweep to the session picker's central rule.
//
// ListAgentSessions excludes a handle that an OPEN row carries, because two
// processes against one session store corrupt it. That exclusion is only as good
// as the row: a sweep that restored the agent into a BLANK session would let the
// startup handshake overwrite agent_session_id, and the handle the user's
// conversation actually lives in would then belong to no open row -- so the
// picker would offer it, and accepting would attach a second process to a store
// the resumed agent is already using.
func TestAgentResume_KeepsTheRowsSessionIDSoThePickerStillExcludesIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	newStartRecorder().install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	before, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	require.NotEmpty(t, before.AgentSessionID)

	runSweep(t, svc)

	after, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, before.AgentSessionID, after.AgentSessionID,
		"the resume moved the row to a different session; the picker would then offer the one the conversation lives in")

	// The picker's own rule, against this row: an open row's handle is excluded.
	summaries := mergeSessionSummaries([]db.ListSessionsForResumeRow{{
		AgentSessionID: after.AgentSessionID,
		Title:          "resumed",
		ClosedAt:       after.ClosedAt,
	}}, nil, 10)
	assert.Empty(t, summaries,
		"a resumed agent's session must stay excluded from the picker while its tab is open")
}

// TestAgentResume_ResumesNothingForAnAgentWithNoSession covers the other side of
// that rule. An agent the sweep reaches on its Resumed flag alone has no session
// id to restore, and must not be handed an empty one as if it did.
func TestAgentResume_ResumesNothingForAnAgentWithNoSession(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)

	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:            "agent-1",
		WorkingDir:    t.TempDir(),
		HomeDir:       t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Resumed:       1,
	}))

	runSweep(t, svc)

	require.Equal(t, []string{"agent-1"}, rec.ids())
	assert.Empty(t, rec.resumeFor("agent-1"))
}

// TestAgentResume_AFailedResumeStaysRetryable pins the choice of succeed() over
// fail() on the error path, which the comment there calls out and nothing else
// checked.
//
// fail() reports STARTUP_FAILED, and SendAgentMessage refuses an agent in that
// state for the whole failed-entry TTL. One CLI that is slow to hand-shake at
// boot would then answer the user's next message with "agent failed to start;
// open a new agent", and every later sweep would pass the agent over as a
// permanent failure.
func TestAgentResume_AFailedResumeStaysRetryable(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.failFor["agent-1"] = true
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	runSweep(t, svc)
	require.Equal(t, []string{"agent-1"}, rec.ids())

	_, _, _, ok := svc.AgentStartup.status("agent-1")
	assert.False(t, ok,
		"a failed resume left a startup override; STARTUP_FAILED refuses the user's next message")

	row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Empty(t, row.StartupError,
		"the sweep must leave the row retryable, the way ensureAgentRunning's own failure path does")
}

// TestAgentResume_ACloseCancelsAnInFlightResume pins the whole reason resumeOne
// registers with AgentStartup.
//
// skipReason only covers a close that lands BEFORE the start. A close that lands
// during the handshake reaches the resume through cancelAndClear, and only
// because begin published this start's cancel. Without that publication the CLI
// keeps running for the life of the worker under a tab no client can see.
func TestAgentResume_ACloseCancelsAnInFlightResume(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.block = make(chan struct{})
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	r := svc.NewAgentResumer()
	r.Start(t.Context())
	require.Eventually(t, func() bool { return rec.inFlight.Load() == 1 }, 5*time.Second, 5*time.Millisecond,
		"the resume never reached its start")

	svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_AGENT, "", "agent-1")

	assert.Eventually(t, func() bool {
		c := rec.ctxFor("agent-1")
		return c != nil && errors.Is(c.Err(), context.Canceled)
	}, 5*time.Second, 5*time.Millisecond,
		"a tab closed during the sweep must cancel its resume; otherwise the CLI runs for the life of the worker")

	close(rec.block)
	r.WaitForSweepForTest()
}

// TestEnsureAgentRunning_ReadsTheRowThroughTheFetchSeam pins that the cold-start
// path and the sweep read the SAME row.
//
// The sweep makes its skip decision through svc.getAgentByID; ensureAgentRunning
// re-reads under the per-agent lifecycle lock and applies the closed-tab
// refusal. A raw query on the second read lets the two disagree about one row --
// and the closed-tab decision is exactly the one that must not diverge.
func TestEnsureAgentRunning_ReadsTheRowThroughTheFetchSeam(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	fetch := svc.getAgentByIDFn
	svc.getAgentByIDFn = func(ctx context.Context, agentID string) (db.Agent, error) {
		row, err := fetch(ctx, agentID)
		if err == nil {
			row.ClosedAt = sqltime.SQLiteNullTimeOf(time.Now())
		}
		return row, err
	}

	require.Error(t, svc.ensureAgentRunning("agent-1", nil, interactiveStart),
		"ensureAgentRunning read the raw query rather than the seam: it gave a process to a row the seam reports closed")
	assert.Empty(t, rec.ids())
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
	svc.Agents.SetStartupConcurrency(1)
	rec := newStartRecorder()
	rec.block = make(chan struct{})
	rec.install(svc)

	seedOpenAgent(t, svc, "agent-a", true)
	seedOpenAgent(t, svc, "agent-b", true)

	r := svc.NewAgentResumer()
	r.Start(t.Context())

	// Wait for the first resume to park inside its start, then prove no second
	// one joins it. peak is monotonic, so a re-read of inFlight would be true by
	// construction here -- and with the fan-out limit removed inFlight jumps
	// straight to 2, the `== 1` predicate is missed, and the test would fail
	// slowly on the poll timeout with a message that names nothing.
	require.Eventually(t, func() bool { return rec.peak.Load() >= 1 }, 5*time.Second, 5*time.Millisecond,
		"the sweep never started its first candidate")
	require.Never(t, func() bool { return rec.peak.Load() > 1 }, 250*time.Millisecond, 5*time.Millisecond,
		"a second resume ran while the first was still in its handshake, under a concurrency of 1")

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
	svc.Agents.SetStartupConcurrency(1)
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
	// Wait for Stop to close the stop channel BEFORE releasing the parked start.
	// Stop closes it under r.mu and only then blocks on the sweep, so this cannot
	// deadlock -- and without the ordering the launcher can drain the remaining
	// candidates (a row read and a stub start apiece) before the Stop goroutine
	// is scheduled, which fails the assertion below as a phantom regression.
	<-r.stop
	// Releasing the parked start lets the launcher return to its loop, where the
	// closed stop channel now ends the sweep.
	close(rec.block)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after the in-flight resume was released")
	}

	assert.LessOrEqual(t, len(rec.ids()), 2,
		"Stop must abandon the candidates the sweep did not reach; it started every one")
}

// TestAgentResume_SweepDoesNotReturnWhileAResumeIsInFlight pins the wait that
// makes Shutdown's own drain safe.
//
// AgentStartup.begin -- the call whose WaitGroup WaitForInFlight blocks on --
// runs INSIDE the goroutine errgroup.Go spawns, and only AFTER that goroutine
// reads the agent row. A sweep that returned while a candidate sat in that
// pre-begin window would let Stop return, and Shutdown's drain would then see a
// zero count, return, and let the caller close the database under a goroutine
// that is about to read it.
//
// The candidates are gated at the ROW READ rather than at the start, because
// that pre-begin window is exactly the hazard.
func TestAgentResume_SweepDoesNotReturnWhileAResumeIsInFlight(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	svc.Agents.SetStartupConcurrency(1)
	rec := newStartRecorder()
	rec.install(svc)

	for _, id := range []string{"agent-a", "agent-b"} {
		seedOpenAgent(t, svc, id, true)
	}

	entered := make(chan string, 2)
	release := make(chan struct{})
	fetch := svc.getAgentByIDFn
	svc.getAgentByIDFn = func(ctx context.Context, agentID string) (db.Agent, error) {
		entered <- agentID
		<-release
		return fetch(ctx, agentID)
	}

	r := svc.NewAgentResumer()
	r.Start(t.Context())
	require.Equal(t, "agent-a", <-entered, "the sweep never reached its first candidate")

	stopped := make(chan struct{})
	go func() { defer close(stopped); r.Stop() }()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a resume sat between the sweep's own check and AgentStartup.begin; Shutdown's drain then sees a zero count")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop never returned after the in-flight resume was released")
	}
	assert.Equal(t, []string{"agent-a"}, rec.ids(),
		"agent-b was handed off after Stop; a resume admitted then registers a cleanup nobody runs")
}

// TestAgentResume_AResumeAdmittedBeforeStopDoesNoWorkAfterIt is the other half
// of that guarantee, and the deterministic one.
//
// errgroup.Go hands a candidate to a goroutine the runtime may not schedule for
// a while, so a candidate can be accepted before Stop and first run after it.
// resumeOne asks the same stop predicate the launcher does, BEFORE its row read,
// so such a straggler reads no database and spawns no process.
func TestAgentResume_AResumeAdmittedBeforeStopDoesNoWorkAfterIt(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	rec := newStartRecorder()
	rec.install(svc)
	seedOpenAgent(t, svc, "agent-1", true)

	r := svc.NewAgentResumer()
	reads := 0
	fetch := svc.getAgentByIDFn
	svc.getAgentByIDFn = func(ctx context.Context, agentID string) (db.Agent, error) {
		reads++
		return fetch(ctx, agentID)
	}

	// Exactly the state a straggler wakes into: the sweep is abandoned.
	close(r.stop)
	r.resumeOne(context.Background(), "agent-1")

	assert.Zero(t, reads, "an abandoned resume read the database; the caller is closing it")
	assert.Empty(t, rec.ids(), "an abandoned resume spawned a process nothing will stop")
}

// TestAgentResume_ACancelledSweepReportsItsOutcome pins that BOTH abandoned
// exits leave a record.
//
// "Why did my agent not come back" is the only question anybody asks of this
// feature, and a sweep that returns silently answers it with nothing: the
// operator sees the opening "restoring agent processes" line, no outcome, and
// reads a cancelled sweep as a wedged one. The context arm is the one the
// desktop and embedded entry points take, because worker.Run tears down on
// `<-ctx.Done()` before it calls Shutdown.
func TestAgentResume_ACancelledSweepReportsItsOutcome(t *testing.T) {
	// Not parallel: it captures the default logger, which is process-global.
	logs := testutil.CaptureDefaultLogger(t)

	svc, _, _ := setupTestService(t)
	// Two slots and four candidates: the first two park in their row read, the
	// launcher parks handing off the third, and once the gate opens it reaches
	// the fourth candidate's check with the context already cancelled. That is
	// the only exit that takes the context arm.
	svc.Agents.SetStartupConcurrency(2)
	rec := newStartRecorder()
	rec.install(svc)
	for _, id := range []string{"agent-a", "agent-b", "agent-c", "agent-d"} {
		seedOpenAgent(t, svc, id, true)
	}

	entered := make(chan string, 4)
	release := make(chan struct{})
	fetch := svc.getAgentByIDFn
	svc.getAgentByIDFn = func(ctx context.Context, agentID string) (db.Agent, error) {
		entered <- agentID
		<-release
		return fetch(ctx, agentID)
	}

	sweepCtx, cancelSweep := context.WithCancel(context.Background())
	r := svc.NewAgentResumer()
	r.Start(sweepCtx)
	// Both slots are taken. Which two candidates got them does not matter; that
	// the launcher is parked handing off the third does.
	<-entered
	<-entered

	cancelSweep()
	close(release)
	r.WaitForSweepForTest()

	assert.Contains(t, logs.String(), "agent resume: context cancelled before the sweep reached every candidate",
		"a cancelled sweep returned silently; the operator sees a start line and no outcome")
	assert.Contains(t, logs.String(), "agent resume: sweep finished",
		"every exit must log the tally, so a partial sweep is legible")
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
