package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/pathutil"
	"github.com/leapmux/leapmux/internal/util/validate"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

type rollbackWorktree struct {
	WorktreeID   string
	WorktreePath string
	RepoRoot     string
	BranchName   string
}

type rollbackBranch struct {
	WorkingDir       string
	CreatedBranch    string
	OriginalBranch   string
	OriginalCommit   string
	OriginalDetached bool
}

type gitModeRollback struct {
	CreatedWorktree *rollbackWorktree
	CreatedBranch   *rollbackBranch
}

// HasPartialMutation reports whether executeGitMode made any rollback-worthy
// change before returning (branch creation or worktree creation). Used by the
// startup goroutines to decide whether to emit a rollback-in-progress label.
func (r gitModeRollback) HasPartialMutation() bool {
	return r.CreatedBranch != nil || r.CreatedWorktree != nil
}

// gitModeKind identifies which git-mode operation a plan describes.
type gitModeKind int

const (
	gitModeUseCurrent gitModeKind = iota
	gitModeCreateWorktree
	gitModeUseWorktreePath
	gitModeCreateBranch
	gitModeCheckoutBranch
)

// gitModePlan is the validated intent of a git-mode request produced by
// validateGitMode. It is consumed later by executeGitMode on the async
// startup goroutine, which performs the (potentially slow) mutations.
type gitModePlan struct {
	Mode gitModeKind

	// WorkingDir is the input directory, with tilde expanded. It is the
	// dir the user picked; for createWorktree it is the parent repo.
	WorkingDir string

	// PlannedWorkingDir is the working dir that the tab will ultimately
	// run in. For createWorktree/useWorktreePath it differs from
	// WorkingDir; for others it equals WorkingDir.
	PlannedWorkingDir string

	// RepoRoot is the main repo's top-level directory (used to scope git
	// commands that must run against the main repo, e.g. `git worktree
	// add`). Populated for modes that need it.
	RepoRoot string

	// BranchName is the new branch to be created (createWorktree or
	// createBranch). Pre-validated with gitutil.ValidateBranchName.
	BranchName string

	// BaseBranch is the starting point for branch/worktree creation. May
	// be empty, which means "current branch or HEAD" (createWorktree) or
	// "HEAD" (createBranch).
	BaseBranch string

	// StartPoint is the resolved start ref for `git worktree add`.
	StartPoint string

	// CheckoutTarget is the branch being switched to (checkoutBranch).
	CheckoutTarget string

	// WorktreePath is the on-disk path — planned (createWorktree) or
	// validated existing (useWorktreePath).
	WorktreePath string
}

// PhaseLabel returns the user-visible "now doing X" label for this plan's
// mode. Empty string means "no dedicated phase-0 broadcast" (useCurrent —
// no mutation to announce).
func (p gitModePlan) PhaseLabel() string {
	switch p.Mode {
	case gitModeCreateWorktree:
		return fmt.Sprintf("Creating worktree %q…", p.BranchName)
	case gitModeUseWorktreePath:
		return fmt.Sprintf("Switching to worktree %q…", filepath.Base(p.WorktreePath))
	case gitModeCreateBranch:
		return fmt.Sprintf("Creating branch %q…", p.BranchName)
	case gitModeCheckoutBranch:
		return fmt.Sprintf("Switching to branch %q…", p.CheckoutTarget)
	default:
		return ""
	}
}

// RollbackLabel returns the user-visible "now rolling back X" label for
// this plan's mode. Only createWorktree and createBranch mutate state that
// we roll back on failure; the other modes return an empty label so the
// startup goroutine can skip the pre-failure broadcast.
func (p gitModePlan) RollbackLabel() string {
	switch p.Mode {
	case gitModeCreateWorktree:
		return fmt.Sprintf("Rolling back worktree %q…", p.BranchName)
	case gitModeCreateBranch:
		return fmt.Sprintf("Rolling back branch %q…", p.BranchName)
	default:
		return ""
	}
}

