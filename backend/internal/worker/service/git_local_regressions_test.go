package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCheckoutBranch_LocalSlashNamePreservesFullIdentity(t *testing.T) {
	t.Parallel()
	for _, branch := range []string{"feature/-foo", "feature/@"} {
		for _, remoteCollision := range []bool{false, true} {
			label := branch
			if remoteCollision {
				label += "/remote-collision"
			}
			t.Run(label, func(t *testing.T) {
				dir := initRepo(t)
				original := strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
				run(t, dir, "git", "branch", branch)
				if remoteCollision {
					run(t, dir, "git", "commit", "--allow-empty", "-m", "different remote commit")
					run(t, dir, "git", "update-ref", "refs/remotes/"+branch, "HEAD")
				}
				branches, err := listGitBranches(t.Context(), dir)
				require.NoError(t, err)
				found := false
				for _, entry := range branches {
					if !entry.GetIsRemote() && entry.GetName() == branch {
						found = true
					}
				}
				require.True(t, found, "the offered branch must be checkoutable")
				require.NoError(t, checkoutBranchInDir(t.Context(), dir, branch))
				assert.Equal(t, "refs/heads/"+branch, strings.TrimSpace(run(t, dir, "git", "symbolic-ref", "HEAD")))
				assert.Equal(t, original, strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD")))
			})
		}
	}
}

func TestCheckoutBranch_RejectsInvalidSuffixOnlyForRemoteResolution(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"--help", "@"} {
		t.Run(suffix, func(t *testing.T) {
			dir := initRepo(t)
			run(t, dir, "git", "update-ref", "refs/remotes/origin/"+suffix, "HEAD")
			err := checkoutBranchInDir(t.Context(), dir, "origin/"+suffix)
			require.Error(t, err)
			assert.True(t, gitutil.IsBranchNameError(err))
			assert.Equal(t, "refs/heads/main", strings.TrimSpace(run(t, dir, "git", "symbolic-ref", "HEAD")))
			_, err = gitutil.Output(t.Context(), dir, "show-ref", "--verify", "refs/heads/"+suffix)
			assert.Error(t, err)
		})
	}
}

func TestReadGitFile_NestedBlobPaths(t *testing.T) {
	t.Parallel()
	for _, ref := range []leapmuxv1.GitFileRef{leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD, leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED} {
		t.Run(ref.String(), func(t *testing.T) {
			dir := canonicalRepoDir(t)
			path := filepath.Join(dir, "nested", "deep", "tracked.txt")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
			require.NoError(t, os.WriteFile(path, []byte("committed\n"), 0600))
			run(t, dir, "git", "add", ".")
			run(t, dir, "git", "commit", "-m", "nested file")
			require.NoError(t, os.WriteFile(path, []byte("staged\n"), 0600))
			run(t, dir, "git", "add", ".")
			require.NoError(t, os.WriteFile(path, []byte("working\n"), 0600))
			_, d, w := setupTestService(t)
			dispatch(d, leapmuxv1connect.WorkerGitServiceReadGitFileProcedure, &leapmuxv1.ReadGitFileRequest{Path: path, Ref: ref}, w)
			require.Empty(t, w.errors)
			require.Len(t, w.responses, 1)
			var resp leapmuxv1.ReadGitFileResponse
			require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
			require.True(t, resp.GetExists())
			want := "committed\n"
			if ref == leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED {
				want = "staged\n"
			}
			assert.Equal(t, want, string(resp.GetContent()))
			assert.Equal(t, path, resp.GetPath())
		})
	}
}

