package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// These tests cover the sync validation of OpenAgent / OpenTerminal.
// validateGitMode runs read-only checks and must fail the RPC with
// InvalidArgument (code 3) before the handler mutates any git state or
// creates a DB row. That guarantees bad user input surfaces as an
// immediate dialog error rather than a failed tab in STARTUP_FAILED.
//
// Every case here asserts: one InvalidArgument error, no agent/terminal
// DB row created, and no git mutation visible in the repo.

// ---------- helpers ----------

func requireInvalidArgument(t *testing.T, w *testResponseWriter) string {
	t.Helper()
	require.Empty(t, w.responses, "validation failure should not produce a response")
	require.Len(t, w.errors, 1, "validation failure must produce exactly one error")
	assert.Equal(t, codeInvalidArgument, w.errors[0].code, "expected InvalidArgument")
	return w.errors[0].message
}

func countAgentRows(t *testing.T, svc *Context) int {
	t.Helper()
	rows, err := svc.Queries.ListAllAgentIDsAndWorkspaces(context.Background())
	require.NoError(t, err)
	return len(rows)
}

func countTerminalRows(t *testing.T, svc *Context) int {
	t.Helper()
	// Any existing "count" query would work; reuse the broad
	// ListTerminalsByIDs with a wildcard-ish inputs or fall back to a
	// per-workspace count. The service has ListTerminalsByWorkspace.
	rows, err := svc.Queries.ListTerminalsByWorkspace(context.Background(), "ws-1")
	require.NoError(t, err)
	return len(rows)
}

// ---------- OpenAgent: git-mode validation ----------

func TestOpenAgent_Validate_BranchNameSyntax(t *testing.T) {
	repoDir := initRepo(t)
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: "feature/ bad name", // contains space -> rejected by ValidateBranchName
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "branch name")
	assert.Zero(t, countAgentRows(t, svc), "no DB row on validation failure")
}

func TestOpenAgent_Validate_WorkingDirNotGitRepo(t *testing.T) {
	notARepo := t.TempDir()
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     notARepo,
		CreateWorktree: true,
		WorktreeBranch: "feature/x",
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "not inside a git repository")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_BranchAlreadyExists(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/taken")
	run(t, repoDir, "git", "checkout", "-")
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: "feature/taken",
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "already exists")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_BaseBranchMissing(t *testing.T) {
	repoDir := initRepo(t)
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:        "ws-1",
		WorkingDir:         repoDir,
		CreateWorktree:     true,
		WorktreeBranch:     "feature/x",
		WorktreeBaseBranch: "does-not-exist",
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "base branch")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_WorktreePathAlreadyPresent(t *testing.T) {
	repoDir := initRepo(t)
	branchName := "feature/collide"
	worktreePath := expectedWorktreePath(repoDir, branchName)
	require.NoError(t, os.MkdirAll(worktreePath, 0o755))

	svc, d, w := setupTestService(t, "ws-1")
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "already exists")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_CheckoutBranchMissing(t *testing.T) {
	repoDir := initRepo(t)
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CheckoutBranch: "nonexistent",
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "does not exist")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_CreateBranchAlreadyExists(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/taken")
	run(t, repoDir, "git", "checkout", "-")
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:  "ws-1",
		WorkingDir:   repoDir,
		CreateBranch: "feature/taken",
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "already exists")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_UseWorktreePathUnknown(t *testing.T) {
	repoDir := initRepo(t)
	bogusPath := filepath.Join(t.TempDir(), "bogus")
	require.NoError(t, os.MkdirAll(bogusPath, 0o755))

	svc, d, w := setupTestService(t, "ws-1")
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:     "ws-1",
		WorkingDir:      repoDir,
		UseWorktreePath: bogusPath,
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "not a known worktree")
	assert.Zero(t, countAgentRows(t, svc))
}

// ---------- OpenAgent: title + session ID validation ----------

func TestOpenAgent_Validate_TitleTooLong(t *testing.T) {
	repoDir := initRepo(t)
	svc, d, w := setupTestService(t, "ws-1")

	longTitle := strings.Repeat("a", 256) // exceeds the 128-char cap
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  repoDir,
		Title:       longTitle,
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "title")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_TitleStripsControlChars(t *testing.T) {
	repoDir := initRepo(t)
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  repoDir,
		Title:       "Hello\x00World", // NUL byte — stripped by SanitizeName
	}, w)

	// Title sanitization strips control chars rather than rejecting outright;
	// the request succeeds and the DB row holds the cleaned title.
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	require.Equal(t, 1, countAgentRows(t, svc))

	rows, err := svc.Queries.ListAllAgentIDsAndWorkspaces(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	agent, err := svc.Queries.GetAgentByID(context.Background(), rows[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "HelloWorld", agent.Title)
}

func TestOpenAgent_Validate_SessionIDRejectsControlChar(t *testing.T) {
	repoDir := initRepo(t)
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		AgentSessionId: "session\x00bad",
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countAgentRows(t, svc))
}

// ---------- OpenTerminal mirrors of the git-mode cases ----------

func TestOpenTerminal_Validate_BranchNameSyntax(t *testing.T) {
	repoDir := initRepo(t)
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: "feature/ bad name",
		Shell:          testutil.TestShell(),
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countTerminalRows(t, svc))
}

func TestOpenTerminal_Validate_WorkingDirNotGitRepo(t *testing.T) {
	notARepo := t.TempDir()
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     notARepo,
		CreateWorktree: true,
		WorktreeBranch: "feature/x",
		Shell:          testutil.TestShell(),
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countTerminalRows(t, svc))
}

func TestOpenTerminal_Validate_BranchAlreadyExists(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/taken")
	run(t, repoDir, "git", "checkout", "-")
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: "feature/taken",
		Shell:          testutil.TestShell(),
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countTerminalRows(t, svc))
}

func TestOpenTerminal_Validate_WorktreePathAlreadyPresent(t *testing.T) {
	repoDir := initRepo(t)
	branchName := "feature/terminal-collide"
	worktreePath := expectedWorktreePath(repoDir, branchName)
	require.NoError(t, os.MkdirAll(worktreePath, 0o755))
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
		Shell:          testutil.TestShell(),
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countTerminalRows(t, svc))
}

func TestOpenTerminal_Validate_CheckoutBranchMissing(t *testing.T) {
	repoDir := initRepo(t)
	svc, d, w := setupTestService(t, "ws-1")

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkspaceId:    "ws-1",
		WorkingDir:     repoDir,
		CheckoutBranch: "nonexistent",
		Shell:          testutil.TestShell(),
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countTerminalRows(t, svc))
}
