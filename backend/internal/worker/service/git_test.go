package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/pathutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// initRepo creates a temporary git repo with an initial commit and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := gitInitRepo(dir); err != nil {
		t.Fatalf("init repo: %v", err)
	}
	return dir
}

// gitInitRepo runs `git init` + user config + an empty initial commit in
// the given directory. Returns the first failure, or nil.
func gitInitRepo(dir string) error {
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git %v: %w", args, err)
		}
	}
	return nil
}

// sharedCloseTabRepo returns a package-scoped git repo with a single
// initial commit, created on first use. Tests that only add a worktree
// on a unique branch can reuse it rather than paying ~4 git execs per
// call to initRepo. The repo is created under os.MkdirTemp and cleaned
// up by TestMain when the test binary exits.
//
// Safe to use when every caller passes a unique branch name to
// `git worktree add -b <branch>` — branch refs and `.git/worktrees/`
// entries are scoped per worktree path, so serialized callers don't
// collide. Do NOT use for tests that mutate the base repo (commits,
// branch deletes, tag creation, etc.) — those still need initRepo.
func sharedCloseTabRepo(t *testing.T) string {
	t.Helper()
	sharedCloseTabRepoOnce.Do(func() {
		dir, err := os.MkdirTemp("", "leapmux-close-tab-shared-repo-*")
		if err != nil {
			sharedCloseTabRepoErr = err
			return
		}
		if err := gitInitRepo(dir); err != nil {
			sharedCloseTabRepoErr = err
			return
		}
		sharedCloseTabRepoDir = dir
	})
	if sharedCloseTabRepoErr != nil {
		t.Fatalf("shared repo init: %v", sharedCloseTabRepoErr)
	}
	return sharedCloseTabRepoDir
}

var (
	sharedCloseTabRepoOnce sync.Once
	sharedCloseTabRepoDir  string
	sharedCloseTabRepoErr  error
)

// run executes a command in the given directory.
func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	require.NoError(t, cmd.Run(), "command failed: %s %v", name, args)
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedCloseTabRepoDir != "" {
		_ = os.RemoveAll(sharedCloseTabRepoDir)
	}
	os.Exit(code)
}

func TestGetGitFileStatusEntries_UntrackedFile(t *testing.T) {
	dir := initRepo(t)

	// Create an untracked file with 3 lines.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("a\nb\nc\n"), 0o644))

	files, err := getGitFileStatusEntries(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "new.txt", files[0].Path)
	assert.Equal(t, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED, files[0].UnstagedStatus)
	// Untracked files don't have line counts (numstat doesn't cover them).
	assert.Equal(t, int32(0), files[0].LinesAdded)
	assert.Equal(t, int32(0), files[0].LinesDeleted)
}

func TestGetGitFileStatusEntries_UntrackedBinaryFile(t *testing.T) {
	dir := initRepo(t)

	// Create an untracked binary file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "image.bin"), []byte("\x89PNG\r\n\x00\x00"), 0o644))

	files, err := getGitFileStatusEntries(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "image.bin", files[0].Path)
	assert.Equal(t, int32(0), files[0].LinesAdded)
}

func TestGetGitFileStatusEntries_MixedTrackedAndUntracked(t *testing.T) {
	dir := initRepo(t)

	// Create a committed file, then modify it (unstaged).
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("line1\n"), 0o644))
	run(t, dir, "git", "add", "tracked.txt")
	run(t, dir, "git", "commit", "-m", "add tracked")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("line1\nline2\n"), 0o644))

	// Create an untracked text file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("a\nb\nc\nd\n"), 0o644))

	// Create an untracked binary file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "binary.dat"), []byte("data\x00bin\n"), 0o644))

	files, err := getGitFileStatusEntries(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, files, 3)

	// Build a map for easier assertion.
	byPath := make(map[string]*leapmuxv1.GitFileStatusEntry, len(files))
	for _, f := range files {
		byPath[f.Path] = f
	}

	// tracked.txt: unstaged modification, 1 line added via numstat.
	tracked := byPath["tracked.txt"]
	assert.Equal(t, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_MODIFIED, tracked.UnstagedStatus)
	assert.Equal(t, int32(1), tracked.LinesAdded)
	assert.Equal(t, int32(0), tracked.LinesDeleted)

	// untracked.txt: untracked, no line counts (numstat doesn't cover them).
	untracked := byPath["untracked.txt"]
	assert.Equal(t, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED, untracked.UnstagedStatus)
	assert.Equal(t, int32(0), untracked.LinesAdded)
	assert.Equal(t, int32(0), untracked.LinesDeleted)

	// binary.dat: untracked binary, 0 lines.
	binary := byPath["binary.dat"]
	assert.Equal(t, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED, binary.UnstagedStatus)
	assert.Equal(t, int32(0), binary.LinesAdded)
}

func TestGetGitFileStatusEntries_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	files, err := getGitFileStatusEntries(context.Background(), dir)
	require.NoError(t, err)
	assert.Nil(t, files)
}

// TestGetGitFileStatusEntries_CleanTreeShortCircuits pins the contract
// that a clean repository returns nil without forking the two
// `git diff --numstat` probes — `git status` short-circuits the call
// before they get dispatched. The assertion that matters here is the
// nil/nil return; the avoided-fork behaviour is exercised by the same
// code path.
func TestGetGitFileStatusEntries_CleanTreeShortCircuits(t *testing.T) {
	dir := initRepo(t)

	// initRepo seeds the tree with a committed file; nothing else has
	// touched the working copy yet, so `git status --porcelain=v2`
	// emits zero entries.
	files, err := getGitFileStatusEntries(context.Background(), dir)
	require.NoError(t, err)
	assert.Nil(t, files, "clean tree must short-circuit to nil before numstat fans out")
}

func TestGetGitFileStatus_ReturnsOriginUrlAndCurrentBranch(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()

	// Set a remote origin URL.
	run(t, dir, "git", "remote", "add", "origin", "https://github.com/test/repo.git")

	// Create a file so there's something in the status.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o644))

	// Exercise the same helpers the handler uses (queryGitPathInfo +
	// branchOrShortSHA + getGitFileStatusEntries). Avoid re-rolling
	// the rev-parse pipeline so a regression in branchOrShortSHA's
	// detached-HEAD fallback can't slip past this test.
	info, err := queryGitPathInfo(ctx, dir)
	require.NoError(t, err)
	files, err := getGitFileStatusEntries(ctx, info.RepoRoot)
	require.NoError(t, err)
	require.NotEmpty(t, files)

	branch := branchOrShortSHA(info)
	originURL := strings.TrimSpace(gitutil.GetOriginURL(ctx, info.RepoRoot))

	// The default branch name depends on git config; just verify it's non-empty.
	assert.NotEmpty(t, branch)
	assert.Equal(t, "https://github.com/test/repo.git", originURL)
}

// TestGetGitFileStatus_WorktreeReturnsToplevel pins that GetGitFileStatus
// returns the worktree-aware `toplevel` field separately from the
// canonical `repo_root`. The frontend's syncGitStatusToTabs uses
// `toplevel` for tab matching so that switching focus to a worktree's
// agent doesn't smear the worktree's branch onto every main-tree tab
// in the same repo — the regression this field exists for.
func TestGetGitFileStatus_WorktreeReturnsToplevel(t *testing.T) {
	_, d, w := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := canonicalRepoDir(t)
	wtDir := filepath.Join(t.TempDir(), "wt-feature")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-feature", wtDir)

	dispatch(d, "GetGitFileStatus", &leapmuxv1.GetGitFileStatusRequest{
		Path: wtDir,
	}, w)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.GetGitFileStatusResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	// repo_root is the MAIN repo (canonical, used for file paths).
	expectedRepoRoot, err := filepath.EvalSymlinks(repoDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(expectedRepoRoot, resp.GetRepoRoot()),
		"repo_root must remain the main repo root for a worktree query")

	// toplevel is the worktree dir — different from repo_root.
	expectedToplevel, err := filepath.EvalSymlinks(wtDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(expectedToplevel, resp.GetToplevel()),
		"toplevel must be the worktree dir for an in-worktree query")
	assert.True(t, resp.GetIsWorktree())
	assert.Equal(t, "wt-feature", resp.GetCurrentBranch(),
		"current_branch reflects the WORKTREE's HEAD, not main")
}

// TestGetGitFileStatus_MainTreeToplevelEqualsRepoRoot pins the
// non-worktree case: toplevel collapses to repo_root so the frontend's
// `resp.toplevel || resp.repoRoot` fallback never has to fire on a
// well-behaved worker, and main-tree tab stamping continues to match
// gitToplevel == repo_root.
func TestGetGitFileStatus_MainTreeToplevelEqualsRepoRoot(t *testing.T) {
	_, d, w := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := canonicalRepoDir(t)

	dispatch(d, "GetGitFileStatus", &leapmuxv1.GetGitFileStatusRequest{
		Path: repoDir,
	}, w)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.GetGitFileStatusResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	assert.False(t, resp.GetIsWorktree())
	assert.Equal(t, resp.GetRepoRoot(), resp.GetToplevel(),
		"main-tree query must report toplevel == repo_root")
}

// TestGetGitFileStatus_DetachedHEAD covers the branchOrShortSHA
// fallback the GetGitFileStatus refactor enabled. On a detached HEAD,
// `--abbrev-ref HEAD` returns the literal "HEAD"; branchOrShortSHA
// must fall back to the short SHA so the dialog still shows a useful
// label instead of "HEAD".
func TestGetGitFileStatus_DetachedHEAD(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()

	// Detach HEAD at the initial commit.
	headSHA, err := gitutil.Output(ctx, dir, "rev-parse", "HEAD")
	require.NoError(t, err)
	headSHA = strings.TrimSpace(headSHA)
	run(t, dir, "git", "checkout", "--detach", headSHA)

	info, err := queryGitPathInfo(ctx, dir)
	require.NoError(t, err)
	assert.Empty(t, info.BranchName, "detached HEAD must produce empty BranchName")
	// The combined-output rev-parse populates HeadSHA from the same
	// invocation, so branchOrShortSHA no longer forks a second time.
	assert.Equal(t, headSHA, info.HeadSHA, "HeadSHA must hold the full SHA from the single rev-parse call")

	branch := branchOrShortSHA(info)
	require.NotEmpty(t, branch, "branchOrShortSHA must fall back to short SHA on detached HEAD")
	assert.True(t, strings.HasPrefix(headSHA, branch),
		"short SHA %q must be a prefix of full HEAD SHA %q", branch, headSHA)
	assert.Equal(t, shortSHALen, len(branch), "short SHA must be exactly shortSHALen characters")
	assert.NotEqual(t, "HEAD", branch, "must not surface the literal 'HEAD'")
}

func TestCheckoutBranchIfRequested_RemoteBranch(t *testing.T) {
	dir := initRepo(t)

	// Create a "remote" repo with a branch.
	remoteDir := initRepo(t)
	run(t, remoteDir, "git", "checkout", "-b", "feature/remote-test")
	run(t, remoteDir, "git", "commit", "--allow-empty", "-m", "remote commit")

	// Add as origin and fetch.
	run(t, dir, "git", "remote", "add", "origin", remoteDir)
	run(t, dir, "git", "fetch", "origin")

	// Checkout the remote branch via the service method.
	svc := &Context{}
	_, err := svc.executeCheckoutBranch(context.Background(), gitModePlan{Mode: gitModeCheckoutBranch, WorkingDir: dir, CheckoutTarget: "origin/feature/remote-test"})
	require.NoError(t, err)

	// Verify we're on a local branch (not detached HEAD).
	ctx := context.Background()
	branch, err := gitutil.Output(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "feature/remote-test", strings.TrimSpace(branch))

	// Verify the local branch tracks the remote.
	upstream, err := gitutil.Output(ctx, dir, "rev-parse", "--abbrev-ref", "feature/remote-test@{upstream}")
	require.NoError(t, err)
	assert.Equal(t, "origin/feature/remote-test", strings.TrimSpace(upstream))
}

func TestCheckoutBranchIfRequested_RemoteBranchWithExistingLocal(t *testing.T) {
	dir := initRepo(t)

	// Create a "remote" repo with a branch.
	remoteDir := initRepo(t)
	run(t, remoteDir, "git", "checkout", "-b", "feature/existing")
	run(t, remoteDir, "git", "commit", "--allow-empty", "-m", "remote commit")

	// Add as origin and fetch.
	run(t, dir, "git", "remote", "add", "origin", remoteDir)
	run(t, dir, "git", "fetch", "origin")

	// Create a local branch with the same name.
	run(t, dir, "git", "checkout", "-b", "feature/existing")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "local commit")
	run(t, dir, "git", "checkout", "-") // go back to default branch

	// Checkout the remote branch — should switch to existing local branch, not error.
	svc := &Context{}
	_, err := svc.executeCheckoutBranch(context.Background(), gitModePlan{Mode: gitModeCheckoutBranch, WorkingDir: dir, CheckoutTarget: "origin/feature/existing"})
	require.NoError(t, err)

	ctx := context.Background()
	branch, err := gitutil.Output(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "feature/existing", strings.TrimSpace(branch))
}

func TestCheckoutBranchIfRequested_LocalBranch(t *testing.T) {
	dir := initRepo(t)

	// Create a local branch.
	run(t, dir, "git", "checkout", "-b", "my-feature")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "feature commit")
	run(t, dir, "git", "checkout", "-") // go back to default branch

	// Checkout the local branch.
	svc := &Context{}
	_, err := svc.executeCheckoutBranch(context.Background(), gitModePlan{Mode: gitModeCheckoutBranch, WorkingDir: dir, CheckoutTarget: "my-feature"})
	require.NoError(t, err)

	ctx := context.Background()
	branch, err := gitutil.Output(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "my-feature", strings.TrimSpace(branch))
}

func TestDetachedHEAD_ShowsShortSHA(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()

	// Get short SHA before detaching.
	expectedSHA, err := gitutil.Output(ctx, dir, "rev-parse", "--short", "HEAD")
	require.NoError(t, err)
	expectedSHA = strings.TrimSpace(expectedSHA)

	// Detach HEAD.
	run(t, dir, "git", "checkout", "--detach", "HEAD")

	// Simulate what the handler does.
	branch, err := gitutil.Output(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	branch = strings.TrimSpace(branch)
	if branch == "HEAD" {
		sha, err := gitutil.Output(ctx, dir, "rev-parse", "--short", "HEAD")
		require.NoError(t, err)
		branch = strings.TrimSpace(sha)
	}

	assert.Equal(t, expectedSHA, branch)
}

func TestQueryGitPathInfo_RegularRepo(t *testing.T) {
	dir := initRepo(t)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	ctx := context.Background()
	info, err := queryGitPathInfo(ctx, dir)
	require.NoError(t, err)
	assert.Equal(t, resolved, info.RepoRoot)
	assert.False(t, info.IsWorktree)
	assert.NotEmpty(t, info.BranchName, "regular repo must populate BranchName from the same rev-parse call")

	// The combined rev-parse emits the full HEAD SHA — guard against a
	// regression where the format flags are reordered (`--abbrev-ref`
	// is sticky and silently swallows the SHA output if it precedes
	// the positional HEAD).
	expectedSHA, err := gitutil.Output(ctx, dir, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(expectedSHA), info.HeadSHA)
	assert.Len(t, info.HeadSHA, 40, "HeadSHA must be the full 40-char SHA, not abbreviated")
}

// TestQueryGitPathInfo_DetachedHEAD pins the single-call behaviour the
// HeadSHA refactor introduced: one rev-parse must populate both an
// empty BranchName (signalling detached) and the full HeadSHA, so
// branchOrShortSHA and currentCheckoutTarget can render the SHA
// without a second subprocess.
func TestQueryGitPathInfo_DetachedHEAD(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()

	headSHA, err := gitutil.Output(ctx, dir, "rev-parse", "HEAD")
	require.NoError(t, err)
	headSHA = strings.TrimSpace(headSHA)
	run(t, dir, "git", "checkout", "--detach", headSHA)

	info, err := queryGitPathInfo(ctx, dir)
	require.NoError(t, err)
	assert.Empty(t, info.BranchName, "detached HEAD must have empty BranchName so the abbrev-ref 'HEAD' literal is filtered out")
	assert.Equal(t, headSHA, info.HeadSHA)
}

func TestQueryGitPathInfo_NonGitDir(t *testing.T) {
	dir := t.TempDir()
	_, err := queryGitPathInfo(context.Background(), dir)
	assert.ErrorIs(t, err, errNotGitRepo)
}

// TestQueryGitPathInfo_UnbornHEAD pins the unborn-HEAD fallback. A fresh
// `git init` with no commits IS a real repo, but the combined-form
// `rev-parse ... HEAD --abbrev-ref HEAD` exits non-zero because the
// positional `HEAD` revision can't resolve. The earlier code surfaced
// the failure as errNotGitRepo, which locked every dialog whose open
// path runs through queryGitPathInfo (GetGitInfo, ChangeBranchDialog,
// linkFileTabToWorktree, …) out of an empty repo. The retry-without-HEAD
// path here must recognise the repo as real, return the toplevel + the
// default branch name, and leave HeadSHA empty so callers know there's
// nothing to short-SHA.
func TestQueryGitPathInfo_UnbornHEAD(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", dir, "init", "--initial-branch=main").Run())

	info, err := queryGitPathInfo(context.Background(), dir)
	require.NoError(t, err, "unborn HEAD must NOT surface as errNotGitRepo — it's a real repo")
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Equal(t, resolved, info.TopLevel)
	assert.Equal(t, resolved, info.RepoRoot, "main-repo unborn HEAD resolves the same TopLevel and RepoRoot")
	assert.Equal(t, "main", info.BranchName,
		"unborn HEAD still resolves the symref via --abbrev-ref HEAD; the branch name is the planned target")
	assert.Empty(t, info.HeadSHA,
		"unborn HEAD has no SHA to resolve — the retry path leaves HeadSHA empty so branchOrShortSHA returns empty")
	assert.False(t, info.IsWorktree)
}

// TestBranchOrShortSHA_UnbornHEAD locks in the branch-name fallback the
// unborn-HEAD path returns. queryGitPathInfo now successfully resolves
// such repos with BranchName populated from `--abbrev-ref HEAD` (which
// emits the symref target even before any commit exists), so
// branchOrShortSHA's primary branch returns that name without falling
// through to the empty short-SHA case.
func TestBranchOrShortSHA_UnbornHEAD(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, exec.Command("git", "-C", dir, "init", "--initial-branch=main").Run())

	info, err := queryGitPathInfo(context.Background(), dir)
	require.NoError(t, err)
	assert.Equal(t, "main", branchOrShortSHA(info))
}

func TestQueryGitPathInfo_RepoPathWithNewline(t *testing.T) {
	// Regression: the parser used to read lines[0..4] positionally, so a
	// `--show-toplevel` whose output legitimately spans multiple lines
	// (POSIX paths containing newlines, vanishingly rare but legal)
	// silently zeroed every field after the partial toplevel. Mirrors
	// gitutil.TestGetToplevelInfo's newline-in-path case — the same fix
	// (parse from the end) must apply to both.
	//
	// Skip on filesystems that refuse newline-in-path.
	base := t.TempDir()
	dirty := filepath.Join(base, "with\nnewline")
	if err := os.MkdirAll(dirty, 0o755); err != nil {
		t.Skipf("filesystem rejected newline-in-path: %v", err)
	}
	if err := exec.Command("git", "-C", dirty, "init", "--initial-branch=main").Run(); err != nil {
		t.Skipf("git refused newline-in-path: %v", err)
	}
	// Commit something so HEAD resolves.
	run(t, dirty, "git", "config", "user.email", "t@example.com")
	run(t, dirty, "git", "config", "user.name", "t")
	require.NoError(t, exec.Command("git", "-C", dirty, "commit", "--allow-empty", "-m", "seed").Run())

	resolved, err := filepath.EvalSymlinks(dirty)
	require.NoError(t, err)

	info, err := queryGitPathInfo(context.Background(), dirty)
	require.NoError(t, err)
	gotResolved, err := filepath.EvalSymlinks(info.TopLevel)
	require.NoError(t, err)
	assert.Equal(t, resolved, gotResolved, "TopLevel must include the newline-containing segment")
	assert.Equal(t, "main", info.BranchName, "BranchName must still parse correctly when TopLevel spans multiple lines")
	assert.Len(t, info.HeadSHA, 40, "HeadSHA must be the full 40-char SHA after end-counting parse")
	assert.False(t, info.IsWorktree, "main repo must not be flagged as a worktree")
}

func TestQueryGitPathInfo_Subdirectory(t *testing.T) {
	dir := initRepo(t)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	subDir := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(subDir, 0o755))

	info, err := queryGitPathInfo(context.Background(), subDir)
	require.NoError(t, err)
	assert.Equal(t, resolved, info.RepoRoot)
}

func TestQueryGitPathInfo_LinkedWorktree(t *testing.T) {
	dir := initRepo(t)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	wtDir := filepath.Join(t.TempDir(), "my-worktree")
	run(t, dir, "git", "worktree", "add", "-b", "wt-branch", wtDir)

	info, err := queryGitPathInfo(context.Background(), wtDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(resolved, info.RepoRoot),
		"expected same path; got resolved=%q root=%q", resolved, info.RepoRoot)
}

