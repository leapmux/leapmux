package service

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

// worktreeCleanupResult holds the result of attempting to clean up a worktree.
type worktreeCleanupResult struct {
	NeedsConfirmation bool
	WorktreePath      string
	WorktreeID        string
}

// createWorktreeIfRequested creates a git worktree on the local filesystem if
// requested. Returns the final working directory (which may be the worktree
// path) and the DB worktree ID (empty if no worktree was created/reused).
func (svc *Context) createWorktreeIfRequested(
	workingDir string,
	createWorktree bool,
	branchName string,
	baseBranch string,
) (finalWorkingDir string, worktreeID string, err error) {
	if !createWorktree {
		return workingDir, "", nil
	}

	if err := gitutil.ValidateBranchName(branchName); err != nil {
		return "", "", err
	}

	ctx := bgCtx()

	// Get git info for the working directory.
	isWorkTree, err := gitOutput(ctx, workingDir, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(isWorkTree) != "true" {
		return "", "", errNotGitRepo
	}

	topLevel, err := gitOutput(ctx, workingDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", errNotGitRepo
	}
	topLevel = strings.TrimSpace(topLevel)

	// Resolve symlinks for consistency.
	if resolved, err := filepath.EvalSymlinks(topLevel); err == nil {
		topLevel = resolved
	}

	// Determine the main repo root. For linked worktrees, --show-toplevel
	// returns the worktree root, not the main repo root. We need the main
	// repo root so we can place sibling worktrees next to the original repo.
	repoRoot := topLevel
	if gitDir, err := gitOutput(ctx, workingDir, "rev-parse", "--git-dir"); err == nil {
		gitDir = strings.TrimSpace(gitDir)
		if strings.Contains(gitDir, filepath.Join(".git", "worktrees")) {
			// This is a linked worktree. Resolve main repo through --git-common-dir.
			if commonDir, err := gitOutput(ctx, workingDir, "rev-parse", "--git-common-dir"); err == nil {
				commonDir = strings.TrimSpace(commonDir)
				if !filepath.IsAbs(commonDir) {
					commonDir = filepath.Join(topLevel, commonDir)
				}
				mainRepoRoot := filepath.Dir(filepath.Clean(commonDir))
				if resolved, err := filepath.EvalSymlinks(mainRepoRoot); err == nil {
					mainRepoRoot = resolved
				}
				repoRoot = mainRepoRoot
			}
		}
	}

	repoDirName := filepath.Base(repoRoot)

	// Determine the start point: use baseBranch if provided, otherwise current branch.
	startPoint := "HEAD"
	if baseBranch != "" {
		startPoint = baseBranch
	} else if branch, err := gitOutput(ctx, workingDir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		if b := strings.TrimSpace(branch); b != "" {
			startPoint = b
		}
	}

	// Compute worktree path.
	worktreePath := filepath.Join(filepath.Dir(repoRoot), repoDirName+"-worktrees", branchName)

	// Check if we already track this worktree.
	existing, err := svc.Queries.GetWorktreeByPath(ctx, worktreePath)
	if err == nil {
		slog.Info("reusing existing worktree",
			"worktree_id", existing.ID, "worktree_path", worktreePath)
		return worktreePath, existing.ID, nil
	}
	if err != sql.ErrNoRows {
		return "", "", err
	}

	// Create the worktree on disk.
	if err := gitCommand(ctx, repoRoot, "worktree", "add", "-b", branchName, worktreePath, startPoint); err != nil {
		return "", "", err
	}

	slog.Info("worktree created", "worktree_path", worktreePath, "branch_name", branchName)

	// Track in DB.
	wtID := id.Generate()
	if err := svc.Queries.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID:           wtID,
		WorktreePath: worktreePath,
		RepoRoot:     repoRoot,
		BranchName:   branchName,
	}); err != nil {
		return "", "", err
	}

	return worktreePath, wtID, nil
}

// registerTabForWorktree associates a tab with a worktree.
// No-op if worktreeID is empty.
func (svc *Context) registerTabForWorktree(worktreeID string, tabType leapmuxv1.TabType, tabID string) {
	if worktreeID == "" {
		return
	}
	if err := svc.Queries.AddWorktreeTab(bgCtx(), db.AddWorktreeTabParams{
		WorktreeID: worktreeID,
		TabType:    tabType,
		TabID:      tabID,
	}); err != nil {
		slog.Warn("failed to register tab for worktree",
			"worktree_id", worktreeID, "tab_id", tabID, "error", err)
	}
}

