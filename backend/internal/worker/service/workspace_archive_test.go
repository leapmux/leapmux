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
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"google.golang.org/grpc/codes"
)

func archiveRequest(state leapmuxv1.WorkspaceArchiveState, tabs ...*leapmuxv1.TabRef) *leapmuxv1.SetTabArchiveStateRequest {
	return &leapmuxv1.SetTabArchiveStateRequest{ArchiveState: state, Tabs: tabs}
}

func TestWorkspaceArchive_StopsProcessesAndPreservesTabData(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, dispatcher, _ := setupTestService(t)
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

	w := newTestWriter()
	dispatch(dispatcher, "SetTabArchiveState", archiveRequest(
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		&leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID},
		&leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: terminalID},
		&leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "archive-file"},
	), w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
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

func TestWorkspaceArchive_UnarchiveResumesOnlyEligibleAgents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	recorder := newStartRecorder()
	recorder.install(svc)
	resumer := svc.NewAgentResumer()
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
	resumeAgentIDs, err := svc.applyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ACTIVE, tabs)
	require.NoError(t, err)
	resumer.Schedule(ctx, resumeAgentIDs)
	testutil.AssertEventually(t, func() bool { return len(recorder.ids()) == 1 }, "eligible unarchive resume")
	assert.Equal(t, []string{"eligible"}, recorder.ids())
	assert.False(t, svc.Terminals.HasTerminal("terminal-exited"), "unarchive never restarts a terminal shell")

	resumeAgentIDs, err = svc.applyTabArchiveState(ctx,
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
	svc, dispatcher, _ := setupTestService(t)
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

	w := newTestWriter()
	dispatch(dispatcher, "SetTabArchiveState", archiveRequest(
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		&leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: agentID},
	), w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.Internal), w.errors[0].code)
	assert.True(t, svc.Agents.HasAgent(agentID))
	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.Zero(t, row.WorkspaceArchived)
}

func TestWorkspaceArchive_InvalidStateChangesNothing(t *testing.T) {
	t.Parallel()

	svc, dispatcher, _ := setupTestService(t)
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID: "agent-1", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}))
	w := newTestWriter()
	dispatch(dispatcher, "SetTabArchiveState", archiveRequest(
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_UNSPECIFIED,
		&leapmuxv1.TabRef{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"},
	), w)
	require.Len(t, w.errors, 1)
	assert.Equal(t, codeInvalidArgument, w.errors[0].code)
	row, err := svc.Queries.GetAgentByID(context.Background(), "agent-1")
	require.NoError(t, err)
	assert.Zero(t, row.WorkspaceArchived)
}
