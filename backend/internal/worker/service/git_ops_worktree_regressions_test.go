package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

func TestGitOps_SanitizeUTF8TruncatedPreservesValidTailAndRespectsBudget(t *testing.T) {
	t.Parallel()

	// TRAPS-9: Invalid earlier UTF-8 byte must not strip valid trailing ASCII or rune.
	assert.Equal(t, "\uFFFDabc", sanitizeUTF8Truncated([]byte("\xffabc"), true))
	assert.Equal(t, "\uFFFD😀", sanitizeUTF8Truncated([]byte("\xff😀"), true))

	// Valid mid-rune split at end of truncated buffer must trim only the incomplete tail.
	full := []byte("prefix-가나다")
	// "가" is 3 bytes (EAB080); cut 1 byte into "다".
	cut := full[:len(full)-2]
	assert.Equal(t, "prefix-가나", sanitizeUTF8Truncated(cut, true))

	// SWEEP-3: Expansion of invalid UTF-8 bytes to 3-byte U+FFFD must stay within budget.
	expanded, trunc := sanitizeUTF8TruncatedWithinBudget([]byte("\xffa\xffb\xffc"), false, 8)
	assert.True(t, trunc)
	assert.LessOrEqual(t, len(expanded), 8)
	assert.Equal(t, "\uFFFDa\uFFFDb", expanded)
}

func TestGitOps_CommitSanitizesNonUTF8SubjectAndCreateWorktreeLiteralPath(t *testing.T) {
	t.Parallel()
	_, d, _ := setupTestService(t)
	repo := testutil.NewGitRepo(t)
	ctx := context.Background()

	// SCAN-12: Configure git log output encoding to ISO-8859-1 so git log emits
	// non-UTF-8 bytes for a commit subject containing "café".
	require.NoError(t, gitutil.Run(ctx, repo, "config", "i18n.logOutputEncoding", "ISO-8859-1"))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "note.txt"), []byte("hello\n"), 0o644))

	wCommit := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerGitServiceGitCommitProcedure, &leapmuxv1.GitCommitRequest{
		Path:     repo,
		Message:  "café commit",
		StageAll: true,
	}, wCommit)
	require.Empty(t, wCommit.errors, "non-UTF-8 git log subject must be sanitized instead of failing proto marshal")
	var commitResp leapmuxv1.GitCommitResponse
	require.NoError(t, proto.Unmarshal(wCommit.responses[0].GetPayload(), &commitResp))
	assert.NotEmpty(t, commitResp.GetHash())
	assert.NotEmpty(t, commitResp.GetSubject())

	// SCAN-11: Whitespace-only WorktreePath must be rejected, not trimmed to empty default.
	wSpace := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerGitServiceCreateGitWorktreeProcedure, &leapmuxv1.CreateGitWorktreeRequest{
		Path:         repo,
		Branch:       "feat-space",
		WorktreePath: "   ",
	}, wSpace)
	require.Len(t, wSpace.errors, 1, "whitespace-only worktree_path must not fall back to default path")
}

func TestGitOps_CreateWorktreeBaseCommitResolvesFromLinkedWorktree(t *testing.T) {
	t.Parallel()
	_, d, _ := setupTestService(t)
	repo := testutil.NewGitRepo(t)
	ctx := context.Background()

	// Create a linked worktree with two extra commits so linked HEAD~1 differs from main HEAD.
	wtA := filepath.Join(t.TempDir(), "wt-a")
	require.NoError(t, gitutil.Run(ctx, repo, "worktree", "add", "-b", "branch-a", wtA))
	require.NoError(t, os.WriteFile(filepath.Join(wtA, "a1.txt"), []byte("one\n"), 0o644))
	require.NoError(t, gitutil.Run(ctx, wtA, "add", "a1.txt"))
	require.NoError(t, gitutil.Run(ctx, wtA, "commit", "-m", "a1"))
	wantCommit, err := gitutil.Output(ctx, wtA, "rev-parse", "HEAD")
	require.NoError(t, err)
	wantCommit = strings.TrimSpace(wantCommit)

	require.NoError(t, os.WriteFile(filepath.Join(wtA, "a2.txt"), []byte("two\n"), 0o644))
	require.NoError(t, gitutil.Run(ctx, wtA, "add", "a2.txt"))
	require.NoError(t, gitutil.Run(ctx, wtA, "commit", "-m", "a2"))

	// RIPPLE-2: Creating a worktree from wtA with BaseBranch="HEAD~1" must resolve
	// HEAD~1 relative to wtA (a1), not fail against main repo (which has only 1 commit).
	wtB := filepath.Join(t.TempDir(), "wt-b")
	wCreate := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerGitServiceCreateGitWorktreeProcedure, &leapmuxv1.CreateGitWorktreeRequest{
		Path:         wtA,
		Branch:       "branch-b",
		BaseBranch:   "HEAD~1",
		WorktreePath: wtB,
	}, wCreate)
	require.Empty(t, wCreate.errors)
	gotCommit, err := gitutil.Output(ctx, wtB, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, wantCommit, strings.TrimSpace(gotCommit))
}

