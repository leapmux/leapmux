package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/internal/worker/db/dbtest"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/claude/claudetest"
)

// This file covers the provider-neutral input recovery in
// agent_input_recovery.go: a provider that reports its process unable to
// accept input gets exactly one replacement that resumes the stored native
// session, and the input reaches the replacement or stays queued.

// recoveryAgent is a fake provider whose preparation is scripted per call. Its
// Wait blocks until Stop, so the manager registers it like a live process.
type recoveryAgent struct {
	agenttest.IdleAgent
	agentID string
	stopped chan struct{}

	mu            sync.Mutex
	prepareScript func(call int) error
	prepareCalls  int
	sentTexts     []string
	sentSessions  []string
}

func newRecoveryAgent(agentID string, prepare func(call int) error) *recoveryAgent {
	return &recoveryAgent{
		agentID:       agentID,
		stopped:       make(chan struct{}),
		prepareScript: prepare,
	}
}

func (a *recoveryAgent) AgentID() string { return a.agentID }

func (a *recoveryAgent) PrepareInputProcess() error {
	a.mu.Lock()
	a.prepareCalls++
	call := a.prepareCalls
	script := a.prepareScript
	a.mu.Unlock()
	if script == nil {
		return nil
	}
	return script(call)
}

func (a *recoveryAgent) SendInput(content string, _ []*leapmuxv1.Attachment) error {
	a.mu.Lock()
	a.sentTexts = append(a.sentTexts, content)
	a.mu.Unlock()
	return nil
}

func (a *recoveryAgent) SendInputForSession(sessionID, content string, _ []*leapmuxv1.Attachment) error {
	a.mu.Lock()
	a.sentTexts = append(a.sentTexts, content)
	a.sentSessions = append(a.sentSessions, sessionID)
	a.mu.Unlock()
	return nil
}

func (a *recoveryAgent) Stop() {
	select {
	case <-a.stopped:
	default:
		close(a.stopped)
	}
}

func (a *recoveryAgent) IsStopped() bool {
	select {
	case <-a.stopped:
		return true
	default:
		return false
	}
}

func (a *recoveryAgent) Wait() error {
	<-a.stopped
	return nil
}

func (a *recoveryAgent) wasStopped() bool { return a.IsStopped() }

func (a *recoveryAgent) sent() ([]string, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.sentTexts...), append([]string(nil), a.sentSessions...)
}

func (a *recoveryAgent) preparations() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prepareCalls
}

// recoveryStarts records every launch and hands out the fake providers. It is
// the service-seam stand-in for a real provider process, so the manager, the
// lifecycle, and the sink wiring all run for real.
type recoveryStarts struct {
	mu       sync.Mutex
	resumeID []string
	agents   []*recoveryAgent
	// sinks holds the root sink each launch received, so a test publishes
	// through the sink the OUTPUT handler adopted as its turn publisher.
	sinks []agent.ProviderServices
	// prepareFor selects each new fake's preparation script by launch number.
	prepareFor func(launch int) func(call int) error
}

func (s *recoveryStarts) start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	s.mu.Lock()
	launch := len(s.agents) + 1
	resume := opts.ResumeSessionID
	script := s.prepareFor(launch)
	s.mu.Unlock()
	fake := newRecoveryAgent(opts.AgentID, script)
	s.mu.Lock()
	s.resumeID = append(s.resumeID, resume)
	s.agents = append(s.agents, fake)
	s.sinks = append(s.sinks, sink)
	s.mu.Unlock()
	return fake, nil
}

func (s *recoveryStarts) launches() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.resumeID...)
}

func (s *recoveryStarts) fake(index int) *recoveryAgent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index >= len(s.agents) {
		return nil
	}
	return s.agents[index]
}

func (s *recoveryStarts) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.agents)
}

