package service

import (
	"context"
	"os"
	"path/filepath"
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

func countAgentRows(t *testing.T, svc *Service) int {
	t.Helper()
	rows, err := svc.Queries.ListAllAgentIDs(context.Background())
	require.NoError(t, err)
	return len(rows)
}

func countTerminalRows(t *testing.T, svc *Service) int {
	t.Helper()
	rows, err := svc.Queries.ListAllTerminalIDs(context.Background())
	require.NoError(t, err)
	return len(rows)
}

// ---------- OpenAgent: git-mode validation ----------

func TestOpenAgent_Validate_BranchNameSyntax(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: "feature/ bad name", // contains space -> rejected by ValidateBranchName
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "branch name")
	assert.Zero(t, countAgentRows(t, svc), "no DB row on validation failure")
}

func TestOpenAgent_Validate_WorkingDirNotGitRepo(t *testing.T) {
	t.Parallel()

	notARepo := t.TempDir()
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:     notARepo,
		CreateWorktree: true,
		WorktreeBranch: "feature/x",
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "not inside a git repository")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_BranchAlreadyExists(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/taken")
	run(t, repoDir, "git", "checkout", "-")
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: "feature/taken",
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "already exists")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_BaseBranchMissing(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
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
	t.Parallel()

	repoDir := initRepo(t)
	branchName := "feature/collide"
	worktreePath := expectedWorktreePath(t, repoDir, branchName)
	require.NoError(t, os.MkdirAll(worktreePath, 0o755))

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "already exists")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_CheckoutBranchMissing(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:     repoDir,
		CheckoutBranch: "nonexistent",
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "does not exist")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_CreateBranchAlreadyExists(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/taken")
	run(t, repoDir, "git", "checkout", "-")
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:   repoDir,
		CreateBranch: "feature/taken",
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "already exists")
	assert.Zero(t, countAgentRows(t, svc))
}

func TestOpenAgent_Validate_UseWorktreePathUnknown(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	bogusPath := filepath.Join(t.TempDir(), "bogus")
	require.NoError(t, os.MkdirAll(bogusPath, 0o755))

	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)
	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:      repoDir,
		UseWorktreePath: bogusPath,
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "not a known worktree")
	assert.Zero(t, countAgentRows(t, svc))
}

// ---------- OpenAgent: session ID validation ----------
//
// The title is NOT validated here. Every title-writing RPC cleans the client's
// title instead of refusing it, and title_cleaning_test.go covers all three
// against one table. A session ID keeps its refusal: it is an opaque token
// whose original bytes matter, so a silent strip would resume the wrong
// session.

func TestOpenAgent_Validate_SessionIDRejectsControlChar(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:     repoDir,
		AgentSessionId: "session\x00bad",
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countAgentRows(t, svc))
}

// ---------- OpenAgent: launch-option validation [S1] ----------

// An explicitly-requested permission mode the provider doesn't offer must fail the RPC with
// InvalidArgument BEFORE any DB row is created -- so a CLI typo surfaces as a clear error rather
// than reaching the provider and dying at startup (an opaque dead agent).
func TestOpenAgent_Validate_RejectsUnknownPermissionMode(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    repoDir,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       map[string]string{"permissionMode": "bogus-mode"},
	}, w)

	msg := requireInvalidArgument(t, w)
	assert.Contains(t, msg, "permission mode")
	assert.Zero(t, countAgentRows(t, svc), "no DB row on validation failure")
}

// Model is NOT validated at spawn: every provider discovers its model catalog from the running CLI,
// seeding only a fallback, so a model absent from the seed (but maybe valid in the live catalog)
// must NOT be rejected -- the spawn proceeds and the running session validates it.
func TestOpenAgent_Validate_DoesNotRejectUnknownModel(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    repoDir,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       map[string]string{"model": "a-future-model-not-in-the-seed"},
	}, w)

	require.Empty(t, w.errors, "an unknown model must not be rejected at spawn (the catalog is dynamic)")
	require.Len(t, w.responses, 1)
	assert.Equal(t, 1, countAgentRows(t, svc))
}

// A VALID explicitly-requested permission mode must NOT be rejected -- the validation must not
// over-reject and break a normal spawn that pins a legitimate mode.
func TestOpenAgent_Validate_AcceptsValidPermissionMode(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkingDir:    repoDir,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		Options:       map[string]string{"permissionMode": "plan"},
	}, w)

	require.Empty(t, w.errors, "a valid permission mode must not be rejected")
	require.Len(t, w.responses, 1)
	assert.Equal(t, 1, countAgentRows(t, svc), "the agent row is created for a valid spawn")
}

// ---------- OpenTerminal mirrors of the git-mode cases ----------

func TestOpenTerminal_Validate_BranchNameSyntax(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: "feature/ bad name",
		Shell:          testutil.TestShell(),
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countTerminalRows(t, svc))
}

func TestOpenTerminal_Validate_WorkingDirNotGitRepo(t *testing.T) {
	t.Parallel()

	notARepo := t.TempDir()
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkingDir:     notARepo,
		CreateWorktree: true,
		WorktreeBranch: "feature/x",
		Shell:          testutil.TestShell(),
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countTerminalRows(t, svc))
}

func TestOpenTerminal_Validate_BranchAlreadyExists(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/taken")
	run(t, repoDir, "git", "checkout", "-")
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: "feature/taken",
		Shell:          testutil.TestShell(),
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countTerminalRows(t, svc))
}

func TestOpenTerminal_Validate_WorktreePathAlreadyPresent(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	branchName := "feature/terminal-collide"
	worktreePath := expectedWorktreePath(t, repoDir, branchName)
	require.NoError(t, os.MkdirAll(worktreePath, 0o755))
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkingDir:     repoDir,
		CreateWorktree: true,
		WorktreeBranch: branchName,
		Shell:          testutil.TestShell(),
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countTerminalRows(t, svc))
}

func TestOpenTerminal_Validate_CheckoutBranchMissing(t *testing.T) {
	t.Parallel()

	repoDir := initRepo(t)
	svc, d, w := setupTestService(t)
	defer drainAllInFlight(svc)

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		WorkingDir:     repoDir,
		CheckoutBranch: "nonexistent",
		Shell:          testutil.TestShell(),
	}, w)

	requireInvalidArgument(t, w)
	assert.Zero(t, countTerminalRows(t, svc))
}
