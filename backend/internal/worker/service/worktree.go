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
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/leapmux/leapmux/util/validate"
	"golang.org/x/sync/errgroup"
)

type rollbackWorktree struct {
	WorktreeID   string
	WorktreePath string
	RepoRoot     string
	RepoGitDir   string
	BranchName   string
	// Creation evidence and the failing tab are runtime-only. Recovery drops rollback rights.
	initialBranchCommit string
	startup             worktreeRollbackStartup
}

type worktreeRollbackStartup struct {
	key      ownedTabKey
	registry *startupCore
	entry    *startupEntry
}

func (startup worktreeRollbackStartup) preservesArtifact() bool {
	if startup.registry == nil || startup.entry == nil {
		return false
	}
	_, closeRaced := startup.registry.dispositionOf(startup.entry)
	return closeRaced || ((startup.registry.archiveStopped(startup.entry) || startup.registry.shutdownStopped(startup.entry)) && startup.registry.phase0Complete(startup.entry))
}

type rollbackBranch struct {
	// RepoGitDir captures the common directory used to choose gitIndexLocks.
	// Later probes validate that authority and never replace it with another repository.
	RepoRoot         string
	RepoGitDir       string
	WorkingDir       string
	CreatedBranch    string
	OriginalBranch   string
	OriginalCommit   string
	OriginalDetached bool
	// initialBranchCommit comes from the immutable creation intent. Recovered
	// phase-0 results discard rollback rights, so this evidence stays runtime-only.
	initialBranchCommit string
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

	// RepoRoot supplies placement. RepoGitDir supplies stable identity
	// and the administrative context for repository-wide commands.
	RepoRoot   string
	RepoGitDir string

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

// validateGitMode performs read-only validation of a request's git setup and
// returns a gitModePlan describing what executeGitMode will do.
// All returned errors are CodeInvalidArgument-eligible — callers should
// surface them via sendInvalidArgument so bad user input fails fast at the
// RPC boundary without mutating any state or creating any DB row.
//
// Every input has one defined result. An absent setup uses the directory as
// it is. A present setup that selects no mode, or a mode whose required value
// is empty, is refused here, before any git command runs. The semantic checks
// (whether a branch exists, whether a path is a worktree) run in the per-mode
// validators.
func (svc *Service) validateGitMode(ctx context.Context, workingDir string, setup *leapmuxv1.GitSetup) (gitModePlan, error) {
	if setup == nil {
		// useCurrent: no mutation, nothing to validate against beyond the dir
		// existing. If the dir happens to be inside a managed worktree we'll
		// register the association during executeGitMode.
		return gitModePlan{
			Mode:              gitModeUseCurrent,
			WorkingDir:        workingDir,
			PlannedWorkingDir: workingDir,
		}, nil
	}
	switch mode := setup.GetMode().(type) {
	case *leapmuxv1.GitSetup_CreateWorktree:
		branch := mode.CreateWorktree.GetBranch()
		if branch == "" {
			return gitModePlan{}, errors.New("create_worktree.branch is required")
		}
		return svc.validateCreateWorktree(ctx, workingDir, branch, mode.CreateWorktree.GetBaseBranch())
	case *leapmuxv1.GitSetup_UseWorktreePath:
		if mode.UseWorktreePath == "" {
			return gitModePlan{}, errors.New("use_worktree_path is required")
		}
		return svc.validateUseWorktreePath(ctx, workingDir, mode.UseWorktreePath)
	case *leapmuxv1.GitSetup_CheckoutBranch:
		if mode.CheckoutBranch == "" {
			return gitModePlan{}, errors.New("checkout_branch is required")
		}
		return svc.validateCheckoutBranch(ctx, workingDir, mode.CheckoutBranch)
	case *leapmuxv1.GitSetup_CreateBranch:
		name := mode.CreateBranch.GetName()
		if name == "" {
			return gitModePlan{}, errors.New("create_branch.name is required")
		}
		return svc.validateCreateBranch(ctx, workingDir, name, mode.CreateBranch.GetBaseBranch())
	default:
		return gitModePlan{}, errors.New("git_setup selects no mode")
	}
}

func (svc *Service) validateCreateWorktree(ctx context.Context, workingDir, branch, baseBranch string) (gitModePlan, error) {
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
		inUse, err := (gitutil.Repository{GitDir: info.CommonDir}).IsBranchInUse(gctx, branch)
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
		RepoGitDir:        info.CommonDir,
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
		// CloseTab(REMOVE) would then attempt `git worktree remove`
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

// gitModeResult holds the final working directory and worktree ID after
// executeGitMode completes (or partially completes before failing).
type gitModeResult struct {
	WorkingDir string
	WorktreeID string
	// RepoRoot supplies placement. RepoGitDir supplies identity and the mutex
	// key from the existing metadata probe. Both are empty outside Git.
	RepoRoot   string
	RepoGitDir string
	Rollback   gitModeRollback
}

// bindCreatedWorktreeStartup identifies the startup tab excluded from rollback's live-owner check.
func (result *gitModeResult) bindCreatedWorktreeStartup(startup worktreeRollbackStartup) {
	if created := result.Rollback.CreatedWorktree; created != nil {
		created.startup = startup
	}
}

// executeGitMode performs the mutations described by plan. Runs on the async
// startup goroutine. Returns partial results with Rollback populated even on
// error, so the caller can decide whether to emit a rollback label and then
// call rollbackGitMode. Honors ctx cancellation between shell-outs so
// CloseTab can abort an in-progress phase 0.
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
		RepoGitDir:   plan.RepoGitDir,
		BranchName:   plan.BranchName,
	}
	result := gitModeResult{
		WorkingDir: plan.WorktreePath,
		RepoRoot:   plan.RepoRoot,
		RepoGitDir: plan.RepoGitDir,
		Rollback:   gitModeRollback{CreatedWorktree: rollback},
	}

	// Logged BEFORE the lock, with the lock key, so a repeat of the
	// `index.lock: File exists` failure below is readable from the log alone:
	// two of these for one branch means two adds really did race (and, if their
	// repo_git_dir strings differ, that they serialized on different keys), while
	// one means the leftover predates this process.
	slog.Info("creating worktree",
		"repo_root", plan.RepoRoot, "repo_git_dir", plan.RepoGitDir, "worktree_path", plan.WorktreePath, "branch_name", plan.BranchName)

	// Under the repo's index lock: a concurrent checkout / commit / second
	// worktree add in the same repository would otherwise make one of the two
	// fail outright on index.lock rather than wait.
	addErr := svc.withGitIndexLock(plan.RepoGitDir, func() error {
		if plan.StartPoint == "" {
			return errors.New("worktree startup requires an initial commit")
		}
		var resolved string
		var err error
		if plan.StartPoint == "HEAD" {
			// HEAD belongs to the selected checkout. Other refs, including the
			// immutable durable intent's object ID, use the common repository.
			resolved, err = gitutil.Output(ctx, plan.WorkingDir, "rev-parse", "--verify", plan.StartPoint+"^{commit}")
		} else {
			resolved, err = (gitutil.Repository{GitDir: plan.RepoGitDir}).Output(ctx, "rev-parse", "--verify", plan.StartPoint+"^{commit}")
		}
		if err != nil {
			return fmt.Errorf("resolve the worktree's initial commit: %w", err)
		}
		initial := strings.TrimSpace(resolved)
		if initial == "" {
			return errors.New("worktree startup requires a resolved initial commit")
		}
		rollback.initialBranchCommit = initial
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
		if pruneErr := (gitutil.Repository{GitDir: plan.RepoGitDir}).Run(ctx, "worktree", "prune"); pruneErr != nil {
			slog.Warn("worktree prune before add failed; continuing",
				"repo_root", plan.RepoRoot, "error", pruneErr)
		}
		return (gitutil.Repository{GitDir: plan.RepoGitDir}).Run(ctx, "worktree", "add", "-b", plan.BranchName, plan.WorktreePath, rollback.initialBranchCommit)
	})
	if addErr != nil {
		// A failed add can leave partial Git state. It does not prove this
		// request owns the directory and branch, so it supplies no rollback rights.
		return gitModeResult{}, fmt.Errorf("failed to create worktree: %w", addErr)
	}
	slog.Info("worktree created", "worktree_path", plan.WorktreePath, "branch_name", plan.BranchName)

	if err := ctx.Err(); err != nil {
		return result, err
	}

	// `git worktree add` was just invoked with these exact values, so
	// they're authoritative for the new worktree — skip the queryGit-
	// PathInfo fork that would re-derive them.
	wtID, err := svc.ensureTrackedWorktreeWith(ctx, plan.WorktreePath, plan.RepoRoot, plan.RepoGitDir, plan.BranchName)
	if err != nil {
		return result, fmt.Errorf("failed to track worktree: %w", err)
	}
	rollback.WorktreeID = wtID
	result.WorktreeID = wtID
	return result, nil
}

