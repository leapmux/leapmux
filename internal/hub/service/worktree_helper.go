package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/id"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

// WorktreeCleanupResult holds the result of attempting to clean up a worktree.
type WorktreeCleanupResult struct {
	NeedsConfirmation bool
	WorktreePath      string
	WorktreeID        string
}

// WorktreeHelper manages worktree lifecycle (creation, tab tracking, cleanup).
// Shared by AgentService, TerminalService, and WorkspaceService.
type WorktreeHelper struct {
	queries   *db.Queries
	workerMgr *workermgr.Manager
	pending   *workermgr.PendingRequests
}

// NewWorktreeHelper creates a new WorktreeHelper.
func NewWorktreeHelper(q *db.Queries, bm *workermgr.Manager, pr *workermgr.PendingRequests) *WorktreeHelper {
	return &WorktreeHelper{queries: q, workerMgr: bm, pending: pr}
}

// CreateWorktreeIfRequested creates a git worktree on the worker if requested.
// Returns the final working directory (which may be the worktree path) and
// the DB worktree ID (empty if no worktree was created/reused).
func (h *WorktreeHelper) CreateWorktreeIfRequested(
	ctx context.Context,
	workerID, workingDir string,
	createWorktree bool,
	branchName string,
) (finalWorkingDir string, worktreeID string, err error) {
	if !createWorktree {
		return workingDir, "", nil
	}

	if err := ValidateBranchName(branchName); err != nil {
		return "", "", fmt.Errorf("invalid branch name: %w", err)
	}

	conn := h.workerMgr.Get(workerID)
	if conn == nil {
		return "", "", fmt.Errorf("worker is offline")
	}

	// Get git info from the worker to find the repo root.
	resp, err := h.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_GitInfo{
			GitInfo: &leapmuxv1.GitInfoRequest{
				Path: workingDir,
			},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("get git info: %w", err)
	}

	gitResp := resp.GetGitInfoResp()
	if gitResp == nil {
		return "", "", fmt.Errorf("unexpected response type for git info")
	}
	if gitResp.GetError() != "" {
		return "", "", fmt.Errorf("git info: %s", gitResp.GetError())
	}
	if !gitResp.GetIsGitRepo() {
		return "", "", fmt.Errorf("path is not inside a git repository")
	}

	repoRoot := gitResp.GetRepoRoot()
	repoDirName := gitResp.GetRepoDirName()
	currentBranch := gitResp.GetCurrentBranch()

	// Use the current branch of the selected directory as the start point.
	// This ensures new worktrees branch from the correct ref, whether
	// creating from the main repo or from an existing worktree.
	startPoint := currentBranch
	if startPoint == "" {
		startPoint = "HEAD"
	}

	// Compute worktree path.
	worktreePath := filepath.Join(filepath.Dir(repoRoot), repoDirName+"-worktrees", branchName)

	// Check if we already track this worktree.
	existing, err := h.queries.GetWorktreeByWorkerAndPath(ctx, db.GetWorktreeByWorkerAndPathParams{
		WorkerID:     workerID,
		WorktreePath: worktreePath,
	})
	if err == nil {
		// Reuse existing worktree record.
		return worktreePath, existing.ID, nil
	}
	if err != sql.ErrNoRows {
		return "", "", fmt.Errorf("check existing worktree: %w", err)
	}

	// Create the worktree on the worker.
	createResp, err := h.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_GitWorktreeCreate{
			GitWorktreeCreate: &leapmuxv1.GitWorktreeCreateRequest{
				RepoRoot:     repoRoot,
				WorktreePath: worktreePath,
				BranchName:   branchName,
				StartPoint:   startPoint,
			},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("create worktree: %w", err)
	}

	wtResp := createResp.GetGitWorktreeCreateResp()
	if wtResp == nil {
		return "", "", fmt.Errorf("unexpected response type for worktree create")
	}
	if wtResp.GetError() != "" {
		return "", "", fmt.Errorf("create worktree: %s", wtResp.GetError())
	}

	// Track in DB.
	wtID := id.Generate()
	if err := h.queries.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID:           wtID,
		WorkerID:     workerID,
		WorktreePath: worktreePath,
		RepoRoot:     repoRoot,
		BranchName:   branchName,
	}); err != nil {
		return "", "", fmt.Errorf("save worktree record: %w", err)
	}

	return worktreePath, wtID, nil
}

