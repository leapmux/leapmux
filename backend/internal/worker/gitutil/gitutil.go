package gitutil

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/procutil"
)

// batchGetGitStatusMaxConcurrent caps the number of concurrent `git status`
// shell-outs BatchGetGitStatus launches. A WatchEvents replay across many
// unique repos would otherwise fork one git subprocess per repo in parallel.
const batchGetGitStatusMaxConcurrent = 8

// BatchGetGitStatus returns one AgentGitStatus per input directory,
// deduplicating identical paths so a request listing many tabs rooted at
// the same repo only runs a single `git status` shell-out. Unique paths
// are fanned out concurrently (capped at batchGetGitStatusMaxConcurrent);
// results are mapped back to every position that asked for that path.
// Empty-string entries yield nil. The supplied context is threaded into
// every shell-out so a caller cancelling mid-fan kills in-flight git
// processes.
func BatchGetGitStatus(ctx context.Context, dirs []string) []*leapmuxv1.AgentGitStatus {
	results := make([]*leapmuxv1.AgentGitStatus, len(dirs))
	unique := make(map[string][]int, len(dirs))
	for i, d := range dirs {
		if d == "" {
			continue
		}
		unique[d] = append(unique[d], i)
	}
	sem := make(chan struct{}, batchGetGitStatusMaxConcurrent)
	var wg sync.WaitGroup
	for dir, indexes := range unique {
		// Select on ctx as well as the semaphore: an uninterruptible
		// kernel syscall in a worker goroutine (NFS in stuck state,
		// FUSE wedged) holds its slot indefinitely even after
		// exec.CommandContext fires SIGKILL — D-state processes don't
		// release until the syscall returns. Without this select, the
		// producer goroutine wedges on `sem <- struct{}{}` forever and
		// the caller's goroutine leaks. Bailing on ctx.Done lets
		// cancellation reclaim the producer (any in-flight workers will
		// still exit when their syscall eventually returns, but the
		// dispatcher goroutine that called us doesn't leak).
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return results
		}
		wg.Add(1)
		go func(dir string, indexes []int) {
			defer wg.Done()
			defer func() { <-sem }()
			gs := GetGitStatus(ctx, dir)
			for _, idx := range indexes {
				results[idx] = gs
			}
		}(dir, indexes)
	}
	wg.Wait()
	return results
}

// IsLinkedWorktreeGitDir reports whether a `git rev-parse --git-dir` output
// points at a linked worktree's gitdir (.git/worktrees/<name>).
func IsLinkedWorktreeGitDir(gitDir string) bool {
	return strings.Contains(filepath.ToSlash(gitDir), ".git/worktrees")
}

// StripRemotePrefix strips the remote prefix from a branch ref so the
// local-branch name remains: "origin/foo" -> "foo". A bare local name
// (no "/") is returned unchanged. Mirrors the frontend
// stripRemotePrefix helper in src/lib/validate.ts.
func StripRemotePrefix(ref string) string {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// ResolveGitDir returns the directory whose git status represents a
// terminal/agent tab's repo state: shellStartDir when set, otherwise
// workingDir. The frontend `effectiveGitDir` helper in tab.store.ts
// mirrors this rule, and the two must stay in sync — the optimistic-
// seed guard (`resolveOptimisticGitInfo`) compares the active tab's
// resolved dir against the new tab's resolved dir to decide whether
// it's safe to reuse git info, which only works if both sides resolve
// the same way.
func ResolveGitDir(shellStartDir, workingDir string) string {
	if shellStartDir != "" {
		return shellStartDir
	}
	return workingDir
}

// NewGitCmd creates an exec.Cmd for git with terminal interaction disabled
// and no console window on Windows.
//
// `LC_ALL=C` pins git's messages to the C locale so the worker's
// regex/string parsers (diff --shortstat's `(\d+) insertion`/`deletion`,
// status header text, error-message substrings) stay byte-for-byte
// identical across hosts. Without it, a worker on a system with a
// localized locale (ja_JP.UTF-8, fr_FR.UTF-8, …) emits translated
// strings — `parseDiffShortstat` silently returns (0, 0) and the
// close-tab / delete-branch dialogs render "no changes" on a dirty
// repo. The user-facing trade-off is that bubbled git error messages
// (stderr surfaced via wrapGitErr) are English-only; we accept that
// over silent wrong-answer correctness bugs.
func NewGitCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Stdin = nil
	procutil.HideConsoleWindow(cmd)
	return cmd
}

