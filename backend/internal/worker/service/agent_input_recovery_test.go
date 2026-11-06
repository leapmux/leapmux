package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
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
	agentID   string
	stopped   chan struct{}
	stopOnce  sync.Once
	stopCalls atomic.Int32

	mu                 sync.Mutex
	prepareScript      func(call int) error
	prepareCalls       int
	recoveryRequired   bool
	sentTexts          []string
	sentSessions       []string
	sentAttachments    [][]*leapmuxv1.Attachment
	turnSink           agent.ProviderServices
	turnState          agent.TurnState
	turnPublicationSeq uint64
}

func (a *recoveryAgent) AgentID() string { return a.agentID }

func (a *recoveryAgent) PrepareInputProcess() error {
	a.mu.Lock()
	a.prepareCalls++
	call := a.prepareCalls
	script := a.prepareScript
	required := a.recoveryRequired
	a.mu.Unlock()
	if required {
		return fmtRestartRequired()
	}
	if script == nil {
		return nil
	}
	err := script(call)
	if errors.Is(err, agent.ErrInputProcessRestartRequired) {
		a.mu.Lock()
		a.recoveryRequired = true
		a.mu.Unlock()
	}
	return err
}

var _ agent.InputProcessPreparer = (*recoveryAgent)(nil)

func (a *recoveryAgent) InputProcessRecoveryRequired() bool {
	a.mu.Lock()
	required := a.recoveryRequired
	a.mu.Unlock()
	return required && !a.IsStopped()
}

func (a *recoveryAgent) PublishTurnActive() agent.TurnState {
	a.mu.Lock()
	state := a.turnState
	a.turnPublicationSeq++
	sequence := a.turnPublicationSeq
	sink := a.turnSink
	a.mu.Unlock()
	if a.IsStopped() {
		state = agent.TurnState{}
	}
	sink.SetTurnState(state, sequence)
	return state
}

func (a *recoveryAgent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	a.mu.Lock()
	a.sentTexts = append(a.sentTexts, content)
	a.sentAttachments = append(a.sentAttachments, attachments)
	a.mu.Unlock()
	return nil
}

func (a *recoveryAgent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	a.mu.Lock()
	a.sentTexts = append(a.sentTexts, content)
	a.sentAttachments = append(a.sentAttachments, attachments)
	a.sentSessions = append(a.sentSessions, sessionID)
	a.mu.Unlock()
	return nil
}

