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
	rows, err := svc.Queries.ListLiveTabWorkingDirs(ctx)
	if err != nil {
		return false, err
	}
	matches := func(dir string) (bool, error) {
		if dir == "" {
			return false, nil
		}
		canonical, err := canonicalGitDirectory(dir)
		if err != nil {
			return false, err
		}
		info, err := queryGitPathInfo(ctx, canonical)
		if errors.Is(err, errNotGitRepo) && !pathutil.HasPathPrefix(canonical, top) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		candidate, err := canonicalGitDirectory(info.TopLevel)
		return err == nil && pathutil.SamePath(candidate, top), err
	}
	for _, row := range rows {
		if newOwnedTabKey(leapmuxv1.TabType(row.TabType), row.TabID, row.UserID) == closing {
			continue
		}
		if found, err := matches(row.WorkingDir); found || err != nil {
			return found, err
		}
	}
	payloads, err := svc.Queries.ListAllWorkerTabPayloads(ctx)
	if err != nil {
		return false, err
	}
	for _, row := range payloads {
		if newOwnedTabKey(leapmuxv1.TabType(row.TabType), row.TabID, row.UserID) == closing {
			continue
		}
		payload, err := decodeTabPayload(row.Payload)
		if err != nil {
			return false, err
		}
		if found, err := matches(tabPayloadLinkDir(payload)); found || err != nil {
			return found, err
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
	info, err := queryGitPathInfo(ctx, dir)
	if err != nil || !isGitTopLevel(dir, info) || !info.IsWorktree {
		return "Only a tab at the linked worktree top level can authorize deletion"
	}
	other, err := svc.hasOtherLiveWorktreeTab(ctx, info.TopLevel, key)
	if err != nil {
		return fmt.Sprintf("Cannot check live worktree references: %v", err)
	}
	if other {
		return "Another live tab still uses this worktree"
	}
	if reason, err := svc.worktreeRemovalBlockedReason(ctx, dir, info); err != nil {
		return fmt.Sprintf("Cannot check worktree removal: %v", err)
	} else if reason != "" {
		return reason
	}
	return ""
}

func (svc *Service) startupCloseAction(action leapmuxv1.WorktreeAction, key ownedTabKey, dir string) leapmuxv1.WorktreeAction {
	if action == leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE && svc.closeRemovalRefusal(bgCtx(), key, dir) != "" {
		return leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP
	}
	return action
}
