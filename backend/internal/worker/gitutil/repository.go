package gitutil

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Repository identifies common administrative storage, independent of a checkout.
type Repository struct{ GitDir string }

func (repository Repository) capture(ctx context.Context, args ...string) (stdout, stderr []byte, err error) {
	if !filepath.IsAbs(repository.GitDir) {
		return nil, nil, errors.New("the Git administrative directory must be absolute")
	}
	out, diagnostic, err := captureGitCommand(ctx, append([]string{"--git-dir=" + repository.GitDir}, args...)...)
	if err != nil {
		err = wrapWithStderr(err, diagnostic)
	}
	return out.Bytes(), diagnostic.Bytes(), err
}

func (repository Repository) Output(ctx context.Context, args ...string) (string, error) {
	output, _, err := repository.capture(ctx, args...)
	if err != nil {
		return "", err
	}
	return string(output), nil
}
func (repository Repository) Bytes(ctx context.Context, args ...string) ([]byte, error) {
	output, _, err := repository.capture(ctx, args...)
	if err != nil {
		return nil, err
	}
	return output, nil
}
func (repository Repository) Run(ctx context.Context, args ...string) error {
	_, _, err := repository.capture(ctx, args...)
	return err
}
func (repository Repository) DeleteBranch(ctx context.Context, branch string) error {
	if err := ValidateBranchName(branch); err != nil {
		return err
	}
	if !filepath.IsAbs(repository.GitDir) {
		return errors.New("the Git administrative directory must be absolute")
	}
	return deleteBranchCommand(ctx, NewGitCmd(ctx, "--git-dir="+repository.GitDir, "branch", "-D", branch), branch)
}

// WorktreeEntry retains the bare marker and unrestricted NUL-framed paths.
type WorktreeEntry struct {
	Path, Branch         string
	IsMain, Bare, Locked bool
	LockReason           string
}

func (repository Repository) Worktrees(ctx context.Context) ([]WorktreeEntry, error) {
	output, err := repository.Bytes(ctx, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return parseWorktrees(output)
}

func parseWorktrees(output []byte) ([]WorktreeEntry, error) {
	if len(output) == 0 || output[len(output)-1] != 0 {
		return nil, errors.New("git returned an unframed worktree list")
	}
	var entries []WorktreeEntry
	var current *WorktreeEntry
	for _, field := range SplitNUL(output) {
		if field == "" {
			current = nil
			continue
		}
		if path, found := strings.CutPrefix(field, "worktree "); found {
			if path == "" || current != nil {
				return nil, errors.New("git returned invalid worktree framing")
			}
			entries = append(entries, WorktreeEntry{Path: path, IsMain: len(entries) == 0})
			current = &entries[len(entries)-1]
			continue
		}
		if current == nil {
			return nil, errors.New("git returned a worktree field without a path")
		}
		switch {
		case strings.HasPrefix(field, "branch refs/heads/"):
			current.Branch = strings.TrimPrefix(field, "branch refs/heads/")
		case field == "bare":
			current.Bare = true
		case field == "locked":
			current.Locked = true
		case strings.HasPrefix(field, "locked "):
			current.Locked = true
			current.LockReason = strings.TrimPrefix(field, "locked ")
		}
	}
	if len(entries) == 0 {
		return nil, errors.New("git returned no repository worktree entry")
	}
	return entries, nil
}

func (repository Repository) IsBranchInUse(ctx context.Context, branch string) (bool, error) {
	if err := ValidateBranchName(branch); err != nil {
		return false, err
	}
	entries, err := repository.Worktrees(ctx)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Branch == branch {
			return true, nil
		}
	}
	return false, nil
}