// rollbackLabelFromRollback mirrors gitModePlan.RollbackLabel but works
// from the gitModeResult.Rollback metadata — used when the plan has been
// consumed (subprocess-start failure) and we only have the rollback info.
func rollbackLabelFromRollback(r gitModeRollback) string {
	if r.CreatedWorktree != nil && r.CreatedWorktree.BranchName != "" {
		return fmt.Sprintf("Rolling back worktree %q…", r.CreatedWorktree.BranchName)
	}
	if r.CreatedBranch != nil && r.CreatedBranch.CreatedBranch != "" {
		return fmt.Sprintf("Rolling back branch %q…", r.CreatedBranch.CreatedBranch)
	}
	return ""
}

// validateGitMode performs read-only validation of the git-mode fields of a
// request and returns a gitModePlan describing what executeGitMode will do.
// All returned errors are CodeInvalidArgument-eligible — callers should
// surface them via sendInvalidArgument so bad user input fails fast at the
// RPC boundary without mutating any state or creating any DB row.
func (svc *Context) validateGitMode(workingDir string, r gitModeRequest) (gitModePlan, error) {
	ctx := bgCtx()

	if r.GetCreateWorktree() {
		return svc.validateCreateWorktree(ctx, workingDir, r.GetWorktreeBranch(), r.GetWorktreeBaseBranch())
	}
	if wp := r.GetUseWorktreePath(); wp != "" {
		return svc.validateUseWorktreePath(ctx, workingDir, wp)
	}
	if br := r.GetCreateBranch(); br != "" {
		return svc.validateCreateBranch(ctx, workingDir, br, r.GetCreateBranchBase())
	}
	if br := r.GetCheckoutBranch(); br != "" {
		return svc.validateCheckoutBranch(ctx, workingDir, br)
	}

	// useCurrent: no mutation, nothing to validate against beyond the dir
	// existing. If the dir happens to be inside a managed worktree we'll
	// register the association during executeGitMode.
	return gitModePlan{
		Mode:              gitModeUseCurrent,
		WorkingDir:        workingDir,
		PlannedWorkingDir: workingDir,
	}, nil
}

func (svc *Context) validateCreateWorktree(ctx context.Context, workingDir, branch, baseBranch string) (gitModePlan, error) {
	if branch == "" {
		return gitModePlan{}, errors.New("worktree_branch is required when create_worktree is true")
	}
	if err := gitutil.ValidateBranchName(branch); err != nil {
		return gitModePlan{}, fmt.Errorf("invalid worktree branch name: %w", err)
	}

	info, err := queryGitPathInfo(ctx, workingDir)
	if err != nil {
		return gitModePlan{}, fmt.Errorf("%s is not inside a git repository", workingDir)
	}
	repoRoot := info.RepoRoot

	// Fail fast if the branch is already present anywhere in this repo —
	// locally (git rev-parse refs/heads/X) or checked out in another
	// worktree (git worktree list --porcelain).
	if branchExists(ctx, workingDir, branch) {
		return gitModePlan{}, fmt.Errorf("branch %q already exists", branch)
	}
	if inUse, err := gitutil.IsBranchInUse(repoRoot, branch); err == nil && inUse {
		return gitModePlan{}, fmt.Errorf("branch %q is checked out in another worktree", branch)
	}

	// Resolve the start point up front so the user sees a base-branch
	// error before we create the new tab row.
	startPoint := "HEAD"
	if baseBranch != "" {
		if !branchExists(ctx, workingDir, baseBranch) && !isRemoteRef(ctx, workingDir, baseBranch) {
			return gitModePlan{}, fmt.Errorf("base branch %q does not exist", baseBranch)
		}
		startPoint = baseBranch
	} else if info.BranchName != "" {
		startPoint = info.BranchName
	}

	// The worktree path follows a stable formula (<repo-parent>/<repo>-worktrees/<branch>),
	// so we can plan it now and reject collisions before any mutation runs.
	worktreePath := filepath.Join(filepath.Dir(repoRoot), filepath.Base(repoRoot)+"-worktrees", branch)
	if _, err := os.Stat(worktreePath); err == nil {
		return gitModePlan{}, fmt.Errorf("worktree path %q already exists on disk", worktreePath)
	} else if !os.IsNotExist(err) {
		return gitModePlan{}, fmt.Errorf("worktree path %q: %w", worktreePath, err)
	}

	return gitModePlan{
		Mode:              gitModeCreateWorktree,
		WorkingDir:        workingDir,
		PlannedWorkingDir: worktreePath,
		RepoRoot:          repoRoot,
		BranchName:        branch,
		BaseBranch:        baseBranch,
		StartPoint:        startPoint,
		WorktreePath:      worktreePath,
	}, nil
}