func (svc *Service) executeUseWorktreePath(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	wtID, err := svc.ensureTrackedWorktree(ctx, plan.WorktreePath)
	if err != nil {
		return gitModeResult{}, err
	}
	tracked, err := svc.Queries.GetWorktreeByID(ctx, wtID)
	if err != nil {
		return gitModeResult{}, err
	}
	return gitModeResult{WorkingDir: plan.WorktreePath, WorktreeID: wtID, RepoRoot: tracked.RepoRoot, RepoGitDir: tracked.RepoGitDir}, nil
}

func (svc *Service) executeCreateBranch(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	branch, base, err := validateBranchCreationArgs(plan.BranchName, plan.BaseBranch)
	if err != nil {
		return gitModeResult{}, err
	}
	var result gitModeResult
	err = svc.withRepoIndexLockForDir(ctx, plan.WorkingDir, func(current *gitPathInfo) error {
		if plan.RepoGitDir != "" && !pathutil.SamePath(plan.RepoGitDir, current.CommonDir) {
			return errors.New("the planned checkout belongs to a different git repository")
		}
		reference := "refs/heads/" + branch
		found, err := gitutil.HasRefs(ctx, plan.WorkingDir, reference)
		if err != nil {
			return err
		}
		if found[reference] {
			return fmt.Errorf("branch %q already exists", branch)
		}
		target, err := rollbackBranchFromPathInfo(plan.WorkingDir, current)
		if err != nil {
			return err
		}
		initialCommit := current.HeadSHA
		if base != "" {
			resolved, err := gitutil.Output(ctx, plan.WorkingDir, "rev-parse", "--verify", base+"^{commit}")
			if err != nil {
				return fmt.Errorf("resolve the new branch's initial commit: %w", err)
			}
			initialCommit = strings.TrimSpace(resolved)
		}
		if initialCommit == "" {
			return errors.New("branch startup requires a resolved initial commit")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		target.CreatedBranch = branch
		target.initialBranchCommit = initialCommit
		mutationErr := createBranchInDir(ctx, plan.WorkingDir, branch, initialCommit)
		if mutationErr != nil {
			// A canceled start or an early Git refusal creates no branch. Verify the
			// artifact while this mutex still excludes another service creator.
			probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitReadTimeout)
			created, probeErr := gitutil.HasRefs(probeCtx, plan.WorkingDir, reference)
			cancel()
			if probeErr != nil {
				slog.Warn("branch rollback ownership could not be verified; keeping git state", "branch", branch, "error", probeErr)
				return mutationErr
			}
			if !created[reference] {
				return mutationErr
			}
		}
		result = gitModeResult{WorkingDir: plan.WorkingDir, RepoRoot: current.RepoRoot, RepoGitDir: current.CommonDir, Rollback: gitModeRollback{CreatedBranch: target}}
		if mutationErr != nil {
			return mutationErr
		}
		// Association and mutation share the same repository hold. A later tab
		// operation cannot change HEAD between this operation and its tracking read.
		return svc.attachWorktreeIfPresent(ctx, &result, plan.WorkingDir)
	})
	return result, err
}

