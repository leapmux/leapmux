package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/pathutil"
	"github.com/leapmux/leapmux/internal/util/validate"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"golang.org/x/sync/errgroup"
)

// registerGitHandlers registers handlers for git operations on the local filesystem.
func registerGitHandlers(d *channel.Dispatcher, svc *Context) {
	d.Register("GetGitInfo", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		ctx := bgCtx()
		resp := &leapmuxv1.GetGitInfoResponse{}

		info, err := queryGitPathInfo(ctx, dirPath)
		if err != nil {
			sendProtoResponse(sender, resp)
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
		resp.CurrentBranch = branchOrShortSHA(ctx, dirPath, info)

		// Origin URL.
		if originURL, err := gitOutput(ctx, dirPath, "config", "--get", "remote.origin.url"); err == nil {
			resp.OriginUrl = strings.TrimSpace(originURL)
		}

		// Dirty status.
		if status, err := gitOutput(ctx, dirPath, "status", "--porcelain"); err == nil {
			resp.IsDirty = strings.TrimSpace(status) != ""
		}

		sendProtoResponse(sender, resp)
	})

	d.Register("GetGitFileStatus", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		ctx := bgCtx()

		// Resolve repo root. `git rev-parse --show-toplevel` emits posix
		// separators on Windows (e.g. `C:/foo/bar`), and when git is invoked
		// from Git-Bash/MSYS it can produce `/c/foo/bar`; both mismatch
		// native paths like agent.workingDir on the frontend, so normalize.
		repoRoot, err := gitOutput(ctx, dirPath, "rev-parse", "--show-toplevel")
		if err != nil {
			sendInternalError(sender, "not a git repository")
			return
		}
		repoRoot = pathutil.NormalizeNative(strings.TrimSpace(repoRoot))

		files, err := getGitFileStatusEntries(ctx, repoRoot)
		if err != nil {
			slog.Error("git file status failed", "path", repoRoot, "error", err)
			sendInternalError(sender, "git status failed")
			return
		}

		resp := &leapmuxv1.GetGitFileStatusResponse{
			RepoRoot: repoRoot,
			Files:    files,
		}
		if branch, err := gitOutput(ctx, repoRoot, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			resp.CurrentBranch = strings.TrimSpace(branch)
			if resp.CurrentBranch == "HEAD" {
				if sha, err := gitOutput(ctx, repoRoot, "rev-parse", "--short", "HEAD"); err == nil {
					resp.CurrentBranch = strings.TrimSpace(sha)
				}
			}
		}
		if originURL, err := gitOutput(ctx, repoRoot, "config", "--get", "remote.origin.url"); err == nil {
			resp.OriginUrl = strings.TrimSpace(originURL)
		}
		sendProtoResponse(sender, resp)
	})

	d.Register("ReadGitFile", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		ctx := bgCtx()

		// Resolve repo root from the file's directory.
		fileDir := filepath.Dir(absPath)
		repoRoot, err := gitOutput(ctx, fileDir, "rev-parse", "--show-toplevel")
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

		// Determine the git ref string.
		var refStr string
		switch r.GetRef() {
		case leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD:
			refStr = "HEAD"
		case leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED:
			refStr = ":0"
		default:
			refStr = "HEAD"
		}

		// Run git show <ref>:<relative_path>.
		showArg := refStr + ":" + relPath
		content, err := gitOutputBytes(ctx, repoRoot, "show", showArg)
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

	d.Register("ListGitBranches", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		ctx := bgCtx()

		info, err := queryGitPathInfo(ctx, dirPath)
		if err != nil {
			sendInternalError(sender, "not a git repository")
			return
		}
		repoRoot := info.RepoRoot

		// Get local branches.
		localOut, err := gitOutput(ctx, repoRoot, "branch", "--list", "--format=%(refname:short)")
		if err != nil {
			sendInternalError(sender, "failed to list local branches")
			return
		}

		localBranches := parseGitLines(localOut)
		localSet := make(map[string]bool, len(localBranches))
		var branches []*leapmuxv1.GitBranchEntry
		for _, b := range localBranches {
			localSet[b] = true
			branches = append(branches, &leapmuxv1.GitBranchEntry{Name: b, IsRemote: false})
		}

		// Get remote tracking branches.
		remoteOut, _ := gitOutput(ctx, repoRoot, "branch", "-r", "--list", "--format=%(refname:short)")
		for _, b := range parseGitLines(remoteOut) {
			// Skip HEAD pointers, bare remote names, and branches that already have a local counterpart.
			if strings.HasSuffix(b, "/HEAD") {
				continue
			}
			if !strings.Contains(b, "/") {
				continue
			}
			// e.g. "origin/main" -> check if "main" exists locally.
			parts := strings.SplitN(b, "/", 2)
			if len(parts) == 2 && localSet[parts[1]] {
				continue
			}
			branches = append(branches, &leapmuxv1.GitBranchEntry{Name: b, IsRemote: true})
		}

		sendProtoResponse(sender, &leapmuxv1.ListGitBranchesResponse{
			Branches:      branches,
			CurrentBranch: branchOrShortSHA(ctx, dirPath, info),
		})
	})

	d.Register("ListGitWorktrees", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
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

		ctx := bgCtx()
		worktrees, err := listGitWorktrees(ctx, dirPath)
		if err != nil {
			sendInternalError(sender, "failed to list worktrees: "+err.Error())
			return
		}

		sendProtoResponse(sender, &leapmuxv1.ListGitWorktreesResponse{
			Worktrees: worktrees,
		})
	})

	d.Register("InspectLastTabClose", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.InspectLastTabCloseRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		// Track on Cleanup so a concurrent Shutdown waits for the
		// errgroup in inspectLastTabClose to finish instead of tearing
		// down the DB mid-query.
		svc.Cleanup.Add(1)
		defer svc.Cleanup.Done()

		traceTabClosePhase("inspect", r.GetTabId(), "handler_begin")
		resp, err := svc.inspectLastTabClose(bgCtx(), r.GetTabType(), r.GetTabId())
		if err != nil {
			slog.Error("inspect last tab close failed", "tab_type", r.GetTabType(), "tab_id", r.GetTabId(), "error", err)
			traceTabClosePhase("inspect", r.GetTabId(), "handler_error")
			sendInternalError(sender, err.Error())
			return
		}
		traceTabClosePhase("inspect", r.GetTabId(), "handler_end")
		sendProtoResponse(sender, resp)
	})

	d.Register("PushBranchForClose", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.PushBranchForCloseRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		if err := svc.pushBranchForClose(bgCtx(), r.GetTabType(), r.GetTabId()); err != nil {
			sendInternalError(sender, err.Error())
			return
		}
		sendProtoResponse(sender, &leapmuxv1.PushBranchForCloseResponse{})
	})

	d.Register("CheckWorktreeStatus", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.CheckWorktreeStatusRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		resp, err := svc.inspectLastTabClose(bgCtx(), r.GetTabType(), r.GetTabId())
		if err != nil {
			slog.Error("check worktree status failed", "tab_type", r.GetTabType(), "tab_id", r.GetTabId(), "error", err)
			sendInternalError(sender, err.Error())
			return
		}

		sendProtoResponse(sender, &leapmuxv1.CheckWorktreeStatusResponse{
			HasWorktree:  resp.GetTarget() == leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
			IsLastTab:    resp.GetTarget() == leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE && resp.GetShouldPrompt(),
			IsDirty:      resp.GetHasUncommittedChanges() || resp.GetUnpushedCommitCount() > 0,
			WorktreePath: resp.GetWorktreePath(),
			WorktreeId:   resp.GetWorktreeId(),
			BranchName:   resp.GetBranchName(),
		})
	})

	d.Register("ForceRemoveWorktree", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ForceRemoveWorktreeRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		ctx := bgCtx()

		// Look up worktree by ID.
		wt, err := svc.Queries.GetWorktreeByID(ctx, r.GetWorktreeId())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				sendNotFoundError(sender, "worktree not found")
				return
			}
			slog.Error("failed to get worktree", "error", err)
			sendInternalError(sender, "failed to query worktree")
			return
		}

		// Remove worktree from disk + branch + DB row. Reports an error
		// when the git worktree-remove failed AND the directory is still
		// on disk, so the caller knows manual cleanup is needed.
		if err := svc.removeWorktreeFromDisk(wt, true); err != nil {
			sendInternalError(sender, err.Error())
			return
		}

		sendProtoResponse(sender, &leapmuxv1.ForceRemoveWorktreeResponse{})
	})

	d.Register("KeepWorktree", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.KeepWorktreeRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		ctx := bgCtx()

		// Look up worktree by ID.
		wt, err := svc.Queries.GetWorktreeByID(ctx, r.GetWorktreeId())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				sendNotFoundError(sender, "worktree not found")
				return
			}
			slog.Error("failed to get worktree", "error", err)
			sendInternalError(sender, "failed to query worktree")
			return
		}

		// Delete the DB record but leave the worktree on disk.
		if err := svc.Queries.DeleteWorktree(ctx, wt.ID); err != nil {
			slog.Error("failed to delete worktree from DB", "id", wt.ID, "error", err)
			sendInternalError(sender, "failed to delete worktree record")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.KeepWorktreeResponse{})
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

