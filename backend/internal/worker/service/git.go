package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/pathutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/validate"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"
)

// pushBranchTimeout caps the worker-side git push (and its preceding
// `add` / `commit -m WIP`) so a credential helper, hung SSH passphrase
// prompt, or unreachable remote can't leak a git subprocess after the
// frontend has already given up (frontend RPC deadline is ~10s by
// default). 60s leaves margin for slow remotes while still being short
// enough that a hung helper bites cleanly.
const pushBranchTimeout = 60 * time.Second

// branchMutationTimeout bounds local-only branch-mutation subprocesses
// (`git checkout`, `git checkout -b`, `git branch -D`) so a stuck hook
// or held lockfile can't leak past the frontend's RPC deadline. These
// don't hit the network, so 30s is plenty.
const branchMutationTimeout = 30 * time.Second

// gitReadTimeout bounds read-only git probes (rev-parse, for-each-ref,
// worktree list, diff stats, push status) used by dialog-open handlers.
// Belt-and-braces with the inbound RPC ctx (now plumbed through the
// dispatcher): a client disconnect cancels the subprocess via ctx, and
// a wedged probe on a still-open channel hits the timeout. 30s is
// enough headroom for slow first probes on large repos while still
// bounding the worst case.
const gitReadTimeout = 30 * time.Second

// wipCommitMessage is the placeholder commit message used to sweep up
// uncommitted changes before `pushBranch` runs `git push`. Centralised so
// tests and future call sites grep for the same string.
const wipCommitMessage = "WIP"

// runBranchMutation runs a local-only branch-mutation subprocess under
// branchMutationTimeout and maps the result to an RPC response. On
// success, `success` is sent back via sendProtoResponse. Anything
// wrapping gitutil.ErrInvalidArgument (including gitutil.BranchNameError
// via its Unwrap) becomes InvalidArgument; everything else becomes
// Internal.
//
// `parent` MUST be a detached background ctx (typically bgCtx()), NOT
// the inbound RPC ctx. A client disconnect mid-mutation would SIGKILL
// `git checkout` / `git branch -D` halfway through and leave the
// working tree on an inconsistent state (half-moved HEAD, dangling
// branch ref). The 30s branchMutationTimeout applied here is the only
// bound; callers detach the parent on purpose so the mutation runs to
// completion or its own timeout, regardless of client liveness.
//
// Use runBranchMutationCustom when the operation chains multiple git
// subprocesses (e.g. deleteBranchInDir's checkout + branch -D pair):
// sharing branchMutationTimeout across both phases lets the first one
// drain the budget and leave the second with no time to run.
func runBranchMutation(parent context.Context, sender channel.ResponseWriter, success proto.Message, fn func(ctx context.Context) error) {
	runBranchMutationCustom(parent, branchMutationTimeout, sender, success, fn)
}

// runBranchMutationCustom is runBranchMutation with a caller-supplied
// outer timeout. Multi-phase mutations pass a budget large enough that
// each phase can apply its own per-subprocess timeout internally
// without being starved by sibling phases.
func runBranchMutationCustom(parent context.Context, timeout time.Duration, sender channel.ResponseWriter, success proto.Message, fn func(ctx context.Context) error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if err := fn(ctx); err != nil {
		if errors.Is(err, gitutil.ErrInvalidArgument) {
			sendInvalidArgument(sender, err.Error())
		} else {
			sendInternalError(sender, err.Error())
		}
		return
	}
	sendProtoResponse(sender, success)
}

// registerGitHandlers registers handlers for git operations on the local filesystem.
func registerGitHandlers(d ownerOnlyRegistrar, svc *Service) {
	d.Register("GetGitInfo", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.GetGitInfoRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
		defer cancel()
		resp := &leapmuxv1.GetGitInfoResponse{}

		// Path-info, origin URL, and dirty status all race in parallel.
		// `git -C <dir>` resolves to the repo root internally, so none of
		// these probes depends on the rev-parse result to start. Origin
		// and dirty are best-effort: failure leaves the field at its
		// proto zero value, matching the prior single-probe semantics.
		var (
			info      *gitPathInfo
			infoErr   error
			originURL string
			dirty     bool
		)
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error {
			info, infoErr = queryGitPathInfo(gctx, dirPath)
			return nil
		})
		g.Go(func() error {
			originURL = gitutil.GetOriginURL(gctx, dirPath)
			return nil
		})
		g.Go(func() error {
			if d, err := isDirty(gctx, dirPath); err == nil {
				dirty = d
			}
			return nil
		})
		_ = g.Wait()

		if infoErr != nil {
			// errNotGitRepo (with or without an underlying-stderr
			// wrap) is "git can't classify this as a work tree" —
			// surface as IsGitRepo=false. Two sub-cases:
			//   * Bare errNotGitRepo: the path genuinely isn't a repo.
			//     error_hint stays empty; the dialog renders the
			//     non-git options without a banner.
			//   * notGitRepoErr wrapping a real diagnostic (dubious-
			//     ownership, EACCES, corrupted config): error_hint
			//     carries git's stderr so the dialog renders an
			//     inline warning the user can act on.
			// Ctx-cancel / DeadlineExceeded are real probe failures,
			// not "this isn't a repo" — those still propagate as
			// Internal so a hung dialog surfaces instead of silently
			// degrading to a non-git UX.
			if errors.Is(infoErr, errNotGitRepo) {
				if hint := unwrappedDiagnostic(infoErr); hint != "" {
					resp.ErrorHint = hint
				}
				sendProtoResponse(sender, resp)
				return
			}
			sendInternalError(sender, infoErr.Error())
			return
		}
		canonDirPath := pathutil.Canonicalize(dirPath)
		resp.IsGitRepo = true
		resp.RepoRoot = info.RepoRoot
		resp.RepoDirName = filepath.Base(info.RepoRoot)
		resp.IsRepoRoot = pathutil.SamePath(canonDirPath, info.RepoRoot)
		resp.IsWorktree = info.IsWorktree
		if info.IsWorktree {
			resp.IsWorktreeRoot = pathutil.SamePath(canonDirPath, info.TopLevel)
		}
		resp.CurrentBranch = branchOrShortSHA(info)
		resp.OriginUrl = originURL
		resp.IsDirty = dirty

		sendProtoResponse(sender, resp)
	})

	d.Register("GetGitFileStatus", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.GetGitFileStatusRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
		defer cancel()

		// Path-info, file status, and origin URL all race in parallel.
		// `git -C <dir>` (used by getGitFileStatusEntries / GetOriginURL)
		// resolves to the repo root internally, so neither probe needs
		// the path-info result to start.
		var (
			info      *gitPathInfo
			infoErr   error
			files     []*leapmuxv1.GitFileStatusEntry
			originURL string
		)
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error {
			info, infoErr = queryGitPathInfo(gctx, dirPath)
			return nil
		})
		g.Go(func() error {
			// getGitFileStatusEntries treats a git failure as an empty
			// status (best-effort sidebar refresh), so its returned
			// error is intentionally discarded here.
			files, _ = getGitFileStatusEntries(gctx, dirPath)
			return nil
		})
		g.Go(func() error {
			originURL = gitutil.GetOriginURL(gctx, dirPath)
			return nil
		})
		_ = g.Wait()

		if infoErr != nil {
			if errors.Is(infoErr, errNotGitRepo) {
				// Same UX rule as GetGitInfo above: render the
				// non-git fallback with error_hint populated when
				// the worker DID see a diagnostic but git refused
				// to classify the path. Hard errors stay on the
				// sendInternalError path so a hung probe surfaces.
				resp := &leapmuxv1.GetGitFileStatusResponse{}
				if hint := unwrappedDiagnostic(infoErr); hint != "" {
					resp.ErrorHint = hint
				}
				sendProtoResponse(sender, resp)
				return
			}
			sendInternalError(sender, infoErr.Error())
			return
		}
		// queryGitPathInfo canonicalises with pathutil.Canonicalize, which
		// uses forward-slash separators. The frontend matches this path
		// against agent.workingDir (native separators), so normalize.
		// `repo_root` is intentionally the main repo root (not TopLevel)
		// — the sidebar groups worktree tabs with their parent repo so
		// the user sees a single "repo" node. `is_worktree` describes the
		// QUERIED dirPath (not repo_root), so consumers must not mass-stamp
		// it onto every tab in the repo — see syncGitStatusToTabs.
		//
		// `toplevel` is the working-tree root the caller queried (worktree
		// root for an in-worktree path, repo root otherwise). The frontend
		// uses it as the stamping identity so a worktree refresh updates
		// only the worktree's tabs — not every tab whose gitToplevel
		// happens to equal repo_root. Without this, switching focus to a
		// freshly-created worktree's agent stamps the worktree's branch
		// onto every main-tree tab in the same repo.
		sendProtoResponse(sender, &leapmuxv1.GetGitFileStatusResponse{
			RepoRoot:      pathutil.NormalizeNative(info.RepoRoot),
			Files:         files,
			CurrentBranch: branchOrShortSHA(info),
			OriginUrl:     originURL,
			IsWorktree:    info.IsWorktree,
			Toplevel:      pathutil.NormalizeNative(info.TopLevel),
		})
	})

	d.Register("ReadGitFile", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.ReadGitFileRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		absPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
		defer cancel()

		// Resolve repo root from the file's directory.
		fileDir := filepath.Dir(absPath)
		repoRoot, err := gitutil.Output(ctx, fileDir, "rev-parse", "--show-toplevel")
		if err != nil {
			sendInternalError(sender, "not a git repository")
			return
		}
		repoRoot = strings.TrimSpace(repoRoot)

		// Compute relative path from repo root.
		relPath, err := filepath.Rel(repoRoot, absPath)
		if err != nil {
			sendInternalError(sender, "failed to compute relative path")
			return
		}

		// Determine the git ref string. Unspecified / unknown values
		// reject explicitly — falling back to HEAD would mask a future
		// enum addition that callers haven't been taught about yet.
		var refStr string
		switch r.GetRef() {
		case leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD:
			refStr = "HEAD"
		case leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED:
			refStr = ":0"
		default:
			sendInvalidArgument(sender, "ref is required")
			return
		}

		// Run git show <ref>:<relative_path>.
		showArg := refStr + ":" + relPath
		content, err := gitutil.Bytes(ctx, repoRoot, "show", showArg)
		if err != nil {
			// File may not exist in the given ref.
			sendProtoResponse(sender, &leapmuxv1.ReadGitFileResponse{
				Path:   absPath,
				Exists: false,
			})
			return
		}

		sendProtoResponse(sender, &leapmuxv1.ReadGitFileResponse{
			Path:    absPath,
			Content: content,
			Exists:  true,
		})
	})

	d.Register("ListGitBranches", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.ListGitBranchesRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
		defer cancel()

		info, err := queryGitPathInfo(ctx, dirPath)
		if err != nil {
			sendInternalError(sender, "not a git repository")
			return
		}

		branches, err := listGitBranches(ctx, info.RepoRoot)
		if err != nil {
			sendInternalError(sender, "failed to list branches")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.ListGitBranchesResponse{
			Branches:      branches,
			CurrentBranch: branchOrShortSHA(info),
		})
	})

	d.Register("ListGitWorktrees", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.ListGitWorktreesRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
		defer cancel()
		worktrees, err := listGitWorktrees(ctx, dirPath)
		if err != nil {
			sendInternalError(sender, "failed to list worktrees: "+err.Error())
			return
		}

		sendProtoResponse(sender, &leapmuxv1.ListGitWorktreesResponse{
			Worktrees: worktrees,
		})
	})

	d.RegisterTracked("InspectLastTabClose", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.InspectLastTabCloseRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		// Tracked via dispatcher RegisterTracked above: the dispatcher
		// Add(1)s the bound Cleanup WaitGroup synchronously BEFORE the
		// handler goroutine launches, so Shutdown.Wait can't slip past
		// an in-flight inspect (DB read + errgroup of git probes).
		traceTabClosePhase("inspect", r.GetTabId(), "handler_begin")
		ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
		defer cancel()
		resp, err := svc.inspectLastTabClose(ctx, r.GetTabType(), r.GetTabId())
		if err != nil {
			slog.Error("inspect last tab close failed", "tab_type", r.GetTabType(), "tab_id", r.GetTabId(), "error", err)
			traceTabClosePhase("inspect", r.GetTabId(), "handler_error")
			sendInternalError(sender, err.Error())
			return
		}
		traceTabClosePhase("inspect", r.GetTabId(), "handler_end")
		sendProtoResponse(sender, resp)
	})

	d.RegisterTracked("PushBranch", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.PushBranchRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		// Tracked via dispatcher RegisterTracked above: Shutdown.Wait
		// drains the git add/commit/push chain. A half-applied push is
		// observable in the repo until the next manual `git push`, so
		// abandoning it on Shutdown leaves the user with a confusing
		// "is my work pushed?" state.

		// Detach from the inbound RPC ctx and bound on a fresh timeout.
		// pushBranch runs `git add -A` → `git commit -m WIP` → `git push`
		// as a single conceptual mutation; cancelling between the
		// `commit` and the `push` would leave an unpushed WIP commit
		// the user expected to be on the remote. Hence bgCtx() + the
		// RegisterTracked gate (the shared rationale for every tracked
		// git mutation, documented on RegisterTracked).
		// A stuck credential helper or SSH passphrase prompt is still
		// killed at pushBranchTimeout.
		ctx, cancel := context.WithTimeout(bgCtx(), pushBranchTimeout)
		defer cancel()
		if err := svc.pushBranch(ctx, r.GetTabType(), r.GetTabId()); err != nil {
			sendInternalError(sender, err.Error())
			return
		}
		sendProtoResponse(sender, &leapmuxv1.PushBranchResponse{})
	})

	d.Register("InspectBranchDeletion", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.InspectBranchDeletionRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		// Read-only handler: skip Cleanup.Add so Shutdown doesn't block
		// on in-flight dialog-open probes (mirrors GetGitInfo /
		// GetGitFileStatus). The dispatcher ctx already cancels the
		// subprocesses on channel teardown.
		ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
		defer cancel()
		resp, err := svc.inspectBranchDeletion(ctx, dirPath, r.GetBranchNameHint())
		if err != nil {
			slog.Error("inspect branch deletion failed", "path", dirPath, "error", err)
			sendInternalError(sender, err.Error())
			return
		}
		sendProtoResponse(sender, resp)
	})

	d.Register("InspectBranchChange", func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.InspectBranchChangeRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		// Read-only handler: skip Cleanup.Add for the same reason as
		// InspectBranchDeletion above.
		ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
		defer cancel()
		resp, err := svc.inspectBranchChange(ctx, dirPath)
		if err != nil {
			slog.Error("inspect branch change failed", "path", dirPath, "error", err)
			sendInternalError(sender, err.Error())
			return
		}
		sendProtoResponse(sender, resp)
	})

	d.RegisterTracked("CheckoutBranch", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.CheckoutBranchRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		// Tracked via dispatcher RegisterTracked above so Shutdown
		// waits for the in-flight `git checkout` to finish. A SIGTERM
		// mid-checkout would otherwise leave the working tree in
		// whatever half-applied state git got to.

		// Destructive mutation — explicitly detach from the inbound RPC
		// ctx (which cancels on channel close / dialog dismissal). A
		// SIGKILL mid-`git checkout` from a client disconnect would
		// leave the working tree half-updated (some files at the new
		// branch's content, some at the old, index in a partial state).
		// PushBranch and DeleteBranch mitigate the same hazard the same
		// way; CheckoutBranch is the parallel case. branchMutationTimeout
		// still bounds the operation.
		runBranchMutation(bgCtx(), sender, &leapmuxv1.CheckoutBranchResponse{}, func(ctx context.Context) error {
			return checkoutBranchInDir(ctx, dirPath, r.GetBranch())
		})
	})

	d.RegisterTracked("CreateBranch", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.CreateBranchRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		// Tracked via dispatcher RegisterTracked above: see
		// CheckoutBranch for the rationale.

		// Destructive mutation — see CheckoutBranch above for the
		// rationale. `git checkout -b` writes refs/heads/<new> before
		// the checkout step, so a cancel between them would leave a
		// branch ref alive on a working tree the user thought they
		// rolled back from.
		runBranchMutation(bgCtx(), sender, &leapmuxv1.CreateBranchResponse{}, func(ctx context.Context) error {
			return createBranchInDir(ctx, dirPath, r.GetNewBranch(), r.GetBaseBranch())
		})
	})

	d.RegisterTracked("DeleteBranch", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var r leapmuxv1.DeleteBranchRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := validate.SanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		// Tracked via dispatcher RegisterTracked above so Shutdown
		// waits for the checkout + branch -D pair to complete rather
		// than returning between the two.

		// Destructive two-step mutation (checkout switch-to → `git branch
		// -D`). Use bgCtx() rather than the inbound request ctx so a
		// dialog dismissed between the steps can't leave the working
		// directory parked on `switchTo` with `branchToDelete` still
		// alive — the shared rationale for the RegisterTracked gate above.
		// Give both phases room to apply their own branchMutationTimeout
		// internally rather than starving the second phase by sharing a
		// single budget with the first.
		runBranchMutationCustom(bgCtx(), 2*branchMutationTimeout, sender, &leapmuxv1.DeleteBranchResponse{}, func(ctx context.Context) error {
			return deleteBranchInDir(ctx, dirPath, r.GetBranchToDelete(), r.GetSwitchToBranch())
		})
	})
}

