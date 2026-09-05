package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// applyArchive drives the lifecycle operation the way its one production
// caller does -- the orphan reconciler, through the ApplyArchiveState hook.
// There is no RPC for it: the Hub nudges, and the reconcile pass applies.
func applyArchive(t *testing.T, svc *Service, state leapmuxv1.WorkspaceArchiveState, tabs ...*leapmuxv1.TabRef) []string {
	t.Helper()
	resumeAgentIDs, err := svc.ApplyTabArchiveState(context.Background(), state, tabs)
	require.NoError(t, err)
	return resumeAgentIDs
}

// agentStatuses collects the statuses broadcast for one agent from a writer
// that a WatchEvents dispatch streams into. Duplicates are kept: "how many
// times" is exactly what the duplicate-request test asks.
func agentStatuses(t *testing.T, w *testResponseWriter, agentID string) []leapmuxv1.AgentStatus {
	t.Helper()
	var out []leapmuxv1.AgentStatus
	for _, s := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &resp); err != nil {
			continue
		}
		event := resp.GetAgentEvent()
		if event.GetAgentId() != agentID {
			continue
		}
		if sc := event.GetStatusChange(); sc != nil {
			out = append(out, sc.GetStatus())
		}
	}
	return out
}

// watchAgent subscribes to one agent's events so a test can read the broadcasts
// archival makes.
//
// The catch-up replay cannot forge a status: it replays MESSAGES, and an agent
// StatusChange reaches a subscriber only from broadcastStatusChange. So every
// status agentStatuses reports is one this operation emitted.
func watchAgent(t *testing.T, d *channel.Dispatcher, agentID string) *testResponseWriter {
	t.Helper()
	w := newTestWriter()
	dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{{
			AgentId: agentID,
			Replay:  leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST,
			Mode:    leapmuxv1.WatchMode_WATCH_MODE_FULL,
		}},
	}, w)
	return w
}