func (a *recoveryAgent) Stop() {
	a.stopCalls.Add(1)
	a.stopOnce.Do(func() { close(a.stopped) })
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
	fake := &recoveryAgent{agentID: opts.AgentID, stopped: make(chan struct{}), prepareScript: script, turnSink: sink}
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

// staleFirstInputPreparation marks the first process for replacement and lets
// every later process prepare successfully.
func staleFirstInputPreparation(launch int) func(call int) error {
	if launch == 1 {
		return func(int) error { return fmtRestartRequired() }
	}
	return nil
}

// newInputRecoveryService wires the fake into every cold start this service
// makes, including the replacements that input recovery launches.
func newInputRecoveryService(t *testing.T, prepareFor func(launch int) func(call int) error) (*Service, *recoveryStarts) {
	t.Helper()
	svc, _, _ := setupTestService(t)
	// Recovery tests exercise the production exit cleanup as well as launch.
	svc.Agents.SetOnExit(svc.HandleAgentProcessExit)
	t.Cleanup(func() {
		for _, id := range svc.Agents.ListAgentIDs() {
			unlock := svc.Agents.LockAgent(id)
			svc.Agents.StopAndWaitAgent(id)
			unlock()
		}
	})
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

// recoveryQueuedItem waits for an unsubmitted input held by recovery approval.
func recoveryQueuedItem(t *testing.T, svc *Service, agentID, inputID string) inputqueue.SnapshotItem {
	t.Helper()
	var stored inputqueue.SnapshotItem
	require.Eventually(t, func() bool {
		snapshot, err := svc.InputQueue.Snapshot(t.Context(), agentID)
		if err != nil || snapshot.PauseReason != leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_PROCESS_RECOVERY {
			return false
		}
		for _, item := range snapshot.Items {
			if item.ID == inputID && item.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED && item.Error != "" {
				stored = item
				return true
			}
		}
		return false
	}, inputQueueWait, 10*time.Millisecond)
	return stored
}

func approveRecoveryInput(t *testing.T, svc *Service, agentID string) {
	t.Helper()
	target := svc.inputProcessRecovery(agentID)
	require.NotNil(t, target)
	require.NoError(t, svc.recoverAgentInputProcess(agentID, target))
}

func TestInputRecoveryReplacesStaleProcessAndDeliversOnce(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	const id = "agent-1"
	seedRecoveryAgent(t, svc, id, "thread-1")
	enqueueRecoveryInput(t, svc, id, "input-1")
	held := recoveryQueuedItem(t, svc, id, "input-1")
	assert.Contains(t, held.Error, "Recover login")
	require.Equal(t, 1, starts.count())
	assert.False(t, starts.fake(0).wasStopped())
	row, err := svc.Queries.GetAgentByID(t.Context(), id)
	require.NoError(t, err)
	assert.Zero(t, row.Resumed, "an unapproved recovery writes no resume intent")
	approveRecoveryInput(t, svc, id)
	waitForMessageCount(t, svc, id, 1)
	assert.Equal(t, []string{"", "thread-1"}, starts.launches())
	assert.True(t, starts.fake(0).wasStopped())
	texts, _ := starts.fake(0).sent()
	assert.Empty(t, texts)
	texts, _ = starts.fake(1).sent()
	assert.Equal(t, []string{"continue"}, texts)
	row, err = svc.Queries.GetAgentByID(t.Context(), id)
	require.NoError(t, err)
	assert.EqualValues(t, 1, row.Resumed)
	assert.Equal(t, "thread-1", row.AgentSessionID)
	assert.Nil(t, svc.inputProcessRecovery(id), "healthy replacement input needs no further approval")
}

func TestInputRecoveryStillSignedOutRetainsInputAndThread(t *testing.T) {
	t.Parallel()
	signedOut := errors.New("codex requires sign-in; log in and approve recovery again")
	svc, starts := newInputRecoveryService(t, func(launch int) func(int) error {
		if launch == 1 {
			return func(int) error { return fmtRestartRequired() }
		}
		if launch == 2 {
			return func(int) error { return fmt.Errorf("%w: %w", agent.ErrInputProcessRestartRequired, signedOut) }
		}
		return nil
	})
	const id = "signed-out-agent"
	seedRecoveryAgent(t, svc, id, "thread-1")
	enqueueRecoveryInput(t, svc, id, "input-1")
	recoveryQueuedItem(t, svc, id, "input-1")
	first := svc.inputProcessRecovery(id)
	require.NotNil(t, first)
	require.ErrorIs(t, svc.recoverAgentInputProcess(id, first), signedOut)
	require.Equal(t, 2, starts.count(), "one approval permits one replacement")
	second := svc.inputProcessRecovery(id)
	require.NotNil(t, second)
	assert.NotEqual(t, first.OutputGeneration, second.OutputGeneration)
	assert.False(t, starts.fake(1).wasStopped(), "failed preparation cannot start a replacement loop")
	texts, _ := starts.fake(1).sent()
	assert.Empty(t, texts)
	row, err := svc.Queries.GetAgentByID(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, "thread-1", row.AgentSessionID)
	assert.EqualValues(t, 1, row.Resumed)
	// The next approval represents the login becoming available for launch 3.
	approveRecoveryInput(t, svc, id)
	waitForMessageCount(t, svc, id, 1)
	assert.Equal(t, []string{"", "thread-1", "thread-1"}, starts.launches())
	texts, _ = starts.fake(2).sent()
	assert.Equal(t, []string{"continue"}, texts)
}

func TestInputRecoveryFailedLaunchPreservesThreadAndRetry(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	const id = "failed-launch-agent"
	seedRecoveryAgent(t, svc, id, "thread-1")
	working := svc.startAgentFn
	var failed atomic.Bool
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		if starts.count() == 1 && failed.CompareAndSwap(false, true) {
			return nil, assert.AnError
		}
		return working(ctx, opts, sink)
	}
	enqueueRecoveryInput(t, svc, id, "input-1")
	recoveryQueuedItem(t, svc, id, "input-1")
	require.ErrorIs(t, svc.recoverAgentInputProcess(id, svc.inputProcessRecovery(id)), assert.AnError)
	assert.True(t, starts.fake(0).wasStopped())
	assert.False(t, svc.Agents.HasAgent(id))
	row, err := svc.Queries.GetAgentByID(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, "thread-1", row.AgentSessionID)
	assert.EqualValues(t, 1, row.Resumed)
	// The old process is gone. Resume starts a fresh process without consuming
	// the approval again and without resending any accepted failed turn.
	_, err = svc.InputQueue.SetPaused(t.Context(), id, false)
	require.NoError(t, err)
	waitForMessageCount(t, svc, id, 1)
	assert.Equal(t, "thread-1", starts.launches()[len(starts.launches())-1])
}

func TestInputRecoveryConcurrentReadinessRequiresApproval(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	entered := make(chan struct{}, 2)
	releasePreparation := make(chan struct{})
	release := sync.OnceFunc(func() { close(releasePreparation) })
	defer release()
	svc, starts := newInputRecoveryService(t, func(int) func(int) error {
		return func(int) error {
			entered <- struct{}{}
			<-releasePreparation
			return fmtRestartRequired()
		}
	})
	const id = "concurrent-readiness"
	seedRecoveryAgent(t, svc, id, "thread-1")
	require.NoError(t, svc.ensureAgentRunning(id, resumeStoredSession, interactiveStart))
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- svc.ensureAgentReadyForInput(id) }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("both preparations did not enter the barrier")
		}
	}
	release()
	for range 2 {
		require.ErrorIs(t, <-results, agent.ErrInputProcessRestartRequired)
	}
	assert.Equal(t, 1, starts.count())
	assert.False(t, starts.fake(0).wasStopped())
}