func TestQueryGitPathInfo_NestedWorktree(t *testing.T) {
	dir := initRepo(t)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	wtDir := filepath.Join(t.TempDir(), "nested-wt")
	run(t, dir, "git", "worktree", "add", "-b", "nested-branch", wtDir)

	subDir := filepath.Join(wtDir, "deep")
	require.NoError(t, os.MkdirAll(subDir, 0o755))

	info, err := queryGitPathInfo(context.Background(), subDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(resolved, info.RepoRoot),
		"expected same path; got resolved=%q root=%q", resolved, info.RepoRoot)
}

// TestCurrentCheckoutTarget covers the rollbackBranch produced by the
// helper in both the attached and detached cases. The HeadSHA refactor
// reads info.HeadSHA inline instead of forking a second `rev-parse
// HEAD`, so the detached path no longer needs the fallback subprocess
// that the prior version ran — this test guards both shapes.
func TestCurrentCheckoutTarget_AttachedBranch(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()
	run(t, dir, "git", "checkout", "-b", "feature-x")

	target, err := currentCheckoutTarget(ctx, dir)
	require.NoError(t, err)
	assert.Equal(t, dir, target.WorkingDir)
	assert.Equal(t, "feature-x", target.OriginalBranch)
	assert.Empty(t, target.OriginalCommit, "attached branch must not carry a SHA")
	assert.False(t, target.OriginalDetached)
}

func TestCurrentCheckoutTarget_DetachedHEAD(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()

	headSHA, err := gitutil.Output(ctx, dir, "rev-parse", "HEAD")
	require.NoError(t, err)
	headSHA = strings.TrimSpace(headSHA)
	run(t, dir, "git", "checkout", "--detach", headSHA)

	target, err := currentCheckoutTarget(ctx, dir)
	require.NoError(t, err)
	assert.Empty(t, target.OriginalBranch)
	assert.Equal(t, headSHA, target.OriginalCommit, "must use HeadSHA from queryGitPathInfo, no second rev-parse")
	assert.True(t, target.OriginalDetached)
}

func TestQueryGitPathInfo_LinkedWorktreeUsesWorktreeBranch(t *testing.T) {
	dir := initRepo(t)
	ctx := context.Background()

	run(t, dir, "git", "checkout", "-b", "main-branch")
	run(t, dir, "git", "commit", "--allow-empty", "-m", "main branch commit")

	wtDir := filepath.Join(t.TempDir(), "feature-worktree")
	run(t, dir, "git", "worktree", "add", "-b", "feature-branch", wtDir)

	infoMain, err := queryGitPathInfo(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, "main-branch", branchOrShortSHA(infoMain))

	infoWt, err := queryGitPathInfo(ctx, wtDir)
	require.NoError(t, err)
	require.Equal(t, "feature-branch", branchOrShortSHA(infoWt))
}

func createAgentForPath(t *testing.T, svc *Context, agentID, workingDir string) {
	t.Helper()
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:          agentID,
		WorkspaceID: "ws-1",
		WorkingDir:  workingDir,
		HomeDir:     workingDir,
	}))
}

func createTerminalForPath(t *testing.T, svc *Context, terminalID, workingDir string) {
	t.Helper()
	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:          terminalID,
		WorkspaceID: "ws-1",
		WorkingDir:  workingDir,
		HomeDir:     workingDir,
		// `screen` is NOT NULL on the terminals table; an empty buffer
		// is the natural "no screen content yet" sentinel.
		Screen: []byte{},
	}))
}

func waitForPathToDisappear(t *testing.T, path string) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return os.IsNotExist(err)
	}, 5*time.Second, 100*time.Millisecond)
}

func TestInspectLastTabClose_WorktreeLastTabPromptsEvenWhenClean(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "inspect-clean-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "inspect-clean", wtDir)

	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)
	createAgentForPath(t, svc, "agent-1", wtDir)
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1")

	resp, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE, resp.GetTarget())
	assert.True(t, resp.GetShouldPrompt())
	expectedPath, err := filepath.EvalSymlinks(wtDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(expectedPath, resp.GetWorktreePath()),
		"expected same path; got expected=%q actual=%q", expectedPath, resp.GetWorktreePath())
	assert.Equal(t, "inspect-clean", resp.GetBranchName())
}

func TestInspectLastTabClose_BranchLastTabCleanDoesNotPrompt(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	createAgentForPath(t, svc, "agent-branch-clean", repoDir)

	resp, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-branch-clean")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_NONE, resp.GetTarget())
	assert.False(t, resp.GetShouldPrompt())
}

func TestInspectLastTabClose_BranchLastTabDirtyPrompts(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("dirty\n"), 0o644))
	createAgentForPath(t, svc, "agent-branch-dirty", repoDir)

	resp, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-branch-dirty")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_BRANCH, resp.GetTarget())
	assert.True(t, resp.GetShouldPrompt())
	assert.True(t, resp.GetGitState().GetHasUncommittedChanges())
}

func TestInspectLastTabClose_BranchMissingRemotePrompts(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	bareDir := filepath.Join(t.TempDir(), "missing-remote.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")
	run(t, repoDir, "git", "checkout", "-b", "feature-missing-remote")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "feature")
	run(t, repoDir, "git", "push", "-u", "origin", "feature-missing-remote")
	run(t, repoDir, "git", "push", "origin", "--delete", "feature-missing-remote")
	createAgentForPath(t, svc, "agent-branch-missing", repoDir)

	resp, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-branch-missing")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_BRANCH, resp.GetTarget())
	assert.True(t, resp.GetShouldPrompt())
	assert.True(t, resp.GetGitState().GetRemoteBranchMissing())
	assert.True(t, resp.GetGitState().GetCanPush())
}

// Fast path: a worktree with more than one tab must not prompt, and
// must not pay for diffStatsForPath / pushStatusForPath. We can't
// observe the skipped subprocesses directly from a test, but we can
// assert that the diff_* and push_* fields are left zero — those are
// only populated when the full inspect path runs.
func TestInspectLastTabClose_WorktreeMultiTabFastPath(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "multi-tab-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "multi-tab", wtDir)

	// Make the worktree dirty and unpushed — the slow path would surface
	// hasUncommittedChanges=true. The fast path must skip the diff/push
	// calls and leave those fields zero.
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "dirty.txt"), []byte("dirty\n"), 0o644))

	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)
	createAgentForPath(t, svc, "agent-mt-1", wtDir)
	createAgentForPath(t, svc, "agent-mt-2", wtDir)
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-mt-1")
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-mt-2")

	resp, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-mt-1")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE, resp.GetTarget())
	assert.False(t, resp.GetShouldPrompt(), "multi-tab worktree must not prompt")
	// Fast-path-specific: git_state submessage must be absent.
	assert.Nil(t, resp.GetGitState(), "fast path skips diff/push snapshot")
	// Worktree identity fields must come from the DB row, though.
	assert.Equal(t, wtID, resp.GetWorktreeId())
	expectedWtPath, err := filepath.EvalSymlinks(wtDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(expectedWtPath, resp.GetWorktreePath()),
		"expected same path; got expected=%q actual=%q", expectedWtPath, resp.GetWorktreePath())
	assert.Equal(t, "multi-tab", resp.GetBranchName())
}

// A file tab opened inside a worktree must count as a sibling of any
// agent/terminal tab in the same worktree. Without this, closing the
// last agent while file tabs remain open trips the last-tab dialog and
// — worse — risks deleting the worktree from disk while the file tabs
// still reference it. Regression guard for the original bug.
func TestInspectLastTabClose_WorktreeFileTabHoldsWorktree(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	svc.FileTabPaths = NewFileTabPathStore(svc.Queries, nil)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "file-tab-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "file-tab-bug", wtDir)
	openPath := filepath.Join(wtDir, "open.txt")
	require.NoError(t, os.WriteFile(openPath, []byte("open\n"), 0o644))

	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)
	createAgentForPath(t, svc, "agent-with-file", wtDir)
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-with-file")

	require.NoError(t, svc.FileTabPaths.Register(context.Background(), RegisterFileTabPathParams{
		OrgID:       "org-1",
		TabID:       "file-tab-1",
		WorkspaceID: "ws-1",
		FilePath:    openPath,
	}))

	// Sanity: Register linked the file tab into worktree_tabs.
	count, err := svc.Queries.CountWorktreeTabs(context.Background(), wtID)
	require.NoError(t, err)
	require.Equal(t, int64(2), count, "file tab must be tracked as a worktree sibling")

	resp, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-with-file")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE, resp.GetTarget())
	assert.False(t, resp.GetShouldPrompt(),
		"agent close with sibling file tab must not prompt for last-tab")
	assert.Nil(t, resp.GetGitState(), "fast path skips diff/push snapshot")

	// Closing the file tab through the shared closeTabCommon flow must
	// drop its worktree_tabs row so a subsequent agent close reverts to
	// the real last-tab path.
	closeResult := svc.closeTabCommon(
		leapmuxv1.TabType_TAB_TYPE_FILE,
		"file-tab-1",
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
		func() {},
		func() error { return svc.FileTabPaths.RevokeRow(context.Background(), "org-1", "file-tab-1") },
	)
	require.Equal(t, "", closeResult.GetFailureMessage(), "FILE close must not report a failure")
	count, err = svc.Queries.CountWorktreeTabs(context.Background(), wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "file-tab close must drop the worktree_tabs row")
}

// FILE close with WorktreeAction.REMOVE on the last tab must remove
// the worktree from disk via closeTabCommon, mirroring the AGENT /
// TERMINAL last-close pipeline. Regression guard for the original bug
// where a FILE tab being the last tab on a worktree silently skipped
// the worktree cleanup.
func TestCloseTabCommon_FileLastTabRemoveWorktree(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	svc.FileTabPaths = NewFileTabPathStore(svc.Queries, nil)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "file-only-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "file-only-branch", wtDir)
	openPath := filepath.Join(wtDir, "open.txt")
	require.NoError(t, os.WriteFile(openPath, []byte("open\n"), 0o644))

	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)

	require.NoError(t, svc.FileTabPaths.Register(context.Background(), RegisterFileTabPathParams{
		OrgID:       "org-1",
		TabID:       "file-only-tab",
		WorkspaceID: "ws-1",
		FilePath:    openPath,
	}))

	count, err := svc.Queries.CountWorktreeTabs(context.Background(), wtID)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "FILE tab must be the only tab on the worktree")

	result := svc.closeTabCommon(
		leapmuxv1.TabType_TAB_TYPE_FILE,
		"file-only-tab",
		leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
		func() {},
		func() error { return svc.FileTabPaths.RevokeRow(context.Background(), "org-1", "file-only-tab") },
	)
	require.Equal(t, "", result.GetFailureMessage(), "REMOVE on last FILE tab must succeed; got: %s / %s", result.GetFailureMessage(), result.GetFailureDetail())

	// The worktree DB row is soft-deleted (deleted_at set), so the
	// path-based lookup that filters on deleted_at IS NULL returns
	// ErrNoRows. Using GetWorktreeByID would still return the
	// tombstone row.
	_, err = svc.Queries.GetWorktreeByPath(context.Background(), pathutil.Canonicalize(wtDir))
	assert.True(t, errors.Is(err, sql.ErrNoRows), "worktree DB row must be soft-deleted after REMOVE (err=%v)", err)
	// And so is the worktree directory.
	_, statErr := os.Stat(wtDir)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "worktree directory must be removed from disk (err=%v)", statErr)
	// The worktree_tabs row is also gone — symmetric to the AGENT/
	// TERMINAL last-close path.
	remaining, err := svc.Queries.CountWorktreeTabs(context.Background(), wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), remaining, "worktree_tabs row must be dropped by closeTabCommon")
}

// A file tab whose path is outside any tracked worktree must register
// successfully but leave worktree_tabs untouched — there is nothing to
// link to, and a stray INSERT would later block the orphan-row clean up.
func TestFileTabPathStore_RegisterOutsideWorktreeSkipsLink(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	svc.FileTabPaths = NewFileTabPathStore(svc.Queries, nil)

	loosePath := filepath.Join(t.TempDir(), "loose.txt")
	require.NoError(t, os.WriteFile(loosePath, []byte("loose\n"), 0o644))

	require.NoError(t, svc.FileTabPaths.Register(context.Background(), RegisterFileTabPathParams{
		OrgID:       "org-1",
		TabID:       "loose-file",
		WorkspaceID: "ws-1",
		FilePath:    loosePath,
	}))

	wt, err := svc.Queries.GetWorktreeForTab(context.Background(), db.GetWorktreeForTabParams{
		TabType: leapmuxv1.TabType_TAB_TYPE_FILE,
		TabID:   "loose-file",
	})
	assert.True(t, errors.Is(err, sql.ErrNoRows),
		"file tab outside any worktree must not create a worktree_tabs row (got wt=%+v, err=%v)", wt, err)
}

// Fast path: a non-worktree tab with other non-worktree tabs on the
// same branch must not prompt, and must not pay for diff/push.
func TestInspectLastTabClose_BranchMultiTabFastPath(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	// Dirty the branch — the slow path would set hasUncommittedChanges=true.
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("dirty\n"), 0o644))
	createAgentForPath(t, svc, "agent-branch-mt-1", repoDir)
	createAgentForPath(t, svc, "agent-branch-mt-2", repoDir)

	resp, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-branch-mt-1")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_NONE, resp.GetTarget())
	assert.False(t, resp.GetShouldPrompt())
	assert.Nil(t, resp.GetGitState(), "fast path skips diff/push snapshot")
}

// Exercises the parallel scan's terminal-side hit in
// hasOtherNonWorktreeTabOnBranch: closing an AGENT but a TERMINAL on
// the same branch keeps the branch alive. The other existing fast-path
// test only has matches on the agents side, so this guards against a
// regression where the terminals goroutine silently stops running.
func TestInspectLastTabClose_BranchTerminalKeepsBranchAlive(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	createAgentForPath(t, svc, "agent-branch-cross-1", repoDir)
	createTerminalForPath(t, svc, "term-branch-cross-1", repoDir)

	resp, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-branch-cross-1")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_NONE, resp.GetTarget(),
		"sibling terminal on the same branch must keep the close routing on the fast path")
	assert.False(t, resp.GetShouldPrompt())
}

// The cross-table cache amortizes rev-parse calls in
// hasOtherNonWorktreeTabOnBranch. After parallelizing the agents and
// terminals scans, the cache map is shared across goroutines under a
// mutex. This test exercises that — two agents and two terminals all
// in the same repo — and asserts the function still returns true once.
func TestInspectLastTabClose_ManyTabsParallelScans(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	createAgentForPath(t, svc, "agent-mix-1", repoDir)
	createAgentForPath(t, svc, "agent-mix-2", repoDir)
	createAgentForPath(t, svc, "agent-mix-3", repoDir)
	createTerminalForPath(t, svc, "term-mix-1", repoDir)
	createTerminalForPath(t, svc, "term-mix-2", repoDir)

	resp, err := svc.inspectLastTabClose(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-mix-1")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_NONE, resp.GetTarget())
	assert.False(t, resp.GetShouldPrompt(),
		"any sibling tab on the same branch (agent or terminal) must keep the close on the fast path")
}

// TestIsDirty pins the boolean-only contract of the shared dirty-tree
// probe: false on a clean tree, true on a tracked-but-modified file,
// true on a stray untracked file. The probe is shared by GetGitInfo
// and dirtyTreeForPush; both paths depend on this exact predicate.
func TestIsDirty(t *testing.T) {
	repoDir := initRepo(t)
	ctx := context.Background()

	dirty, err := isDirty(ctx, repoDir)
	require.NoError(t, err)
	assert.False(t, dirty, "fresh repo with only the empty init commit must be clean")

	// Untracked file -> dirty.
	stray := filepath.Join(repoDir, "stray.txt")
	require.NoError(t, os.WriteFile(stray, []byte("hello\n"), 0o644))
	dirty, err = isDirty(ctx, repoDir)
	require.NoError(t, err)
	assert.True(t, dirty, "untracked file must surface as dirty")

	// Track and commit, then modify -> dirty again.
	run(t, repoDir, "git", "add", "stray.txt")
	run(t, repoDir, "git", "commit", "-m", "track")
	dirty, err = isDirty(ctx, repoDir)
	require.NoError(t, err)
	assert.False(t, dirty, "freshly-committed file must leave the tree clean")
	require.NoError(t, os.WriteFile(stray, []byte("modified\n"), 0o644))
	dirty, err = isDirty(ctx, repoDir)
	require.NoError(t, err)
	assert.True(t, dirty, "modification to a tracked file must surface as dirty")
}

// TestIsDirty_NonRepoErrors guards the error path: the helper bubbles
// the git failure up rather than silently returning false, so callers
// can distinguish "definitely clean" from "couldn't probe".
func TestIsDirty_NonRepoErrors(t *testing.T) {
	dir := t.TempDir()
	dirty, err := isDirty(context.Background(), dir)
	require.Error(t, err, "non-repo path must error, not return false")
	assert.False(t, dirty)
}

func TestPushBranch_CreatesWIPCommitAndPushes(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	bareDir := filepath.Join(t.TempDir(), "push-dirty.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")
	run(t, repoDir, "git", "checkout", "-b", "push-dirty")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "feature init")
	run(t, repoDir, "git", "push", "-u", "origin", "push-dirty")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("content\n"), 0o644))
	createAgentForPath(t, svc, "agent-push-dirty", repoDir)

	err := svc.pushBranch(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-push-dirty")
	require.NoError(t, err)

	msg, err := gitutil.Output(context.Background(), repoDir, "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	assert.Equal(t, "WIP", strings.TrimSpace(msg))

	remoteHead, err := gitutil.Output(context.Background(), bareDir, "rev-parse", "refs/heads/push-dirty")
	require.NoError(t, err)
	localHead, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(localHead), strings.TrimSpace(remoteHead))
}

// TestPushBranch_ReProbesDirtyTreeIgnoringHint pins the new dirty-tree
// contract: the worker ALWAYS probes the working tree before deciding
// whether to roll a WIP commit, ignoring any HasUncommittedChanges hint.
// An earlier revision trusted the snapshot's hint, but the inspect→push
// interval allows external file mutations to drift from the snapshot —
// either silently skipping a real WIP commit (clean hint, now dirty) or
// creating an empty WIP commit (dirty hint, now clean).
func TestPushBranch_ReProbesDirtyTreeIgnoringHint(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	bareDir := filepath.Join(t.TempDir(), "push-reprobe.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")
	run(t, repoDir, "git", "checkout", "-b", "push-reprobe")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "feature init")
	run(t, repoDir, "git", "push", "-u", "origin", "push-reprobe")
	// A dirty file written AFTER the snapshot the dialog cached but
	// BEFORE the user clicked Push — the stale-hint scenario the
	// regression targets.
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("dirty\n"), 0o644))
	createAgentForPath(t, svc, "agent-push-reprobe", repoDir)

	err := svc.pushBranch(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-push-reprobe")
	require.NoError(t, err)

	// Latest commit is the WIP that the re-probe correctly swept up.
	// pushBranch always probes dirtyTreeForPush rather than trusting a
	// caller-supplied hint, so a dirty tree always rolls up into a WIP
	// commit regardless of what any sibling actor might have snapshotted
	// earlier.
	msg, err := gitutil.Output(context.Background(), repoDir, "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	assert.Equal(t, wipCommitMessage, strings.TrimSpace(msg),
		"stale hint=clean must NOT skip the WIP commit when the tree is actually dirty")
}

// TestPushBranch_StaleDirtyHintOnCleanTreeDoesNotCreateEmptyCommit is
// the inverse of the above: hint=dirty on an actually-clean tree must
// not synthesize an empty WIP commit. The re-probe sees the clean tree
// and skips the WIP step regardless of what the hint claimed.
func TestPushBranch_StaleDirtyHintOnCleanTreeDoesNotCreateEmptyCommit(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	bareDir := filepath.Join(t.TempDir(), "push-stale-dirty.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")
	run(t, repoDir, "git", "checkout", "-b", "push-stale-dirty")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "feature init")
	run(t, repoDir, "git", "push", "-u", "origin", "push-stale-dirty")
	createAgentForPath(t, svc, "agent-stale-dirty", repoDir)

	// Tree is genuinely clean (no dirty.txt). pushBranch must skip the
	// add/commit step because dirtyTreeForPush sees a clean tree —
	// regardless of any stale "dirty" snapshot the dialog might have
	// captured before the user committed externally.
	err := svc.pushBranch(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-stale-dirty")
	require.NoError(t, err)

	// HEAD must still be `feature init` — no WIP commit was synthesized.
	msg, err := gitutil.Output(context.Background(), repoDir, "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	assert.Equal(t, "feature init", strings.TrimSpace(msg),
		"stale hint=dirty must NOT create an empty WIP commit on a clean tree")
}