func TestWorkspaceArchive_StopsProcessesAndPreservesTabData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const agentID = "archive-agent"
	const terminalID = "archive-terminal"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Resumed:       1,
	}))
	require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
		AgentSessionID: "session-archive", ID: agentID,
	}))
	_, err := svc.Queries.CreateMessage(ctx, db.CreateMessageParams{
		ID: "message-1", AgentID: agentID, Source: leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		Content: []byte("preserved transcript"), ContentCompression: leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE,
		SpanLines: "[]", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		CreatedAt: sqltime.NewSQLiteTime(time.Now()),
	})
	require.NoError(t, err)
	_, err = svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: agentID, WorkingDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)

	terminalDir := t.TempDir()
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: terminalID, WorkingDir: terminalDir, HomeDir: t.TempDir(),
		Shell: testutil.TestShell(), Cols: 80, Rows: 24, Screen: []byte{},
	}))
	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: terminalID, Shell: testutil.TestShell(), WorkingDir: terminalDir, Cols: 80, Rows: 24,
	}, svc.makeTerminalOutputFn(terminalID), svc.makeTerminalExitFn()))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, terminalID)
	require.True(t, svc.Terminals.AppendOutput(terminalID, []byte("preserved terminal output")))

	worktreePath := t.TempDir()
	_, err = svc.Queries.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID: "worktree-1", WorktreePath: worktreePath, RepoRoot: t.TempDir(), BranchName: "archive-test",
	})
	require.NoError(t, err)
	for _, tab := range []*leapmuxv1.TabRef{
		{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID},
		{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: terminalID},
	} {
		require.NoError(t, svc.Queries.AddWorktreeTab(ctx, db.AddWorktreeTabParams{
			WorktreeID: "worktree-1", TabType: tab.GetTabType(), TabID: tab.GetTabId(),
		}))
	}
	filePath := testutil.NativeAbsPath("/tmp/archive-preserved.txt")
	require.NoError(t, svc.TabPayloads.Register(ctx, RegisterTabPayloadParams{
		UserID: "user-1", TabID: "archive-file", Payload: &leapmuxv1.TabPayload{
			Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filePath}},
		},
	}))
	require.NoError(t, svc.Queries.CreateControlRequest(ctx, db.CreateControlRequestParams{
		AgentID: agentID, RequestID: "control-1", Payload: []byte("pending"), ClaimToken: "claim-1",
	}))
	require.NoError(t, svc.Queries.UpsertAutoContinueSchedule(ctx, db.UpsertAutoContinueScheduleParams{
		AgentID: agentID, Reason: string(agent.AutoContinueReasonAPIError), Content: "Continue.",
		DueAt: sqltime.NewSQLiteTime(time.Now().Add(time.Hour)), SourcePayload: []byte{},
	}))
	var agentCleanup, terminalCleanup atomic.Bool
	svc.agentCleanups.register(agentID, func() { agentCleanup.Store(true) })
	svc.terminalCleanups.register(terminalID, func() { terminalCleanup.Store(true) })

	applyArchive(t, svc,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		&leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID},
		&leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: terminalID},
		&leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "archive-file"})

	assert.False(t, svc.Agents.HasAgent(agentID))
	assert.False(t, svc.Terminals.IsRunning(terminalID))
	agentRow, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), agentRow.WorkspaceArchived)
	assert.False(t, agentRow.ClosedAt.Valid)
	terminalRow, err := svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), terminalRow.WorkspaceArchived)
	assert.False(t, terminalRow.ClosedAt.Valid)
	assert.Contains(t, string(terminalRow.Screen), "preserved terminal output")
	messages, err := svc.Queries.ListAllMessagesByAgentID(ctx, db.ListAllMessagesByAgentIDParams{AgentID: agentID})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	assert.Equal(t, []byte("preserved transcript"), messages[0].Content)
	links, err := svc.Queries.CountWorktreeTabs(ctx, "worktree-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), links)
	payload, err := svc.TabPayloads.Get(ctx, "user-1", "archive-file")
	require.NoError(t, err)
	assert.Equal(t, filePath, payload.GetFile().GetFilePath())
	controls, err := svc.Queries.ListControlRequestsByAgentID(ctx, agentID)
	require.NoError(t, err)
	assert.Empty(t, controls)
	activeSchedules, err := svc.Queries.ListActiveAutoContinueSchedules(ctx)
	require.NoError(t, err)
	assert.Empty(t, activeSchedules)
	assert.True(t, agentCleanup.Load())
	assert.True(t, terminalCleanup.Load())
}

// TestWorkspaceArchive_StopsAnAgentThatOwnsNoProcess covers the tab that is
// already INACTIVE when the archive lands -- a worker restart, or a tab the
// user opened and never talked to. There is nothing to stop, so the DB write
// and the broadcast are the whole operation, and a client watching that tab
// still has to be told which state it settled in.
func TestWorkspaceArchive_StopsAnAgentThatOwnsNoProcess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	const agentID = "archive-inactive"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.False(t, svc.Agents.HasAgent(agentID))
	watcher := watchAgent(t, dispatcher, agentID)

	applyArchive(t, svc,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		&leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID})

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), row.WorkspaceArchived)
	assert.False(t, row.ClosedAt.Valid, "archival preserves the open row")
	// The broadcast reaches a subscriber's stream asynchronously, so this waits
	// for it rather than reading once: the DB write above is what the handler
	// completes synchronously, and the delivery trails it.
	testutil.AssertEventually(t, func() bool {
		return len(agentStatuses(t, watcher, agentID)) == 1
	}, "archival broadcasts the agent's settled state")
	assert.Equal(t,
		[]leapmuxv1.AgentStatus{leapmuxv1.AgentStatus_AGENT_STATUS_INACTIVE},
		agentStatuses(t, watcher, agentID))
}