func (svc *Service) executeCheckoutBranch(ctx context.Context, plan gitModePlan) (gitModeResult, error) {
	result := gitModeResult{WorkingDir: plan.WorkingDir}
	err := svc.withRepoIndexLockForDir(ctx, plan.WorkingDir, func(current *gitPathInfo) error {
		if plan.RepoGitDir != "" && !pathutil.SamePath(plan.RepoGitDir, current.CommonDir) {
			return errors.New("the planned checkout belongs to a different git repository")
		}
		// Register before checkout: a failed association must leave HEAD alone.
		if err := svc.attachWorktreeIfPresent(ctx, &result, plan.WorkingDir); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkoutBranchInDir(ctx, plan.WorkingDir, plan.CheckoutTarget); err != nil {
			return err
		}
		// This best-effort read stays under the same hold as its own checkout.
		svc.restampWorktreeBranch(ctx, result.WorktreeID, plan.WorkingDir)
		return nil
	})
	return result, err
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
	// Detached HEAD has no attached branch and keeps the previous stored name.
	// An unborn symbolic HEAD still supplies its branch name.
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

// attachWorktreeIfPresent reads Git metadata for the tab's actual directory.
// Only a tab at the linked worktree top level creates a tracking record.
// A nested tab can link to an existing record. Detection never owns an existing artifact.
// A non-repository directory needs no association. Other reported errors reach the caller.
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
	result.RepoGitDir = info.CommonDir
	if !info.IsWorktree {
		return nil
	}
	if !isGitTopLevel(dir, info) {
		wt, err := svc.Queries.GetWorktreeByPath(ctx, pathutil.Canonicalize(info.TopLevel))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		result.WorktreeID = wt.ID
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
// links go through TabPayloadStore.linkTabToWorktree), so user_id is
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

// registerTabForWorktreeAfterClose links a tab after its Git startup phase.
// A close can finish before startup writes this link.
// KEEP and REMOVE skip a link for a closed tab. Workspace deletion leaves a dead link for database cleanup.
// The caller combines the stored closed state with the startup close disposition.
// A read failure can leave a dead link, which the reconciler removes without deleting Git artifacts.
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
	return svc.ensureTrackedWorktreeWith(ctx, worktreePath, "", "", "")
}

// ensureTrackedWorktreeWith is the same as ensureTrackedWorktree but
// lets the caller supply known repoRoot, repoGitDir and branchName values to skip
// the `git rev-parse` probe on the cold path. Callers that have just
// created the worktree (e.g. `executeCreateWorktree`) already know
// these fields; paying the fork latency twice for the same data is
// pure dialog-open overhead. Pass "" for a field to fall back to
// queryGitPathInfo.
func (svc *Service) ensureTrackedWorktreeWith(ctx context.Context, worktreePath, repoRoot, repoGitDir, branchName string) (string, error) {
	canonicalPath := pathutil.Canonicalize(worktreePath)

	existing, err := svc.Queries.GetWorktreeByPath(ctx, canonicalPath)
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	if repoRoot == "" || repoGitDir == "" || branchName == "" {
		info, err := queryGitPathInfo(ctx, canonicalPath)
		if err != nil {
			return "", err
		}
		if repoRoot == "" {
			repoRoot = info.RepoRoot
		}
		if repoGitDir == "" {
			repoGitDir = info.CommonDir
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
		RepoGitDir:   repoGitDir,
		BranchName:   branchName,
	})
	if err != nil {
		return "", err
	}
	// Tracking succeeded. A failed backfill must not lose the stored ID or abort startup.
	svc.backfillWorktreeLinks(bgCtx(), stored)
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
	entries, err := listRepositoryWorktrees(ctx, gitutil.Repository{GitDir: wt.RepoGitDir})
	if err != nil {
		slog.Warn("could not list worktrees to confirm a failed removal; reporting the failure",
			"worktree_id", wt.ID, "repo_root", wt.RepoRoot, "error", err)
		return true
	}
	return worktreeEntryFor(entries, wt.WorktreePath) != nil
}

// removeWorktreeFromDisk removes a worktree after its caller authorizes removal.
// It resolves the current branch because an external checkout can change the recorded branch.
// The repository index lock protects removal and branch deletion as one operation.
// Git locks still prevent removal. A failed removal keeps the tracking record.
// A missing directory counts as removed only when Git no longer lists it.
func (svc *Service) removeWorktreeFromDisk(wt db.Worktree) error {
	identity, err := svc.captureWorktreeRemovalIdentity(bgCtx(), wt, newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED, wt.ID, ""))
	if err != nil {
		if _, statErr := os.Stat(wt.WorktreePath); !errors.Is(statErr, os.ErrNotExist) || svc.gitStillListsWorktree(bgCtx(), wt) {
			return fmt.Errorf("prepare removal of worktree %q: %w", wt.WorktreePath, err)
		}
	}
	defer releaseRemovalIdentityBestEffort(identity)
	return svc.removeWorktreeFromDiskRecorded(bgCtx(), wt, &memoryWorktreeRemovalJournal{identity: identity})
}