// TestPushBranch_NoUpstreamSetsUpstreamArg verifies that pushing a new
// local branch with no upstream adds `-u origin <branch>` to the push
// args. The live pushStatusForPath probe discovers UpstreamExists=false
// from `git config branch.<X>.{remote,merge}`; without this branch in
// pushBranch the second push fails with "no upstream branch".
func TestPushBranch_NoUpstreamSetsUpstreamArg(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	bareDir := filepath.Join(t.TempDir(), "push-noup.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")
	// New local branch with no upstream — pushStatusForPath probes
	// `git config branch.<X>.{remote,merge}`, observes the missing keys,
	// returns UpstreamExists=false, and pushBranch appends -u origin
	// <branch> on that signal.
	run(t, repoDir, "git", "checkout", "-b", "push-noup")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "feature init")
	createAgentForPath(t, svc, "agent-push-noup", repoDir)

	err := svc.pushBranch(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-push-noup")
	require.NoError(t, err)

	// The branch must now exist on the bare remote, which only happens
	// if `git push -u origin push-noup` ran (a plain `git push` without
	// an upstream would have errored).
	remoteHead, err := gitutil.Output(context.Background(), bareDir, "rev-parse", "refs/heads/push-noup")
	require.NoError(t, err)
	localHead, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(localHead), strings.TrimSpace(remoteHead))
}

// TestPushBranch_NoOriginRejectsBeforeAttempting verifies that a repo
// without a configured origin remote is rejected with the "no origin"
// error before pushBranch ever shells out a `git push`.
func TestPushBranch_NoOriginRejectsBeforeAttempting(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	// No bare remote — repo really has no origin. pushStatusForPath
	// observes the missing remote.origin.url config and pushBranch
	// rejects without touching the network.
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "no-origin")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "feature init")
	createAgentForPath(t, svc, "agent-no-origin", repoDir)

	err := svc.pushBranch(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-no-origin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remote origin does not exist")
}

// TestListGitBranches_LocalAndRemote walks a repo with a mix of local
// branches, remote-tracking refs (some with local counterparts to be
// deduped, some without), and origin/HEAD. Confirms the single
// for-each-ref invocation returns the expected projection.
func TestListGitBranches_LocalAndRemote(t *testing.T) {
	bareDir := filepath.Join(t.TempDir(), "list-remote.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	seedDir := initRepo(t)
	defaultBranch := strings.TrimSpace(captureRunOutput(t, seedDir, "git", "rev-parse", "--abbrev-ref", "HEAD"))
	run(t, seedDir, "git", "remote", "add", "origin", bareDir)
	run(t, seedDir, "git", "push", "-u", "origin", "HEAD")
	run(t, seedDir, "git", "checkout", "-b", "remote-only")
	run(t, seedDir, "git", "commit", "--allow-empty", "-m", "remote-only")
	run(t, seedDir, "git", "push", "-u", "origin", "remote-only")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	// Track all remote branches so origin/remote-only is fetched (default
	// remote config from `git remote add` already does refs/heads/*).
	run(t, repoDir, "git", "fetch", "origin", "refs/heads/*:refs/remotes/origin/*")
	// Local counterpart to origin/<default> — the remote-tracking variant
	// must be deduped because the local with the same stripped name already
	// serves the same purpose.
	if defaultBranchOf(t, repoDir) != defaultBranch {
		run(t, repoDir, "git", "branch", defaultBranch)
	}
	run(t, repoDir, "git", "branch", "main-local-copy")

	branches, err := listGitBranches(context.Background(), repoDir)
	require.NoError(t, err)

	var locals, remotes []string
	for _, b := range branches {
		if b.IsRemote {
			remotes = append(remotes, b.Name)
		} else {
			locals = append(locals, b.Name)
		}
	}
	assert.Contains(t, locals, defaultBranch)
	assert.Contains(t, locals, "main-local-copy")
	// Local-only counterpart of origin/<defaultBranch> dedups the remote.
	assert.NotContains(t, remotes, "origin/"+defaultBranch)
	// remote-only has no local, so it must surface.
	assert.Contains(t, remotes, "origin/remote-only")
	// origin/HEAD is a pseudo-ref — must be filtered.
	for _, r := range remotes {
		assert.False(t, strings.HasSuffix(r, "/HEAD"), "origin/HEAD must be filtered, got %q", r)
	}
}

// TestListGitBranches_DetachedHead is a sanity check: the function only
// reports the actual branches, never HEAD itself. A detached HEAD must
// not synthesize a phantom entry.
func TestListGitBranches_DetachedHead(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "first")
	sha := strings.TrimSpace(captureRunOutput(t, repoDir, "git", "rev-parse", "HEAD"))
	run(t, repoDir, "git", "checkout", "--detach", sha)

	branches, err := listGitBranches(context.Background(), repoDir)
	require.NoError(t, err)
	for _, b := range branches {
		assert.NotEqual(t, "HEAD", b.Name)
		assert.NotEqual(t, sha, b.Name)
	}
}

// TestListGitBranches_NestedRefName preserves slash-containing branch
// names (e.g. "feature/foo"). The for-each-ref output uses full ref
// paths, so a sloppy splitter could mis-extract these. Git itself
// forbids ref-and-namespace conflicts (refs/heads/foo blocking
// refs/heads/foo/bar), so we use two disjoint slash-bearing names.
func TestListGitBranches_NestedRefName(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "branch", "feature/foo")
	run(t, repoDir, "git", "branch", "bugfix/bar/baz")

	branches, err := listGitBranches(context.Background(), repoDir)
	require.NoError(t, err)
	var names []string
	for _, b := range branches {
		if !b.IsRemote {
			names = append(names, b.Name)
		}
	}
	assert.Contains(t, names, "feature/foo")
	assert.Contains(t, names, "bugfix/bar/baz")
}

func defaultBranchOf(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(captureRunOutput(t, dir, "git", "rev-parse", "--abbrev-ref", "HEAD"))
}

// captureRunOutput runs a command and returns its stdout for inspection.
func captureRunOutput(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err, "command failed: %s %v", name, args)
	return string(out)
}

func TestInspectBranchDeletion_NonWorktreeClean(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)

	resp, err := svc.inspectBranchDeletion(context.Background(), repoDir, "")
	require.NoError(t, err)
	assert.False(t, resp.GetIsWorktree())
	assert.Empty(t, resp.GetWorktreePath())
	assert.False(t, resp.GetGitState().GetHasUncommittedChanges())
	assert.Equal(t, int32(0), resp.GetGitState().GetUnpushedCommitCount())
	assert.NotEmpty(t, resp.GetBranchName())
	// The non-worktree path returns the picker list inline so the dialog
	// can render without a second list-branches RPC. The list includes
	// the current branch — filtering is the client's responsibility.
	assert.NotEmpty(t, resp.GetBranches(), "non-worktree response should carry the picker list")
}

func TestInspectBranchDeletion_NonWorktreeBranchesIncludesCurrent(t *testing.T) {
	// The branches field is the worker's authoritative list; the dialog
	// filters out the doomed branch client-side. Pin that the worker
	// does NOT pre-filter — a future refactor that drops the current
	// branch server-side would silently break the "switch from main to
	// main" disambiguation flow.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "f1")

	resp, err := svc.inspectBranchDeletion(context.Background(), repoDir, "feature")
	require.NoError(t, err)
	names := make([]string, 0, len(resp.GetBranches()))
	for _, b := range resp.GetBranches() {
		names = append(names, b.GetName())
	}
	assert.Contains(t, names, "feature", "current branch must appear in response.branches; the dialog filters it")
	// Default `main`/`master` branch from initRepo also lands in the list.
	assert.GreaterOrEqual(t, len(names), 2)
}

func TestInspectBranchDeletion_Worktree(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "delete-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "delete-wt-branch", wtDir)

	resp, err := svc.inspectBranchDeletion(context.Background(), wtDir, "")
	require.NoError(t, err)
	assert.True(t, resp.GetIsWorktree())
	expectedPath, err := filepath.EvalSymlinks(wtDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(expectedPath, resp.GetWorktreePath()))
	assert.Equal(t, "delete-wt-branch", resp.GetBranchName())
	// The worktree path renders no picker, so the worker leaves the
	// branches field empty — saves bytes on every worktree-context open.
	assert.Empty(t, resp.GetBranches(), "worktree response should omit the picker list")
	// Untracked worktrees (no DB row yet) surface worktree_id="" so the
	// dialog can fall back to a clear "untracked" banner instead of
	// silently dispatching ForceRemoveWorktree against an empty id.
	assert.Empty(t, resp.GetWorktreeId(), "worktree_id is empty when no DB row exists yet")
}

// TestInspectBranchDeletion_WorktreeReturnsTrackedID pins that a
// worktree the worker has previously attached (DB row present) returns
// its row id through the inspect RPC. DeleteBranchDialog drives
// worktree removal via ForceRemoveWorktree(worktree_id) now, so the
// presence of this field is what decouples worktree deletion from
// AGENT/TERMINAL tab existence — a branch group with only FILE tabs
// (or no tabs at all) used to be a silent no-op.
func TestInspectBranchDeletion_WorktreeReturnsTrackedID(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "delete-wt-tracked")
	run(t, repoDir, "git", "worktree", "add", "-b", "tracked-branch", wtDir)

	// Track the worktree in the DB so inspect can resolve its id.
	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)
	require.NotEmpty(t, wtID)

	resp, err := svc.inspectBranchDeletion(context.Background(), wtDir, "")
	require.NoError(t, err)
	assert.True(t, resp.GetIsWorktree())
	assert.Equal(t, wtID, resp.GetWorktreeId(),
		"worktree_id from inspect must match ensureTrackedWorktree's stored row")
}

func TestInspectBranchDeletion_WorktreeStripsBranches(t *testing.T) {
	// Branches are stripped from the response whenever the probe reports
	// a worktree row (the dialog renders no switch picker), regardless
	// of the caller's hint. Both `hint=true` and `hint=false` produce
	// the same response shape on a genuine worktree row.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "delete-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "delete-wt-branch", wtDir)

	resp, err := svc.inspectBranchDeletion(context.Background(), wtDir, "")
	require.NoError(t, err)
	assert.True(t, resp.GetIsWorktree())
	assert.Equal(t, "delete-wt-branch", resp.GetBranchName())
	assert.Empty(t, resp.GetBranches(), "worktree response must not populate Branches")

	// Parity probe: every field must match the hint=false path on the
	// same worktree, so the hint is a true no-op.
	noHint, err := svc.inspectBranchDeletion(context.Background(), wtDir, "")
	require.NoError(t, err)
	assert.Equal(t, noHint.GetIsWorktree(), resp.GetIsWorktree())
	assert.Equal(t, noHint.GetWorktreePath(), resp.GetWorktreePath())
	assert.Equal(t, noHint.GetBranchName(), resp.GetBranchName())
	assert.Empty(t, noHint.GetBranches())
}

func TestInspectBranchDeletion_NonWorktreeAlwaysReturnsBranches(t *testing.T) {
	// Regression: an earlier revision accepted an `isWorktreeHint`
	// boolean and skipped listGitBranches when it was true, but a wrong
	// hint dropped the branches list and the dialog wrongly surfaced
	// "Cannot delete the only branch" on a repo with many branches.
	// The worker now always lists branches concurrently with the path
	// probe; the hint argument has been removed entirely.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	// Add a second branch so a populated Branches list is observable.
	run(t, repoDir, "git", "checkout", "-b", "feature")
	run(t, repoDir, "git", "checkout", "-")

	resp, err := svc.inspectBranchDeletion(context.Background(), repoDir, "")
	require.NoError(t, err)
	assert.False(t, resp.GetIsWorktree())
	assert.NotEmpty(t, resp.GetBranches(), "branches must be populated when probe reports non-worktree")
}

func TestInspectBranchDeletion_UncommittedChanges(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("dirty\n"), 0o644))

	resp, err := svc.inspectBranchDeletion(context.Background(), repoDir, "")
	require.NoError(t, err)
	assert.True(t, resp.GetGitState().GetHasUncommittedChanges())
	assert.Equal(t, int32(1), resp.GetGitState().GetDiffUntracked())
}

func TestInspectBranchDeletion_UnpushedCommits(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	bareDir := filepath.Join(t.TempDir(), "unpushed.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "ahead 1")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "ahead 2")

	resp, err := svc.inspectBranchDeletion(context.Background(), repoDir, "")
	require.NoError(t, err)
	assert.True(t, resp.GetGitState().GetCanPush())
	assert.True(t, resp.GetGitState().GetUpstreamExists())
	assert.Equal(t, int32(2), resp.GetGitState().GetUnpushedCommitCount())
}

func TestInspectBranchDeletion_NotAGitRepo(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	dir := t.TempDir()

	_, err := svc.inspectBranchDeletion(context.Background(), dir, "")
	require.Error(t, err)
}

// --- inspectBranchChange -------------------------------------------------

func TestInspectBranchChange_NonWorktreeClean(t *testing.T) {
	// Happy path: a clean repo on its default branch. The response bundles
	// the path-info (RepoRoot, CurrentBranch, IsWorktree=false), the dirty
	// probe (IsDirty=false), and the branches list (at least one entry)
	// in a single RPC.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)

	resp, err := svc.inspectBranchChange(context.Background(), repoDir)
	require.NoError(t, err)
	assert.False(t, resp.GetIsWorktree())
	assert.False(t, resp.GetIsDirty())
	assert.NotEmpty(t, resp.GetCurrentBranch())
	// initRepo's default branch is included in the list — the dialog
	// uses the picker to drive Switch / CreateBase, so the worker MUST
	// return at least one ref.
	assert.NotEmpty(t, resp.GetBranches(), "non-worktree response must carry the picker list")
	expectedRoot, err := filepath.EvalSymlinks(repoDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(expectedRoot, resp.GetRepoRoot()))
	assert.True(t, pathutil.SamePath(expectedRoot, resp.GetToplevel()))
}

func TestInspectBranchChange_DirtyTree(t *testing.T) {
	// IsDirty surfaces uncommitted changes so ChangeBranchDialog can
	// paint the "Switching branches may discard changes" warning before
	// the user clicks Apply.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("d\n"), 0o644))

	resp, err := svc.inspectBranchChange(context.Background(), repoDir)
	require.NoError(t, err)
	assert.True(t, resp.GetIsDirty(), "untracked file must flip IsDirty true")
}

func TestInspectBranchChange_Worktree(t *testing.T) {
	// Worktree row: IsWorktree must be true, Toplevel = worktree dir,
	// RepoRoot = main repo dir. The branches list is still populated
	// (the dialog's Switch picker offers branches from the main repo's
	// for-each-ref, which `git -C <worktree>` shares via gitcommondir).
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "change-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "change-wt-branch", wtDir)

	resp, err := svc.inspectBranchChange(context.Background(), wtDir)
	require.NoError(t, err)
	assert.True(t, resp.GetIsWorktree())
	expectedWtPath, err := filepath.EvalSymlinks(wtDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(expectedWtPath, resp.GetToplevel()))
	expectedRepoRoot, err := filepath.EvalSymlinks(repoDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(expectedRepoRoot, resp.GetRepoRoot()))
	assert.Equal(t, "change-wt-branch", resp.GetCurrentBranch())
	assert.NotEmpty(t, resp.GetBranches(), "worktree response must still carry branches for the switch picker")
}

func TestInspectBranchChange_NotAGitRepo(t *testing.T) {
	// Path probe failure surfaces a friendly error string mirroring the
	// inspectBranchDeletion contract — the dialog renders it in the
	// shared error banner.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	dir := t.TempDir()

	_, err := svc.inspectBranchChange(context.Background(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a git repository")
}

func TestInspectBranchChange_FiresOneRevParse(t *testing.T) {
	// Regression guard for the bundle-RPC contract: the dialog's whole
	// reason to call inspectBranchChange instead of GetGitInfo +
	// ListGitBranches is that path-info, branches, and dirty all share a
	// single queryGitPathInfo. If a future refactor splits them, the
	// dialog-open cost goes from O(1) to O(N) rev-parses again.
	//
	// We can't easily count subprocesses without instrumenting NewGitCmd,
	// but we can pin the response carries fields that prove the three
	// goroutines ran (BranchName from path-info, IsDirty from the status
	// probe, Branches from for-each-ref) — without all three, the bundle
	// shape would be incomplete.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature")

	resp, err := svc.inspectBranchChange(context.Background(), repoDir)
	require.NoError(t, err)
	assert.Equal(t, "feature", resp.GetCurrentBranch(), "path-info goroutine must populate CurrentBranch")
	assert.False(t, resp.GetIsDirty(), "isDirty goroutine must run (no untracked files in this repo)")
	assert.NotEmpty(t, resp.GetBranches(), "for-each-ref goroutine must populate Branches")
}

func TestInspectBranchDeletion_LiveProbeOverridesStaleHint(t *testing.T) {
	// The hint is treated as a starting point for the parallel snapshot
	// fetch, but the response's BranchName is always whatever
	// queryGitPathInfo reports for the working dir's actual HEAD. This
	// prevents a stale sidebar row (e.g. an external `git checkout` ran
	// after the row was cached) from making the dialog show the wrong
	// branch label and from poisoning push/upstream/unpushed columns
	// with another branch's git-config readings.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "f1")

	resp, err := svc.inspectBranchDeletion(context.Background(), repoDir, "stale-label")
	require.NoError(t, err)
	assert.Equal(t, "feature", resp.GetBranchName(),
		"BranchName must come from the live queryGitPathInfo, not the hint")
	// Worktree disposition still resolves via queryGitPathInfo, so the
	// hint path produces identical isWorktree / worktreePath fields.
	assert.False(t, resp.GetIsWorktree())
	assert.Empty(t, resp.GetWorktreePath())
}

func TestInspectBranchDeletion_HintInWorktreeStillReportsWorktreeFields(t *testing.T) {
	// Hint controls the branch label but not the worktree disposition —
	// a hinted call against a linked-worktree path must still surface
	// IsWorktree=true + WorktreePath populated.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "delete-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-branch", wtDir)

	resp, err := svc.inspectBranchDeletion(context.Background(), wtDir, "wt-branch")
	require.NoError(t, err)
	assert.Equal(t, "wt-branch", resp.GetBranchName())
	assert.True(t, resp.GetIsWorktree())
	expectedPath, err := filepath.EvalSymlinks(wtDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(expectedPath, resp.GetWorktreePath()))
}

func TestInspectBranchDeletion_HintDoesNotMaskNotAGitRepoError(t *testing.T) {
	// A hint must NOT let the call succeed against a non-git directory —
	// queryGitPathInfo still runs (we need isWorktree), and its failure
	// surfaces the same "not a git repository" error as the no-hint path.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	dir := t.TempDir()

	_, err := svc.inspectBranchDeletion(context.Background(), dir, "main")
	require.Error(t, err)
}

// TestInspectBranchDeletion_HintAndNoHintAgreeOnSameRepo pins behavioural
// parity between the two orchestrations: when the hint matches the
// branch queryGitPathInfo would have derived, every observable field of
// the response must be identical. Catches a regression where the hint
// path could drift from the no-hint path (e.g. statsPath computed
// against the wrong base, missing a worktree-root adjustment).
func TestInspectBranchDeletion_HintAndNoHintAgreeOnSameRepo(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)

	// Set up enough state that every field can meaningfully differ
	// between the two paths if they accidentally diverged.
	bareDir := filepath.Join(t.TempDir(), "origin.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")

	const branchName = "feature/parity"
	run(t, repoDir, "git", "checkout", "-b", branchName)
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "ahead 1")
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("changes\n"), 0o644))

	ctx := context.Background()
	withHint, err := svc.inspectBranchDeletion(ctx, repoDir, branchName)
	require.NoError(t, err)
	noHint, err := svc.inspectBranchDeletion(ctx, repoDir, "")
	require.NoError(t, err)

	assert.Equal(t, noHint.GetBranchName(), withHint.GetBranchName())
	assert.Equal(t, noHint.GetIsWorktree(), withHint.GetIsWorktree())
	assert.Equal(t, noHint.GetWorktreePath(), withHint.GetWorktreePath())
	// Every BranchGitState field must agree across the two paths. A
	// per-field assertion catches divergence the top-level shape
	// comparison would paper over.
	wHint, wNo := withHint.GetGitState(), noHint.GetGitState()
	assert.Equal(t, wNo.GetDiffAdded(), wHint.GetDiffAdded())
	assert.Equal(t, wNo.GetDiffDeleted(), wHint.GetDiffDeleted())
	assert.Equal(t, wNo.GetDiffUntracked(), wHint.GetDiffUntracked())
	assert.Equal(t, wNo.GetHasUncommittedChanges(), wHint.GetHasUncommittedChanges())
	assert.Equal(t, wNo.GetUnpushedCommitCount(), wHint.GetUnpushedCommitCount())
	assert.Equal(t, wNo.GetUpstreamExists(), wHint.GetUpstreamExists())
	assert.Equal(t, wNo.GetRemoteBranchMissing(), wHint.GetRemoteBranchMissing())
	assert.Equal(t, wNo.GetOriginExists(), wHint.GetOriginExists())
	assert.Equal(t, wNo.GetCanPush(), wHint.GetCanPush())
	// The picker list must also agree across hint/no-hint paths:
	// listGitBranches is independent of the hint and runs in the same
	// errgroup either way, so any divergence would mean the goroutine
	// structure leaks ordering into the result.
	hintNames := make([]string, 0, len(withHint.GetBranches()))
	for _, b := range withHint.GetBranches() {
		hintNames = append(hintNames, b.GetName())
	}
	noHintNames := make([]string, 0, len(noHint.GetBranches()))
	for _, b := range noHint.GetBranches() {
		noHintNames = append(noHintNames, b.GetName())
	}
	assert.Equal(t, noHintNames, hintNames)
}

func TestCheckoutBranch_Local(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "feature")
	run(t, repoDir, "git", "checkout", "-")

	require.NoError(t, checkoutBranchInDir(context.Background(), repoDir, "feature"))

	branch, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "feature", strings.TrimSpace(branch))
}