// TestWorkspaceArchive_StopsEveryTabInTheRequest covers the LIST, which the
// single-tab cases cannot: a workspace normally holds several agents and
// terminals, and a loop that stopped the first and returned -- or that stopped
// the agents and skipped the terminals -- passes every other test in this file.
func TestWorkspaceArchive_StopsEveryTabInTheRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	agentIDs := []string{"multi-agent-1", "multi-agent-2", "multi-agent-3"}
	terminalIDs := []string{"multi-terminal-1", "multi-terminal-2"}
	tabs := make([]*leapmuxv1.TabRef, 0, len(agentIDs)+len(terminalIDs))
	for _, agentID := range agentIDs {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
		_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
			AgentID: agentID, WorkingDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}, svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
		require.NoError(t, err)
		require.True(t, svc.Agents.HasAgent(agentID))
		tabs = append(tabs, &leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID})
	}
	for _, terminalID := range terminalIDs {
		terminalDir := t.TempDir()
		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: terminalID, WorkingDir: terminalDir, HomeDir: t.TempDir(),
			Shell: testutil.TestShell(), Cols: 80, Rows: 24, Screen: []byte{},
		}))
		require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
			ID: terminalID, Shell: testutil.TestShell(), WorkingDir: terminalDir, Cols: 80, Rows: 24,
		}, svc.makeTerminalOutputFn(terminalID), svc.makeTerminalExitFn()))
		testutil.RegisterTerminalCleanup(t, svc.Terminals, terminalID)
		require.True(t, svc.Terminals.IsRunning(terminalID))
		tabs = append(tabs, &leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: terminalID})
	}

	applyArchive(t, svc, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, tabs...)

	for _, agentID := range agentIDs {
		assert.False(t, svc.Agents.HasAgent(agentID), "agent %s must stop", agentID)
		row, err := svc.Queries.GetAgentByID(ctx, agentID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), row.WorkspaceArchived, "agent %s must record the archive", agentID)
	}
	for _, terminalID := range terminalIDs {
		assert.False(t, svc.Terminals.IsRunning(terminalID), "terminal %s must stop", terminalID)
		row, err := svc.Queries.GetTerminal(ctx, terminalID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), row.WorkspaceArchived, "terminal %s must record the archive", terminalID)
	}
}

// TestWorkspaceArchive_DuplicateArchiveIsANoOp pins the idempotency the Hub
// relies on: the reconciler re-sends the authoritative state on EVERY pass, so
// a second ARCHIVED for a tab already archived must run no teardown at all.
// Re-running it would retire a control socket that a legitimate later spawn had
// registered, and re-broadcast a settled state on every hourly pass.
func TestWorkspaceArchive_DuplicateArchiveIsANoOp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const agentID = "archive-twice"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	var firstCleanup atomic.Bool
	svc.agentCleanups.register(agentID, func() { firstCleanup.Store(true) })

	tab := &leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID}
	applyArchive(t, svc, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, tab)
	require.True(t, firstCleanup.Load(), "the first archive runs the teardown")
	firstQueue, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)

	// A fresh cleanup under the same id: the second request must leave it
	// registered, which no assertion on the FIRST one could tell apart from a
	// teardown that simply found an empty registry.
	var secondCleanup atomic.Bool
	svc.agentCleanups.register(agentID, func() { secondCleanup.Store(true) })

	applyArchive(t, svc, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, tab)

	assert.False(t, secondCleanup.Load(), "an archive that changes no row runs no teardown")
	secondQueue, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, firstQueue.Revision, secondQueue.Revision, "an unchanged archive must not mutate the queue")
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), row.WorkspaceArchived)
}

func TestWorkspaceArchive_PausesAndRestoresAnUnpausedInputQueue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const agentID = "queued-agent"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.InputQueue.TurnStarted(ctx, agentID)
	require.NoError(t, err)
	_, err = svc.InputQueue.Enqueue(ctx, inputqueue.NewItem{
		ID: "queued", AgentID: agentID, Text: "after unarchive",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	_, _, _, err = svc.InputQueue.BeginEdit(ctx, agentID, "queued", "client-1", false)
	require.NoError(t, err)
	tab := &leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID}

	_, err = svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, []*leapmuxv1.TabRef{tab})
	require.NoError(t, err)
	archived, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.True(t, archived.Paused)
	assert.False(t, archived.ActiveTurn)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, archived.PauseReason)

	_, err = svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, []*leapmuxv1.TabRef{tab})
	require.NoError(t, err)
	active, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.False(t, active.Paused)
	assert.False(t, active.ActiveTurn)
	require.Len(t, active.Items, 1)
}