// sinkOf returns the root sink that launch number (1-based) received.
func (s *recoveryStarts) sinkOf(launch int) agent.ProviderServices {
	s.mu.Lock()
	defer s.mu.Unlock()
	if launch < 1 || launch > len(s.sinks) {
		return nil
	}
	return s.sinks[launch-1]
}

// newInputRecoveryService wires the fake into every cold start this service
// makes, including the replacements that input recovery launches.
func newInputRecoveryService(t *testing.T, prepareFor func(launch int) func(call int) error) (*Service, *recoveryStarts) {
	t.Helper()
	svc, _, _ := setupTestService(t)
	if prepareFor == nil {
		prepareFor = func(int) func(int) error { return nil }
	}
	starts := &recoveryStarts{prepareFor: prepareFor}
	wrap := func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		return svc.Agents.StartAgentWith(ctx, opts, sink, starts.start)
	}
	svc.startAgentFn = wrap
	svc.startBackgroundAgentFn = wrap
	return svc, starts
}

// seedRecoveryAgent creates an open root agent row with a stored native
// session, no accepted user messages, and resumed = 0: the state whose next
// ordinary cold start would NOT resume the session.
func seedRecoveryAgent(t *testing.T, svc *Service, agentID, sessionID string) db.Agent {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		ID: agentID, AgentSessionID: sessionID,
	}))
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	require.Zero(t, row.Resumed, "the seed starts without durable resume intent")
	return row
}

func enqueueRecoveryInput(t *testing.T, svc *Service, agentID, inputID string) {
	t.Helper()
	_, err := svc.InputQueue.Enqueue(context.Background(), inputqueue.NewItem{
		ID: inputID, AgentID: agentID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "continue",
	})
	require.NoError(t, err)
}

// recoveryFailedItem returns the retained failed item, once it exists.
func recoveryFailedItem(t *testing.T, svc *Service, agentID, inputID string) inputqueue.SnapshotItem {
	t.Helper()
	var stored inputqueue.SnapshotItem
	require.Eventually(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(context.Background(), agentID)
		if err != nil {
			return false
		}
		for _, item := range snapshot.Items {
			if item.ID == inputID && item.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED {
				stored = item
				return true
			}
		}
		return false
	}, inputQueueWait, 10*time.Millisecond)
	return stored
}

// TestInputRecoveryReplacesStaleProcessAndDeliversOnce is the core scenario:
// a process that reports restart-required is stopped, its stored thread is
// resumed by the replacement (the resume intent persisted before the stop --
// the seed holds resumed = 0 and no user messages, so an ordinary start would
// not resume), the replacement is prepared, and the queued input is delivered
// exactly once, never to the stale process.
func TestInputRecoveryReplacesStaleProcessAndDeliversOnce(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
		if launch == 1 {
			return func(int) error {
				return fmtRestartRequired()
			}
		}
		return nil
	})
	const agentID = "agent-1"
	seedRecoveryAgent(t, svc, agentID, "thread-1")
	enqueueRecoveryInput(t, svc, agentID, "input-1")

	waitForMessageCount(t, svc, agentID, 1)

	require.Equal(t, []string{"", "thread-1"}, starts.launches(),
		"the cold start resumes nothing; the replacement resumes the stored thread")
	stale := starts.fake(0)
	require.NotNil(t, stale)
	assert.True(t, stale.wasStopped(), "the stale process is stopped")
	texts, _ := stale.sent()
	assert.Empty(t, texts, "no input reached the stale process")
	replacement := starts.fake(1)
	require.NotNil(t, replacement)
	assert.Equal(t, 1, replacement.preparations(), "the replacement is prepared before delivery")
	texts, _ = replacement.sent()
	assert.Equal(t, []string{"continue"}, texts, "the input is delivered once, to the replacement")
	row, err := svc.Queries.GetAgentByID(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), row.Resumed, "the resume intent is durable")
	assert.Equal(t, "thread-1", row.AgentSessionID, "the stored thread survives the replacement")
}