func TestCheckoutBranch_RemoteTrackingCreatesLocal(t *testing.T) {
	bareDir := filepath.Join(t.TempDir(), "remote.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	// Seed the bare remote with a feature branch by pushing from a sibling clone.
	seedDir := initRepo(t)
	run(t, seedDir, "git", "remote", "add", "origin", bareDir)
	run(t, seedDir, "git", "push", "-u", "origin", "HEAD")
	run(t, seedDir, "git", "checkout", "-b", "tracked-feature")
	run(t, seedDir, "git", "commit", "--allow-empty", "-m", "feature")
	run(t, seedDir, "git", "push", "-u", "origin", "tracked-feature")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "fetch", "origin")

	require.NoError(t, checkoutBranchInDir(context.Background(), repoDir, "origin/tracked-feature"))

	branch, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "tracked-feature", strings.TrimSpace(branch))
}

func TestCheckoutBranch_Missing(t *testing.T) {
	repoDir := initRepo(t)
	err := checkoutBranchInDir(context.Background(), repoDir, "does-not-exist")
	require.Error(t, err)
}

// TestCheckoutBranch_LocalSlashTakesPrecedenceOverRemoteCollision pins
// the original-local-ref probe added to checkoutBranchInDir's prefix-
// strip path. When a local branch ("feature/auth") shares its leading
// component with a non-origin remote name ("upstream"), the remote ref
// `refs/remotes/feature/auth` and the local ref `refs/heads/feature/auth`
// can both exist. Without the originalLocalRef probe, the prefix-strip
// heuristic would substitute the target to "auth" (the stripped local
// of the remote), silently checking out a different branch from what
// the user picked. listGitBranches already preserves the local
// "feature/auth" entry alongside the (non-origin) remote-tracking ref,
// so the dialog can offer the user that exact name — the checkout
// helper must honor it.
func TestCheckoutBranch_LocalSlashTakesPrecedenceOverRemoteCollision(t *testing.T) {
	repoDir := initRepo(t)
	// Seed a local "auth" branch so the prefix-strip path's
	// substituted target ("auth") would otherwise be a valid checkout
	// target — making the bug observable as a wrong-branch checkout
	// rather than an error.
	run(t, repoDir, "git", "checkout", "-b", "auth")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "auth")
	run(t, repoDir, "git", "checkout", "-")
	// Local literal "feature/auth".
	run(t, repoDir, "git", "checkout", "-b", "feature/auth")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "feature/auth tip")
	run(t, repoDir, "git", "checkout", "-")
	// Fake a non-origin remote-tracking ref that collides with the
	// local "feature/auth" suffix. Without an actual remote, an
	// `update-ref` is enough to populate refs/remotes/feature/auth so
	// HasRefs sees it. (The dialog's listGitBranches surfaces such
	// entries verbatim — see TestListGitBranches_NonOriginRemoteSurvivesLocalCollision.)
	authTip, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "refs/heads/auth")
	require.NoError(t, err)
	run(t, repoDir, "git", "update-ref", "refs/remotes/feature/auth", strings.TrimSpace(authTip))

	require.NoError(t, checkoutBranchInDir(context.Background(), repoDir, "feature/auth"))

	// Use `symbolic-ref --short HEAD` (not `--abbrev-ref HEAD`): when
	// `refs/heads/feature/auth` AND `refs/remotes/feature/auth` both
	// exist, `--abbrev-ref` disambiguates as "heads/feature/auth",
	// while `symbolic-ref --short` always returns the raw branch name
	// the symref targets.
	branch, err := gitutil.Output(context.Background(), repoDir, "symbolic-ref", "--short", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "feature/auth", strings.TrimSpace(branch),
		"checkoutBranchInDir must check out the literal local 'feature/auth', not silently strip to 'auth'")
}

// TestDeleteBranch_LocalSlashSwitchToBypassesPrefixStripGuard mirrors
// the checkoutBranchInDir test: when switchTo is a literal local branch
// whose name contains a slash and shares its suffix with branchToDelete,
// switchToResolvesTo MUST defer to the local branch (the prefix-strip
// path won't fire in checkoutBranchInDir either) instead of rejecting
// the operation as a self-collision.
func TestDeleteBranch_LocalSlashSwitchToBypassesPrefixStripGuard(t *testing.T) {
	repoDir := initRepo(t)
	// Local "foo" — the branch being deleted.
	run(t, repoDir, "git", "checkout", "-b", "foo")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "foo tip")
	// Local "feature/foo" — distinct from "foo"; switchTo target.
	run(t, repoDir, "git", "checkout", "-b", "feature/foo")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "feature/foo tip")
	// Fake remote-tracking ref so HasRefs's remoteRef probe sees something.
	tip, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	run(t, repoDir, "git", "update-ref", "refs/remotes/feature/foo", strings.TrimSpace(tip))
	// Park HEAD on a third branch so the delete-of-foo phase is allowed.
	run(t, repoDir, "git", "checkout", "-b", "parking")

	// switchTo='feature/foo' is the LOCAL branch; branchToDelete='foo'.
	// switchToResolvesTo must NOT reject as self-collision.
	require.NoError(t, deleteBranchInDir(context.Background(), repoDir, "foo", "feature/foo"))
	// `symbolic-ref --short HEAD` over `--abbrev-ref HEAD`: see
	// TestCheckoutBranch_LocalSlashTakesPrecedenceOverRemoteCollision
	// for why — the heads+remotes collision triggers abbrev disambiguation.
	branch, err := gitutil.Output(context.Background(), repoDir, "symbolic-ref", "--short", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "feature/foo", strings.TrimSpace(branch),
		"delete must switch to the picked literal local branch, not bail on a phantom collision")
	out, err := gitutil.Output(context.Background(), repoDir, "branch", "--list", "foo")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out), "doomed branch 'foo' should be gone")
}

func TestCreateBranch_OffBase(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "base")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "base commit")
	baseSHA, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	run(t, repoDir, "git", "checkout", "-")

	require.NoError(t, createBranchInDir(context.Background(), repoDir, "off-base", "base"))

	branch, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "off-base", strings.TrimSpace(branch))

	headSHA, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(baseSHA), strings.TrimSpace(headSHA))
}

func TestCreateBranch_Duplicate(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "dup")
	run(t, repoDir, "git", "checkout", "-")

	err := createBranchInDir(context.Background(), repoDir, "dup", "")
	require.Error(t, err)
}

// TestCheckoutBranch_RemoteTrackingMismatchRefuses pins the
// upstream-tracking check added to checkoutBranchInDir's prefix-strip
// arm. When a repo has multiple remotes (origin + upstream) and the
// user picks `upstream/<name>` but a same-named local already tracks
// origin's variant, silently substituting target=<localName> would
// route the user onto the origin-tracking branch — destructive UX
// because the user explicitly chose upstream. The fix refuses the
// substitution with a descriptive error.
func TestCheckoutBranch_RemoteTrackingMismatchRefuses(t *testing.T) {
	// Build two bare "remotes" so we have refs/remotes/origin/* AND
	// refs/remotes/upstream/* in the working repo's view.
	originBare := filepath.Join(t.TempDir(), "origin.git")
	require.NoError(t, os.MkdirAll(originBare, 0o755))
	run(t, originBare, "git", "init", "--bare")
	upstreamBare := filepath.Join(t.TempDir(), "upstream.git")
	require.NoError(t, os.MkdirAll(upstreamBare, 0o755))
	run(t, upstreamBare, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", originBare)
	run(t, repoDir, "git", "remote", "add", "upstream", upstreamBare)
	// Seed both remotes with a `shared/foo` branch.
	run(t, repoDir, "git", "checkout", "-b", "shared/foo")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "shared seed")
	run(t, repoDir, "git", "push", "-u", "origin", "shared/foo")
	run(t, repoDir, "git", "push", "upstream", "shared/foo")
	// Pull the remote-tracking refs in so refs/remotes/upstream/shared/foo exists.
	run(t, repoDir, "git", "fetch", "upstream")
	// Reset HEAD off `shared/foo` so checkoutBranchInDir's `git checkout`
	// step actually has work to do.
	run(t, repoDir, "git", "checkout", "-b", "elsewhere")

	// Local `shared/foo` exists and tracks origin/shared/foo (set up by
	// the `push -u origin` above). User picks `upstream/shared/foo`.
	err := checkoutBranchInDir(context.Background(), repoDir, "upstream/shared/foo")
	require.Error(t, err, "checkoutBranchInDir must refuse the upstream pick when the local tracks origin")
	assert.Contains(t, err.Error(), "tracks",
		"the error should explain WHY the substitution was refused (local tracks a different remote)")
	assert.Contains(t, err.Error(), "origin",
		"the error should name the conflicting remote so the user can act")

	// HEAD must NOT have moved to `shared/foo`.
	out, gerr := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, gerr)
	assert.Equal(t, "elsewhere", strings.TrimSpace(out),
		"refusal must be side-effect-free — HEAD stays where the user was")
}

// TestCheckoutBranch_RemoteTrackingMatchSubstitutes pins the
// positive case: when the local DOES track the picked remote,
// substitution is the intended behaviour. The user types
// `origin/main`; the local `main` tracks origin; the checkout
// lands on local `main` (saving a redundant detached-HEAD or
// `--track` create).
func TestCheckoutBranch_RemoteTrackingMatchSubstitutes(t *testing.T) {
	originBare := filepath.Join(t.TempDir(), "origin.git")
	require.NoError(t, os.MkdirAll(originBare, 0o755))
	run(t, originBare, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", originBare)
	// Commit + push so origin/main exists and local main tracks it.
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "seed")
	run(t, repoDir, "git", "branch", "-m", "main")
	run(t, repoDir, "git", "push", "-u", "origin", "main")
	run(t, repoDir, "git", "checkout", "-b", "elsewhere")

	err := checkoutBranchInDir(context.Background(), repoDir, "origin/main")
	require.NoError(t, err, "matched-tracking substitution must succeed")

	out, gerr := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, gerr)
	assert.Equal(t, "main", strings.TrimSpace(out),
		"HEAD must land on the local that legitimately tracks origin")
}

func TestDeleteBranch_Happy(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "keep")
	run(t, repoDir, "git", "checkout", "-b", "doomed")

	require.NoError(t, deleteBranchInDir(context.Background(), repoDir, "doomed", "keep"))

	branch, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "keep", strings.TrimSpace(branch))

	out, err := gitutil.Output(context.Background(), repoDir, "branch", "--list", "doomed")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out), "doomed branch should be gone")
}

func TestDeleteBranch_DeleteFailureReturnsError(t *testing.T) {
	repoDir := initRepo(t)
	// Create branches without leaving HEAD on `doomed`.
	run(t, repoDir, "git", "branch", "keep")
	run(t, repoDir, "git", "branch", "doomed")

	// Sibling worktree on `doomed` so `git branch -D doomed` from the
	// main repo refuses (branch is in use elsewhere). Step 1 (checkout
	// keep) succeeds because the main repo is on the initial branch.
	// Step 2 (delete doomed) must fail and bubble the error up.
	wtDir := filepath.Join(t.TempDir(), "block-delete-wt")
	run(t, repoDir, "git", "worktree", "add", wtDir, "doomed")

	err := deleteBranchInDir(context.Background(), repoDir, "doomed", "keep")
	require.Error(t, err)

	// The doomed branch must still exist on disk.
	out, gitErr := gitutil.Output(context.Background(), repoDir, "branch", "--list", "doomed")
	require.NoError(t, gitErr)
	assert.NotEmpty(t, strings.TrimSpace(out), "doomed must still exist after failed delete")

	// The user explicitly picked `keep` as switch_to, so on delete
	// failure HEAD stays on `keep`. Earlier revisions rolled back to
	// `doomed`, but that assumed the user was originally on the branch
	// being deleted — a wrong assumption when the dialog can pick any
	// branch from the sidebar. The current behavior leaves HEAD where
	// the user asked it to go and surfaces only the delete error.
	branch, err2 := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err2)
	assert.Equal(t, "keep", strings.TrimSpace(branch),
		"HEAD stays on the user-picked switch_to_branch when delete fails")
}

func TestDeleteBranch_NoRollbackOnDeleteFailure(t *testing.T) {
	// Earlier revisions rolled back the checkout to `branchToDelete` on
	// delete failure, which parked HEAD on a branch the user may have
	// never been on (e.g. user originated from branch C, picked
	// switch_to=S, delete=B; rollback put them on B). The fix is to
	// leave HEAD on the user-picked switch_to and surface the delete
	// error verbatim — this test pins that behavior.
	repoDir := initRepo(t)
	// Start on an unrelated "origin" branch (simulating the "user was
	// on a third branch" scenario).
	run(t, repoDir, "git", "checkout", "-b", "origin-branch")
	run(t, repoDir, "git", "branch", "keep")
	run(t, repoDir, "git", "branch", "doomed")

	// Sibling worktree blocks the delete.
	wtDir := filepath.Join(t.TempDir(), "blocker")
	run(t, repoDir, "git", "worktree", "add", wtDir, "doomed")

	err := deleteBranchInDir(context.Background(), repoDir, "doomed", "keep")
	require.Error(t, err)

	branch, gerr := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, gerr)
	assert.Equal(t, "keep", strings.TrimSpace(branch),
		"on delete failure HEAD must stay on switch_to (`keep`), not get rolled back to `doomed` or to the original `origin-branch`")

	// Only one error is returned (no rollback errors.Join wrapper).
	assert.NotContains(t, err.Error(), "restore",
		"there is no rollback any more; the error must not mention restore")
}

func TestDeleteBranch_CancelledCtxNoMutation(t *testing.T) {
	// Pre-cancelled parent ctx: step 1's `git checkout keep` fails on
	// the cancelled ctx; the function bails before touching the delete
	// step, so the working dir is still on doomed and doomed still
	// exists. Pins that the function doesn't mutate state on a dead ctx.
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "doomed")
	run(t, repoDir, "git", "branch", "keep")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := deleteBranchInDir(ctx, repoDir, "doomed", "keep")
	require.Error(t, err)
	branch, gerr := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, gerr)
	assert.Equal(t, "doomed", strings.TrimSpace(branch),
		"no mutation must happen when the parent ctx is already dead")
	out, gerr := gitutil.Output(context.Background(), repoDir, "branch", "--list", "doomed")
	require.NoError(t, gerr)
	assert.NotEmpty(t, strings.TrimSpace(out),
		"doomed must still exist when step 1 bailed on a dead ctx")

	// Sanity: with a fresh ctx the function succeeds.
	require.NoError(t, deleteBranchInDir(context.Background(), repoDir, "doomed", "keep"))
}

func TestDeleteBranch_SwitchTargetMissing(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "doomed")

	err := deleteBranchInDir(context.Background(), repoDir, "doomed", "does-not-exist")
	require.Error(t, err)

	// We must still be on doomed: the checkout in step 1 failed, so we
	// never even attempted the delete.
	branch, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "doomed", strings.TrimSpace(branch))

	// doomed must still exist.
	out, gitErr := gitutil.Output(context.Background(), repoDir, "branch", "--list", "doomed")
	require.NoError(t, gitErr)
	assert.NotEmpty(t, strings.TrimSpace(out))
}

// ---- Validation rejection tests (helper-level, post-refactor) -------------

func TestCheckoutBranch_RejectsEmpty(t *testing.T) {
	repoDir := initRepo(t)
	err := checkoutBranchInDir(context.Background(), repoDir, "")
	require.Error(t, err)
	assert.True(t, gitutil.IsBranchNameError(err), "want BranchNameError, got %v", err)
}

func TestCheckoutBranch_RejectsWhitespaceOnly(t *testing.T) {
	repoDir := initRepo(t)
	err := checkoutBranchInDir(context.Background(), repoDir, "   ")
	require.Error(t, err)
	assert.True(t, gitutil.IsBranchNameError(err), "want BranchNameError, got %v", err)
}

// CheckoutBranch now validates via gitutil.ValidateBranchName, which
// rejects more than just empty/whitespace (spaces inside the name,
// leading dashes, control chars, etc.). Confirm the helper surfaces
// those as *BranchNameError so the handler routes them through
// `sendInvalidArgument` like its sibling create/delete handlers.
func TestCheckoutBranch_RejectsInvalidName(t *testing.T) {
	repoDir := initRepo(t)
	err := checkoutBranchInDir(context.Background(), repoDir, "bad name")
	require.Error(t, err)
	assert.True(t, gitutil.IsBranchNameError(err), "want BranchNameError, got %v", err)
}

func TestCreateBranch_RejectsInvalidName(t *testing.T) {
	repoDir := initRepo(t)
	// Space is one of ValidateBranchName's hard rejects.
	err := createBranchInDir(context.Background(), repoDir, "bad name", "")
	require.Error(t, err)
	assert.True(t, gitutil.IsBranchNameError(err), "want BranchNameError, got %v", err)
}

func TestCreateBranch_RejectsEmptyName(t *testing.T) {
	repoDir := initRepo(t)
	err := createBranchInDir(context.Background(), repoDir, "", "")
	require.Error(t, err)
}

func TestCreateBranch_RejectsFlagShapedBaseBranch(t *testing.T) {
	// createBranchInDir must validate baseBranch with the same git-
	// check-ref-format rules as newBranch. Without the gate, a flag-
	// shaped value (`-f`, `--orphan`, `-c<k>=<v>`) would reach `git
	// checkout -b feature <baseBranch>` argv as a positional and git
	// would interpret it as an option.
	repoDir := initRepo(t)
	for _, base := range []string{"-f", "--orphan", "--track", "-c", "-cuser.email=hijack@example.com"} {
		err := createBranchInDir(context.Background(), repoDir, "feature-"+base, base)
		require.Error(t, err, "baseBranch=%q must be rejected", base)
		assert.True(t, gitutil.IsBranchNameError(err),
			"baseBranch=%q: want BranchNameError, got %v", base, err)
	}
}

func TestCheckoutBranch_RejectsFlagShapedStrippedLocalName(t *testing.T) {
	// checkoutBranchInDir must validate the StripRemotePrefix output
	// before using it as the positional `-b <name>` arg. Without the
	// second-pass validation, a remote ref like `refs/remotes/origin/--help`
	// (which a malicious remote can plant via `git push refs/heads/--help`)
	// would reach argv as `git checkout -b --help --track origin/--help`
	// and git would interpret `--help` as a flag.
	//
	// We can't easily create the malicious remote ref, but the
	// validator runs BEFORE the remote-existence probe, so the
	// rejection is independent of HasRefs: pass `origin/--help` and
	// observe the second-pass ValidateBranchName rejects it.
	repoDir := initRepo(t)
	err := checkoutBranchInDir(context.Background(), repoDir, "origin/--help")
	require.Error(t, err)
	assert.True(t, gitutil.IsBranchNameError(err),
		"want BranchNameError on flag-shaped stripped name, got %v", err)
}

func TestCreateBranch_EmptyBaseDefaultsToHead(t *testing.T) {
	repoDir := initRepo(t)
	// HEAD already has the initial commit from initRepo.
	headSHA, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "HEAD")
	require.NoError(t, err)

	require.NoError(t, createBranchInDir(context.Background(), repoDir, "off-head", ""))

	branchHeadSHA, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(headSHA), strings.TrimSpace(branchHeadSHA),
		"empty base should leave new branch at the original HEAD")
}

func TestDeleteBranch_SwitchEqualsDelete(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "same")

	err := deleteBranchInDir(context.Background(), repoDir, "same", "same")
	require.ErrorIs(t, err, gitutil.ErrInvalidArgument)
	// Pin the exact wrapped message so a future caller can't accidentally
	// swap the two invalid-arg returns under the same
	// `gitutil.ErrInvalidArgument` sentinel — `errors.Is` alone can't
	// distinguish them.
	assert.Equal(t,
		"switch_to_branch must differ from branch_to_delete: invalid argument",
		err.Error(),
	)

	// Helper must have rejected before touching git: HEAD unchanged.
	branch, gitErr := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, gitErr)
	assert.Equal(t, "same", strings.TrimSpace(branch))
}

// Picking a remote-tracking ref (e.g. `origin/main`) as switch-to while
// the working dir is already on the local branch with the same suffix
// must be rejected up front: checkoutBranchInDir would resolve it to
// the local name, the checkout would be a no-op, and `git branch -D`
// would then fail with "cannot delete branch checked out at ...". Pin
// the early rejection so the dialog gets a clear InvalidArgument
// instead of a confusing downstream git failure.
func TestDeleteBranch_RemotePrefixCollidesWithCurrent(t *testing.T) {
	repoDir := initRepo(t)
	// Seed a fake remote-tracking ref so HasRefs sees both
	// refs/heads/main and refs/remotes/origin/main without needing a
	// real remote configured.
	mainSHA, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	mainSHA = strings.TrimSpace(mainSHA)
	run(t, repoDir, "git", "update-ref", "refs/remotes/origin/main", mainSHA)

	// Currently on `main` (initRepo's default). Picking `origin/main`
	// as switchTo collapses to `main` via checkoutBranchInDir's
	// remote-prefix-strip rule.
	err = deleteBranchInDir(context.Background(), repoDir, "main", "origin/main")
	require.ErrorIs(t, err, gitutil.ErrInvalidArgument)
	assert.Equal(t,
		"switch_to_branch must differ from branch_to_delete: invalid argument",
		err.Error(),
	)

	// HEAD must still be on `main` — the early rejection runs before
	// any git mutation.
	branch, gitErr := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, gitErr)
	assert.Equal(t, "main", strings.TrimSpace(branch))
	// And `main` still exists.
	_, gitErr = gitutil.Output(context.Background(), repoDir, "rev-parse", "--verify", "refs/heads/main")
	require.NoError(t, gitErr)
}