func TestGitOps_RemoveWorktreeBlocksLiveTabInNestedRepoAndInheritedFileTab(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	repo := testutil.NewGitRepo(t)
	ctx := context.Background()

	wt := filepath.Join(t.TempDir(), "wt-nested")
	require.NoError(t, gitutil.Run(ctx, repo, "worktree", "add", "-b", "feat-nested", wt))

	// Create a nested git repository inside the linked worktree and open an agent there.
	nested := filepath.Join(wt, "subrepo")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, gitutil.Run(ctx, nested, "init"))
	require.NoError(t, gitutil.Run(ctx, nested, "config", "core.fsmonitor", "false"))
	require.NoError(t, gitutil.Run(ctx, nested, "config", "gc.auto", "0"))
	require.NoError(t, gitutil.Run(ctx, nested, "config", "maintenance.auto", "false"))

	err := svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-nested",
		Title:         "Nested",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		WorkingDir:    nested,
	})
	require.NoError(t, err)

	// RIPPLE-1: countLiveTabsInWorktree must detect nested-repo live tab and Force removal must still refuse.
	count, err := svc.countLiveTabsInWorktree(ctx, wt)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	wRm := &testResponseWriter{}
	dispatch(d, leapmuxv1connect.WorkerGitServiceRemoveGitWorktreeProcedure, &leapmuxv1.RemoveGitWorktreeRequest{
		Path:  wt,
		Force: true,
	}, wRm)
	require.Len(t, wRm.errors, 1, "worktree removal must fail while a live tab occupies a nested repo inside the worktree")
	assert.Equal(t, int32(codes.FailedPrecondition), wRm.errors[0].code)
	assert.Contains(t, wRm.errors[0].message, "1 live tab(s)")
	assert.DirExists(t, wt)
}

func TestWorktreeResolve_ClosedCallingTabDoesNotResolveWorktree(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := testutil.NewGitRepo(t)
	ctx := context.Background()

	err := svc.Queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:            "agent-closed",
		Title:         "Closed",
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		WorkingDir:    repo,
	})
	require.NoError(t, err)
	_, err = svc.Queries.CloseAgent(ctx, "agent-closed")
	require.NoError(t, err)

	// SCAN-16: A closed tab must not resolve a calling worktree for self=true.
	_, err = svc.resolveCallingWorktree(ctx, channel.Caller{
		UserID:      svc.RegisteredBy(),
		SourceTabID: "agent-closed",
	})
	require.ErrorIs(t, err, errGitWorktreeNotFound)
}

func TestGitOps_GetGitDiffIgnoresExternalDiffHelper(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell diff.external helper test")
	}
	svc, d, _ := setupTestService(t)
	ctx := context.Background()
	repoDir := filepath.Join(t.TempDir(), "repo")
	svc.HomeDir = filepath.Dir(repoDir)
	require.NoError(t, os.MkdirAll(repoDir, 0o755))

	runGit := func(args ...string) {
		t.Helper()
		_, err := gitutil.Output(ctx, repoDir, args...)
		require.NoError(t, err)
	}
	runGit("init")
	runGit("config", "core.fsmonitor", "false")
	runGit("config", "gc.auto", "0")
	runGit("config", "maintenance.auto", "false")
	runGit("config", "user.name", "Test")
	runGit("config", "user.email", "test@example.com")

	filePath := filepath.Join(repoDir, "hello.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("before\n"), 0o644))
	runGit("add", "hello.txt")
	runGit("commit", "-m", "initial")

	marker := filepath.Join(t.TempDir(), "ext-diff-ran")
	helper := filepath.Join(t.TempDir(), "ext-diff.sh")
	script := "#!/bin/sh\necho ran > " + marker + "\necho HIJACKED\n"
	testutil.WriteExecutable(t, helper, script)
	runGit("config", "diff.external", helper)

	require.NoError(t, os.WriteFile(filePath, []byte("after\n"), 0o644))

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerGitServiceGetGitDiffProcedure, &leapmuxv1.GetGitDiffRequest{
		Path: repoDir,
	}, w)
	require.Empty(t, w.errors)
	var resp leapmuxv1.GetGitDiffResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	_, statErr := os.Stat(marker)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "GetGitDiff must not execute repository diff.external hooks")
	assert.Contains(t, resp.GetDiff(), "-before")
	assert.Contains(t, resp.GetDiff(), "+after")
}

