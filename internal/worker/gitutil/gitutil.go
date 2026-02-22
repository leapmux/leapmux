package gitutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

var errNotGitRepo = errors.New("not a git repository")

// GitStatus holds comprehensive git status information for a working directory.
type GitStatus struct {
	Branch      string
	Ahead       int
	Behind      int
	Conflicted  bool // Has merge conflicts (unmerged paths)
	Stashed     bool // Has stashed changes
	Deleted     bool // Has staged deletions
	Renamed     bool // Has staged renames
	Modified    bool // Has modifications in working directory
	TypeChanged bool // Has type changes in staging area
	Added       bool // Has staged additions
	Untracked   bool // Has untracked files
}

// GetGitStatus returns the git status for the given directory.
// Best-effort: returns nil if git is not available or the path is not a git repo.
func GetGitStatus(dir string) *GitStatus {
	status := &GitStatus{}

	// Try porcelain v2 first (git 2.13.2+).
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain=v2", "--branch")
	output, err := cmd.Output()
	if err != nil {
		// Fallback to porcelain v1 for older git versions.
		return getGitStatusV1(dir)
	}
	parseStatusV2(output, status)

	// Check for stashes (separate command, not included in status output).
	checkStash(dir, status)

	return status
}

// parseStatusV2 parses the output of `git status --porcelain=v2 --branch`.
func parseStatusV2(output []byte, status *GitStatus) {
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
func parseXY(x, y byte, status *GitStatus) {
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
func getGitStatusV1(dir string) *GitStatus {
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain", "--branch")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	status := &GitStatus{}
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

	// Check for stashes.
	checkStash(dir, status)

	return status
}

// checkStash checks if the repository has any stashed changes.
func checkStash(dir string, status *GitStatus) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", "refs/stash")
	if err := cmd.Run(); err == nil {
		status.Stashed = true
	}
}

// GitInfo holds information about a path's git context.
type GitInfo struct {
	IsGitRepo      bool
	IsWorktree     bool
	RepoRoot       string // Main repo root (resolved through worktree links)
	RepoDirName    string // filepath.Base(RepoRoot)
	IsRepoRoot     bool   // True if the queried path IS the repo root (after symlink resolution)
	IsWorktreeRoot bool   // True if the queried path IS a linked worktree root
}

// GetGitInfo checks if a path is inside a git repo and returns info.
// Returns GitInfo with IsGitRepo=false (not an error) if the path is not inside a git repo.
func GetGitInfo(path string) (*GitInfo, error) {
	gitDir, isWorktree, worktreeRoot, err := findGitRoot(path)
	if err != nil {
		if errors.Is(err, errNotGitRepo) {
			return &GitInfo{IsGitRepo: false}, nil
		}
		return nil, err
	}

	var repoRoot string
	if isWorktree {
		// For linked worktrees, the .git file points to <main-repo>/.git/worktrees/<name>.
		// We resolved up to the main repo's .git dir already, so strip /.git.
		repoRoot = filepath.Dir(gitDir)
	} else {
		// For regular repos, gitDir is <repo>/.git, so the repo root is its parent.
		repoRoot = filepath.Dir(gitDir)
	}

	// Resolve the queried path to compare with repoRoot (both symlink-resolved).
	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		resolvedPath = path
	}
	if rp, err := filepath.EvalSymlinks(resolvedPath); err == nil {
		resolvedPath = rp
	}

	// Determine if the queried path is a linked worktree root.
	isWorktreeRoot := false
	if isWorktree && worktreeRoot != "" {
		resolvedWTRoot := worktreeRoot
		if rp, err := filepath.EvalSymlinks(worktreeRoot); err == nil {
			resolvedWTRoot = rp
		}
		isWorktreeRoot = resolvedPath == resolvedWTRoot
	}

	return &GitInfo{
		IsGitRepo:      true,
		IsWorktree:     isWorktree,
		RepoRoot:       repoRoot,
		RepoDirName:    filepath.Base(repoRoot),
		IsRepoRoot:     resolvedPath == repoRoot,
		IsWorktreeRoot: isWorktreeRoot,
	}, nil
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
	cmd := exec.Command("git", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		outStr := string(output)
		// If branch already exists, try without -b (checkout existing branch).
		if strings.Contains(outStr, "already exists") {
			cmd2 := exec.Command("git", "-C", repoRoot, "worktree", "add", worktreePath, branchName)
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
	cmd := exec.Command("git", "-C", worktreePath, "status", "--porcelain")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	if len(strings.TrimSpace(string(output))) > 0 {
		return false, nil
	}

	// Check for unpushed commits.
	// First, try comparing against the upstream tracking branch.
	cmd2 := exec.Command("git", "-C", worktreePath, "log", "@{upstream}..HEAD", "--oneline")
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
	branchCmd := exec.Command("git", "-C", worktreePath, "branch", "--show-current")
	if branchOutput, branchErr := branchCmd.Output(); branchErr == nil {
		currentBranch = strings.TrimSpace(string(branchOutput))
	}
	if currentBranch != "" {
		// Show commits on HEAD that aren't reachable from any other branch.
		cmd3 := exec.Command("git", "-C", worktreePath, "log", "HEAD",
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

	cmd := exec.Command("git", "-C", repoRoot, "worktree", "remove", worktreePath, "--force")
	if output, err := cmd.CombinedOutput(); err != nil {
		// If git worktree remove fails, try to remove the directory manually.
		if rmErr := os.RemoveAll(worktreePath); rmErr != nil {
			return fmt.Errorf("git worktree remove: %s; manual removal also failed: %w", strings.TrimSpace(string(output)), rmErr)
		}
		// Directory removed manually, but we should also prune the worktree list.
		_ = exec.Command("git", "-C", repoRoot, "worktree", "prune").Run()
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

	cmd := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain")
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

	cmd := exec.Command("git", "-C", repoRoot, "branch", "-D", branchName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git branch -D %s: %s", branchName, strings.TrimSpace(string(output)))
	}
	return nil
}

// findGitRoot walks up from path toward / looking for .git.
// Returns:
//   - gitDir: the absolute path of the main repo's .git directory
//   - isWorktree: true if the found .git was a file (linked worktree)
//   - worktreeRoot: the directory containing the .git file (only set for linked worktrees)
//   - err: errNotGitRepo if no .git found, or other OS errors
func findGitRoot(path string) (string, bool, string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, "", err
	}
	// Resolve symlinks for canonical paths (e.g. /var -> /private/var on macOS).
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolved
	}

	dir := absPath
	for {
		dotGit := filepath.Join(dir, ".git")
		fi, err := os.Lstat(dotGit)
		if err == nil {
			if fi.IsDir() {
				// Regular git repo: .git is a directory.
				return dotGit, false, "", nil
			}
			// Linked worktree: .git is a file containing "gitdir: <path>".
			mainGitDir, err := resolveWorktreeGitFile(dotGit)
			if err != nil {
				return "", false, "", err
			}
			return mainGitDir, true, dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root.
			return "", false, "", errNotGitRepo
		}
		dir = parent
	}
}

// resolveWorktreeGitFile reads a .git file (from a linked worktree) and resolves
// it back to the main repo's .git directory.
//
// A worktree's .git file contains: "gitdir: /path/to/main-repo/.git/worktrees/<name>"
// We need to return: "/path/to/main-repo/.git"
func resolveWorktreeGitFile(dotGitFile string) (string, error) {
	data, err := os.ReadFile(dotGitFile)
	if err != nil {
		return "", fmt.Errorf("read .git file: %w", err)
	}

	content := strings.TrimSpace(string(data))
	if !strings.HasPrefix(content, "gitdir: ") {
		return "", fmt.Errorf("unexpected .git file content: %q", content)
	}

	gitDir := strings.TrimPrefix(content, "gitdir: ")

	// Resolve relative paths against the directory containing the .git file.
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(dotGitFile), gitDir)
	}
	gitDir = filepath.Clean(gitDir)

	// Resolve symlinks so we get canonical paths (e.g. /var -> /private/var on macOS).
	if resolved, err := filepath.EvalSymlinks(gitDir); err == nil {
		gitDir = resolved
	}

	// The gitDir now points to something like <main-repo>/.git/worktrees/<name>.
	// Walk up to find the main .git directory.
	// Expected structure: .../.git/worktrees/<name>
	// We need: .../.git
	parts := strings.Split(gitDir, string(filepath.Separator))
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == ".git" {
			mainGitDir := string(filepath.Separator) + filepath.Join(parts[1:i+1]...)
			if parts[0] != "" {
				// Windows or relative path edge case
				mainGitDir = filepath.Join(parts[0], filepath.Join(parts[1:i+1]...))
			}
			return mainGitDir, nil
		}
	}

	return "", fmt.Errorf("could not find .git directory in worktree path: %q", gitDir)
}