func TestQueryGitPathInfo_UsesExactLocalBranchUnderRefCollision(t *testing.T) {
	t.Parallel()
	for _, additionalLocalCollision := range []bool{false, true} {
		label := "remote collision"
		if additionalLocalCollision {
			label = "remote and local collisions"
		}
		t.Run(label, func(t *testing.T) {
			dir := canonicalRepoDir(t)
			run(t, dir, "git", "checkout", "-b", "feature/auth")
			head := strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
			run(t, dir, "git", "update-ref", "refs/remotes/feature/auth", head)
			if additionalLocalCollision {
				run(t, dir, "git", "update-ref", "refs/heads/heads/feature/auth", head)
			}
			run(t, dir, "git", "config", "remote.origin.url", "unused")
			run(t, dir, "git", "config", "branch.feature/auth.remote", "elsewhere")
			run(t, dir, "git", "config", "branch.feature/auth.merge", "refs/heads/auth")
			run(t, dir, "git", "update-ref", "refs/remotes/elsewhere/auth", head)
			info, err := queryGitPathInfo(t.Context(), dir)
			require.NoError(t, err)
			assert.Equal(t, "feature/auth", info.BranchName)
			assert.Equal(t, head, info.HeadSHA)
			assert.True(t, pathutil.SamePath(dir, info.TopLevel))
			assert.True(t, pathutil.SamePath(dir, info.RepoRoot))
			assert.False(t, info.IsWorktree)
			status, err := pushStatusForPath(t.Context(), dir, info.BranchName)
			require.NoError(t, err)
			assert.True(t, status.UpstreamExists)
			assert.Zero(t, status.Unpushed)
			assert.False(t, status.RemoteMissing)
		})
	}
}

func TestPushBranch_PreservesUpstreamUnderRefCollision(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	dir := initRepo(t)
	remote := t.TempDir()
	initBareRepo(t, remote)
	run(t, dir, "git", "remote", "add", "origin", remote)
	run(t, dir, "git", "remote", "add", "elsewhere", remote)
	run(t, dir, "git", "checkout", "-b", "feature/auth")
	run(t, dir, "git", "push", "-u", "elsewhere", "feature/auth")
	run(t, dir, "git", "update-ref", "refs/remotes/feature/auth", "HEAD")
	require.NoError(t, svc.pushBranch(t.Context(), dir))
	assert.Equal(t, "elsewhere", strings.TrimSpace(run(t, dir, "git", "config", "branch.feature/auth.remote")))
	assert.Equal(t, "refs/heads/feature/auth", strings.TrimSpace(run(t, dir, "git", "config", "branch.feature/auth.merge")))
}

func TestDetachedGitInspection_DisplaysCommitWithoutOfferingPush(t *testing.T) {
	t.Parallel()
	for _, worktree := range []bool{false, true} {
		label := "main checkout"
		if worktree {
			label = "linked worktree"
		}
		t.Run(label, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			repo := initRepo(t)
			remote := t.TempDir()
			initBareRepo(t, remote)
			run(t, repo, "git", "remote", "add", "origin", remote)
			run(t, repo, "git", "push", "-u", "origin", "main")
			dir := repo
			if worktree {
				dir = filepath.Join(t.TempDir(), "linked")
				run(t, repo, "git", "worktree", "add", "-b", "local-work", dir)
			}
			run(t, dir, "git", "commit", "--allow-empty", "-m", "local only")
			fullSHA := strings.TrimSpace(run(t, dir, "git", "rev-parse", "HEAD"))
			run(t, dir, "git", "checkout", "--detach", "HEAD")
			for _, hint := range []string{"", fullSHA[:7]} {
				resp, err := svc.inspectBranchDeletion(t.Context(), dir, hint)
				require.NoError(t, err)
				assert.Equal(t, fullSHA[:7], resp.GetBranchName())
				require.NotNil(t, resp.GetGitState())
				assert.Equal(t, int32(1), resp.GetGitState().GetUnpushedCommitCount())
				assert.False(t, resp.GetGitState().GetCanPush())
			}
			createAgentForPath(t, svc, "detached-agent", dir)
			resp, err := svc.inspectLastTabClose(t.Context(), leapmuxv1.TabType_TAB_TYPE_AGENT, "detached-agent", "user-1")
			require.NoError(t, err)
			assert.Equal(t, fullSHA[:7], resp.GetBranchName())
			require.NotNil(t, resp.GetGitState())
			assert.Equal(t, int32(1), resp.GetGitState().GetUnpushedCommitCount())
			assert.False(t, resp.GetGitState().GetCanPush())
			if !worktree {
				createAgentForPath(t, svc, "detached-sibling", dir)
				resp, err = svc.inspectLastTabClose(t.Context(), leapmuxv1.TabType_TAB_TYPE_AGENT, "detached-agent", "user-1")
				require.NoError(t, err)
				assert.False(t, resp.GetShouldPrompt())
				assert.Nil(t, resp.GetGitState())
			}
		})
	}
}