type tabGitContext struct {
	workingDir   string
	repoRoot     string
	branchName   string
	worktreeRoot string
	worktreeID   string
	isWorktree   bool
}

func (t *tabGitContext) commitDir() string {
	if t.isWorktree && t.worktreeRoot != "" {
		return t.worktreeRoot
	}
	return t.repoRoot
}

func (svc *Service) inspectLastTabClose(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (*leapmuxv1.InspectLastTabCloseResponse, error) {
	trace := func(phase string) { traceTabClosePhase("inspect", tabID, phase) }

	// Fast path: the only reason to run the expensive git subprocesses
	// (diffStatsForPath, pushStatusForPath) is to populate the dialog.
	// If the tab count already tells us no dialog will be shown, we can
	// answer from DB alone and skip every git call below.
	//
	// - Worktree tab with siblings (CountWorktreeTabs > 1) → no prompt.
	//   Answer entirely from the worktree DB row; don't touch git.
	// - Non-worktree tab with other tabs on the same branch (otherTabs
	//   > 0) → no prompt. We still need the branch name to do the
	//   sibling count, so loadTabGitContext still runs; but diff/push
	//   are skipped.
	//
	// When shouldPrompt is false, the frontend ignores diff_* / push_*
	// fields, so leaving them zero is safe.
	var tabCtx *tabGitContext
	if wt, err := svc.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
		TabType: tabType,
		TabID:   tabID,
	}); err == nil {
		count, countErr := svc.Queries.CountWorktreeTabs(ctx, wt.ID)
		trace("worktree_count_done")
		if countErr == nil && count > 1 {
			// Sibling tabs still hold the worktree, so the close will
			// not prompt. Re-probe HEAD before sending the response:
			// the worktrees.branch_name row reflects whatever branch
			// the worktree was created on, but an external `git
			// checkout` (terminal, IDE, sibling agent) can change it,
			// and the dialog-less CLI close inspector relies on this
			// label to render its banner.
			//
			// Fall back to wt.BranchName on ANY queryGitPathInfo
			// failure (not just errNotGitRepo): inspectLastTabClose's
			// output is read-only — the response only RENDERS the
			// branch name in the close-dialog; nothing destructive
			// keys off it. A transient probe failure (slow disk,
			// permission flicker, ctx-near-deadline) used to hard-
			// block the close path with InternalError, leaving the
			// user with "Worker is unreachable" toasts when a stale
			// label would have been a far better UX. The mutation
			// helpers (removeWorktreeFromDisk) STILL refuse the
			// fallback because their `git branch -D <stale>` would
			// touch a real branch; the display surface here doesn't.
			branchName := wt.BranchName
			info, infoErr := queryGitPathInfo(ctx, wt.WorktreePath)
			if infoErr == nil {
				branchName = branchOrShortSHA(info)
			} else if !errors.Is(infoErr, errNotGitRepo) {
				slog.Warn("inspectLastTabClose fast-path: HEAD probe failed, falling back to DB-row branch name",
					"worktree_id", wt.ID, "worktree_path", wt.WorktreePath, "stale_db_branch", wt.BranchName, "error", infoErr)
			}
			trace("fast_path_taken")
			return &leapmuxv1.InspectLastTabCloseResponse{
				Target:       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
				ShouldPrompt: false,
				RepoRoot:     wt.RepoRoot,
				WorktreePath: wt.WorktreePath,
				WorktreeId:   wt.ID,
				BranchName:   branchName,
			}, nil
		}
		// The worktree DB row carries RepoRoot / WorktreePath which a
		// `git checkout` can't change. BranchName, however, can drift
		// (see comment above), so re-derive it from a single rev-parse
		// before populating tabCtx. The remaining work skipped vs.
		// loadTabGitContext is GetWorktreeByPath (DB) and an additional
		// queryGitPathInfo call's worktree-disposition derivation.
		//
		// On probe failure, fall back to wt.BranchName + log: this
		// path also drives display-side rendering (the prompt dialog
		// shows the branch label), not destructive action. The
		// stale-name hazard only matters for mutating helpers
		// (removeWorktreeFromDisk) which now correctly refuse the
		// fallback. Hard-blocking the inspect on a transient probe
		// error used to surface as "Worker unreachable" — a stale
		// label is the lesser evil.
		branchName := wt.BranchName
		info, infoErr := queryGitPathInfo(ctx, wt.WorktreePath)
		if infoErr == nil {
			branchName = branchOrShortSHA(info)
		} else if !errors.Is(infoErr, errNotGitRepo) {
			slog.Warn("inspectLastTabClose slow-path: HEAD probe failed, falling back to DB-row branch name",
				"worktree_id", wt.ID, "worktree_path", wt.WorktreePath, "stale_db_branch", wt.BranchName, "error", infoErr)
		}
		tabCtx = &tabGitContext{
			workingDir:   wt.WorktreePath,
			repoRoot:     wt.RepoRoot,
			branchName:   branchName,
			worktreeRoot: wt.WorktreePath,
			worktreeID:   wt.ID,
			isWorktree:   true,
		}
		trace("git_ctx_synth")
	}

	if tabCtx == nil {
		loaded, err := svc.loadTabGitContext(ctx, tabType, tabID)
		trace("git_ctx_done")
		if err != nil {
			// If the tab's working directory is not (or no longer) a git repository,
			// there is nothing to prompt about — allow the tab to close normally.
			return &leapmuxv1.InspectLastTabCloseResponse{
				Target: leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_NONE,
			}, nil
		}
		tabCtx = loaded
	}

	resp := &leapmuxv1.InspectLastTabCloseResponse{
		Target:     leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_NONE,
		RepoRoot:   tabCtx.repoRoot,
		BranchName: tabCtx.branchName,
	}

	// Non-worktree fast path: if there are sibling tabs on the same
	// branch, we'll never prompt regardless of git state — skip diff
	// and push.
	if !tabCtx.isWorktree {
		hasOther, err := svc.hasOtherNonWorktreeTabOnBranch(ctx, tabType, tabID, tabCtx.repoRoot, tabCtx.branchName)
		trace("branch_count_done")
		if err != nil {
			return nil, err
		}
		if hasOther {
			trace("fast_path_taken")
			return resp, nil
		}
	}

	snap, err := gatherBranchSnapshot(ctx, tabCtx.commitDir(), tabCtx.branchName)
	if err != nil {
		return nil, err
	}
	// Single emission covers both parallel calls completing — their
	// individual timestamps are no longer meaningful once they race.
	trace("diff_and_push_done")
	resp.GitState = snap.toProto(tabCtx.branchName)

	if tabCtx.isWorktree {
		// If we reached here, the worktree fast path didn't apply
		// (GetWorktreeForTab miss or count <= 1). The last-tab case is
		// the only reason we still need worktree details in the
		// response.
		resp.Target = leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE
		resp.ShouldPrompt = true
		resp.WorktreePath = tabCtx.worktreeRoot
		resp.WorktreeId = tabCtx.worktreeID
		return resp, nil
	}

	// Non-worktree, last tab on branch: prompt only if the branch has
	// state the user might lose.
	if snap.hasChanges || snap.push.Unpushed > 0 || snap.push.RemoteMissing {
		resp.Target = leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_BRANCH
		resp.ShouldPrompt = true
	}

	return resp, nil
}