func TestInputRecoveryConcurrentApprovalsReplaceOnce(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	const id = "concurrent-approvals"
	seedRecoveryAgent(t, svc, id, "thread-1")
	require.ErrorIs(t, svc.ensureAgentReadyForInput(id), agent.ErrInputProcessRestartRequired)
	approved := svc.inputProcessRecovery(id)
	require.NotNil(t, approved)
	entered := make(chan struct{})
	releaseWrite := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseWrite) })
	defer release()
	svc.Queries = db.New(&recoveryIntentStore{DBTX: svc.DB, before: func(context.Context) error {
		close(entered)
		<-releaseWrite
		return nil
	}})
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- svc.recoverAgentInputProcess(id, approved) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the approval did not reach its durable intent")
	}
	go func() { second <- svc.recoverAgentInputProcess(id, approved) }()
	require.Eventually(t, func() bool { return svc.Agents.LifecycleLockCallersForTest(id) >= 2 }, inputQueueWait, time.Millisecond)
	release()
	require.NoError(t, <-first)
	require.ErrorIs(t, <-second, inputqueue.ErrConflict)
	assert.Equal(t, 2, starts.count())
	assert.True(t, starts.fake(0).wasStopped())
	assert.False(t, starts.fake(1).wasStopped())
}

func TestInputRecoveryApprovalCanInterruptAnActiveTurn(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	const id = "active-turn-recovery"
	seedRecoveryAgent(t, svc, id, "thread-1")
	require.NoError(t, svc.ensureAgentRunning(id, resumeStoredSession, interactiveStart))
	starts.sinkOf(1).SetTurnState(agent.TurnState{Active: true}, 1)
	svc.Output.NoteAgentStopRequested(id, id)
	require.True(t, svc.Output.TurnActive(id))
	assert.False(t, svc.Output.AgentActivitySnapshot(id, id).InterruptsWork())
	require.ErrorIs(t, svc.ensureAgentReadyForInput(id), agent.ErrInputProcessRestartRequired)
	assert.False(t, starts.fake(0).wasStopped())
	assert.True(t, svc.Output.TurnActive(id), "preparation preserves work before approval")
	approveRecoveryInput(t, svc, id)
	assert.Equal(t, 2, starts.count())
	assert.True(t, starts.fake(0).wasStopped(), "explicit approval authorizes interruption")
}

