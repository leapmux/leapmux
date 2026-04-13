package service

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

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

	require.Len(t, w.errors, 1)
	require.Equal(t, "failed to start agent: forced start failure", w.errors[0].message)
	assert.NoDirExists(t, worktreePath)
	assert.False(t, localBranchExists(t, repoDir, branchName))

	_, err := svc.Queries.GetWorktreeByPath(ctx, worktreePath)
	assert.ErrorIs(t, err, sql.ErrNoRows)
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

	require.Len(t, w.errors, 1)
	require.Equal(t, "failed to start agent: forced start failure", w.errors[0].message)
	assert.Equal(t, originalBranch, currentBranchName(t, repoDir))
	assert.False(t, localBranchExists(t, repoDir, branchName))
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

	require.Len(t, w.errors, 1)
	require.Equal(t, "failed to start agent: forced start failure", w.errors[0].message)
	assert.Equal(t, "HEAD", strings.TrimSpace(mustGitOutput(t, ctx, repoDir, "rev-parse", "--abbrev-ref", "HEAD")))
	assert.Equal(t, originalCommit, strings.TrimSpace(mustGitOutput(t, ctx, repoDir, "rev-parse", "HEAD")))
	assert.False(t, localBranchExists(t, repoDir, branchName))
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
		Shell:          "/bin/sh",
	}, w)

	require.Len(t, w.errors, 1)
	require.Equal(t, "failed to start terminal", w.errors[0].message)
	assert.NoDirExists(t, worktreePath)
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
		Shell:        "/bin/sh",
	}, w)

	require.Len(t, w.errors, 1)
	require.Equal(t, "failed to start terminal", w.errors[0].message)
	assert.Equal(t, originalBranch, currentBranchName(t, repoDir))
	assert.False(t, localBranchExists(t, repoDir, branchName))
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

func TestOpenAgent_RollsBackCreatedWorktreeOnCreateRecordFailure(t *testing.T) {
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

func TestOpenAgent_RollsBackCreatedBranchOnCreateRecordFailure(t *testing.T) {
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
		Shell:          "/bin/sh",
	}, w)

	require.Len(t, w.errors, 1)
	require.Equal(t, "failed to start terminal", w.errors[0].message)
	assert.Equal(t, "feature/existing", currentBranchName(t, repoDir))
}
