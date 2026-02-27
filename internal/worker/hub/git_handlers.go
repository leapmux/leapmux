package hub

import (
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

func (c *Client) handleGitInfo(requestID string, req *leapmuxv1.GitInfoRequest) {
	info, err := gitutil.GetGitInfo(req.GetPath())
	resp := &leapmuxv1.GitInfoResponse{Path: req.GetPath()}

	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.IsGitRepo = info.IsGitRepo
		resp.IsWorktree = info.IsWorktree
		resp.RepoRoot = info.RepoRoot
		resp.RepoDirName = info.RepoDirName
		resp.IsRepoRoot = info.IsRepoRoot
		resp.IsWorktreeRoot = info.IsWorktreeRoot

		// Populate dirty status and current branch for repo/worktree roots.
		if info.IsRepoRoot || info.IsWorktreeRoot {
			if status := gitutil.GetGitStatus(req.GetPath()); status != nil {
				resp.CurrentBranch = status.Branch
				resp.IsDirty = status.Modified || status.Added || status.Deleted ||
					status.Renamed || status.Untracked || status.TypeChanged || status.Conflicted
			}
		}
	}

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_GitInfoResp{
			GitInfoResp: resp,
		},
	})
}

func (c *Client) handleGitWorktreeCreate(requestID string, req *leapmuxv1.GitWorktreeCreateRequest) {
	slog.Info("creating worktree",
		"repo_root", req.GetRepoRoot(), "worktree_path", req.GetWorktreePath(),
		"branch_name", req.GetBranchName(), "start_point", req.GetStartPoint())

	err := gitutil.CreateWorktree(req.GetRepoRoot(), req.GetWorktreePath(), req.GetBranchName(), req.GetStartPoint())
	resp := &leapmuxv1.GitWorktreeCreateResponse{WorktreePath: req.GetWorktreePath()}

	if err != nil {
		slog.Warn("worktree creation failed", "worktree_path", req.GetWorktreePath(), "error", err)
		resp.Error = err.Error()
	} else {
		slog.Info("worktree created successfully", "worktree_path", req.GetWorktreePath())
	}

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_GitWorktreeCreateResp{
			GitWorktreeCreateResp: resp,
		},
	})
}

func (c *Client) handleGitWorktreeRemove(requestID string, req *leapmuxv1.GitWorktreeRemoveRequest) {
	slog.Info("handling worktree remove request",
		"worktree_path", req.GetWorktreePath(), "check_only", req.GetCheckOnly(),
		"force", req.GetForce(), "branch_name", req.GetBranchName())

	resp := &leapmuxv1.GitWorktreeRemoveResponse{WorktreePath: req.GetWorktreePath()}

	if req.GetCheckOnly() {
		// Only check cleanliness, don't remove.
		clean, err := gitutil.IsWorktreeClean(req.GetWorktreePath())
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.IsClean = clean
		}
	} else {
		// Check cleanliness first.
		clean, err := gitutil.IsWorktreeClean(req.GetWorktreePath())
		if err != nil {
			resp.Error = err.Error()
		} else if !clean && !req.GetForce() {
			// Dirty and not forced: report status without removing.
			resp.IsClean = false
		} else {
			// Clean or forced: get repo root and remove.
			info, err := gitutil.GetGitInfo(req.GetWorktreePath())
			if err != nil {
				resp.Error = err.Error()
			} else if !info.IsGitRepo {
				resp.Error = "not a git repository"
			} else {
				// Respond immediately so the Hub doesn't time out, then
				// perform the actual removal asynchronously.
				resp.IsClean = clean
				_ = c.Send(&leapmuxv1.ConnectRequest{
					RequestId: requestID,
					Payload: &leapmuxv1.ConnectRequest_GitWorktreeRemoveResp{
						GitWorktreeRemoveResp: resp,
					},
				})

				go func() {
					slog.Info("removing worktree",
						"repo_root", info.RepoRoot, "worktree_path", req.GetWorktreePath())
					if err := gitutil.RemoveWorktree(info.RepoRoot, req.GetWorktreePath()); err != nil {
						slog.Warn("worktree removal failed",
							"worktree_path", req.GetWorktreePath(), "error", err)
						return
					}
					slog.Info("worktree removed successfully",
						"worktree_path", req.GetWorktreePath())
					// Best-effort: delete the branch if it's no longer in use.
					branchName := req.GetBranchName()
					if branchName != "" {
						inUse, err := gitutil.IsBranchInUse(info.RepoRoot, branchName)
						if err != nil {
							slog.Warn("failed to check if branch is in use", "branch", branchName, "error", err)
						} else if !inUse {
							slog.Info("deleting branch after worktree removal",
								"branch", branchName, "repo_root", info.RepoRoot)
							if err := gitutil.DeleteBranch(info.RepoRoot, branchName); err != nil {
								slog.Warn("failed to delete branch after worktree removal", "branch", branchName, "error", err)
							} else {
								slog.Info("branch deleted successfully", "branch", branchName)
							}
						} else {
							slog.Info("branch still in use, not deleting", "branch", branchName)
						}
					}
				}()
				return
			}
		}
	}

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_GitWorktreeRemoveResp{
			GitWorktreeRemoveResp: resp,
		},
	})
}