func TestInputRecoveryKeepsWorkAndControlsUntilApproval(t *testing.T) {
	for _, kind := range []string{"background task", "root control", "child control", "closed child question"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
			const id = "approval-work"
			seedRecoveryAgent(t, svc, id, "thread-1")
			require.NoError(t, svc.ensureAgentRunning(id, resumeStoredSession, interactiveStart))
			owner := id
			if kind == "background task" {
				require.NoError(t, starts.sinkOf(1).UpsertBackgroundTask(bgtask.Upsert{RowKey: "command", Kind: bgtask.KindShell, Status: bgtask.StatusRunning}))
			} else {
				if kind != "root control" {
					owner = "child"
					require.NoError(t, svc.Queries.CreateChildAgent(t.Context(), db.CreateChildAgentParams{ID: owner, ParentAgentID: sql.NullString{String: id, Valid: true}, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX}))
					if kind == "closed child question" {
						_, err := svc.Queries.CloseAgent(t.Context(), owner)
						require.NoError(t, err)
					}
				}
				createTestControlRequest(t, t.Context(), svc.Queries, db.StoreControlRequestParams{AgentID: owner, RequestID: "pending", Payload: []byte(`{}`), NonBlocking: kind == "closed child question"})
			}
			enqueueRecoveryInput(t, svc, id, "held-input")
			recoveryQueuedItem(t, svc, id, "held-input")
			assert.False(t, starts.fake(0).wasStopped())
			if kind == "background task" {
				assert.EqualValues(t, 1, svc.Output.AgentActivitySnapshot(id, id).ActiveTasks)
			} else {
				controls, err := svc.Queries.ListControlRequestsByAgentID(t.Context(), owner)
				require.NoError(t, err)
				require.Len(t, controls, 1)
			}
			approveRecoveryInput(t, svc, id)
			waitForMessageCount(t, svc, id, 1)
			if kind != "background task" {
				controls, err := svc.Queries.ListControlRequestsByAgentID(t.Context(), owner)
				require.NoError(t, err)
				if kind == "closed child question" {
					assert.Len(t, controls, 1, "conversation-owned questions survive the same-session replacement")
				} else {
					assert.Empty(t, controls, "blocking requests belonged to the stopped process")
				}
			}
		})
	}
}

func TestInputRecoveryLeavesOtherTabsAlone(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, func(launch int) func(int) error {
		if launch == 2 {
			return func(int) error { return fmtRestartRequired() }
		}
		return nil
	})
	seedRecoveryAgent(t, svc, "stale-tab", "thread-stale")
	seedRecoveryAgent(t, svc, "busy-tab", "thread-busy")
	require.NoError(t, svc.ensureAgentRunning("busy-tab", resumeStoredSession, interactiveStart))
	starts.sinkOf(1).SetTurnState(agent.TurnState{Active: true}, 1)
	enqueueRecoveryInput(t, svc, "stale-tab", "held-input")
	recoveryQueuedItem(t, svc, "stale-tab", "held-input")
	approveRecoveryInput(t, svc, "stale-tab")
	waitForMessageCount(t, svc, "stale-tab", 1)
	assert.Equal(t, 3, starts.count())
	assert.False(t, starts.fake(0).wasStopped())
	assert.True(t, svc.Output.TurnActive("busy-tab"))
	assert.Zero(t, starts.fake(0).preparations())
	assert.True(t, starts.fake(1).wasStopped())
}