// TestInputRecoveryStillSignedOutRetainsInputAndThread covers a replacement
// whose own preparation fails (the user retried while still signed out): no
// second replacement, the input stays queued as failed, and the thread ID and
// resume intent survive.
func TestInputRecoveryStillSignedOutRetainsInputAndThread(t *testing.T) {
	t.Parallel()
	signedOut := errors.New("codex requires sign-in; log in and send the message again")
	svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
		if launch == 1 {
			return func(int) error { return fmtRestartRequired() }
		}
		// The replacement is still signed out at its first check; the login
		// exists before the retained input is retried.
		return func(call int) error {
			if call == 1 {
				return signedOut
			}
			return nil
		}
	})
	const agentID = "agent-1"
	seedRecoveryAgent(t, svc, agentID, "thread-1")
	enqueueRecoveryInput(t, svc, agentID, "input-1")

	item := recoveryFailedItem(t, svc, agentID, "input-1")
	assert.Contains(t, item.Error, "sign-in")
	assert.Equal(t, 2, starts.count(), "no replacement loop")

	// Login becomes available before the retry: the retained input is
	// delivered once, through the registered replacement whose next check
	// succeeds. No third launch exists -- the process is healthy, only its
	// login was missing.
	row, err := svc.Queries.GetAgentByID(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, "thread-1", row.AgentSessionID)
	_, err = svc.InputQueue.Retry(context.Background(), agentID, "input-1", false)
	require.NoError(t, err)
	waitForMessageCount(t, svc, agentID, 1)
	assert.Equal(t, []string{"", "thread-1"}, starts.launches(),
		"no second replacement: the signed-out process stays until a check passes")
	replacement := starts.fake(1)
	require.NotNil(t, replacement)
	assert.Equal(t, 2, replacement.preparations(), "the retry checked the account again")
	texts, _ := replacement.sent()
	assert.Equal(t, []string{"continue"}, texts)
}

// TestInputRecoveryFailedLaunchPreservesThreadAndRetry covers a replacement
// launch that fails: the input is retained, the thread ID survives, and the
// later retry (and even a Worker-restart-style resume) selects the same thread
// through the persisted resume intent.
func TestInputRecoveryFailedLaunchPreservesThreadAndRetry(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
		if launch == 1 {
			return func(int) error { return fmtRestartRequired() }
		}
		return nil
	})
	const agentID = "agent-1"
	seedRecoveryAgent(t, svc, agentID, "thread-1")
	// The replacement launch -- the second one -- fails once; every later
	// start (the retained input's retry) succeeds.
	working := svc.startAgentFn
	var failedReplacement atomic.Bool
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		if starts.count() == 1 && failedReplacement.CompareAndSwap(false, true) {
			return nil, assert.AnError
		}
		return working(ctx, opts, sink)
	}
	enqueueRecoveryInput(t, svc, agentID, "input-1")

	item := recoveryFailedItem(t, svc, agentID, "input-1")
	require.NotEmpty(t, item.Error)
	row, err := svc.Queries.GetAgentByID(context.Background(), agentID)
	require.NoError(t, err)
	assert.Equal(t, "thread-1", row.AgentSessionID, "a failed launch clears no session")
	assert.Equal(t, int64(1), row.Resumed, "the persisted intent survives the failed launch")

	// A later message-less recovery retry selects the same stored thread.
	_, err = svc.InputQueue.Retry(context.Background(), agentID, "input-1", false)
	require.NoError(t, err)
	waitForMessageCount(t, svc, agentID, 1)
	assert.Equal(t, "thread-1", starts.launches()[len(starts.launches())-1],
		"the retry resumes the original thread")
}