// inspectBranchDeletion gathers the git state needed to render the Delete
// Branch confirmation dialog. Always returns full info — there's no
// fast-path skip like inspectLastTabClose has, because the caller always
// shows the dialog. Returns the branch picker's switch-to candidates
// alongside the snapshot when the dialog will render the non-worktree
// variant, so the dialog needs only one RPC at open time.
//
// `branchNameHint` is the branch label the caller already knows (the row
// the user opened the dialog from). When non-empty, queryGitPathInfo runs
// in parallel with the snapshot since the snapshot already has the
// branch name; when empty, queryGitPathInfo must finish first so we can
// derive both the branch name and the worktree-aware statsPath before
// dispatching the snapshot.
//
// listGitBranches always runs concurrently with the path probe and the
// snapshot so the response is correct regardless of the path's worktree
// disposition. The non-worktree response uses Branches for the dialog's
// switch-target picker; the worktree response strips Branches because
// the worktree dialog renders no picker. Both paths are bounded by
// max(probe, snapshot, branches) instead of summing them.
func (svc *Service) inspectBranchDeletion(ctx context.Context, dirPath, branchNameHint string) (*leapmuxv1.InspectBranchDeletionResponse, error) {
	var (
		info        *gitPathInfo
		infoErr     error
		snap        branchSnapshot
		branches    []*leapmuxv1.GitBranchEntry
		branchesErr error
	)

	// The hint is used only to start the snapshot in parallel with the
	// path probe — the response branchName is always derived from
	// queryGitPathInfo. A stale hint (e.g. the sidebar row was cached
	// while the worktree was on 'feature-A' but an external `git
	// checkout feature-B` has since switched it) would otherwise make
	// the snapshot's UpstreamExists/RemoteBranchMissing/Unpushed reflect
	// a different branch than the dialog actually renders.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		// Capture the error without propagating it through the errgroup:
		// the worktree response variant discards Branches anyway, so a
		// transient for-each-ref failure shouldn't block a dialog whose
		// rendered shape doesn't need the list. The check below routes
		// the error only when the response actually needs branches.
		b, err := listGitBranches(gctx, dirPath)
		if err != nil {
			branchesErr = err
			return nil
		}
		branches = b
		return nil
	})
	if branchNameHint != "" {
		// Fast path: the hint lets the snapshot start without waiting
		// for queryGitPathInfo. If queryGitPathInfo returns a different
		// branch name OR a different statsPath we DROP the hint-derived
		// snapshot below and re-probe with the authoritative inputs.
		// This races but doesn't race-window: the dialog already locks
		// the user out of the repo (no actions fire until the response
		// lands).
		//
		// Canonicalize dirPath up front so the hint snapshot runs from
		// the same path string the post-wait gate compares against
		// (snapshotStatsPath also returns a canonical path for the
		// non-worktree case). Without this both arms diverge on a
		// symlinked ancestor — `/tmp` -> `/private/tmp` on macOS — and
		// the gate fires every dialog open even when the hint was
		// correct, doubling open latency.
		hintSnapshotPath := pathutil.Canonicalize(dirPath)
		g.Go(func() error {
			info, infoErr = queryGitPathInfo(gctx, dirPath)
			// Surface the raw error to the errgroup so a ctx.Canceled
			// (propagated by a sibling goroutine's failure) doesn't get
			// masked as errNotGitRepo. The g.Wait() handler below routes
			// the user-visible message based on whether infoErr is truly
			// errNotGitRepo — anything else (DeadlineExceeded, ctx-cancel
			// from a sibling, permission) must surface as itself.
			return infoErr
		})
		g.Go(func() error {
			s, err := gatherBranchSnapshot(gctx, hintSnapshotPath, branchNameHint)
			if err != nil {
				return err
			}
			snap = s
			return nil
		})
	} else {
		// queryGitPathInfo must land before gatherBranchSnapshot — the
		// branch name and (for worktrees) the statsPath come from it.
		// Both run inside this single goroutine so the errgroup still
		// races them against the branches probe above.
		g.Go(func() error {
			info, infoErr = queryGitPathInfo(gctx, dirPath)
			if infoErr != nil {
				// See the hint-arm goroutine above: returning the raw
				// error keeps ctx.Canceled and friends distinguishable
				// from errNotGitRepo at g.Wait().
				return infoErr
			}
			name := branchOrShortSHA(info)
			s, err := gatherBranchSnapshot(gctx, snapshotStatsPath(info, dirPath), name)
			if err != nil {
				return err
			}
			snap = s
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		// Only the canonical "git says this isn't a work tree" signal
		// (errNotGitRepo) maps to the user-facing "not a git repository".
		// A ctx.Canceled / DeadlineExceeded / permission error stored in
		// infoErr by a sibling-cancelled goroutine must NOT be conflated
		// with that — earlier revisions wrapped every queryGitPathInfo
		// failure as errNotGitRepo, which silently turned dialog
		// dismissals and probe timeouts into "this isn't a repo" toasts
		// against real repos.
		if errors.Is(infoErr, errNotGitRepo) {
			return nil, errors.New("not a git repository")
		}
		return nil, err
	}

	// Re-probe the snapshot when the hint disagrees with the
	// authoritative info — either the branch name has changed since the
	// sidebar row was cached, or the hint path used dirPath while the
	// authoritative statsPath is info.TopLevel (worktree variant).
	// Either disagreement means the hint-derived snapshot reflects a
	// different shape than the dialog will render.
	//
	// Canonicalize dirPath before comparing against statsPath:
	// snapshotStatsPath returns info.TopLevel for a worktree, and
	// queryGitPathInfo runs pathutil.Canonicalize on its rev-parse
	// output (forward slashes on Windows, symlink-resolved on POSIX).
	// Without the canonicalize step the gate spuriously fires on every
	// Windows worktree open because `C:/Users/me/wt` is never == raw
	// `C:\Users\me\wt`, defeating the parallel hint snapshot.
	branchName := branchOrShortSHA(info)
	statsPath := snapshotStatsPath(info, dirPath)
	hintPath := pathutil.Canonicalize(dirPath)
	if branchNameHint != "" && (branchNameHint != branchName || statsPath != hintPath) {
		s, err := gatherBranchSnapshot(ctx, statsPath, branchName)
		if err != nil {
			return nil, err
		}
		snap = s
	}

	// Surface the branches error ONLY when the response variant needs
	// the list. Worktree dialogs render no picker and strip Branches
	// from the response, so a transient for-each-ref failure on a
	// worktree path shouldn't block the dialog from opening.
	if branchesErr != nil {
		if !info.IsWorktree {
			return nil, branchesErr
		}
		// Log the swallowed error so a future call site that adds a
		// branch picker to the worktree variant (or an operator
		// triaging "no branches" reports against a corrupted-refs
		// repo) has a breadcrumb instead of silent zero. Warn-level
		// because the dialog still opens correctly; the user just
		// loses the branch list this one render.
		slog.Warn("inspectBranchDeletion: branches probe failed on worktree path; response omits branches",
			"dir", dirPath, "error", branchesErr)
	}

	resp := &leapmuxv1.InspectBranchDeletionResponse{
		IsWorktree: info.IsWorktree,
		BranchName: branchName,
		GitState:   snap.toProto(branchName),
	}
	if info.IsWorktree {
		resp.WorktreePath = info.TopLevel
		// Look up the worktree DB row so the dialog can tell a tracked
		// worktree (has a row, so closing its tabs with REMOVE actually
		// deletes the dir) from an untracked one. Treat sql.ErrNoRows as
		// "untracked worktree" and return worktree_id="" so the dialog
		// toasts honestly instead of promising a removal the coupled
		// tab-close path can't make.
		// Surface every other lookup error so a transient DB failure
		// doesn't silently strip the id and force the fallback path.
		wt, wtErr := svc.Queries.GetWorktreeByPath(ctx, pathutil.Canonicalize(info.TopLevel))
		if wtErr == nil {
			resp.WorktreeId = wt.ID
		} else if !errors.Is(wtErr, sql.ErrNoRows) {
			return nil, wtErr
		}
	} else {
		// Worktree path renders no branch picker — saving the bytes.
		// Non-worktree path needs the candidates for the switch-to
		// dropdown; filtering out the doomed branch happens client-side.
		resp.Branches = branches
	}
	return resp, nil
}

// inspectBranchChange races the path-info probe, the dirty-tree probe,
// and the branches list in one errgroup. ChangeBranchDialog opens against
// a fixed (workerId, gitToplevel) and immediately needs all three —
// returning them in a single RPC drops the dialog-open round trip from
// two (GetGitInfo then ListGitBranches, each forking queryGitPathInfo
// server-side) to one shared rev-parse.
//
// Failure model mirrors inspectBranchDeletion: errNotGitRepo from the
// path probe surfaces as "not a git repository"; any other goroutine's
// failure aborts the rest via gctx and returns the raw error.
func (svc *Service) inspectBranchChange(ctx context.Context, dirPath string) (*leapmuxv1.InspectBranchChangeResponse, error) {
	var (
		info     *gitPathInfo
		infoErr  error
		branches []*leapmuxv1.GitBranchEntry
		dirty    bool
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		info, infoErr = queryGitPathInfo(gctx, dirPath)
		// Surface the raw error so ctx.Canceled (from a sibling failure)
		// stays distinguishable from errNotGitRepo at g.Wait(). The
		// wait-handler below maps errNotGitRepo to "not a git
		// repository" and surfaces everything else verbatim.
		return infoErr
	})
	g.Go(func() error {
		// `git for-each-ref` works from any subdir; passing dirPath lets
		// this race against queryGitPathInfo instead of waiting for the
		// resolved RepoRoot.
		b, err := listGitBranches(gctx, dirPath)
		if err != nil {
			return err
		}
		branches = b
		return nil
	})
	g.Go(func() error {
		// `git status --porcelain` works from any subdir too. Best-
		// effort: a probe failure leaves dirty=false, matching the
		// GetGitInfo handler's existing "swallow on error" semantics.
		if d, err := isDirty(gctx, dirPath); err == nil {
			dirty = d
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		// Only the canonical "git says this isn't a work tree" signal
		// (errNotGitRepo) maps to the user-facing "not a git
		// repository". A ctx.Canceled / DeadlineExceeded / permission
		// error stored in infoErr by a sibling-cancelled goroutine must
		// NOT be conflated with that — earlier revisions wrapped every
		// queryGitPathInfo failure as errNotGitRepo, which silently
		// turned dialog dismissals and probe timeouts into "this isn't
		// a repo" toasts against real repos.
		if errors.Is(infoErr, errNotGitRepo) {
			return nil, errors.New("not a git repository")
		}
		return nil, err
	}

	return &leapmuxv1.InspectBranchChangeResponse{
		RepoRoot:      info.RepoRoot,
		Toplevel:      info.TopLevel,
		IsWorktree:    info.IsWorktree,
		CurrentBranch: branchOrShortSHA(info),
		IsDirty:       dirty,
		Branches:      branches,
	}, nil
}

// snapshotStatsPath returns the directory `gatherBranchSnapshot` should
// probe diff/push stats from. For a linked worktree the snapshot must
// run from the worktree's own root (info.TopLevel) so the diff stats
// reflect that worktree's working copy rather than whatever subdir the
// dialog was opened against. The non-worktree case falls through to
// the caller's path verbatim.
// snapshotStatsPath returns the canonical path the snapshot goroutine
// should pass to `git -C ...`. Always canonical (forward slashes on
// Windows, symlink-resolved on POSIX) so the inspectBranchDeletion gate
// can compare it against `pathutil.Canonicalize(dirPath)` without
// spuriously firing on a symlinked ancestor (`/tmp` -> `/private/tmp`
// on macOS would otherwise re-probe every non-worktree dialog open).
func snapshotStatsPath(info *gitPathInfo, dirPath string) string {
	if info != nil && info.IsWorktree {
		// info.TopLevel was already canonicalized by queryGitPathInfo.
		return info.TopLevel
	}
	return pathutil.Canonicalize(dirPath)
}

// branchSnapshot bundles the diff/push state captured by gatherBranchSnapshot.
type branchSnapshot struct {
	diffAdded     int32
	diffDeleted   int32
	diffUntracked int32
	hasChanges    bool
	push          pushStatus
}

// toProto projects the snapshot onto the shared BranchGitState submessage
// that both InspectLastTabClose and InspectBranchDeletion responses embed.
func (s branchSnapshot) toProto(branchName string) *leapmuxv1.BranchGitState {
	return &leapmuxv1.BranchGitState{
		DiffAdded:             s.diffAdded,
		DiffDeleted:           s.diffDeleted,
		DiffUntracked:         s.diffUntracked,
		UnpushedCommitCount:   s.push.Unpushed,
		HasUncommittedChanges: s.hasChanges,
		UpstreamExists:        s.push.UpstreamExists,
		RemoteBranchMissing:   s.push.RemoteMissing,
		OriginExists:          s.push.OriginExists,
		CanPush:               s.push.CanPush(branchName),
	}
}

// gatherBranchSnapshot runs diffStatsForPath and pushStatusForPath
// concurrently against the same working directory. Both are independent git
// subprocesses, so the caller pays the max of the two, not the sum.
func gatherBranchSnapshot(ctx context.Context, statsPath, branchName string) (branchSnapshot, error) {
	var snap branchSnapshot
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		a, d, u, hc, err := diffStatsForPath(gctx, statsPath)
		if err != nil {
			return fmt.Errorf("failed to inspect diff stats: %w", err)
		}
		snap.diffAdded, snap.diffDeleted, snap.diffUntracked, snap.hasChanges = a, d, u, hc
		return nil
	})
	g.Go(func() error {
		ps, err := pushStatusForPath(gctx, statsPath, branchName)
		if err != nil {
			return fmt.Errorf("failed to inspect push status: %w", err)
		}
		snap.push = ps
		return nil
	})
	if err := g.Wait(); err != nil {
		return snap, err
	}
	return snap, nil
}

