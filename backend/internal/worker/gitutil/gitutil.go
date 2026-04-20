package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/procutil"
)

// IsLinkedWorktreeGitDir reports whether a `git rev-parse --git-dir` output
// points at a linked worktree's gitdir (.git/worktrees/<name>).
func IsLinkedWorktreeGitDir(gitDir string) bool {
	return strings.Contains(filepath.ToSlash(gitDir), ".git/worktrees")
}

// NewGitCmd creates an exec.Cmd for git with terminal interaction disabled
// and no console window on Windows.
func NewGitCmd(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = nil
	procutil.HideConsoleWindow(cmd)
	return cmd
}

// GetGitStatus returns the git status for the given directory.
// Best-effort: returns nil if git is not available or the path is not a git repo.
func GetGitStatus(dir string) *leapmuxv1.AgentGitStatus {
	status := &leapmuxv1.AgentGitStatus{}

	// Try porcelain v2 first (git 2.13.2+).
	cmd := NewGitCmd(context.Background(), "-C", dir, "status", "--porcelain=v2", "--branch")
	output, err := cmd.Output()
	if err != nil {
		// Fallback to porcelain v1 for older git versions.
		return getGitStatusV1(dir)
	}
	parseStatusV2(output, status)

	// If detached, resolve to short SHA.
	if status.Branch == "" {
		status.Branch = getShortHEAD(dir)
	}

	// Check for stashes (separate command, not included in status output).
	checkStash(dir, status)

	// Get the remote origin URL.
	status.OriginUrl = GetOriginURL(dir)

	return status
}