// removeWorktreeFromDiskRecorded keeps one destructive implementation for all closes.
func (svc *Service) removeWorktreeFromDiskRecorded(ctx context.Context, wt db.Worktree, journal worktreeRemovalJournal) error {
	defer svc.lockGitIndex(wt.RepoGitDir)()
	return svc.removeWorktreeFromDiskRecordedLocked(ctx, wt, journal, worktreeRemoveArgs(wt.WorktreePath))
}

// The caller holds the index lock. Removal owners take the outer removal and
// lifecycle locks before entering this function.
func (svc *Service) removeWorktreeFromDiskRecordedLocked(ctx context.Context, wt db.Worktree, journal worktreeRemovalJournal, removeArgs []string) error {
	checkpoint := journal.Checkpoint()
	if checkpoint != nil && (checkpoint.WorktreeID != wt.ID || !pathutil.SamePath(pathutil.Canonicalize(checkpoint.WorktreePath), pathutil.Canonicalize(wt.WorktreePath)) || !pathutil.SamePath(checkpoint.RepoGitDir, wt.RepoGitDir)) {
		return errors.New("the saved removal identifies a different worktree")
	}
	if checkpoint == nil {
		intent := worktreeRemovalCheckpoint{WorktreeID: wt.ID, WorktreePath: wt.WorktreePath, RepoRoot: wt.RepoRoot, RepoGitDir: wt.RepoGitDir}
		// A failed live probe omits branch cleanup. The SQL branch can be stale.
		if info, err := queryGitPathInfo(ctx, wt.WorktreePath); err == nil {
			intent.Branch = info.BranchName
			intent.Commit = info.HeadSHA
		} else if err != nil {
			slog.Warn("read worktree branch before removal failed; keeping its branch", "worktree_id", wt.ID, "error", err)
		}
		if svc.afterWorktreeBranchProbeFn != nil {
			svc.afterWorktreeBranchProbeFn()
		}
		if err := journal.SaveIntent(ctx, intent); err != nil {
			return &closeProgressWriteError{err}
		}
		checkpoint = journal.Checkpoint()
	}
	if !checkpoint.DirectoryRemoved {
		removed := false
		if _, err := os.Stat(wt.WorktreePath); errors.Is(err, os.ErrNotExist) && !svc.gitStillListsWorktree(ctx, wt) {
			removed = true
		}
		var removeErr error
		if !removed {
			if err := validateWorktreeRemovalIdentity(ctx, wt, journal.TargetIdentity()); err != nil {
				return err
			}
			info, err := queryGitPathInfo(ctx, wt.WorktreePath)
			if err != nil {
				return err
			}
			if info.BranchName != checkpoint.Branch || info.HeadSHA != checkpoint.Commit {
				return errors.New("the worktree branch or commit changed after removal was prepared")
			}
			removeErr = (gitutil.Repository{GitDir: wt.RepoGitDir}).Run(ctx, removeArgs...)
			removed = removeErr == nil
		}
		if !removed {
			if _, err := os.Stat(wt.WorktreePath); errors.Is(err, os.ErrNotExist) && !svc.gitStillListsWorktree(ctx, wt) {
				removed = true
			}
		}
		if !removed {
			return fmt.Errorf(`git worktree remove "%s": %w`, wt.WorktreePath, removeErr)
		}
		if err := journal.SaveRemoved(ctx, *checkpoint); err != nil {
			return &closeProgressWriteError{err}
		}
		checkpoint = journal.Checkpoint()
	}
	if err := svc.finishRecordedWorktreeRemoval(ctx, wt, *checkpoint); err != nil {
		return journal.TrackingCleanupFailed(err)
	}
	return nil
}