// checkoutBranchInDir runs `git checkout <branch>` (or
// `git checkout -b <local> --track <remote>` for a remote-tracking ref
// without a matching local branch) in the given working directory.
// Returns a *gitutil.BranchNameError when branch is invalid — it unwraps
// to gitutil.ErrInvalidArgument so runBranchMutation surfaces it as
// InvalidArgument.
//
// Both the input and any stripped local name are validated before they
// reach git argv: a malicious remote could push a ref like
// `refs/heads/--help` that fetch mirrors into `refs/remotes/origin/--help`,
// so StripRemotePrefix output is not safe to forward as a positional arg
// without a second ValidateBranchName pass.
func checkoutBranchInDir(ctx context.Context, workingDir, branch string) error {
	branch = strings.TrimSpace(branch)
	if err := gitutil.ValidateBranchName(branch); err != nil {
		return err
	}
	target := branch

	// When the input looks like a remote ref ("origin/foo"), one
	// `git show-ref refs/remotes/<target> refs/heads/<localName>` call
	// resolves both the remote-exists and local-exists probes in a
	// single fork instead of two sequential `git rev-parse --verify`s.
	if strings.Contains(target, "/") {
		localName := gitutil.StripRemotePrefix(target)
		if err := gitutil.ValidateBranchName(localName); err != nil {
			return err
		}
		// `refs/heads/<target>` exists iff the user picked a LOCAL
		// branch whose name happens to contain a slash (e.g. literal
		// "feature/auth"). In that case the prefix-strip heuristic
		// must NOT redirect to "auth": a non-origin remote
		// ("upstream/auth") shares its name with the local branch's
		// suffix, so silently substituting would land HEAD on the
		// wrong branch. Probing this ref first lets us short-circuit
		// the prefix-strip path when the user's pick is itself a
		// local branch.
		originalLocalRef := "refs/heads/" + target
		remoteRef := "refs/remotes/" + target
		localRef := "refs/heads/" + localName
		// Surface HasRefs failures rather than silently falling through to
		// `git checkout origin/foo`: with checkout.guess=false (set in some
		// corporate/hardened installs) that bare command lands on a
		// detached HEAD on the remote-tracking commit, and a user picking
		// "origin/foo" from the branch dropdown silently starts working
		// against dangling commits.
		found, err := gitutil.HasRefs(ctx, workingDir, originalLocalRef, remoteRef, localRef)
		if err != nil {
			return fmt.Errorf("failed to resolve %q: %w", branch, err)
		}
		if !found[originalLocalRef] && found[remoteRef] {
			if found[localRef] {
				// Verify the local branch isn't already tracking a
				// DIFFERENT remote before substituting. Without this
				// check, a repo with multiple remotes (origin + upstream
				// + fork) silently lands the user on a same-named local
				// that tracks a different remote. Example: local
				// `feature/auth` tracks `origin/feature/auth`; user
				// picks `upstream/feature/auth`; the substitution would
				// put HEAD on the origin-tracking local even though the
				// user explicitly chose upstream's branch.
				//
				// `branch.<localName>.remote` is the remote-name half
				// of the upstream config (the other half is
				// `branch.<localName>.merge`). We refuse substitution
				// ONLY when the local tracks a remote that DOESN'T
				// match the picked prefix. The "no upstream configured"
				// case is intentionally permitted: the local was
				// created without `--track` (common for branches the
				// user made before adding a remote), and substituting
				// onto it matches the historical UX (and what every
				// existing call site relied on). The substituting-onto-
				// a-divergent-tracking-branch case — the actual bug
				// behind this gate — only happens when the config IS
				// set and disagrees with the user's pick.
				slash := strings.IndexByte(branch, '/')
				if slash <= 0 {
					// Defensive: StripRemotePrefix's `/`-presence check
					// already guaranteed `branch` contains a `/` at a
					// positive index. Unreachable in practice but cheap
					// to gate.
					return fmt.Errorf("internal: cannot compute remote prefix for %q", branch)
				}
				remotePrefix := branch[:slash]
				cfg := readGitConfigRegexp(ctx, workingDir,
					fmt.Sprintf(`^branch\.%s\.remote$`, regexp.QuoteMeta(localName)))
				localRemote := strings.TrimSpace(cfg["branch."+localName+".remote"])
				if localRemote != "" && localRemote != remotePrefix {
					return fmt.Errorf(
						"refusing to check out %q: a local branch %q already exists but tracks %q, not %q — rename the local or pick the remote-tracking variant explicitly",
						branch, localName, localRemote, remotePrefix,
					)
				}
				target = localName
			} else {
				stderr, err := gitutil.OutputStderr(ctx, workingDir, "checkout", "-b", localName, "--track", branch)
				if err != nil {
					return wrapGitErr("check out branch", stderr, err)
				}
				return nil
			}
		}
	}

	stderr, err := gitutil.OutputStderr(ctx, workingDir, "checkout", target)
	if err != nil {
		return wrapGitErr("check out branch", stderr, err)
	}
	return nil
}

// wrapGitErr formats a git subprocess failure as an error with the stderr
// trimmed in, falling back to the error message when stderr is empty.
func wrapGitErr(action, stderr string, err error) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("failed to %s: %s", action, msg)
}

// switchToResolvesTo reports whether checkoutBranchInDir(switchTo) would
// collapse onto `target` via the remote-prefix-strip rule. The rule
// fires only when switchTo contains a `/`, the suffix after the first
// `/` equals target, the input is NOT itself an existing local branch,
// AND both `refs/remotes/<switchTo>` and `refs/heads/<target>` exist —
// mirroring the same gate checkoutBranchInDir applies before substituting
// localName. The probe is a single `git show-ref` fork.
//
// Returns (collides, err). The error path is fail-CLOSED: callers must
// treat a non-nil err as "might collide, refuse" rather than silently
// falling through to a destructive op. The probe runs under its own
// per-call timeout so a wedged `git show-ref` can't starve the calling
// branchMutationTimeout budget.
func switchToResolvesTo(parent context.Context, workingDir, switchTo, target string) (bool, error) {
	if !strings.Contains(switchTo, "/") {
		return false, nil
	}
	if gitutil.StripRemotePrefix(switchTo) != target {
		return false, nil
	}
	probeCtx, cancel := context.WithTimeout(parent, gitReadTimeout)
	defer cancel()
	// `refs/heads/<switchTo>` rules the prefix-strip path out: when the
	// caller picked a literal local branch whose name happens to
	// collide with `<remote>/<target>`, checkoutBranchInDir defers to
	// the local. switchToResolvesTo must do the same so a delete with
	// `switch_to = "feature/foo"` (local) against `branch_to_delete = "foo"`
	// is not falsely rejected as a self-collision.
	originalLocalRef := "refs/heads/" + switchTo
	remoteRef := "refs/remotes/" + switchTo
	localRef := "refs/heads/" + target
	found, err := gitutil.HasRefs(probeCtx, workingDir, originalLocalRef, remoteRef, localRef)
	if err != nil {
		return false, err
	}
	if found[originalLocalRef] {
		return false, nil
	}
	return found[remoteRef] && found[localRef], nil
}

