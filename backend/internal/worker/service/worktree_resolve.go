package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/leapmux/leapmux/util/validate"
)

var (
	errGitWorktreeNotFound  = errors.New("no matching checked-out worktree")
	errGitWorktreeAmbiguous = errors.New("multiple checked-out worktrees match")
	errGitWorktreeLookup    = errors.New("could not resolve the Git worktree")
	errGitWorktreeReadScope = errors.New("branch selection requires git:read")
)

type worktreeAmbiguity struct {
	branch string
	paths  []string
}

func (err *worktreeAmbiguity) Error() string {
	choices := make([]string, len(err.paths))
	for i, path := range err.paths {
		choices[i] = fmt.Sprintf("%q", "path:"+path)
	}
	return fmt.Sprintf("%s for branch %q. Choose one of %s", errGitWorktreeAmbiguous, err.branch, strings.Join(choices, ", "))
}

func (err *worktreeAmbiguity) Unwrap() error { return errGitWorktreeAmbiguous }

// resolveGitWorktree finds an exact native checkout in one repository.
// A path supplies its own repository when the caller gives no repository directory.
func (svc *Service) resolveGitWorktree(ctx context.Context, repoDir string, selector *leapmuxv1.GitWorktreeSelector) (*gitPathInfo, gitutil.WorktreeEntry, error) {
	var selected gitutil.WorktreeEntry
	path, branch := selector.GetPath(), strings.TrimPrefix(selector.GetBranch(), "refs/heads/")
	switch selector.GetSelector().(type) {
	case *leapmuxv1.GitWorktreeSelector_Path:
		var err error
		path, err = validate.ResolveLiteralPath(path, svc.HomeDir)
		if err != nil {
			return nil, selected, fmt.Errorf("invalid worktree path: %w", err)
		}
		absolutePath := path
		path, err = canonicalGitDirectory(absolutePath)
		if errors.Is(err, os.ErrNotExist) {
			return nil, selected, fmt.Errorf("%w for path %q", errGitWorktreeNotFound, absolutePath)
		}
		if err != nil {
			return nil, selected, fmt.Errorf("%w: %w", errGitWorktreeLookup, err)
		}
		if repoDir == "" {
			repoDir = path
		}
	case *leapmuxv1.GitWorktreeSelector_Branch:
		if branch == "" {
			return nil, selected, errors.New("the worktree branch is required")
		}
		if repoDir == "" {
			return nil, selected, errors.New("branch selection requires a repository directory")
		}
	default:
		return nil, selected, errors.New("a worktree selector needs a path or branch")
	}
	repoDir, err := validate.ResolveLiteralPath(repoDir, svc.HomeDir)
	if err != nil {
		return nil, selected, fmt.Errorf("invalid repository directory: %w", err)
	}
	commonDir, err := gitutil.QueryCommonGitDir(ctx, repoDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isGitWorktreeAbsent(err) {
			return nil, selected, fmt.Errorf("%w in repository %q", errGitWorktreeNotFound, repoDir)
		}
		return nil, selected, fmt.Errorf("%w: read the selected repository: %w", errGitWorktreeLookup, err)
	}
	entries, err := (gitutil.Repository{GitDir: commonDir}).Worktrees(ctx)
	if err != nil {
		return nil, selected, fmt.Errorf("%w: %w", errGitWorktreeLookup, err)
	}
	var matches []string
	for _, entry := range entries {
		if entry.Bare || branch != "" && entry.Branch != branch {
			continue
		}
		canonicalPath, err := canonicalGitDirectory(entry.Path)
		if errors.Is(err, os.ErrNotExist) || err != nil && path != "" && !pathutil.SamePath(path, entry.Path) {
			continue
		}
		if err != nil {
			return nil, selected, fmt.Errorf("%w: %w", errGitWorktreeLookup, err)
		}
		if path != "" && !pathutil.SamePath(path, canonicalPath) || slices.ContainsFunc(matches, func(match string) bool {
			return pathutil.SamePath(match, canonicalPath)
		}) {
			continue
		}
		matches = append(matches, canonicalPath)
		selected = entry
		selected.Path = canonicalPath
	}
	switch len(matches) {
	case 0:
		if branch != "" {
			return nil, selected, fmt.Errorf("%w for branch %q in %q", errGitWorktreeNotFound, branch, repoDir)
		}
		return nil, selected, fmt.Errorf("%w for path %q in %q", errGitWorktreeNotFound, path, repoDir)
	case 1:
	default:
		slices.Sort(matches)
		return nil, selected, &worktreeAmbiguity{branch: branch, paths: matches}
	}
	info, err := queryGitPathInfo(ctx, selected.Path)
	if err != nil {
		return nil, selected, fmt.Errorf("%w: %w", errGitWorktreeLookup, err)
	}
	if !pathutil.SamePath(info.TopLevel, selected.Path) || !pathutil.SamePath(info.CommonDir, commonDir) ||
		branch != "" && info.BranchName != branch {
		return nil, selected, errSelectedWorktreeChanged
	}
	selected.Branch = info.BranchName
	selected.IsMain = !info.IsWorktree
	return info, selected, nil
}

func (svc *Service) resolveCallingWorktree(ctx context.Context, caller channel.Caller) (*leapmuxv1.GitWorktreeSelector, error) {
	if caller.SourceTabID == "" || caller.RelayWorkerID != "" {
		return nil, errors.New("self requires a calling tab on this worker")
	}
	var dir string
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL} {
		candidate, err := svc.getTabWorkingDir(ctx, kind, caller.SourceTabID, caller.UserID.String())
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errGitWorktreeLookup, err)
		}
		if dir != "" {
			return nil, errors.New("self matches more than one local tab")
		}
		dir = candidate
	}
	if dir == "" {
		return nil, fmt.Errorf("%w for the calling tab", errGitWorktreeNotFound)
	}
	root, err := gitutil.QueryToplevel(ctx, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isGitWorktreeAbsent(err) {
			return nil, fmt.Errorf("%w for the calling tab", errGitWorktreeNotFound)
		}
		return nil, fmt.Errorf("%w: read the calling tab's checkout: %w", errGitWorktreeLookup, err)
	}
	return &leapmuxv1.GitWorktreeSelector{Selector: &leapmuxv1.GitWorktreeSelector_Path{Path: root}}, nil
}

// openGitWorkingDir resolves the repository directory for an existing selection.
// The selected path itself can supply the repository. A branch needs a directory.
func (svc *Service) openGitWorkingDir(raw string, setup *leapmuxv1.GitSetup, caller channel.Caller) (string, error) {
	selector := setup.GetUseWorktree()
	if selector == nil {
		return normalizeWorkingDir(raw, svc.HomeDir, svc.HomeDir)
	}
	if _, byBranch := selector.GetSelector().(*leapmuxv1.GitWorktreeSelector_Branch); byBranch {
		if !caller.Allows(leapmuxv1.Scope_SCOPE_GIT_READ) {
			return "", errGitWorktreeReadScope
		}
		if raw == "" {
			return "", errors.New("branch selection requires a repository directory")
		}
	}
	if raw == "" {
		return "", nil
	}
	return validate.ResolveLiteralPath(raw, svc.HomeDir)
}
