package service

import (
	"context"
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

	// Fail early if a local branch with this name already exists.
	if branchExists(ctx, workingDir, branchName) {
		return "", "", fmt.Errorf("branch %q already exists", branchName)
	}

	// Resolve to the main repo root (handles linked worktrees).
	repoRoot, err := resolveMainRepoRoot(ctx, workingDir)
	if err != nil {
		return "", "", errNotGitRepo
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

// checkoutBranchIfRequested runs `git checkout <branch>` in the given working directory.
// No-op if branch is empty.
func (svc *Context) checkoutBranchIfRequested(workingDir, branch string) error {
	if branch == "" {
		return nil
	}

	ctx := bgCtx()
	stderr, err := gitOutputStderr(ctx, workingDir, "checkout", branch)
	if err != nil {
		if msg := strings.TrimSpace(stderr); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("git checkout failed: %w", err)
	}
	return nil
}

// branchExists checks if a local branch with the given name already exists.
func branchExists(ctx context.Context, workingDir, branch string) bool {
	_, err := gitOutput(ctx, workingDir, "rev-parse", "--verify", "refs/heads/"+branch)
	return err == nil
}

// createBranchIfRequested runs `git checkout -b <branch> [base]` in the given working directory.
// No-op if branch is empty. Returns an error if the branch already exists.
func (svc *Context) createBranchIfRequested(workingDir, branch, base string) error {
	if branch == "" {
		return nil
	}

	if err := gitutil.ValidateBranchName(branch); err != nil {
		return err
	}

	ctx := bgCtx()
	if branchExists(ctx, workingDir, branch) {
		return fmt.Errorf("branch %q already exists", branch)
	}

	args := []string{"checkout", "-b", branch}
	if base != "" {
		args = append(args, base)
	}
	stderr, err := gitOutputStderr(ctx, workingDir, args...)
	if err != nil {
		if msg := strings.TrimSpace(stderr); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("git checkout -b failed: %w", err)
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

// gitModeRequest is the common interface for proto messages that carry
// git-mode fields (OpenAgentRequest, OpenTerminalRequest, etc.).
type gitModeRequest interface {
	GetCreateWorktree() bool
	GetWorktreeBranch() string
	GetWorktreeBaseBranch() string
	GetCheckoutBranch() string
	GetCreateBranch() string
	GetCreateBranchBase() string
	GetUseWorktreePath() string
}

// gitModeResult holds the final working directory and worktree ID after
// applying git-mode options (create-worktree, checkout-branch, etc.).
type gitModeResult struct {
	WorkingDir string
	WorktreeID string
}

// applyGitMode applies the git-mode options from a request to the working
// directory, returning the (possibly changed) working directory and worktree ID.
func (svc *Context) applyGitMode(workingDir string, r gitModeRequest) (gitModeResult, error) {
	var worktreeID string

	// Create a worktree if requested.
	if r.GetCreateWorktree() {
		finalDir, wtID, err := svc.createWorktreeIfRequested(
			workingDir, true, r.GetWorktreeBranch(), r.GetWorktreeBaseBranch(),
		)
		if err != nil {
			return gitModeResult{}, fmt.Errorf("failed to create worktree: %w", err)
		}
		workingDir = finalDir
		worktreeID = wtID
	}

	// Checkout branch if requested.
	if branch := r.GetCheckoutBranch(); branch != "" {
		if err := svc.checkoutBranchIfRequested(workingDir, branch); err != nil {
			return gitModeResult{}, fmt.Errorf("failed to checkout branch: %w", err)
		}
	}

	// Create new branch if requested.
	if branch := r.GetCreateBranch(); branch != "" {
		if err := svc.createBranchIfRequested(workingDir, branch, r.GetCreateBranchBase()); err != nil {
			return gitModeResult{}, fmt.Errorf("failed to create branch: %w", err)
		}
	}

	// Use existing worktree if requested.
	if wtPath := r.GetUseWorktreePath(); wtPath != "" {
		finalDir, wtID, err := svc.useExistingWorktreeIfRequested(workingDir, wtPath)
		if err != nil {
			return gitModeResult{}, fmt.Errorf("failed to use worktree: %w", err)
		}
		workingDir = finalDir
		if wtID != "" {
			worktreeID = wtID
		}
	}

	// "Use current state" — if the selected dir is an already-managed worktree, register this tab.
	if !r.GetCreateWorktree() && r.GetCheckoutBranch() == "" && r.GetCreateBranch() == "" && r.GetUseWorktreePath() == "" {
		if existing, err := svc.Queries.GetWorktreeByPath(bgCtx(), workingDir); err == nil {
			if count, err := svc.Queries.CountWorktreeTabs(bgCtx(), existing.ID); err == nil && count > 0 {
				worktreeID = existing.ID
			}
		}
	}

	return gitModeResult{WorkingDir: workingDir, WorktreeID: worktreeID}, nil
}

var errNotGitRepo = errors.New("path is not inside a git repository")
