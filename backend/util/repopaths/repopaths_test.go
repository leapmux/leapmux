package repopaths

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), string(out))
	return string(out)
}

func TestGitToplevelAndExclude_RegularRepoAndSubdir(t *testing.T) {
	t.Parallel()

	repo := testutil.NewGitRepo(t)
	sub := filepath.Join(repo, "a", "b", "c")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	gotTop, ok := GitToplevel(repo)
	require.True(t, ok)
	assert.Equal(t, filepath.Clean(repo), gotTop)

	gotSubTop, ok := GitToplevel(sub)
	require.True(t, ok)
	assert.Equal(t, filepath.Clean(repo), gotSubTop)

	// Non-existent nested path inside the repo still resolves to the toplevel.
	gotAbsentTop, ok := GitToplevel(filepath.Join(repo, "not-yet", "nested", "file.json"))
	require.True(t, ok)
	assert.Equal(t, filepath.Clean(repo), gotAbsentTop)

	wantExclude := filepath.Join(repo, ".git", "info", "exclude")
	gotExclude, ok := GitInfoExcludePath(sub)
	require.True(t, ok)
	assert.Equal(t, wantExclude, gotExclude)

	require.NoError(t, EnsureTmpExcluded(sub))

	// git check-ignore confirms Git actually ignores files under .leapmux/tmp/.
	runGit(t, repo, "check-ignore", "-q", ".leapmux/tmp/foo")
}

func TestGitInfoExcludePathAndEnsureTmpExcluded_LinkedWorktree(t *testing.T) {
	t.Parallel()

	mainRepo := testutil.NewGitRepo(t)
	wtDir := filepath.Join(t.TempDir(), "linked-wt")
	runGit(t, mainRepo, "worktree", "add", "-b", "wt-branch", wtDir)

	wtSub := filepath.Join(wtDir, "nested", "dir")
	require.NoError(t, os.MkdirAll(wtSub, 0o755))

	// GitToplevel returns the linked worktree directory, not the main checkout.
	gotTop, ok := GitToplevel(wtSub)
	require.True(t, ok)
	assert.Equal(t, filepath.Clean(wtDir), gotTop)

	// GitInfoExcludePath resolves through commondir to the shared main .git/info/exclude.
	gotExclude, ok := GitInfoExcludePath(wtSub)
	require.True(t, ok)
	wantExclude, err := filepath.EvalSymlinks(filepath.Join(mainRepo, ".git", "info", "exclude"))
	if err != nil {
		wantExclude = filepath.Join(mainRepo, ".git", "info", "exclude")
	}
	gotExcludeEval, err := filepath.EvalSymlinks(gotExclude)
	if err != nil {
		gotExcludeEval = gotExclude
	}
	assert.Equal(t, filepath.Clean(wantExclude), filepath.Clean(gotExcludeEval))

	require.NoError(t, EnsureTmpExcluded(wtSub))

	// Both the main checkout and the linked worktree must ignore .leapmux/tmp/foo.
	runGit(t, mainRepo, "check-ignore", "-q", ".leapmux/tmp/foo")
	runGit(t, wtDir, "check-ignore", "-q", ".leapmux/tmp/foo")
}

func TestGitInfoExcludePath_SubmoduleStyleGitDirWithoutCommonDir(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	fakeGitDir := filepath.Join(base, "modules", "sub")
	require.NoError(t, os.MkdirAll(fakeGitDir, 0o755))
	workDir := filepath.Join(base, "work")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	// Relative gitdir pointer without commondir (submodule layout).
	require.NoError(t, os.WriteFile(filepath.Join(workDir, ".git"), []byte("gitdir: ../modules/sub\r\n"), 0o644))

	top, ok := GitToplevel(workDir)
	require.True(t, ok)
	assert.Equal(t, filepath.Clean(workDir), top)

	excludePath, ok := GitInfoExcludePath(workDir)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(fakeGitDir, "info", "exclude"), excludePath)

	// EnsureTmpExcluded creates the missing info/ directory and writes exclude.
	require.NoError(t, EnsureTmpExcluded(workDir))
	raw, err := os.ReadFile(excludePath)
	require.NoError(t, err)
	assert.Equal(t, ".leapmux/tmp/\n", string(raw))
}

func TestNonGitDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	top, ok := GitToplevel(dir)
	assert.False(t, ok)
	assert.Empty(t, top)

	excludePath, ok := GitInfoExcludePath(dir)
	assert.False(t, ok)
	assert.Empty(t, excludePath)

	// Empty input returns false safely.
	top, ok = GitToplevel("")
	assert.False(t, ok)
	assert.Empty(t, top)

	// EnsureTmpExcluded and EnsureTmpExcludedIfInside are no-ops outside a git repo.
	require.NoError(t, EnsureTmpExcluded(dir))
	require.NoError(t, EnsureTmpExcludedIfInside(filepath.Join(dir, ".leapmux", "tmp", "file.json")))
}

func TestEnsureTmpExcluded_IdempotencyAndEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("calling twice does not duplicate lines", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		excludePath := filepath.Join(repo, ".git", "info", "exclude")

		require.NoError(t, EnsureTmpExcluded(repo))
		first, err := os.ReadFile(excludePath)
		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(string(first), ".leapmux/tmp/"))

		require.NoError(t, EnsureTmpExcluded(repo))
		second, err := os.ReadFile(excludePath)
		require.NoError(t, err)
		assert.Equal(t, string(first), string(second))
	})

	t.Run("existing .leapmux/tmp without trailing slash is respected", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		excludePath := filepath.Join(repo, ".git", "info", "exclude")
		initial := "# local excludes\n.leapmux/tmp\n"
		require.NoError(t, os.WriteFile(excludePath, []byte(initial), 0o644))

		require.NoError(t, EnsureTmpExcluded(repo))
		got, err := os.ReadFile(excludePath)
		require.NoError(t, err)
		assert.Equal(t, initial, string(got))
	})

	t.Run("commented-out entry does not count as excluded", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		excludePath := filepath.Join(repo, ".git", "info", "exclude")
		initial := "# .leapmux/tmp/\n*.log\n"
		require.NoError(t, os.WriteFile(excludePath, []byte(initial), 0o644))

		require.NoError(t, EnsureTmpExcluded(repo))
		got, err := os.ReadFile(excludePath)
		require.NoError(t, err)
		assert.Equal(t, initial+".leapmux/tmp/\n", string(got))
	})

	t.Run("existing file missing trailing newline gets newline before entry", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		excludePath := filepath.Join(repo, ".git", "info", "exclude")
		require.NoError(t, os.WriteFile(excludePath, []byte("*.swp"), 0o600))
		require.NoError(t, os.Chmod(excludePath, 0o600))

		require.NoError(t, EnsureTmpExcluded(repo))
		got, err := os.ReadFile(excludePath)
		require.NoError(t, err)
		assert.Equal(t, "*.swp\n.leapmux/tmp/\n", string(got))

		if runtime.GOOS != "windows" {
			fi, err := os.Stat(excludePath)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm(), "existing file mode must be preserved")
		}
	})

	t.Run("missing info dir and exclude file are created", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		infoDir := filepath.Join(repo, ".git", "info")
		require.NoError(t, os.RemoveAll(infoDir))

		require.NoError(t, EnsureTmpExcluded(repo))
		got, err := os.ReadFile(filepath.Join(infoDir, "exclude"))
		require.NoError(t, err)
		assert.Equal(t, ".leapmux/tmp/\n", string(got))
	})

	t.Run("exclude path as directory returns error from EnsureTmpExcluded", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		excludePath := filepath.Join(repo, ".git", "info", "exclude")
		require.NoError(t, os.RemoveAll(excludePath))
		require.NoError(t, os.MkdirAll(excludePath, 0o755))

		err := EnsureTmpExcluded(repo)
		require.Error(t, err)
		assert.ErrorContains(t, err, "is a directory")
	})

	t.Run("symlinked info/exclude preserves symlink and updates resolved target", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		excludePath := filepath.Join(repo, ".git", "info", "exclude")
		targetDir := t.TempDir()
		targetPath := filepath.Join(targetDir, "shared-exclude")
		require.NoError(t, os.WriteFile(targetPath, []byte("*.bak\n"), 0o600))
		require.NoError(t, os.Remove(excludePath))
		if err := os.Symlink(targetPath, excludePath); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink creation unavailable on Windows: %v", err)
			}
			require.NoError(t, err)
		}

		require.NoError(t, EnsureTmpExcluded(repo))

		lfi, err := os.Lstat(excludePath)
		require.NoError(t, err)
		assert.NotZero(t, lfi.Mode()&os.ModeSymlink, "info/exclude must remain a symlink")

		gotTarget, err := os.ReadFile(targetPath)
		require.NoError(t, err)
		assert.Equal(t, "*.bak\n.leapmux/tmp/\n", string(gotTarget))

		runGit(t, repo, "check-ignore", "-q", ".leapmux/tmp/foo")
	})

	t.Run("concurrent EnsureTmpExcluded writes entry once with lock file", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		excludePath := filepath.Join(repo, ".git", "info", "exclude")
		require.NoError(t, os.WriteFile(excludePath, []byte("*.tmp\n"), 0o644))

		const workers = 8
		var wg sync.WaitGroup
		errs := make([]error, workers)
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				errs[idx] = EnsureTmpExcluded(repo)
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			require.NoError(t, err)
		}

		got, err := os.ReadFile(excludePath)
		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(string(got), ".leapmux/tmp/"))
		assert.FileExists(t, excludePath+".lock")
	})
}