// GetGitStatus returns the git status for the given directory.
// Best-effort: returns nil if git is not available or the path is not a git repo.
// Failures are logged at debug level so operators can diagnose unexpected misses
// (e.g. permission errors) without the nil return silently losing context.
// The supplied context is threaded into every shell-out so a caller that
// cancels mid-call (e.g. CloseAgent landing during the async startup
// "Checking Git status…" phase) terminates the in-flight `git status`
// subprocess instead of waiting for it to finish.
func GetGitStatus(ctx context.Context, dir string) *leapmuxv1.AgentGitStatus {
	status := &leapmuxv1.AgentGitStatus{}

	// Try porcelain v2 first (git 2.13.2+).
	output, err := Output(ctx, dir, "status", "--porcelain=v2", "--branch")
	if err != nil {
		slog.Debug("git status --porcelain=v2 failed, falling back to v1", "dir", dir, "error", err)
		return getGitStatusV1(ctx, dir)
	}
	parseStatusV2(output, status)

	fanoutGitStatusProbes(ctx, dir, status)
	return status
}

// fanoutGitStatusProbes runs the trailing per-status probes
// (detached-HEAD SHA, stash, origin URL, toplevel) in parallel. Each
// probe is a `git` subprocess that doesn't depend on any of the others,
// so doing them sequentially turned a single `GetGitStatus` into N
// blocking fork/exec round-trips.
//
// Each goroutine writes to a local variable and the merge into `status`
// happens after wg.Wait() — concurrent writes to disjoint fields of a
// proto struct are race-free per Go's memory model TODAY, but the
// protobuf-go contract doesn't promise that future generated accessors
// won't lazily mutate internal state, and any future probe that touched
// an already-claimed field would be a silent data race. The
// goroutine-local + serial-merge pattern keeps this code race-clean
// regardless of how the goroutine bodies evolve.
func fanoutGitStatusProbes(ctx context.Context, dir string, status *leapmuxv1.AgentGitStatus) {
	var (
		wg          sync.WaitGroup
		branch      string
		stashed     bool
		originURL   string
		toplevel    string
		isWorktree  bool
		needsBranch = status.Branch == ""
	)
	if needsBranch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			branch = getShortHEAD(ctx, dir)
		}()
	}
	wg.Add(3)
	go func() {
		defer wg.Done()
		stashed = hasStash(ctx, dir)
	}()
	go func() {
		defer wg.Done()
		originURL = GetOriginURL(ctx, dir)
	}()
	go func() {
		defer wg.Done()
		// Working-tree root so the frontend can distinguish local repos
		// that share the same (missing) origin URL; worktree disposition
		// so the sidebar's BranchGroup.isWorktree (consumed by
		// ChangeBranchDialog for path-info seeding) can be derived
		// without a separate probe.
		info := GetToplevelInfo(ctx, dir)
		toplevel = info.Toplevel
		isWorktree = info.IsWorktree
	}()
	wg.Wait()

	if needsBranch {
		status.Branch = branch
	}
	status.Stashed = stashed
	status.OriginUrl = originURL
	status.Toplevel = toplevel
	status.IsWorktree = isWorktree
}