// deleteBranchInDir checks out switchTo in workingDir and then force-deletes
// branchToDelete. On delete failure, attempts to restore the original branch
// so the working dir doesn't end up on a surprise branch. Rejects invalid
// arg combinations (empty/equal branches) before touching git. The two
// invalid-arg returns wrap gitutil.ErrInvalidArgument so runBranchMutation
// surfaces them as InvalidArgument.
//
// `parent` is expected to be a generous outer ctx (e.g. 2*branchMutationTimeout)
// rather than a single branchMutationTimeout-bounded ctx: each phase
// applies its own branchMutationTimeout, and sharing a single 30s budget
// across checkout + delete would let a slow first phase starve the
// second.
func deleteBranchInDir(parent context.Context, workingDir, branchToDelete, switchTo string) error {
	branchToDelete = strings.TrimSpace(branchToDelete)
	switchTo = strings.TrimSpace(switchTo)
	if err := gitutil.ValidateBranchName(branchToDelete); err != nil {
		return err
	}
	if switchTo == "" {
		return fmt.Errorf("switch_to_branch is required: %w", gitutil.ErrInvalidArgument)
	}
	// Plain string equality is insufficient: checkoutBranchInDir
	// transparently resolves a remote ref like `origin/main` to the
	// local `main` when both refs exist, so picking `origin/main` while
	// on `main` would slip past the guard, the checkout below would be
	// a no-op, and `git branch -D main` would then fail with "cannot
	// delete branch checked out at ...". Reject the resolved-collision
	// case up front with the same InvalidArgument shape so the dialog
	// surfaces a clear message instead of a downstream git error.
	//
	// Fail-CLOSED on probe error: a transient `git show-ref` failure
	// (index.lock contention, slow disk, dubious-ownership) must NOT
	// silently let the destructive op proceed. Returning the probe
	// error here parks HEAD on its current branch and lets the user
	// retry once the underlying issue clears.
	if branchToDelete == switchTo {
		return fmt.Errorf("switch_to_branch must differ from branch_to_delete: %w", gitutil.ErrInvalidArgument)
	}
	collides, probeErr := switchToResolvesTo(parent, workingDir, switchTo, branchToDelete)
	if probeErr != nil {
		return fmt.Errorf("failed to validate switch_to_branch: %w", probeErr)
	}
	if collides {
		return fmt.Errorf("switch_to_branch must differ from branch_to_delete: %w", gitutil.ErrInvalidArgument)
	}

	checkoutCtx, cancelCheckout := context.WithTimeout(parent, branchMutationTimeout)
	defer cancelCheckout()
	if err := checkoutBranchInDir(checkoutCtx, workingDir, switchTo); err != nil {
		return err
	}

	deleteCtx, cancelDelete := context.WithTimeout(parent, branchMutationTimeout)
	defer cancelDelete()
	if delErr := gitutil.DeleteBranch(deleteCtx, workingDir, branchToDelete); delErr != nil {
		// Do NOT roll back the checkout: the user explicitly picked
		// `switch_to` as their destination, so leaving HEAD on it is
		// the least-surprising outcome on partial failure. An earlier
		// revision tried to restore HEAD to `branchToDelete`, but that
		// assumed the user was originally on `branchToDelete` — which
		// only holds when DeleteBranch was invoked from a current-
		// branch context. From the sidebar context menu the user can
		// pick any branch, so restoring to `branchToDelete` parks
		// them on a branch they may have never been on. Surface the
		// delete error verbatim and let the dialog tell the user the
		// switch succeeded but the delete didn't.
		return delErr
	}
	return nil
}

// createBranchInDir runs `git checkout -b <newBranch> [<baseBranch>]` in
// the given working directory. Validates both newBranch and baseBranch
// with the git-check-ref-format rules before invoking git — without the
// baseBranch validation an authenticated caller can inject git flags
// (`-f`, `--orphan`, `-c<k>=<v>`) through the base-branch field, since
// it lands in argv as a positional after `-b <newBranch>`.
func createBranchInDir(ctx context.Context, workingDir, newBranch, baseBranch string) error {
	newBranch = strings.TrimSpace(newBranch)
	if err := gitutil.ValidateBranchName(newBranch); err != nil {
		return err
	}
	args := []string{"checkout", "-b", newBranch}
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch != "" {
		if err := gitutil.ValidateBranchName(baseBranch); err != nil {
			return err
		}
		args = append(args, baseBranch)
	}
	stderr, err := gitutil.OutputStderr(ctx, workingDir, args...)
	if err != nil {
		return wrapGitErr("create branch", stderr, err)
	}
	return nil
}

func (svc *Service) pushBranch(ctx context.Context, tabType leapmuxv1.TabType, tabID string) error {
	tabCtx, err := svc.loadTabGitContext(ctx, tabType, tabID)
	if err != nil {
		return err
	}

	// Defense-in-depth: ValidateBranchName the value before it lands in
	// argv as a positional `git push -u origin <branchName>`. Every
	// other branch-name argv path in this file (checkoutBranchInDir,
	// createBranchInDir, deleteBranchInDir) runs this check; pushBranch
	// is the lone exception even though tabCtx.branchName is derived
	// from a raw `git rev-parse --abbrev-ref HEAD` parse rather than
	// validated user input. Today the value comes from git itself so
	// no flag injection is reachable; the check exists so a future
	// call site that pulls the branch name from a less trusted source
	// (DB row, hint hand-off) can't silently bypass the validator.
	if err := gitutil.ValidateBranchName(tabCtx.branchName); err != nil {
		return err
	}

	commitDir := tabCtx.commitDir()

	hasChanges, err := dirtyTreeForPush(ctx, commitDir)
	if err != nil {
		return err
	}
	if hasChanges {
		// Use OutputStderr so an index.lock / permission / hook failure
		// surfaces git's actual message instead of a bare exit status.
		stderr, err := gitutil.OutputStderr(ctx, commitDir, "add", "-A")
		if err != nil {
			return wrapGitErr("git add", stderr, err)
		}
		stderr, err = gitutil.OutputStderr(ctx, commitDir, "commit", "-m", wipCommitMessage)
		if err != nil {
			return wrapGitErr("commit "+wipCommitMessage, stderr, err)
		}
	}

	push, err := resolvePushStatus(ctx, commitDir, tabCtx.branchName)
	if err != nil {
		return err
	}
	if !push.CanPush(tabCtx.branchName) {
		if !push.OriginExists {
			return errors.New("cannot push: remote origin does not exist")
		}
		return errors.New("cannot push this branch")
	}

	pushArgs := []string{"push"}
	if !push.UpstreamExists {
		pushArgs = append(pushArgs, "-u", "origin", tabCtx.branchName)
	}
	stderr, err := gitutil.OutputStderr(ctx, commitDir, pushArgs...)
	if err != nil {
		return wrapGitErr("push", stderr, err)
	}
	return nil
}

// isDirty reports whether the working tree at `dir` has uncommitted
// changes via the lightweight `git status --porcelain` probe. Boolean-
// only — no numstat parsing, unlike diffStatsForPath.
func isDirty(ctx context.Context, dir string) (bool, error) {
	status, err := gitutil.Output(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(status) != "", nil
}

// dirtyTreeForPush decides whether the commit-dir has uncommitted changes
// the WIP-commit step should sweep up.
//
// Always probes at push time. The caller's snapshot may have raced an
// out-of-band file write or commit (another agent, an external editor,
// a terminal `git commit`) in the inspect→push interval, and trusting
// the stale hint either skips a real WIP commit or pushes an empty WIP
// commit. The probe is one `git status --porcelain` fork — cheap next to
// the push itself.
func dirtyTreeForPush(ctx context.Context, dir string) (bool, error) {
	dirty, err := isDirty(ctx, dir)
	if err != nil {
		return false, fmt.Errorf("failed to inspect git status: %w", err)
	}
	return dirty, nil
}

// resolvePushStatus returns the push-time snapshot via a fresh
// pushStatusForPath call. An earlier revision accepted an inspect-time
// `BranchGitState` hint and copied its `UnpushedCommitCount` onto the
// live probe — that was a footgun: pushStatusForPath already runs the
// `rev-list --count` fork it was meant to elide, and the user can have
// a sibling terminal/IDE push commits between inspect and the Push
// click, so blindly overwriting the live count with the stale hint
// produced wrong numbers under exactly the multi-actor scenario the
// hint was supposed to optimise.
func resolvePushStatus(ctx context.Context, dir, branchName string) (pushStatus, error) {
	return pushStatusForPath(ctx, dir, branchName)
}

func (svc *Service) loadTabGitContext(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (*tabGitContext, error) {
	workingDir, err := svc.getTabWorkingDir(ctx, tabType, tabID)
	if err != nil {
		return nil, err
	}

	info, err := queryGitPathInfo(ctx, workingDir)
	if err != nil {
		return nil, errors.New("tab is not in a git repository")
	}

	branchName := branchOrShortSHA(info)
	var worktreeRoot string
	if info.IsWorktree {
		worktreeRoot = info.TopLevel
	}
	tabCtx := &tabGitContext{
		workingDir:   workingDir,
		repoRoot:     info.RepoRoot,
		branchName:   branchName,
		worktreeRoot: worktreeRoot,
		isWorktree:   info.IsWorktree,
	}

	if tabCtx.isWorktree {
		wt, err := svc.Queries.GetWorktreeByPath(ctx, worktreeRoot)
		if err == nil {
			tabCtx.worktreeID = wt.ID
		} else if errors.Is(err, sql.ErrNoRows) {
			wtID, err := svc.ensureTrackedWorktree(ctx, worktreeRoot)
			if err != nil {
				return nil, err
			}
			tabCtx.worktreeID = wtID
		} else {
			return nil, err
		}
	}

	return tabCtx, nil
}

func (svc *Service) getTabWorkingDir(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (string, error) {
	switch tabType {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		agentRow, err := svc.Queries.GetAgentByID(ctx, tabID)
		if err != nil {
			return "", err
		}
		return agentRow.WorkingDir, nil
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		terminalRow, err := svc.Queries.GetTerminal(ctx, tabID)
		if err != nil {
			return "", err
		}
		return terminalRow.WorkingDir, nil
	default:
		return "", errors.New("unsupported tab type")
	}
}

// branchProbeConcurrency caps the number of `git rev-parse` forks issued
// in parallel by a single hasOtherNonWorktreeTabOnBranch scan. The probes
// are CPU+IO light but each forks a subprocess; 6 keeps the worker from
// thrashing on workspaces with hundreds of open tabs while still hiding
// rev-parse latency for the typical few-tab case.
const branchProbeConcurrency = 6

func (svc *Service) hasOtherNonWorktreeTabOnBranch(ctx context.Context, tabType leapmuxv1.TabType, tabID, repoRoot, branchName string) (bool, error) {
	// gitPathInfo lookups dedupe at two levels:
	//   - `cache` persists results so post-completion lookups skip the fork
	//     (matters when two terminals share a workingDir and the second
	//     probe runs after the first has finished).
	//   - `sf` coalesces in-flight lookups so concurrent misses for the
	//     same key wait on the first goroutine instead of each forking
	//     their own rev-parse (matters when an agent and terminal in the
	//     same dir race to probe).
	probe := newBranchInfoProbe()

	// Fan out the agents/terminals scans against a cancellable ctx so the
	// first match aborts the other scan's in-flight rev-parse calls.
	gctx, cancel := context.WithCancel(ctx)
	defer cancel()

	scan := func(query string, skipType leapmuxv1.TabType) (bool, error) {
		dirs, err := svc.collectTabDirs(gctx, query, tabID, tabType == skipType)
		if err != nil {
			return false, err
		}
		// Per-scan bounded fanout. The shared probe ensures dirs that
		// also appear in the sibling scan don't refork.
		probeG, probeCtx := errgroup.WithContext(gctx)
		probeG.SetLimit(branchProbeConcurrency)
		var hit atomic.Bool
		for _, dir := range dirs {
			if hit.Load() {
				break
			}
			probeG.Go(func() error {
				if hit.Load() {
					return nil
				}
				matches, err := probe.matches(probeCtx, dir, repoRoot, branchName)
				if err == nil && matches {
					hit.Store(true)
				}
				return nil
			})
		}
		_ = probeG.Wait()
		return hit.Load(), nil
	}

	g := new(errgroup.Group)
	var agentHit, terminalHit bool
	g.Go(func() error {
		hit, err := scan(`SELECT id, working_dir FROM agents WHERE closed_at IS NULL`, leapmuxv1.TabType_TAB_TYPE_AGENT)
		if err != nil {
			return err
		}
		if hit {
			agentHit = true
			cancel()
		}
		return nil
	})
	g.Go(func() error {
		hit, err := scan(`SELECT id, working_dir FROM terminals WHERE closed_at IS NULL`, leapmuxv1.TabType_TAB_TYPE_TERMINAL)
		if err != nil {
			return err
		}
		if hit {
			terminalHit = true
			cancel()
		}
		return nil
	})
	if err := g.Wait(); err != nil {
		// One scan may have errored because its sibling cancelled the ctx
		// after finding a hit — that's not a real failure.
		if (agentHit || terminalHit) && errors.Is(err, context.Canceled) {
			return true, nil
		}
		return false, err
	}
	return agentHit || terminalHit, nil
}

// collectTabDirs reads (id, working_dir) rows from the given query into a
// slice of working_dir values, skipping the caller's own tabID when the
// query targets the caller's tab type. Reading rows synchronously up
// front means the DB iterator doesn't have to be safe against concurrent
// probe goroutines below. The row loop checks `ctx` between scans so a
// sibling errgroup scan that found a hit and cancelled the shared ctx
// aborts the drain instead of paying full SQL load latency on a
// workspace with many tabs.
func (svc *Service) collectTabDirs(ctx context.Context, query, tabID string, skipSelf bool) ([]string, error) {
	rows, err := svc.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			slog.Warn("failed to close row iterator", "error", closeErr)
		}
	}()
	var dirs []string
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var id, workingDir string
		if err := rows.Scan(&id, &workingDir); err != nil {
			return nil, err
		}
		if skipSelf && id == tabID {
			continue
		}
		dirs = append(dirs, workingDir)
	}
	return dirs, rows.Err()
}