// finishRecordedWorktreeRemoval never removes a directory. Expected-ref deletion
// preserves a branch whose tip differs from the recorded removal commit.
func (svc *Service) finishRecordedWorktreeRemoval(ctx context.Context, wt db.Worktree, checkpoint worktreeRemovalCheckpoint) error {
	var cleanup errgroup.Group
	if checkpoint.Branch != "" && checkpoint.Commit != "" {
		cleanup.Go(func() error {
			if err := deleteUnusedBranchAtCommit(ctx, gitutil.Repository{GitDir: wt.RepoGitDir}, checkpoint.Branch, checkpoint.Commit); err != nil {
				slog.Warn("worktree branch cleanup did not complete", "branch", checkpoint.Branch, "error", err)
			}
			return nil
		})
	}
	cleanup.Go(func() error {
		// A path created after confirmed removal belongs to a new filesystem object.
		// Preserve its tracking row rather than detach a newly admitted resource.
		if _, err := os.Stat(wt.WorktreePath); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return svc.Queries.DeleteWorktree(ctx, wt.ID)
	})
	return cleanup.Wait()
}

// CleanupOrphanWorktreeLinks removes only links whose tabs are no longer live.
// Neither a missing tab nor a zero link count authorizes Git deletion.
func (svc *Service) CleanupOrphanWorktreeLinks(ctx context.Context, wt db.Worktree) error {
	mu := svc.worktreeRemovalLock(wt.ID)
	mu.Lock()
	defer mu.Unlock()
	return svc.Queries.DeleteDeadWorktreeTabs(ctx, wt.ID)
}

func currentCheckoutTarget(ctx context.Context, workingDir string) (*rollbackBranch, error) {
	info, err := queryGitPathInfo(ctx, workingDir)
	if err != nil {
		return nil, err
	}
	return rollbackBranchFromPathInfo(workingDir, info)
}

