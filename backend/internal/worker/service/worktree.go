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

// removeWorktreeFromDisk force-removes a worktree and its branch from disk,
// then deletes the DB record. The force parameter controls whether --force
// is passed to `git worktree remove`.
func (svc *Context) removeWorktreeFromDisk(wt db.Worktree, force bool) {
	ctx := bgCtx()
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, wt.WorktreePath)
	if err := gitCommand(ctx, wt.RepoRoot, args...); err != nil {
		slog.Warn("failed to remove worktree",
			"worktree_path", wt.WorktreePath, "force", force, "error", err)
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
		svc.removeWorktreeFromDisk(wt, true)
		return worktreeCleanupResult{}

	default:
		// UNSPECIFIED — no prior user choice; backend decides based on dirtiness.
	}

	clean, _ := gitutil.IsWorktreeClean(wt.WorktreePath)
	if !clean {
		// Dirty worktree — needs user confirmation.
		return worktreeCleanupResult{
			NeedsConfirmation: true,
			WorktreePath:      wt.WorktreePath,
			WorktreeID:        wt.ID,
		}
	}

	// Clean worktree — remove it automatically.
	svc.removeWorktreeFromDisk(wt, false)
	return worktreeCleanupResult{}
}

var errNotGitRepo = errors.New("path is not inside a git repository")