func TestEnsureTmpExcludedIfInside(t *testing.T) {
	t.Parallel()

	t.Run("excludes when path is inside repo .leapmux/tmp", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		excludePath := filepath.Join(repo, ".git", "info", "exclude")

		require.NoError(t, EnsureTmpExcludedIfInside(filepath.Join(repo, ".leapmux", "tmp", "exports", "out.md")))
		raw, err := os.ReadFile(excludePath)
		require.NoError(t, err)
		assert.Contains(t, string(raw), ".leapmux/tmp/")
	})

	t.Run("does not modify exclude for paths outside .leapmux/tmp", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		excludePath := filepath.Join(repo, ".git", "info", "exclude")
		before, err := os.ReadFile(excludePath)
		require.NoError(t, err)

		for _, p := range []string{
			filepath.Join(repo, "transcript.md"),
			filepath.Join(repo, ".leapmux", "conf", "workspace.yaml"),
			filepath.Join(repo, ".leapmux", "tmp2", "file.json"),
			filepath.Join(repo, "sub", ".leapmux", "tmp", "nested.json"),
		} {
			require.NoError(t, EnsureTmpExcludedIfInside(p))
		}

		after, err := os.ReadFile(excludePath)
		require.NoError(t, err)
		assert.Equal(t, string(before), string(after))
	})
}

func TestIsInsideLeapMuxTmp(t *testing.T) {
	t.Parallel()

	repo := testutil.NewGitRepo(t)
	nonGit := t.TempDir()

	// Exact .leapmux/tmp directory matches.
	top, ok := IsInsideLeapMuxTmp(filepath.Join(repo, ".leapmux", "tmp"))
	assert.True(t, ok)
	assert.Equal(t, filepath.Clean(repo), top)

	// Nested file inside .leapmux/tmp matches even before the file exists.
	top, ok = IsInsideLeapMuxTmp(filepath.Join(repo, ".leapmux", "tmp", "checkpoints", "snap.json"))
	assert.True(t, ok)
	assert.Equal(t, filepath.Clean(repo), top)

	// Sibling prefix .leapmux/tmp2 must NOT match.
	top, ok = IsInsideLeapMuxTmp(filepath.Join(repo, ".leapmux", "tmp2"))
	assert.False(t, ok)
	assert.Empty(t, top)

	top, ok = IsInsideLeapMuxTmp(filepath.Join(repo, ".leapmux", "tmp2", "file.json"))
	assert.False(t, ok)
	assert.Empty(t, top)

	// Other .leapmux categories and repo root do not match.
	for _, p := range []string{
		"",
		"   ",
		repo,
		filepath.Join(repo, ".leapmux"),
		filepath.Join(repo, ".leapmux", "conf", "settings.yaml"),
		filepath.Join(repo, ".leapmux", "tmp", "..", "conf", "file.yaml"),
		filepath.Join(nonGit, ".leapmux", "tmp", "file.json"),
	} {
		gotTop, gotOK := IsInsideLeapMuxTmp(p)
		assert.False(t, gotOK, "path %q should not match", p)
		assert.Empty(t, gotTop, "path %q should return empty toplevel", p)
	}
}