func TestGitOps_SendGitReadErrorRequiresNativeExitCode(t *testing.T) {
	t.Parallel()
	wPlain := newTestWriter()
	sendGitReadError(wPlain, "fatal: not a git repository", errors.New("boom"))
	require.Len(t, wPlain.errors, 1)
	assert.Equal(t, int32(codes.Internal), wPlain.errors[0].code)

	wCanceled := newTestWriter()
	sendGitReadError(wCanceled, "fatal: not a git repository", &gitutil.CommandError{
		RunError:     context.Canceled,
		CleanupError: errors.New("cleanup"),
	})
	require.Len(t, wCanceled.errors, 1)
	assert.Equal(t, int32(codes.Internal), wCanceled.errors[0].code)
}

func TestSelectedWorktreeIdentity_FallsBackToFileIDWithoutBirthTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repoDir := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))
	runGit := func(args ...string) {
		t.Helper()
		_, err := gitutil.Output(ctx, repoDir, args...)
		require.NoError(t, err)
	}
	runGit("init")
	runGit("config", "core.fsmonitor", "false")
	runGit("config", "gc.auto", "0")
	runGit("config", "maintenance.auto", "false")
	runGit("config", "user.name", "Test")
	runGit("config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "a.txt"), []byte("a\n"), 0o644))
	runGit("add", "a.txt")
	runGit("commit", "-m", "init")

	wtDir := filepath.Join(t.TempDir(), "wt")
	runGit("worktree", "add", "-b", "feat-fallback", wtDir)

	info, err := queryGitPathInfo(ctx, wtDir)
	require.NoError(t, err)
	captured, err := captureSelectedWorktree(info)
	require.NoError(t, err)
	if !captured.CheckoutID.hasFileID() || !captured.GitDirID.hasFileID() {
		t.Skip("platform did not report directory file identity")
	}

	// Simulate a filesystem without birth time: CreatedAtUnixNanos == 0, Volume/FileIndex set.
	noBirth := captured
	noBirth.CheckoutID.CreatedAtUnixNanos = 0
	noBirth.GitDirID.CreatedAtUnixNanos = 0
	assert.NoError(t, noBirth.verify(info, true), "matching (Volume, FileIndex) must allow removal when birth time is unavailable")

	// Mismatched FileIndex must still be rejected as changed.
	wrongIndex := noBirth
	wrongIndex.CheckoutID.FileIndex++
	assert.ErrorIs(t, wrongIndex.verify(info, true), errSelectedWorktreeChanged)

	// Missing both birth time and file identity must still fail closed on removal.
	unproved := captured
	unproved.CheckoutID = worktreeDirectoryID{}
	assert.ErrorIs(t, unproved.verify(info, true), errSelectedWorktreeUnproved)
}

func TestGitOps_CreateWorktreeExistingPathAndDirtyRemovePreconditions(t *testing.T) {
	t.Parallel()
	svc, d, _ := setupTestService(t)
	ctx := context.Background()
	repoDir := filepath.Join(t.TempDir(), "repo")
	svc.HomeDir = filepath.Dir(repoDir)
	require.NoError(t, os.MkdirAll(repoDir, 0o755))

	runGit := func(args ...string) {
		t.Helper()
		_, err := gitutil.Output(ctx, repoDir, args...)
		require.NoError(t, err)
	}
	runGit("init")
	runGit("config", "core.fsmonitor", "false")
	runGit("config", "gc.auto", "0")
	runGit("config", "maintenance.auto", "false")
	runGit("config", "user.name", "Test")
	runGit("config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "a.txt"), []byte("a\n"), 0o644))
	runGit("add", "a.txt")
	runGit("commit", "-m", "init")

	existingDir := filepath.Join(filepath.Dir(repoDir), "already-exists")
	require.NoError(t, os.MkdirAll(existingDir, 0o755))

	// CreateGitWorktree on an existing target path must return FailedPrecondition.
	wExists := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerGitServiceCreateGitWorktreeProcedure, &leapmuxv1.CreateGitWorktreeRequest{
		Path:         repoDir,
		Branch:       "feat-exists",
		WorktreePath: existingDir,
	}, wExists)
	require.Len(t, wExists.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), wExists.errors[0].code)

	// Create a valid worktree, dirty it, and verify non-forced RemoveGitWorktree returns FailedPrecondition.
	wtDir := filepath.Join(filepath.Dir(repoDir), "wt-dirty")
	wCreate := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerGitServiceCreateGitWorktreeProcedure, &leapmuxv1.CreateGitWorktreeRequest{
		Path:         repoDir,
		Branch:       "feat-dirty",
		WorktreePath: wtDir,
	}, wCreate)
	require.Empty(t, wCreate.errors)

	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "untracked.txt"), []byte("dirty\n"), 0o644))
	wRemove := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerGitServiceRemoveGitWorktreeProcedure, &leapmuxv1.RemoveGitWorktreeRequest{
		Path:  wtDir,
		Force: false,
	}, wRemove)
	require.Len(t, wRemove.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), wRemove.errors[0].code)
}
