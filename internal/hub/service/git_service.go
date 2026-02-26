package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/timeout"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

// GitService implements the GitServiceHandler interface.
// It routes git info queries to the appropriate worker and manages worktree lifecycle.
type GitService struct {
	queries        *db.Queries
	workerMgr      *workermgr.Manager
	pending        *workermgr.PendingRequests
	worktreeHelper *WorktreeHelper
	timeoutCfg     *timeout.Config
}

// NewGitService creates a new GitService.
func NewGitService(q *db.Queries, bm *workermgr.Manager, pr *workermgr.PendingRequests, tc *timeout.Config) *GitService {
	return &GitService{
		queries:        q,
		workerMgr:      bm,
		pending:        pr,
		worktreeHelper: NewWorktreeHelper(q, bm, pr, tc),
		timeoutCfg:     tc,
	}
}

func (s *GitService) GetGitInfo(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetGitInfoRequest],
) (*connect.Response[leapmuxv1.GetGitInfoResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	conn, err := s.getWorkerConn(ctx, user, workerID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	resp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_GitInfo{
			GitInfo: &leapmuxv1.GitInfoRequest{
				Path: req.Msg.GetPath(),
			},
		},
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	gitResp := resp.GetGitInfoResp()
	if gitResp == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected response type"))
	}

	if gitResp.GetError() != "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("%s", gitResp.GetError()))
	}

	return connect.NewResponse(&leapmuxv1.GetGitInfoResponse{
		IsGitRepo:      gitResp.GetIsGitRepo(),
		IsWorktree:     gitResp.GetIsWorktree(),
		RepoRoot:       gitResp.GetRepoRoot(),
		RepoDirName:    gitResp.GetRepoDirName(),
		IsRepoRoot:     gitResp.GetIsRepoRoot(),
		IsWorktreeRoot: gitResp.GetIsWorktreeRoot(),
		IsDirty:        gitResp.GetIsDirty(),
		CurrentBranch:  gitResp.GetCurrentBranch(),
	}), nil
}

func (s *GitService) CheckWorktreeStatus(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CheckWorktreeStatusRequest],
) (*connect.Response[leapmuxv1.CheckWorktreeStatusResponse], error) {
	_, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	tabType := req.Msg.GetTabType()
	tabID := req.Msg.GetTabId()
	if tabID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("tab_id is required"))
	}

	slog.Info("checking worktree status for tab", "tab_type", tabType, "tab_id", tabID)
	result := s.worktreeHelper.CheckTabWorktreeStatus(ctx, tabType, tabID)
	slog.Info("worktree status check complete",
		"tab_id", tabID, "has_worktree", result.HasWorktree,
		"is_last_tab", result.IsLastTab, "is_dirty", result.IsDirty,
		"worktree_path", result.WorktreePath, "branch_name", result.BranchName)

	return connect.NewResponse(&leapmuxv1.CheckWorktreeStatusResponse{
		HasWorktree:  result.HasWorktree,
		IsLastTab:    result.IsLastTab,
		IsDirty:      result.IsDirty,
		WorktreePath: result.WorktreePath,
		WorktreeId:   result.WorktreeID,
		BranchName:   result.BranchName,
	}), nil
}

func (s *GitService) ForceRemoveWorktree(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ForceRemoveWorktreeRequest],
) (*connect.Response[leapmuxv1.ForceRemoveWorktreeResponse], error) {
	_, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	worktreeID := req.Msg.GetWorktreeId()
	if worktreeID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	slog.Info("force-removing worktree", "worktree_id", worktreeID)
	wt, err := s.queries.GetWorktreeByID(ctx, worktreeID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worktree not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	slog.Info("force-removing worktree: deleting DB record",
		"worktree_id", worktreeID, "worktree_path", wt.WorktreePath,
		"branch_name", wt.BranchName, "worker_id", wt.WorkerID)

	// Delete DB record first — this is the authoritative state.
	if err := s.queries.DeleteWorktree(ctx, worktreeID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete worktree record: %w", err))
	}

	// Fire-and-forget: send removal request to worker in background.
	// Worktree deletion is best-effort; the caller shouldn't wait for it.
	conn := s.workerMgr.Get(wt.WorkerID)
	if conn != nil {
		slog.Info("force-removing worktree: sending removal to worker",
			"worktree_id", worktreeID, "worker_id", wt.WorkerID)
		go func() {
			if err := conn.Send(&leapmuxv1.ConnectResponse{
				Payload: &leapmuxv1.ConnectResponse_GitWorktreeRemove{
					GitWorktreeRemove: &leapmuxv1.GitWorktreeRemoveRequest{
						WorktreePath: wt.WorktreePath,
						Force:        true,
						BranchName:   wt.BranchName,
					},
				},
			}); err != nil {
				slog.Warn("failed to send worktree remove to worker",
					"worktree_id", worktreeID, "worker_id", wt.WorkerID, "error", err)
			}
		}()
	}

	return connect.NewResponse(&leapmuxv1.ForceRemoveWorktreeResponse{}), nil
}

func (s *GitService) KeepWorktree(
	ctx context.Context,
	req *connect.Request[leapmuxv1.KeepWorktreeRequest],
) (*connect.Response[leapmuxv1.KeepWorktreeResponse], error) {
	_, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	worktreeID := req.Msg.GetWorktreeId()
	if worktreeID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worktree_id is required"))
	}

	// Just delete the DB record — leave the worktree on disk.
	slog.Info("keeping worktree: deleting DB record only", "worktree_id", worktreeID)
	if err := s.queries.DeleteWorktree(ctx, worktreeID); err != nil {
		if err == sql.ErrNoRows {
			// Already gone, that's fine.
			return connect.NewResponse(&leapmuxv1.KeepWorktreeResponse{}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&leapmuxv1.KeepWorktreeResponse{}), nil
}

func (s *GitService) getWorkerConn(ctx context.Context, user *auth.UserInfo, workerID, requestedOrgID string) (*workermgr.Conn, error) {
	if workerID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}

	worker, err := s.queries.GetWorkerByIDInternal(ctx, workerID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	_, err = s.queries.GetOwnedWorker(ctx, db.GetOwnedWorkerParams{
		UserID:   user.ID,
		WorkerID: worker.ID,
		OrgID:    worker.OrgID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is offline"))
	}

	return conn, nil
}