// RegisterTabForWorktree associates a tab with a worktree.
// No-op if worktreeID is empty (tab is not using a managed worktree).
func (h *WorktreeHelper) RegisterTabForWorktree(ctx context.Context, worktreeID string, tabType leapmuxv1.TabType, tabID string) {
	if worktreeID == "" {
		return
	}

	if err := h.queries.AddWorktreeTab(ctx, db.AddWorktreeTabParams{
		WorktreeID: worktreeID,
		TabType:    tabType,
		TabID:      tabID,
	}); err != nil {
		slog.Warn("failed to register tab for worktree", "worktree_id", worktreeID, "tab_id", tabID, "error", err)
	}
}

// WorktreeStatusResult holds the result of a pre-close worktree status check.
type WorktreeStatusResult struct {
	HasWorktree  bool
	IsLastTab    bool
	IsDirty      bool
	WorktreePath string
	WorktreeID   string
	BranchName   string
}

// CheckTabWorktreeStatus checks whether closing a tab would require user confirmation
// because it's the last tab referencing a dirty worktree. This is a read-only check
// that does NOT modify any state — it's called before closing the tab so the frontend
// can show a confirmation dialog.
func (h *WorktreeHelper) CheckTabWorktreeStatus(ctx context.Context, tabType leapmuxv1.TabType, tabID string) WorktreeStatusResult {
	// Find the worktree for this tab.
	wt, err := h.queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
		TabType: tabType,
		TabID:   tabID,
	})
	if err != nil {
		// No worktree associated with this tab.
		return WorktreeStatusResult{}
	}

	result := WorktreeStatusResult{
		HasWorktree:  true,
		WorktreePath: wt.WorktreePath,
		WorktreeID:   wt.ID,
		BranchName:   wt.BranchName,
	}

	// Check if this is the last tab using this worktree.
	count, err := h.queries.CountWorktreeTabs(ctx, wt.ID)
	if err != nil {
		slog.Warn("failed to count worktree tabs for pre-check", "worktree_id", wt.ID, "error", err)
		return result
	}
	if count > 1 {
		// Other tabs still use this worktree — no confirmation needed.
		return result
	}

	result.IsLastTab = true

	// This is the last tab. Check if the worktree is dirty.
	conn := h.workerMgr.Get(wt.WorkerID)
	if conn == nil {
		// Worker offline — can't check, assume clean (cleanup will handle it).
		return result
	}

	resp, err := h.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_GitWorktreeRemove{
			GitWorktreeRemove: &leapmuxv1.GitWorktreeRemoveRequest{
				WorktreePath: wt.WorktreePath,
				CheckOnly:    true,
				Force:        false,
				BranchName:   wt.BranchName,
			},
		},
	})
	if err != nil {
		slog.Warn("failed to check worktree cleanliness", "worktree_id", wt.ID, "error", err)
		return result
	}

	removeResp := resp.GetGitWorktreeRemoveResp()
	if removeResp == nil {
		return result
	}

	if !removeResp.GetIsClean() || removeResp.GetError() != "" {
		result.IsDirty = true
	}

	return result
}