// TestInputRecoveryConcurrentCallersReplaceOnce covers two readiness callers
// racing on the same stale process: exactly one replacement starts, and no
// input reaches the stale process.
func TestInputRecoveryConcurrentCallersReplaceOnce(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
		if launch == 1 {
			return func(int) error { return fmtRestartRequired() }
		}
		return nil
	})
	const agentID = "agent-1"
	seedRecoveryAgent(t, svc, agentID, "thread-1")

	// Start the first process by hand so both callers see the same stale one.
	require.NoError(t, svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart))
	svc.Output.WaitActivityRefreshes()
	require.Equal(t, 1, starts.count())

	var wg sync.WaitGroup
	var failures atomic.Int32
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.ensureAgentReadyForInput(agentID); err != nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, int(failures.Load()), 1,
		"at most the losing racer fails; the winner's replacement serves both")
	assert.Equal(t, 2, starts.count(), "exactly one replacement")
	stale := starts.fake(0)
	assert.True(t, stale.wasStopped())
	texts, _ := stale.sent()
	assert.Empty(t, texts)
}

// TestInputRecoveryRefusesWhileTurnRuns covers the blocker rule with the raw
// turn flag: display activity can read idle under a pending stop while the
// provider has not confirmed the turn ended, and replacement still refuses.
func TestInputRecoveryRefusesWhileTurnRuns(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
		if launch == 1 {
			return func(int) error { return fmtRestartRequired() }
		}
		return nil
	})
	const agentID = "agent-1"
	seedRecoveryAgent(t, svc, agentID, "thread-1")
	require.NoError(t, svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart))
	svc.Output.WaitActivityRefreshes()
	stale := starts.fake(0)
	require.NotNil(t, stale)

	// The raw turn flag is active; a pending stop makes the DISPLAYED
	// activity idle before the provider ends the turn. Publish through the
	// launch's own sink, which the output handler adopted as its publisher.
	starts.sinkOf(1).SetTurnState(agent.TurnState{Active: true}, 1)
	svc.Output.NoteAgentStopRequested(agentID, agentID)
	snapshot := svc.Output.AgentActivitySnapshot(agentID, agentID)
	require.True(t, svc.Output.TurnActive(agentID), "the raw turn flag stays authoritative")
	assert.False(t, snapshot.InterruptsWork(), "display activity reads idle under the pending stop")

	// The readiness check itself refuses while the raw flag is active, even
	// though the displayed activity reads idle. (The queue's own active-turn
	// guard holds items in this state; this drives the readiness rule that
	// the guard hands the item to once its copy goes stale.)
	err := svc.ensureAgentReadyForInput(agentID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still runs a turn")
	assert.Equal(t, 1, starts.count(), "nothing was replaced while the turn runs")
	assert.False(t, stale.wasStopped(), "the process that owes the turn is stopped by nobody")

	// The provider ends the turn; the same input then recovers the process.
	starts.sinkOf(1).SetTurnState(agent.TurnState{Active: false}, 2)
	require.NoError(t, svc.ensureAgentReadyForInput(agentID))
	assert.Equal(t, 2, starts.count(), "the ended turn releases the replacement")
	assert.True(t, stale.wasStopped())
}