func rollbackBranchFromPathInfo(workingDir string, info *gitPathInfo) (*rollbackBranch, error) {
	if info == nil {
		return nil, errors.New("current checkout metadata is required")
	}
	if info.BranchName != "" {
		return &rollbackBranch{
			RepoRoot:       info.RepoRoot,
			RepoGitDir:     info.CommonDir,
			WorkingDir:     workingDir,
			OriginalBranch: info.BranchName,
		}, nil
	}
	if info.HeadSHA == "" {
		return nil, errors.New("failed to resolve current HEAD")
	}
	return &rollbackBranch{
		RepoRoot:         info.RepoRoot,
		RepoGitDir:       info.CommonDir,
		WorkingDir:       workingDir,
		OriginalCommit:   info.HeadSHA,
		OriginalDetached: true,
	}, nil
}

// rollbackGitModeAfterClose applies a confirmed close to a worktree that this request created.
// Detection of an existing worktree supplies no rollback ownership.
// KEEP and workspace deletion preserve the artifact, even when they interrupt startup.
// Recheck the directory and live references because the close can precede worktree registration.
func (svc *Service) rollbackGitModeAfterClose(result gitModeResult, disposition closeWorktreeDisposition) {
	// A switch with no default so `exhaustive` fails the build if a fourth
	// disposition is added without deciding what it means here -- the two
	// no-op cases are spelled out rather than defaulted for that reason.
	switch disposition {
	case removeWorktreeOnClose:
		created := result.Rollback.CreatedWorktree
		if created == nil {
			return
		}
		mu := svc.worktreeRemovalLock(created.WorktreeID)
		mu.Lock()
		defer mu.Unlock()
		defer svc.worktreeLifecycle.remove(created.WorktreePath)()
		verdict := svc.checkWorktreeRemovalEligibility(bgCtx(), result.WorkingDir, created.WorktreePath, ownedTabKey{})
		if verdict.refusal != "" || verdict.err != nil || verdict.otherLiveTabs {
			return
		}
		if reason, err := svc.worktreeRemovalBlockedReason(bgCtx(), created.WorktreePath, verdict.info); err != nil || reason != "" {
			return
		}
		wt, err := svc.createdWorktreeRecord(bgCtx(), *created)
		if err != nil {
			slog.Warn("read created worktree for confirmed removal failed", "error", err)
			return
		}
		if err := svc.removeWorktreeFromDisk(wt); err != nil {
			slog.Warn("confirmed created-worktree removal failed", "worktree_path", created.WorktreePath, "error", err)
		}
	case keepWorktreeOnClose, strandWorktreeOnClose:
		// Both dispositions preserve Git artifacts. The reconciler cleans dead links only.
	}
}

// rollbackGitModeAfterStartup applies the rollback policy for a startup that is
// ending without having handed its tab over.
//
// `raced` is closeDisposition's ok flag. A KEEP close and an ordinary failure
// both carry keepWorktreeOnClose, so the disposition alone cannot select the
// policy. An ordinary failure attempts guarded automatic cleanup. A close
// supplies the user's explicit worktree decision instead.
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
	mutex := svc.worktreeRemovalLock(r.WorktreeID)
	mutex.Lock()
	defer mutex.Unlock()
	defer svc.worktreeLifecycle.remove(r.WorktreePath)()
	defer svc.lockGitIndex(r.RepoGitDir)()
	preserve := func(reason string, err error) {
		slog.Warn("automatic worktree rollback preserved its artifact", "worktree_path", r.WorktreePath, "reason", reason, "error", err)
	}
	if r.startup.preservesArtifact() {
		preserve("a lifecycle interruption owns the decision", nil)
		return
	}
	if r.initialBranchCommit == "" {
		preserve("initial commit evidence is missing", nil)
		return
	}
	if err := gitutil.ValidateBranchName(r.BranchName); err != nil {
		preserve("the created branch identity is invalid", err)
		return
	}
	verdict := svc.checkWorktreeRemovalEligibility(ctx, r.WorktreePath, r.WorktreePath, r.startup.key)
	if verdict.refusal != "" || verdict.err != nil || verdict.otherLiveTabs {
		preserve("the worktree is not exclusively eligible", verdict.err)
		return
	}
	info := verdict.info
	if !pathutil.SamePath(info.CommonDir, r.RepoGitDir) || info.BranchName != r.BranchName || info.HeadSHA != r.initialBranchCommit {
		preserve("repository, branch or commit changed after creation", nil)
		return
	}
	// Git can remove ignored files without --force. Include them in progress.
	status, err := gitutil.Output(ctx, r.WorktreePath, "status", "--porcelain", "--untracked-files=normal", "--ignored=matching", "--ignore-submodules=none")
	if err != nil || strings.TrimSpace(status) != "" {
		preserve("working-tree state is changed or unreadable", err)
		return
	}
	if reason, err := svc.worktreeRemovalBlockedReason(ctx, r.WorktreePath, info); err != nil || reason != "" {
		preserve("Git refuses worktree removal", err)
		return
	}
	wt, err := svc.createdWorktreeRecord(ctx, r)
	if err != nil {
		preserve("the tracking record changed or cannot be read", err)
		return
	}
	identity, err := svc.captureWorktreeRemovalIdentityLocked(ctx, wt, r.startup.key)
	if err != nil {
		preserve("worktree identity cannot be captured", err)
		return
	}
	defer releaseRemovalIdentityBestEffort(identity)
	journal := &memoryWorktreeRemovalJournal{identity: identity, checkpoint: &worktreeRemovalCheckpoint{
		WorktreeID: wt.ID, WorktreePath: wt.WorktreePath, RepoRoot: wt.RepoRoot, RepoGitDir: wt.RepoGitDir,
		Branch: r.BranchName, Commit: r.initialBranchCommit,
	}}
	// Automatic cleanup never authorizes Git to discard dirty or untracked
	// files. Native Git checks that refusal again at the actual removal.
	if r.startup.preservesArtifact() {
		preserve("a lifecycle interruption owns the decision", nil)
		return
	}
	if err := svc.removeWorktreeFromDiskRecordedLocked(ctx, wt, journal, []string{"worktree", "remove", wt.WorktreePath}); err != nil {
		preserve("native removal did not complete", err)
	}
}