// UnregisterTabAndCleanup removes a tab's worktree association and cleans up
// the worktree if it was the last tab. Returns cleanup result indicating if
// user confirmation is needed (dirty worktree).
func (h *WorktreeHelper) UnregisterTabAndCleanup(ctx context.Context, tabType leapmuxv1.TabType, tabID string) WorktreeCleanupResult {
	// Find the worktree for this tab.
	wt, err := h.queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
		TabType: tabType,
		TabID:   tabID,
	})
	if err != nil {
		// No worktree associated with this tab — nothing to do.
		return WorktreeCleanupResult{}
	}

	// Remove the tab association.
	if err := h.queries.RemoveWorktreeTab(ctx, db.RemoveWorktreeTabParams{
		WorktreeID: wt.ID,
		TabType:    tabType,
		TabID:      tabID,
	}); err != nil {
		slog.Warn("failed to remove worktree tab", "worktree_id", wt.ID, "tab_id", tabID, "error", err)
		return WorktreeCleanupResult{}
	}

	// Check if other tabs still use this worktree.
	count, err := h.queries.CountWorktreeTabs(ctx, wt.ID)
	if err != nil {
		slog.Warn("failed to count worktree tabs", "worktree_id", wt.ID, "error", err)
		return WorktreeCleanupResult{}
	}
	if count > 0 {
		return WorktreeCleanupResult{} // Other tabs still using it.
	}

	// Last tab closed — try to remove the worktree.
	conn := h.workerMgr.Get(wt.WorkerID)
	if conn == nil {
		// Worker offline — clean up DB, leave worktree on disk.
		slog.Warn("worker offline during worktree cleanup, removing DB record only",
			"worktree_id", wt.ID, "worktree_path", wt.WorktreePath)
		if err := h.queries.DeleteWorktree(ctx, wt.ID); err != nil {
			slog.Warn("failed to delete worktree record", "worktree_id", wt.ID, "error", err)
		}
		return WorktreeCleanupResult{}
	}

	resp, err := h.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_GitWorktreeRemove{
			GitWorktreeRemove: &leapmuxv1.GitWorktreeRemoveRequest{
				WorktreePath: wt.WorktreePath,
				CheckOnly:    false,
				Force:        false,
				BranchName:   wt.BranchName,
			},
		},
	})
	if err != nil {
		slog.Warn("failed to send worktree remove to worker", "worktree_id", wt.ID, "error", err)
		// Clean up DB on communication error.
		if delErr := h.queries.DeleteWorktree(ctx, wt.ID); delErr != nil {
			slog.Warn("failed to delete worktree record", "worktree_id", wt.ID, "error", delErr)
		}
		return WorktreeCleanupResult{}
	}

	removeResp := resp.GetGitWorktreeRemoveResp()
	if removeResp == nil {
		slog.Warn("unexpected response type for worktree remove", "worktree_id", wt.ID)
		return WorktreeCleanupResult{}
	}

	if removeResp.GetError() != "" {
		// Error removing — treat as dirty, ask user.
		slog.Warn("worktree remove error", "worktree_id", wt.ID, "error", removeResp.GetError())
		return WorktreeCleanupResult{
			NeedsConfirmation: true,
			WorktreePath:      wt.WorktreePath,
			WorktreeID:        wt.ID,
		}
	}

	if !removeResp.GetIsClean() {
		// Dirty worktree — needs user confirmation.
		return WorktreeCleanupResult{
			NeedsConfirmation: true,
			WorktreePath:      wt.WorktreePath,
			WorktreeID:        wt.ID,
		}
	}

	// Clean worktree was removed successfully — delete DB record.
	if err := h.queries.DeleteWorktree(ctx, wt.ID); err != nil {
		slog.Warn("failed to delete worktree record after cleanup", "worktree_id", wt.ID, "error", err)
	}

	return WorktreeCleanupResult{}
}

// UnregisterTabBestEffort removes a tab's worktree association and attempts
// cleanup without returning confirmation results. Used in cleanup paths
// where user interaction is not possible (e.g. tile removal, workspace deletion).
func (h *WorktreeHelper) UnregisterTabBestEffort(ctx context.Context, tabType leapmuxv1.TabType, tabID string) {
	result := h.UnregisterTabAndCleanup(ctx, tabType, tabID)
	if result.NeedsConfirmation {
		// In best-effort mode, just delete the DB record and leave the worktree.
		slog.Info("worktree not clean during best-effort cleanup, leaving on disk",
			"worktree_id", result.WorktreeID, "worktree_path", result.WorktreePath)
		if err := h.queries.DeleteWorktree(ctx, result.WorktreeID); err != nil {
			slog.Warn("failed to delete worktree record during best-effort cleanup",
				"worktree_id", result.WorktreeID, "error", err)
		}
	}
}