// TestInputRecoveryRefusesWhileWorkAndControlsRemain covers the remaining
// blockers: a background task, an unanswered root control, and an unanswered
// child control each refuse the replacement and preserve the input.
func TestInputRecoveryRefusesWhileWorkAndControlsRemain(t *testing.T) {
	t.Parallel()
	newBlockedService := func(t *testing.T) (*Service, *recoveryStarts) {
		svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
			if launch == 1 {
				return func(int) error { return fmtRestartRequired() }
			}
			return nil
		})
		return svc, starts
	}
	t.Run("background task", func(t *testing.T) {
		t.Parallel()
		svc, starts := newBlockedService(t)
		const agentID = "agent-1"
		seedRecoveryAgent(t, svc, agentID, "thread-1")
		require.NoError(t, svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart))
		svc.Output.WaitActivityRefreshes()
		taskSink, err := svc.Output.NewRootSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)
		require.NoError(t, err)
		require.NoError(t, taskSink.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: "task-1", Kind: bgtask.KindShell, Title: "long command", Status: bgtask.StatusRunning,
		}))

		enqueueRecoveryInput(t, svc, agentID, "input-1")
		item := recoveryFailedItem(t, svc, agentID, "input-1")
		assert.Contains(t, item.Error, "background task")
		assert.Equal(t, 1, starts.count())
	})
	t.Run("root control", func(t *testing.T) {
		t.Parallel()
		svc, starts := newBlockedService(t)
		const agentID = "agent-1"
		seedRecoveryAgent(t, svc, agentID, "thread-1")
		require.NoError(t, svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart))
		svc.Output.WaitActivityRefreshes()
		createTestControlRequest(t, context.Background(), svc.Queries, db.StoreControlRequestParams{
			AgentID: agentID, RequestID: "root-control-1", Payload: []byte(`{}`),
		})

		enqueueRecoveryInput(t, svc, agentID, "input-1")
		item := recoveryFailedItem(t, svc, agentID, "input-1")
		assert.Contains(t, item.Error, "unanswered control")
		assert.Equal(t, 1, starts.count(), "control-answer delivery stays available; nothing is cleared")
	})
	t.Run("child control", func(t *testing.T) {
		t.Parallel()
		svc, starts := newBlockedService(t)
		const agentID = "agent-1"
		seedRecoveryAgent(t, svc, agentID, "thread-1")
		require.NoError(t, svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart))
		svc.Output.WaitActivityRefreshes()
		// A virtual child with its own unanswered control.
		require.NoError(t, svc.Queries.CreateChildAgent(context.Background(), db.CreateChildAgentParams{
			ID: "child-1", ParentAgentID: sqlNullString(agentID),
			WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		}))
		createTestControlRequest(t, context.Background(), svc.Queries, db.StoreControlRequestParams{
			AgentID: "child-1", RequestID: "child-control-1", Payload: []byte(`{}`),
		})

		enqueueRecoveryInput(t, svc, agentID, "input-1")
		item := recoveryFailedItem(t, svc, agentID, "input-1")
		assert.Contains(t, item.Error, "child-1")
		assert.Equal(t, 1, starts.count())
	})
}

// TestInputRecoveryReadFailureStopsNothing covers a descendant or control read
// that fails during the blocker check: the input is retained, nothing stops,
// and the pause states the store fault.
func TestInputRecoveryReadFailureStopsNothing(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
		if launch == 1 {
			return func(int) error { return fmtRestartRequired() }
		}
		return nil
	})
	const agentID = "agent-1"
	seedRecoveryAgent(t, svc, agentID, "thread-1")
	require.NoError(t, svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart))
	svc.Output.WaitActivityRefreshes()

	svc.Queries = db.New(descendantReadFault{DBTX: svc.DB})
	enqueueRecoveryInput(t, svc, agentID, "input-1")

	require.Eventually(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(context.Background(), agentID)
		return err == nil && snapshot.PauseReason == leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT
	}, inputQueueWait, 10*time.Millisecond, "the dispatch's store fault pauses the queue")
	assert.Equal(t, 1, starts.count(), "a failed read stops no process")
	assert.False(t, starts.fake(0).wasStopped())
}

// TestInputRecoveryLeavesOtherTabsAlone covers the isolation rule: recovering
// one tab's process stops nothing that another tab owns.
func TestInputRecoveryLeavesOtherTabsAlone(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
		if launch == 1 {
			return func(int) error { return fmtRestartRequired() }
		}
		return nil
	})
	seedRecoveryAgent(t, svc, "stale-tab", "thread-stale")
	seedRecoveryAgent(t, svc, "busy-tab", "thread-busy")
	require.NoError(t, svc.ensureAgentRunning("busy-tab", resumeIfConversation, interactiveStart))
	svc.Output.WaitActivityRefreshes()

	enqueueRecoveryInput(t, svc, "stale-tab", "input-1")
	waitForMessageCount(t, svc, "stale-tab", 1)

	assert.Equal(t, 2, starts.count())
	busy := starts.fake(0)
	require.NotNil(t, busy)
	assert.Equal(t, "busy-tab", busy.AgentID())
	assert.False(t, busy.wasStopped(), "another tab's process is never touched")
}

