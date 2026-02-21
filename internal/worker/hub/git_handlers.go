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
	}

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_GitInfoResp{
			GitInfoResp: resp,
		},
	})
}

func (c *Client) handleGitWorktreeCreate(requestID string, req *leapmuxv1.GitWorktreeCreateRequest) {
	err := gitutil.CreateWorktree(req.GetRepoRoot(), req.GetWorktreePath(), req.GetBranchName())
	resp := &leapmuxv1.GitWorktreeCreateResponse{WorktreePath: req.GetWorktreePath()}

	if err != nil {
		resp.Error = err.Error()
	}

	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_GitWorktreeCreateResp{
			GitWorktreeCreateResp: resp,
		},
	})
}

func (c *Client) handleGitWorktreeRemove(requestID string, req *leapmuxv1.GitWorktreeRemoveRequest) {
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
				if err := gitutil.RemoveWorktree(info.RepoRoot, req.GetWorktreePath()); err != nil {
					resp.Error = err.Error()
				} else {
					resp.IsClean = clean
					// Best-effort: delete the branch if it's no longer in use.
					branchName := req.GetBranchName()
					if branchName != "" {
						inUse, err := gitutil.IsBranchInUse(info.RepoRoot, branchName)
						if err != nil {
							slog.Warn("failed to check if branch is in use", "branch", branchName, "error", err)
						} else if !inUse {
							if err := gitutil.DeleteBranch(info.RepoRoot, branchName); err != nil {
								slog.Warn("failed to delete branch after worktree removal", "branch", branchName, "error", err)
							}
						}
					}
				}
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