func TestPushBranch_RefusesDetachedHEADBeforeChangingFilesOrRefs(t *testing.T) {
	t.Parallel()
	for _, dirty := range []bool{false, true} {
		label := "clean"
		if dirty {
			label = "dirty"
		}
		t.Run(label, func(t *testing.T) {
			svc, _, _ := setupTestService(t)
			dir := initRepo(t)
			remote := t.TempDir()
			initBareRepo(t, remote)
			run(t, dir, "git", "remote", "add", "origin", remote)
			run(t, dir, "git", "push", "-u", "origin", "main")
			run(t, dir, "git", "checkout", "--detach", "HEAD")
			if dirty {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("keep dirty\n"), 0600))
			}
			head := run(t, dir, "git", "rev-parse", "HEAD")
			status := run(t, dir, "git", "status", "--porcelain")
			refs := run(t, remote, "git", "show-ref")
			err := svc.pushBranch(t.Context(), dir)
			require.ErrorContains(t, err, "detached HEAD")
			assert.Equal(t, head, run(t, dir, "git", "rev-parse", "HEAD"))
			assert.Equal(t, status, run(t, dir, "git", "status", "--porcelain"))
			assert.Equal(t, refs, run(t, remote, "git", "show-ref"))
		})
	}
}

func TestPushBranch_AllowsAttachedHexadecimalBranch(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	dir := initRepo(t)
	remote := t.TempDir()
	initBareRepo(t, remote)
	run(t, dir, "git", "remote", "add", "origin", remote)
	run(t, dir, "git", "checkout", "-b", "abcdef0")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("content\n"), 0600))
	require.NoError(t, svc.pushBranch(t.Context(), dir))
	assert.Equal(t, run(t, dir, "git", "rev-parse", "HEAD"), run(t, remote, "git", "rev-parse", "refs/heads/abcdef0"))
}

type fileInfoWithModTime struct {
	os.FileInfo
	timestamp time.Time
}

func (f fileInfoWithModTime) ModTime() time.Time { return f.timestamp }

func TestAnnotateFileStats_PreservesOrderedInstantsWithinOneSecond(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "base")
	require.NoError(t, os.WriteFile(path, nil, 0600))
	base, err := os.Stat(path)
	require.NoError(t, err)
	files := []*leapmuxv1.GitFileStatusEntry{{Path: "first"}, {Path: "second"}}
	first := time.Date(2026, time.January, 2, 15, 4, 5, 125000000, time.UTC)
	second := time.Date(2026, time.January, 2, 15, 4, 5, 875000000, time.UTC)
	annotateFileStatsWithTimeout(dir, files, 30*time.Second, func(path string) (os.FileInfo, error) {
		timestamp := first
		if filepath.Base(path) == "second" {
			timestamp = second
		}
		return fileInfoWithModTime{FileInfo: base, timestamp: timestamp}, nil
	})
	assert.Equal(t, "2026-01-02T15:04:05.125000000Z", files[0].GetModTime())
	assert.Equal(t, "2026-01-02T15:04:05.875000000Z", files[1].GetModTime())
	assert.Less(t, files[0].GetModTime(), files[1].GetModTime())
}

func TestQueryGitPathInfo_UnbornBranchKeepsExactSymrefUnderCollision(t *testing.T) {
	t.Parallel()
	dir := testutil.NewEmptyGitRepo(t)
	donor := initRepo(t)
	run(t, dir, "git", "fetch", donor, "HEAD:refs/remotes/feature/auth")
	run(t, dir, "git", "symbolic-ref", "HEAD", "refs/heads/feature/auth")
	info, err := queryGitPathInfo(t.Context(), dir)
	require.NoError(t, err)
	assert.Equal(t, "feature/auth", info.BranchName)
	assert.Empty(t, info.HeadSHA)
	assert.False(t, info.IsWorktree)
}

func TestLoadGitContextForDirPreservesContextFailure(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	dir := initRepo(t)
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		want := context.Canceled
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Hour))
			want = context.DeadlineExceeded
		}
		cancel()
		info, err := svc.loadGitContextForDir(ctx, dir)
		assert.Nil(t, info)
		assert.ErrorIs(t, err, want)
	}
	info, err := svc.loadGitContextForDir(t.Context(), t.TempDir())
	assert.Nil(t, info)
	assert.EqualError(t, err, "tab is not in a git repository")
}