func TestWorkspaceArchive_PreservesAManualQueuePause(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const agentID = "manually-paused-agent"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.InputQueue.TurnStarted(ctx, agentID)
	require.NoError(t, err)
	_, err = svc.InputQueue.SetPaused(ctx, agentID, true)
	require.NoError(t, err)
	tab := &leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID}

	_, err = svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, []*leapmuxv1.TabRef{tab})
	require.NoError(t, err)
	_, err = svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, []*leapmuxv1.TabRef{tab})
	require.NoError(t, err)
	snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.True(t, snapshot.Paused)
	assert.False(t, snapshot.ActiveTurn)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL, snapshot.PauseReason)
}

func TestWorkspaceArchive_PreservesAnExistingAgentStoppedPause(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const agentID = "crashed-agent"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.InputQueue.Pause(ctx, agentID, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
	require.NoError(t, err)
	tab := &leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID}

	_, err = svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, []*leapmuxv1.TabRef{tab})
	require.NoError(t, err)
	_, err = svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, []*leapmuxv1.TabRef{tab})
	require.NoError(t, err)
	snapshot, err := svc.InputQueue.Snapshot(ctx, agentID)
	require.NoError(t, err)
	assert.True(t, snapshot.Paused)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, snapshot.PauseReason)
}

// TestWorkspaceArchive_CancelsAnInFlightStartup covers the startup race.
//
// Archival cancels the startup context and then WAITS for that goroutine's own
// trailing work before it clears the tab's runtime state -- otherwise the
// startup registers a control socket after the teardown ran and nothing ever
// retires it. It must also record the stop as an ARCHIVE rather than as a close
// or a failure: the tab stays open, keeps its worktree decision unmade, and
// starts again on unarchive.
func TestWorkspaceArchive_CancelsAnInFlightStartup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const agentID = "archive-starting"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	startupCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	handle := svc.AgentStartup.begin(agentID, cancel)
	require.NotNil(t, handle)

	// Stands in for runAgentStartup: it returns only once the archive cancelled
	// its context, so `settled` is closed strictly before finishEntry.
	settled := make(chan struct{})
	go func() {
		<-startupCtx.Done()
		close(settled)
		svc.AgentStartup.finishEntry(handle)
	}()

	applyArchive(t, svc, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		&leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID})

	select {
	case <-settled:
	default:
		require.FailNow(t, "the archive answered while the startup it cancelled was still running")
	}
	assert.True(t, svc.AgentStartup.archiveStopped(handle),
		"the startup must read the stop as an archive")
	_, closeRaced := svc.AgentStartup.dispositionOf(handle)
	assert.False(t, closeRaced, "archival is not a tab close, so no worktree decision is recorded")
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), row.WorkspaceArchived)
	assert.False(t, row.ClosedAt.Valid)
	assert.Empty(t, row.StartupError, "archival is not a startup failure, so the tab stays retryable")
}

