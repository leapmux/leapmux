package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/pathutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/validate"
	"golang.org/x/sync/errgroup"
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

// rollbackLabelFromRollback returns the user-visible "now rolling back X"
// label for a partial git-mode mutation, derived from the rollback metadata
// on a gitModeResult. Only createWorktree and createBranch mutate state
// that we roll back on failure; the other modes return an empty label so
// the startup goroutine can skip the pre-failure broadcast.
func rollbackLabelFromRollback(r gitModeRollback) string {
	if r.CreatedWorktree != nil && r.CreatedWorktree.BranchName != "" {
		return fmt.Sprintf("Rolling back worktree %q…", r.CreatedWorktree.BranchName)
	}
	if r.CreatedBranch != nil && r.CreatedBranch.CreatedBranch != "" {
		return fmt.Sprintf("Rolling back branch %q…", r.CreatedBranch.CreatedBranch)
	}
	return ""
}

// refExistence captures whether a ref resolves locally and/or as a remote.
// Both default to false when the underlying LookupRef errored — the gate
// logic in callers treats that as "doesn't exist", matching git's own
// "ref not found" behaviour.
type refExistence struct {
	Local, Remote bool
}

// probeRef fans out a single LookupRef inside the supplied errgroup. The
// result is written into out on success; errors are swallowed so a flaky
// probe doesn't fail the whole validation pass (callers treat absent refs
// as "doesn't exist" anyway). Always returns nil so the group never
// short-circuits on a failed probe.
func probeRef(ctx context.Context, g *errgroup.Group, dir, ref string, out *refExistence) {
	g.Go(func() error {
		local, remote, err := gitutil.LookupRef(ctx, dir, ref)
		if err == nil {
			out.Local, out.Remote = local, remote
		}
		return nil
	})
}

// probeIsRepo fans out a queryGitPathInfo probe inside the supplied
// errgroup so a "is this dir a repo?" check can race against the
// independent LookupRef probes. The result is written into out; non-
// errNotGitRepo failures (ctx.Canceled, permission, dubious-ownership,
// transient I/O) are logged at Warn so operators can diagnose why a
// user sees "not inside a git repository" for what is in fact a real
// repo the worker simply can't read. Always returns nil so the
// errgroup doesn't short-circuit on a probe failure — callers gate the
// out-var with their own ctx.Err() check after g.Wait().
func probeIsRepo(ctx context.Context, g *errgroup.Group, dir string, out *bool) {
	g.Go(func() error {
		_, err := queryGitPathInfo(ctx, dir)
		*out = err == nil
		if err != nil && !errors.Is(err, errNotGitRepo) && ctx.Err() == nil {
			slog.Warn("probeIsRepo: rev-parse failed against a dir we will report as 'not a git repo'",
				"dir", dir, "error", err)
		}
		return nil
	})
}