func (svc *Context) inspectLastTabClose(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (*leapmuxv1.InspectLastTabCloseResponse, error) {
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
			trace("fast_path_taken")
			return &leapmuxv1.InspectLastTabCloseResponse{
				Target:       leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_WORKTREE,
				ShouldPrompt: false,
				RepoRoot:     wt.RepoRoot,
				WorktreePath: wt.WorktreePath,
				WorktreeId:   wt.ID,
				BranchName:   wt.BranchName,
			}, nil
		}
		// The worktree DB row already carries RepoRoot / BranchName /
		// WorktreePath, so we can skip getTabWorkingDir (DB),
		// queryGitPathInfo (rev-parse), branchOrShortSHA (possibly
		// another rev-parse), and GetWorktreeByPath (DB) — all work
		// that loadTabGitContext would otherwise repeat on the
		// dialog-latency path.
		tabCtx = &tabGitContext{
			workingDir:   wt.WorktreePath,
			repoRoot:     wt.RepoRoot,
			branchName:   wt.BranchName,
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

	// diffStatsForPath and pushStatusForPath are independent git
	// subprocesses on the same working directory; run them concurrently
	// so the dialog-latency path pays the max of the two, not the sum.
	statsPath := tabCtx.commitDir()
	var (
		added, deleted, untracked int32
		hasChanges                bool
		push                      pushStatus
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		a, d, u, hc, err := diffStatsForPath(gctx, statsPath)
		if err != nil {
			return fmt.Errorf("failed to inspect diff stats: %w", err)
		}
		added, deleted, untracked, hasChanges = a, d, u, hc
		return nil
	})
	g.Go(func() error {
		ps, err := pushStatusForPath(gctx, statsPath, tabCtx.branchName)
		if err != nil {
			return fmt.Errorf("failed to inspect push status: %w", err)
		}
		push = ps
		return nil
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	// Single emission covers both parallel calls completing — their
	// individual timestamps are no longer meaningful once they race.
	trace("diff_and_push_done")
	resp.DiffAdded = added
	resp.DiffDeleted = deleted
	resp.DiffUntracked = untracked
	resp.HasUncommittedChanges = hasChanges
	resp.UnpushedCommitCount = push.Unpushed
	resp.UpstreamExists = push.UpstreamExists
	resp.RemoteBranchMissing = push.RemoteMissing
	resp.OriginExists = push.OriginExists
	resp.CanPush = push.CanPush(tabCtx.branchName)

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
	if hasChanges || push.Unpushed > 0 || push.RemoteMissing {
		resp.Target = leapmuxv1.LastTabCloseTarget_LAST_TAB_CLOSE_TARGET_BRANCH
		resp.ShouldPrompt = true
	}

	return resp, nil
}

func (svc *Context) pushBranchForClose(ctx context.Context, tabType leapmuxv1.TabType, tabID string) error {
	tabCtx, err := svc.loadTabGitContext(ctx, tabType, tabID)
	if err != nil {
		return err
	}

	commitDir := tabCtx.commitDir()
	_, _, _, hasChanges, err := diffStatsForPath(ctx, commitDir)
	if err != nil {
		return err
	}

	if hasChanges {
		if err := gitCommand(ctx, commitDir, "add", "-A"); err != nil {
			return fmt.Errorf("git add failed: %w", err)
		}
		stderr, err := gitOutputStderr(ctx, commitDir, "commit", "-m", "WIP")
		if err != nil {
			if msg := strings.TrimSpace(stderr); msg != "" {
				return errors.New(msg)
			}
			return fmt.Errorf("git commit failed: %w", err)
		}
	}

	push, err := pushStatusForPath(ctx, commitDir, tabCtx.branchName)
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
	stderr, err := gitOutputStderr(ctx, commitDir, pushArgs...)
	if err != nil {
		if msg := strings.TrimSpace(stderr); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("git push failed: %w", err)
	}
	return nil
}

func (svc *Context) loadTabGitContext(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (*tabGitContext, error) {
	workingDir, err := svc.getTabWorkingDir(ctx, tabType, tabID)
	if err != nil {
		return nil, err
	}

	info, err := queryGitPathInfo(ctx, workingDir)
	if err != nil {
		return nil, errors.New("tab is not in a git repository")
	}

	branchName := branchOrShortSHA(ctx, workingDir, info)
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

func (svc *Context) getTabWorkingDir(ctx context.Context, tabType leapmuxv1.TabType, tabID string) (string, error) {
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

func (svc *Context) hasOtherNonWorktreeTabOnBranch(ctx context.Context, tabType leapmuxv1.TabType, tabID, repoRoot, branchName string) (bool, error) {
	// Two terminals in the same repo share a workingDir; cache so each unique
	// dir costs one rev-parse.
	infoCache := map[string]*gitPathInfo{}

	scan := func(query string, skipType leapmuxv1.TabType) (bool, error) {
		rows, err := svc.DB.QueryContext(ctx, query)
		if err != nil {
			return false, err
		}
		defer func() {
			if closeErr := rows.Close(); closeErr != nil {
				slog.Warn("failed to close row iterator", "error", closeErr)
			}
		}()
		for rows.Next() {
			var id, workingDir string
			if err := rows.Scan(&id, &workingDir); err != nil {
				return false, err
			}
			if tabType == skipType && id == tabID {
				continue
			}
			if matches, err := tabMatchesBranch(ctx, workingDir, repoRoot, branchName, infoCache); err == nil && matches {
				return true, nil
			}
		}
		return false, nil
	}

	if hit, err := scan(`SELECT id, working_dir FROM agents WHERE closed_at IS NULL`, leapmuxv1.TabType_TAB_TYPE_AGENT); err != nil || hit {
		return hit, err
	}
	return scan(`SELECT id, working_dir FROM terminals WHERE closed_at IS NULL`, leapmuxv1.TabType_TAB_TYPE_TERMINAL)
}

// tabMatchesBranch reports whether a tab's workingDir resolves to repoRoot
// and is on branchName. infoCache amortizes rev-parse cost across many tabs.
func tabMatchesBranch(ctx context.Context, workingDir, repoRoot, branchName string, infoCache map[string]*gitPathInfo) (bool, error) {
	info := infoCache[workingDir]
	if info == nil {
		fetched, err := queryGitPathInfo(ctx, workingDir)
		if err != nil {
			return false, err
		}
		info = fetched
		infoCache[workingDir] = info
	}
	if !pathutil.SamePath(info.RepoRoot, repoRoot) || info.IsWorktree {
		return false, nil
	}
	return branchOrShortSHA(ctx, workingDir, info) == branchName, nil
}

func diffStatsForPath(ctx context.Context, dir string) (added, deleted, untracked int32, hasChanges bool, err error) {
	files, err := getGitFileStatusEntries(ctx, dir)
	if err != nil {
		return 0, 0, 0, false, err
	}
	for _, file := range files {
		added += file.GetLinesAdded() + file.GetStagedLinesAdded()
		deleted += file.GetLinesDeleted() + file.GetStagedLinesDeleted()
		if file.GetStagedStatus() == leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED ||
			file.GetUnstagedStatus() == leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED {
			untracked++
		}
	}
	return added, deleted, untracked, len(files) > 0, nil
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
	if _, originErr := gitOutput(ctx, dir, "config", "--get", "remote.origin.url"); originErr == nil {
		s.OriginExists = true
	}

	if branchName == "" || branchName == "HEAD" {
		return s, nil
	}

	remoteName, remoteErr := gitOutput(ctx, dir, "config", "--get", "branch."+branchName+".remote")
	mergeRef, mergeErr := gitOutput(ctx, dir, "config", "--get", "branch."+branchName+".merge")
	if remoteErr != nil || mergeErr != nil {
		if s.OriginExists {
			if out, revErr := gitOutput(ctx, dir, "rev-list", "--count", "HEAD", "--not", "--remotes"); revErr == nil {
				var count int
				if _, scanErr := fmt.Sscanf(strings.TrimSpace(out), "%d", &count); scanErr != nil {
					return s, scanErr
				}
				s.Unpushed = int32(count)
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

	s.RemoteMissing = !remoteRefExists(ctx, dir, remoteName, remoteBranch)
	if s.RemoteMissing {
		return s, nil
	}

	upstreamRef := "refs/remotes/" + remoteName + "/" + remoteBranch
	if out, revErr := gitOutput(ctx, dir, "rev-list", "--count", upstreamRef+"..HEAD"); revErr == nil {
		var count int
		if _, scanErr := fmt.Sscanf(strings.TrimSpace(out), "%d", &count); scanErr != nil {
			return s, scanErr
		}
		s.Unpushed = int32(count)
	}
	return s, nil
}

func remoteRefExists(ctx context.Context, dir, remoteName, branchName string) bool {
	if remoteName == "" || branchName == "" {
		return false
	}
	cmd := gitutil.NewGitCmd(ctx, "-C", dir, "ls-remote", "--exit-code", "--heads", remoteName, branchName)
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// getGitFileStatusEntries computes per-file git status for the given repo root.
// It runs three git commands:
//   - git status --porcelain=v2 -z (all changed files with XY status codes)
//   - git diff --numstat -z (unstaged diff stats)
//   - git diff --numstat --staged -z (staged diff stats)
//
// Returns nil for non-git directories.
func getGitFileStatusEntries(ctx context.Context, repoRoot string) ([]*leapmuxv1.GitFileStatusEntry, error) {
	// 1. Get file status via porcelain v2.
	cmd := gitutil.NewGitCmd(ctx, "-C", repoRoot, "status", "--porcelain=v2", "-z")
	statusOut, err := cmd.Output()
	if err != nil {
		return nil, nil // Not a git repo or git unavailable.
	}

	files := parseStatusV2(statusOut)
	if len(files) == 0 {
		return nil, nil
	}

	// Build a map for easy lookup.
	fileMap := make(map[string]*leapmuxv1.GitFileStatusEntry, len(files))
	for _, f := range files {
		fileMap[f.Path] = f
	}

	// 2. Get unstaged diff stats.
	cmd2 := gitutil.NewGitCmd(ctx, "-C", repoRoot, "diff", "--numstat", "-z")
	if numstatOut, err := cmd2.Output(); err == nil {
		applyNumstat(numstatOut, fileMap, false)
	}

	// 3. Get staged diff stats.
	cmd3 := gitutil.NewGitCmd(ctx, "-C", repoRoot, "diff", "--numstat", "--staged", "-z")
	if numstatOut, err := cmd3.Output(); err == nil {
		applyNumstat(numstatOut, fileMap, true)
	}

	return files, nil
}

// gitOutput runs a git command and returns its stdout as a string.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitutil.NewGitCmd(ctx, append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// gitOutputBytes runs a git command and returns its stdout as raw bytes.
func gitOutputBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := gitutil.NewGitCmd(ctx, append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// gitOutputStderr runs a git command and returns its stderr as a string.
func gitOutputStderr(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := gitutil.NewGitCmd(ctx, append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

// gitCommand runs a git command and returns an error if it fails.
func gitCommand(ctx context.Context, dir string, args ...string) error {
	return gitutil.NewGitCmd(ctx, append([]string{"-C", dir}, args...)...).Run()
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

// parseStatusV2 parses NUL-delimited `git status --porcelain=v2 -z` output
// into proto entries.
func parseStatusV2(data []byte) []*leapmuxv1.GitFileStatusEntry {
	var files []*leapmuxv1.GitFileStatusEntry
	parts := splitNUL(data)

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
			if f := parseOrdinaryEntry(entry); f != nil {
				files = append(files, f)
			}
			i++

		case '2':
			// Renamed/copied entry: "2 XY sub mH mI mW hH hI Xscore path\0origPath"
			if f := parseRenameEntry(entry); f != nil {
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
			if f := parseUnmergedEntry(entry); f != nil {
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

func parseOrdinaryEntry(entry string) *leapmuxv1.GitFileStatusEntry {
	// Format: "1 XY sub mH mI mW hH hI path"
	if len(entry) < 4 {
		return nil
	}
	path := nthField(entry, 8)
	if path == "" {
		return nil
	}
	return &leapmuxv1.GitFileStatusEntry{
		Path:           path,
		StagedStatus:   parseGitStatusCode(entry[2]),
		UnstagedStatus: parseGitStatusCode(entry[3]),
	}
}

func parseRenameEntry(entry string) *leapmuxv1.GitFileStatusEntry {
	// Format: "2 XY sub mH mI mW hH hI Xscore path"
	if len(entry) < 4 {
		return nil
	}
	path := nthField(entry, 9)
	if path == "" {
		return nil
	}
	return &leapmuxv1.GitFileStatusEntry{
		Path:           path,
		StagedStatus:   parseGitStatusCode(entry[2]),
		UnstagedStatus: parseGitStatusCode(entry[3]),
	}
}

func parseUnmergedEntry(entry string) *leapmuxv1.GitFileStatusEntry {
	// Format: "u XY sub m1 m2 m3 hH h1 h2 h3 path"
	if len(entry) < 4 {
		return nil
	}
	path := nthField(entry, 10)
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
	parts := splitNUL(data)
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

		var addedVal, deletedVal int32
		if _, err := fmt.Sscanf(tabs[0], "%d", &addedVal); err != nil {
			addedVal = 0
		}
		if _, err := fmt.Sscanf(tabs[1], "%d", &deletedVal); err != nil {
			deletedVal = 0
		}

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
	BranchName string // abbrev-ref of HEAD; empty when detached
	IsWorktree bool
}

// queryGitPathInfo runs one `git rev-parse` and returns the resolved metadata
// for dirPath. Fails with errNotGitRepo if dirPath is not in a work tree.
func queryGitPathInfo(ctx context.Context, dirPath string) (*gitPathInfo, error) {
	output, err := gitOutput(ctx, dirPath,
		"rev-parse", "--show-toplevel", "--git-dir", "--git-common-dir", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, errNotGitRepo
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 4 {
		return nil, errNotGitRepo
	}

	topLevel := pathutil.Canonicalize(strings.TrimSpace(lines[0]))
	gitDir := strings.TrimSpace(lines[1])
	commonDir := strings.TrimSpace(lines[2])
	info := &gitPathInfo{
		TopLevel: topLevel,
		RepoRoot: topLevel,
	}
	if b := strings.TrimSpace(lines[3]); b != "HEAD" {
		info.BranchName = b
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

// branchOrShortSHA returns info.BranchName when set, otherwise the short HEAD
// SHA via a second subprocess. Handles the "detached HEAD" display case.
func branchOrShortSHA(ctx context.Context, dirPath string, info *gitPathInfo) string {
	if info.BranchName != "" {
		return info.BranchName
	}
	return shortHEADSHA(ctx, dirPath)
}

func shortHEADSHA(ctx context.Context, dirPath string) string {
	sha, err := gitOutput(ctx, dirPath, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(sha)
}

// parseGitLines splits git output by newlines, trimming whitespace and filtering empty lines.
func parseGitLines(output string) []string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var result []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			result = append(result, l)
		}
	}
	return result
}

// listGitWorktrees parses `git worktree list --porcelain` output into proto entries.
func listGitWorktrees(ctx context.Context, dirPath string) ([]*leapmuxv1.GitWorktreeEntry, error) {
	output, err := gitOutput(ctx, dirPath, "worktree", "list", "--porcelain")
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

// splitNUL splits data by NUL bytes, discarding a trailing empty element.
func splitNUL(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