// TestInputRecoveryRefusesClosedAndChangedSessions covers the store guards:
// a closed tab replaces nothing, and a resume-intent write that affects zero
// rows (the session changed) stops nothing.
func TestInputRecoveryRefusesClosedAndChangedSessions(t *testing.T) {
	t.Parallel()
	t.Run("closed tab", func(t *testing.T) {
		t.Parallel()
		svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
			if launch == 1 {
				return func(int) error { return fmtRestartRequired() }
			}
			return nil
		})
		const agentID = "agent-1"
		seedRecoveryAgent(t, svc, agentID, "thread-1")
		require.NoError(t, svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart))
		svc.Output.WaitActivityRefreshes()
		_, closeErr := svc.Queries.CloseAgent(context.Background(), agentID)
		require.NoError(t, closeErr)

		err := svc.ensureAgentReadyForInput(agentID)
		require.Error(t, err)
		assert.ErrorIs(t, err, errAgentClosedDuringLaunch)
		assert.Equal(t, 1, starts.count(), "a closed tab gets no replacement")
		assert.False(t, starts.fake(0).wasStopped())
	})
	t.Run("session changed before the intent write", func(t *testing.T) {
		t.Parallel()
		svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
			if launch == 1 {
				return func(int) error { return fmtRestartRequired() }
			}
			return nil
		})
		const agentID = "agent-1"
		seedRecoveryAgent(t, svc, agentID, "thread-1")
		require.NoError(t, svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart))
		svc.Output.WaitActivityRefreshes()
		// The guarded write matches nothing: the stored session changed.
		svc.Queries = db.New(zeroRowExecFault{DBTX: svc.DB})

		err := svc.ensureAgentReadyForInput(agentID)
		require.Error(t, err)
		var delivery *inputqueue.DeliveryError
		require.ErrorAs(t, err, &delivery, "a zero-row write is a retained, safe failure")
		assert.Equal(t, inputqueue.DispatchNotReady, delivery.Outcome)
		assert.Equal(t, 1, starts.count(), "nothing stopped when the session changed")
		assert.False(t, starts.fake(0).wasStopped())
	})
}

// TestInputRecoveryRefusesAVanishedProvider covers the window where the
// process disappeared between the start and the preparation read: the input
// is retained, and no preparation runs.
func TestInputRecoveryRefusesAVanishedProvider(t *testing.T) {
	t.Parallel()
	svc, _ := newInputRecoveryService(t, nil)
	const agentID = "agent-1"
	seedRecoveryAgent(t, svc, agentID, "thread-1")

	// The process vanished after the start step: the preparation read alone
	// answers for it, and the input stays retained.
	err := svc.prepareAgentInputProcess(agentID, true)
	require.Error(t, err)
	var delivery *inputqueue.DeliveryError
	require.ErrorAs(t, err, &delivery, "a vanished provider is a retained failure")
	assert.Equal(t, inputqueue.DispatchNotReady, delivery.Outcome)
	assert.ErrorIs(t, err, agent.ErrAgentNotFound)
}

