package service

import (
	"context"
	"database/sql"
	"fmt"

	"connectrpc.com/connect"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/validate"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
)

// FileService implements the FileServiceHandler interface.
// It routes file operations to the appropriate worker via the bidi stream.
type FileService struct {
	queries   *db.Queries
	workerMgr *workermgr.Manager
	pending   *workermgr.PendingRequests
}

// NewFileService creates a new FileService.
func NewFileService(q *db.Queries, bm *workermgr.Manager, pr *workermgr.PendingRequests) *FileService {
	return &FileService{queries: q, workerMgr: bm, pending: pr}
}

func (s *FileService) ListDirectory(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ListDirectoryRequest],
) (*connect.Response[leapmuxv1.ListDirectoryResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	conn, workerHomeDir, err := s.getWorkerConn(ctx, user, workerID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	browsePath := validate.SanitizePath(req.Msg.GetPath(), workerHomeDir)
	if browsePath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid path"))
	}

	resp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_FileBrowse{
			FileBrowse: &leapmuxv1.FileBrowseRequest{
				Path:     browsePath,
				MaxDepth: req.Msg.GetMaxDepth(),
			},
		},
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	browseResp := resp.GetFileBrowseResp()
	if browseResp == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected response type"))
	}

	if browseResp.GetError() != "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("%s", browseResp.GetError()))
	}

	entries := make([]*leapmuxv1.FileInfo, len(browseResp.GetEntries()))
	for i, e := range browseResp.GetEntries() {
		entries[i] = &leapmuxv1.FileInfo{
			Name:        e.GetName(),
			Path:        browseResp.GetPath() + "/" + e.GetName(),
			IsDir:       e.GetIsDir(),
			Size:        e.GetSize(),
			ModTime:     e.GetModTime(),
			Permissions: e.GetPermissions(),
		}
	}

	return connect.NewResponse(&leapmuxv1.ListDirectoryResponse{
		Path:    browseResp.GetPath(),
		Entries: entries,
	}), nil
}

func (s *FileService) ReadFile(
	ctx context.Context,
	req *connect.Request[leapmuxv1.ReadFileRequest],
) (*connect.Response[leapmuxv1.ReadFileResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	conn, workerHomeDir, err := s.getWorkerConn(ctx, user, workerID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	readPath := validate.SanitizePath(req.Msg.GetPath(), workerHomeDir)
	if readPath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid path"))
	}

	resp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_FileRead{
			FileRead: &leapmuxv1.FileReadRequest{
				Path:   readPath,
				Offset: req.Msg.GetOffset(),
				Limit:  req.Msg.GetLimit(),
			},
		},
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	readResp := resp.GetFileReadResp()
	if readResp == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected response type"))
	}

	if readResp.GetError() != "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("%s", readResp.GetError()))
	}

	return connect.NewResponse(&leapmuxv1.ReadFileResponse{
		Path:      readResp.GetPath(),
		Content:   readResp.GetContent(),
		TotalSize: readResp.GetTotalSize(),
	}), nil
}

func (s *FileService) StatFile(
	ctx context.Context,
	req *connect.Request[leapmuxv1.StatFileRequest],
) (*connect.Response[leapmuxv1.StatFileResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}

	workerID := req.Msg.GetWorkerId()
	conn, workerHomeDir, err := s.getWorkerConn(ctx, user, workerID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	statPath := validate.SanitizePath(req.Msg.GetPath(), workerHomeDir)
	if statPath == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid path"))
	}

	resp, err := s.pending.SendAndWait(ctx, conn, &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_FileStat{
			FileStat: &leapmuxv1.FileStatRequest{
				Path: statPath,
			},
		},
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	statResp := resp.GetFileStatResp()
	if statResp == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unexpected response type"))
	}

	if statResp.GetError() != "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("%s", statResp.GetError()))
	}

	entry := statResp.GetEntry()
	var info *leapmuxv1.FileInfo
	if entry != nil {
		info = &leapmuxv1.FileInfo{
			Name:        entry.GetName(),
			Path:        statResp.GetPath(),
			IsDir:       entry.GetIsDir(),
			Size:        entry.GetSize(),
			ModTime:     entry.GetModTime(),
			Permissions: entry.GetPermissions(),
		}
	}

	return connect.NewResponse(&leapmuxv1.StatFileResponse{
		Info: info,
	}), nil
}

func (s *FileService) getWorkerConn(ctx context.Context, user *auth.UserInfo, workerID, requestedOrgID string) (*workermgr.Conn, string, error) {
	if workerID == "" {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("worker_id is required"))
	}

	// Verify the worker exists and the user can see it.
	worker, err := s.queries.GetWorkerByIDInternal(ctx, workerID)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}

	_, err = s.queries.GetOwnedWorker(ctx, db.GetOwnedWorkerParams{
		UserID:   user.ID,
		WorkerID: worker.ID,
		OrgID:    worker.OrgID,
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("worker not found"))
		}
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}

	conn := s.workerMgr.Get(workerID)
	if conn == nil {
		return nil, "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("worker is offline"))
	}

	return conn, worker.HomeDir, nil
}