// parseStatusV2 parses the output of `git status --porcelain=v2 --branch`.
func parseStatusV2(output string, status *leapmuxv1.AgentGitStatus) {
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}

		switch {
		case strings.HasPrefix(line, "# branch.head "):
			status.Branch = strings.TrimPrefix(line, "# branch.head ")
			if status.Branch == "(detached)" {
				status.Branch = ""
			}

		case strings.HasPrefix(line, "# branch.ab "):
			// Format: "# branch.ab +N -M"
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				_, _ = fmt.Sscanf(parts[2], "+%d", &status.Ahead)
				_, _ = fmt.Sscanf(parts[3], "-%d", &status.Behind)
			}

		case strings.HasPrefix(line, "1 "):
			// Ordinary changed entry: "1 XY ..."
			if len(line) >= 4 {
				parseXY(line[2], line[3], status)
			}

		case strings.HasPrefix(line, "2 "):
			// Renamed/copied entry: "2 XY ..."
			status.Renamed = true
			if len(line) >= 4 {
				parseXY(line[2], line[3], status)
			}

		case strings.HasPrefix(line, "u "):
			// Unmerged entry
			status.Conflicted = true

		case strings.HasPrefix(line, "? "):
			// Untracked file
			status.Untracked = true
		}
	}
}

// parseXY parses the X (staging) and Y (worktree) status codes.
func parseXY(x, y byte, status *leapmuxv1.AgentGitStatus) {
	for _, c := range []byte{x, y} {
		switch c {
		case 'M':
			status.Modified = true
		case 'A':
			status.Added = true
		case 'D':
			status.Deleted = true
		case 'T':
			status.TypeChanged = true
		case 'R':
			status.Renamed = true
		}
	}
}

// getGitStatusV1 is the fallback for git versions that don't support --porcelain=v2.
func getGitStatusV1(ctx context.Context, dir string) *leapmuxv1.AgentGitStatus {
	output, err := Output(ctx, dir, "status", "--porcelain", "--branch")
	if err != nil {
		slog.Debug("git status --porcelain failed", "dir", dir, "error", err)
		return nil
	}

	status := &leapmuxv1.AgentGitStatus{}
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "## ") {
			// Branch header: "## branch...tracking [ahead N, behind M]"
			header := strings.TrimPrefix(line, "## ")
			// Extract branch name (before "..." or end of line).
			if dotIdx := strings.Index(header, "..."); dotIdx >= 0 {
				status.Branch = header[:dotIdx]
				// Parse ahead/behind from the rest.
				rest := header[dotIdx+3:]
				if bracketIdx := strings.Index(rest, " ["); bracketIdx >= 0 {
					info := rest[bracketIdx+2:]
					info = strings.TrimSuffix(info, "]")
					for _, part := range strings.Split(info, ", ") {
						_, _ = fmt.Sscanf(part, "ahead %d", &status.Ahead)
						_, _ = fmt.Sscanf(part, "behind %d", &status.Behind)
					}
				}
			} else {
				// No tracking branch, just branch name.
				// Could include markers like " [ahead N]" at the end.
				status.Branch = strings.TrimSpace(header)
			}
			continue
		}

		// File entry: "XY path" (at least 2 status chars + space).
		if len(line) < 3 {
			continue
		}
		x, y := line[0], line[1]

		// Untracked.
		if x == '?' && y == '?' {
			status.Untracked = true
			continue
		}

		// Unmerged (conflict) codes.
		if x == 'U' || y == 'U' || (x == 'D' && y == 'D') || (x == 'A' && y == 'A') {
			status.Conflicted = true
		}

		parseXY(x, y, status)

		if x == 'R' {
			status.Renamed = true
		}
	}

	if status.Branch == "HEAD (no branch)" {
		status.Branch = ""
	}
	fanoutGitStatusProbes(ctx, dir, status)
	return status
}

// hasStash reports whether the repository has any stashed changes.
// Pure read with no shared-state mutation so it's safe to call from a
// fanout goroutine — the caller writes the result back into the proto
// after wg.Wait().
func hasStash(ctx context.Context, dir string) bool {
	return Run(ctx, dir, "rev-parse", "--verify", "refs/stash") == nil
}

