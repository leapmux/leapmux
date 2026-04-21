package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// waitForStartupFailure blocks until the agent or terminal startup
// registry reports STARTUP_FAILED for id, so tests can synchronize on
// the async startup goroutine's post-rollback work before asserting.
func waitForStartupFailure(t *testing.T, svc *Context, id string) {
	t.Helper()
	testutil.RequireEventually(t, func() bool {
		if status, _, _, ok := svc.AgentStartup.status(id); ok && status == leapmuxv1.AgentStatus_AGENT_STATUS_STARTUP_FAILED {
			return true
		}
		if status, _, _, ok := svc.TerminalStartup.status(id); ok && status == leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED {
			return true
		}
		return false
	}, "startup never reached STARTUP_FAILED for id=%s", id)
}

func TestOpenAgent_RollsBackCreatedWorktreeOnStartFailure(t *testing.T) {
	ctx := context.Background()
	repoDir := initRepo(t)
	branchName := "feature/agent-worktree"
	worktreePath := expectedWorktreePath(repoDir, branchName)

	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return nil, errors.New("forced start failure")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
	}, w)

	require.Empty(t, w.errors)
	// waitForStartupFailure syncs on AgentStartup.fail(), which the runAgentStartup
	// goroutine calls AFTER rollbackGitMode completes (worktree remove + branch -D).
	// Assert on git/DB state only once that's done — otherwise `git branch -D` may
	// still be running when the earlier `directoryExists == false` poll fires.
	for _, id := range collectAgentIDs(w) {
		waitForStartupFailure(t, svc, id)
	}
	assert.False(t, directoryExists(worktreePath))
	assert.False(t, localBranchExists(t, repoDir, branchName))

	_, err := svc.Queries.GetWorktreeByPath(ctx, worktreePath)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func directoryExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestOpenAgent_RollsBackCreatedBranchOnStartFailure(t *testing.T) {
	repoDir := initRepo(t)
	originalBranch := currentBranchName(t, repoDir)
	branchName := "feature/agent-branch"

	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return nil, errors.New("forced start failure")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:  "ws-1",
		WorkingDir:   repoDir,
		CreateBranch: branchName,
	}, w)

	require.Empty(t, w.errors)
	require.Eventually(t, func() bool {
		return currentBranchName(t, repoDir) == originalBranch && !localBranchExists(t, repoDir, branchName)
	}, 5*time.Second, 20*time.Millisecond)
	for _, id := range collectAgentIDs(w) {
		waitForStartupFailure(t, svc, id)
	}
}

func TestOpenAgent_RollsBackCreatedBranchToDetachedHEADOnStartFailure(t *testing.T) {
	ctx := context.Background()
	repoDir := initRepo(t)
	originalCommit := strings.TrimSpace(mustGitOutput(t, ctx, repoDir, "rev-parse", "HEAD"))
	run(t, repoDir, "git", "checkout", "--detach", "HEAD")
	branchName := "feature/detached-rollback"

	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.startAgentFn = func(context.Context, agent.Options, agent.OutputSink) (*leapmuxv1.AgentSettings, error) {
		return nil, errors.New("forced start failure")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:  "ws-1",
		WorkingDir:   repoDir,
		CreateBranch: branchName,
	}, w)

	require.Empty(t, w.errors)
	require.Eventually(t, func() bool {
		head := strings.TrimSpace(mustGitOutput(t, ctx, repoDir, "rev-parse", "--abbrev-ref", "HEAD"))
		commit := strings.TrimSpace(mustGitOutput(t, ctx, repoDir, "rev-parse", "HEAD"))
		return head == "HEAD" && commit == originalCommit && !localBranchExists(t, repoDir, branchName)
	}, 5*time.Second, 20*time.Millisecond)
	for _, id := range collectAgentIDs(w) {
		waitForStartupFailure(t, svc, id)
	}
}

func TestOpenTerminal_RollsBackCreatedWorktreeOnStartFailure(t *testing.T) {
	ctx := context.Background()
	repoDir := initRepo(t)
	branchName := "feature/terminal-worktree"
	worktreePath := expectedWorktreePath(repoDir, branchName)

	svc, d, w := setupTestService(t, "ws-1")
	svc.startTerminalFn = func(terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		return errors.New("forced start failure")
	}

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
		Shell:          testutil.TestShell(),
	}, w)

	require.Empty(t, w.errors)
	// See TestOpenAgent_RollsBackCreatedWorktreeOnStartFailure for why the
	// wait-for-fail synchronization precedes the git/DB asserts.
	for _, id := range collectTerminalIDs(w) {
		waitForStartupFailure(t, svc, id)
	}
	assert.False(t, directoryExists(worktreePath))
	assert.False(t, localBranchExists(t, repoDir, branchName))

	_, err := svc.Queries.GetWorktreeByPath(ctx, worktreePath)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestOpenTerminal_RollsBackCreatedBranchOnStartFailure(t *testing.T) {
	repoDir := initRepo(t)
	originalBranch := currentBranchName(t, repoDir)
	branchName := "feature/terminal-branch"

	svc, d, w := setupTestService(t, "ws-1")
	svc.startTerminalFn = func(terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		return errors.New("forced start failure")
	}

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId:  "ws-1",
		WorkingDir:   repoDir,
		CreateBranch: branchName,
		Shell:        testutil.TestShell(),
	}, w)

	require.Empty(t, w.errors)
	require.Eventually(t, func() bool {
		return currentBranchName(t, repoDir) == originalBranch && !localBranchExists(t, repoDir, branchName)
	}, 5*time.Second, 20*time.Millisecond)
	for _, id := range collectTerminalIDs(w) {
		waitForStartupFailure(t, svc, id)
	}
}