// validateGitMode performs read-only validation of the git-mode fields of a
// request and returns a gitModePlan describing what executeGitMode will do.
// All returned errors are CodeInvalidArgument-eligible — callers should
// surface them via sendInvalidArgument so bad user input fails fast at the
// RPC boundary without mutating any state or creating any DB row.
func (svc *Context) validateGitMode(ctx context.Context, workingDir string, r gitModeRequest) (gitModePlan, error) {
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
		return gitModePlan{}, err
	}

	info, err := queryGitPathInfo(ctx, workingDir)
	if err != nil {
		return gitModePlan{}, fmt.Errorf("%s is not inside a git repository", workingDir)
	}
	repoRoot := info.RepoRoot

	// Fan out the existence probes. The branch + base LookupRef calls each
	// roll local + remote ref checks into a single `git show-ref`; the
	// IsBranchInUse probe runs `git worktree list --porcelain` against the
	// repo root. All three are independent. On a slow filesystem each fork
	// costs 30–80ms, so running them serially makes the dialog spinner
	// stick. The gate logic below uses the captured booleans to preserve
	// the original error-message ordering.
	var (
		branchRef   refExistence
		baseRef     refExistence
		branchInUse bool
	)
	g, gctx := errgroup.WithContext(ctx)
	probeRef(gctx, g, workingDir, branch, &branchRef)
	g.Go(func() error {
		// Propagate the IsBranchInUse error rather than treating it as
		// "branch is free": when gctx cancels (sibling probe failed,
		// outer ctx cancelled) or git itself fails, returning ok=false
		// here would let executeCreateWorktree run `git worktree add
		// -b X` against a branch that IS in use, and the user would see
		// git's bare error instead of the validator's curated
		// "branch X is checked out in another worktree".
		inUse, err := gitutil.IsBranchInUse(gctx, repoRoot, branch)
		if err != nil {
			return err
		}
		branchInUse = inUse
		return nil
	})
	if baseBranch != "" {
		probeRef(gctx, g, workingDir, baseBranch, &baseRef)
	}
	if err := g.Wait(); err != nil {
		return gitModePlan{}, err
	}

	// Surface ctx cancellation before interpreting the probe out-vars
	// (mirrors validateCreateBranch / validateCheckoutBranch). probeRef
	// swallows LookupRef errors and leaves the refExistence at zero on
	// ctx.Canceled; without this gate, a cancellation-zeroed branchRef
	// would silently bypass the "branch already exists" check and let
	// executeCreateWorktree run `git worktree add -b <existing>` only
	// to surface git's raw "fatal: branch already exists" message
	// instead of the curated validator error.
	if err := ctx.Err(); err != nil {
		return gitModePlan{}, err
	}

	if branchRef.Local {
		return gitModePlan{}, fmt.Errorf("branch %q already exists", branch)
	}
	if branchInUse {
		return gitModePlan{}, fmt.Errorf("branch %q is checked out in another worktree", branch)
	}

	// Resolve the start point up front so the user sees a base-branch
	// error before we create the new tab row.
	startPoint := "HEAD"
	if baseBranch != "" {
		if !baseRef.Local && !baseRef.Remote {
			return gitModePlan{}, fmt.Errorf("base branch %q does not exist", baseBranch)
		}
		startPoint = baseBranch
	} else if info.BranchName != "" {
		startPoint = info.BranchName
	}

	// The worktree path follows a stable formula (<repo-parent>/<repo>-worktrees/<branch>),
	// so we can plan it now and reject collisions before any mutation runs.
	// This os.Stat is not about TOCTOU safety — `git worktree add` itself
	// refuses to overwrite an existing path. It exists so a collision
	// surfaces during the synchronous validation phase with a clean,
	// worktree-specific error *before* OpenAgent returns and the tab row
	// is created. Without it, the collision would instead surface
	// asynchronously in phase 0, wrapped in git's message, after the
	// frontend has already rendered a partially-initialized tab.
	worktreePath := filepath.Join(filepath.Dir(repoRoot), filepath.Base(repoRoot)+"-worktrees", branch)
	if _, err := os.Stat(worktreePath); err == nil {
		return gitModePlan{}, fmt.Errorf(`worktree path "%s" already exists on disk`, worktreePath)
	} else if !os.IsNotExist(err) {
		return gitModePlan{}, fmt.Errorf(`worktree path "%s": %w`, worktreePath, err)
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
		return gitModePlan{}, fmt.Errorf(`path "%s" does not exist`, sanitized)
	}
	worktrees, err := listGitWorktrees(ctx, workingDir)
	if err != nil {
		return gitModePlan{}, fmt.Errorf("failed to list worktrees: %w", err)
	}
	found := false
	for _, wt := range worktrees {
		// Skip the main-repo entry: `git worktree list` includes the
		// repo root as IsMain=true, but the dialog intent is "open this
		// LINKED worktree as the working dir." Allowing the main repo
		// through would let ensureTrackedWorktree write a Worktree DB
		// row whose worktree_path equals repo_root; a later
		// CloseAgent(REMOVE) would then attempt `git worktree remove`
		// on the main repo (which git refuses) and `git branch -D`
		// against the main-repo HEAD branch (which can succeed and
		// destroy the user's primary branch). attachWorktreeIfPresent
		// applies an IsWorktree gate for the same reason; this is the
		// matching gate for the user-driven worktree-pick path.
		if wt.GetIsMain() {
			continue
		}
		// Fast path: when the git-reported worktree path is already
		// canonical (the common case — git records the path as the user
		// created it, and `pathutil.SamePath` handles Windows
		// case-insensitivity), a direct compare avoids the syscall.
		if pathutil.SamePath(canonSanitized, wt.GetPath()) {
			found = true
			break
		}
		// Deliberately NOT pathutil.Canonicalize here: that helper falls
		// back to the raw input when EvalSymlinks fails, so a worktree
		// path git lists but can't resolve (missing/broken) would still
		// be compared by its unresolved form — weakening the gate
		// against jumping to arbitrary dirs. Skip-on-resolve-failure is
		// the conservative choice for a security check.
		if canonWt, err := filepath.EvalSymlinks(wt.GetPath()); err == nil && pathutil.SamePath(canonSanitized, canonWt) {
			found = true
			break
		}
	}
	if !found {
		return gitModePlan{}, fmt.Errorf(`path "%s" is not a known worktree`, sanitized)
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
		return gitModePlan{}, err
	}

	// Fan out the existence probes — the queryGitPathInfo probe is
	// independent of the LookupRef calls, and each LookupRef rolls local
	// + remote ref checks into a single `git show-ref` fork. Running
	// them in parallel collapses 2–3 sequential forks to one wall-time.
	var (
		isRepo    bool
		branchRef refExistence
		baseRef   refExistence
	)
	g, gctx := errgroup.WithContext(ctx)
	probeIsRepo(gctx, g, workingDir, &isRepo)
	probeRef(gctx, g, workingDir, branch, &branchRef)
	if base != "" {
		probeRef(gctx, g, workingDir, base, &baseRef)
	}
	_ = g.Wait()

	// Surface ctx cancellation before interpreting the probe out-vars:
	// probeIsRepo/probeRef swallow their own errors and leave the
	// out-vars at zero values on ctx.Canceled, which would otherwise
	// make the caller report a misleading "not inside a git repository"
	// for what was actually a dialog dismissal / channel teardown.
	if err := ctx.Err(); err != nil {
		return gitModePlan{}, err
	}
	if !isRepo {
		return gitModePlan{}, fmt.Errorf("%s is not inside a git repository", workingDir)
	}
	if branchRef.Local {
		return gitModePlan{}, fmt.Errorf("branch %q already exists", branch)
	}
	if base != "" && !baseRef.Local && !baseRef.Remote {
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
	// queryGitPathInfo and LookupRef are independent existence probes;
	// run them concurrently so a dialog click pays one fork latency
	// instead of two.
	var (
		isRepo    bool
		branchRef refExistence
	)
	g, gctx := errgroup.WithContext(ctx)
	probeIsRepo(gctx, g, workingDir, &isRepo)
	probeRef(gctx, g, workingDir, branch, &branchRef)
	_ = g.Wait()

	// See validateCreateBranch: ctx.Err() takes precedence so a
	// cancellation doesn't surface as "not inside a git repository".
	if err := ctx.Err(); err != nil {
		return gitModePlan{}, err
	}
	if !isRepo {
		return gitModePlan{}, fmt.Errorf("%s is not inside a git repository", workingDir)
	}
	if !branchRef.Local && !branchRef.Remote {
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

	if err := gitutil.Run(ctx, plan.RepoRoot, "worktree", "add", "-b", plan.BranchName, plan.WorktreePath, plan.StartPoint); err != nil {
		// Worktree add failed before the dir was created. Drop the
		// rollback pointer so the caller doesn't emit a spurious
		// "rolling back" label for a worktree that never existed.
		return gitModeResult{}, fmt.Errorf("failed to create worktree: %w", err)
	}
	slog.Info("worktree created", "worktree_path", plan.WorktreePath, "branch_name", plan.BranchName)

	if err := ctx.Err(); err != nil {
		return result, err
	}

	// `git worktree add` was just invoked with these exact values, so
	// they're authoritative for the new worktree — skip the queryGit-
	// PathInfo fork that would re-derive them.
	wtID, err := svc.ensureTrackedWorktreeWith(ctx, plan.WorktreePath, plan.RepoRoot, plan.BranchName)
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

	currentTarget.CreatedBranch = plan.BranchName
	if err := createBranchInDir(ctx, plan.WorkingDir, plan.BranchName, plan.BaseBranch); err != nil {
		// BranchNameError aborts inside createBranchInDir before any
		// git subprocess is invoked, so no rollback is needed and
		// surfacing one would emit a spurious "rolling back branch X"
		// label plus a misleading Warn log from `git branch -D` on a
		// branch that never existed. For all other failures `git
		// checkout -b` may have written refs/heads/<new> before the
		// checkout step aborted (pre-checkout hook, index race), so
		// the Rollback metadata must travel back so the caller's
		// HasPartialMutation can run the cleanup.
		if gitutil.IsBranchNameError(err) {
			return gitModeResult{}, err
		}
		return gitModeResult{
			Rollback: gitModeRollback{CreatedBranch: currentTarget},
		}, err
	}

	// Mirror executeCheckoutBranch / executeUseCurrent: when the working
	// dir lives inside a linked worktree, the new branch was created in
	// that worktree, so the tab must be linked to its worktree row.
	// Without the linkage a later CloseAgent(REMOVE) silently degrades
	// to KEEP. Carry the rollback so a probe failure still triggers the
	// just-created branch's cleanup.
	result := gitModeResult{
		WorkingDir: plan.WorkingDir,
		Rollback:   gitModeRollback{CreatedBranch: currentTarget},
	}
	if err := svc.attachWorktreeIfPresent(ctx, &result, plan.WorkingDir); err != nil {
		return result, err
	}
	return result, nil
}

func (svc *Context) executeCheckoutBranch(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	// Probe + register the worktree association BEFORE the checkout so
	// a ctx-cancel / DB / git probe failure aborts cleanly, instead of
	// stranding the working dir on the new branch with no tab linkage.
	// Once `git checkout` has moved HEAD, we have no clean rollback path
	// (the user may be staring at the new branch); a pre-checkout abort
	// keeps the failure side-effect-free.
	//
	// Mirror executeCreateBranch's return shape: pass the populated
	// `result` back even on error so the caller can observe any
	// WorktreeID the attach already wrote. The two paths used to return
	// `gitModeResult{}` (checkout) and `result` (createBranch) for the
	// same failure mode, which made it impossible for shared post-
	// failure logic to read worktree linkage off a checkout failure.
	result := gitModeResult{WorkingDir: plan.WorkingDir}
	if err := svc.attachWorktreeIfPresent(ctx, &result, plan.WorkingDir); err != nil {
		return result, err
	}
	if err := checkoutBranchInDir(ctx, plan.WorkingDir, plan.CheckoutTarget); err != nil {
		return result, err
	}
	// Keep the worktree row's branch_name in sync with the post-checkout
	// HEAD. attachWorktreeIfPresent above creates the row on first
	// touch with branch_name pinned to PRE-checkout HEAD; without this
	// re-stamp the row drifts the moment HEAD moves, and the fallback
	// path in removeWorktreeFromDisk (when its live re-probe fails)
	// would delete the stale branch_name instead of the live one. Best
	// effort: a row write failure is logged but doesn't abort — the
	// checkout already succeeded and the live re-probe at delete time
	// is the real source of truth.
	svc.restampWorktreeBranch(ctx, result.WorktreeID, plan.WorkingDir)
	return result, nil
}

// restampWorktreeBranch refreshes the worktree row's branch_name from
// a live HEAD probe. No-op when the working dir isn't tracked as a
// worktree (worktreeID empty). Best-effort: probe + row failures log
// and continue so a transient hiccup doesn't block the user's
// checkout. Exposed at package scope so tests can pin the re-stamp
// invariant without going through the full executeCheckoutBranch
// pipeline.
func (svc *Context) restampWorktreeBranch(ctx context.Context, worktreeID, workingDir string) {
	if worktreeID == "" {
		return
	}
	info, err := queryGitPathInfo(ctx, workingDir)
	if err != nil {
		slog.Warn("worktree branch re-stamp skipped: path probe failed",
			"worktree_id", worktreeID, "working_dir", workingDir, "error", err)
		return
	}
	// info.BranchName is empty on detached HEAD AND on unborn HEAD;
	// both leave the column at whatever value the previous stamp set.
	// A future detached-HEAD-aware schema could distinguish the two,
	// but as long as removeWorktreeFromDisk re-probes live HEAD before
	// trusting wt.BranchName, an out-of-sync row is recoverable.
	if info.BranchName == "" {
		return
	}
	if err := svc.Queries.UpdateWorktreeBranchName(ctx, db.UpdateWorktreeBranchNameParams{
		BranchName: info.BranchName,
		ID:         worktreeID,
	}); err != nil {
		slog.Warn("worktree branch re-stamp failed",
			"worktree_id", worktreeID, "branch", info.BranchName, "error", err)
	}
}

func (svc *Context) executeUseCurrent(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	// Same return-on-error shape as executeCheckoutBranch /
	// executeCreateBranch: pass `result` back so any WorktreeID the
	// attach already wrote stays observable.
	result := gitModeResult{WorkingDir: plan.WorkingDir}
	if err := svc.attachWorktreeIfPresent(ctx, &result, plan.WorkingDir); err != nil {
		return result, err
	}
	return result, nil
}

// attachWorktreeIfPresent registers the tab→worktree association when
// `dir` is inside a linked worktree. ensureTrackedWorktree failures
// bubble out as errors instead of silent Warn logs so the caller can
// reject OpenAgent rather than ship a tab with no worktree linkage —
// without the association a later REMOVE-on-close degrades to KEEP.
//
// `dir` not being a git repo at all is fine (no association to make).
// That case is identified by errNotGitRepo, which queryGitPathInfo
// returns when git says the path isn't in a work tree; ctx cancellation
// and other unexpected errors are surfaced so the caller can reject the
// OpenAgent instead of silently shipping an unlinked tab.
func (svc *Context) attachWorktreeIfPresent(ctx context.Context, result *gitModeResult, dir string) error {
	info, err := queryGitPathInfo(ctx, dir)
	if err != nil {
		if errors.Is(err, errNotGitRepo) {
			return nil
		}
		return fmt.Errorf("failed to probe worktree at %q: %w", dir, err)
	}
	if !info.IsWorktree {
		return nil
	}
	wtID, err := svc.ensureTrackedWorktree(ctx, info.TopLevel)
	if err != nil {
		return fmt.Errorf("failed to track worktree %q: %w", info.TopLevel, err)
	}
	result.WorktreeID = wtID
	return nil
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
	return svc.ensureTrackedWorktreeWith(ctx, worktreePath, "", "")
}

// ensureTrackedWorktreeWith is the same as ensureTrackedWorktree but
// lets the caller supply known repoRoot and branchName values to skip
// the `git rev-parse` probe on the cold path. Callers that have just
// created the worktree (e.g. `executeCreateWorktree`) already know
// these fields; paying the fork latency twice for the same data is
// pure dialog-open overhead. Pass "" for either field to fall back to
// queryGitPathInfo.
func (svc *Context) ensureTrackedWorktreeWith(ctx context.Context, worktreePath, repoRoot, branchName string) (string, error) {
	canonicalPath := pathutil.Canonicalize(worktreePath)

	existing, err := svc.Queries.GetWorktreeByPath(ctx, canonicalPath)
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	if repoRoot == "" || branchName == "" {
		info, err := queryGitPathInfo(ctx, canonicalPath)
		if err != nil {
			return "", err
		}
		if repoRoot == "" {
			repoRoot = info.RepoRoot
		}
		if branchName == "" {
			branchName = info.BranchName
		}
	}

	wtID := id.Generate()
	if err := svc.Queries.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID:           wtID,
		WorktreePath: canonicalPath,
		RepoRoot:     repoRoot,
		BranchName:   branchName,
	}); err != nil {
		return "", err
	}
	return wtID, nil
}