func (svc *Service) createdWorktreeRecord(ctx context.Context, r rollbackWorktree) (db.Worktree, error) {
	if r.WorktreeID == "" {
		return db.Worktree{WorktreePath: r.WorktreePath, RepoRoot: r.RepoRoot, RepoGitDir: r.RepoGitDir, BranchName: r.BranchName}, nil
	}
	stored, err := svc.Queries.GetWorktreeByID(ctx, r.WorktreeID)
	if err != nil {
		return db.Worktree{}, err
	}
	if stored.DeletedAt.Valid || !pathutil.SamePath(stored.WorktreePath, r.WorktreePath) || !pathutil.SamePath(stored.RepoGitDir, r.RepoGitDir) {
		return db.Worktree{}, errors.New("the created worktree tracking identity changed")
	}
	return stored, nil
}

func (svc *Service) rollbackCreatedBranch(r rollbackBranch) {
	ctx := bgCtx()
	defer svc.lockGitIndex(r.RepoGitDir)()
	if err := gitutil.ValidateBranchName(r.CreatedBranch); err != nil {
		slog.Warn("branch rollback has an invalid branch identity; keeping git state", "error", err)
		return
	}
	current, err := queryGitPathInfo(ctx, r.WorkingDir)
	if err != nil || !pathutil.SamePath(current.CommonDir, r.RepoGitDir) {
		slog.Warn("branch rollback refused a changed or unreadable repository", "working_dir", r.WorkingDir, "error", err)
		return
	}
	if r.initialBranchCommit == "" {
		slog.Warn("branch rollback has no initial commit evidence; keeping git state", "branch", r.CreatedBranch)
		return
	}
	repository := gitutil.Repository{GitDir: r.RepoGitDir}
	reference := "refs/heads/" + r.CreatedBranch
	tip, err := repository.Output(ctx, "rev-parse", "--verify", reference+"^{commit}")
	if err != nil || strings.TrimSpace(tip) != r.initialBranchCommit {
		slog.Warn("branch changed after creation or cannot be read; keeping its checkout and ref", "branch", r.CreatedBranch, "error", err)
		return
	}
	// Preserve a later successful checkout. If restoration itself fails after
	// changing HEAD, owned-ref cleanup must still inspect whether deletion is safe.
	var restoreErr error
	if current.BranchName == r.CreatedBranch {
		if r.OriginalDetached {
			restoreErr = gitutil.Run(ctx, r.WorkingDir, "checkout", "--detach", r.OriginalCommit)
		} else {
			restoreErr = gitutil.Run(ctx, r.WorkingDir, "checkout", r.OriginalBranch)
		}
		if restoreErr != nil {
			slog.Warn("failed to restore checkout before branch rollback", "working_dir", r.WorkingDir, "error", restoreErr)
		}
	}
	if err := deleteUnusedBranchAtCommit(ctx, repository, r.CreatedBranch, r.initialBranchCommit); err != nil {
		slog.Warn("branch rollback cleanup did not complete", "branch", r.CreatedBranch, "error", err)
	}
}