func TestIsInsideLeapMuxTmp_RelativePath(t *testing.T) {
	repo := testutil.NewGitRepo(t)
	t.Chdir(repo)

	top, ok := IsInsideLeapMuxTmp(filepath.Join(".leapmux", "tmp", "export.json"))
	require.True(t, ok)
	wantTop, err := filepath.EvalSymlinks(repo)
	if err != nil {
		wantTop = repo
	}
	gotTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		gotTop = top
	}
	assert.Equal(t, filepath.Clean(wantTop), filepath.Clean(gotTop))

	_, ok = IsInsideLeapMuxTmp(filepath.Join(".leapmux", "tmp2", "export.json"))
	assert.False(t, ok)
}

func TestGitToplevel_InvalidGitFileIgnored(t *testing.T) {
	t.Parallel()

	t.Run("non-gitdir content is ignored", func(t *testing.T) {
		t.Parallel()
		outer := testutil.NewGitRepo(t)
		inner := filepath.Join(outer, "sub")
		require.NoError(t, os.MkdirAll(inner, 0o755))
		// A regular .git file that does not start with "gitdir:" is not a valid git entry.
		require.NoError(t, os.WriteFile(filepath.Join(inner, ".git"), []byte("not a gitdir pointer\n"), 0o644))

		top, ok := GitToplevel(inner)
		require.True(t, ok)
		assert.Equal(t, filepath.Clean(outer), top)
	})

	t.Run("empty gitdir target is ignored", func(t *testing.T) {
		t.Parallel()
		outer := testutil.NewGitRepo(t)
		inner := filepath.Join(outer, "sub")
		require.NoError(t, os.MkdirAll(inner, 0o755))
		gitFile := filepath.Join(inner, ".git")
		require.NoError(t, os.WriteFile(gitFile, []byte("gitdir:   \n"), 0o644))

		gotDir, ok := parseGitDirFile(inner, gitFile)
		assert.False(t, ok)
		assert.Empty(t, gotDir)

		top, ok := GitToplevel(inner)
		require.True(t, ok)
		assert.Equal(t, filepath.Clean(outer), top)

		standalone := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(standalone, ".git"), []byte("gitdir:   "), 0o644))
		top, ok = GitToplevel(standalone)
		assert.False(t, ok)
		assert.Empty(t, top)
	})
}

func TestGitInfoExcludePath_CommonDirEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("empty commondir file falls back to gitDir", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()
		fakeGitDir := filepath.Join(base, "worktrees", "wt1")
		require.NoError(t, os.MkdirAll(fakeGitDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(fakeGitDir, "commondir"), []byte("   \n"), 0o644))
		workDir := filepath.Join(base, "work")
		require.NoError(t, os.MkdirAll(workDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(workDir, ".git"), []byte("gitdir: ../worktrees/wt1\n"), 0o644))

		excludePath, ok := GitInfoExcludePath(workDir)
		require.True(t, ok)
		assert.Equal(t, filepath.Join(fakeGitDir, "info", "exclude"), excludePath)
	})

	t.Run("commondir as directory returns false", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()
		fakeGitDir := filepath.Join(base, "worktrees", "wt1")
		require.NoError(t, os.MkdirAll(filepath.Join(fakeGitDir, "commondir"), 0o755))
		workDir := filepath.Join(base, "work")
		require.NoError(t, os.MkdirAll(workDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(workDir, ".git"), []byte("gitdir: ../worktrees/wt1\n"), 0o644))

		excludePath, ok := GitInfoExcludePath(workDir)
		assert.False(t, ok)
		assert.Empty(t, excludePath)
	})
}