// removeWorktreeFromDisk force-removes a worktree from disk, deletes its
// branch if no other worktree still uses it, and soft-deletes the DB row.
//
// The DB row is always soft-deleted so a stale entry does not block future
// reuse of the working directory: the next attach against the same path
// either finds nothing (clean removal) or creates a fresh row consistent
// with whatever the on-disk worktree actually looks like. Returns a
// non-nil error when the git worktree-remove command failed AND the path
// is still on disk — callers surface this so the user can clean up
// manually. Branch deletion failures are logged but never bubbled
// (a retained branch is recoverable manually; mass-deleting unrelated
// branches is not).
func (svc *Context) removeWorktreeFromDisk(wt db.Worktree, force bool) error {
	ctx := bgCtx()

	// Resolve the branch to delete BEFORE the worktree-remove call: the
	// DB row's branch_name was stamped at attach time, but an external
	// `git checkout` (terminal, IDE, sibling agent) or the post-attach
	// checkout in executeCheckoutBranch can move HEAD without updating
	// the row. Trusting wt.BranchName would attempt `git branch -D
	// <stale-name>` against the wrong target — at best a no-op log, at
	// worst the deletion of an unrelated branch (e.g. `main`). A live
	// HEAD probe pinned against the worktree path produces the truth.
	//
	// Probe failure paths MUST fall through to "skip the branch delete"
	// (branchToDelete=""), not "use the stale DB row". Falling back to
	// wt.BranchName is the exact hazard the comment above warns
	// against: a transient probe failure (DeadlineExceeded, permission,
	// transient I/O) would route us straight into the
	// delete-unrelated-branch case the live probe was added to defend
	// against. errNotGitRepo is a special case: the worktree's .git
	// admin dir is corrupt, so we have no live ground truth either way
	// — leaving branchToDelete empty avoids a destructive guess.
	branchToDelete := ""
	if info, infoErr := queryGitPathInfo(ctx, wt.WorktreePath); infoErr == nil {
		if info.BranchName != "" {
			branchToDelete = info.BranchName
		}
		// else: detached HEAD — nothing branch-named to delete.
	} else {
		slog.Warn("removeWorktreeFromDisk: live HEAD probe failed; skipping branch delete to avoid touching a stale name",
			"worktree_id", wt.ID,
			"worktree_path", wt.WorktreePath,
			"stale_db_branch", wt.BranchName,
			"error", infoErr)
	}

	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, wt.WorktreePath)
	// `git worktree remove` must complete before the branch cleanup: git
	// refuses to delete a branch still checked out in a worktree. Once the
	// worktree is gone, the branch cleanup and the DB DeleteWorktree are
	// independent and run in parallel.
	removeErr := gitutil.Run(ctx, wt.RepoRoot, args...)
	if removeErr != nil {
		slog.Warn("failed to remove worktree",
			"worktree_path", wt.WorktreePath, "force", force, "error", removeErr)
	}

	var wg errgroup.Group
	if branchToDelete != "" {
		wg.Go(func() error {
			// Always probe IsBranchInUse before delete. `git worktree add
			// --force` lets the same branch live in multiple worktrees, so
			// removing one of them does NOT guarantee the branch is free —
			// a `git branch -D` that runs unconditionally on the success
			// path would fail with "branch is checked out at <other>" and
			// the failure would only surface as a Debug log.
			inUse, err := gitutil.IsBranchInUse(ctx, wt.RepoRoot, branchToDelete)
			if err != nil {
				slog.Warn("failed to check branch usage; skipping branch delete",
					"branch", branchToDelete, "error", err)
				return nil
			}
			if inUse {
				slog.Info("branch still checked out in another worktree; skipping delete",
					"branch", branchToDelete)
				return nil
			}
			if err := gitutil.DeleteBranch(ctx, wt.RepoRoot, branchToDelete); err != nil {
				slog.Warn("failed to delete branch",
					"branch", branchToDelete, "error", err)
			}
			return nil
		})
	}
	wg.Go(func() error {
		if err := svc.Queries.DeleteWorktree(ctx, wt.ID); err != nil {
			slog.Warn("failed to delete worktree record",
				"worktree_id", wt.ID, "error", err)
		}
		return nil
	})
	_ = wg.Wait()

	if removeErr == nil {
		return nil
	}
	// git worktree-remove failed. If the path is gone anyway (user
	// pre-deleted it, or git cleaned up partially), treat as success —
	// nothing for the user to clean up.
	if _, statErr := os.Stat(wt.WorktreePath); os.IsNotExist(statErr) {
		return nil
	}
	// Use %s + literal quotes rather than %q so Windows paths stay in
	// their native backslash form. %q would render C:\Users\foo as
	// "C:\\Users\\foo", which is both ugly in the UI (failure_detail
	// is shown to end users) and breaks substring assertions in tests.
	return fmt.Errorf(`git worktree remove "%s": %w`, wt.WorktreePath, removeErr)
}