func TestWorkspaceArchive_UnarchiveResumesOnlyEligibleAgents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	recorder := newStartRecorder()
	recorder.install(svc)
	resumer := svc.AgentResumer()
	t.Cleanup(resumer.Stop)

	for _, testAgent := range []struct {
		id           string
		sessionID    string
		resumed      int64
		startupError string
		closed       bool
	}{
		{id: "eligible", sessionID: "session-eligible", resumed: 1},
		{id: "startup-failed", sessionID: "session-failed", resumed: 1, startupError: "failed"},
		{id: "never-started"},
		{id: "closed", sessionID: "session-closed", resumed: 1, closed: true},
	} {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: testAgent.id, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
			Resumed:       testAgent.resumed,
		}))
		if testAgent.sessionID != "" {
			require.NoError(t, svc.Queries.UpdateAgentSessionID(ctx, db.UpdateAgentSessionIDParams{
				AgentSessionID: testAgent.sessionID, ID: testAgent.id,
			}))
		}
		if testAgent.startupError != "" {
			require.NoError(t, svc.Queries.SetAgentStartupError(ctx, db.SetAgentStartupErrorParams{
				StartupError: testAgent.startupError, ID: testAgent.id,
			}))
		}
		if testAgent.closed {
			_, err := svc.Queries.CloseAgent(ctx, testAgent.id)
			require.NoError(t, err)
		}
		_, err := svc.Queries.SetAgentWorkspaceArchived(ctx, db.SetAgentWorkspaceArchivedParams{
			WorkspaceArchived: 1, ID: testAgent.id,
		})
		require.NoError(t, err)
	}
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "terminal-exited", Screen: []byte("screen"),
	}))
	_, err := svc.Queries.SetTerminalWorkspaceArchived(ctx, db.SetTerminalWorkspaceArchivedParams{
		WorkspaceArchived: 1, ID: "terminal-exited",
	})
	require.NoError(t, err)

	tabs := []*leapmuxv1.TabRef{
		{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "eligible"},
		{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "startup-failed"},
		{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "never-started"},
		{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "closed"},
		{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "terminal-exited"},
	}
	resumeAgentIDs, err := svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, tabs)
	require.NoError(t, err)
	resumer.Schedule(ctx, resumeAgentIDs)
	testutil.AssertEventually(t, func() bool { return len(recorder.ids()) == 1 }, "eligible unarchive resume")
	assert.Equal(t, []string{"eligible"}, recorder.ids())
	assert.False(t, svc.Terminals.HasTerminal("terminal-exited"), "unarchive never restarts a terminal shell")

	resumeAgentIDs, err = svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, tabs)
	require.NoError(t, err)
	resumer.Schedule(ctx, resumeAgentIDs)
	assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.Equal(collect, []string{"eligible"}, recorder.ids())
	}, 100*time.Millisecond, 10*time.Millisecond)
}

func TestWorkspaceArchive_DatabaseFailureStopsNoProcess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const agentID = "archive-db-failure"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: agentID, WorkingDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)
	t.Cleanup(func() { svc.Agents.StopAndWaitAgent(agentID) })
	_, err = svc.DB.ExecContext(ctx, `
CREATE TRIGGER fail_archive_update
BEFORE UPDATE OF workspace_archived ON agents
BEGIN
  SELECT RAISE(FAIL, 'forced archive failure');
END`)
	require.NoError(t, err)

	_, err = svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		[]*leapmuxv1.TabRef{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID}})

	require.Error(t, err)
	assert.True(t, svc.Agents.HasAgent(agentID),
		"a failed flag write must stop no process: the teardown runs only after the commit")
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Zero(t, row.WorkspaceArchived)
}

