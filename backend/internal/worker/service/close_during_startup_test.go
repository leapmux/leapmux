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
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
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
	svc.startAgentFn = func(sCtx context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
		closeOnce.Do(func() {
			startEntered <- opts.AgentID
			// Subscribe here — by this point the DB row exists, so
			// WatchEvents accepts the subscription.
			dispatch(d, "WatchEvents", &leapmuxv1.WatchEventsRequest{
				Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: opts.AgentID, Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_LATEST, Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
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
	svc.startAgentFn = func(sCtx context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
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
	svc.startAgentFn = func(sCtx context.Context, opts agent.Options, _ agent.OutputSink) (map[string]string, error) {
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
			svc.AgentStartup.finish()
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
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID: agentID, WorkingDir: workingDir, HomeDir: workingDir,
	}))
	row, err := svc.getAgentByID(context.Background(), agentID)
	require.NoError(t, err)
	return row
}
