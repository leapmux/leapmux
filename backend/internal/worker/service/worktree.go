package service

import (
	"database/sql"
	"errors"
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

	// Get current branch for the start point.
	startPoint := "HEAD"
	if branch, err := gitOutput(ctx, workingDir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
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

// isWorktreeDirty checks whether a worktree has uncommitted changes or
// unpushed commits. For branches without an upstream, it checks for commits
// not reachable from any other local branch (conservative heuristic).
func isWorktreeDirty(worktreePath string) bool {
	ctx := bgCtx()

	// Check for uncommitted changes.
	if status, err := gitOutput(ctx, worktreePath, "status", "--porcelain"); err == nil {
		if strings.TrimSpace(status) != "" {
			return true
		}
	}

	// Check for unpushed commits.
	unpushed, err := gitOutput(ctx, worktreePath, "log", "@{upstream}..HEAD", "--oneline")
	if err == nil {
		// Upstream exists — check if there are commits ahead.
		return strings.TrimSpace(unpushed) != ""
	}

	// No upstream configured. Check if the branch has commits
	// that aren't reachable from any other local branch.
	currentBranch, branchErr := gitOutput(ctx, worktreePath, "branch", "--show-current")
	if branchErr == nil {
		currentBranch = strings.TrimSpace(currentBranch)
		if currentBranch != "" {
			unique, uniqueErr := gitOutput(ctx, worktreePath,
				"log", "HEAD", "--not", "--exclude="+currentBranch, "--branches", "--oneline")
			if uniqueErr == nil && strings.TrimSpace(unique) != "" {
				return true
			}
		}
	}

	return false
}

// unregisterTabAndCleanup removes a tab's worktree association and cleans up
// the worktree if it was the last tab. The action parameter conveys the user's
// pre-close choice; when UNSPECIFIED the backend decides based on dirtiness.
func (svc *Context) unregisterTabAndCleanup(tabType leapmuxv1.TabType, tabID string, action leapmuxv1.WorktreeAction) worktreeCleanupResult {
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
		// Other tabs still using the worktree — never delete, regardless
		// of the user's choice. This guards against the TOCTOU race where
		// a new tab is registered between the frontend dialog and close RPC.
		return worktreeCleanupResult{}
	}

	// Last tab closed — branch on the user's pre-close action.
	switch action {
	case leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP:
		// User chose to keep the worktree+branch on disk. Delete DB record only.
		if err := svc.Queries.DeleteWorktree(ctx, wt.ID); err != nil {
			slog.Warn("failed to delete worktree record",
				"worktree_id", wt.ID, "error", err)
		}
		return worktreeCleanupResult{}

	case leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE:
		// User chose to force-remove the worktree and branch.
		if err := gitCommand(ctx, wt.RepoRoot, "worktree", "remove", "--force", wt.WorktreePath); err != nil {
			slog.Warn("failed to force-remove worktree",
				"worktree_path", wt.WorktreePath, "error", err)
		}
		if wt.BranchName != "" {
			if err := gitCommand(ctx, wt.RepoRoot, "branch", "-D", wt.BranchName); err != nil {
				slog.Debug("failed to delete branch",
					"branch", wt.BranchName, "error", err)
			}
		}
		if err := svc.Queries.DeleteWorktree(ctx, wt.ID); err != nil {
			slog.Warn("failed to delete worktree record",
				"worktree_id", wt.ID, "error", err)
		}
		return worktreeCleanupResult{}

	default:
		// UNSPECIFIED — no prior user choice; backend decides based on dirtiness.
	}

	if isWorktreeDirty(wt.WorktreePath) {
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

var errNotGitRepo = errors.New("path is not inside a git repository")
