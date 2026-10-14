package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/util/pathutil"
)

// canonicalGitDirectory refuses absent or unreadable directories.
// A failed symlink resolution must never authorize deletion.
func canonicalGitDirectory(dir string) (string, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return "", errors.New("working directory must be an absolute path")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	return pathutil.Canonicalize(resolved), nil
}

// isGitTopLevel compares the tab directory with its own Git top level.
// The main repository root cannot identify a linked worktree's top level.
func isGitTopLevel(workingDir string, info *gitPathInfo) bool {
	if info == nil {
		return false
	}
	dir, err := canonicalGitDirectory(workingDir)
	if err != nil {
		return false
	}
	top, err := canonicalGitDirectory(info.TopLevel)
	return err == nil && pathutil.SamePath(dir, top)
}

// hasOtherLiveWorktreeTab includes tabs that opened before tracking began.
// Payload files protect their physical directory and their inherited directory.
func (svc *Service) hasOtherLiveWorktreeTab(ctx context.Context, target string, closing ownedTabKey) (bool, error) {
	top, err := canonicalGitDirectory(target)
	if err != nil {
		return false, err
	}
	tabs, err := svc.liveWorktreeTabs(ctx)
	if err != nil {
		return false, err
	}
	var dirs []string
	for _, tab := range tabs {
		if tab.key == closing {
			continue
		}
		if tab.err != nil {
			return false, tab.err
		}
		dirs = append(dirs, tab.workingDir, tab.linkDir)
	}
	seen := make(map[string]struct{}, len(dirs))
	for _, dir := range dirs {
		if _, duplicate := seen[dir]; duplicate {
			continue
		}
		seen[dir] = struct{}{}
		canonical, err := worktreeDirectoryCandidate(dir, top)
		if err != nil || canonical != "" {
			return canonical != "", err
		}
	}
	return false, nil
}

// closeRemovalRefusal checks the live tab before startup cancellation or teardown.
func (svc *Service) closeRemovalRefusal(ctx context.Context, key ownedTabKey, dir string) string {
	rows, err := svc.Queries.ListLiveTabWorkingDirs(ctx)
	if err != nil {
		return fmt.Sprintf("Cannot read live tabs: %v", err)
	}
	live := false
	for _, row := range rows {
		if newOwnedTabKey(leapmuxv1.TabType(row.TabType), row.TabID, row.UserID) == key {
			live = true
			break
		}
	}
	if !live {
		return "The tab is already closed or has no working directory"
	}
	verdict := svc.checkWorktreeRemovalEligibility(ctx, dir, "", key)
	if verdict.refusal != "" {
		return verdict.refusal
	}
	if verdict.err != nil {
		return fmt.Sprintf("Cannot check live worktree references: %v", verdict.err)
	}
	if verdict.otherLiveTabs {
		return "Another live tab still uses this worktree"
	}
	return ""
}

func (svc *Service) startupCloseAction(action leapmuxv1.WorktreeAction, key ownedTabKey, dir string, startup *startupCore) leapmuxv1.WorktreeAction {
	if !startup.hasUnfinishedClaim(key.tabID) {
		return action
	}
	if action == leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE && svc.closeRemovalRefusal(bgCtx(), key, dir) != "" {
		return leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP
	}
	return action
}

// worktreeRemovalVerdict separates authorization from each caller's error reporting.
type worktreeRemovalVerdict struct {
	info          *gitPathInfo
	refusal       string
	otherLiveTabs bool
	err           error
}

// checkWorktreeRemovalEligibility checks the current directory and live references.
// Callers retain their live-row checks and apply Git policy immediately before removal.
func (svc *Service) checkWorktreeRemovalEligibility(ctx context.Context, dir, expectedPath string, closing ownedTabKey) worktreeRemovalVerdict {
	info, err := queryGitPathInfo(ctx, dir)
	if err != nil || !isGitTopLevel(dir, info) || !info.IsWorktree {
		return worktreeRemovalVerdict{refusal: "Only a tab at the linked worktree top level can authorize deletion"}
	}
	if expectedPath != "" && !pathutil.SamePath(info.TopLevel, pathutil.Canonicalize(expectedPath)) {
		return worktreeRemovalVerdict{refusal: "The tab directory no longer identifies this linked worktree"}
	}
	other, err := svc.hasOtherLiveWorktreeTab(ctx, info.TopLevel, closing)
	return worktreeRemovalVerdict{info: info, otherLiveTabs: other, err: err}
}
