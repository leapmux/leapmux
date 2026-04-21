package service

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

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
	ctx := context.Background()
	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	// Subscribe before OpenAgent so an accidental ACTIVE broadcast would
	// be captured regardless of where in the sequence it fires.
	wWatch := &testResponseWriter{channelID: "test-ch"}

	var (
		closeOnce    sync.Once
		startEntered = make(chan string, 1)
	)
	svc.startAgentFn = func(sCtx context.Context, opts agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		closeOnce.Do(func() {
			startEntered <- opts.AgentID
			// Subscribe here — by this point the DB row exists, so
			// WatchEvents accepts the subscription.
			dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
				Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: opts.AgentID, AfterSeq: 0}},
			}, wWatch)

			// Drive CloseAgent synchronously. dispatch returns only
			// after the full handler runs, so when control comes back
			// here the ctx is cancelled and closed_at is set in the DB.
			wClose := &testResponseWriter{channelID: "test-ch"}
			dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: opts.AgentID}, wClose)
		})
		<-sCtx.Done()
		return nil, sCtx.Err()
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:   "ws-1",
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

// TestCloseAgent_DuringStartup_RollsBackCreatedWorktree extends the
// close-detection test to the git-mode path: phase 0 created a worktree
// and branch before phase 2 parked; CloseAgent lands mid-phase-2; the
// post-spawn close-detection branch must call rollbackGitMode so the
// worktree directory and branch are removed along with the tab.
func TestCloseAgent_DuringStartup_RollsBackCreatedWorktree(t *testing.T) {
	ctx := context.Background()
	repoDir := initRepo(t)
	branchName := "feature/close-during-startup"
	worktreePath := expectedWorktreePath(repoDir, branchName)

	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)

	var closeOnce sync.Once
	svc.startAgentFn = func(sCtx context.Context, opts agent.Options, _ agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		closeOnce.Do(func() {
			// Worktree must exist by the time we get here — phase 0
			// ran to completion before phase 2 was entered.
			require.DirExists(t, worktreePath)
			require.True(t, localBranchExists(t, repoDir, branchName))

			wClose := &testResponseWriter{channelID: "test-ch"}
			dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: opts.AgentID}, wClose)
		})
		<-sCtx.Done()
		return nil, sCtx.Err()
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
		AgentProvider:  leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var openResp leapmuxv1.OpenAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &openResp))
	agentID := openResp.GetAgent().GetId()

	// Rollback happens in the goroutine after startAgentFn returns, so
	// poll for the filesystem + DB side effects under Eventually rather
	// than assuming the goroutine finished by the time dispatch returned.
	require.Eventually(t, func() bool {
		if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
			return false
		}
		if localBranchExists(t, repoDir, branchName) {
			return false
		}
		if _, err := svc.Queries.GetWorktreeByPath(ctx, worktreePath); err != sql.ErrNoRows {
			return false
		}
		_, _, _, registered := svc.AgentStartup.status(agentID)
		return !registered
	}, 5*time.Second, 20*time.Millisecond,
		"close-during-startup must remove worktree, branch, and worktree DB row")

	row, err := svc.Queries.GetAgentByID(ctx, agentID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid)
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
func TestCloseTerminal_DuringStartup_SuppressesReadyAndCleansUp(t *testing.T) {
	ctx := context.Background()
	repoDir := initRepo(t)
	branchName := "feature/close-term-during-startup"
	worktreePath := expectedWorktreePath(repoDir, branchName)

	svc, d, w := setupTestService(t, "ws-1")

	wWatch := &testResponseWriter{channelID: "test-ch"}

	var closeOnce sync.Once
	svc.startTerminalFn = func(sCtx context.Context, opts terminal.Options, _ terminal.OutputHandler, _ terminal.ExitHandler) error {
		closeOnce.Do(func() {
			// Worktree and branch were created in phase 0 before we got here.
			require.DirExists(t, worktreePath)
			require.True(t, localBranchExists(t, repoDir, branchName))

			dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
				TerminalIds: []string{opts.ID},
			}, wWatch)

			wClose := &testResponseWriter{channelID: "test-ch"}
			dispatch(d, "CloseTerminal", &leapmuxv1.CloseTerminalRequest{TerminalId: opts.ID}, wClose)
		})
		// Return sCtx.Err() to simulate "spawn aborted" — exercises the
		// close-detected branch with startErr != nil. The branch must
		// still suppress READY and roll back the worktree.
		return sCtx.Err()
	}

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId:    "ws-1",
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
	// returns; poll for all observable side effects.
	require.Eventually(t, func() bool {
		if _, statErr := os.Stat(worktreePath); !os.IsNotExist(statErr) {
			return false
		}
		if localBranchExists(t, repoDir, branchName) {
			return false
		}
		if _, err := svc.Queries.GetWorktreeByPath(ctx, worktreePath); err != sql.ErrNoRows {
			return false
		}
		row, err := svc.Queries.GetTerminal(ctx, terminalID)
		if err != nil || !row.ClosedAt.Valid {
			return false
		}
		_, _, _, registered := svc.TerminalStartup.status(terminalID)
		return !registered && !svc.Terminals.HasTerminal(terminalID)
	}, 5*time.Second, 20*time.Millisecond,
		"close-during-terminal-startup must roll back worktree, close DB row, clear registry, and drop PTY")

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