func currentCheckoutTarget(ctx context.Context, workingDir string) (*rollbackBranch, error) {
	info, err := queryGitPathInfo(ctx, workingDir)
	if err != nil {
		return nil, err
	}
	if info.BranchName != "" {
		return &rollbackBranch{
			WorkingDir:     workingDir,
			OriginalBranch: info.BranchName,
		}, nil
	}
	if info.HeadSHA == "" {
		return nil, errors.New("failed to resolve current HEAD")
	}
	return &rollbackBranch{
		WorkingDir:       workingDir,
		OriginalCommit:   info.HeadSHA,
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

	// `git worktree remove` must complete before `git branch -D` — git
	// refuses to delete a branch still checked out in a worktree. The DB
	// DeleteWorktree is independent of either git step, so run it in
	// parallel with the branch cleanup.
	if err := gitutil.Run(ctx, r.RepoRoot, "worktree", "remove", "--force", r.WorktreePath); err != nil {
		slog.Warn("failed to roll back worktree",
			"worktree_path", r.WorktreePath, "repo_root", r.RepoRoot, "error", err)
	}

	var wg errgroup.Group
	if r.BranchName != "" {
		wg.Go(func() error {
			if err := gitutil.DeleteBranch(ctx, r.RepoRoot, r.BranchName); err != nil {
				slog.Warn("failed to roll back worktree branch",
					"branch", r.BranchName, "repo_root", r.RepoRoot, "error", err)
			}
			return nil
		})
	}
	if r.WorktreeID != "" {
		wg.Go(func() error {
			if err := svc.Queries.DeleteWorktree(ctx, r.WorktreeID); err != nil {
				slog.Warn("failed to roll back tracked worktree",
					"worktree_id", r.WorktreeID, "error", err)
			}
			return nil
		})
	}
	_ = wg.Wait()
}

func (svc *Context) rollbackCreatedBranch(r rollbackBranch) {
	ctx := bgCtx()

	// Restore the original HEAD first so `git branch -D <created>` isn't
	// blocked by "Cannot delete branch X checked out at …" — but ALWAYS
	// attempt the delete even when the restore fails. Otherwise a
	// non-fatal restore error (a working-tree dirty fragment left by the
	// partial checkout, a stale index.lock, a sibling git fighting for
	// the same repo) silently leaves the half-created branch ref behind,
	// and the caller's "rolling back branch %q" UI label is a lie. An
	// orphan ref then blocks a retry with the same name.
	var restoreErr error
	if r.OriginalDetached {
		restoreErr = gitutil.Run(ctx, r.WorkingDir, "checkout", "--detach", r.OriginalCommit)
		if restoreErr != nil {
			slog.Warn("failed to restore detached HEAD before deleting branch",
				"working_dir", r.WorkingDir, "commit", r.OriginalCommit, "error", restoreErr)
		}
	} else {
		restoreErr = gitutil.Run(ctx, r.WorkingDir, "checkout", r.OriginalBranch)
		if restoreErr != nil {
			slog.Warn("failed to restore branch before deleting branch",
				"working_dir", r.WorkingDir, "branch", r.OriginalBranch, "error", restoreErr)
		}
	}

	if err := gitutil.DeleteBranch(ctx, r.WorkingDir, r.CreatedBranch); err != nil {
		// If the restore failed AND the delete failed, git is most
		// likely refusing because the created branch is still the
		// current HEAD — log both so the operator sees the chain.
		if restoreErr != nil {
			slog.Warn("failed to roll back branch creation after restore failure",
				"working_dir", r.WorkingDir, "branch", r.CreatedBranch,
				"restore_error", restoreErr, "delete_error", err)
		} else {
			slog.Warn("failed to roll back branch creation",
				"working_dir", r.WorkingDir, "branch", r.CreatedBranch, "error", err)
		}
	}
}

// unregisterTab drops a tab's worktree association row. Worktree
// deletion itself is driven by the close handler when the caller
// passed WorktreeAction_REMOVE and no other tabs remain.
//
// The REMOVE-close path in closeTabCommon still uses removeTabFromWorktree
// so it can guard the delete by worktree_id (defense-in-depth against a
// stale association). Other callers don't have the worktree in hand and
// would pay for a GetWorktreeForTab round-trip; the bare (tab_type, tab_id)
// delete is enough, because (tab_type, tab_id) is unique in practice.
func (svc *Context) unregisterTab(tabType leapmuxv1.TabType, tabID string) {
	if err := svc.Queries.DeleteWorktreeTabsByTabID(bgCtx(), db.DeleteWorktreeTabsByTabIDParams{
		TabType: tabType,
		TabID:   tabID,
	}); err != nil {
		slog.Warn("failed to remove worktree tab",
			"tab_type", tabType, "tab_id", tabID, "error", err)
	}
}

// removeTabFromWorktree drops the tab→worktree association row for a
// caller that already holds the worktree row. Used by the REMOVE-close
// path to avoid a second GetWorktreeForTab lookup AND to guard the
// delete by worktree_id against a stale association.
func (svc *Context) removeTabFromWorktree(tabType leapmuxv1.TabType, tabID, worktreeID string) {
	if err := svc.Queries.RemoveWorktreeTab(bgCtx(), db.RemoveWorktreeTabParams{
		WorktreeID: worktreeID,
		TabType:    tabType,
		TabID:      tabID,
	}); err != nil {
		slog.Warn("failed to remove worktree tab",
			"worktree_id", worktreeID, "tab_id", tabID, "error", err)
	}
}

var errNotGitRepo = errors.New("path is not inside a git repository")

// notGitRepoErr wraps the underlying git stderr that triggered an
// errNotGitRepo classification, so callers that want to log the real
// diagnostic (dubious-ownership, EACCES, corrupted config) can do so
// while routing decisions keyed on `errors.Is(err, errNotGitRepo)`
// keep working. queryGitPathInfo only returns this type when the
// 3-arg rev-parse retry exits non-zero with a non-ctx error — every
// other "not a repo" path still returns errNotGitRepo bare for
// brevity (those paths don't capture stderr).
//
// Unwrap returns the underlying error so a future caller can route
// on a more specific type (e.g. distinguishing dubious-ownership);
// the Is method anchors the errNotGitRepo classification chain.
type notGitRepoErr struct {
	underlying error
}

func (n *notGitRepoErr) Error() string {
	if n == nil || n.underlying == nil {
		return errNotGitRepo.Error()
	}
	return errNotGitRepo.Error() + ": " + n.underlying.Error()
}

func (n *notGitRepoErr) Is(target error) bool {
	return target == errNotGitRepo
}

func (n *notGitRepoErr) Unwrap() error {
	if n == nil {
		return nil
	}
	return n.underlying
}

// wrapNotGitRepo returns an error that satisfies
// errors.Is(_, errNotGitRepo) but carries `underlying` for logging.
// Pass nil to get the bare errNotGitRepo (callers without a stderr to
// preserve still emit the cleaner sentinel).
func wrapNotGitRepo(underlying error) error {
	if underlying == nil {
		return errNotGitRepo
	}
	return &notGitRepoErr{underlying: underlying}
}

// unwrappedDiagnostic returns the underlying git stderr / error
// message that triggered an errNotGitRepo classification, or "" when
// the caller has no diagnostic to surface (bare errNotGitRepo). Used
// by the GetGitInfo / GetGitFileStatus handlers to populate the
// response's error_hint without leaking the "not a git repository"
// prefix the dialog already renders from is_git_repo=false.
func unwrappedDiagnostic(err error) string {
	var n *notGitRepoErr
	if errors.As(err, &n) && n != nil && n.underlying != nil {
		return n.underlying.Error()
	}
	return ""
}
