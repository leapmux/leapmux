package testutil

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// NewGitRepo returns the path to a fresh git repository holding a single
// empty initial commit on branch `main`. The repo lives under the test's own
// temp dir, so it is removed with the test and callers may mutate it freely
// (commit, branch, tag, add worktrees) without affecting any other test.
//
// The repo is materialized from an in-process template rather than built by
// running git each time. `git init` + two `git config` + `git commit` is four
// process spawns and costs ~250ms on macOS; writing out the ~30 small files
// they produce costs single-digit milliseconds. The worker packages create
// hundreds of these repos per run, so that gap is most of their wall time.
//
// `--initial-branch=main` is pinned when the template is built so the helper
// does not inherit the host's `init.defaultBranch`. CI runners and contributor
// machines without that config land on `master`, but the tests here assume
// `main` (rev-parse against `refs/heads/main`, `origin/main..HEAD` ranges,
// switch-to-`main` targets).
//
// Every repo in a process is byte-identical, so they share one initial-commit
// hash. Tests that need two repos with DIFFERENT histories must add a commit
// of their own to at least one of them.
func NewGitRepo(t *testing.T) string {
	t.Helper()

	tmpl, err := gitRepoTemplate()
	require.NoError(t, err, "build git repo template")

	dir := t.TempDir()
	for _, f := range tmpl {
		path := filepath.Join(dir, f.path)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755), "materialize git repo")
		require.NoError(t, os.WriteFile(path, f.data, f.mode), "materialize git repo")
	}
	return dir
}

// gitDaemonOffConfig turns off every git background process that outlives the
// command which triggered it.
//
// A host with core.fsmonitor enabled globally (macOS dev machines commonly do)
// makes git spawn a filesystem-monitor DAEMON per repo. It keeps writing under
// .git and drops a unix socket there, so a test's t.TempDir() cleanup races it
// and fails with "directory not empty". gc.auto / maintenance.auto are the same
// hazard in slower motion: a background `git gc` fired by a commit writes into
// .git after the command has returned.
var gitDaemonOffConfig = [][]string{
	{"config", "core.fsmonitor", "false"},
	{"config", "gc.auto", "0"},
	{"config", "maintenance.auto", "false"},
}

// NewEmptyGitRepo creates a repo with an UNBORN HEAD -- initialized, no commit
// -- in a fresh t.TempDir(), and returns its path.
//
// Separate from NewGitRepo because the template there carries an initial
// commit, and the unborn-HEAD paths (queryGitPathInfo, diffStatsForPath's
// numstat fallback) are exactly what several tests exist to pin. It is not
// templated: `git init` alone is one fork, and the point of the template is to
// avoid the config+commit forks on top of it.
//
// Use this rather than a bare `git init`, so the daemon-off config above
// applies here too -- an unpinned repo reintroduces the fsmonitor/TempDir race
// for whichever host has fsmonitor on.
func NewEmptyGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitConfigured(t, dir, []string{"-c", "core.fsmonitor=false", "init", "--initial-branch=main"})
	for _, args := range gitDaemonOffConfig {
		runGitConfigured(t, dir, args)
	}
	return dir
}

func runGitConfigured(t *testing.T, dir string, args []string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
}

// templateFile is one regular file of the template repo, held in memory.
type templateFile struct {
	path string // relative to the repo root
	mode fs.FileMode
	data []byte
}

// gitRepoTemplate returns the process-wide template, building it on first use.
//
// sync.OnceValues rather than a hand-rolled mutex+flag pair (the same shape
// terminal.shells uses), and the scratch directory is its own rather than
// borrowed from whichever test called first -- a borrowed t.TempDir() tied the
// build to that test's lifetime for no reason, and the snapshot is in memory
// the moment it has been read, so the directory is disposable immediately.
var gitRepoTemplate = sync.OnceValues(func() ([]templateFile, error) {
	scratch, err := os.MkdirTemp("", "leapmux-git-template-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	return buildGitRepoTemplate(scratch)
})

func buildGitRepoTemplate(scratch string) ([]templateFile, error) {
	dir := filepath.Join(scratch, "git-repo-template")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// -c on the init itself, then the same settings written INTO the repo
	// (gitDaemonOffConfig) so every materialized copy carries them -- a copy
	// that only inherited the init-time -c would spawn the daemon on its own
	// first command. The socket such a daemon drops is not a regular file, so
	// it could not be carried in the template either.
	steps := [][]string{{"-c", "core.fsmonitor=false", "init", "--initial-branch=main"}}
	steps = append(steps, gitDaemonOffConfig...)
	steps = append(steps,
		[]string{"config", "user.email", "test@test.com"},
		[]string{"config", "user.name", "Test"},
		[]string{"commit", "--allow-empty", "-m", "init"},
	)
	for _, args := range steps {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, out)
		}
	}

	var files []templateFile
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Only regular files are carried. Materialization MkdirAlls each
		// file's own parent, so a directory git left EMPTY (.git/objects/pack,
		// .git/objects/info, .git/refs/tags) is absent from every copy -- git
		// recreates all three on demand (verified against `git tag`, `gc`,
		// `repack`, `fetch`, `push`, `clone`, `stash`, `worktree add`, `fsck`),
		// because its leading-directory creation for loose objects, refs and
		// packs is unconditional. A non-regular entry (socket, device) has no
		// bytes to carry either.
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, templateFile{path: rel, mode: info.Mode().Perm(), data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