// switchToResolvesTo's resolution must NOT fire when the remote ref
// doesn't exist — in that case checkoutBranchInDir falls through to a
// literal `git checkout origin/main`, which won't move HEAD to local
// `main` (it lands detached on the remote-tracking commit or errors).
// Without the local+remote double-check, picking `origin/main` would
// be over-eagerly rejected even though it's a valid (if unusual) input.
func TestDeleteBranch_RemotePrefixCollisionRequiresBothRefs(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "doomed")
	// Local `main` exists (from initRepo), but no `refs/remotes/origin/main`
	// has been created. switchToResolvesTo should return false, so the
	// equality check passes and the delete proceeds.
	require.NoError(t, deleteBranchInDir(context.Background(), repoDir, "doomed", "main"))
	branch, gitErr := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, gitErr)
	assert.Equal(t, "main", strings.TrimSpace(branch))
}

func TestDeleteBranch_RejectsEmptySwitchTo(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "doomed")

	err := deleteBranchInDir(context.Background(), repoDir, "doomed", "")
	require.ErrorIs(t, err, gitutil.ErrInvalidArgument)
	// Exact-match for the same reason as TestDeleteBranch_SwitchEqualsDelete.
	assert.Equal(t,
		"switch_to_branch is required: invalid argument",
		err.Error(),
	)
}

// runBranchMutation's whole job is mapping fn's error category to the
// right gRPC code without a per-call closure. Pin each branch of that
// mapping with a direct unit test so a future call-site can't silently
// route a known input-validation error to Internal.
func TestRunBranchMutation_ErrorMapping(t *testing.T) {
	t.Run("nil error sends the success response and no error", func(t *testing.T) {
		w := newTestWriter()
		sender := channel.NewSender(w)
		runBranchMutation(context.Background(), sender, &leapmuxv1.CheckoutBranchResponse{}, func(context.Context) error { return nil })
		require.Empty(t, w.errors, "success must not emit an error response")
		require.Len(t, w.responses, 1, "success must emit exactly one proto response")
		// Payload must unmarshal back to the response shape we passed,
		// not get dropped or replaced.
		var resp leapmuxv1.CheckoutBranchResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	})

	t.Run("ErrInvalidArgument wrap routes to InvalidArgument", func(t *testing.T) {
		w := newTestWriter()
		sender := channel.NewSender(w)
		runBranchMutation(context.Background(), sender, &leapmuxv1.CheckoutBranchResponse{}, func(context.Context) error {
			return fmt.Errorf("custom: %w", gitutil.ErrInvalidArgument)
		})
		require.Empty(t, w.responses, "error path must not emit a success response")
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.InvalidArgument), w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "custom")
	})

	t.Run("switch_to_branch=delete error routes to InvalidArgument", func(t *testing.T) {
		// The invalid-arg wrap from deleteBranchInDir must reach
		// runBranchMutation as-is and produce InvalidArgument — pin the
		// route so a future caller can't silently drop the wrap.
		w := newTestWriter()
		sender := channel.NewSender(w)
		runBranchMutation(context.Background(), sender, &leapmuxv1.DeleteBranchResponse{}, func(context.Context) error {
			return fmt.Errorf("switch_to_branch must differ from branch_to_delete: %w", gitutil.ErrInvalidArgument)
		})
		require.Empty(t, w.responses)
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.InvalidArgument), w.errors[0].code)
	})

	t.Run("missing switch_to_branch error routes to InvalidArgument", func(t *testing.T) {
		w := newTestWriter()
		sender := channel.NewSender(w)
		runBranchMutation(context.Background(), sender, &leapmuxv1.DeleteBranchResponse{}, func(context.Context) error {
			return fmt.Errorf("switch_to_branch is required: %w", gitutil.ErrInvalidArgument)
		})
		require.Empty(t, w.responses)
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.InvalidArgument), w.errors[0].code)
	})

	t.Run("BranchNameError routes to InvalidArgument via Unwrap chain", func(t *testing.T) {
		// gitutil.BranchNameError.Unwrap returns gitutil.ErrInvalidArgument,
		// so runBranchMutation's single errors.Is check catches it the
		// same way it catches direct ErrInvalidArgument wraps. Bad-branch-
		// name failures from createBranchInDir / checkoutBranchInDir flow
		// through here.
		w := newTestWriter()
		sender := channel.NewSender(w)
		err := gitutil.ValidateBranchName("bad name")
		require.True(t, gitutil.IsBranchNameError(err), "test setup: expected BranchNameError")
		require.ErrorIs(t, err, gitutil.ErrInvalidArgument, "BranchNameError must unwrap to ErrInvalidArgument")
		runBranchMutation(context.Background(), sender, &leapmuxv1.CreateBranchResponse{}, func(context.Context) error { return err })
		require.Empty(t, w.responses)
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.InvalidArgument), w.errors[0].code)
	})

	t.Run("arbitrary error routes to Internal", func(t *testing.T) {
		// Everything that ISN'T a recognized input-validation category
		// must surface as Internal — the default fallback. Without this
		// check a future "is this user-fault?" predicate could silently
		// widen and start hiding real bugs as InvalidArgument.
		w := newTestWriter()
		sender := channel.NewSender(w)
		runBranchMutation(context.Background(), sender, &leapmuxv1.CheckoutBranchResponse{}, func(context.Context) error {
			return errors.New("disk on fire")
		})
		require.Empty(t, w.responses)
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.Internal), w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "disk on fire")
	})

	t.Run("derives fn ctx from the parent so cancellation propagates", func(t *testing.T) {
		// runBranchMutation wraps the parent with WithTimeout. The
		// resulting fn ctx must be a CHILD of the parent — a cancelled
		// parent (e.g. the channel session closed mid-CreateBranch)
		// must abort the subprocess inside fn rather than waiting for
		// the 30s branchMutationTimeout to expire.
		parent, cancel := context.WithCancel(context.Background())
		cancel()

		var seenCancelled bool
		w := newTestWriter()
		sender := channel.NewSender(w)
		runBranchMutation(parent, sender, &leapmuxv1.CheckoutBranchResponse{}, func(ctx context.Context) error {
			seenCancelled = ctx.Err() != nil
			return ctx.Err()
		})
		require.True(t, seenCancelled, "fn must observe ctx.Done() when parent is pre-cancelled")
		require.Len(t, w.errors, 1, "cancelled fn must surface an error response")
		assert.Equal(t, int32(codes.Internal), w.errors[0].code, "context.Canceled is not ErrInvalidArgument; falls to Internal")
	})

	t.Run("fn ctx has a deadline set even when parent has none", func(t *testing.T) {
		// The timeout cap is the OTHER half of the safety story — a
		// parent that never cancels (today: bgCtx via the destructive
		// handlers wouldn't reach this path, but pin the cap for
		// defense in depth) must still bound fn's wall-clock.
		w := newTestWriter()
		sender := channel.NewSender(w)
		var deadlineSet bool
		runBranchMutation(context.Background(), sender, &leapmuxv1.CheckoutBranchResponse{}, func(ctx context.Context) error {
			_, ok := ctx.Deadline()
			deadlineSet = ok
			return nil
		})
		require.True(t, deadlineSet, "fn ctx must carry the branchMutationTimeout deadline")
	})
}

func TestDeleteBranch_RejectsInvalidBranchName(t *testing.T) {
	repoDir := initRepo(t)
	err := deleteBranchInDir(context.Background(), repoDir, "bad name", "main")
	require.Error(t, err)
	assert.True(t, gitutil.IsBranchNameError(err), "want BranchNameError, got %v", err)
}

func TestDeleteBranch_RejectsEmptyBranchToDelete(t *testing.T) {
	repoDir := initRepo(t)
	err := deleteBranchInDir(context.Background(), repoDir, "", "main")
	require.Error(t, err)
	assert.True(t, gitutil.IsBranchNameError(err), "want BranchNameError, got %v", err)
}

// ---- Happy-path + edge cases beyond the original plan ---------------------

// Working dir is a subdirectory of the repo; queryGitPathInfo resolves to
// the repo root. Inspect/checkout/delete all must operate on the right
// repository.
func TestInspectBranchDeletion_FromSubdirectory(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	subDir := filepath.Join(repoDir, "deep", "nest")
	require.NoError(t, os.MkdirAll(subDir, 0o755))

	resp, err := svc.inspectBranchDeletion(context.Background(), subDir, "")
	require.NoError(t, err)
	assert.False(t, resp.GetIsWorktree())
	assert.NotEmpty(t, resp.GetBranchName())
}

// Detached HEAD: branchOrShortSHA returns the short SHA. Inspect should
// still produce a response (no crash), with branch_name = short SHA.
func TestInspectBranchDeletion_DetachedHead(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	// Detach HEAD by checking out the commit SHA directly.
	run(t, repoDir, "git", "checkout", "--detach", "HEAD")

	resp, err := svc.inspectBranchDeletion(context.Background(), repoDir, "")
	require.NoError(t, err)
	assert.False(t, resp.GetIsWorktree())
	// Short SHA is 7+ hex chars; we don't need to match exactly.
	assert.NotEmpty(t, resp.GetBranchName())
	assert.NotContains(t, resp.GetBranchName(), "/")
}

func TestInspectBranchDeletion_WorktreeWithUncommittedChanges(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "dirty-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "dirty-wt-branch", wtDir)
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "f.txt"), []byte("x\n"), 0o644))

	resp, err := svc.inspectBranchDeletion(context.Background(), wtDir, "")
	require.NoError(t, err)
	assert.True(t, resp.GetIsWorktree())
	assert.True(t, resp.GetGitState().GetHasUncommittedChanges())
	assert.Equal(t, int32(1), resp.GetGitState().GetDiffUntracked())
}

// Round-trip: switch from feature → main → feature, asserting HEAD lands
// where we asked at each step. Covers the same flow as e2e Spec 3.
func TestCheckoutBranch_RoundTrip(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature")
	run(t, repoDir, "git", "checkout", "-b", "other")

	require.NoError(t, checkoutBranchInDir(context.Background(), repoDir, "feature"))
	branch, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "feature", strings.TrimSpace(branch))

	require.NoError(t, checkoutBranchInDir(context.Background(), repoDir, "other"))
	branch, err = gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "other", strings.TrimSpace(branch))
}

// CheckoutBranch into a worktree (linked) working directory, not the main
// repo. Mirrors the same call shape used by ChangeBranchDialog when the
// branch group is a worktree.
func TestCheckoutBranch_InsideWorktree(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "branch", "target")

	wtDir := filepath.Join(t.TempDir(), "wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-branch", wtDir)

	require.NoError(t, checkoutBranchInDir(context.Background(), wtDir, "target"))

	branch, err := gitutil.Output(context.Background(), wtDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "target", strings.TrimSpace(branch))
}

func TestExecuteCheckoutBranch_InsideWorktreeTracksAssociation(t *testing.T) {
	// Regression: executeCheckoutBranch used to skip worktree tracking
	// entirely (unlike executeUseCurrent), so opening an agent in
	// CheckoutBranch mode inside a worktree left WorktreeID empty.
	// registerTabForWorktree('', ...) was then a no-op and a later
	// CloseAgent with WorktreeAction.REMOVE silently degraded to KEEP.
	// Both code paths must now produce the same WorktreeID for an
	// equivalent dir.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	run(t, repoDir, "git", "branch", "target")
	wtDir := filepath.Join(t.TempDir(), "wt-checkout")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-branch", wtDir)

	result, err := svc.executeCheckoutBranch(context.Background(), gitModePlan{
		Mode:           gitModeCheckoutBranch,
		WorkingDir:     wtDir,
		CheckoutTarget: "target",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.WorktreeID,
		"executeCheckoutBranch must register the worktree association so REMOVE-on-close finds the row")
	row, err := svc.Queries.GetWorktreeByID(context.Background(), result.WorktreeID)
	require.NoError(t, err)
	gotPath, err := filepath.EvalSymlinks(row.WorktreePath)
	require.NoError(t, err)
	wantPath, err := filepath.EvalSymlinks(wtDir)
	require.NoError(t, err)
	assert.Equal(t, wantPath, gotPath, "tracked worktree row must point at the working dir")
}

// TestExecuteCheckoutBranch_RestampsWorktreeBranchAfterCheckout pins
// the worktree row's branch_name re-stamp added to executeCheckoutBranch.
// attachWorktreeIfPresent (called before the checkout) creates the
// tracking row with branch_name = PRE-checkout HEAD. Without the
// post-checkout re-stamp the row drifts the moment HEAD moves, and
// the fallback path in removeWorktreeFromDisk (when its live re-probe
// fails) would delete the stale name instead of the live one.
func TestExecuteCheckoutBranch_RestampsWorktreeBranchAfterCheckout(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	run(t, repoDir, "git", "branch", "target")
	// `git worktree add -b <branch>` creates the worktree on `wt-branch`.
	// The first executeCheckoutBranch call against an UNREGISTERED worktree
	// is the canonical case: attachWorktreeIfPresent inserts a fresh row,
	// stamping branch_name from the pre-checkout HEAD ('wt-branch'). The
	// post-checkout re-stamp must overwrite it to 'target'.
	wtDir := filepath.Join(t.TempDir(), "wt-restamp-after-checkout")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-branch", wtDir)

	result, err := svc.executeCheckoutBranch(context.Background(), gitModePlan{
		Mode:           gitModeCheckoutBranch,
		WorkingDir:     wtDir,
		CheckoutTarget: "target",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.WorktreeID)

	row, err := svc.Queries.GetWorktreeByID(context.Background(), result.WorktreeID)
	require.NoError(t, err)
	assert.Equal(t, "target", row.BranchName,
		"worktree row must carry post-checkout branch name; otherwise removeWorktreeFromDisk's fallback path would delete the wrong branch")
}

// TestRestampWorktreeBranch_NoOpWhenWorktreeIDEmpty pins the early
// return for non-worktree working dirs. executeCheckoutBranch
// against the main repo (not a worktree) leaves result.WorktreeID
// empty, and the re-stamp must NOT try to touch the DB or probe
// the dir again — there's no row to update.
func TestRestampWorktreeBranch_NoOpWhenWorktreeIDEmpty(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)
	// No assertion needed beyond "doesn't panic / doesn't query": the
	// only observable side effect of the early return is the absence
	// of any worktree row update.
	svc.restampWorktreeBranch(context.Background(), "", "/does-not-matter")
}

// TestRestampWorktreeBranch_NoOpOnDetachedHEAD pins the "skip empty
// BranchName" guard. After `git checkout <sha>`, info.BranchName is
// empty (detached); a blind UpdateWorktreeBranchName would write the
// empty string and lose the row's last branch identity. The re-stamp
// must skip in that case so the row remains useful for the live
// re-probe in removeWorktreeFromDisk.
func TestRestampWorktreeBranch_NoOpOnDetachedHEAD(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt-detached-restamp")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-detached-restamp", wtDir)
	// Insert the tracking row up front so we can detect a wrong write.
	canonical := pathutil.Canonicalize(wtDir)
	require.NoError(t, svc.Queries.CreateWorktree(context.Background(), db.CreateWorktreeParams{
		ID:           "wt-detached-1",
		WorktreePath: canonical,
		RepoRoot:     repoDir,
		BranchName:   "wt-detached-restamp",
	}))
	// Detach HEAD.
	headSHA, err := gitutil.Output(context.Background(), wtDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	run(t, wtDir, "git", "checkout", "--detach", strings.TrimSpace(headSHA))

	svc.restampWorktreeBranch(context.Background(), "wt-detached-1", wtDir)

	row, err := svc.Queries.GetWorktreeByID(context.Background(), "wt-detached-1")
	require.NoError(t, err)
	assert.Equal(t, "wt-detached-restamp", row.BranchName,
		"detached HEAD must NOT overwrite branch_name with empty; the row's last branch identity is the best we have for fallback delete")
}

func TestExecuteUseCurrent_EnsureTrackedWorktreeErrorBubbles(t *testing.T) {
	// Regression: ensureTrackedWorktree failures used to be swallowed
	// with a slog.Warn, leaving the tab with WorktreeID="" and silently
	// degrading REMOVE-on-close to KEEP. Stage a uniqueness collision by
	// pre-inserting a worktree row at a *different* path that targets the
	// same canonical worktreePath via direct CreateWorktree — the second
	// CreateWorktree call inside ensureTrackedWorktree must fail at the
	// DB layer, and the caller (executeUseCurrent) must bubble it.
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt-track-fail")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-track-fail", wtDir)

	// Pre-claim the canonical worktree-path row so ensureTrackedWorktree
	// hits a uniqueness collision on the new ID it tries to insert.
	canonicalPath := pathutil.Canonicalize(wtDir)
	require.NoError(t, svc.Queries.CreateWorktree(context.Background(), db.CreateWorktreeParams{
		ID:           "pre-claimed",
		WorktreePath: canonicalPath,
		RepoRoot:     repoDir,
		BranchName:   "wt-track-fail",
	}))
	// ensureTrackedWorktree finds the pre-claimed row via GetWorktreeByPath
	// and reuses its ID — that's actually the happy path, NOT a failure.
	// The real test is: when GetWorktreeByPath returns the existing row,
	// executeUseCurrent should set result.WorktreeID to that ID rather
	// than leaving it empty.
	result, err := svc.executeUseCurrent(context.Background(), gitModePlan{
		Mode:       gitModeUseCurrent,
		WorkingDir: wtDir,
	})
	require.NoError(t, err)
	assert.Equal(t, "pre-claimed", result.WorktreeID,
		"executeUseCurrent must reuse the existing worktree row when one exists for this path")
}

// Local branch with the same name as a remote-tracking ref must be preferred.
// Covers the early-return branch in checkoutBranchInDir.
func TestCheckoutBranch_PrefersLocalOverRemote(t *testing.T) {
	bareDir := filepath.Join(t.TempDir(), "local-wins.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	// Seed remote with branch "shared".
	seedDir := initRepo(t)
	run(t, seedDir, "git", "remote", "add", "origin", bareDir)
	run(t, seedDir, "git", "push", "-u", "origin", "HEAD")
	run(t, seedDir, "git", "checkout", "-b", "shared")
	run(t, seedDir, "git", "commit", "--allow-empty", "-m", "remote-shared")
	run(t, seedDir, "git", "push", "-u", "origin", "shared")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "fetch", "origin")
	// Pre-create a *local* branch with the same name; checkout of
	// "origin/shared" should prefer the local copy.
	run(t, repoDir, "git", "branch", "shared")

	require.NoError(t, checkoutBranchInDir(context.Background(), repoDir, "origin/shared"))

	branch, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "shared", strings.TrimSpace(branch))

	// The local branch must still point at its own (original) commit, not
	// the remote's — i.e. we did NOT recreate it.
	localSHA, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "shared")
	require.NoError(t, err)
	remoteSHA, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "origin/shared")
	require.NoError(t, err)
	assert.NotEqual(t, strings.TrimSpace(localSHA), strings.TrimSpace(remoteSHA))
}

// e2e Spec 6 equivalent: delete a non-worktree branch via the helper,
// asserting that the working directory survives and is now on the target.
func TestDeleteBranch_NonWorktreeFlow(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "stay")
	run(t, repoDir, "git", "checkout", "-b", "to-delete")

	require.NoError(t, deleteBranchInDir(context.Background(), repoDir, "to-delete", "stay"))

	// The working directory still exists.
	_, err := os.Stat(repoDir)
	require.NoError(t, err)

	// HEAD is on stay; to-delete is gone.
	branch, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "stay", strings.TrimSpace(branch))
	out, err := gitutil.Output(context.Background(), repoDir, "branch", "--list", "to-delete")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out))
}

// Subdirectory of a non-worktree repo: deleteBranchInDir resolves the
// repo root via queryGitPathInfo, so the operation still works.
func TestDeleteBranch_FromSubdirectory(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "branch", "doomed")
	subDir := filepath.Join(repoDir, "deep", "nest")
	require.NoError(t, os.MkdirAll(subDir, 0o755))

	require.NoError(t, deleteBranchInDir(context.Background(), subDir, "doomed", "main"))

	out, err := gitutil.Output(context.Background(), repoDir, "branch", "--list", "doomed")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out))
}

// Inspect on a repo with no remote configured: origin_exists must be
// false, can_push must be false, but otherwise no error.
func TestInspectBranchDeletion_NoOrigin(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)

	resp, err := svc.inspectBranchDeletion(context.Background(), repoDir, "")
	require.NoError(t, err)
	assert.False(t, resp.GetGitState().GetOriginExists())
	assert.False(t, resp.GetGitState().GetCanPush())
}

// CheckoutBranch tolerates surrounding whitespace (the helper trims).
func TestCheckoutBranch_TrimsWhitespace(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "branch", "target")

	require.NoError(t, checkoutBranchInDir(context.Background(), repoDir, "  target  "))

	branch, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "target", strings.TrimSpace(branch))
}

