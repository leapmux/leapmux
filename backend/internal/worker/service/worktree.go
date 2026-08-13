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
	// RepoRoot is the MAIN repo root, the key gitIndexLocks uses. Carried on the
	// struct rather than re-probed at rollback time: currentCheckoutTarget
	// already has it from its own queryGitPathInfo, and a rollback runs on a
	// failure path where an extra git fork is the last thing wanted.
	RepoRoot         string
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
func (svc *Service) validateGitMode(ctx context.Context, workingDir string, r gitModeRequest) (gitModePlan, error) {
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

func (svc *Service) validateCreateWorktree(ctx context.Context, workingDir, branch, baseBranch string) (gitModePlan, error) {
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

func (svc *Service) validateUseWorktreePath(ctx context.Context, workingDir, worktreePath string) (gitModePlan, error) {
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

func (svc *Service) validateCreateBranch(ctx context.Context, workingDir, branch, base string) (gitModePlan, error) {
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

func (svc *Service) validateCheckoutBranch(ctx context.Context, workingDir, branch string) (gitModePlan, error) {
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
	// RepoRoot is the MAIN repo root, recorded by attachWorktreeIfPresent from
	// the probe it already runs. It is the gitIndexLocks key, so having it here
	// lets the checkout paths lock without a second git fork. Empty when the
	// working dir is not a git repo at all -- there is nothing to serialize then.
	RepoRoot string
	Rollback gitModeRollback
}

// executeGitMode performs the mutations described by plan. Runs on the async
// startup goroutine. Returns partial results with Rollback populated even on
// error, so the caller can decide whether to emit a rollback label and then
// call rollbackGitMode. Honors ctx cancellation between shell-outs so
// CloseAgent/CloseTerminal can abort an in-progress phase 0.
func (svc *Service) executeGitMode(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
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

func (svc *Service) executeCreateWorktree(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	rollback := &rollbackWorktree{
		WorktreePath: plan.WorktreePath,
		RepoRoot:     plan.RepoRoot,
		BranchName:   plan.BranchName,
	}
	result := gitModeResult{Rollback: gitModeRollback{CreatedWorktree: rollback}}

	// Logged BEFORE the lock, with the lock key, so a repeat of the
	// `index.lock: File exists` failure below is readable from the log alone:
	// two of these for one branch means two adds really did race (and, if their
	// repo_root strings differ, that they serialized on different keys), while
	// one means the leftover predates this process.
	slog.Info("creating worktree",
		"repo_root", plan.RepoRoot, "worktree_path", plan.WorktreePath, "branch_name", plan.BranchName)

	// Under the repo's index lock: a concurrent checkout / commit / second
	// worktree add in the same repository would otherwise make one of the two
	// fail outright on index.lock rather than wait.
	addErr := svc.withGitIndexLock(plan.RepoRoot, func() error {
		// Prune first, and this is not hygiene -- it is required for the add to
		// succeed at all after an interrupted attempt.
		//
		// `git worktree add` creates the ADMIN dir (.git/worktrees/<name>/, index
		// and lock included) before it creates the worktree directory. A ctx
		// cancellation in that window -- an agent closed during startup, a channel
		// dropped -- leaves the admin dir behind with no worktree beside it. The
		// validator's collision check cannot see that: it stats the WORKTREE path,
		// which does not exist. So every later add for the same branch name dies
		// with `Unable to create '.git/worktrees/<name>/index.lock': File exists`,
		// permanently, until someone prunes by hand.
		//
		// `worktree prune` removes exactly those orphaned admin entries and
		// nothing else -- an entry whose worktree directory is still present is
		// left alone -- so it cannot disturb a live worktree. Best effort: a prune
		// failure should not mask the add's own error.
		//
		// That "left alone" is also this guard's limit, and the limit is not
		// theoretical. An add interrupted AFTER the worktree directory appeared
		// leaves the directory AND the admin dir's index.lock; prune keeps such
		// an entry (its worktree is present), and the next add for that branch
		// name dies on `Unable to create '.git/worktrees/<name>/index.lock':
		// File exists`. Observed once under an 8-worker e2e load, on a repo
		// whose worktree had never been created successfully. Not reproduced
		// deterministically yet, so deliberately NOT patched by hand here:
		// deleting an index.lock that a live git process still holds trades a
		// rare failure for a corrupt index.
		if pruneErr := gitutil.Run(ctx, plan.RepoRoot, "worktree", "prune"); pruneErr != nil {
			slog.Warn("worktree prune before add failed; continuing",
				"repo_root", plan.RepoRoot, "error", pruneErr)
		}
		return gitutil.Run(ctx, plan.RepoRoot, "worktree", "add", "-b", plan.BranchName, plan.WorktreePath, plan.StartPoint)
	})
	if addErr != nil {
		// Worktree add failed before the dir was created. Drop the
		// rollback pointer so the caller doesn't emit a spurious
		// "rolling back" label for a worktree that never existed.
		return gitModeResult{}, fmt.Errorf("failed to create worktree: %w", addErr)
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

func (svc *Service) executeUseWorktreePath(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	wtID, err := svc.ensureTrackedWorktree(ctx, plan.WorktreePath)
	if err != nil {
		return gitModeResult{}, err
	}
	return gitModeResult{
		WorkingDir: plan.WorktreePath,
		WorktreeID: wtID,
	}, nil
}

func (svc *Service) executeCreateBranch(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
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
	// `git checkout -b` mutates the index, so it runs under the repo's index
	// lock -- currentCheckoutTarget above already resolved the main repo root, so
	// this costs no extra fork. The comment below notes an "index race" as one
	// reason the checkout can abort after writing the ref; this removes the
	// worker's own contribution to that race.
	createErr := svc.withGitIndexLock(currentTarget.RepoRoot, func() error {
		return createBranchInDir(ctx, plan.WorkingDir, plan.BranchName, plan.BaseBranch)
	})
	if err := createErr; err != nil {
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

func (svc *Service) executeCheckoutBranch(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
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
	// Under the repo's index lock. attachWorktreeIfPresent above recorded the
	// main repo root from the probe it already ran, so this costs no extra fork.
	checkoutErr := svc.withGitIndexLock(result.RepoRoot, func() error {
		return checkoutBranchInDir(ctx, plan.WorkingDir, plan.CheckoutTarget)
	})
	if checkoutErr != nil {
		return result, checkoutErr
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
func (svc *Service) restampWorktreeBranch(ctx context.Context, worktreeID, workingDir string) {
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

func (svc *Service) executeUseCurrent(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
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
func (svc *Service) attachWorktreeIfPresent(ctx context.Context, result *gitModeResult, dir string) error {
	info, err := queryGitPathInfo(ctx, dir)
	if err != nil {
		if errors.Is(err, errNotGitRepo) {
			return nil
		}
		return fmt.Errorf("failed to probe worktree at %q: %w", dir, err)
	}
	// Recorded regardless of IsWorktree: the checkout paths need the index-lock
	// key even when the dir is the main repo itself.
	result.RepoRoot = info.RepoRoot
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
// No-op if worktreeID is empty. Used for AGENT/TERMINAL links only (FILE
// links go through FileTabPathStore.linkFileTabToWorktree), so user_id is
// left "" -- agent/terminal ids are globally unique, so neither
// worktree_tab_liveness nor the by-tab delete queries need the user id to
// disambiguate them. worktreeTabUserID is the read side of this same rule.
func (svc *Service) registerTabForWorktree(worktreeID string, tabType leapmuxv1.TabType, tabID string) {
	if worktreeID == "" {
		return
	}
	if err := svc.Queries.AddWorktreeTab(bgCtx(), db.AddWorktreeTabParams{
		WorktreeID: worktreeID,
		TabType:    tabType,
		TabID:      tabID,
		UserID:     worktreeTabUserID(tabType, ""),
	}); err != nil {
		slog.Warn("failed to register tab for worktree",
			"worktree_id", worktreeID, "tab_id", tabID, "error", err)
	}
}

// registerTabForWorktreeAfterClose links a freshly-started tab to its
// worktree, unless a close already landed during startup and decided the
// worktree's fate itself. OpenAgent / OpenTerminal return (and the frontend
// renders the tab) before the startup goroutine reaches the link step, so a
// close can arrive while the association does not exist yet.
//
// What the link means depends on which close arrived, so the decision is the
// close's (carried as a closeWorktreeDisposition), not this function's:
//
//   - keep / remove: skip the link. Writing it would strand a worktree_tabs
//     row pointing at an already-closed tab, whose ref-count never reaches
//     zero. Under KEEP the zero-link worktree is exactly what an online KEEP
//     close leaves; under REMOVE the caller rolls the worktree back instead.
//   - strand: write it deliberately. The workspace itself was deleted, so the
//     row is what makes the worktree an orphan-GC candidate.
//
// closedDuringStartup is supplied by linkWorktreeAfterPhase0, which ORs the
// caller's post-phase-0 re-read of the tab row against the registry's "a close
// recorded a disposition against this startup" flag. Neither source is
// sufficient alone: the re-read misses a close that has cancelled but not yet
// committed closed_at, and the registry misses a close that never went through
// cancelAndClear. A transient re-read error still leaves both false, so we fall
// through and link; the orphan reconciler's worktree GC reclaims that strand (a
// worktree whose links are all dead across two consecutive passes is removed).
// Shared by runAgentStartup / runTerminalStartup so the skip-vs-link decision
// can't drift between the two paths.
func (svc *Service) registerTabForWorktreeAfterClose(worktreeID string, tabType leapmuxv1.TabType, tabID string, closedDuringStartup bool, disposition closeWorktreeDisposition) {
	if !closedDuringStartup {
		svc.registerTabForWorktree(worktreeID, tabType, tabID)
		return
	}
	// A switch with no default so `exhaustive` fails the build if a fourth
	// disposition is added without deciding whether it wants the link.
	switch disposition {
	case strandWorktreeOnClose:
		svc.registerTabForWorktree(worktreeID, tabType, tabID)
	case keepWorktreeOnClose, removeWorktreeOnClose:
		slog.Info("tab closed during startup; skipping worktree link",
			"tab_type", tabType, "tab_id", tabID, "worktree_id", worktreeID)
	}
}

func (svc *Service) ensureTrackedWorktree(ctx context.Context, worktreePath string) (string, error) {
	return svc.ensureTrackedWorktreeWith(ctx, worktreePath, "", "")
}

// ensureTrackedWorktreeWith is the same as ensureTrackedWorktree but
// lets the caller supply known repoRoot and branchName values to skip
// the `git rev-parse` probe on the cold path. Callers that have just
// created the worktree (e.g. `executeCreateWorktree`) already know
// these fields; paying the fork latency twice for the same data is
// pure dialog-open overhead. Pass "" for either field to fall back to
// queryGitPathInfo.
func (svc *Service) ensureTrackedWorktreeWith(ctx context.Context, worktreePath, repoRoot, branchName string) (string, error) {
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

	// CreateWorktree RETURNs the stored row rather than reporting success, so
	// the id below is the one that actually won. The lookup above and this
	// insert are two statements, and a concurrent caller can slip between them
	// -- opening two agents on one worktree ("use existing worktree") starts
	// two async startups that both miss, and both insert -- so the insert
	// completing is NOT proof that our minted id is the stored one.
	stored, err := svc.Queries.CreateWorktree(ctx, db.CreateWorktreeParams{
		ID:           id.Generate(),
		WorktreePath: canonicalPath,
		RepoRoot:     repoRoot,
		BranchName:   branchName,
	})
	if err != nil {
		return "", err
	}
	// Adopt any already-open FILE tabs under this newly-tracked worktree
	// so they ref-count it. Without this, a file opened before the
	// worktree row existed stays unlinked and a sibling close could
	// `git worktree remove` while that editor is still mounted. Use
	// bgCtx() (not the request ctx) so a dialog dismissed / RPC cancelled
	// mid-adoption can't abort the loop partway and leave some FILE tabs
	// unlinked — the same detached-write rationale as registerTabForWorktree.
	svc.FileTabPaths.BackfillWorktreeLinks(bgCtx(), canonicalPath)
	return stored.ID, nil
}

// worktreeRemoveArgs builds the argv for `git worktree remove`. It is the one
// description of that command, because worktreeRemovalBlockedReason derives its
// refusals from exactly this shape: always ONE --force, which removes a
// worktree holding untracked or modified files and still refuses a locked one.
//
// Neither neighbouring form matches that policy, so neither is representable
// here. Two --force override a lock, which the preflight blocks on. Zero
// --force also refuse a DIRTY worktree, which the preflight deliberately never
// blocks on. A change here changes which refusals the preflight may state, and
// TestWorktreeRemoveArgs_PinsThePreflightsAssumption fails until both move
// together.
func worktreeRemoveArgs(worktreePath string) []string {
	return []string{"worktree", "remove", "--force", worktreePath}
}

// gitStillListsWorktree reports whether `git worktree list` still carries an
// entry for wt. It reads the repository's admin directory, so it answers for a
// worktree whose working tree is already gone, which is exactly the case that
// tells a refusal apart from a removal that had nothing left to do.
//
// A listing this cannot read answers true: the caller uses it to suppress a
// failure report, and a failure the worker cannot disprove must reach the user.
func (svc *Service) gitStillListsWorktree(ctx context.Context, wt db.Worktree) bool {
	entries, err := listGitWorktrees(ctx, wt.RepoRoot)
	if err != nil {
		slog.Warn("could not list worktrees to confirm a failed removal; reporting the failure",
			"worktree_id", wt.ID, "repo_root", wt.RepoRoot, "error", err)
		return true
	}
	return worktreeEntryFor(entries, wt.WorktreePath) != nil
}

// removeWorktreeFromDisk force-removes a worktree from disk, deletes its
// branch if no other worktree still uses it, and soft-deletes the DB row.
// It returns a non-nil error when `git worktree remove` failed AND the
// worktree is still there — callers surface that so the user can clean up
// manually. Branch deletion failures are logged but never bubbled (a retained
// branch is recoverable manually; mass-deleting unrelated branches is not).
//
// The DB row is soft-deleted ONLY on the paths that return nil, so the row
// outlives a removal that did not happen. That matters because the row is the
// only handle anything keeps on the directory: ListOrphanCandidateWorktrees
// filters `deleted_at IS NULL`, so a tombstone for a worktree git still holds
// strands the directory forever, and a later `git worktree unlock` never brings
// it back. Keeping the row means the next reconciler pass re-probes it, which
// also self-heals a removal that failed for a transient reason.
//
// The cost of keeping it is that the row still claims worktree_path under the
// unique partial index, so a re-attach at that path adopts this row instead of
// minting a fresh one. That is correct: the directory is still there, and it is
// still this worktree.
func (svc *Service) removeWorktreeFromDisk(wt db.Worktree) error {
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

	args := worktreeRemoveArgs(wt.WorktreePath)
	// Both git commands below run under the repo's index lock, so a concurrent
	// startup doing `worktree add` in the same repository waits instead of
	// failing on index.lock. Taken HERE rather than around each command: the
	// remove and the branch delete are one logical mutation, and releasing
	// between them would let another command interleave into exactly the window
	// where the branch is free but not yet deleted.
	//
	// Safe with respect to worktreeRemovalLocks, which the callers already hold:
	// the documented order is removal (outer) -> index (inner).
	defer svc.lockGitIndex(wt.RepoRoot)()

	// `git worktree remove` must complete before the branch cleanup: git
	// refuses to delete a branch still checked out in a worktree. Once the
	// worktree is gone, the branch cleanup and the DB DeleteWorktree are
	// independent and run in parallel.
	removeErr := gitutil.Run(ctx, wt.RepoRoot, args...)
	if removeErr != nil {
		slog.Warn("failed to remove worktree",
			"worktree_path", wt.WorktreePath, "error", removeErr)
	}

	// Settle the verdict BEFORE the cleanup below, because the soft-delete now
	// depends on it.
	//
	// A failed remove still counts as removed when the path is gone anyway (the
	// user pre-deleted it, or git cleaned up partially) — there is nothing left
	// for anyone to clean up. "Gone" is not the stat alone: `git worktree list`
	// reads the admin directory, not the working tree, so git keeps a LOCKED
	// worktree in its listing after the user deletes the directory by hand, and
	// keeps refusing to remove it. A stat-only check reported that refusal as a
	// removal, and the user was told "Worktree removed" over a worktree git
	// still held, on a branch that still existed.
	removed := removeErr == nil
	if !removed {
		if _, statErr := os.Stat(wt.WorktreePath); os.IsNotExist(statErr) && !svc.gitStillListsWorktree(ctx, wt) {
			removed = true
		}
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
	if removed {
		wg.Go(func() error {
			if err := svc.Queries.DeleteWorktree(ctx, wt.ID); err != nil {
				slog.Warn("failed to delete worktree record",
					"worktree_id", wt.ID, "error", err)
			}
			return nil
		})
	}
	_ = wg.Wait()

	if removed {
		return nil
	}
	// Use %s + literal quotes rather than %q so Windows paths stay in
	// their native backslash form. %q would render C:\Users\foo as
	// "C:\\Users\\foo", which is both ugly in the UI (failure_detail
	// is shown to end users) and breaks substring assertions in tests.
	return fmt.Errorf(`git worktree remove "%s": %w`, wt.WorktreePath, removeErr)
}

// ReapOrphanWorktree removes a worktree the orphan reconciler determined
// has no live tab references — every worktree_tabs link is a startup-race
// strand pointing at a closed/deleted tab. It holds the per-worktree
// removal lock and re-checks live refs under it, so it can never race a
// concurrent REMOVE close or a tab that linked the worktree between the
// reconciler's scan and this call. On success it also drops the dangling
// strand links: worktrees soft-delete, so the worktree_tabs ON DELETE
// CASCADE never fires for them, and the rows would otherwise over-count a
// future worktree adopted at the same path.
//
// It refuses to reap a worktree holding uncommitted or unpushed work --- see
// [Service.worktreeHoldsUnsavedWork]. That guard is what makes an offline
// close safe: no user ever confirms this removal, so the probe stands in for
// the last-tab dialog they never saw.
func (svc *Service) ReapOrphanWorktree(ctx context.Context, wt db.Worktree) {
	mu := svc.worktreeRemovalLock(wt.ID)
	mu.Lock()
	defer mu.Unlock()

	// Re-check under the lock: a close or a late startup link could have
	// changed the live-ref count since the reconciler scanned.
	live, err := svc.Queries.CountLiveWorktreeRefs(ctx, wt.ID)
	if err != nil {
		slog.Warn("orphan worktree GC: count live refs", "worktree_id", wt.ID, "error", err)
		return
	}
	if live != 0 {
		// A tab linked it after the scan — no longer an orphan.
		return
	}
	// Never force-remove work the user has not saved anywhere. The reap runs
	// `git worktree remove --force`, which discards uncommitted changes and
	// deletes the branch, and it runs WITHOUT the user ever seeing the
	// last-tab dialog: an offline close pins KEEP, the choice never reaches
	// this worker, the tab's rows close, and every worktree_tabs link becomes
	// a strand. So the only thing standing between "your laptop's channel
	// dropped" and "your uncommitted work is gone" is this probe.
	//
	// Deliberately conservative in both failure directions: a probe error
	// counts as "might hold work" and skips the reap. The worktree keeps its
	// strand links, so it stays a GC candidate and is re-examined on the next
	// pass -- committing and pushing the work is enough to let it through.
	if hasWork, reason := svc.worktreeHoldsUnsavedWork(ctx, wt); hasWork {
		slog.Info("orphan worktree GC: skipping reap, worktree may hold unsaved work",
			"worktree_id", wt.ID, "worktree_path", wt.WorktreePath, "reason", reason)
		return
	}
	if err := svc.removeWorktreeFromDisk(wt); err != nil {
		// `git worktree remove --force` failed (the worktree is locked, or its
		// .git admin dir is corrupt) and the directory is still on disk. Leave
		// it, and leave the row: removeWorktreeFromDisk soft-deletes only on
		// the paths that removed something, and the strand links below are
		// dropped only on the success path, so this worktree stays a GC
		// candidate and the next pass re-probes it. A `git worktree unlock`,
		// or a repair of the admin dir, is then enough to let the reap through.
		//
		// The retry costs one pass per interval for a worktree that may never
		// become removable. That is the price of not stranding one that will:
		// a tombstone here is permanent, because ListOrphanCandidateWorktrees
		// filters `deleted_at IS NULL` and nothing ever revisits the row.
		slog.Warn("orphan worktree GC: `git worktree remove` failed; leaving the directory on disk and keeping the GC candidate",
			"worktree_id", wt.ID, "worktree_path", wt.WorktreePath, "error", err)
		return
	}
	if err := svc.Queries.DeleteWorktreeTabsByWorktreeID(bgCtx(), wt.ID); err != nil {
		slog.Warn("orphan worktree GC: drop strand links", "worktree_id", wt.ID, "error", err)
	}
	slog.Info("orphan worktree GC: reclaimed unreferenced worktree",
		"worktree_id", wt.ID, "worktree_path", wt.WorktreePath)
}

// worktreeHoldsUnsavedWork reports whether wt's directory holds work that
// only exists there: uncommitted changes, or commits no remote has. The
// second return value names the reason for the operator log.
//
// Fails CLOSED -- every error answers "yes, might hold work". A worktree
// whose directory is already gone answers "no": there is nothing left to
// lose, and refusing would strand the DB row forever.
//
// The two probes mirror what the last-tab close dialog shows the user
// (inspectLastTabClose's dirty + unpushed counts), so an offline reap
// applies the same standard the user would have been asked about.
func (svc *Service) worktreeHoldsUnsavedWork(ctx context.Context, wt db.Worktree) (bool, string) {
	if _, err := os.Stat(wt.WorktreePath); os.IsNotExist(err) {
		return false, ""
	}
	dirty, err := isDirty(ctx, wt.WorktreePath)
	if err != nil {
		return true, fmt.Sprintf("dirty-tree probe failed: %v", err)
	}
	if dirty {
		return true, "uncommitted changes"
	}
	// Probe the LIVE branch, not wt.BranchName. removeWorktreeFromDisk -- the
	// thing this guard protects -- deliberately re-resolves HEAD for the same
	// reason (an external `git checkout` from a terminal, IDE, or sibling agent,
	// or the post-attach checkout in executeCheckoutBranch, moves HEAD without
	// updating the row), and it is the LIVE branch it will `git branch -D`. A
	// guard measuring the stale row could clear a branch it never examined.
	branchName := wt.BranchName
	if info, infoErr := queryGitPathInfo(ctx, wt.WorktreePath); infoErr == nil {
		branchName = info.BranchName
	}
	status, err := pushStatusForPath(ctx, wt.WorktreePath, branchName)
	if err != nil {
		return true, fmt.Sprintf("push-status probe failed: %v", err)
	}
	// With no origin at all, "unpushed" is not a meaningful state for this repo
	// and only dirtiness can save the tree.
	if !status.OriginExists {
		return false, ""
	}
	// The one question that matters, and the only one that is correct for every
	// shape: are there commits no remote has? pushStatusForPath answers it for a
	// tracking branch, for a branch with no upstream, for a detached HEAD, and
	// for a branch whose upstream was pruned.
	//
	// The predicate used to be `!status.UpstreamExists`, an upstream-CONFIG
	// proxy, which was wrong in both directions: `git worktree add -b` gives a
	// new branch no upstream by construction, so every worktree LeapMux itself
	// created was reported as holding work forever and the GC reclaimed nothing
	// it made; while the pruned-upstream case set RemoteMissing and left
	// Unpushed at 0, which read as "nothing to lose" and force-removed
	// local-only commits.
	if status.Unpushed > 0 {
		return true, fmt.Sprintf("%d unpushed commit(s)", status.Unpushed)
	}
	return false, ""
}

func currentCheckoutTarget(ctx context.Context, workingDir string) (*rollbackBranch, error) {
	info, err := queryGitPathInfo(ctx, workingDir)
	if err != nil {
		return nil, err
	}
	if info.BranchName != "" {
		return &rollbackBranch{
			RepoRoot:       info.RepoRoot,
			WorkingDir:     workingDir,
			OriginalBranch: info.BranchName,
		}, nil
	}
	if info.HeadSHA == "" {
		return nil, errors.New("failed to resolve current HEAD")
	}
	return &rollbackBranch{
		RepoRoot:         info.RepoRoot,
		WorkingDir:       workingDir,
		OriginalCommit:   info.HeadSHA,
		OriginalDetached: true,
	}, nil
}

// rollbackGitModeAfterClose is the close-during-startup counterpart of
// rollbackGitMode. A close is NOT a failed startup: the git-mode mutation
// succeeded, the user saw the tab, and the close carries an explicit decision
// about the worktree. Undoing it unconditionally -- which is what this path
// used to do -- made the outcome of closing a tab depend on whether the close
// happened to race startup, and `git worktree remove --force` there discarded
// uncommitted work the user had just been shown a dialog about and asked to
// KEEP, silently (the rollback only logs when it fails).
//
// So only a REMOVE close rolls anything back, and it does so because the close
// itself could not: the worktree_tabs link it looks up did not exist yet. The
// other two dispositions leave the directory alone -- keep because the user
// said so, strand because the orphan reconciler owns it and applies the
// unsaved-work probe this path has never had.
func (svc *Service) rollbackGitModeAfterClose(result gitModeResult, disposition closeWorktreeDisposition) {
	// A switch with no default so `exhaustive` fails the build if a fourth
	// disposition is added without deciding what it means here -- the two
	// no-op cases are spelled out rather than defaulted for that reason.
	switch disposition {
	case removeWorktreeOnClose:
		svc.rollbackGitMode(result)
	case keepWorktreeOnClose, strandWorktreeOnClose:
		// keep: the user asked to keep the worktree. strand: the workspace is
		// gone and the orphan reconciler owns the worktree, applying the
		// unsaved-work probe this path has never had.
	}
}

// rollbackGitModeAfterStartup applies the rollback policy for a startup that is
// ending without having handed its tab over.
//
// `raced` is closeDisposition's ok flag, and it is the authority for "was this
// startup overtaken by a close" -- the disposition alone cannot say, because a
// KEEP close and a startup nothing closed both arrive as the zero value
// keepWorktreeOnClose. The two need opposite handling: a startup that failed on
// its own OWNS the rollback, since no close is coming to decide the worktree's
// fate and leaving the mutation in place would strand a worktree and a branch
// the user never saw. One a close overtook must defer to that close instead.
func (svc *Service) rollbackGitModeAfterStartup(result gitModeResult, disposition closeWorktreeDisposition, raced bool) {
	if !raced {
		svc.rollbackGitMode(result)
		return
	}
	svc.rollbackGitModeAfterClose(result, disposition)
}

func (svc *Service) rollbackGitMode(result gitModeResult) {
	if result.Rollback.CreatedBranch != nil {
		svc.rollbackCreatedBranch(*result.Rollback.CreatedBranch)
	}
	if result.Rollback.CreatedWorktree != nil {
		svc.rollbackCreatedWorktree(*result.Rollback.CreatedWorktree)
	}
}

func (svc *Service) rollbackCreatedWorktree(r rollbackWorktree) {
	ctx := bgCtx()

	// Under the repo's index lock for the same reason as removeWorktreeFromDisk:
	// a rollback races whatever else is starting in this repository, and losing
	// that race would leave the half-created worktree on disk.
	defer svc.lockGitIndex(r.RepoRoot)()

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

func (svc *Service) rollbackCreatedBranch(r rollbackBranch) {
	ctx := bgCtx()

	// Under the repo's index lock. The note below already named "a stale
	// index.lock, a sibling git fighting for the same repo" among the reasons the
	// HEAD recovery can fail; this is what stops the worker BEING that sibling.
	defer svc.lockGitIndex(r.RepoRoot)()

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

// worktreeTabUserID returns the user_id a worktree_tabs row carries for a tab
// of this type. It is the single definition of that convention, shared by
// every by-tab read and delete so they cannot drift from what
// registerTabForWorktree / linkFileTabToWorktree wrote.
//
// FILE links carry the owning user because file tab ids are unique only within
// a user (worker_file_tabs is keyed by (user_id, tab_id)); AGENT/TERMINAL ids
// are globally unique, so their links carry "" and callers that only have an
// agent/terminal in hand need not know the user at all. Normalizing here --
// rather than at each call site -- means passing the authenticated user for an
// AGENT close is harmless instead of a silent no-match.
func worktreeTabUserID(tabType leapmuxv1.TabType, userID string) string {
	if tabType == leapmuxv1.TabType_TAB_TYPE_FILE {
		return userID
	}
	return ""
}

// unregisterTab drops a tab's worktree association row. Worktree
// deletion itself is driven by the close handler when the caller
// passed WorktreeAction_REMOVE and no other tabs remain.
//
// The REMOVE-close path in closeTabCommon still uses removeTabFromWorktree
// so it can guard the delete by worktree_id (defense-in-depth against a
// stale association). Other callers don't have the worktree in hand and
// would pay for a GetWorktreeForTab round-trip; the (tab_type, tab_id,
// user_id) delete is enough, because a tab belongs to at most one worktree in
// practice.
//
// userID is normalized through worktreeTabUserID, so AGENT/TERMINAL callers
// may pass either the authenticated user or "".
func (svc *Service) unregisterTab(tabType leapmuxv1.TabType, tabID, userID string) {
	if err := svc.Queries.DeleteWorktreeTabsByTabID(bgCtx(), db.DeleteWorktreeTabsByTabIDParams{
		TabType: tabType,
		TabID:   tabID,
		UserID:  worktreeTabUserID(tabType, userID),
	}); err != nil {
		slog.Warn("failed to remove worktree tab",
			"tab_type", tabType, "tab_id", tabID, "error", err)
	}
}

// removeTabFromWorktree drops the tab→worktree association row for a
// caller that already holds the worktree row. Used by the REMOVE-close
// path to avoid a second GetWorktreeForTab lookup AND to guard the
// delete by worktree_id against a stale association. Returns the DB
// error (also logged) so the REMOVE-close path can surface a partial
// failure rather than letting the surviving link masquerade as a
// sibling still referencing the worktree.
func (svc *Service) removeTabFromWorktree(tabType leapmuxv1.TabType, tabID, userID, worktreeID string) error {
	if err := svc.Queries.RemoveWorktreeTab(bgCtx(), db.RemoveWorktreeTabParams{
		WorktreeID: worktreeID,
		TabType:    tabType,
		TabID:      tabID,
		UserID:     worktreeTabUserID(tabType, userID),
	}); err != nil {
		slog.Warn("failed to remove worktree tab",
			"worktree_id", worktreeID, "tab_id", tabID, "error", err)
		return err
	}
	return nil
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