// getShortHEAD returns the short commit SHA for HEAD, or empty string on failure.
func getShortHEAD(ctx context.Context, dir string) string {
	output, err := Output(ctx, dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

// GetOriginURL returns the remote origin URL for the given directory.
// Returns an empty string if the directory is not a git repo or has no origin remote.
func GetOriginURL(ctx context.Context, dir string) string {
	output, err := Output(ctx, dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

// GetToplevel returns the absolute path of the git working-tree root for the
// given directory. Returns an empty string when the path is not inside a git
// repository. Used by the sidebar grouping to distinguish origin-less local
// repos that would otherwise collapse under a single "(local repo)" bucket.
func GetToplevel(ctx context.Context, dir string) string {
	return GetToplevelInfo(ctx, dir).Toplevel
}

// ToplevelInfo bundles the working-tree root with the worktree disposition
// so a single `rev-parse` invocation can answer both "where is the repo
// rooted?" and "is this a linked worktree?". `--git-dir` returns the
// per-worktree `.git/worktrees/<name>` for a linked worktree and the
// shared `.git` for the main repo; `--git-common-dir` always points at
// the shared `.git`. They differ iff the path resolves to a linked
// worktree.
type ToplevelInfo struct {
	Toplevel   string
	IsWorktree bool
}

// GetToplevelInfo runs a single rev-parse for both the working-tree root
// and the worktree disposition. Returns a zero value (empty toplevel,
// IsWorktree=false) when the path is not inside a git repository or
// `rev-parse` fails for any other reason.
//
// `git rev-parse` emits one line per requested flag with no `-z` mode
// available for these directory queries. Parse from the END so a path
// containing embedded newlines (legal POSIX, vanishingly rare) doesn't
// silently zero the result: the last two lines are always the `.git`
// paths (which by convention don't contain newlines), and everything
// before them is the (possibly multi-line) toplevel.
func GetToplevelInfo(ctx context.Context, dir string) ToplevelInfo {
	output, err := Output(ctx, dir, "rev-parse", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		return ToplevelInfo{}
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 3 {
		return ToplevelInfo{}
	}
	gitDir := strings.TrimSpace(lines[len(lines)-2])
	gitCommonDir := strings.TrimSpace(lines[len(lines)-1])
	toplevel := strings.TrimSpace(strings.Join(lines[:len(lines)-2], "\n"))
	return ToplevelInfo{
		Toplevel:   toplevel,
		IsWorktree: gitDir != gitCommonDir,
	}
}

// HasRefs probes each of the given fully-qualified refs in a single
// `git show-ref` invocation and returns a map keyed by the input ref
// strings with `true` values for refs that exist. A non-nil error is
// returned only when git itself fails to run; a `show-ref` exit code of
// 1 (no requested ref exists) yields a non-nil empty map.
func HasRefs(ctx context.Context, dir string, refs ...string) (map[string]bool, error) {
	found := make(map[string]bool, len(refs))
	if len(refs) == 0 {
		return found, nil
	}
	args := append([]string{"show-ref"}, refs...)
	out, runErr := Output(ctx, dir, args...)
	if runErr != nil {
		// `git show-ref` exits 1 when none of the requested refs exist;
		// that's a successful "not found" probe, not a git failure.
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
			return found, nil
		}
		return nil, runErr
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		found[fields[1]] = true
	}
	return found, nil
}

// HasAnyRef reports whether any ref exists under the given prefix
// (e.g. "refs/remotes/origin/"). A trailing `/` on the prefix scopes
// the probe to that namespace; an empty prefix matches every ref.
// Returns a non-nil error only when `git for-each-ref` itself fails to
// run — a successful command with empty output yields (false, nil).
func HasAnyRef(ctx context.Context, dir, prefix string) (bool, error) {
	out, runErr := Output(ctx, dir, "for-each-ref", "--count=1", "--format=%(refname)", prefix)
	if runErr != nil {
		return false, runErr
	}
	return strings.TrimSpace(out) != "", nil
}

// LookupRef probes whether `name` resolves as a local branch
// (`refs/heads/<name>`) and/or as a remote-tracking ref
// (`refs/remotes/<name>`) in a single `git show-ref` invocation.
// Returns (false, false, nil) when neither ref exists; only returns a
// non-nil error if git itself fails to run.
//
// Used by the worktree validators to halve the subprocess count
// compared to firing two separate `git rev-parse --verify` probes.
func LookupRef(ctx context.Context, dir, name string) (local, remote bool, err error) {
	headRef := "refs/heads/" + name
	remoteRef := "refs/remotes/" + name
	found, err := HasRefs(ctx, dir, headRef, remoteRef)
	if err != nil {
		return false, false, err
	}
	return found[headRef], found[remoteRef], nil
}

// ErrInvalidArgument is the umbrella sentinel callers wrap their input-
// validation errors with so RPC handlers can route them to a single
// "invalid argument" response category via errors.Is. BranchNameError
// chains to it through Unwrap.
var ErrInvalidArgument = errors.New("invalid argument")

// BranchNameError is the typed error returned by ValidateBranchName for
// every check-ref-format violation. Unwrap chains to ErrInvalidArgument
// so callers that only need the routing decision can use errors.Is;
// callers that need the Reason string can still use
// errors.As(err, &gitutil.BranchNameError{}).
type BranchNameError struct {
	Reason string
}

func (e *BranchNameError) Error() string {
	return "invalid branch name: " + e.Reason
}

func (e *BranchNameError) Unwrap() error {
	return ErrInvalidArgument
}

// IsBranchNameError reports whether err (or any wrapped error in its chain)
// is a *BranchNameError.
func IsBranchNameError(err error) bool {
	var target *BranchNameError
	return errors.As(err, &target)
}

func branchNameErrorf(format string, args ...any) *BranchNameError {
	return &BranchNameError{Reason: fmt.Sprintf(format, args...)}
}

// ValidateBranchName validates a git branch name according to git-check-ref-format rules.
func ValidateBranchName(name string) error {
	if name == "" {
		return branchNameErrorf("must not be empty")
	}
	if len(name) > 256 {
		return branchNameErrorf("must be at most 256 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return branchNameErrorf("must not contain control characters")
		}
		switch r {
		case ' ', '~', '^', ':', '?', '*', '[', ']', '\\':
			return branchNameErrorf("must not contain '%c'", r)
		}
	}
	if name[0] == '/' || name[0] == '.' || name[0] == '-' || name[0] == '@' {
		return branchNameErrorf("must not start with '%c'", name[0])
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return branchNameErrorf("must not end with /, ., or .lock")
	}
	if strings.Contains(name, "..") {
		return branchNameErrorf("must not contain '..'")
	}
	if strings.Contains(name, "//") {
		return branchNameErrorf("must not contain '//'")
	}
	if strings.Contains(name, "/.") {
		return branchNameErrorf("must not contain '/.'")
	}
	return nil
}

// IsBranchInUse checks if a branch is currently checked out by any worktree
// (including the main working copy). Uses `git worktree list --porcelain`.
func IsBranchInUse(ctx context.Context, repoRoot, branchName string) (bool, error) {
	if err := ValidateBranchName(branchName); err != nil {
		return false, err
	}

	output, err := Output(ctx, repoRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git worktree list: %w", err)
	}

	target := "refs/heads/" + branchName
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "branch ") {
			branch := strings.TrimPrefix(line, "branch ")
			if branch == target {
				return true, nil
			}
		}
	}

	return false, nil
}

// DeleteBranch force-deletes a local git branch. dir may be any path inside
// the repository; git resolves the repo root itself.
func DeleteBranch(ctx context.Context, dir, branchName string) error {
	if err := ValidateBranchName(branchName); err != nil {
		return err
	}

	cmd := NewGitCmd(ctx, "-C", dir, "branch", "-D", branchName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git branch -D %s: %s", branchName, strings.TrimSpace(string(output)))
	}
	return nil
}

// ParseLines splits git output by newlines, trimming whitespace and
// dropping empty lines. Use for newline-terminated `for-each-ref` /
// `branch --list` / similar text output.
func ParseLines(output string) []string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var result []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			result = append(result, l)
		}
	}
	return result
}

// SplitNUL splits NUL-delimited (`-z`) git output into records, discarding
// the trailing empty element git emits after the final NUL.
func SplitNUL(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