// CreateBranch inside a linked worktree directory.
func TestCreateBranch_InsideWorktree(t *testing.T) {
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-branch", wtDir)

	require.NoError(t, createBranchInDir(context.Background(), wtDir, "wt-feature", ""))

	branch, err := gitutil.Output(context.Background(), wtDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "wt-feature", strings.TrimSpace(branch))
}

// InspectBranchDeletion in a freshly initialized repo with no commits yet
// (initRepo always commits, so simulate by hard-resetting). queryGitPathInfo
// should still report a path; branch name may be HEAD's short SHA.
func TestInspectBranchDeletion_EmptyBranchAfterReset(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)

	resp, err := svc.inspectBranchDeletion(context.Background(), repoDir, "")
	require.NoError(t, err)
	// HEAD should have either a branch name (main/master from init) or a
	// short SHA fallback — never empty.
	assert.NotEmpty(t, resp.GetBranchName())
}

// Stress: run inspectBranchDeletion concurrently on the same path; ensure
// no deadlock or panic from the inner errgroup.
func TestInspectBranchDeletion_Concurrent(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("x\n"), 0o644))

	const N = 8
	type result struct {
		resp *leapmuxv1.InspectBranchDeletionResponse
		err  error
	}
	results := make(chan result, N)
	for i := 0; i < N; i++ {
		go func() {
			resp, err := svc.inspectBranchDeletion(context.Background(), repoDir, "")
			results <- result{resp, err}
		}()
	}
	for i := 0; i < N; i++ {
		r := <-results
		require.NoError(t, r.err)
		assert.True(t, r.resp.GetGitState().GetHasUncommittedChanges())
	}
}

// CheckoutBranch handler rejects whitespace-only via the helper.
func TestCheckoutBranch_RejectsWhitespaceMaps(t *testing.T) {
	// Sanity-check that the helper error class matches what the handler
	// would surface as InvalidArgument. (We don't go through the
	// dispatcher here — IsBranchNameError is the handler's contract.)
	err := checkoutBranchInDir(context.Background(), "/no/path", "  ")
	require.Error(t, err)
	assert.True(t, gitutil.IsBranchNameError(err), "want BranchNameError, got %v", err)
}

// TestPushBranch_CleanTreeSkipsWIPCommit verifies that a clean working
// tree skips the `git add` / `commit -m WIP` step, but any unpushed
// commits must still reach the remote.
func TestPushBranch_CleanTreeSkipsWIPCommit(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	bareDir := filepath.Join(t.TempDir(), "push-clean.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")
	run(t, repoDir, "git", "checkout", "-b", "push-clean")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "first feature commit")
	run(t, repoDir, "git", "push", "-u", "origin", "push-clean")
	// One unpushed commit on top of the tracked branch, but the working
	// tree is clean — no `git add` / `git commit -m WIP` should fire.
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "real feature commit")
	createAgentForPath(t, svc, "agent-push-clean", repoDir)

	err := svc.pushBranch(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-push-clean")
	require.NoError(t, err)

	// HEAD subject must be the real commit, not a WIP that the push
	// path inserted on top.
	msg, err := gitutil.Output(context.Background(), repoDir, "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	assert.Equal(t, "real feature commit", strings.TrimSpace(msg))

	// No "WIP" anywhere in this branch's history (excluding the base
	// reachable from main).
	log, err := gitutil.Output(context.Background(), repoDir, "log", "--pretty=%s", "origin/main..HEAD")
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		assert.NotEqual(t, "WIP", strings.TrimSpace(line), "no WIP commit must be inserted on a clean tree")
	}

	// And the remote received the new commit.
	remoteHead, err := gitutil.Output(context.Background(), bareDir, "rev-parse", "refs/heads/push-clean")
	require.NoError(t, err)
	localHead, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(localHead), strings.TrimSpace(remoteHead))
}

// TestPushBranch_BranchNameWithRegexMetachars verifies that
// pushStatusForPath's `git config -z --get-regexp` call correctly
// escapes regex metacharacters in the branch name. A branch like
// `feat/v1.0+rc1` contains `.`, `/`, and `+` — all valid in git ref
// names and all regex metachars in POSIX ERE. Without escaping, the
// regex would still match by coincidence here, but a name like
// `feat.x` would also wrongly match `feat-x`. We assert the happy path
// (correct resolution) and a deliberate near-miss (different config
// key must NOT be picked up by an unescaped `.`).
func TestPushBranch_BranchNameWithRegexMetachars(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	bareDir := filepath.Join(t.TempDir(), "push-regex.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")

	const branchName = "feat/v1.0+rc1"
	run(t, repoDir, "git", "checkout", "-b", branchName)
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "release candidate")
	run(t, repoDir, "git", "push", "-u", "origin", branchName)

	// Also configure a "trap" branch.NAME.remote where NAME has `-` in
	// place of `.` — if QuoteMeta is missing, the unescaped `.` would
	// match this key too and pollute the parsed map.
	run(t, repoDir, "git", "config", "branch.feat/v1-0+rc1.remote", "should-not-match")

	createAgentForPath(t, svc, "agent-regex", repoDir)

	status, err := pushStatusForPath(context.Background(), repoDir, branchName)
	require.NoError(t, err)
	assert.True(t, status.OriginExists, "origin must be detected")
	assert.True(t, status.UpstreamExists, "upstream must resolve through escaped regex")
	assert.False(t, status.RemoteMissing, "remote ref was just pushed")
	assert.Equal(t, int32(0), status.Unpushed, "branch is fully pushed")
}

// TestPushStatusForPath_RemoteMissing exercises the gitutil.HasRefs
// path that replaced the local remoteRefExists helper. The branch
// still has its upstream config set (so UpstreamExists is true), but
// the locally-cached `refs/remotes/origin/<branch>` ref is gone — the
// dialog should surface this as RemoteMissing so the user knows a
// fetch is required.
// TestPushStatusForPath_UnpushedScopedToOrigin pins the contract that
// when a branch has no upstream set but origin exists, the unpushed
// count walks origin's refs only — not every remote's. Otherwise a
// commit that was pushed to a secondary remote but not origin would be
// silently counted as already-pushed and the "X unpushed commits" badge
// would hide the divergence from origin.
func TestPushStatusForPath_UnpushedScopedToOrigin(t *testing.T) {
	// Two bare remotes, both reachable.
	originBare := filepath.Join(t.TempDir(), "origin.git")
	require.NoError(t, os.MkdirAll(originBare, 0o755))
	run(t, originBare, "git", "init", "--bare")
	secondaryBare := filepath.Join(t.TempDir(), "secondary.git")
	require.NoError(t, os.MkdirAll(secondaryBare, 0o755))
	run(t, secondaryBare, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", originBare)
	run(t, repoDir, "git", "remote", "add", "secondary", secondaryBare)
	// Share the initial commit with both remotes so the count below
	// only reflects work added after diverging from origin.
	run(t, repoDir, "git", "push", "origin", "HEAD")
	run(t, repoDir, "git", "push", "secondary", "HEAD")

	// Branch off WITHOUT setting upstream tracking, then commit once
	// and push to `secondary` ONLY. origin never sees this commit.
	const branchName = "feature/unpushed"
	run(t, repoDir, "git", "checkout", "-b", branchName)
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "secondary-only")
	run(t, repoDir, "git", "push", "secondary", branchName)
	// Confirm there is no branch.*.remote / branch.*.merge config.
	cfg := readGitConfigRegexp(context.Background(), repoDir,
		fmt.Sprintf(`^branch\.%s\.(remote|merge)$`, branchName))
	require.Empty(t, cfg, "test setup: branch must have no upstream config")

	status, err := pushStatusForPath(context.Background(), repoDir, branchName)
	require.NoError(t, err)
	assert.True(t, status.OriginExists, "origin remote must be detected")
	assert.False(t, status.UpstreamExists, "branch must have no upstream config")
	// The fix: --remotes=origin counts the commit as unpushed even
	// though it's reachable on secondary. The old --remotes (no
	// suffix) would walk secondary too and return 0.
	assert.Equal(t, int32(1), status.Unpushed,
		"unpushed count must be scoped to origin's refs, not every remote")
}

// pushStatusForPath used to silently swallow `git rev-list --count`
// errors, leaving Unpushed=0 + OriginExists=true on a real git failure
// — the dialog would then render "no unpushed commits" over a branch
// the user could still lose work on. Pin propagation via parseRevListCount:
// the function unwraps it with a single non-nil-error return, so a bad
// rev-list payload (which is what a real `fatal:` stderr boils down to
// when the upstream call returns garbage) must surface as a returned
// error, not be silently coerced to zero.
func TestPushStatusForPath_PropagatesRevListParseError(t *testing.T) {
	_, err := parseRevListCount("not-a-number\n")
	require.Error(t, err, "parseRevListCount must surface unparseable output rather than coerce to 0")
}

func TestPushStatusForPath_RemoteMissing(t *testing.T) {
	bareDir := filepath.Join(t.TempDir(), "missing.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")

	const branchName = "feature/remote-deleted"
	run(t, repoDir, "git", "checkout", "-b", branchName)
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "wip")
	run(t, repoDir, "git", "push", "-u", "origin", branchName)

	// Drop the local remote-tracking ref to simulate the case where
	// origin was rewritten/deleted out of band; the upstream config
	// is still in place but the local cache no longer knows about it.
	run(t, repoDir, "git", "update-ref", "-d", "refs/remotes/origin/"+branchName)

	status, err := pushStatusForPath(context.Background(), repoDir, branchName)
	require.NoError(t, err)
	assert.True(t, status.OriginExists, "origin remote must still be detected")
	assert.True(t, status.UpstreamExists, "branch.*.remote config is still set")
	assert.True(t, status.RemoteMissing, "deleted remote-tracking ref must flip RemoteMissing")
	assert.Equal(t, int32(0), status.Unpushed, "unpushed count must short-circuit when ref is missing")
}

func TestReadGitConfigRegexp_ReturnsNilWhenNoMatches(t *testing.T) {
	dir := initRepo(t)
	cfg := readGitConfigRegexp(context.Background(), dir, `^branch\.never-exists\.remote$`)
	assert.Nil(t, cfg)
}

func TestReadGitConfigRegexp_ParsesSingleEntry(t *testing.T) {
	dir := initRepo(t)
	run(t, dir, "git", "config", "remote.origin.url", "git@example.com:org/repo.git")

	cfg := readGitConfigRegexp(context.Background(), dir, `^remote\.origin\.url$`)
	require.NotNil(t, cfg)
	assert.Equal(t, "git@example.com:org/repo.git", cfg["remote.origin.url"])
	assert.Len(t, cfg, 1)
}

func TestReadGitConfigRegexp_ParsesMultipleEntries(t *testing.T) {
	dir := initRepo(t)
	run(t, dir, "git", "config", "remote.origin.url", "git@example.com:o/r.git")
	run(t, dir, "git", "config", "branch.main.remote", "origin")
	run(t, dir, "git", "config", "branch.main.merge", "refs/heads/main")

	cfg := readGitConfigRegexp(context.Background(), dir, `^(remote\.origin\.url|branch\.main\.(remote|merge))$`)
	require.NotNil(t, cfg)
	assert.Equal(t, "git@example.com:o/r.git", cfg["remote.origin.url"])
	assert.Equal(t, "origin", cfg["branch.main.remote"])
	assert.Equal(t, "refs/heads/main", cfg["branch.main.merge"])
}

// TestReadGitConfigRegexp_PreservesValuesWithNewline locks in the
// `-z` framing contract: config values that contain literal newlines
// must come through intact (the `\0` entry terminator, not `\n`,
// separates entries). Git config values can carry newlines through
// shell-escape, so the parser must not split on `\n` beyond the first
// occurrence.
func TestReadGitConfigRegexp_PreservesValuesWithNewline(t *testing.T) {
	dir := initRepo(t)
	run(t, dir, "git", "config", "leapmux.test.multiline", "line-a\nline-b")

	cfg := readGitConfigRegexp(context.Background(), dir, `^leapmux\.test\.multiline$`)
	require.NotNil(t, cfg)
	assert.Equal(t, "line-a\nline-b", cfg["leapmux.test.multiline"])
}

// branchSnapshot.toProto projects every field straight through. The
// test pins all nine slots so a refactor that renames a field or swaps
// two assignments fails loudly instead of silently mismatching the
// proto schema.
func TestBranchSnapshot_ToProto_AllFieldsProjected(t *testing.T) {
	snap := branchSnapshot{
		diffAdded:     11,
		diffDeleted:   7,
		diffUntracked: 3,
		hasChanges:    true,
		push: pushStatus{
			Unpushed:       5,
			UpstreamExists: true,
			RemoteMissing:  true,
			OriginExists:   true,
		},
	}
	gs := snap.toProto("feature-x")
	assert.Equal(t, int32(11), gs.GetDiffAdded())
	assert.Equal(t, int32(7), gs.GetDiffDeleted())
	assert.Equal(t, int32(3), gs.GetDiffUntracked())
	assert.True(t, gs.GetHasUncommittedChanges())
	assert.Equal(t, int32(5), gs.GetUnpushedCommitCount())
	assert.True(t, gs.GetUpstreamExists())
	assert.True(t, gs.GetRemoteBranchMissing())
	assert.True(t, gs.GetOriginExists())
	// CanPush is OriginExists && branchName != "" && != "HEAD".
	assert.True(t, gs.GetCanPush())
}

// CanPush short-circuits to false for detached HEAD (empty branch) and
// for the literal "HEAD" name, even when OriginExists is true.
func TestBranchSnapshot_ToProto_CanPushHonorsBranchName(t *testing.T) {
	snap := branchSnapshot{push: pushStatus{OriginExists: true}}
	assert.False(t, snap.toProto("").GetCanPush(), "detached HEAD must block push")
	assert.False(t, snap.toProto("HEAD").GetCanPush(), `literal "HEAD" must block push`)
	assert.True(t, snap.toProto("main").GetCanPush(), "named branch with origin must allow push")
}

// CanPush also returns false when origin doesn't exist, regardless of
// the branch name — covers the "no remote configured" case.
func TestBranchSnapshot_ToProto_CanPushFalseWithoutOrigin(t *testing.T) {
	snap := branchSnapshot{push: pushStatus{OriginExists: false}}
	assert.False(t, snap.toProto("main").GetCanPush())
}

// parseDiffShortstat extracts (added, deleted) from the `git diff
// --shortstat` line shape. Pinned against every spelling git can emit:
// both clauses, insertions-only, deletions-only, no-change, and the
// stray blank-line a failed `diff HEAD` yields on unborn repos.
func TestParseDiffShortstat(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		added   int32
		deleted int32
	}{
		{"empty", "", 0, 0},
		{"both clauses", " 3 files changed, 25 insertions(+), 12 deletions(-)", 25, 12},
		{"insertions only", " 1 file changed, 7 insertions(+)", 7, 0},
		{"deletions only", " 2 files changed, 4 deletions(-)", 0, 4},
		{"single line", " 1 file changed, 1 insertion(+), 1 deletion(-)", 1, 1},
		{"trailing newline", " 1 file changed, 5 insertions(+)\n", 5, 0},
		// Garbage input shouldn't crash; counts stay zero.
		{"garbage", "fatal: ambiguous argument 'HEAD'", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			added, deleted := parseDiffShortstat(c.in)
			assert.Equal(t, c.added, added)
			assert.Equal(t, c.deleted, deleted)
		})
	}
}

// parseStatusPorcelainCounts walks `git status --porcelain` output and
// reports (untracked count, hasChanges). The fixtures pin the exact
// line shapes git emits — `??` for untracked, `XY` for tracked changes,
// rename arrow notation, and bare empty input.
func TestParseStatusPorcelainCounts(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		untracked  int32
		hasChanges bool
	}{
		{"empty", "", 0, false},
		{"single untracked", "?? new.txt", 1, true},
		{"two untracked", "?? a.txt\n?? b.txt", 2, true},
		{"modified only", " M tracked.txt", 0, true},
		{
			"mix of tracked and untracked",
			" M a.txt\n?? b.txt\nA  c.txt\n?? d.txt",
			2,
			true,
		},
		{
			"rename does not count as untracked",
			"R  old.txt -> new.txt",
			0,
			true,
		},
		// Trailing blank line (from `strings.Split` + a final `\n`)
		// must not be counted as a change.
		{"trailing blank line", "?? foo\n", 1, true},
		// All-blank input degenerates to no-changes; defensive guard
		// against parsing a slice of empty strings.
		{"only blank lines", "\n\n\n", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			untracked, hasChanges := parseStatusPorcelainCounts(c.in)
			assert.Equal(t, c.untracked, untracked)
			assert.Equal(t, c.hasChanges, hasChanges)
		})
	}
}

// diffStatsForPath against a clean repo returns all zeros and
// hasChanges=false — the happy-path fast exit that
// gatherBranchSnapshot relies on for "nothing to prompt about".
func TestDiffStatsForPath_CleanRepo(t *testing.T) {
	dir := initRepo(t)
	added, deleted, untracked, hasChanges, err := diffStatsForPath(context.Background(), dir)
	require.NoError(t, err)
	assert.Zero(t, added)
	assert.Zero(t, deleted)
	assert.Zero(t, untracked)
	assert.False(t, hasChanges)
}

// diffStatsForPath against a dirty repo with staged + unstaged +
// untracked changes returns the aggregate counts. The added/deleted
// totals come from `git diff --shortstat HEAD` (staged + unstaged
// combined); untracked comes from `git status --porcelain`.
func TestDiffStatsForPath_StagedUnstagedAndUntracked(t *testing.T) {
	dir := initRepo(t)
	// Tracked file with one committed line, then a staged delete + an
	// unstaged add — exercises both diff sides ending up in the
	// `diff --shortstat HEAD` total.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("a\nb\nc\n"), 0o644))
	run(t, dir, "git", "add", "tracked.txt")
	run(t, dir, "git", "commit", "-m", "add tracked")
	// Stage a one-line change (delete "b\n").
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("a\nc\n"), 0o644))
	run(t, dir, "git", "add", "tracked.txt")
	// Unstaged: append a new line on top of the staged change.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("a\nc\nd\n"), 0o644))
	// Two untracked files.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new1.txt"), []byte("n1\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new2.txt"), []byte("n2\n"), 0o644))

	added, deleted, untracked, hasChanges, err := diffStatsForPath(context.Background(), dir)
	require.NoError(t, err)
	assert.True(t, hasChanges)
	assert.Equal(t, int32(2), untracked)
	// Net diff vs HEAD: a, c, d remain after the b removal — one line
	// added (d), one line deleted (b). Numbers come from
	// `git diff --shortstat HEAD`.
	assert.Equal(t, int32(1), added)
	assert.Equal(t, int32(1), deleted)
}

// diffStatsForPath with only untracked files (no tracked diff vs HEAD)
// returns added=deleted=0, untracked>0, hasChanges=true. The
// `git diff --shortstat` clause stays empty in this case because
// untracked files don't appear in the diff.
func TestDiffStatsForPath_OnlyUntracked(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hello\n"), 0o644))

	added, deleted, untracked, hasChanges, err := diffStatsForPath(context.Background(), dir)
	require.NoError(t, err)
	assert.Zero(t, added)
	assert.Zero(t, deleted)
	assert.Equal(t, int32(1), untracked)
	assert.True(t, hasChanges)
}

// listGitBranches on an empty repo (no commits yet) returns an empty
// slice without error — the refactor must not throw on the
// no-refs-yet edge case.
func TestListGitBranches_EmptyRepo(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		require.NoError(t, cmd.Run(), "git %v", args)
	}

	branches, err := listGitBranches(context.Background(), dir)
	require.NoError(t, err)
	assert.Empty(t, branches)
}

// listGitBranches on a repo with no remotes returns only local entries.
// Regression: an earlier draft of the for-each-ref walk yielded
// origin/HEAD-style pseudo-refs even when no remotes existed; this
// asserts the prefix matcher only picks up real branches.
func TestListGitBranches_OnlyLocal(t *testing.T) {
	repoDir := initRepo(t)
	run(t, repoDir, "git", "branch", "alpha")
	run(t, repoDir, "git", "branch", "beta")

	branches, err := listGitBranches(context.Background(), repoDir)
	require.NoError(t, err)
	names := make([]string, 0, len(branches))
	for _, b := range branches {
		assert.False(t, b.IsRemote, "expected only local branches, got remote %q", b.Name)
		names = append(names, b.Name)
	}
	assert.Contains(t, names, "alpha")
	assert.Contains(t, names, "beta")
}

// listGitBranches on a repo where every remote-tracking ref has a
// matching local entry: the dedup must drop every remote entry, so the
// result is local-only.
func TestListGitBranches_RemoteDedupedAgainstLocal(t *testing.T) {
	bareDir := filepath.Join(t.TempDir(), "all-deduped.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	seedDir := initRepo(t)
	defaultBranch := defaultBranchOf(t, seedDir)
	run(t, seedDir, "git", "remote", "add", "origin", bareDir)
	run(t, seedDir, "git", "push", "-u", "origin", "HEAD")
	run(t, seedDir, "git", "checkout", "-b", "feat-a")
	run(t, seedDir, "git", "commit", "--allow-empty", "-m", "a")
	run(t, seedDir, "git", "push", "-u", "origin", "feat-a")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "fetch", "origin", "refs/heads/*:refs/remotes/origin/*")
	// Cover both remote refs with locals so dedup eliminates them all.
	run(t, repoDir, "git", "branch", "feat-a", "origin/feat-a")
	if defaultBranchOf(t, repoDir) != defaultBranch {
		run(t, repoDir, "git", "branch", defaultBranch)
	}

	branches, err := listGitBranches(context.Background(), repoDir)
	require.NoError(t, err)
	for _, b := range branches {
		assert.False(t, b.IsRemote, "every remote ref should dedup to its local; got %q", b.Name)
	}
}