func expectedWorktreePath(repoDir, branchName string) string {
	return filepath.Join(filepath.Dir(repoDir), filepath.Base(repoDir)+"-worktrees", branchName)
}

func currentBranchName(t *testing.T, repoDir string) string {
	t.Helper()
	out := mustGitOutput(t, context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	return strings.TrimSpace(out)
}

func localBranchExists(t *testing.T, repoDir, branchName string) bool {
	t.Helper()
	_, err := gitOutput(context.Background(), repoDir, "rev-parse", "--verify", "refs/heads/"+branchName)
	return err == nil
}

func mustGitOutput(t *testing.T, ctx context.Context, repoDir string, args ...string) string {
	t.Helper()
	out, err := gitOutput(ctx, repoDir, args...)
	require.NoError(t, err)
	return out
}

// TestOpenAgent_NoWorktreeMutationOnCreateRecordFailure verifies that a
// createAgentRecord failure aborts before any git mutation. Since validate
// + DB write both run synchronously in OpenAgent and the worktree/branch
// creation lives in runAgentStartup, a sync failure must leave the repo
// untouched — there is literally nothing to roll back.
func TestOpenAgent_NoWorktreeMutationOnCreateRecordFailure(t *testing.T) {
	ctx := context.Background()
	repoDir := initRepo(t)
	branchName := "feature/agent-create-failure"
	worktreePath := expectedWorktreePath(repoDir, branchName)

	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.createAgentRecordFn = func(context.Context, db.CreateAgentParams) error {
		return errors.New("forced create failure")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
	}, w)

	require.Len(t, w.errors, 1)
	require.Equal(t, "failed to create agent", w.errors[0].message)
	assert.NoDirExists(t, worktreePath)
	assert.False(t, localBranchExists(t, repoDir, branchName))

	_, err := svc.Queries.GetWorktreeByPath(ctx, worktreePath)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

// TestOpenAgent_NoBranchMutationOnCreateRecordFailure is the create-branch
// analogue: a failing createAgentRecord returns before `git checkout -b`
// runs, so the repo's HEAD and branch set are unchanged.
func TestOpenAgent_NoBranchMutationOnCreateRecordFailure(t *testing.T) {
	repoDir := initRepo(t)
	originalBranch := currentBranchName(t, repoDir)
	branchName := "feature/agent-create-branch"

	svc, d, w := setupTestService(t, "ws-1")
	svc.Output = NewOutputHandler(svc.Queries, svc.Watchers, svc.Agents, nil)
	svc.createAgentRecordFn = func(context.Context, db.CreateAgentParams) error {
		return errors.New("forced create failure")
	}

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:  "ws-1",
		WorkingDir:   repoDir,
		CreateBranch: branchName,
	}, w)

	require.Len(t, w.errors, 1)
	require.Equal(t, "failed to create agent", w.errors[0].message)
	assert.Equal(t, originalBranch, currentBranchName(t, repoDir))
	assert.False(t, localBranchExists(t, repoDir, branchName))
}

func TestOpenTerminal_DoesNotRollBackSwitchBranchOnStartFailure(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/existing")
	run(t, repoDir, "git", "checkout", "-")

	svc, d, w := setupTestService(t, "ws-1")
	svc.startTerminalFn = func(terminal.Options, terminal.OutputHandler, terminal.ExitHandler) error {
		return errors.New("forced start failure")
	}

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CheckoutBranch: "feature/existing",
		Shell:          testutil.TestShell(),
	}, w)

	require.Empty(t, w.errors)
	// Switching to an existing branch should NOT be rolled back on failure.
	// Poll for the async startup to complete, then verify branch unchanged.
	for _, id := range collectTerminalIDs(w) {
		waitForStartupFailure(t, svc, id)
	}
	assert.Equal(t, "feature/existing", currentBranchName(t, repoDir))
}

// collectTerminalIDs extracts terminal_id values from OpenTerminal
// responses captured on the test writer.
func collectTerminalIDs(w *testResponseWriter) []string {
	var ids []string
	for _, r := range w.responses {
		var resp leapmuxv1.OpenTerminalResponse
		if err := proto.Unmarshal(r.Payload, &resp); err == nil && resp.TerminalId != "" {
			ids = append(ids, resp.TerminalId)
		}
	}
	return ids
}

// collectAgentIDs extracts agent ids from OpenAgent responses on the
// test writer.
func collectAgentIDs(w *testResponseWriter) []string {
	var ids []string
	for _, r := range w.responses {
		var resp leapmuxv1.OpenAgentResponse
		if err := proto.Unmarshal(r.Payload, &resp); err == nil && resp.GetAgent().GetId() != "" {
			ids = append(ids, resp.GetAgent().GetId())
		}
	}
	return ids
}
