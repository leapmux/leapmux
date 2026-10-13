package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/util/pathutil"
	"golang.org/x/sync/errgroup"
)

type liveWorktreeTab struct {
	key        ownedTabKey
	workingDir string
	linkDir    string
	err        error
}

// liveWorktreeTabs reads one snapshot of all tab types and both payload directories.
func (svc *Service) liveWorktreeTabs(ctx context.Context) ([]liveWorktreeTab, error) {
	rows, err := svc.Queries.ListLiveTabWorkingDirs(ctx)
	if err != nil {
		return nil, err
	}
	tabs := make([]liveWorktreeTab, 0, len(rows))
	for _, row := range rows {
		tab := liveWorktreeTab{
			key:        newOwnedTabKey(row.TabType, row.TabID, row.UserID),
			workingDir: row.WorkingDir, linkDir: row.WorkingDir,
		}
		if row.TabType == leapmuxv1.TabType_TAB_TYPE_FILE || row.TabType == leapmuxv1.TabType_TAB_TYPE_IMAGE {
			payload, err := decodeTabPayload(row.Payload)
			tab.err = err
			if err == nil {
				tab.linkDir = tabPayloadLinkDir(payload)
			}
		}
		tabs = append(tabs, tab)
	}
	return tabs, nil
}

// unrelatedMissingDirectory proves that an absent directory is outside the target.
// Stop at a symlink or an unreadable component because either can hide the target.
func unrelatedMissingDirectory(dir, target string) bool {
	if !filepath.IsAbs(dir) {
		return false
	}
	for candidate := dir; ; candidate = filepath.Dir(candidate) {
		resolved, err := canonicalGitDirectory(candidate)
		if err == nil {
			suffix, relErr := filepath.Rel(candidate, dir)
			return relErr == nil && !pathutil.HasPathPrefix(filepath.Join(resolved, suffix), target)
		}
		if !os.IsNotExist(err) {
			return false
		}
		if _, statErr := os.Lstat(candidate); !os.IsNotExist(statErr) {
			return false
		}
		if filepath.Dir(candidate) == candidate {
			return false
		}
	}
}

type worktreeDirectoryMatch struct {
	matches bool
	err     error
}

// worktreeDirectoryCandidate returns a physical path that deletion would remove.
// A nested repository has its own Git identity, but deletion of its ancestor still removes its files.
func worktreeDirectoryCandidate(dir, top string) (string, error) {
	if dir == "" {
		return "", nil
	}
	canonical, err := canonicalGitDirectory(dir)
	if err != nil {
		if unrelatedMissingDirectory(dir, top) {
			return "", nil
		}
		return "", err
	}
	if !pathutil.HasPathPrefix(canonical, top) {
		return "", nil
	}
	return canonical, nil
}

// matchWorktreeDirectories probes each distinct candidate once with a fixed concurrency cap.
// Canonical containment excludes unrelated repositories before any Git process starts.
func matchWorktreeDirectories(ctx context.Context, top string, dirs []string, probe *branchInfoProbe) map[string]worktreeDirectoryMatch {
	results := make(map[string]worktreeDirectoryMatch, len(dirs))
	canonicalFor := make(map[string]string, len(dirs))
	indexes := make(map[string]int)
	var candidates []string
	for _, dir := range dirs {
		if _, seen := results[dir]; seen {
			continue
		}
		results[dir] = worktreeDirectoryMatch{}
		canonical, err := worktreeDirectoryCandidate(dir, top)
		if err != nil {
			results[dir] = worktreeDirectoryMatch{err: err}
			continue
		}
		if canonical == "" {
			continue
		}
		canonicalFor[dir] = canonical
		if _, exists := indexes[canonical]; !exists {
			indexes[canonical] = len(candidates)
			candidates = append(candidates, canonical)
		}
	}
	matched := make([]worktreeDirectoryMatch, len(candidates))
	var group errgroup.Group
	group.SetLimit(branchProbeConcurrency)
	for index, dir := range candidates {
		group.Go(func() error {
			info, err := probe.lookup(ctx, dir)
			if err != nil {
				matched[index].err = err
			} else {
				matched[index].matches = pathutil.SamePath(info.TopLevel, top)
			}
			return nil
		})
	}
	_ = group.Wait()
	for dir, canonical := range canonicalFor {
		results[dir] = matched[indexes[canonical]]
	}
	return results
}

// backfillWorktreeLinks associates each live tab with its physical worktree.
// The caller detaches this best-effort operation from startup cancellation after the tracking write.
func (svc *Service) backfillWorktreeLinks(ctx context.Context, wt db.Worktree) {
	tabs, err := svc.liveWorktreeTabs(ctx)
	if err != nil {
		slog.Warn("cannot read live tabs for worktree tracking", "worktree_id", wt.ID, "error", err)
		return
	}
	dirs := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		dirs = append(dirs, tab.linkDir)
	}
	matched := matchWorktreeDirectories(ctx, wt.WorktreePath, dirs, newBranchInfoProbe(branchProbeConcurrency))
	for _, tab := range tabs {
		match := matched[tab.linkDir]
		if err := errors.Join(tab.err, match.err); err != nil {
			slog.Warn("cannot resolve a live tab during worktree tracking", "tab_id", tab.key.tabID, "error", err)
			continue
		}
		if !match.matches {
			continue
		}
		if err := svc.Queries.AddWorktreeTab(ctx, db.AddWorktreeTabParams{
			WorktreeID: wt.ID, TabType: tab.key.tabType, TabID: tab.key.tabID, UserID: tab.key.userID,
		}); err != nil {
			slog.Warn("cannot link a live tab during worktree tracking", "tab_id", tab.key.tabID, "error", err)
		}
	}
}