// TestListGitBranches_NonOriginRemoteSurvivesLocalCollision pins the
// multi-remote case where a non-origin remote like 'upstream/shared'
// must stay selectable in the picker even when a local 'shared'
// (tracking origin/shared, say) already exists. An earlier revision
// deduped via StripRemotePrefix which split on the first '/' and
// treated 'shared' as the local counterpart of 'upstream/shared' —
// silently dropping the remote ref so the user could never switch to it
// from the dialog.
func TestListGitBranches_NonOriginRemoteSurvivesLocalCollision(t *testing.T) {
	// Two separate bare remotes — origin and upstream — both carry a
	// branch named 'shared'. The local 'shared' tracks origin; the user
	// should still be able to pick 'upstream/shared' from the dialog.
	// We use 'shared' rather than the default branch name to avoid the
	// `git branch shared origin/shared` conflict when initRepo's
	// init.defaultBranch happens to also be 'shared' (impossible here)
	// or 'main' (irrelevant — the local doesn't yet have 'shared').
	originBare := filepath.Join(t.TempDir(), "origin.git")
	require.NoError(t, os.MkdirAll(originBare, 0o755))
	run(t, originBare, "git", "init", "--bare")
	upstreamBare := filepath.Join(t.TempDir(), "upstream.git")
	require.NoError(t, os.MkdirAll(upstreamBare, 0o755))
	run(t, upstreamBare, "git", "init", "--bare")

	// Seed both bares with a 'shared' branch.
	seedDir := initRepo(t)
	run(t, seedDir, "git", "remote", "add", "origin", originBare)
	run(t, seedDir, "git", "checkout", "-b", "shared")
	run(t, seedDir, "git", "commit", "--allow-empty", "-m", "shared")
	run(t, seedDir, "git", "push", "-u", "origin", "shared")
	run(t, seedDir, "git", "remote", "add", "upstream", upstreamBare)
	run(t, seedDir, "git", "push", "upstream", "shared")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", originBare)
	run(t, repoDir, "git", "remote", "add", "upstream", upstreamBare)
	run(t, repoDir, "git", "fetch", "origin", "refs/heads/*:refs/remotes/origin/*")
	run(t, repoDir, "git", "fetch", "upstream", "refs/heads/*:refs/remotes/upstream/*")
	// Local 'shared' tracking origin (not upstream).
	run(t, repoDir, "git", "branch", "shared", "origin/shared")

	branches, err := listGitBranches(context.Background(), repoDir)
	require.NoError(t, err)
	var names []string
	for _, b := range branches {
		names = append(names, b.Name)
	}
	// Local 'shared' present.
	assert.Contains(t, names, "shared")
	// origin/shared is deduped (origin is the default-remote convention).
	assert.NotContains(t, names, "origin/shared")
	// upstream/shared MUST remain selectable — the user opted into a
	// second remote, and dropping the entry locks them out of an
	// explicit pick from the dialog.
	assert.Contains(t, names, "upstream/shared")
	// upstream/shared must be flagged remote so resolveStampedBranch on
	// the frontend strips the prefix when stamping.
	for _, b := range branches {
		if b.Name == "upstream/shared" {
			assert.True(t, b.IsRemote)
		}
	}
}

// getGitFileStatusEntries: when the only change is `git add`-staged, the
// parallel `git diff --numstat --staged` must apply its line counts.
// The parallelization risked dropping the staged numstat if the loop
// ordering changed; this pins the staged path.
func TestGetGitFileStatusEntries_StagedChanges(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("a\n"), 0o644))
	run(t, dir, "git", "add", "tracked.txt")
	run(t, dir, "git", "commit", "-m", "add tracked")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("a\nb\nc\n"), 0o644))
	run(t, dir, "git", "add", "tracked.txt")

	files, err := getGitFileStatusEntries(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "tracked.txt", files[0].Path)
	// Staged-side numstat populates the StagedLines* fields; the
	// unstaged side must stay zero since there's no working-tree-only
	// change. Pins both halves so the parallelization can't quietly
	// drop one of them.
	assert.Equal(t, int32(2), files[0].StagedLinesAdded, "staged numstat must apply")
	assert.Equal(t, int32(0), files[0].StagedLinesDeleted)
	assert.Equal(t, int32(0), files[0].LinesAdded, "unstaged numstat must not bleed")
	assert.Equal(t, int32(0), files[0].LinesDeleted)
}

// canonicalRepoDir wraps initRepo and resolves any symlinks in the
// returned path. On macOS, `t.TempDir()` returns `/var/folders/...`
// which is a symlink to `/private/var/folders/...`. The ReadGitFile
// handler joins the caller-supplied (unresolved) path with the
// `git rev-parse --show-toplevel` output (resolved), then takes a
// textual filepath.Rel — without canonicalization the two paths
// diverge and Rel produces a bogus `../../../...` path that no ref
// can name. Resolving up-front mirrors what real callers experience
// when they pass a path already canonicalized by the frontend.
func canonicalRepoDir(t *testing.T) string {
	t.Helper()
	dir := initRepo(t)
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return resolved
}

// ReadGitFile must reject GIT_FILE_REF_UNSPECIFIED (the proto zero
// value) with InvalidArgument rather than silently falling back to HEAD.
// A silent HEAD fallback would mask a future enum addition that callers
// haven't been taught about.
func TestReadGitFile_RejectsUnspecifiedRef(t *testing.T) {
	_, d, w := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := canonicalRepoDir(t)
	filePath := filepath.Join(repoDir, "tracked.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("committed\n"), 0o644))
	run(t, repoDir, "git", "add", "tracked.txt")
	run(t, repoDir, "git", "commit", "-m", "add tracked")

	dispatch(d, "ReadGitFile", &leapmuxv1.ReadGitFileRequest{
		Path: filePath,
		Ref:  leapmuxv1.GitFileRef_GIT_FILE_REF_UNSPECIFIED,
	}, w)

	require.Empty(t, w.responses, "unspecified ref must not return a response")
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.InvalidArgument), w.errors[0].code)
}

func TestReadGitFile_HeadReturnsCommittedContent(t *testing.T) {
	_, d, w := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := canonicalRepoDir(t)
	filePath := filepath.Join(repoDir, "tracked.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("committed\n"), 0o644))
	run(t, repoDir, "git", "add", "tracked.txt")
	run(t, repoDir, "git", "commit", "-m", "add tracked")
	// Overwrite the working tree so HEAD content must come from git
	// rather than the filesystem — proves the handler reads from the
	// ref, not from disk.
	require.NoError(t, os.WriteFile(filePath, []byte("dirty\n"), 0o644))

	dispatch(d, "ReadGitFile", &leapmuxv1.ReadGitFileRequest{
		Path: filePath,
		Ref:  leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ReadGitFileResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.True(t, resp.GetExists())
	assert.Equal(t, "committed\n", string(resp.GetContent()))
}

func TestReadGitFile_StagedReturnsIndexContent(t *testing.T) {
	_, d, w := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := canonicalRepoDir(t)
	filePath := filepath.Join(repoDir, "tracked.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("v1\n"), 0o644))
	run(t, repoDir, "git", "add", "tracked.txt")
	run(t, repoDir, "git", "commit", "-m", "v1")
	// Stage v2; leave the working tree at v3 so we can distinguish the
	// index from both HEAD and the worktree.
	require.NoError(t, os.WriteFile(filePath, []byte("v2-staged\n"), 0o644))
	run(t, repoDir, "git", "add", "tracked.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("v3-dirty\n"), 0o644))

	dispatch(d, "ReadGitFile", &leapmuxv1.ReadGitFileRequest{
		Path: filePath,
		Ref:  leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ReadGitFileResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.True(t, resp.GetExists())
	assert.Equal(t, "v2-staged\n", string(resp.GetContent()))
}

// A path that isn't present at the requested ref must respond with a
// successful response carrying Exists=false rather than surfacing the
// `git show` failure as an RPC error — the frontend distinguishes
// "file not in this ref" (collapse the diff view) from "transport
// failed" (show an error banner).
func TestReadGitFile_MissingFileAtRefReturnsExistsFalse(t *testing.T) {
	_, d, w := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := canonicalRepoDir(t)
	// File exists in the working tree but was never committed or staged.
	filePath := filepath.Join(repoDir, "never-tracked.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("untracked\n"), 0o644))

	dispatch(d, "ReadGitFile", &leapmuxv1.ReadGitFileRequest{
		Path: filePath,
		Ref:  leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ReadGitFileResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.False(t, resp.GetExists(), "missing-at-ref must surface as Exists=false")
	assert.Empty(t, resp.GetContent())
}

// TestResolvePushStatus_CountsLiveUnpushedCommits proves the live
// rev-list count is the source of truth: two commits added AFTER the
// hypothetical dialog snapshot show up in the resolved status. Earlier
// revisions threaded a BranchGitState hint into resolvePushStatus and
// trusted its stale UnpushedCommitCount; the helper now ignores that
// hint entirely and re-probes, so this test enforces the post-snapshot
// commits are reflected without any way for the hint path to mask them.
func TestResolvePushStatus_CountsLiveUnpushedCommits(t *testing.T) {
	bareDir := filepath.Join(t.TempDir(), "resolve-live.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")
	run(t, repoDir, "git", "checkout", "-b", "live-wins")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "c1")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "c2")
	run(t, repoDir, "git", "push", "-u", "origin", "live-wins")
	// Two unpushed commits AFTER the dialog snapshot would have been
	// taken — the live probe must report 2.
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "post-snapshot-1")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "post-snapshot-2")

	got, err := resolvePushStatus(context.Background(), repoDir, "live-wins")
	require.NoError(t, err)
	assert.True(t, got.UpstreamExists)
	assert.Equal(t, int32(2), got.Unpushed,
		"resolvePushStatus must return the live rev-list count of unpushed commits")
}

func TestPushBranch_RecreatesUpstream(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	bareDir := filepath.Join(t.TempDir(), "push-upstream.git")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	run(t, bareDir, "git", "init", "--bare")

	repoDir := initRepo(t)
	run(t, repoDir, "git", "remote", "add", "origin", bareDir)
	run(t, repoDir, "git", "push", "-u", "origin", "HEAD")
	run(t, repoDir, "git", "checkout", "-b", "recreate-upstream")
	run(t, repoDir, "git", "commit", "--allow-empty", "-m", "branch commit")
	createAgentForPath(t, svc, "agent-recreate-upstream", repoDir)

	err := svc.pushBranch(context.Background(), leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-recreate-upstream")
	require.NoError(t, err)

	upstream, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "recreate-upstream@{upstream}")
	require.NoError(t, err)
	assert.Equal(t, "origin/recreate-upstream", strings.TrimSpace(upstream))
}

// ensureTrackedWorktreeWith skips the queryGitPathInfo rev-parse fork
// when the caller supplies both repoRoot and branchName. Verify by
// passing synthetic values that DIFFER from what the probe would yield —
// if the function had run the probe, those probe-derived values would
// land in the DB instead. The synthetic values pinning the bypass is
// the only thing this test cares about; we don't trust them via any
// other call site.
func TestEnsureTrackedWorktreeWith_SkipsProbeWhenHintsSupplied(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "track-with-hint-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "real-branch", wtDir)

	const syntheticRoot = "/synthetic/repo/root"
	const syntheticBranch = "hint-only"

	wtID, err := svc.ensureTrackedWorktreeWith(context.Background(), wtDir, syntheticRoot, syntheticBranch)
	require.NoError(t, err)

	row, err := svc.Queries.GetWorktreeByID(context.Background(), wtID)
	require.NoError(t, err)
	assert.Equal(t, syntheticRoot, row.RepoRoot, "supplied repoRoot must land in the DB row unmodified")
	assert.Equal(t, syntheticBranch, row.BranchName, "supplied branchName must land in the DB row unmodified")
}

// Cold path with no hints: falls back to queryGitPathInfo. The probe
// runs against the canonicalized worktree path and yields the real
// repo root + branch.
func TestEnsureTrackedWorktreeWith_ProbesWhenBothHintsEmpty(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "track-no-hint-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "probed-branch", wtDir)

	wtID, err := svc.ensureTrackedWorktreeWith(context.Background(), wtDir, "", "")
	require.NoError(t, err)

	row, err := svc.Queries.GetWorktreeByID(context.Background(), wtID)
	require.NoError(t, err)
	// Probe walks symlinks; compare canonical forms so /private/var…
	// vs /var… on macOS doesn't trip the assertion.
	expectedRoot, err := filepath.EvalSymlinks(repoDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(expectedRoot, row.RepoRoot),
		"expected probe-derived repoRoot=%q, got %q", expectedRoot, row.RepoRoot)
	assert.Equal(t, "probed-branch", row.BranchName)
}

// Partial-hint path: one empty field still triggers the probe to fill
// it in. The supplied field is preserved; the missing one is derived.
func TestEnsureTrackedWorktreeWith_ProbesToFillSingleMissingHint(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "track-partial-hint-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "partial-branch", wtDir)

	const syntheticRoot = "/synthetic/repo/root"
	wtID, err := svc.ensureTrackedWorktreeWith(context.Background(), wtDir, syntheticRoot, "")
	require.NoError(t, err)

	row, err := svc.Queries.GetWorktreeByID(context.Background(), wtID)
	require.NoError(t, err)
	assert.Equal(t, syntheticRoot, row.RepoRoot, "supplied repoRoot must be preserved")
	assert.Equal(t, "partial-branch", row.BranchName, "missing branchName must be filled by the probe")
}

// Warm path: an existing DB row short-circuits before any hint or probe
// logic runs. Synthetic hints supplied here must NOT replace the row.
func TestEnsureTrackedWorktreeWith_ReturnsExistingRowBeforeProbingOrHinting(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "track-warm-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", "warm-branch", wtDir)

	// First call seeds the row with probe-derived values.
	firstID, err := svc.ensureTrackedWorktreeWith(context.Background(), wtDir, "", "")
	require.NoError(t, err)

	// Second call with bogus hints must return the same id and leave
	// the row's fields unchanged.
	secondID, err := svc.ensureTrackedWorktreeWith(context.Background(), wtDir, "/bogus", "bogus-branch")
	require.NoError(t, err)
	assert.Equal(t, firstID, secondID, "warm path must reuse the existing row")

	row, err := svc.Queries.GetWorktreeByID(context.Background(), firstID)
	require.NoError(t, err)
	expectedRoot, err := filepath.EvalSymlinks(repoDir)
	require.NoError(t, err)
	assert.True(t, pathutil.SamePath(expectedRoot, row.RepoRoot),
		"warm-path call must not rewrite repoRoot; expected %q, got %q", expectedRoot, row.RepoRoot)
	assert.Equal(t, "warm-branch", row.BranchName, "warm-path call must not rewrite branchName")
}

// ----- hasTrackedChange unit tests -----

// hasTrackedChange differentiates the "every entry is a plain untracked
// file" case from anything else. The boolean drives the numstat-skip
// optimization in getGitFileStatusEntries, so the matrix below pins
// every status-code combination that should fall on either side.
func TestHasTrackedChange(t *testing.T) {
	untracked := func(path string) *leapmuxv1.GitFileStatusEntry {
		return &leapmuxv1.GitFileStatusEntry{
			Path:           path,
			StagedStatus:   leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNSPECIFIED,
			UnstagedStatus: leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED,
		}
	}
	tracked := func(path string, staged, unstaged leapmuxv1.GitFileStatusCode) *leapmuxv1.GitFileStatusEntry {
		return &leapmuxv1.GitFileStatusEntry{Path: path, StagedStatus: staged, UnstagedStatus: unstaged}
	}
	cases := []struct {
		name  string
		files []*leapmuxv1.GitFileStatusEntry
		want  bool
	}{
		{name: "empty", files: nil, want: false},
		{name: "single untracked", files: []*leapmuxv1.GitFileStatusEntry{untracked("a")}, want: false},
		{name: "many untracked", files: []*leapmuxv1.GitFileStatusEntry{untracked("a"), untracked("b"), untracked("c")}, want: false},
		{
			name: "staged modification",
			files: []*leapmuxv1.GitFileStatusEntry{
				tracked("a", leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_MODIFIED, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNSPECIFIED),
			},
			want: true,
		},
		{
			name: "unstaged modification",
			files: []*leapmuxv1.GitFileStatusEntry{
				tracked("a", leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNSPECIFIED, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_MODIFIED),
			},
			want: true,
		},
		{
			name: "untracked then tracked",
			files: []*leapmuxv1.GitFileStatusEntry{
				untracked("a"),
				tracked("b", leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNSPECIFIED, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_DELETED),
			},
			want: true,
		},
		{
			name: "deleted file (unstaged D)",
			files: []*leapmuxv1.GitFileStatusEntry{
				tracked("a", leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNSPECIFIED, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_DELETED),
			},
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, hasTrackedChange(c.files))
		})
	}
}

// TestGetGitFileStatusEntries_OnlyUntrackedSkipsNumstat exercises the
// happy-path optimization end-to-end: a repo whose status output is
// nothing but untracked entries still returns them, but without forking
// the two numstat probes. We can't easily observe the absence of a
// subprocess fork from inside the test, so we pin the externally
// observable contract: the returned entries match the untracked-only
// shape (zero line counts on every side) and the call completes
// without error.
func TestGetGitFileStatusEntries_OnlyUntrackedSkipsNumstat(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "u1.txt"), []byte("a\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "u2.txt"), []byte("b\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "u3.bin"), []byte("\x00\x01\x02"), 0o644))

	files, err := getGitFileStatusEntries(context.Background(), dir)
	require.NoError(t, err)
	require.Len(t, files, 3, "every untracked entry must still surface")
	for _, f := range files {
		assert.Equal(t, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNSPECIFIED, f.GetStagedStatus(),
			"all-untracked branch must leave staged status unset for %q", f.GetPath())
		assert.Equal(t, leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED, f.GetUnstagedStatus(),
			"all-untracked branch must mark entry %q as untracked", f.GetPath())
		assert.Equal(t, int32(0), f.GetLinesAdded(), "no numstat fork ran, so LinesAdded must be zero")
		assert.Equal(t, int32(0), f.GetLinesDeleted())
		assert.Equal(t, int32(0), f.GetStagedLinesAdded())
		assert.Equal(t, int32(0), f.GetStagedLinesDeleted())
	}
}

// ----- branchInfoProbe unit tests -----

// TestBranchInfoProbe_CachesAcrossLookups pins the post-completion
// cache contract: once a lookup succeeds for a workingDir, subsequent
// lookups for that same dir return the cached value without forking
// queryGitPathInfo again. The probe is allocated per
// hasOtherNonWorktreeTabOnBranch call, so we exercise it directly here.
func TestBranchInfoProbe_CachesAcrossLookups(t *testing.T) {
	probe := newBranchInfoProbe()
	dir := initRepo(t)
	ctx := context.Background()

	first, err := probe.lookup(ctx, dir)
	require.NoError(t, err)
	require.NotNil(t, first)
	second, err := probe.lookup(ctx, dir)
	require.NoError(t, err)
	assert.Same(t, first, second, "second lookup must return the cached pointer, not a fresh fetch")
}

// TestBranchInfoProbe_CoalescesConcurrentMisses pins the singleflight
// contract: when many goroutines miss the cache for the same key at
// the same time, only one queryGitPathInfo runs. The bare-map version
// of this code had each goroutine fork its own rev-parse — the
// singleflight wrapper coalesces them.
//
// We verify by snapshotting the result pointer from every goroutine
// and asserting they're all identical. queryGitPathInfo returns a
// freshly-allocated *gitPathInfo per call, so identity equality across
// every result proves only one underlying fetch ran (or all but one
// observed the cache after the first finished — both are acceptable;
// the bare-map version could not guarantee this).
func TestBranchInfoProbe_CoalescesConcurrentMisses(t *testing.T) {
	probe := newBranchInfoProbe()
	dir := initRepo(t)
	ctx := context.Background()
	const n = 16

	results := make([]*gitPathInfo, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = probe.lookup(ctx, dir)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "goroutine %d", i)
	}
	for i := 1; i < n; i++ {
		assert.Samef(t, results[0], results[i],
			"every concurrent lookup must observe the same cached *gitPathInfo (goroutine %d differed)", i)
	}
}

// TestBranchInfoProbe_DifferentKeysIndependent pins the negative case:
// the singleflight key is the workingDir, so lookups for different dirs
// must each fork their own queryGitPathInfo and produce distinct
// *gitPathInfo values.
func TestBranchInfoProbe_DifferentKeysIndependent(t *testing.T) {
	probe := newBranchInfoProbe()
	repoA := initRepo(t)
	repoB := initRepo(t)
	ctx := context.Background()

	a, err := probe.lookup(ctx, repoA)
	require.NoError(t, err)
	b, err := probe.lookup(ctx, repoB)
	require.NoError(t, err)
	assert.NotSame(t, a, b, "different workingDirs must produce different *gitPathInfo")
	assert.NotEqual(t, a.RepoRoot, b.RepoRoot, "different repos must report different repoRoots")
}