// parseStatusV2 parses the output of `git status --porcelain=v2 --branch`.
func parseStatusV2(output []byte, status *leapmuxv1.AgentGitStatus) {
	for _, line := range strings.Split(string(output), "\n") {
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
func getGitStatusV1(dir string) *leapmuxv1.AgentGitStatus {
	cmd := NewGitCmd(context.Background(), "-C", dir, "status", "--porcelain", "--branch")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	status := &leapmuxv1.AgentGitStatus{}
	for _, line := range strings.Split(string(output), "\n") {
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

	// If detached, resolve to short SHA.
	if status.Branch == "" || status.Branch == "HEAD (no branch)" {
		status.Branch = getShortHEAD(dir)
	}

	// Check for stashes.
	checkStash(dir, status)

	// Get the remote origin URL.
	status.OriginUrl = GetOriginURL(dir)

	return status
}

// checkStash checks if the repository has any stashed changes.
func checkStash(dir string, status *leapmuxv1.AgentGitStatus) {
	cmd := NewGitCmd(context.Background(), "-C", dir, "rev-parse", "--verify", "refs/stash")
	if err := cmd.Run(); err == nil {
		status.Stashed = true
	}
}

// getShortHEAD returns the short commit SHA for HEAD, or empty string on failure.
func getShortHEAD(dir string) string {
	cmd := NewGitCmd(context.Background(), "-C", dir, "rev-parse", "--short", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// GetOriginURL returns the remote origin URL for the given directory.
// Returns an empty string if the directory is not a git repo or has no origin remote.
func GetOriginURL(dir string) string {
	cmd := NewGitCmd(context.Background(), "-C", dir, "config", "--get", "remote.origin.url")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// ValidateBranchName validates a git branch name according to git-check-ref-format rules.
func ValidateBranchName(name string) error {
	if name == "" {
		return fmt.Errorf("branch name must not be empty")
	}
	if len(name) > 256 {
		return fmt.Errorf("branch name must be at most 256 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("branch name must not contain control characters")
		}
		switch r {
		case ' ', '~', '^', ':', '?', '*', '[', ']', '\\':
			return fmt.Errorf("branch name must not contain '%c'", r)
		}
	}
	if name[0] == '/' || name[0] == '.' || name[0] == '-' || name[0] == '@' {
		return fmt.Errorf("branch name must not start with '%c'", name[0])
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("branch name must not end with /, ., or .lock")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("branch name must not contain '..'")
	}
	if strings.Contains(name, "//") {
		return fmt.Errorf("branch name must not contain '//'")
	}
	if strings.Contains(name, "/.") {
		return fmt.Errorf("branch name must not contain '/.'")
	}
	return nil
}

// CreateWorktree creates a new git worktree at the specified path.
// startPoint specifies the base commit/branch for the new worktree.
// If the branch already exists, it checks it out into the new worktree.
func CreateWorktree(repoRoot, worktreePath, branchName, startPoint string) error {
	if err := ValidateBranchName(branchName); err != nil {
		return fmt.Errorf("invalid branch name: %w", err)
	}

	// Fail fast: verify this is a git repo.
	info, err := os.Stat(filepath.Join(repoRoot, ".git"))
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%q is not a git repository", repoRoot)
	}

	// Create parent directory if needed.
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}

	// Try creating with new branch first.
	args := []string{"-C", repoRoot, "worktree", "add", worktreePath, "-b", branchName}
	args = append(args, startPoint)
	cmd := NewGitCmd(context.Background(), args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		outStr := string(output)
		// If branch already exists, try without -b (checkout existing branch).
		if strings.Contains(outStr, "already exists") {
			cmd2 := NewGitCmd(context.Background(), "-C", repoRoot, "worktree", "add", worktreePath, branchName)
			if output2, err2 := cmd2.CombinedOutput(); err2 != nil {
				return fmt.Errorf("git worktree add: %s", strings.TrimSpace(string(output2)))
			}
			return nil
		}
		return fmt.Errorf("git worktree add: %s", strings.TrimSpace(outStr))
	}

	return nil
}

// IsWorktreeClean checks if a worktree has uncommitted changes or unpushed commits.
// Returns true only if both the working tree is clean and there are no unpushed commits.
func IsWorktreeClean(worktreePath string) (bool, error) {
	// Fail fast: verify this path is inside a git repo.
	dotGit := filepath.Join(worktreePath, ".git")
	if _, err := os.Lstat(dotGit); err != nil {
		return false, fmt.Errorf("%q is not a git working tree", worktreePath)
	}

	// Check for uncommitted changes.
	cmd := NewGitCmd(context.Background(), "-C", worktreePath, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	if len(strings.TrimSpace(string(output))) > 0 {
		return false, nil
	}

	// Check for unpushed commits.
	// First, try comparing against the upstream tracking branch.
	cmd2 := NewGitCmd(context.Background(), "-C", worktreePath, "log", "@{upstream}..HEAD", "--oneline")
	output2, err := cmd2.Output()
	if err == nil {
		// Upstream exists — check if there are commits ahead of it.
		if len(strings.TrimSpace(string(output2))) > 0 {
			return false, nil
		}
		return true, nil
	}

	// No upstream configured. Fall back to checking if the current branch has
	// commits that don't exist on any other local branch. This catches the case
	// where a worktree branch was created with `git worktree add -b <name>` and
	// has local commits that would be lost if the worktree were deleted.
	currentBranch := ""
	branchCmd := NewGitCmd(context.Background(), "-C", worktreePath, "branch", "--show-current")
	if branchOutput, branchErr := branchCmd.Output(); branchErr == nil {
		currentBranch = strings.TrimSpace(string(branchOutput))
	}
	if currentBranch != "" {
		// Show commits on HEAD that aren't reachable from any other branch.
		cmd3 := NewGitCmd(context.Background(), "-C", worktreePath, "log", "HEAD",
			"--not", "--exclude="+currentBranch, "--branches", "--oneline")
		output3, err3 := cmd3.Output()
		if err3 == nil && len(strings.TrimSpace(string(output3))) > 0 {
			return false, nil
		}
	}

	return true, nil
}

// RemoveWorktree removes a git worktree.
func RemoveWorktree(repoRoot, worktreePath string) error {
	// Fail fast: verify this is a git repo.
	info, err := os.Stat(filepath.Join(repoRoot, ".git"))
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%q is not a git repository", repoRoot)
	}

	cmd := NewGitCmd(context.Background(), "-C", repoRoot, "worktree", "remove", worktreePath, "--force")
	if output, err := cmd.CombinedOutput(); err != nil {
		// If git worktree remove fails, try to remove the directory manually.
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			return fmt.Errorf("git worktree remove: %s; manual removal also failed: %w", strings.TrimSpace(string(output)), rmErr)
		}
		// Directory removed manually, but we should also prune the worktree list.
		_ = NewGitCmd(context.Background(), "-C", repoRoot, "worktree", "prune").Run()
	}

	// Clean up the parent *-worktrees directory if it's now empty.
	// os.Remove only removes empty directories, so this is a no-op if
	// other worktrees still exist under the same parent.
	_ = os.Remove(filepath.Dir(worktreePath))

	return nil
}

// IsBranchInUse checks if a branch is currently checked out by any worktree
// (including the main working copy). Uses `git worktree list --porcelain`.
func IsBranchInUse(repoRoot, branchName string) (bool, error) {
	if err := ValidateBranchName(branchName); err != nil {
		return false, fmt.Errorf("invalid branch name: %w", err)
	}

	cmd := NewGitCmd(context.Background(), "-C", repoRoot, "worktree", "list", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git worktree list: %w", err)
	}

	target := "refs/heads/" + branchName
	for _, line := range strings.Split(string(output), "\n") {
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

// DeleteBranch force-deletes a local git branch.
func DeleteBranch(repoRoot, branchName string) error {
	if err := ValidateBranchName(branchName); err != nil {
		return fmt.Errorf("invalid branch name: %w", err)
	}

	cmd := NewGitCmd(context.Background(), "-C", repoRoot, "branch", "-D", branchName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git branch -D %s: %s", branchName, strings.TrimSpace(string(output)))
	}
	return nil
}

// ReadFileAtRef reads a file from git at the given ref.
// ref must be "HEAD" or "STAGED" (empty ref means HEAD).
// Returns (content, exists, error).
func ReadFileAtRef(dir, relPath, ref string) ([]byte, bool, error) {
	var spec string
	switch strings.ToUpper(ref) {
	case "STAGED", "":
		// Staged (index): ":<path>"
		spec = ":" + relPath
	case "HEAD":
		spec = "HEAD:" + relPath
	default:
		return nil, false, fmt.Errorf("unsupported ref: %q", ref)
	}

	cmd := NewGitCmd(context.Background(), "-C", dir, "show", spec)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Exit code 128 typically means the file doesn't exist at that ref.
			return nil, false, nil
		}
		return nil, false, err
	}
	return output, true, nil
}