// worktreeTabUserID returns the user_id a worktree_tabs row carries for a tab
// of this type. It is the single definition of that convention, shared by
// every by-tab read and delete so they cannot drift from what
// registerTabForWorktree / linkTabToWorktree wrote.
//
// FILE and IMAGE links carry the owning user because their tab ids are unique
// only within a user (worker_tab_payloads is keyed by (user_id, tab_id));
// AGENT/TERMINAL ids are globally unique, so their links carry "" and callers
// that only have an agent/terminal in hand need not know the user at all.
// Normalizing here -- rather than at each call site -- means passing the
// authenticated user for an AGENT close is harmless instead of a silent
// no-match.
// No `default:` case, so `exhaustive` (enabled in .golangci.yml) names this site
// when a tab kind is added. It has to: linkTabToWorktree writes the real owner
// for every payload-backed kind, and a new kind that silently fell to a default
// would read back "" -- GetWorktreeForTab then misses, and a requested
// WORKTREE_ACTION_REMOVE degrades to KEEP with nothing reported.
func worktreeTabUserID(tabType leapmuxv1.TabType, userID string) string {
	switch tabType {
	case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
		return userID
	case leapmuxv1.TabType_TAB_TYPE_AGENT,
		leapmuxv1.TabType_TAB_TYPE_TERMINAL,
		leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED:
		return ""
	}
	return ""
}

// unregisterTab drops a tab's worktree association row. Worktree
// deletion itself is driven by the close handler when the caller
// passed WorktreeAction_REMOVE and no other tabs remain.
//
// The REMOVE-close path uses removeTabFromWorktreeContext
// so it can guard the delete by worktree_id (defense-in-depth against a
// stale association). Other callers don't have the worktree in hand and
// would pay for a GetWorktreeForTab round-trip; the (tab_type, tab_id,
// user_id) delete is enough, because a tab belongs to at most one worktree in
// practice.
//
// userID is normalized through worktreeTabUserID, so AGENT/TERMINAL callers
// may pass either the authenticated user or "".
func (svc *Service) unregisterTab(tabType leapmuxv1.TabType, tabID, userID string) {
	if err := svc.unregisterTabContext(bgCtx(), tabType, tabID, userID); err != nil {
		slog.Warn("remove closed worktree tab link failed", "tab_type", tabType, "tab_id", tabID, "error", err)
	}
}
func (svc *Service) unregisterTabContext(ctx context.Context, tabType leapmuxv1.TabType, tabID, userID string) error {
	return svc.Queries.DeleteWorktreeTabsByTabID(ctx, db.DeleteWorktreeTabsByTabIDParams{TabType: tabType, TabID: tabID, UserID: worktreeTabUserID(tabType, userID)})
}

// removeTabFromWorktreeContext deletes only the association captured for this worktree.
func (svc *Service) removeTabFromWorktreeContext(ctx context.Context, tabType leapmuxv1.TabType, tabID, userID, worktreeID string) error {
	if err := svc.Queries.RemoveWorktreeTab(ctx, db.RemoveWorktreeTabParams{
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

// notGitRepoErr retains the diagnostic for verified native worktree absence.
// Config, I/O, policy and cleanup failures never receive this classification.
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

// unwrappedDiagnostic returns the original diagnostic of verified native
// absence. A plain errNotGitRepo carries none. Response builders forward the
// diagnostic as error_hint.
func unwrappedDiagnostic(err error) string {
	var n *notGitRepoErr
	if errors.As(err, &n) && n != nil && n.underlying != nil {
		return n.underlying.Error()
	}
	return ""
}

// deleteUnusedBranchAtCommit retains branch -D's worktree guard and config cleanup,
// while the atomic expected-value delete preserves a ref whose tip changed.
func deleteUnusedBranchAtCommit(ctx context.Context, repository gitutil.Repository, branch, commit string) error {
	if err := gitutil.ValidateBranchName(branch); err != nil {
		return err
	}
	if commit == "" {
		return errors.New("branch deletion requires an expected commit")
	}
	inUse, err := repository.IsBranchInUse(ctx, branch)
	if err != nil {
		return err
	}
	if inUse {
		return fmt.Errorf("branch %q is still checked out in a worktree", branch)
	}
	if err := repository.Run(ctx, "update-ref", "--no-deref", "-d", "refs/heads/"+branch, commit); err != nil {
		return err
	}
	if err := repository.Run(ctx, "config", "--remove-section", "branch."+branch); err != nil {
		native := nativeGitExit(err)
		missing := "fatal: no such section: branch." + branch + "\n"
		if native == nil || native.ExitCode() != 128 || string(native.Stderr) != missing {
			return err
		}
	}
	return nil
}