func (svc *Context) validateUseWorktreePath(ctx context.Context, workingDir, worktreePath string) (gitModePlan, error) {
	sanitized, err := validate.SanitizePath(worktreePath, svc.HomeDir)
	if err != nil {
		return gitModePlan{}, fmt.Errorf("invalid worktree path: %w", err)
	}

	// Security: confirm the path is one this repo's `git worktree list`
	// already knows about — prevents jumping to arbitrary dirs.
	canonSanitized, err := filepath.EvalSymlinks(sanitized)
	if err != nil {
		return gitModePlan{}, fmt.Errorf("path %q does not exist", sanitized)
	}
	worktrees, err := listGitWorktrees(ctx, workingDir)
	if err != nil {
		return gitModePlan{}, fmt.Errorf("failed to list worktrees: %w", err)
	}
	found := false
	for _, wt := range worktrees {
		if canonWt, err := filepath.EvalSymlinks(wt.Path); err == nil && pathutil.SamePath(canonSanitized, canonWt) {
			found = true
			break
		}
	}
	if !found {
		return gitModePlan{}, fmt.Errorf("path %q is not a known worktree", sanitized)
	}

	return gitModePlan{
		Mode:              gitModeUseWorktreePath,
		WorkingDir:        workingDir,
		PlannedWorkingDir: sanitized,
		WorktreePath:      sanitized,
	}, nil
}

func (svc *Context) validateCreateBranch(ctx context.Context, workingDir, branch, base string) (gitModePlan, error) {
	if err := gitutil.ValidateBranchName(branch); err != nil {
		return gitModePlan{}, fmt.Errorf("invalid branch name: %w", err)
	}
	if _, err := queryGitPathInfo(ctx, workingDir); err != nil {
		return gitModePlan{}, fmt.Errorf("%s is not inside a git repository", workingDir)
	}
	if branchExists(ctx, workingDir, branch) {
		return gitModePlan{}, fmt.Errorf("branch %q already exists", branch)
	}
	if base != "" && !branchExists(ctx, workingDir, base) && !isRemoteRef(ctx, workingDir, base) {
		return gitModePlan{}, fmt.Errorf("base branch %q does not exist", base)
	}
	return gitModePlan{
		Mode:              gitModeCreateBranch,
		WorkingDir:        workingDir,
		PlannedWorkingDir: workingDir,
		BranchName:        branch,
		BaseBranch:        base,
	}, nil
}