// TestInputRecoveryRefusesWithoutStoredSession covers a stale-marked process
// whose row holds no native session: the recovery says so and stops nothing.
func TestInputRecoveryRefusesWithoutStoredSession(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
		if launch == 1 {
			return func(int) error { return fmtRestartRequired() }
		}
		return nil
	})
	const agentID = "agent-1"
	seedRecoveryAgent(t, svc, agentID, "")
	require.NoError(t, svc.ensureAgentRunning(agentID, resumeIfConversation, interactiveStart))
	svc.Output.WaitActivityRefreshes()

	err := svc.ensureAgentReadyForInput(agentID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no stored native session")
	assert.Equal(t, 1, starts.count())
	assert.False(t, starts.fake(0).wasStopped())
}

// TestInputRecoveryWithoutPreparationKeepsDispatch covers providers that do
// not implement the interface: an alive process receives input with no
// preparation step, exactly as before.
func TestInputRecoveryWithoutPreparationKeepsDispatch(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	// claudetest.StartEcho's agent implements no InputProcessPreparer.
	const agentID = "agent-1"
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		return svc.Agents.StartAgentWith(ctx, opts, sink, claudetest.StartSilent)
	}
	enqueueRecoveryInput(t, svc, agentID, "input-1")
	waitForMessageCount(t, svc, agentID, 1)
}

// TestInputRecoveryIdleQuestionAnswerRecoversFirst covers an idle
// QUESTION_ANSWER that reaches ordinary dispatch: it recovers the process
// first, keeps its source-session validation, and delivers once to the
// resumed thread.
func TestInputRecoveryIdleQuestionAnswerRecoversFirst(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, func(launch int) func(call int) error {
		if launch == 1 {
			return func(int) error { return fmtRestartRequired() }
		}
		return nil
	})
	const agentID = "agent-1"
	seedRecoveryAgent(t, svc, agentID, "session-1")
	require.NoError(t, dbtest.StoreControlResponse(context.Background(), svc.DB, db.ControlResponseAnswer{
		AgentID: agentID, RequestID: "question-1", ClaimToken: "claim-1", State: storedStateCompleted,
		RequestPayload: asyncQuestionPayload("question-1"), FollowUp: "Red",
		FollowUpKind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
		InputID:      "answer-input", AgentSessionID: "session-1",
		NonBlocking: true,
	}))

	_, err := svc.InputQueue.Enqueue(context.Background(), inputqueue.NewItem{
		ID: "answer-input", AgentID: agentID,
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, Text: "Red",
	})
	require.NoError(t, err)
	waitForMessageCount(t, svc, agentID, 1)

	assert.Equal(t, []string{"", "session-1"}, starts.launches(), "the answer's dispatch recovered the stored session")
	replacement := starts.fake(1)
	require.NotNil(t, replacement)
	_, sessions := replacement.sent()
	assert.Equal(t, []string{"session-1"}, sessions, "delivery retained the source-session validation")
}

// fmtRestartRequired states the one replacement request this suite uses: an
// error that wraps the shared sentinel, the way a real provider does.
func fmtRestartRequired() error {
	return fmt.Errorf("%w: the Codex login that this process holds cannot accept further input",
		agent.ErrInputProcessRestartRequired)
}

// sqlNullString wraps a parent id the way the sqlc params do.
func sqlNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

// descendantReadFault fails the descendant listing and passes the rest on.
type descendantReadFault struct {
	db.DBTX
}

func (store descendantReadFault) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "-- name: ListDescendantAgentIDs :many") {
		return nil, assert.AnError
	}
	return store.DBTX.QueryContext(ctx, query, args...)
}

// zeroRowExecFault answers the guarded resume-intent write with zero affected
// rows: the state the database holds when the session or the tab changed
// between the row read and the write.
type zeroRowExecFault struct {
	db.DBTX
}

type zeroRowResult struct{}

func (zeroRowResult) LastInsertId() (int64, error) { return 0, nil }
func (zeroRowResult) RowsAffected() (int64, error) { return 0, nil }

func (store zeroRowExecFault) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "-- name: MarkAgentResumedForInputRecovery :execrows") {
		return zeroRowResult{}, nil
	}
	return store.DBTX.ExecContext(ctx, query, args...)
}