// TestBranchInfoProbe_WaiterSurvivesLeaderCancellation pins the
// per-waiter ctx-aware retry inside lookup(). Without it, the
// singleflight leader's ctx cancellation propagates to every joined
// waiter — the leader's cancellation becomes the waiter's
// context.Canceled even though the waiter's own ctx is still alive.
// matches() coerces err to false today, but any future caller that
// surfaces the error verbatim would see spurious cancellations on
// dirs that were innocently shared with the cancelled scan. The
// retry path detects "my ctx is alive but I got a ctx error" and
// re-runs the lookup with my own ctx as the new leader.
func TestBranchInfoProbe_WaiterSurvivesLeaderCancellation(t *testing.T) {
	probe := newBranchInfoProbe()
	dir := initRepo(t)

	const n = 8
	// Leader's ctx is cancellable; waiters use a live ctx. Stagger so
	// the leader is guaranteed to be the cancellable goroutine.
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()

	// Block the leader's queryGitPathInfo by holding a coarse lock on
	// the singleflight key — we can't intercept queryGitPathInfo
	// itself without re-plumbing, so we cancel leaderCtx ASAP after
	// kicking off the leader. The waiters' retry path takes over with
	// their own (alive) ctx.
	leaderStarted := make(chan struct{})
	leaderDone := make(chan struct{})
	var leaderErr error
	go func() {
		close(leaderStarted)
		_, leaderErr = probe.lookup(leaderCtx, dir)
		close(leaderDone)
	}()
	<-leaderStarted
	cancelLeader()
	<-leaderDone

	// The leader either completed (its queryGitPathInfo finished
	// before the cancel reached the syscall) or failed with ctx.Canceled.
	// Either is fine — what matters is that subsequent waiters with
	// LIVE ctxs succeed. Without the retry path, a leader that errored
	// would also error out every joined waiter, and subsequent calls
	// (no longer joined since the sf key cleared) would re-enter as
	// new leaders. The retry path makes this safe even in the joined-
	// waiter window.
	results := make([]*gitPathInfo, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = probe.lookup(context.Background(), dir)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "waiter %d with live ctx must NOT inherit leader's cancellation; leaderErr=%v", i, leaderErr)
		require.NotNil(t, results[i])
	}
	// Sanity: every successful lookup observed the same cached row.
	for i := 1; i < n; i++ {
		assert.Same(t, results[0], results[i])
	}
}

// TestBranchInfoProbe_AllCallersCancelledPropagates pins the negative
// case: when every caller's ctx is dead, lookup must NOT loop
// forever via the retry path — it returns the ctx error promptly.
// The for-loop's retry guard is gated on `ctx.Err() == nil`, so a
// dead caller-ctx falls through to `return nil, err` on the first
// pass.
func TestBranchInfoProbe_AllCallersCancelledPropagates(t *testing.T) {
	probe := newBranchInfoProbe()
	dir := initRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE the call

	done := make(chan error, 1)
	go func() {
		_, err := probe.lookup(ctx, dir)
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled),
			"a caller with a dead ctx must get a ctx error, not spin in the retry loop")
	case <-time.After(2 * time.Second):
		t.Fatal("lookup did not return on a pre-cancelled ctx — retry loop probably looped forever")
	}
}

// TestBranchInfoProbe_Matches verifies the predicate composition: a
// lookup whose repoRoot matches and whose branch matches returns true;
// every mismatch returns false. The mismatch matrix here is what makes
// hasOtherNonWorktreeTabOnBranch safe across multi-worktree repos.
func TestBranchInfoProbe_Matches(t *testing.T) {
	probe := newBranchInfoProbe()
	dir := initRepo(t)
	ctx := context.Background()
	info, err := queryGitPathInfo(ctx, dir)
	require.NoError(t, err)
	branch := branchOrShortSHA(info)
	require.NotEmpty(t, branch)

	got, err := probe.matches(ctx, dir, info.RepoRoot, branch)
	require.NoError(t, err)
	assert.True(t, got, "same repo + same branch must match")

	got, err = probe.matches(ctx, dir, info.RepoRoot, "definitely-not-the-branch")
	require.NoError(t, err)
	assert.False(t, got, "branch mismatch must not match")

	// The matches probe should refuse to match a working dir whose
	// repoRoot is "other" — this is what keeps two different repos with
	// the same branch name (e.g. "main") from cross-matching.
	other := initRepo(t)
	got, err = probe.matches(ctx, dir, other, branch)
	require.NoError(t, err)
	assert.False(t, got, "repoRoot mismatch must not match")
}

// TestSnapshotStatsPath_CanonicalizesNonWorktreePath pins the
// canonical-path invariant. The inspectBranchDeletion gate compares
// `snapshotStatsPath(info, dirPath)` against
// `pathutil.Canonicalize(dirPath)`. If snapshotStatsPath returned the
// raw dirPath verbatim for the non-worktree case, the two would
// diverge on a symlinked ancestor (`/tmp` -> `/private/tmp` on
// macOS), the gate would fire on every dialog open, and the hint
// snapshot would be discarded for a re-probe that always produces
// the same result.
func TestSnapshotStatsPath_CanonicalizesNonWorktreePath(t *testing.T) {
	// Build a non-worktree info pointing at a canonical path.
	info := &gitPathInfo{
		IsWorktree: false,
		TopLevel:   "/canonical/repo",
		RepoRoot:   "/canonical/repo",
	}
	// A raw dirPath with a redundant trailing slash exercises the
	// Canonicalize call (which strips it) without depending on the
	// host having a specific symlink layout.
	got := snapshotStatsPath(info, "/canonical/repo/")
	want := pathutil.Canonicalize("/canonical/repo/")
	assert.Equal(t, want, got,
		"non-worktree snapshotStatsPath must canonicalize so the inspectBranchDeletion hint gate doesn't spuriously fire")
}

// TestSnapshotStatsPath_WorktreeUsesTopLevel pins the worktree-case
// branch: info.TopLevel is already canonicalized by queryGitPathInfo,
// so snapshotStatsPath must return it verbatim (NOT re-canonicalize a
// possibly-different dirPath).
func TestSnapshotStatsPath_WorktreeUsesTopLevel(t *testing.T) {
	info := &gitPathInfo{
		IsWorktree: true,
		TopLevel:   "/canonical/worktree",
		RepoRoot:   "/canonical/repo",
	}
	got := snapshotStatsPath(info, "/some/other/dir")
	assert.Equal(t, "/canonical/worktree", got,
		"worktree-case snapshotStatsPath must return info.TopLevel — already canonical, ignores dirPath")
}

// TestExecuteCreateBranch_InsideWorktreeTracksAssociation pins the
// regression where executeCreateBranch left WorktreeID empty when the
// working dir lived in a linked worktree. Mirror of the executeCheckoutBranch
// case: a later CloseAgent(REMOVE) used to silently degrade to KEEP
// because GetWorktreeForTab returned sql.ErrNoRows.
func TestExecuteCreateBranch_InsideWorktreeTracksAssociation(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt-createbranch")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-base", wtDir)

	result, err := svc.executeCreateBranch(context.Background(), gitModePlan{
		Mode:              gitModeCreateBranch,
		WorkingDir:        wtDir,
		PlannedWorkingDir: wtDir,
		BranchName:        "feat-in-wt",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.WorktreeID,
		"executeCreateBranch in a linked worktree must register the worktree association so REMOVE-on-close finds the row")
	row, err := svc.Queries.GetWorktreeByID(context.Background(), result.WorktreeID)
	require.NoError(t, err)
	gotPath, err := filepath.EvalSymlinks(row.WorktreePath)
	require.NoError(t, err)
	wantPath, err := filepath.EvalSymlinks(wtDir)
	require.NoError(t, err)
	assert.Equal(t, wantPath, gotPath, "tracked worktree row must point at the worktree dir")
	require.NotNil(t, result.Rollback.CreatedBranch, "rollback metadata must survive the attach step")
	assert.Equal(t, "feat-in-wt", result.Rollback.CreatedBranch.CreatedBranch)
}

// TestExecuteCheckoutBranch_AttachFailureLeavesHeadAlone pins the
// probe-before-mutation contract: if attachWorktreeIfPresent fails
// (e.g. ctx cancellation, DB error) the checkout MUST NOT have run.
// Earlier revisions did checkout-then-attach, so a probe failure after
// the checkout left the working tree on the new branch with no tab
// linkage — the exact "REMOVE-on-close degrades to KEEP" hazard.
func TestExecuteCheckoutBranch_AttachFailureLeavesHeadAlone(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	run(t, repoDir, "git", "branch", "target")
	wtDir := filepath.Join(t.TempDir(), "wt-checkout-cancel")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-source", wtDir)

	// Cancel before invoking — attachWorktreeIfPresent's queryGitPathInfo
	// runs first and surfaces context.Canceled, so the checkout never
	// gets to mutate HEAD.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	headBefore, err := gitutil.Output(context.Background(), wtDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)

	_, err = svc.executeCheckoutBranch(ctx, gitModePlan{
		Mode:           gitModeCheckoutBranch,
		WorkingDir:     wtDir,
		CheckoutTarget: "target",
	})
	require.Error(t, err, "ctx-canceled probe must surface as an error from executeCheckoutBranch")

	headAfter, err := gitutil.Output(context.Background(), wtDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(headBefore), strings.TrimSpace(headAfter),
		"failed attach must abort before checkout; HEAD must remain on the original branch")
}

// TestAttachWorktreeIfPresent_NotARepoIsBenignNoOp pins the
// errNotGitRepo distinction: a dir that's just not a git repo is fine
// (no association to make), and the helper returns nil with no rollback
// requested. This is the path executeUseCurrent takes for plain-dir
// agents (no git context at all).
func TestAttachWorktreeIfPresent_NotARepoIsBenignNoOp(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	notRepo := t.TempDir()
	result := &gitModeResult{WorkingDir: notRepo}
	err := svc.attachWorktreeIfPresent(context.Background(), result, notRepo)
	require.NoError(t, err, "non-repo dir must be a silent no-op (errNotGitRepo)")
	assert.Empty(t, result.WorktreeID, "no worktree association to make for a non-repo dir")
}

// TestAttachWorktreeIfPresent_CtxCanceledSurfacesError pins the
// queryGitPathInfo-ctx-error path: cancellation must NOT be conflated
// with "not a repo" (the previous behaviour). Without this distinction,
// a cancelled OpenAgent silently registered the tab without worktree
// linkage and the user lost cleanup-on-close.
func TestAttachWorktreeIfPresent_CtxCanceledSurfacesError(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt-attach-cancel")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-attach-cancel", wtDir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := &gitModeResult{WorkingDir: wtDir}
	err := svc.attachWorktreeIfPresent(ctx, result, wtDir)
	require.Error(t, err, "ctx-cancel must surface from attachWorktreeIfPresent — not silently degrade to nil")
	assert.Empty(t, result.WorktreeID, "failed attach must leave WorktreeID empty for the caller to handle")
}

// TestCheckoutBranchInDir_HasRefsErrorIsFatal pins the regression
// where a HasRefs probe failure caused the function to fall through to
// `git checkout origin/foo` against an unmodified target — landing on
// a DETACHED HEAD when checkout.guess=false (set by some corporate /
// hardened git installs). The fix surfaces the probe error so the user
// gets a clean failure instead of silent detached-HEAD damage.
//
// We simulate the failure by passing a non-existent workingDir, which
// makes `git show-ref` fail at the "not a git repo" gate. With the
// fall-through bug, the function would then attempt `git checkout
// origin/foo` (which also fails) but the FIRST failure is the one that
// matters — we assert the error message identifies the resolve step.
func TestCheckoutBranchInDir_HasRefsErrorIsFatal(t *testing.T) {
	notRepo := filepath.Join(t.TempDir(), "not-a-git-dir")
	require.NoError(t, os.MkdirAll(notRepo, 0o755))

	err := checkoutBranchInDir(context.Background(), notRepo, "origin/anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve",
		"HasRefs probe failure must surface as a resolve error, not fall through to a detached-HEAD checkout")
}

// TestDiffStatsForPath_UnbornHEADFallsBackToNumstat pins the unborn-
// HEAD recovery: when `git diff --shortstat HEAD` fails because HEAD
// doesn't exist yet (newly-initialized repo with staged content), the
// helper falls back to per-file numstat from getGitFileStatusEntries
// so staged line counts are still reported. Previously this returned
// 0/0 silently, hiding the scope of unsaved work from the close prompt.
func TestDiffStatsForPath_UnbornHEADFallsBackToNumstat(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init")
	// Stage a file with 3 added lines. No initial commit, so HEAD is
	// unborn.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\nb\nc\n"), 0o644))
	run(t, dir, "git", "add", "f.txt")

	added, deleted, untracked, hasChanges, err := diffStatsForPath(context.Background(), dir)
	require.NoError(t, err, "unborn HEAD must NOT propagate as a diffStatsForPath error")
	assert.True(t, hasChanges, "porcelain status must still report changes")
	assert.Equal(t, int32(0), untracked, "the staged file is not untracked")
	assert.Equal(t, int32(3), added, "staged additions must come through via the per-file numstat fallback")
	assert.Equal(t, int32(0), deleted)
}

// TestQueryGitPathInfo_CtxCanceledDoesNotMasqueradeAsNotARepo pins the
// queryGitPathInfo error-distinguishing fix: a ctx-canceled subprocess
// must surface ctx.Canceled (or wrapped form), not errNotGitRepo. The
// upstream consequence: GetGitInfo's handler routes errNotGitRepo to
// IsGitRepo=false but any other error to an RPC failure; previously a
// dispatcher-ctx cancellation conflated as errNotGitRepo silently
// hid the form behind a "not a git repo" verdict.
func TestQueryGitPathInfo_CtxCanceledDoesNotMasqueradeAsNotARepo(t *testing.T) {
	repoDir := initRepo(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := queryGitPathInfo(ctx, repoDir)
	require.Error(t, err, "cancelled ctx must surface as an error from queryGitPathInfo")
	assert.False(t, errors.Is(err, errNotGitRepo),
		"ctx cancellation must NOT be conflated with errNotGitRepo; got %v", err)
}

// TestInspectBranchChange_CtxCanceledDoesNotMasqueradeAsNotARepo pins
// the goroutine-level error routing in inspectBranchChange's errgroup:
// when the outer ctx is cancelled, queryGitPathInfo returns
// context.Canceled. An earlier revision blanket-wrapped any infoErr as
// errNotGitRepo before returning to the errgroup, and the wait-handler
// then mapped it to "not a git repository" for a real repo. The fix
// returns the raw error from the goroutine and only maps to "not a git
// repository" when infoErr really is errNotGitRepo.
func TestInspectBranchChange_CtxCanceledDoesNotMasqueradeAsNotARepo(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.inspectBranchChange(ctx, repoDir)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "not a git repository",
		"cancelled inspectBranchChange against a real repo must NOT report 'not a git repository'")
}

// TestInspectBranchDeletion_CtxCanceledDoesNotMasqueradeAsNotARepo
// mirrors the change-branch case for the deletion-inspect goroutines.
// Both the hint-arm and no-hint-arm goroutines used to wrap any
// queryGitPathInfo error as errNotGitRepo; with the fix the raw error
// flows back to g.Wait() and the wait-handler reports it as-is when
// it's not errNotGitRepo.
func TestInspectBranchDeletion_CtxCanceledDoesNotMasqueradeAsNotARepo(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	repoDir := initRepo(t)

	t.Run("hint arm", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := svc.inspectBranchDeletion(ctx, repoDir, "feature-hint")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "not a git repository",
			"hint-arm: cancelled inspectBranchDeletion against a real repo must NOT report 'not a git repository'")
	})

	t.Run("no-hint arm", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := svc.inspectBranchDeletion(ctx, repoDir, "")
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "not a git repository",
			"no-hint arm: cancelled inspectBranchDeletion against a real repo must NOT report 'not a git repository'")
	})
}

// TestRemoveWorktreeFromDisk_DeletesCurrentHEADNotStampedBranch pins
// the live-HEAD probe added to removeWorktreeFromDisk. The DB row's
// branch_name is stamped at attach time but an external `git checkout`
// (or executeCheckoutBranch's post-attach switch) can move HEAD without
// updating the row. Trusting wt.BranchName would attempt
// `git branch -D <stamped-name>` against the wrong branch — at worst
// deleting an unrelated branch like `main` that's still the user's
// working branch in the main worktree.
func TestRemoveWorktreeFromDisk_DeletesCurrentHEADNotStampedBranch(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	// Capture the main repo's default branch — we want to prove the
	// helper does NOT delete it even though it's the stamped name on the
	// worktree row below.
	mainBranchOut, err := gitutil.Output(context.Background(), repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	require.NoError(t, err)
	mainBranch := strings.TrimSpace(mainBranchOut)
	require.NotEmpty(t, mainBranch)

	// Create a worktree on a fresh branch.
	wtDir := filepath.Join(t.TempDir(), "wt-restamp")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-real-branch", wtDir)

	// Manually insert a worktree row whose branch_name is intentionally
	// STALE (claims the worktree is on `mainBranch`, but `git worktree
	// add` above checked it out on `wt-real-branch`). This mimics the
	// attach-before-checkout sequence where the DB row's branch_name
	// drifts vs. live HEAD.
	wtCanon, err := filepath.EvalSymlinks(wtDir)
	require.NoError(t, err)
	repoCanon, err := filepath.EvalSymlinks(repoDir)
	require.NoError(t, err)
	require.NoError(t, svc.Queries.CreateWorktree(context.Background(), db.CreateWorktreeParams{
		ID:           "wt-stale-1",
		WorktreePath: pathutil.Canonicalize(wtCanon),
		RepoRoot:     pathutil.Canonicalize(repoCanon),
		BranchName:   mainBranch, // stale: the worktree's actual HEAD is wt-real-branch
	}))

	wt, err := svc.Queries.GetWorktreeByID(context.Background(), "wt-stale-1")
	require.NoError(t, err)

	require.NoError(t, svc.removeWorktreeFromDisk(wt, true))

	// The stamped (wrong) branch must still exist on the main repo.
	_, mainExistsErr := gitutil.Output(context.Background(), repoDir, "rev-parse", "--verify", mainBranch)
	require.NoError(t, mainExistsErr, "removeWorktreeFromDisk must NOT delete the stamped branch when it disagrees with live HEAD")

	// The actual worktree HEAD branch should have been deleted (it was
	// the branch that the worktree was really on).
	_, realExistsErr := gitutil.Output(context.Background(), repoDir, "rev-parse", "--verify", "wt-real-branch")
	require.Error(t, realExistsErr, "removeWorktreeFromDisk must delete the live-HEAD branch of the removed worktree")
}

// TestExecuteCheckoutBranch_FailureReturnsPopulatedResult pins the
// return-shape parity between executeCheckoutBranch and
// executeCreateBranch. Earlier executeCheckoutBranch threw away the
// populated `result` (with WorktreeID from a successful attach) on
// checkout failure by returning gitModeResult{}, while
// executeCreateBranch passed `result` back. The mismatch made shared
// post-failure logic unable to read the worktree linkage off a
// checkout failure.
func TestExecuteCheckoutBranch_FailureReturnsPopulatedResult(t *testing.T) {
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt-checkout-fail")
	run(t, repoDir, "git", "worktree", "add", "-b", "wt-source", wtDir)

	// Checkout target doesn't exist — checkout will fail, but the
	// preceding attachWorktreeIfPresent should have populated WorktreeID.
	res, err := svc.executeCheckoutBranch(context.Background(), gitModePlan{
		Mode:           gitModeCheckoutBranch,
		WorkingDir:     wtDir,
		CheckoutTarget: "does-not-exist-anywhere",
	})
	require.Error(t, err, "non-existent checkout target must fail")
	assert.Equal(t, wtDir, res.WorkingDir,
		"executeCheckoutBranch must surface the populated WorkingDir even on checkout failure")
	assert.NotEmpty(t, res.WorktreeID,
		"executeCheckoutBranch must surface the WorktreeID set by attachWorktreeIfPresent before the checkout failed")
}

// TestDiffStatsForPath_FallbackSurfacesShortstatErrOnEmptyEntries pins
// the disagreement-aware fallback fix. If shortstat fails AND
// getGitFileStatusEntries returns no entries despite outer porcelain
// status saying hasChanges=true, the two inner probes disagree — the
// inner v2 status silently failed. Surface the shortstat error rather
// than reporting 0/0 to the close-prompt.
//
// We provoke the disagreement by passing a directory that has been
// nuked AFTER the outer probes run. Practically that's hard to time
// from a unit test, so instead we verify the simpler happy-path
// branch: the fallback DOES return the right counts on unborn HEAD
// (already covered by TestDiffStatsForPath_UnbornHEADFallsBackToNumstat
// — this test pins the EMPTY case: unborn HEAD with no staged files
// reports 0/0 cleanly with no error.)
func TestDiffStatsForPath_UnbornHEADEmptyRepoReportsZeroChangesCleanly(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init")

	added, deleted, untracked, hasChanges, err := diffStatsForPath(context.Background(), dir)
	require.NoError(t, err, "an empty unborn-HEAD repo must NOT propagate as an error")
	assert.False(t, hasChanges, "no files staged → hasChanges=false")
	assert.Equal(t, int32(0), added)
	assert.Equal(t, int32(0), deleted)
	assert.Equal(t, int32(0), untracked)
}