func TestWorkspaceArchive_InvalidRequestChangesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: "agent-1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))

	// The reconciler drops an unclassifiable row through archivableTabType
	// BEFORE it calls, so these refusals are a backstop for a future caller
	// that forgets that filter. Each must refuse before it writes anything.
	_, err := svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_UNSPECIFIED,
		[]*leapmuxv1.TabRef{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archive_state must be ACTIVE or ARCHIVED")

	_, err = svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		[]*leapmuxv1.TabRef{{TabType: leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, TabId: "agent-1"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unspecified type")

	row, err := svc.Queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Zero(t, row.WorkspaceArchived, "a refused request writes nothing")
}

// TestWorkspaceArchive_RefusesEveryWriteRPCOnAnArchivedTab pins the rule that
// moved from four hand-written per-handler guards into the registrars.
//
// The four handlers that carried a guard were the four the browser happened to
// call. Every OTHER write RPC ran, and some wrote state that outlives the
// archive, such as RenameAgent and UpdateTerminalTitle. A second client, the
// CLI, or any holder of the write scope reached all of them.
//
// It also asserts the other half of the rule: a READ stays reachable, which is
// what makes an archived workspace browsable rather than merely frozen.
func TestWorkspaceArchive_RefusesEveryWriteRPCOnAnArchivedTab(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
	const agentID = "archived-agent"
	const terminalID = "archived-terminal"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), Title: "before",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: terminalID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), Title: "before",
		Shell: testutil.TestShell(), Cols: 80, Rows: 24, Screen: []byte{},
	}))
	_, err := svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		[]*leapmuxv1.TabRef{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID},
			{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: terminalID},
		})
	require.NoError(t, err)

	refused := []struct {
		method string
		req    proto.Message
	}{
		{"RenameAgent", &leapmuxv1.RenameAgentRequest{AgentId: agentID, Title: "after"}},
		{"CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: agentID}},
		{"InterruptAgent", &leapmuxv1.InterruptAgentRequest{AgentId: agentID}},
		{"EnqueueAgentInput", &leapmuxv1.EnqueueAgentInputRequest{
			AgentId: agentID, InputId: "input-1", Text: "hi",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		}},
		{"BeginQueuedAgentInputEdit", &leapmuxv1.BeginQueuedAgentInputEditRequest{AgentId: agentID, InputId: "input-1", ClientId: "client-1"}},
		{"UpdateQueuedAgentInput", &leapmuxv1.UpdateQueuedAgentInputRequest{AgentId: agentID, InputId: "input-1", ClientId: "client-1", ExpectedVersion: 1, Text: "changed"}},
		{"CancelQueuedAgentInputEdit", &leapmuxv1.CancelQueuedAgentInputEditRequest{AgentId: agentID, InputId: "input-1", ClientId: "client-1"}},
		{"DeleteQueuedAgentInput", &leapmuxv1.DeleteQueuedAgentInputRequest{AgentId: agentID, InputId: "input-1"}},
		{"MoveQueuedAgentInput", &leapmuxv1.MoveQueuedAgentInputRequest{AgentId: agentID, InputId: "input-1"}},
		{"SetAgentInputQueuePaused", &leapmuxv1.SetAgentInputQueuePausedRequest{AgentId: agentID, Paused: true}},
		{"SteerQueuedAgentInput", &leapmuxv1.SteerQueuedAgentInputRequest{AgentId: agentID, InputId: "input-1"}},
		{"RetryQueuedAgentInput", &leapmuxv1.RetryQueuedAgentInputRequest{AgentId: agentID, InputId: "input-1"}},
		{"UpdateTerminalTitle", &leapmuxv1.UpdateTerminalTitleRequest{TerminalId: terminalID, Title: "after"}},
		{"CloseTerminal", &leapmuxv1.CloseTerminalRequest{TerminalId: terminalID}},
		{"SendInput", &leapmuxv1.SendInputRequest{TerminalId: terminalID, Data: []byte("x")}},
		{"ResizeTerminal", &leapmuxv1.ResizeTerminalRequest{TerminalId: terminalID, Cols: 10, Rows: 10}},
		{"RestartTerminal", &leapmuxv1.RestartTerminalRequest{TerminalId: terminalID}},
	}
	for _, tc := range refused {
		t.Run(tc.method, func(t *testing.T) {
			w := newTestWriter()
			dispatch(dispatcher, tc.method, tc.req, w)
			require.Len(t, w.errors, 1, "%s must answer an error while archived", tc.method)
			assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code,
				"%s must name the archive as the reason, not fail for some other cause", tc.method)
		})
	}

	// Nothing above stored anything.
	agentRow, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, "before", agentRow.Title, "a refused rename must not store a title")
	assert.False(t, agentRow.ClosedAt.Valid, "a refused close must not stamp closed_at")
	terminalRow, err := svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	assert.Equal(t, "before", terminalRow.Title, "a refused rename must not store a title")
	assert.False(t, terminalRow.ClosedAt.Valid, "a refused close must not stamp closed_at")

	// A READ stays reachable: the archive freezes mutation, not visibility.
	w := newTestWriter()
	dispatch(dispatcher, "ListAgentMessages", &leapmuxv1.ListAgentMessagesRequest{AgentId: agentID}, w)
	assert.Empty(t, w.errors, "an archived workspace must stay readable")
}