func TestInputRecoveryApprovalRejectsChangedTargets(t *testing.T) {
	for _, change := range []string{"closed", "archived", "session", "generation", "replacement"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
			const id = "changed-target"
			seedRecoveryAgent(t, svc, id, "thread-1")
			require.ErrorIs(t, svc.ensureAgentReadyForInput(id), agent.ErrInputProcessRestartRequired)
			approved := svc.inputProcessRecovery(id)
			require.NotNil(t, approved)
			switch change {
			case "closed":
				_, err := svc.Queries.CloseAgent(t.Context(), id)
				require.NoError(t, err)
			case "archived":
				_, err := svc.Queries.SetAgentWorkspaceArchived(t.Context(), db.SetAgentWorkspaceArchivedParams{ID: id, WorkspaceArchived: true})
				require.NoError(t, err)
			case "session":
				require.NoError(t, svc.Queries.UpdateAgentSessionID(t.Context(), db.UpdateAgentSessionIDParams{ID: id, AgentSessionID: "changed-thread"}))
			case "generation":
				_, err := svc.DB.ExecContext(t.Context(), "UPDATE agents SET output_generation = output_generation + 1 WHERE id = ?", id)
				require.NoError(t, err)
			case "replacement":
				unlock := svc.Agents.LockAgent(id)
				svc.Agents.StopAndWaitAgent(id)
				unlock()
				require.NoError(t, svc.ensureAgentRunning(id, resumeStoredSession, interactiveStart))
			}
			err := svc.recoverAgentInputProcess(id, approved)
			require.Error(t, err)
			if change == "replacement" {
				assert.Equal(t, 2, starts.count())
				assert.False(t, starts.fake(1).wasStopped())
			} else {
				assert.Equal(t, 1, starts.count())
				assert.False(t, starts.fake(0).wasStopped())
			}
		})
	}
}

func TestInputRecoveryRefusesAVanishedProvider(t *testing.T) {
	t.Parallel()
	svc, _ := newInputRecoveryService(t, nil)
	seedRecoveryAgent(t, svc, "vanished", "thread-1")
	err := svc.prepareAgentInputProcess("vanished")
	require.ErrorIs(t, err, agent.ErrAgentNotFound)
	var delivery *inputqueue.DeliveryError
	require.ErrorAs(t, err, &delivery)
	assert.Equal(t, inputqueue.DispatchNotReady, delivery.Outcome)
}

func TestInputRecoveryRefusesWithoutStoredSession(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	seedRecoveryAgent(t, svc, "no-session", "")
	err := svc.ensureAgentReadyForInput("no-session")
	require.ErrorContains(t, err, "no stored native session")
	assert.Nil(t, svc.inputProcessRecovery("no-session"))
	assert.Equal(t, 1, starts.count())
	assert.False(t, starts.fake(0).wasStopped())
}

func TestInputRecoveryWithoutPreparationKeepsDispatch(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	const id = "plain-provider"
	require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: id, WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE}))
	svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
		return svc.Agents.StartAgentWith(ctx, opts, sink, claudetest.StartSilent)
	}
	enqueueRecoveryInput(t, svc, id, "plain-input")
	waitForMessageCount(t, svc, id, 1)
	assert.Nil(t, svc.inputProcessRecovery(id))
}

func TestInputRecoveryIdleQuestionAnswerRecoversFirst(t *testing.T) {
	t.Parallel()
	svc, starts := newInputRecoveryService(t, staleFirstInputPreparation)
	const id = "idle-answer"
	seedRecoveryAgent(t, svc, id, "session-1")
	require.NoError(t, dbtest.StoreControlResponse(t.Context(), svc.DB, db.ControlResponseAnswer{
		AgentID: id, RequestID: "question-1", ClaimToken: "claim-1", State: storedStateCompleted,
		RequestPayload: asyncQuestionPayload("question-1"), FollowUp: "Red", FollowUpKind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
		InputID: "answer-input", AgentSessionID: "session-1", NonBlocking: true,
	}))
	_, err := svc.InputQueue.Enqueue(t.Context(), inputqueue.NewItem{ID: "answer-input", AgentID: id, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, Text: "Red"})
	require.NoError(t, err)
	recoveryQueuedItem(t, svc, id, "answer-input")
	approveRecoveryInput(t, svc, id)
	waitForMessageCount(t, svc, id, 1)
	assert.Equal(t, []string{"", "session-1"}, starts.launches())
	_, sessions := starts.fake(1).sent()
	assert.Equal(t, []string{"session-1"}, sessions)
}

func fmtRestartRequired() error {
	return fmt.Errorf("%w: the Codex login that this process holds cannot accept further input", agent.ErrInputProcessRestartRequired)
}