// branchInfoProbe coalesces gitPathInfo lookups for a single
// hasOtherNonWorktreeTabOnBranch call. The struct is scoped to one call:
// `cache` persists across the call so a post-completion lookup is free,
// `sf` deduplicates concurrent misses so two probes for the same dir
// share one rev-parse fork.
//
// Cancellation contract: singleflight runs queryGitPathInfo on the
// FIRST caller's ctx, so if that ctx is cancelled (e.g. its parent
// errgroup short-circuits when a sibling found a hit), every joined
// waiter receives context.Canceled even though their own ctx is alive.
// `lookup` detects that mismatch and re-runs the probe with its own
// ctx — so a waiter never inherits a leader's cancellation. Bounded
// retry count prevents a pathological storm of cancelling siblings
// from spinning forever.
type branchInfoProbe struct {
	mu    sync.Mutex
	cache map[string]*gitPathInfo
	sf    singleflight.Group
}

func newBranchInfoProbe() *branchInfoProbe {
	return &branchInfoProbe{cache: map[string]*gitPathInfo{}}
}

// matches reports whether a tab's workingDir resolves to repoRoot and is
// on branchName.
func (p *branchInfoProbe) matches(ctx context.Context, workingDir, repoRoot, branchName string) (bool, error) {
	info, err := p.lookup(ctx, workingDir)
	if err != nil {
		return false, err
	}
	if !pathutil.SamePath(info.RepoRoot, repoRoot) || info.IsWorktree {
		return false, nil
	}
	return branchOrShortSHA(info) == branchName, nil
}

// branchInfoProbeMaxRetries caps how many times lookup retries when it
// inherits a sibling's cancellation. In normal operation one retry is
// enough: the first iteration either lands the cache hit or runs the
// leader; if the leader was cancelled, the second iteration becomes
// the new leader with the caller's own (live) ctx. The cap exists
// only so a pathological storm of cancelling siblings can't spin the
// loop forever — at most maxRetries waiters can pile up before one
// runs to completion.
const branchInfoProbeMaxRetries = 8

func (p *branchInfoProbe) lookup(ctx context.Context, workingDir string) (*gitPathInfo, error) {
	for attempt := 0; ; attempt++ {
		p.mu.Lock()
		if cached, ok := p.cache[workingDir]; ok {
			p.mu.Unlock()
			return cached, nil
		}
		p.mu.Unlock()

		v, err, _ := p.sf.Do(workingDir, func() (any, error) {
			// Re-check under the lock in case a sibling goroutine completed
			// between our miss and entering Do.
			p.mu.Lock()
			if cached, ok := p.cache[workingDir]; ok {
				p.mu.Unlock()
				return cached, nil
			}
			p.mu.Unlock()

			fetched, err := queryGitPathInfo(ctx, workingDir)
			if err != nil {
				return nil, err
			}
			p.mu.Lock()
			p.cache[workingDir] = fetched
			p.mu.Unlock()
			return fetched, nil
		})
		if err == nil {
			return v.(*gitPathInfo), nil
		}
		// Inherited cancellation from another waiter: our own ctx is
		// still alive, sf.Do's leader was someone else whose ctx died
		// (their parent scan found a hit and cancelled). Retry once
		// with our own ctx as the new leader. The cap defends against
		// a pathological storm of cancelling siblings.
		isCtxErr := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		if isCtxErr && ctx.Err() == nil && attempt < branchInfoProbeMaxRetries {
			continue
		}
		return nil, err
	}
}

// shortstatInsRe / shortstatDelRe pull the integer counts out of
// `git diff --shortstat HEAD` output, e.g. " 3 files changed, 25
// insertions(+), 12 deletions(-)". Either side may be absent (a
// deletion-only or insertion-only diff drops the missing clause).
var (
	shortstatInsRe = regexp.MustCompile(`(\d+) insertion`)
	shortstatDelRe = regexp.MustCompile(`(\d+) deletion`)
)

// diffStatsForPath returns aggregate counters for the working tree at
// `dir`: total added/deleted lines vs HEAD (staged + unstaged combined),
// untracked file count, and whether the tree has any uncommitted changes
// at all. Two parallel git subprocesses — one `diff --shortstat HEAD`
// for line totals, one `status --porcelain` for untracked + hasChanges —
// avoid the per-file numstat parsing in getGitFileStatusEntries.
func diffStatsForPath(ctx context.Context, dir string) (added, deleted, untracked int32, hasChanges bool, err error) {
	var (
		shortstat    string
		shortstatErr error
		status       string
		statusErr    error
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		// `git diff --shortstat HEAD` sums staged + unstaged diffs against
		// HEAD. Failure modes are split below: unborn HEAD is recovered
		// via a per-file numstat fallback; real errors (permission,
		// corrupt index, dubious-ownership) are propagated so the
		// close-prompt does not silently report 0 added / 0 deleted on a
		// broken repo.
		shortstat, shortstatErr = gitutil.Output(gctx, dir, "diff", "--shortstat", "HEAD")
		return nil
	})
	g.Go(func() error {
		out, e := gitutil.Output(gctx, dir, "status", "--porcelain")
		if e != nil {
			statusErr = e
			return nil
		}
		status = out
		return nil
	})
	_ = g.Wait()
	if statusErr != nil {
		return 0, 0, 0, false, statusErr
	}
	untracked, hasChanges = parseStatusPorcelainCounts(status)
	if shortstatErr == nil {
		added, deleted = parseDiffShortstat(shortstat)
		return added, deleted, untracked, hasChanges, nil
	}
	// shortstat failed but status worked, so the dir IS a repo — the
	// most common cause is an unborn HEAD (no initial commit yet, but
	// files have been staged). Fall back to summing per-file numstat
	// from getGitFileStatusEntries: that path uses per-file `--numstat`
	// which works against the empty tree, so it surfaces real line
	// counts where `diff --shortstat HEAD` returns nothing.
	entries, fallbackErr := getGitFileStatusEntries(ctx, dir)
	if fallbackErr != nil {
		// Both diff approaches failed but porcelain status worked.
		// Trust the more-specific error from the shortstat probe
		// (likely the same root cause) and surface it so the user
		// sees the underlying failure instead of zero stats.
		return 0, 0, 0, false, shortstatErr
	}
	// getGitFileStatusEntries swallows its own `git status --porcelain=v2`
	// failure as (nil, nil) — see its top-of-function early return. If the
	// outer porcelain status reported changes but the inner v2 status
	// returned no entries, the two probes disagree and we have no signal
	// to compute line counts. Surface the original shortstat error rather
	// than silently shipping 0/0 to the close-prompt; staging-only changes
	// would otherwise be invisible to the user about to close the tab.
	if hasChanges && len(entries) == 0 {
		return 0, 0, 0, false, shortstatErr
	}
	for _, f := range entries {
		added += f.GetLinesAdded() + f.GetStagedLinesAdded()
		deleted += f.GetLinesDeleted() + f.GetStagedLinesDeleted()
	}
	return added, deleted, untracked, hasChanges, nil
}

// parseDiffShortstat extracts (added, deleted) line counts from
// `git diff --shortstat` output. Empty or unparseable input yields (0,0).
func parseDiffShortstat(out string) (added, deleted int32) {
	if m := shortstatInsRe.FindStringSubmatch(out); m != nil {
		n, _ := strconv.Atoi(m[1])
		added = int32(n)
	}
	if m := shortstatDelRe.FindStringSubmatch(out); m != nil {
		n, _ := strconv.Atoi(m[1])
		deleted = int32(n)
	}
	return
}

// parseStatusPorcelainCounts walks `git status --porcelain` output and
// returns the number of untracked entries plus whether any change is
// present. Lines beginning with `??` are untracked; any other non-empty
// line is a tracked change.
func parseStatusPorcelainCounts(out string) (untracked int32, hasChanges bool) {
	for _, line := range gitutil.ParseLines(out) {
		hasChanges = true
		if strings.HasPrefix(line, "??") {
			untracked++
		}
	}
	return
}

type pushStatus struct {
	Unpushed       int32
	UpstreamExists bool
	RemoteMissing  bool
	OriginExists   bool
}

// CanPush reports whether a branch can be pushed to origin. Detached HEAD and
// empty branch names are rejected even when origin exists.
func (s pushStatus) CanPush(branchName string) bool {
	return s.OriginExists && branchName != "" && branchName != "HEAD"
}