// unregisterTabAndCleanup removes a tab's worktree association and cleans up
// the worktree if it was the last tab. Returns cleanup result indicating if
// user confirmation is needed (dirty worktree).
func (svc *Context) unregisterTabAndCleanup(tabType leapmuxv1.TabType, tabID string) worktreeCleanupResult {
	ctx := bgCtx()

	// Find the worktree for this tab.
	wt, err := svc.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
		TabType: tabType,
		TabID:   tabID,
	})
	if err != nil {
		// No worktree associated with this tab.
		return worktreeCleanupResult{}
	}

	// Remove the tab association.
	if err := svc.Queries.RemoveWorktreeTab(ctx, db.RemoveWorktreeTabParams{
		WorktreeID: wt.ID,
		TabType:    tabType,
		TabID:      tabID,
	}); err != nil {
		slog.Warn("failed to remove worktree tab",
			"worktree_id", wt.ID, "tab_id", tabID, "error", err)
		return worktreeCleanupResult{}
	}

	// Check if other tabs still use this worktree.
	count, err := svc.Queries.CountWorktreeTabs(ctx, wt.ID)
	if err != nil {
		slog.Warn("failed to count worktree tabs",
			"worktree_id", wt.ID, "error", err)
		return worktreeCleanupResult{}
	}
	if count > 0 {
		// Other tabs still using the worktree.
		return worktreeCleanupResult{}
	}

	// Last tab closed — check if worktree is dirty.
	isDirty := false

	// Check for uncommitted changes.
	if status, err := gitOutput(ctx, wt.WorktreePath, "status", "--porcelain"); err == nil {
		if strings.TrimSpace(status) != "" {
			isDirty = true
		}
	}

	// Check for unpushed commits.
	if !isDirty {
		unpushed, err := gitOutput(ctx, wt.WorktreePath, "log", "@{upstream}..HEAD", "--oneline")
		if err == nil {
			// Upstream exists — check if there are commits ahead.
			if strings.TrimSpace(unpushed) != "" {
				isDirty = true
			}
		} else {
			// No upstream configured. Check if the branch has commits
			// that aren't reachable from any other local branch.
			currentBranch, branchErr := gitOutput(ctx, wt.WorktreePath, "branch", "--show-current")
			if branchErr == nil {
				currentBranch = strings.TrimSpace(currentBranch)
				if currentBranch != "" {
					unique, uniqueErr := gitOutput(ctx, wt.WorktreePath,
						"log", "HEAD", "--not", "--exclude="+currentBranch, "--branches", "--oneline")
					if uniqueErr == nil && strings.TrimSpace(unique) != "" {
						isDirty = true
					}
				}
			}
		}
	}

	if isDirty {
		// Dirty worktree — needs user confirmation.
		return worktreeCleanupResult{
			NeedsConfirmation: true,
			WorktreePath:      wt.WorktreePath,
			WorktreeID:        wt.ID,
		}
	}

	// Clean worktree — remove it automatically.
	if err := gitCommand(ctx, wt.RepoRoot, "worktree", "remove", wt.WorktreePath); err != nil {
		slog.Warn("failed to remove clean worktree",
			"worktree_path", wt.WorktreePath, "error", err)
	}

	// Delete the branch.
	if wt.BranchName != "" {
		if err := gitCommand(ctx, wt.RepoRoot, "branch", "-D", wt.BranchName); err != nil {
			slog.Debug("failed to delete branch",
				"branch", wt.BranchName, "error", err)
		}
	}

	// Delete from DB.
	if err := svc.Queries.DeleteWorktree(ctx, wt.ID); err != nil {
		slog.Warn("failed to delete worktree record",
			"worktree_id", wt.ID, "error", err)
	}

	return worktreeCleanupResult{}
}

// checkoutBranchIfRequested runs `git checkout <branch>` in the given working directory.
// No-op if branch is empty.
func (svc *Context) checkoutBranchIfRequested(workingDir, branch string) error {
	if branch == "" {
		return nil
	}

	ctx := bgCtx()
	if err := gitCommand(ctx, workingDir, "checkout", branch); err != nil {
		// Capture stderr for a more descriptive error.
		stderr, _ := gitOutputStderr(ctx, workingDir, "checkout", branch)
		if stderr != "" {
			return errors.New(strings.TrimSpace(stderr))
		}
		return fmt.Errorf("git checkout failed: %w", err)
	}
	return nil
}

// useExistingWorktreeIfRequested switches to an existing worktree directory.
// Returns the final working directory and the worktree DB ID (if managed).
func (svc *Context) useExistingWorktreeIfRequested(workingDir, worktreePath string) (string, string, error) {
	if worktreePath == "" {
		return workingDir, "", nil
	}

	sanitized, err := sanitizePath(worktreePath, svc.HomeDir)
	if err != nil {
		return "", "", fmt.Errorf("invalid worktree path: %w", err)
	}

	ctx := bgCtx()

	// Verify the path appears in the worktree list (security: prevent arbitrary path access).
	worktrees, err := listGitWorktrees(ctx, workingDir)
	if err != nil {
		return "", "", fmt.Errorf("failed to list worktrees: %w", err)
	}

	// Resolve symlinks for the sanitized path for accurate comparison.
	resolvedSanitized := sanitized
	if r, err := filepath.EvalSymlinks(sanitized); err == nil {
		resolvedSanitized = r
	}

	found := false
	for _, wt := range worktrees {
		resolvedWt := wt.Path
		if r, err := filepath.EvalSymlinks(wt.Path); err == nil {
			resolvedWt = r
		}
		if resolvedWt == resolvedSanitized {
			found = true
			break
		}
	}
	if !found {
		return "", "", fmt.Errorf("path %q is not a known worktree", sanitized)
	}

	// Check if already tracked in DB.
	var wtID string
	if existing, err := svc.Queries.GetWorktreeByPath(ctx, sanitized); err == nil {
		if count, err := svc.Queries.CountWorktreeTabs(ctx, existing.ID); err == nil && count > 0 {
			wtID = existing.ID
		}
	}

	return sanitized, wtID, nil
}

var errNotGitRepo = errors.New("path is not inside a git repository")