func (svc *Context) validateCheckoutBranch(ctx context.Context, workingDir, branch string) (gitModePlan, error) {
	if _, err := queryGitPathInfo(ctx, workingDir); err != nil {
		return gitModePlan{}, fmt.Errorf("%s is not inside a git repository", workingDir)
	}
	if !branchExists(ctx, workingDir, branch) && !isRemoteRef(ctx, workingDir, branch) {
		return gitModePlan{}, fmt.Errorf("branch %q does not exist", branch)
	}
	return gitModePlan{
		Mode:              gitModeCheckoutBranch,
		WorkingDir:        workingDir,
		PlannedWorkingDir: workingDir,
		CheckoutTarget:    branch,
	}, nil
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
// executeGitMode completes (or partially completes before failing).
type gitModeResult struct {
	WorkingDir string
	WorktreeID string
	Rollback   gitModeRollback
}

// executeGitMode performs the mutations described by plan. Runs on the async
// startup goroutine. Returns partial results with Rollback populated even on
// error, so the caller can decide whether to emit a rollback label and then
// call rollbackGitMode. Honors ctx cancellation between shell-outs so
// CloseAgent/CloseTerminal can abort an in-progress phase 0.
func (svc *Context) executeGitMode(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	if err := ctx.Err(); err != nil {
		return gitModeResult{}, err
	}

	switch plan.Mode {
	case gitModeCreateWorktree:
		return svc.executeCreateWorktree(ctx, plan)
	case gitModeUseWorktreePath:
		return svc.executeUseWorktreePath(ctx, plan)
	case gitModeCreateBranch:
		return svc.executeCreateBranch(ctx, plan)
	case gitModeCheckoutBranch:
		return svc.executeCheckoutBranch(ctx, plan)
	default:
		return svc.executeUseCurrent(ctx, plan)
	}
}

func (svc *Context) executeCreateWorktree(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	rollback := &rollbackWorktree{
		WorktreePath: plan.WorktreePath,
		RepoRoot:     plan.RepoRoot,
		BranchName:   plan.BranchName,
	}
	result := gitModeResult{Rollback: gitModeRollback{CreatedWorktree: rollback}}

	args := []string{"-C", plan.RepoRoot, "worktree", "add", "-b", plan.BranchName, plan.WorktreePath, plan.StartPoint}
	if err := gitutil.NewGitCmd(ctx, args...).Run(); err != nil {
		// Worktree add failed before the dir was created. Drop the
		// rollback pointer so the caller doesn't emit a spurious
		// "rolling back" label for a worktree that never existed.
		return gitModeResult{}, fmt.Errorf("failed to create worktree: %w", err)
	}
	slog.Info("worktree created", "worktree_path", plan.WorktreePath, "branch_name", plan.BranchName)

	if err := ctx.Err(); err != nil {
		return result, err
	}

	wtID, err := svc.ensureTrackedWorktree(ctx, plan.WorktreePath)
	if err != nil {
		return result, fmt.Errorf("failed to track worktree: %w", err)
	}
	rollback.WorktreeID = wtID
	result.WorkingDir = plan.WorktreePath
	result.WorktreeID = wtID
	return result, nil
}

func (svc *Context) executeUseWorktreePath(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	wtID, err := svc.ensureTrackedWorktree(ctx, plan.WorktreePath)
	if err != nil {
		return gitModeResult{}, err
	}
	return gitModeResult{
		WorkingDir: plan.WorktreePath,
		WorktreeID: wtID,
	}, nil
}

func (svc *Context) executeCreateBranch(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	// Capture the current checkout now so the rollback can restore
	// HEAD if `git checkout -b` partially succeeds.
	currentTarget, err := currentCheckoutTarget(ctx, plan.WorkingDir)
	if err != nil {
		return gitModeResult{}, fmt.Errorf("failed to capture current checkout: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return gitModeResult{}, err
	}

	args := []string{"checkout", "-b", plan.BranchName}
	if plan.BaseBranch != "" {
		args = append(args, plan.BaseBranch)
	}
	stderr, err := gitOutputStderr(ctx, plan.WorkingDir, args...)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return gitModeResult{}, fmt.Errorf("failed to create branch: %s", msg)
	}

	currentTarget.CreatedBranch = plan.BranchName
	return gitModeResult{
		WorkingDir: plan.WorkingDir,
		Rollback:   gitModeRollback{CreatedBranch: currentTarget},
	}, nil
}

func (svc *Context) executeCheckoutBranch(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	target := plan.CheckoutTarget

	// Remote tracking refs ("origin/feature") become a new local branch
	// that tracks the remote; otherwise fall through to a plain checkout.
	if isRemoteRef(ctx, plan.WorkingDir, target) {
		if parts := strings.SplitN(target, "/", 2); len(parts) == 2 {
			localName := parts[1]
			if branchExists(ctx, plan.WorkingDir, localName) {
				target = localName
			} else {
				stderr, err := gitOutputStderr(ctx, plan.WorkingDir, "checkout", "-b", localName, "--track", plan.CheckoutTarget)
				if err != nil {
					msg := strings.TrimSpace(stderr)
					if msg == "" {
						msg = err.Error()
					}
					return gitModeResult{}, fmt.Errorf("failed to check out branch: %s", msg)
				}
				return gitModeResult{WorkingDir: plan.WorkingDir}, nil
			}
		}
	}

	stderr, err := gitOutputStderr(ctx, plan.WorkingDir, "checkout", target)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return gitModeResult{}, fmt.Errorf("failed to check out branch: %s", msg)
	}
	return gitModeResult{WorkingDir: plan.WorkingDir}, nil
}