func pushStatusForPath(ctx context.Context, dir, branchName string) (pushStatus, error) {
	var s pushStatus

	// Collapse origin URL + branch.<name>.{remote,merge} into one
	// `git config -z --get-regexp` call so each dialog open / push
	// click forks one subprocess instead of three.
	hasBranch := branchName != "" && branchName != "HEAD"
	pattern := `^remote\.origin\.url$`
	if hasBranch {
		pattern = fmt.Sprintf(`^(remote\.origin\.url|branch\.%s\.(remote|merge))$`, regexp.QuoteMeta(branchName))
	}
	cfg := readGitConfigRegexp(ctx, dir, pattern)

	if _, ok := cfg["remote.origin.url"]; ok {
		s.OriginExists = true
	}

	if !hasBranch {
		return s, nil
	}

	remoteName, remoteOk := cfg["branch."+branchName+".remote"]
	mergeRef, mergeOk := cfg["branch."+branchName+".merge"]
	if !remoteOk || !mergeOk {
		if s.OriginExists {
			// `rev-list --count HEAD --not --remotes=origin` collapses to
			// HEAD's full ancestry when origin has zero fetched refs:
			// `--remotes=origin` matches nothing, `--not` excludes
			// nothing, and the user sees "<N> commits not pushed" over
			// what is in fact a freshly-added remote (e.g. `git remote
			// add origin URL` without a follow-up fetch, or a `--depth=1`
			// clone with no namespace). Probe origin first; if there are
			// no fetched refs, leave Unpushed at 0 — "unknown" is closer
			// to truth than "your entire history is unpushed".
			hasOriginRefs, refsErr := gitutil.HasAnyRef(ctx, dir, "refs/remotes/origin/")
			if refsErr != nil {
				return s, fmt.Errorf("git for-each-ref: %w", refsErr)
			}
			if hasOriginRefs {
				// Scope to origin's refs (the remote we just confirmed exists)
				// so we don't walk every other remote's fetched refs.
				// Propagate the error so a `rev-list --count` failure
				// (corrupt refs, transient I/O, permission glitch) doesn't
				// silently leave Unpushed=0 and let the dialog claim "no
				// unpushed commits" over a real git failure.
				out, revErr := gitutil.Output(ctx, dir, "rev-list", "--count", "HEAD", "--not", "--remotes=origin")
				if revErr != nil {
					return s, fmt.Errorf("git rev-list: %w", revErr)
				}
				count, scanErr := parseRevListCount(out)
				if scanErr != nil {
					return s, scanErr
				}
				s.Unpushed = count
			}
		}
		return s, nil
	}

	s.UpstreamExists = true
	remoteName = strings.TrimSpace(remoteName)
	mergeRef = strings.TrimSpace(mergeRef)
	remoteBranch := strings.TrimPrefix(mergeRef, "refs/heads/")
	if remoteName == "" || remoteBranch == "" {
		return s, nil
	}

	upstreamRef := "refs/remotes/" + remoteName + "/" + remoteBranch
	// HasRefs treats a `show-ref` exit-1 ("none of the requested refs exist")
	// as a non-error empty map; a non-nil error here means git itself failed
	// to run, in which case we conservatively mark the upstream as missing
	// rather than silently claim the count is zero.
	found, err := gitutil.HasRefs(ctx, dir, upstreamRef)
	if err != nil || !found[upstreamRef] {
		s.RemoteMissing = true
		return s, nil
	}

	// Same swallow-on-error hazard as the no-upstream arm above: a silent
	// failure here would leave Unpushed=0 + UpstreamExists=true, which the
	// frontend renders as "no unpushed commits" over a real git failure.
	out, revErr := gitutil.Output(ctx, dir, "rev-list", "--count", upstreamRef+"..HEAD")
	if revErr != nil {
		return s, fmt.Errorf("git rev-list: %w", revErr)
	}
	count, scanErr := parseRevListCount(out)
	if scanErr != nil {
		return s, scanErr
	}
	s.Unpushed = count
	return s, nil
}

// parseRevListCount converts the (trimmed) stdout of `git rev-list --count`
// into an int32. Returns the parse error on bad input.
func parseRevListCount(out string) (int32, error) {
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, err
	}
	return int32(n), nil
}

// getGitFileStatusEntries computes per-file git status for the given repo
// root. Runs `git status --porcelain=v2 -z` first to learn whether the tree
// has any changes; bails before forking the two numstat probes when the
// tree is clean (the common case for sidebar refreshes). When changes do
// exist, the two numstat probes are issued in parallel as best-effort
// annotations. Returns nil for non-git directories.
func getGitFileStatusEntries(ctx context.Context, repoRoot string) ([]*leapmuxv1.GitFileStatusEntry, error) {
	statusOut, err := gitutil.Bytes(ctx, repoRoot, "status", "--porcelain=v2", "-z")
	if err != nil {
		return nil, nil // Not a git repo or git unavailable.
	}

	files := parseStatusV2(statusOut)
	if len(files) == 0 {
		return nil, nil
	}

	// Skip the numstat fan-out when every entry is untracked. `git diff`
	// does not report untracked files, so both probes would return empty
	// output. The most common trigger is a stray directory that slipped
	// past .gitignore (e.g. a freshly-built node_modules) — saves two
	// subprocess forks per sidebar refresh for that case.
	if !hasTrackedChange(files) {
		return files, nil
	}

	var (
		unstagedOut, stagedOut []byte
		wg                     sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		// Failure is treated as "no annotation" — applyNumstat handles
		// nil/empty input — so the local err is intentionally discarded.
		unstagedOut, _ = gitutil.Bytes(ctx, repoRoot, "diff", "--numstat", "-z")
	}()
	go func() {
		defer wg.Done()
		stagedOut, _ = gitutil.Bytes(ctx, repoRoot, "diff", "--numstat", "--staged", "-z")
	}()
	wg.Wait()

	fileMap := make(map[string]*leapmuxv1.GitFileStatusEntry, len(files))
	for _, f := range files {
		fileMap[f.Path] = f
	}
	applyNumstat(unstagedOut, fileMap, false)
	applyNumstat(stagedOut, fileMap, true)
	return files, nil
}

// readGitConfigRegexp runs `git config -z --get-regexp <pattern>` and
// returns the matched entries as a key→value map. A non-zero git exit
// (no matches) yields a nil map; callers should treat absence as
// equivalent to a missing key. The `-z` output framing is `key\nvalue\0`
// per entry, which lets values contain `\n` (config values can have
// embedded newlines, even if the ones we read here don't).
func readGitConfigRegexp(ctx context.Context, dir, pattern string) map[string]string {
	out, err := gitutil.Bytes(ctx, dir, "config", "-z", "--get-regexp", pattern)
	if err != nil || len(out) == 0 {
		return nil
	}
	result := make(map[string]string)
	for _, entry := range gitutil.SplitNUL(out) {
		if entry == "" {
			continue
		}
		if nl := strings.IndexByte(entry, '\n'); nl >= 0 {
			result[entry[:nl]] = entry[nl+1:]
		} else {
			// `-z` emits `key\0` for a bool-style key with no value.
			result[entry] = ""
		}
	}
	return result
}

// parseGitStatusCode converts a single character from git status --porcelain
// into a GitFileStatusCode enum value.
func parseGitStatusCode(c byte) leapmuxv1.GitFileStatusCode {
	switch c {
	case 'M':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_MODIFIED
	case 'A':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_ADDED
	case 'D':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_DELETED
	case 'R':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_RENAMED
	case 'C':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_COPIED
	case 'T':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_TYPE_CHANGED
	case '?':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED
	case 'U':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNMERGED
	default:
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNSPECIFIED
	}
}

// hasTrackedChange reports whether any entry is something other than a
// plain untracked file. parseStatusV2 emits untracked entries with
// StagedStatus = UNSPECIFIED and UnstagedStatus = UNTRACKED; every other
// variant fills both fields via parseGitStatusCode.
func hasTrackedChange(files []*leapmuxv1.GitFileStatusEntry) bool {
	for _, f := range files {
		if f.GetStagedStatus() != leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNSPECIFIED ||
			f.GetUnstagedStatus() != leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED {
			return true
		}
	}
	return false
}

// parseStatusV2 parses NUL-delimited `git status --porcelain=v2 -z` output
// into proto entries.
func parseStatusV2(data []byte) []*leapmuxv1.GitFileStatusEntry {
	var files []*leapmuxv1.GitFileStatusEntry
	parts := gitutil.SplitNUL(data)

	i := 0
	for i < len(parts) {
		entry := parts[i]
		if len(entry) == 0 {
			i++
			continue
		}

		switch entry[0] {
		case '1':
			// Ordinary changed entry: "1 XY sub mH mI mW hH hI path"
			if f := parseStatusV2Entry(entry, 8); f != nil {
				files = append(files, f)
			}
			i++

		case '2':
			// Renamed/copied entry: "2 XY sub mH mI mW hH hI Xscore path\0origPath"
			if f := parseStatusV2Entry(entry, 9); f != nil {
				if i+1 < len(parts) {
					f.OldPath = parts[i+1]
					i += 2
				} else {
					i++
				}
				files = append(files, f)
			} else {
				i++
			}

		case 'u':
			// Unmerged entry: "u XY sub m1 m2 m3 hH h1 h2 h3 path"
			if f := parseStatusV2Entry(entry, 10); f != nil {
				files = append(files, f)
			}
			i++

		case '?':
			// Untracked: "? path"
			if len(entry) > 2 {
				files = append(files, &leapmuxv1.GitFileStatusEntry{
					Path:           entry[2:],
					StagedStatus:   leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNSPECIFIED,
					UnstagedStatus: leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED,
				})
			}
			i++

		default:
			i++
		}
	}

	return files
}

// parseStatusV2Entry parses one porcelain=v2 record into a status entry.
// Every variant of the record (ordinary "1", rename "2", unmerged "u")
// puts XY (the staged/unstaged status pair) at positions 2 and 3, with
// the file path after a variant-specific number of space-delimited
// fields. Callers pass `pathField` per variant (8 / 9 / 10). Returns
// nil if the entry is too short to carry XY or if the path field is
// empty.
func parseStatusV2Entry(entry string, pathField int) *leapmuxv1.GitFileStatusEntry {
	if len(entry) < 4 {
		return nil
	}
	path := nthField(entry, pathField)
	if path == "" {
		return nil
	}
	return &leapmuxv1.GitFileStatusEntry{
		Path:           path,
		StagedStatus:   parseGitStatusCode(entry[2]),
		UnstagedStatus: parseGitStatusCode(entry[3]),
	}
}

// nthField returns the content after the nth space in s (0-indexed).
func nthField(s string, n int) string {
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			count++
			if count == n {
				return s[i+1:]
			}
		}
	}
	return ""
}

// applyNumstat parses NUL-delimited `git diff --numstat -z` output and applies
// line counts to the file map. When staged is true, it populates
// StagedLinesAdded/StagedLinesDeleted; otherwise LinesAdded/LinesDeleted.
func applyNumstat(data []byte, fileMap map[string]*leapmuxv1.GitFileStatusEntry, staged bool) {
	parts := gitutil.SplitNUL(data)
	i := 0
	for i < len(parts) {
		line := parts[i]
		if line == "" {
			i++
			continue
		}

		tabs := strings.SplitN(line, "\t", 3)
		if len(tabs) < 3 {
			i++
			continue
		}

		added, _ := strconv.Atoi(tabs[0])
		deleted, _ := strconv.Atoi(tabs[1])
		addedVal := int32(added)
		deletedVal := int32(deleted)

		filePath := tabs[2]
		if filePath == "" {
			// Rename: next two parts are oldpath and newpath.
			if i+2 < len(parts) {
				filePath = parts[i+2]
				i += 3
			} else {
				i++
				continue
			}
		} else {
			i++
		}

		if f, ok := fileMap[filePath]; ok {
			if staged {
				f.StagedLinesAdded = addedVal
				f.StagedLinesDeleted = deletedVal
			} else {
				f.LinesAdded = addedVal
				f.LinesDeleted = deletedVal
			}
		}
	}
}