// TestWorkspaceArchive_SkipsAClosedRow pins the closed_at predicate on both
// archive UPDATEs.
//
// A closed row stays in these tables for the whole 7-day cleanup retention, and
// the caller treats every row the UPDATE changed as a tab to stop and broadcast
// for. Without the predicate an archive that races a close re-ran the full
// teardown for a tab the worker already tore down -- the same reason the orphan
// reconciler reads ListAllOpenRootAgentIDs -- and the matching unarchive spent
// a resume permit on it.
func TestWorkspaceArchive_SkipsAClosedRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const closedAgent = "closed-agent"
	const openAgent = "open-agent"
	for _, id := range []string{closedAgent, openAgent} {
		require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
			ID: id, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		}))
	}
	_, err := svc.Queries.CloseAgent(ctx, closedAgent)
	require.NoError(t, err)

	tabs := []*leapmuxv1.TabRef{
		{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: closedAgent},
		{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: openAgent},
	}
	_, err = svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, tabs)
	require.NoError(t, err)

	closedRow, err := svc.Queries.GetAgentByID(ctx, closedAgent)
	require.NoError(t, err)
	assert.Zero(t, closedRow.WorkspaceArchived, "a closed row takes no archive flag and no teardown")
	openRow, err := svc.Queries.GetAgentByID(ctx, openAgent)
	require.NoError(t, err)
	assert.Equal(t, int64(1), openRow.WorkspaceArchived)

	// The unarchive must not offer the closed row as a resume candidate.
	resumeAgentIDs, err := svc.ApplyTabArchiveState(ctx, leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, tabs)
	require.NoError(t, err)
	assert.Equal(t, []string{openAgent}, resumeAgentIDs,
		"a closed row must not spend a resume permit only to be skipped")
}

// TestWorkspaceArchive_UnarchiveWaitsForAnArchiveDrain pins the atomicity of
// one tab's lifecycle: the flag write and the teardown that follows it are ONE
// operation, not two.
//
// The failure it excludes: archive A commits workspace_archived=1 and starts
// draining. Unarchive B commits 0 and hands its agent id back for a resume. A's
// drain -- which re-reads nothing after its own commit -- then stops the
// process the resume just started, clears its runtime state, and broadcasts
// INACTIVE for a row that says active. The user watches an agent they just
// unarchived go inactive again, with no error anywhere.
//
// LockAgent is the seam that makes this deterministic. The archive drain takes
// it per agent, so holding it here parks A precisely mid-drain, with its flag
// already committed. B must then WAIT. Without the per-tab lock B runs straight
// through and answers with a resume id while A is still tearing that agent
// down, which is the race in one step.
func TestWorkspaceArchive_UnarchiveWaitsForAnArchiveDrain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	const agentID = "archive-drain-race"
	require.NoError(t, svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, Resumed: 1,
	}))
	_, err := svc.Agents.MockStartAgent(ctx, agent.Options{
		AgentID: agentID, WorkingDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE))
	require.NoError(t, err)

	tabs := []*leapmuxv1.TabRef{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID}}

	// Park the archive inside its drain, holding the agent's lifecycle lock.
	releaseAgent := svc.Agents.LockAgent(agentID)
	archiveDone := make(chan struct{})
	go func() {
		defer close(archiveDone)
		_, archiveErr := svc.ApplyTabArchiveState(ctx,
			leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED, tabs)
		assert.NoError(t, archiveErr)
	}()

	// The archive committed its flag and is now blocked in the drain.
	testutil.AssertEventually(t, func() bool {
		row, rowErr := svc.Queries.GetAgentByID(ctx, agentID)
		return rowErr == nil && row.WorkspaceArchived == 1
	}, "the archive commits its flag before it drains")

	unarchiveDone := make(chan []string, 1)
	go func() {
		resumeIDs, unarchiveErr := svc.ApplyTabArchiveState(ctx,
			leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, tabs)
		assert.NoError(t, unarchiveErr)
		unarchiveDone <- resumeIDs
	}()

	select {
	case <-unarchiveDone:
		require.FailNow(t, "the unarchive answered while the archive was still draining that same tab; "+
			"its resume would be stopped by the drain that is still running")
	case <-time.After(150 * time.Millisecond):
	}
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), row.WorkspaceArchived,
		"the unarchive must not commit its flag while the archive still owns the tab")

	// Let the drain finish; the unarchive then proceeds in full.
	releaseAgent()
	<-archiveDone
	select {
	case resumeIDs := <-unarchiveDone:
		assert.Equal(t, []string{agentID}, resumeIDs,
			"the unarchive that ran after the drain must offer the resume")
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the unarchive never completed after the archive drain returned")
	}
	settled, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Zero(t, settled.WorkspaceArchived)
	assert.False(t, settled.ClosedAt.Valid, "neither operation may close the tab")
	assert.Empty(t, settled.StartupError, "neither operation may mark the tab failed")
}