func (svc *Context) executeUseCurrent(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	result := gitModeResult{WorkingDir: plan.WorkingDir}
	// If the dir happens to be inside a managed worktree, register the
	// association so CloseAgent cleanup can find it later.
	if info, err := queryGitPathInfo(ctx, plan.WorkingDir); err == nil && info.IsWorktree {
		if wtID, err := svc.ensureTrackedWorktree(ctx, info.TopLevel); err == nil {
			result.WorktreeID = wtID
		} else {
			slog.Warn("failed to track current worktree", "path", info.TopLevel, "error", err)
		}
	}
	return result, nil
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

func (svc *Context) ensureTrackedWorktree(ctx context.Context, worktreePath string) (string, error) {
	canonicalPath := pathutil.Canonicalize(worktreePath)

	existing, err := svc.Queries.GetWorktreeByPath(ctx, canonicalPath)
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	info, err := queryGitPathInfo(ctx, canonicalPath)
	if err != nil {
		return "", err
	}

	wtID := id.Generate()
	if err := svc.Queries.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID:           wtID,
		WorktreePath: canonicalPath,
		RepoRoot:     info.RepoRoot,
		BranchName:   info.BranchName,
	}); err != nil {
		return "", err
	}
	return wtID, nil
}

// removeWorktreeFromDisk force-removes a worktree and deletes its branch when
// it is no longer used by any remaining worktree.
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
		if inUse, err := gitutil.IsBranchInUse(wt.RepoRoot, wt.BranchName); err == nil && !inUse {
			if err := gitCommand(ctx, wt.RepoRoot, "branch", "-D", wt.BranchName); err != nil {
				slog.Debug("failed to delete branch",
					"branch", wt.BranchName, "error", err)
			}
		} else if err != nil {
			slog.Debug("failed to check branch usage", "branch", wt.BranchName, "error", err)
		}
	}
	if err := svc.Queries.DeleteWorktree(ctx, wt.ID); err != nil {
		slog.Warn("failed to delete worktree record",
			"worktree_id", wt.ID, "error", err)
	}
}