type gitPathInfo struct {
	RepoRoot   string // canonical main repo root
	TopLevel   string // canonical --show-toplevel (worktree root when IsWorktree)
	BranchName string // abbrev-ref of HEAD; empty when detached or unborn
	HeadSHA    string // full 40-char HEAD SHA; empty on unborn HEAD
	IsWorktree bool
}

// shortSHALen is the prefix length used for "short" SHA display when HEAD is
// detached, matching git's default core.abbrev (7). A static prefix can be
// ambiguous in very large repos, but the value is for UI display only;
// disambiguation would require a separate `git rev-parse --short HEAD`
// subprocess, which is the fork we're saving here.
const shortSHALen = 7

// queryGitPathInfo runs `git rev-parse` and returns the resolved metadata
// for dirPath. Fails with errNotGitRepo if dirPath is not in a work tree.
//
// The combined-form call below packs five outputs into one fork in the
// common case (HEAD points at a real commit):
//
//	lines[len-4] = .git dir         (`--git-dir`, never contains a newline by convention)
//	lines[len-3] = .git common dir  (`--git-common-dir`)
//	lines[len-2] = HEAD SHA         (positional `HEAD`)
//	lines[len-1] = abbrev-ref       (`--abbrev-ref HEAD`)
//
// Everything before that is the (possibly multi-line) `--show-toplevel`
// output, joined with newlines. Parse from the END so a toplevel path
// containing embedded newlines (legal POSIX, vanishingly rare) doesn't
// silently zero the result. gitutil.GetToplevelInfo uses the same
// end-counting strategy.
//
// Output order matters: positional `HEAD` comes before `--abbrev-ref HEAD`
// so the first emits the full SHA and the second emits the abbreviated
// branch name. Reversing them would make `--abbrev-ref` apply to both
// (option flags in `git rev-parse` are sticky across subsequent revs)
// and the SHA would be lost.
//
// Unborn HEAD fallback: a freshly-initialized repo or a
// `git worktree add --no-checkout` before its first commit IS a real
// git repo, but BOTH the positional `HEAD` revision AND `--abbrev-ref HEAD`
// fail to resolve, making the combined call exit non-zero. Retry with
// the commit-independent subset (`--show-toplevel`, `--git-dir`,
// `--git-common-dir`) and then probe the symref separately —
// `symbolic-ref --short HEAD` resolves the unborn branch name (the
// symref target git stamps on `init`) without needing a commit. The
// alternative (returning errNotGitRepo) misreported every empty repo
// as not-a-git-repo and locked the user out of every dialog whose
// open path runs through this helper.
func queryGitPathInfo(ctx context.Context, dirPath string) (*gitPathInfo, error) {
	output, err := gitutil.Output(ctx, dirPath,
		"rev-parse", "--show-toplevel", "--git-dir", "--git-common-dir", "HEAD", "--abbrev-ref", "HEAD")
	if err == nil {
		return parseGitPathInfoOutput(output, true)
	}
	// Distinguish ctx cancellation/timeout from "git says this isn't a
	// repo" or "HEAD is unborn". Callers like attachWorktreeIfPresent
	// treat errNotGitRepo as a no-op (no worktree to register) but
	// must surface ctx errors so a cancelled OpenAgent doesn't ship a
	// tab without worktree linkage. errors.Is so a wrapped/joined ctx
	// error still routes correctly.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	// Retry with the commit-independent rev-parse options only. A true
	// non-git path will fail this second call too and fall through to
	// errNotGitRepo; only the unborn-HEAD case reaches the successful
	// return below with the branch name supplied by symbolic-ref.
	output, err = gitutil.Output(ctx, dirPath,
		"rev-parse", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		// Wrap rather than collapse to bare errNotGitRepo so callers
		// that want the underlying git diagnostic (dubious-ownership,
		// EACCES, corrupted config) can log it. errors.Is against
		// errNotGitRepo still routes the way every existing call site
		// expects, and gitutil.Output already folds stderr into the
		// error message via wrapWithStderr so the wrapped chain
		// carries git's real "fatal: …" text.
		return nil, wrapNotGitRepo(err)
	}
	info, err := parseGitPathInfoOutput(output, false)
	if err != nil {
		return nil, err
	}
	// Fill in the branch name from symbolic-ref. Works on unborn HEAD;
	// fails on detached HEAD — but detached HEAD always resolves the
	// 5-arg form successfully (the positional HEAD finds a SHA) so we
	// never reach this fallback in that case. Tolerate the error so a
	// future call site that ever does reach here on detached HEAD just
	// returns the path info with empty BranchName.
	if branch, branchErr := gitutil.Output(ctx, dirPath, "symbolic-ref", "--short", "HEAD"); branchErr == nil {
		info.BranchName = strings.TrimSpace(branch)
	}
	return info, nil
}

// parseGitPathInfoOutput parses the stdout of the rev-parse pipeline
// queryGitPathInfo runs. `hasHeadFields` selects the 5-line layout
// (with the positional `HEAD` SHA + `--abbrev-ref` lines) vs. the
// 3-line unborn-HEAD layout (toplevel + the two .git paths only;
// HeadSHA and BranchName left empty for the caller to fill in via a
// separate symbolic-ref probe). End-counting handles a multi-line
// `--show-toplevel`; see queryGitPathInfo's doc comment for the line
// layout.
func parseGitPathInfoOutput(output string, hasHeadFields bool) (*gitPathInfo, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	fixedTail := 2 // gitDir + commonDir
	if hasHeadFields {
		fixedTail = 4 // + positional HEAD SHA + abbrevRef
	}
	if len(lines) < fixedTail+1 {
		return nil, errNotGitRepo
	}
	n := len(lines)
	topLevel := pathutil.Canonicalize(strings.TrimSpace(strings.Join(lines[:n-fixedTail], "\n")))
	var gitDir, commonDir string
	if hasHeadFields {
		gitDir = strings.TrimSpace(lines[n-4])
		commonDir = strings.TrimSpace(lines[n-3])
	} else {
		gitDir = strings.TrimSpace(lines[n-2])
		commonDir = strings.TrimSpace(lines[n-1])
	}
	info := &gitPathInfo{
		TopLevel: topLevel,
		RepoRoot: topLevel,
	}
	if hasHeadFields {
		info.HeadSHA = strings.TrimSpace(lines[n-2])
		if abbrevRef := strings.TrimSpace(lines[n-1]); abbrevRef != "HEAD" {
			info.BranchName = abbrevRef
		}
	}
	if gitutil.IsLinkedWorktreeGitDir(gitDir) {
		info.IsWorktree = true
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(topLevel, commonDir)
		}
		// commonDir is <main-repo>/.git, so its parent is the main repo root.
		info.RepoRoot = pathutil.Canonicalize(filepath.Dir(filepath.Clean(commonDir)))
	}
	return info, nil
}

// branchOrShortSHA returns info.BranchName when set, otherwise a short
// prefix of HeadSHA for the "detached HEAD" display case. Reads from the
// info struct only — the SHA was already resolved by queryGitPathInfo.
func branchOrShortSHA(info *gitPathInfo) string {
	if info.BranchName != "" {
		return info.BranchName
	}
	if len(info.HeadSHA) < shortSHALen {
		return info.HeadSHA
	}
	return info.HeadSHA[:shortSHALen]
}

// listGitBranches walks refs/heads + refs/remotes in one
// `git for-each-ref` call (vs. the prior pair of `git branch --list`
// invocations) and projects them onto GitBranchEntry. Remote-tracking
// refs under `origin/` whose suffix already exists locally are skipped
// — the caller would only use that remote variant to create a tracking
// local branch, and the existing local already serves the same purpose
// for the default-remote case. Refs under any other remote (upstream/,
// fork/, …) are always preserved because a same-named local branch may
// be tracking a different remote, and silently dropping the entry would
// lock the user out of explicitly picking it. Remote HEAD pseudo-refs
// ("origin/HEAD") and bare remote names (refs/remotes/origin) are
// filtered out too.
func listGitBranches(ctx context.Context, repoRoot string) ([]*leapmuxv1.GitBranchEntry, error) {
	out, err := gitutil.Output(ctx, repoRoot, "for-each-ref",
		"--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}

	const (
		headsPrefix   = "refs/heads/"
		remotesPrefix = "refs/remotes/"
		// Only origin/* gets the local-counterpart dedup. Non-origin
		// remotes are kept verbatim because StripRemotePrefix's
		// first-`/` heuristic does not know which remote a local branch
		// actually tracks, and dropping an entry on a name collision
		// would silently make `upstream/main` (or `fork/main`)
		// unselectable in any repo that also has a local `main`.
		originPrefix = "origin/"
	)
	var (
		localBranches  []*leapmuxv1.GitBranchEntry
		remoteBranches []*leapmuxv1.GitBranchEntry
		localSet       = make(map[string]bool)
	)
	for _, ref := range gitutil.ParseLines(out) {
		switch {
		case strings.HasPrefix(ref, headsPrefix):
			name := ref[len(headsPrefix):]
			if name == "" {
				continue
			}
			localSet[name] = true
			localBranches = append(localBranches, &leapmuxv1.GitBranchEntry{Name: name, IsRemote: false})
		case strings.HasPrefix(ref, remotesPrefix):
			name := ref[len(remotesPrefix):]
			// Skip "origin/HEAD" pseudo-refs and bare remote names like
			// "origin" — neither is a checkoutable branch.
			if name == "" || strings.HasSuffix(name, "/HEAD") || !strings.Contains(name, "/") {
				continue
			}
			remoteBranches = append(remoteBranches, &leapmuxv1.GitBranchEntry{Name: name, IsRemote: true})
		}
	}

	// Dedup origin/<branch> against local <branch> only. The UI would
	// otherwise show "main" and "origin/main" as separate options when
	// checking out either lands on the same local branch.
	branches := localBranches
	for _, r := range remoteBranches {
		if strings.HasPrefix(r.Name, originPrefix) && localSet[r.Name[len(originPrefix):]] {
			continue
		}
		branches = append(branches, r)
	}
	return branches, nil
}

// listGitWorktrees parses `git worktree list --porcelain` output into proto entries.
func listGitWorktrees(ctx context.Context, dirPath string) ([]*leapmuxv1.GitWorktreeEntry, error) {
	output, err := gitutil.Output(ctx, dirPath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}

	var worktrees []*leapmuxv1.GitWorktreeEntry
	isFirst := true

	// Porcelain output: entries separated by blank lines.
	blocks := strings.Split(strings.TrimSpace(output), "\n\n")
	for _, block := range blocks {
		if strings.TrimSpace(block) == "" {
			continue
		}

		entry := &leapmuxv1.GitWorktreeEntry{IsMain: isFirst}
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "worktree ") {
				entry.Path = strings.TrimPrefix(line, "worktree ")
			} else if strings.HasPrefix(line, "branch refs/heads/") {
				entry.Branch = strings.TrimPrefix(line, "branch refs/heads/")
			}
		}
		if entry.Path != "" {
			worktrees = append(worktrees, entry)
		}
		isFirst = false
	}

	return worktrees, nil
}