func currentCheckoutTarget(ctx context.Context, workingDir string) (*rollbackBranch, error) {
	if info, err := queryGitPathInfo(ctx, workingDir); err == nil && info.BranchName != "" {
		return &rollbackBranch{
			WorkingDir:     workingDir,
			OriginalBranch: info.BranchName,
		}, nil
	}

	commitSHA, err := gitOutput(ctx, workingDir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	commitSHA = strings.TrimSpace(commitSHA)
	if commitSHA == "" {
		return nil, errors.New("failed to resolve current HEAD")
	}

	return &rollbackBranch{
		WorkingDir:       workingDir,
		OriginalCommit:   commitSHA,
		OriginalDetached: true,
	}, nil
}

func (svc *Context) rollbackGitMode(result gitModeResult) {
	if result.Rollback.CreatedBranch != nil {
		svc.rollbackCreatedBranch(*result.Rollback.CreatedBranch)
	}
	if result.Rollback.CreatedWorktree != nil {
		svc.rollbackCreatedWorktree(*result.Rollback.CreatedWorktree)
	}
}

func (svc *Context) rollbackCreatedWorktree(r rollbackWorktree) {
	ctx := bgCtx()

	if err := gitCommand(ctx, r.RepoRoot, "worktree", "remove", "--force", r.WorktreePath); err != nil {
		slog.Warn("failed to roll back worktree",
			"worktree_path", r.WorktreePath, "repo_root", r.RepoRoot, "error", err)
	}

	if r.BranchName != "" {
		if err := gitCommand(ctx, r.RepoRoot, "branch", "-D", r.BranchName); err != nil {
			slog.Warn("failed to roll back worktree branch",
				"branch", r.BranchName, "repo_root", r.RepoRoot, "error", err)
		}
	}

	if r.WorktreeID != "" {
		if err := svc.Queries.DeleteWorktree(ctx, r.WorktreeID); err != nil {
			slog.Warn("failed to roll back tracked worktree",
				"worktree_id", r.WorktreeID, "error", err)
		}
	}
}

func (svc *Context) rollbackCreatedBranch(r rollbackBranch) {
	ctx := bgCtx()

	if r.OriginalDetached {
		if err := gitCommand(ctx, r.WorkingDir, "checkout", "--detach", r.OriginalCommit); err != nil {
			slog.Warn("failed to restore detached HEAD before deleting branch",
				"working_dir", r.WorkingDir, "commit", r.OriginalCommit, "error", err)
			return
		}
	} else {
		if err := gitCommand(ctx, r.WorkingDir, "checkout", r.OriginalBranch); err != nil {
			slog.Warn("failed to restore branch before deleting branch",
				"working_dir", r.WorkingDir, "branch", r.OriginalBranch, "error", err)
			return
		}
	}

	if err := gitCommand(ctx, r.WorkingDir, "branch", "-D", r.CreatedBranch); err != nil {
		slog.Warn("failed to roll back branch creation",
			"working_dir", r.WorkingDir, "branch", r.CreatedBranch, "error", err)
	}
}

// unregisterTabAndCleanup removes a tab's worktree association but does not
// delete the worktree. Deletion now requires an explicit schedule RPC.
func (svc *Context) unregisterTabAndCleanup(tabType leapmuxv1.TabType, tabID string) {
	ctx := bgCtx()

	// Find the worktree for this tab.
	wt, err := svc.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
		TabType: tabType,
		TabID:   tabID,
	})
	if err != nil {
		return
	}

	// Remove the tab association.
	if err := svc.Queries.RemoveWorktreeTab(ctx, db.RemoveWorktreeTabParams{
		WorktreeID: wt.ID,
		TabType:    tabType,
		TabID:      tabID,
	}); err != nil {
		slog.Warn("failed to remove worktree tab",
			"worktree_id", wt.ID, "tab_id", tabID, "error", err)
	}
}

// isRemoteRef checks if the given name is a remote tracking ref (e.g. "origin/feature").
func isRemoteRef(ctx context.Context, workingDir, name string) bool {
	_, err := gitOutput(ctx, workingDir, "rev-parse", "--verify", "refs/remotes/"+name)
	return err == nil
}

// branchExists checks if a local branch with the given name already exists.
func branchExists(ctx context.Context, workingDir, branch string) bool {
	_, err := gitOutput(ctx, workingDir, "rev-parse", "--verify", "refs/heads/"+branch)
	return err == nil
}

var errNotGitRepo = errors.New("path is not inside a git repository")
